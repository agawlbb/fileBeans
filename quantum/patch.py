#!/usr/bin/env python3
"""Version-specific, checked transformations for the pinned Quantum source."""
import pathlib
import sys

root = pathlib.Path(sys.argv[1]) / "backend"
BRIDGE = '"github.com/gtsteffaniak/filebrowser/backend/pkg/vaultbridge"'


def edit(name, old, new, count=1, bridge=True):
    path = root / name
    text = path.read_text()
    found = text.count(old)
    if found != count:
        raise SystemExit(f"Pinned Quantum anchor mismatch: {name}: expected {count}, got {found}: {old[:100]}")
    text = text.replace(old, new)
    if bridge and BRIDGE not in text:
        text = text.replace("import (", "import (\n\t" + BRIDGE, 1)
    path.write_text(text)


# The entire HTTP operation, including streaming and synchronous timeout helpers,
# retains a lease. WebDAV enters through this same handler.
edit("internal/web/server.go", "Handler: muxWithMiddleware(router),", "Handler: vaultbridge.HTTP(muxWithMiddleware(router)),")
edit("internal/web/server.go", "\tinitRuntime(deps, fs)", "\tif err := vaultbridge.ValidateCreationModes(fileutils.PermFile,fileutils.PermDir); err != nil { panic(\"Protected-folder consumer permissions failed\") }\n\tif err := vaultbridge.Start(ctx); err != nil { panic(\"Protected-folder consumer setup failed\") }\n\tinitRuntime(deps, fs)")

# No filename-bearing application/access logs reach persistent ordinary storage.
edit("pkg/settings/config.go", "func setupLogging() {", '''func setupLogging() {
    if vaultbridge.Enabled() {
        Config.Server.Logging = []LogConfig{{Output: "/dev/null", Levels: "info|warning|error", ApiLevels: "error", ApiFilter: ".*"}}
    }''')

# Check access before the real-path cache, and retain a denied sentinel even for
# old upstream call sites that ignored the error return.
edit("pkg/indexing/indexingFiles.go", '\tcacheKey := fmt.Sprintf("realpath|scoped=', '''
    vaultLease, vaultErr := vaultbridge.Read(joinedPath, false)
    if vaultErr != nil { return "/proc/filebeans-protected-denied", false, vaultErr }
    defer vaultLease.Close()
\tcacheKey := fmt.Sprintf("realpath|scoped=''')
edit("pkg/indexing/indexingFiles.go", "\t// 1. Resolve path context (single stat call)", '''
    vaultLease, vaultErr := vaultbridge.Read(utils.JoinUnderSourceRoot(idx.Path, req.IndexPath), false)
    if vaultErr != nil { return nil, vaultErr }
    defer vaultLease.Close()
\t// 1. Resolve path context (single stat call)''')
edit("pkg/indexing/indexingFiles.go", "func (idx *Index) IsViewable(isDir bool, indexPath string, isSymlink bool, isHidden bool) bool {", '''func (idx *Index) IsViewable(isDir bool, indexPath string, isSymlink bool, isHidden bool) bool {
    if !vaultbridge.Viewable(utils.JoinUnderSourceRoot(idx.Path, indexPath), isDir) { return false }''')
edit("pkg/indexing/indexingFiles.go", "func (idx *Index) ShouldSkip(isDir bool, adjustedPath string, isHidden bool, isSymlink bool, isRoutineScan bool) bool {", '''func (idx *Index) ShouldSkip(isDir bool, adjustedPath string, isHidden bool, isSymlink bool, isRoutineScan bool) bool {
    if !vaultbridge.Indexable(utils.JoinUnderSourceRoot(idx.Path, adjustedPath)) { return true }''')
edit("pkg/indexing/indexingFiles.go", "\t// Open the directory\n\tdir, err := os.Open(realPath)", "\tif !vaultbridge.Indexable(realPath) { return 0, false, errors.ErrNotIndexed }\n\t// Open the directory\n\tdir, err := os.Open(realPath)")
edit("pkg/indexing/indexingFiles.go", "\t\titemInfo.HasPreview = false\n\t}\n\n\treturn itemInfo, itemInfo.Size, shouldCountSize, bubblesUpHasPreview", '''
        itemInfo.HasPreview = false
        if vaultbridge.Protected(fullCombined) {
            setFilePreviewFlags(itemInfo,fullCombined)
            bubblesUpHasPreview = iteminfo.ShouldBubbleUpToFolderPreview(*itemInfo)
        }
    }

\treturn itemInfo, itemInfo.Size, shouldCountSize, bubblesUpHasPreview''')

