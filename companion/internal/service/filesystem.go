package service

// The filesystem API deliberately shares the companion's confined roots, jobs,
// trash policy and broker leases. It has no persistent filename index.
import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

const thumbnailDirectory = ".filebeans-thumbnails"

type FilesystemConfig struct {
	DatasetMinFreeBytes uint64 `json:"datasetMinFreeBytes,omitempty"`
	Enabled             bool   `json:"enabled"`
	FFmpeg              string `json:"ffmpeg,omitempty"`
	PDFToPPM            string `json:"pdftoppm,omitempty"`
}

type filesystemEntry struct {
	ArchiveLink string            `json:"archiveLink,omitempty"`
	ArchiveRole string            `json:"archiveRole,omitempty"`
	ArchiveRoot bool              `json:"archiveRoot,omitempty"`
	Vault       string            `json:"vault,omitempty"`
	Name        string            `json:"name"`
	Path        string            `json:"path"`
	Type        string            `json:"type"`
	Size        int64             `json:"size"`
	Modified    string            `json:"modified"`
	Revision    string            `json:"revision"`
	HasPreview  bool              `json:"hasPreview"`
	Children    []filesystemEntry `json:"children,omitempty"`
}

func filesystemHidden(p string) bool {
	for _, part := range strings.Split(filepath.ToSlash(p), "/") {
		if strings.EqualFold(part, thumbnailDirectory) || strings.EqualFold(part, brokerStore) || strings.EqualFold(part, ".filebeans-stage") {
			return true
		}
	}
	return false
}

func filesystemPath(p string) (string, error) {
	if p == "" {
		return "", ErrUnsafe
	}
	p, e := clean(p)
	if e != nil || filesystemHidden(p) {
		return "", ErrUnsafe
	}
	return p, nil
}

func filesystemRevision(st os.FileInfo) string {
	sum := sha256.Sum256([]byte(version(st)))
	return hex.EncodeToString(sum[:])
}
func filesystemInfo(p string, st os.FileInfo) filesystemEntry {
	ext := strings.ToLower(filepath.Ext(p))
	typ := mime.TypeByExtension(ext)
	if typ == "" {
		typ = map[string]string{".mov": "video/quicktime", ".m4v": "video/mp4", ".mkv": "video/x-matroska", ".m4a": "audio/mp4", ".heic": "image/heic", ".heif": "image/heif", ".webp": "image/webp"}[ext]
	}
	if i := strings.IndexByte(typ, ';'); i >= 0 {
		typ = typ[:i]
	}
	if typ == "" {
		typ = "application/octet-stream"
	}
	if st.IsDir() {
		typ = "directory"
	}
	return filesystemEntry{Name: st.Name(), Path: "/" + strings.TrimPrefix(p, "./"), Type: typ,
		Size: st.Size(), Modified: st.ModTime().UTC().Format(time.RFC3339Nano), Revision: filesystemRevision(st),
		HasPreview: !st.IsDir() && previewSupported(typ)}
}

func (s *Service) filesystemRoot(r *http.Request) (rootFS, string, error) {
	source := r.URL.Query().Get("source")
	// Internal upload spools must never be exposed as a source.
	found := false
	for _, root := range s.config.Roots {
		if root.Name == source {
			found = true
			break
		}
	}
	if !found {
		return nil, "", ErrUnsafe
	}
	p, e := filesystemPath(r.URL.Query().Get("path"))
	if e != nil {
		return nil, "", e
	}
	root, e := s.root(source)
	if e != nil {
		return nil, "", e
	}
	if e = safe(root, p); e != nil {
		root.Close()
		return nil, "", e
	}
	return root, p, nil
}
func filesystemFail(w http.ResponseWriter, e error) {
	switch {
	case errors.Is(e, ErrUnsupported):
		reply(w, 415, map[string]string{"error": "Preview unavailable"})
		return
	case errors.Is(e, os.ErrExist), errors.Is(e, unix.ENOTEMPTY):
		e = ErrConflict
	case errors.Is(e, os.ErrPermission):
		w.Header().Set("Content-Type", "application/json")
		reply(w, 403, map[string]string{"error": "Permission denied"})
		return
	}
	fail(w, e)
}