# A final persistence guard covers API refreshes as well as scanner batches.
edit("internal/database/sql/index.go", "func (db *IndexDB) BulkInsertItems(source string, items []*iteminfo.FileInfo) error {", '''func (db *IndexDB) BulkInsertItems(source string, items []*iteminfo.FileInfo) error {
    if vaultbridge.Enabled() {
        sourceRoot := ""
        for _, src := range settings.Config.Server.SourceMap { if src.Name == source || src.Path == source { sourceRoot = src.Path; break } }
        if sourceRoot == "" { return fmt.Errorf("unknown protected index source") }
        filtered := make([]*iteminfo.FileInfo, 0, len(items))
        for _, item := range items {
            if vaultbridge.Indexable(filepath.Join(sourceRoot, item.Path)) { filtered = append(filtered, item) }
        }
        items = filtered
    }''')

# Lower-level mutations also cover Web/API callers and avoid a recursive ordinary
# copy or move crossing a vault boundary. Explicit exports use the companion.
for signature, expression in [
    ("func DeleteFiles(source, absPath string, isDir bool) error {", 'absPath, ""'),
    ("func moveResourceImpl(isSrcDir bool, sourceIndex, destIndex, realsrc, realdst string, s *Service) error {", "realsrc, realdst"),
    ("func CopyResource(isSrcDir bool, sourceIndex, destIndex, realsrc, realdst string) error {", "realsrc, realdst"),
]:
    edit("internal/adapters/fs/files/files.go", signature, signature + f'''
    vaultLease, vaultErr := vaultbridge.Mutation({expression})
    if vaultErr != nil {{ return vaultErr }}
    defer vaultLease.Close()
''')

# Filesystem semantic activity contains paths in free-form errors/changes too.
# Do not put those entries in the ordinary audit database in protected mode.
edit("internal/activity/recorder.go", "func Record(entry activitydb.Entry) {", '''func Record(entry activitydb.Entry) {
    if vaultbridge.Enabled() && (entry.Source != "" || entry.Path != "" || entry.TargetPath != "" || entry.Details.Source != "" || entry.Details.Path != "" || len(entry.Details.Paths) > 0) { return }''')

# Shares may name the public root but cannot persist protected child names.
for signature, obj in [("func (c *crudBackend) Save(obj *Share) error {", "obj"), ("func (s *Storage) UpdateShare(l *Share) error {", "l")]:
    edit("internal/database/share/storage.go", signature, signature + f'''
    if !vaultbridge.MetadataAllowed(filepath.Join({obj}.SourcePath, {obj}.Path)) {{ return fmt.Errorf("protected child sharing is unavailable") }}
''')
path = root / "internal/database/share/storage.go"
path.write_text(path.read_text().replace('"fmt"', '"fmt"\n"path/filepath"', 1))

# Per-vault FFmpeg services share the global process semaphore but use encrypted
# temporary storage. Global FFmpeg configuration is never changed per request.
edit("internal/ffmpeg/service.go", "// Enabled reports whether ffmpeg initialized successfully.", '''func (s *Service) WithCacheDir(path string) *Service {
    if s == nil { return nil }
    return &Service{inner:s.inner, cacheDir:path}
}

// Enabled reports whether ffmpeg initialized successfully.''', bridge=False)

# Select protected storage before both cache lookup and generation. A separate
# receiver avoids swapping a process-global service during concurrent requests.
edit("internal/preview/preview.go", "docGenMutex   sync.Mutex", "docGenMutex   *sync.Mutex", bridge=False)
edit("internal/preview/preview.go", "\treturn &Service{\n\t\tfileCache:", "\treturn &Service{\n\t\tdocGenMutex: &sync.Mutex{},\n\t\tfileCache:", bridge=False)
preview = root / "internal/preview/preview.go"
text = preview.read_text()
a = text.index("func GetPreviewForFile(")
b = text.index("// filePreviewType", a)
part = text[a:b]
anchor = "\tif !file.HasPreview {"
assert part.count(anchor) == 1
part = part.replace(anchor, '''
    selected, lease, vaultErr := previewServiceForFile(file)
    if vaultErr != nil { return nil, vaultErr }
    defer lease.Close()
\tif !file.HasPreview {''', 1)
part = part.replace("service.fileCache", "selected.fileCache").replace("return GeneratePreviewWithMD5(", "return selected.generateWithMD5(")
text = text[:a] + part + text[b:]
a = text.index("func GeneratePreviewWithMD5(")
b = text.index("func GeneratePreview(", a)
part = text[a:b].replace("func GeneratePreviewWithMD5(", "func (s *Service) generateWithMD5(", 1).replace("service.", "s.")
text = text[:a] + part + text[b:]
a = text.index("func DelThumbs(")
part = text[a:].replace("\t// Generate metadata-based cache hash", '''
    selected, lease, vaultErr := previewServiceForFile(file)
    if vaultErr != nil { return }
    defer lease.Close()
\t// Generate metadata-based cache hash''', 1).replace("service.fileCache", "selected.fileCache")
preview.write_text(text[:a] + part)

# Keep source permissions intact, but reject filename-bearing pins and deep
# scopes rather than placing those names in the ordinary user database.
for signature, obj in [
    ("func (c *crudBackend) Save(obj *User) error {", "obj"),
    ("func (s *Storage) Update(user *User, adminIActor bool, fields ...string) error {", "user"),
    ("func (s *Storage) Save(user *User, changePass, disableScopeChange bool) error {", "user"),
]:
    edit("internal/database/users/storage.go", signature, signature + f"\n if err := validateVaultUserMetadata({obj}); err != nil {{ return err }}")
path = root / "internal/database/users/storage.go"
text = path.read_text().replace('"fmt"', '"fmt"\n"path/filepath"', 1)
text += '''
func validateVaultUserMetadata(user *User) error {
    if !vaultbridge.Enabled() { return nil }
    for _, scope := range user.BackendScopes {
        if !vaultbridge.MetadataAllowed(filepath.Join(scope.Path,scope.Scope)) { return fmt.Errorf("protected child scope is unavailable") }
    }
    for source, dirs := range user.PinnedItems {
        for dir, names := range dirs { for _, name := range names {
            if !vaultbridge.MetadataAllowed(filepath.Join(source,dir,name)) { return fmt.Errorf("protected child pinning is unavailable") }
        } }
    }
    return nil
}
'''
path.write_text(text)

# Access-rule paths are also filename-bearing metadata. Existing source/root
# rules continue to apply; protected child rules remain memory-only.
edit("internal/database/access/access.go", "func (s *Storage) persistRuleSQLNL(sourcePath, normalizedPath string) {", '''func (s *Storage) persistRuleSQLNL(sourcePath, normalizedPath string) {
    if !vaultbridge.MetadataAllowed(filepath.Join(sourcePath,normalizedPath)) { return }''')
edit("internal/web/access.go", '\tif body.RuleCategory == "user" {', '''
    if !vaultbridge.MetadataAllowed(filepath.Join(index.Path,parsedPath.String())) { return http.StatusForbidden,fmt.Errorf("protected child access rules are unavailable") }
\tif body.RuleCategory == "user" {''')
edit("internal/web/access.go", "\terr = state.UpdateRulePath(index.Path, oldPath, newPath)", '''
    if !vaultbridge.MetadataAllowed(filepath.Join(index.Path,oldPath.String())) || !vaultbridge.MetadataAllowed(filepath.Join(index.Path,newPath.String())) { return http.StatusForbidden,fmt.Errorf("protected child access rules are unavailable") }
\terr = state.UpdateRulePath(index.Path, oldPath, newPath)''')
path = root / "internal/web/access.go"
if '"path/filepath"' not in path.read_text():
    path.write_text(path.read_text().replace("import (", 'import (\n"path/filepath"', 1))