func (s *Service) filesystemRoutes(mux *http.ServeMux) {
	if !s.config.Filesystem.Enabled {
		return
	}
	s.filesystemUploadSessions(mux)
	mux.HandleFunc("GET /v1/fs/sources", func(w http.ResponseWriter, r *http.Request) {
		type scope struct {
			Name        string          `json:"name"`
			Scope       string          `json:"scope"`
			Permissions map[string]bool `json:"permissions"`
		}
		scopes := []scope{}
		for _, root := range s.config.Roots {
			scopes = append(scopes, scope{root.Name, "/", map[string]bool{"view": true, "download": true, "modify": true, "create": true, "delete": true}})
		}
		reply(w, 200, map[string]any{"version": 1, "scopes": scopes})
	})
	mux.HandleFunc("GET /v1/fs/resources", s.filesystemResource)
	mux.HandleFunc("GET /v1/fs/content", s.filesystemContent)
	mux.HandleFunc("POST /v1/fs/folders", s.filesystemFolder)
	mux.HandleFunc("POST /v1/fs/content", s.filesystemUpload)
	mux.HandleFunc("PUT /v1/fs/content", s.filesystemUpload)
	mux.HandleFunc("POST /v1/fs/move", s.filesystemMove)
	mux.HandleFunc("GET /v1/fs/thumbnail", s.filesystemThumbnail)
}
func (s *Service) filesystemResource(w http.ResponseWriter, r *http.Request) {
	root, p, e := s.filesystemRoot(r)
	if e != nil {
		filesystemFail(w, e)
		return
	}
	defer root.Close()
	st, e := root.Stat(p)
	if e != nil {
		filesystemFail(w, e)
		return
	}
	if !st.IsDir() && !st.Mode().IsRegular() {
		filesystemFail(w, ErrUnsafe)
		return
	}
	links, e := s.resolvedArchiveLinks()
	unknownArchive := errors.Is(e, ErrVaultLocked)
	if unknownArchive {
		e = nil
	}
	if e != nil {
		filesystemFail(w, e)
		return
	}
	decorate := func(entry *filesystemEntry) {
		entry.ArchiveLink, entry.ArchiveRole, entry.ArchiveRoot = archiveMembership(links, r.URL.Query().Get("source"), entry.Path)
		if unknownArchive || entry.ArchiveRole == "archive" {
			entry.HasPreview = false
		}
	}
	entry := filesystemInfo(p, st)
	decorate(&entry)
	if guarded, ok := root.(*guardedRoot); ok && guarded.lease != nil {
		v, _, err := guarded.lease.Boundary(filepath.Join(root.Name(), p))
		if err != nil {
			filesystemFail(w, protectionError(err))
			return
		}
		if v != nil {
			entry.Vault = v.ID
		}
	} else if s.vaults != nil {
		if v := s.vaults.pathVault(r.URL.Query().Get("source"), p); v != nil {
			entry.Vault = v.ID
		}
	}

	if p == "." {
		entry.Path = "/"
	}
	if st.IsDir() {
		f, e := root.Open(p)
		if e != nil {
			filesystemFail(w, e)
			return
		}
		defer f.Close()
		children, e := f.ReadDir(-1)
		if e != nil {
			filesystemFail(w, e)
			return
		}
		entry.Children = []filesystemEntry{}
		for _, child := range children {
			cp := filepath.Join(p, child.Name())
			if filesystemHidden(cp) || child.Type()&os.ModeSymlink != 0 {
				continue
			}
			// Locked placeholders are supplied by the existing protection API instead.
			if e := safe(root, cp); e != nil {
				continue
			}
			info, e := root.Stat(cp)
			if e != nil {
				continue
			}
			if info.IsDir() || info.Mode().IsRegular() {
				childInfo := filesystemInfo(cp, info)
				decorate(&childInfo)
				entry.Children = append(entry.Children, childInfo)
			}
		}
	}
	w.Header().Set("ETag", `"`+entry.Revision+`"`)
	reply(w, 200, entry)
}
func (s *Service) filesystemContent(w http.ResponseWriter, r *http.Request) {
	root, p, e := s.filesystemRoot(r)
	if e != nil {
		filesystemFail(w, e)
		return
	}
	defer root.Close()
	f, e := filesystemOpenFile(root, p)
	if e != nil {
		filesystemFail(w, e)
		return
	}
	defer f.Close()
	st, e := f.Stat()
	if e != nil {
		filesystemFail(w, e)
		return
	}
	if !st.Mode().IsRegular() {
		filesystemFail(w, ErrUnsafe)
		return
	}
	entry := filesystemInfo(p, st)
	w.Header().Set("Content-Type", entry.Type)
	w.Header().Set("ETag", `"`+entry.Revision+`"`)
	// The root and broker lease stay alive until streaming completes. Stop stalled
	// clients from pinning a protected mount indefinitely.
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(60 * time.Second))
	http.ServeContent(filesystemWriter{w}, r, st.Name(), st.ModTime(), f)
}
func (s *Service) filesystemFolder(w http.ResponseWriter, r *http.Request) {
	root, p, e := s.filesystemRoot(r)
	if e != nil {
		filesystemFail(w, e)
		return
	}
	defer root.Close()
	if p == "." || protected(p) {
		filesystemFail(w, ErrUnsafe)
		return
	}
	s.fsMu.Lock()
	defer s.fsMu.Unlock()
	if g, ok := root.(*guardedRoot); ok {
		if e = g.mutation(p, ""); e != nil {
			filesystemFail(w, e)
			return
		}
	}
	parent, e := root.Open(filepath.Dir(p))
	if e != nil {
		filesystemFail(w, e)
		return
	}
	defer parent.Close()
	if e = unix.Mkdirat(int(parent.Fd()), filepath.Base(p), 0770); e != nil {
		filesystemFail(w, e)
		return
	}
	w.WriteHeader(http.StatusCreated)
}
func (s *Service) filesystemUpload(w http.ResponseWriter, r *http.Request) {
	root, p, e := s.filesystemRoot(r)
	if e != nil {
		filesystemFail(w, e)
		return
	}
	defer root.Close()
	if p == "." || protected(p) {
		filesystemFail(w, ErrUnsafe)
		return
	}
	if g, ok := root.(*guardedRoot); ok {
		if e = g.mutation(p, ""); e != nil {
			filesystemFail(w, e)
			return
		}
	}
	replacing := r.Method == "PUT"
	expected := r.Header.Get("If-Match")
	if replacing && (expected == "" || expected == "*") {
		reply(w, 428, map[string]string{"error": "An expected revision is required"})
		return
	}
	if st, err := root.Stat(p); err == nil {
		if !replacing {
			filesystemFail(w, ErrConflict)
			return
		}
		if !st.Mode().IsRegular() || expected != `"`+filesystemRevision(st)+`"` {
			reply(w, 412, map[string]string{"error": "File changed"})
			return
		}
	} else if replacing || !errors.Is(err, os.ErrNotExist) {
		filesystemFail(w, err)
		return
	}
	// The temporary file is beside its destination, including inside encrypted
	// mounts. No plaintext payload is spooled into global state or /tmp.
	stageDir := filepath.Join(filepath.Dir(p), thumbnailDirectory)
	if e = safe(root, stageDir); e != nil {
		filesystemFail(w, e)
		return
	}
	if e = root.MkdirAll(stageDir, 0770); e != nil {
		filesystemFail(w, e)
		return
	}
	temp := filepath.Join(stageDir, "upload-"+ID())
	f, e := root.OpenFile(temp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0660)
	if e != nil {
		filesystemFail(w, e)
		return
	}
	defer root.Remove(temp)
	_, e = io.Copy(f, filesystemReader{r.Context(), r.Body, http.NewResponseController(w)})
	if e == nil {
		e = f.Sync()
	}
	closeErr := f.Close()
	if e == nil {
		e = closeErr
	}
	if e == nil {
		e = r.Context().Err()
	}
	if e != nil {
		filesystemFail(w, e)
		return
	}
	s.fsMu.Lock()
	defer s.fsMu.Unlock()
	if e = safe(root, p); e != nil {
		filesystemFail(w, e)
		return
	}
	if replacing {
		st, err := root.Stat(p)
		if err != nil {
			filesystemFail(w, err)
			return
		}
		if !st.Mode().IsRegular() || expected != `"`+filesystemRevision(st)+`"` {
			reply(w, 412, map[string]string{"error": "File changed"})
			return
		}
		a, err := root.Open(stageDir)
		if err != nil {
			filesystemFail(w, err)
			return
		}
		defer a.Close()
		b, err := root.Open(filepath.Dir(p))
		if err != nil {
			filesystemFail(w, err)
			return
		}
		defer b.Close()
		unlock, lockErr := lockFilenamePublication(root)
		if lockErr != nil {
			filesystemFail(w, lockErr)
			return
		}
		e = filenameReplacement(root, p)
		if e == nil {
			e = unix.Renameat(int(a.Fd()), filepath.Base(temp), int(b.Fd()), filepath.Base(p))
		}
		unlock()
	} else {
		e = renameExclusive(root, temp, p)
	}
	if e != nil {
		filesystemFail(w, e)
		return
	}
	st, e := root.Stat(p)
	if e != nil {
		filesystemFail(w, e)
		return
	}
	reply(w, 201, filesystemInfo(p, st))
}
func (s *Service) filesystemMove(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Source string `json:"source"`
		From   string `json:"from"`
		To     string `json:"to"`
	}
	if e := body(r, &input); e != nil {
		filesystemFail(w, e)
		return
	}
	from, e := filesystemPath(input.From)
	if e != nil {
		filesystemFail(w, e)
		return
	}
	to, e := filesystemPath(input.To)
	if e != nil || from == "." || to == "." || protected(from) || protected(to) || within(to, from) {
		filesystemFail(w, ErrUnsafe)
		return
	}
	q := r.URL.Query()
	q.Set("source", input.Source)
	q.Set("path", input.From)
	r.URL.RawQuery = q.Encode()
	root, _, e := s.filesystemRoot(r)
	if e != nil {
		filesystemFail(w, e)
		return
	}
	defer root.Close()
	archiveMu.Lock()
	defer archiveMu.Unlock()
	s.fsMu.Lock()
	defer s.fsMu.Unlock()
	if e = s.prepareArchiveRename(input.Source, from, to); e != nil {
		filesystemFail(w, e)
		return
	}
	if e = safe(root, to); e == nil {
		e = renameExclusive(root, from, to)
	}
	if e != nil {
		filesystemFail(w, e)
		return
	}
	if e = s.finishArchiveRename(); e != nil {
		filesystemFail(w, e)
		return
	}
	w.WriteHeader(204)
}