# WebDAV's per-request metadata cache must check the path before serving a hit.
edit("internal/web/webdav.go", "func (ffs *filteredFileSystem) getFileInfo(requestPath string, expand bool) (*iteminfo.ExtendedFileInfo, error) {", '''func (ffs *filteredFileSystem) getFileInfo(requestPath string, expand bool) (*iteminfo.ExtendedFileInfo, error) {
    if vaultbridge.Enabled() {
        indexPath, _, err := files.CheckPermissions(utils.FileOptions{Path:requestPath,Source:ffs.source,FollowSymlinks:true,ShowHidden:true}, ffs.user)
        if err != nil { return nil,err }
        idx := indexing.GetIndex(ffs.source)
        if idx == nil { return nil,fmt.Errorf("source unavailable") }
        lease, err := vaultbridge.Read(utils.JoinUnderSourceRoot(idx.Path,indexPath),false)
        if err != nil { return nil,err }; defer lease.Close()
    }''')

# Build-and-stream archives may span several explicit selections. An ordinary
# ancestor containing a vault is rejected; a protected selection uses its private
# encrypted spool, including subsequent archiveToken range requests.
edit("internal/web/archive.go", "\talgo := r.URL.Query().Get(\"algo\")\n\tvar extension string", '''
    protectedSpool := ""
    if vaultbridge.Enabled() {
        for _, selected := range fileList {
            path,_,err := idx.GetRealPath(selected)
            if err != nil { return http.StatusConflict,err }
            lease,err := vaultbridge.Read(path,true)
            if err != nil { return http.StatusConflict,err }
            private,err := lease.CacheDirectory(path)
            lease.Close()
            if err != nil { return http.StatusConflict,err }
            if private != "" {
                next := filepath.Join(private,"tmp","archives")
                if protectedSpool != "" && protectedSpool != next { return http.StatusConflict,fmt.Errorf("archives cannot span protected folders") }
                protectedSpool = next
            }
        }
        if protectedSpool != "" { if err := os.MkdirAll(protectedSpool,0700); err != nil { return http.StatusConflict,err } }
    }
\talgo := r.URL.Query().Get("algo")
\tvar extension string''')
edit("internal/web/archive.go", "\tdlDir := settings.DownloadCacheDir()", "\tdlDir := settings.DownloadCacheDir()\n if protectedSpool != \"\" { dlDir = protectedSpool }")

# Bind range/resume tokens to the original selection and principal. A token
# cannot be replayed with an ordinary path to bypass a protected lock check.
edit("internal/web/archive.go", "var archiveSpoolPathCache = cache.NewCache[string](archiveMultiRequestIdle)", "var archiveSpoolPathCache = cache.NewCache[string](archiveMultiRequestIdle)\nvar archiveVaultBindingCache = cache.NewCache[string](archiveMultiRequestIdle)")
edit("internal/web/archive.go", '\ttoken := r.URL.Query().Get("archiveToken")', '''
    vaultBindingBytes,_ := json.Marshal([]any{source,fileList,d.User.ID,d.Share.Hash})
    vaultBinding := string(vaultBindingBytes)
\ttoken := r.URL.Query().Get("archiveToken")''')
edit("internal/web/archive.go", "\t\ttmpPath, ok := archiveSpoolPathCache.Get(token)", '''
        if vaultbridge.Enabled() {
            original,ok := archiveVaultBindingCache.Get(token)
            if !ok || original != vaultBinding { return http.StatusGone,fmt.Errorf("invalid archive selection") }
            archiveVaultBindingCache.SetWithExp(token,original,archiveMultiRequestIdle)
        }
\t\ttmpPath, ok := archiveSpoolPathCache.Get(token)''')
edit("internal/web/archive.go", "\tarchiveSpoolPathCache.SetWithExp(newTok, tmpPath, archiveMultiRequestIdle)", "\tarchiveSpoolPathCache.SetWithExp(newTok, tmpPath, archiveMultiRequestIdle)\n if vaultbridge.Enabled() { archiveVaultBindingCache.SetWithExp(newTok,vaultBinding,archiveMultiRequestIdle) }")
edit("internal/web/archive.go", "\tarchiveSpoolPathCache.Delete(token)\n\tif err := os.Remove(tmpPath)", "\tarchiveSpoolPathCache.Delete(token)\n archiveVaultBindingCache.Delete(token)\n\tif err := os.Remove(tmpPath)")

# Explicit server-side archive/extract destinations obey mutation boundaries,
# before opening any output. Exports to ordinary storage use the companion.
edit("internal/web/archive.go", "\tvar createErr error\n\tfile, err := os.OpenFile(destFileReal", '''
    for _, selected := range itemPaths {
        from,_,err := idx.GetRealPath(selected)
        if err != nil { return http.StatusConflict,err }
        lease,err := vaultbridge.Mutation(from,destFileReal)
        if err != nil { return http.StatusConflict,err }; defer lease.Close()
    }
\tvar createErr error
\tfile, err := os.OpenFile(destFileReal''')
edit("internal/web/archive.go", "\tdestInfo, err := os.Stat(destReal)", '''
    vaultLease,vaultErr := vaultbridge.Mutation(archiveReal,destReal)
    if vaultErr != nil { return http.StatusConflict,vaultErr }; defer vaultLease.Close()
\tdestInfo, err := os.Stat(destReal)''')

edit("internal/web/archive.go", "\trealPath, _, _ := idx.GetRealPath(path)\n\tinfo, err := os.Stat(realPath)", '''
    realPath, _, _ := idx.GetRealPath(path)
    lease, vaultErr := vaultbridge.Read(realPath,true)
    if vaultErr != nil { return vaultErr }; defer lease.Close()
\tinfo, err := os.Stat(realPath)''')
edit("internal/web/archive.go", "\t\treturn filepath.Walk(realPath, func(filePath string, fileInfo os.FileInfo, err error) error {", '''
\t\treturn filepath.Walk(realPath, func(filePath string, fileInfo os.FileInfo, err error) error {
        if vaultbridge.Hidden(filePath) { if fileInfo != nil && fileInfo.IsDir() { return filepath.SkipDir }; return nil }''')

# WebDAV's raw filesystem calls must not bypass a cached listing or an ignored
# upstream error. Scope and source checks still run in the original handlers.
for anchor, args in [
    ("\tif err := ffs.fs.Mkdir(ctx, name, perm); err != nil {", 'name, ""'),
    ("\tif err := ffs.fs.RemoveAll(ctx, requestPath); err != nil {", 'requestPath, ""'),
    ("\tif err := ffs.fs.Rename(ctx, oldPath, newPath); err != nil {", 'oldPath, newPath'),
]:
    edit("internal/web/webdav.go", anchor, f'''    vaultLease, vaultErr := ffs.vaultMutation({args})
    if vaultErr != nil {{ return vaultErr }}; defer vaultLease.Close()
'''+anchor)
edit("internal/web/webdav.go", "\tfile, err := ffs.fs.OpenFile(ctx, requestPath, flag, perm)", '''
    vaultLease, vaultErr := vaultbridge.Read(ffs.vaultPath(requestPath),false)
    if vaultErr != nil { return nil,vaultErr }; defer vaultLease.Close()
    if isWrite {
        mutationLease, mutationErr := ffs.vaultMutation(requestPath, "")
        if mutationErr != nil { return nil,mutationErr }; defer mutationLease.Close()
    }
\tfile, err := ffs.fs.OpenFile(ctx, requestPath, flag, perm)''')
edit("internal/web/webdav.go", '\tprefix := webDavPrefix + "/" + source', '''\tprefix := webDavPrefix + "/" + source
    if vaultbridge.Enabled() && (r.Method == "COPY" || r.Method == "MOVE") {
        destination,ok := resolveDestination(r,prefix,scopePath)
        if !ok { return http.StatusBadRequest,fmt.Errorf("invalid destination") }
        lease,err := vaultbridge.Mutation(filepath.Join(scopePath,requestPath),destination)
        if err != nil { return http.StatusConflict,err }; defer lease.Close()
    }''')
path = root / "internal/web/webdav.go"
text = path.read_text().replace("import (", 'import (\n"filebeans/companion/protection"', 1)
text += '''
func (ffs *filteredFileSystem) vaultPath(name string) string {
    dir,ok := ffs.fs.(webdav.Dir)
    if !ok { return "/proc/filebeans-protected-denied" }
    return filepath.Join(string(dir),strings.TrimPrefix(path.Clean("/"+name),"/"))
}
func (ffs *filteredFileSystem) vaultMutation(from,to string)(*protection.Lease,error) {
    destination := ""
    if to != "" { destination = ffs.vaultPath(to) }
    return vaultbridge.Mutation(ffs.vaultPath(from),destination)
}
'''
path.write_text(text)