// Nonblocking open lets us reject device/FIFO objects without hanging a request.
func filesystemOpenFile(root rootFS, p string) (*os.File, error) {
	f, e := root.OpenFile(p, os.O_RDONLY|unix.O_NONBLOCK|unix.O_NOFOLLOW, 0)
	if e != nil {
		return nil, e
	}
	st, e := f.Stat()
	if e != nil || !st.Mode().IsRegular() {
		f.Close()
		if e != nil {
			return nil, e
		}
		return nil, ErrUnsafe
	}
	return f, nil
}

// Deadlines measure inactivity rather than total transfer time. Large files on
// slow links can continue while clients keep making progress.
type filesystemWriter struct{ http.ResponseWriter }

func (w filesystemWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
func (w filesystemWriter) Write(p []byte) (int, error) {
	_ = http.NewResponseController(w.ResponseWriter).SetWriteDeadline(time.Now().Add(time.Minute))
	return w.ResponseWriter.Write(p)
}

type filesystemReader struct {
	ctx        context.Context
	body       io.Reader
	controller *http.ResponseController
}

func (r filesystemReader) Read(p []byte) (int, error) {
	if e := r.ctx.Err(); e != nil {
		return 0, e
	}
	_ = r.controller.SetReadDeadline(time.Now().Add(2 * time.Minute))
	return r.body.Read(p)
}
