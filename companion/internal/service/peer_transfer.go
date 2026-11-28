package service

import (
	"context"
	"crypto/md5"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// A native Drive credential belongs to this job, never the server account.
type peerDriveConnectionKey struct{}

func peerDriveConnection(ctx context.Context) string {
	connection, _ := ctx.Value(peerDriveConnectionKey{}).(string)
	return connection
}

type PeerSelection struct {
	Item    Item   `json:"item"`
	Files   []File `json:"files"`
	Removed bool   `json:"removed,omitempty"`
}

func validatePeerPlan(req CopyRequest, plan []PeerSelection) error {
	if len(plan) != len(req.Items) {
		return ErrUnsafe
	}
	names := map[string]bool{}
	total := 0
	for i, p := range plan {
		if p.Item != req.Items[i] || len(p.Files) == 0 {
			return ErrUnsafe
		}
		top := strings.Split(p.Files[0].To, "/")[0]
		if req.DestinationName != "" {
			top = req.DestinationName
		}
		if !validName(top) || protected(top) || names[strings.ToLower(top)] {
			return ErrConflict
		}
		names[strings.ToLower(top)] = true
		paths := map[string]bool{}
		for _, f := range p.Files {
			total++
			if total > piManifestLimit || f.Source != p.Item.Source || f.Size < 0 || len(f.To) > 4096 || len(f.From) > 4096 {
				return ErrUnsafe
			}
			cleanTo, e := clean(f.To)
			if e != nil || cleanTo != f.To || protected(f.To) || strings.Split(f.To, "/")[0] != strings.Split(p.Files[0].To, "/")[0] || paths[strings.ToLower(f.To)] {
				return ErrUnsafe
			}
			paths[strings.ToLower(f.To)] = true
			if !f.Directory {
				if _, _, e = peerHasher(f.Hash); e != nil {
					return e
				}
			}
		}
	}
	return nil
}

func (s *Service) peerSnapshot(ctx context.Context, item Item) ([]File, error) {
	if s.config.Profile == "pi" {
		ctx = context.WithValue(ctx, piPaceKey{}, s.piPace)
	}
	if p := s.peerFor(item.Source); p != nil {
		var out []File
		e := peerJSON(ctx, *p, "/v1/peer/snapshot", item, &out)
		return out, e
	}
	if item.Source == "drive" {
		files, _, e := s.driveTree(context.WithValue(ctx, peerInventoryKey{}, true), item.Path, peerDriveConnection(ctx), false)
		if len(files) > piManifestLimit {
			return nil, ErrUnsafe
		}
		return files, e
	}
	path, e := clean(item.Path)
	if e != nil || path == "." || protected(path) {
		return nil, ErrUnsafe
	}
	r, e := s.root(item.Source)
	if e != nil {
		return nil, e
	}
	defer r.Close()
	entries, e := inventoryMove(context.WithValue(ctx, peerInventoryKey{}, true), r, path, true)
	if e != nil {
		return nil, e
	}
	vault := ""
	if v := s.vaults.pathVault(item.Source, path); v != nil {
		vault = v.ID
	}
	out := make([]File, 0, len(entries))
	for _, v := range entries {
		out = append(out, File{Source: item.Source, From: filepath.Join(path, v.Path), To: filepath.Join(filepath.Base(path), v.Path), Directory: v.Directory, Size: v.Size, Hash: "sha256:" + v.Hash, Version: v.Version, Vault: vault})
	}
	return out, nil
}

type peerInventoryKey struct{}

func (s *Service) peerRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /v1/peer/version", func(w http.ResponseWriter, r *http.Request) {
		var f File
		if body(r, &f) != nil || s.peerFor(f.Source) != nil {
			fail(w, ErrUnsafe)
			return
		}
		if e := s.verifyPeerFile(r.Context(), f); e != nil {
			fail(w, e)
			return
		}
		reply(w, 200, map[string]bool{"current": true})
	})
	mux.HandleFunc("POST /v1/peer/snapshot", func(w http.ResponseWriter, r *http.Request) {
		var item Item
		if body(r, &item) != nil || s.peerFor(item.Source) != nil {
			fail(w, ErrUnsafe)
			return
		}
		files, e := s.peerSnapshot(r.Context(), item)
		if e != nil {
			fail(w, e)
			return
		}
		reply(w, 200, files)
	})
	mux.HandleFunc("GET /v1/peer/content", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		source, path := q.Get("source"), q.Get("path")
		offset, e := strconv.ParseInt(q.Get("offset"), 10, 64)
		if e != nil || offset < 0 || s.peerFor(source) != nil {
			fail(w, ErrUnsafe)
			return
		}
		f := File{Source: source, From: path, Version: q.Get("version")}
		data, e := s.readPeerChunk(r.Context(), f, offset, 256<<10)
		if e != nil {
			fail(w, e)
			return
		}
		w.WriteHeader(200)
		_, _ = w.Write(data)
	})
	mux.HandleFunc("POST /v1/peer/trash", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Selection PeerSelection `json:"selection"`
			Key       string        `json:"key"`
		}
		decoder := json.NewDecoder(io.LimitReader(r.Body, 16<<20))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&in) != nil || len(in.Key) != 64 || s.peerFor(in.Selection.Item.Source) != nil || len(in.Selection.Files) > piManifestLimit {
			fail(w, ErrUnsafe)
			return
		}
		if e := s.removePeerSource(r.Context(), in.Selection, in.Key); e != nil {
			fail(w, e)
			return
		}
		reply(w, 200, map[string]bool{"removed": true})
	})
}

func (s *Service) verifyPeerFile(ctx context.Context, f File) error {
	if p := s.peerFor(f.Source); p != nil {
		var out map[string]bool
		return peerJSON(ctx, *p, "/v1/peer/version", f, &out)
	}
	if f.Source == "drive" {
		m, e := s.driveMeta(ctx, f.From, peerDriveConnection(ctx))
		if e != nil {
			return e
		}
		if m.Trashed || m.Version != f.Version {
			return ErrConflict
		}
		return nil
	}
	path, e := clean(f.From)
	if e != nil || protected(path) || path == "." {
		return ErrUnsafe
	}
	root, e := s.root(f.Source)
	if e != nil {
		return e
	}
	defer root.Close()
	if e = safe(root, path); e != nil {
		return e
	}
	st, e := root.Stat(path)
	if e != nil {
		return e
	}
	if !st.Mode().IsRegular() || version(st) != f.Version || st.Size() != f.Size {
		return ErrConflict
	}
	return nil
}

func verifyPeerDriveFile(f File, m DriveItem) error {
	if m.Trashed || m.Size != fmt.Sprint(f.Size) {
		return ErrConflict
	}
	if strings.HasPrefix(f.Hash, "sha256:") {
		if m.SHA256 != strings.TrimPrefix(f.Hash, "sha256:") {
			return ErrConflict
		}
	} else if strings.HasPrefix(f.Hash, "md5:") {
		if m.MD5 != strings.TrimPrefix(f.Hash, "md5:") {
			return ErrConflict
		}
	} else {
		return ErrUnsupported
	}
	return nil
}

func (s *Service) readPeerChunk(ctx context.Context, f File, offset int64, length int) ([]byte, error) {
	if e := s.piPace(ctx, length); e != nil {
		return nil, e
	}
	if p := s.peerFor(f.Source); p != nil {
		q := url.Values{"source": {f.Source}, "path": {f.From}, "version": {f.Version}, "offset": {strconv.FormatInt(offset, 10)}}
		resp, e := peerRequest(ctx, *p, "GET", "/v1/peer/content", q, nil)
		if e != nil {
			return nil, e
		}
		defer resp.Body.Close()
		if resp.StatusCode != 200 {
			return nil, peerError(resp.StatusCode)
		}
		b, e := io.ReadAll(io.LimitReader(resp.Body, (256<<10)+1))
		if len(b) > 256<<10 {
			return nil, ErrUnsafe
		}
		if len(b) > length {
			b = b[:length]
		}
		return b, e
	}
	if f.Source == "drive" {
		m, e := s.driveMeta(ctx, f.From, peerDriveConnection(ctx))
		if e != nil {
			return nil, e
		}
		if m.Version != f.Version || m.Trashed {
			return nil, ErrConflict
		}
		reader, e := s.driveContent(ctx, f.From, offset, peerDriveConnection(ctx))
		if e != nil {
			return nil, e
		}
		defer reader.Close()
		return io.ReadAll(io.LimitReader(reader, int64(length)))
	}
	p, e := clean(f.From)
	if e != nil || protected(p) || p == "." {
		return nil, ErrUnsafe
	}
	root, e := s.root(f.Source)
	if e != nil {
		return nil, e
	}
	defer root.Close()
	if e = safe(root, p); e != nil {
		return nil, e
	}
	file, e := root.Open(p)
	if e != nil {
		return nil, e
	}
	defer file.Close()
	st, e := file.Stat()
	if e != nil {
		return nil, e
	}
	if !st.Mode().IsRegular() || version(st) != f.Version {
		return nil, ErrConflict
	}
	data := make([]byte, length)
	n, e := file.ReadAt(data, offset)
	if e == io.EOF {
		e = nil
	}
	after, er := file.Stat()
	if er != nil || version(after) != f.Version {
		return nil, ErrConflict
	}
	return data[:n], e
}

func samePeerFiles(a, b []File) bool {
	if len(a) != len(b) {
		return false
	}
	a = append([]File(nil), a...)
	b = append([]File(nil), b...)
	sort.Slice(a, func(i, j int) bool { return a[i].To < a[j].To })
	sort.Slice(b, func(i, j int) bool { return b[i].To < b[j].To })
	for i, v := range a {
		w := b[i]
		if v.Source != w.Source || v.From != w.From || v.To != w.To || v.Directory != w.Directory || v.Size != w.Size || v.Version != w.Version || v.Hash != w.Hash {
			return false
		}
	}
	return true
}
func peerCleanupKey(job string, item Item) string {
	b, _ := json.Marshal(item)
	sum := sha256.Sum256(append([]byte(job), b...))
	return hex.EncodeToString(sum[:])
}
func (s *Service) removePeerSource(ctx context.Context, selection PeerSelection, key string) error {
	if p := s.peerFor(selection.Item.Source); p != nil {
		var out map[string]bool
		return peerJSON(ctx, *p, "/v1/peer/trash", map[string]any{"selection": selection, "key": key}, &out)
	}
	s.fsMu.Lock()
	defer s.fsMu.Unlock()
	var old Trash
	if e := s.existing(key, "trash", &old); e == nil {
		if old.Source != selection.Item.Source || old.Original != strings.TrimPrefix(selection.Item.Path, "/") {
			return ErrConflict
		}
		if old.State == "retained" {
			return nil
		}
		return ErrMount
	} else if !errors.Is(e, sql.ErrNoRows) {
		return e
	}
	if selection.Item.Source == "drive" {
		var prior driveMutationRecord
		if e := s.existing(key, "driveMutation", &prior); e == nil {
			_, e = s.driveMutationForConnection(ctx, DriveMutation{Action: "trash", FileID: selection.Item.Path}, key, peerDriveConnection(ctx))
			return e
		}
	}
	current, e := s.peerSnapshot(ctx, selection.Item)
	if e != nil {
		return e
	}
	if !samePeerFiles(selection.Files, current) {
		return ErrConflict
	}
	if selection.Item.Source == "drive" {
		_, e = s.driveMutationForConnection(ctx, DriveMutation{Action: "trash", FileID: selection.Item.Path}, key, peerDriveConnection(ctx))
		return e
	}
	_, e = s.trash(selection.Item, key)
	return e
}

func (s *Service) executePeer(ctx context.Context, j *Job) error {
	ctx = context.WithValue(ctx, peerDriveConnectionKey{}, j.Connection)
	if len(j.PeerPlan) == 0 {
		var plan []PeerSelection
		total := 0
		for _, item := range j.Request.Items {
			files, e := s.peerSnapshot(ctx, item)
			if e != nil {
				return e
			}
			if len(files) == 0 {
				return ErrUnsafe
			}
			total += len(files)
			if total > piManifestLimit {
				return ErrUnsafe
			}
			plan = append(plan, PeerSelection{Item: item, Files: files})
		}
		j.PeerPlan = plan
		if e := s.checkpoint(j); e != nil {
			return e
		}
	}
	if e := validatePeerPlan(j.Request, j.PeerPlan); e != nil {
		return e
	}
	if j.Request.Destination.Source == "drive" {
		if len(j.Manifest) == 0 {
			for _, p := range j.PeerPlan {
				j.Manifest = append(j.Manifest, p.Files...)
			}
			j.ItemsTotal = len(j.Manifest)
			for _, f := range j.Manifest {
				if !f.Directory {
					j.Total += f.Size
				}
			}
		}
		if e := s.executeDrive(ctx, j); e != nil {
			return e
		}
	} else {
		if e := s.copyPeerLocal(ctx, j); e != nil {
			return e
		}
	}
	if j.Request.Operation != "move" {
		return nil
	}
	if j.Move == nil {
		j.Move = &MoveState{Published: true}
	}
	if !j.Move.Committed {
		for _, p := range j.PeerPlan {
			now, e := s.peerSnapshot(ctx, p.Item)
			if e != nil {
				return e
			}
			if !samePeerFiles(p.Files, now) {
				return ErrConflict
			}
		}
		j.Move.Committed = true
		if e := s.checkpoint(j); e != nil {
			return e
		}
	}
	for i := range j.PeerPlan {
		selection := &j.PeerPlan[i]
		if selection.Removed {
			continue
		}
		// Destination verification runs again before every destructive cleanup retry.
		if e := s.verifyPeerDestination(ctx, j); e != nil {
			return e
		}
		if e := s.removePeerSource(ctx, *selection, peerCleanupKey(j.ID, selection.Item)); e != nil {
			return e
		}
		selection.Removed = true
		// Cleanup is committed and cannot be cancelled. The source may now be in
		// locked Trash, so seal progress without requiring it to remain readable.
		if e := s.save(j.ID, "copy", j.State, j); e != nil {
			return e
		}
	}
	return nil
}

func peerHasher(expected string) (hash.Hash, string, error) {
	if strings.HasPrefix(expected, "sha256:") {
		return sha256.New(), strings.TrimPrefix(expected, "sha256:"), nil
	}
	if strings.HasPrefix(expected, "md5:") {
		return md5.New(), strings.TrimPrefix(expected, "md5:"), nil
	}
	return nil, "", ErrUnsupported
}
func (s *Service) copyPeerLocal(ctx context.Context, j *Job) error {
	j.Done = 0
	j.Total = 0
	j.ItemsDone = 0
	j.ItemsTotal = 0
	for _, p := range j.PeerPlan {
		j.ItemsTotal += len(p.Files)
		for _, f := range p.Files {
			if !f.Directory {
				j.Total += f.Size
			}
		}
	}
	root, e := s.root(j.Request.Destination.Source)
	if e != nil {
		return e
	}
	defer root.Close()
	dest, e := clean(j.Request.Destination.Path)
	if e != nil || protected(dest) {
		return ErrUnsafe
	}
	if e = safe(root, dest); e != nil {
		return e
	}
	stage := s.stagePath(j)
	if e = mkdir(root, stage); e != nil {
		return e
	}
	j.State = "running"
	for _, p := range j.PeerPlan {
		name := strings.Split(p.Files[0].To, "/")[0]
		if j.Request.DestinationName != "" {
			name = j.Request.DestinationName
		}
		if !validName(name) || protected(name) {
			return ErrUnsafe
		}
		if contains(j.Published, name) {
			j.ItemsDone += len(p.Files)
			for _, f := range p.Files {
				if !f.Directory {
					j.Done += f.Size
				}
			}
			continue
		}
		target := filepath.Join(dest, name)
		staged := filepath.Join(stage, name)
		if strings.HasPrefix(j.Current, "publish:") && j.Current == "publish:"+name {
			if _, er := root.Stat(staged); errors.Is(er, os.ErrNotExist) {
				if e = s.verifyPeerTree(ctx, root, target, p.Files); e != nil {
					return e
				}
				j.Published = append(j.Published, name)
				j.ItemsDone += len(p.Files)
				for _, f := range p.Files {
					if !f.Directory {
						j.Done += f.Size
					}
				}
				if e = s.checkpoint(j); e != nil {
					return e
				}
				continue
			}
		}
		if _, er := root.Lstat(target); !errors.Is(er, os.ErrNotExist) {
			return ErrConflict
		}
		for _, f := range p.Files {
			rel, e := filepath.Rel(strings.Split(f.To, "/")[0], f.To)
			if e != nil || rel == ".." || strings.HasPrefix(rel, "../") {
				return ErrUnsafe
			}
			to := filepath.Join(staged, rel)
			if e = safe(root, to); e != nil {
				return e
			}
			j.Current = f.To
			if e = s.checkpoint(j); e != nil {
				return e
			}
			if f.Directory {
				j.ItemsDone++
				if e = mkdir(root, to); e != nil {
					return e
				}
				continue
			}
			if e = mkdir(root, filepath.Dir(to)); e != nil {
				return e
			}
			file, e := root.OpenFile(to, os.O_CREATE|os.O_RDWR, 0600)
			if e != nil {
				return e
			}
			e = s.copyPeerFile(ctx, j, file, f)
			closeErr := file.Close()
			if e != nil {
				return e
			}
			if closeErr != nil {
				return closeErr
			}
			j.ItemsDone++
		}
		current, e := s.peerSnapshot(ctx, p.Item)
		if e != nil {
			return e
		}
		if !samePeerFiles(p.Files, current) {
			return ErrConflict
		}
		if e = s.verifyPeerTree(ctx, root, staged, p.Files); e != nil {
			return e
		}
		j.Current = "publish:" + name
		if e = s.checkpoint(j); e != nil {
			return e
		}
		if e = renameExclusive(root, staged, target); e != nil {
			return e
		}
		if e = syncDir(root, dest); e != nil {
			return e
		}
		j.Published = append(j.Published, name)
		if e = s.checkpoint(j); e != nil {
			return e
		}
	}
	return root.RemoveAll(stage)
}
func (s *Service) copyPeerFile(ctx context.Context, j *Job, file *os.File, f File) error {
	h, expected, e := peerHasher(f.Hash)
	if e != nil {
		return e
	}
	st, e := file.Stat()
	if e != nil {
		return e
	}
	offset := st.Size()
	j.Done += offset
	if offset > f.Size {
		return ErrConflict
	}
	if _, e = io.Copy(h, moveReader{ctx: ctx, reader: io.NewSectionReader(file, 0, offset)}); e != nil {
		return e
	}
	for offset < f.Size {
		if e = s.checkpoint(j); e != nil {
			return e
		}
		b, e := s.readPeerChunk(ctx, f, offset, int(min(256<<10, f.Size-offset)))
		if e != nil {
			return e
		}
		if len(b) == 0 || int64(len(b)) > f.Size-offset {
			return io.ErrUnexpectedEOF
		}
		n, e := file.WriteAt(b, offset)
		if e != nil {
			return e
		}
		if n != len(b) {
			return io.ErrShortWrite
		}
		h.Write(b)
		offset += int64(n)
		j.Done += int64(n)
	}
	if hex.EncodeToString(h.Sum(nil)) != expected {
		return ErrConflict
	}
	return file.Sync()
}
func (s *Service) verifyPeerTree(ctx context.Context, root rootFS, path string, files []File) error {
	actual, e := inventoryMove(context.WithValue(ctx, peerInventoryKey{}, true), root, path, true)
	if e != nil {
		return e
	}
	if len(actual) != len(files) {
		return ErrConflict
	}
	by := map[string]MoveEntry{}
	for _, v := range actual {
		by[v.Path] = v
	}
	for _, v := range files {
		rel, e := filepath.Rel(strings.Split(v.To, "/")[0], v.To)
		if e != nil {
			return e
		}
		a, ok := by[rel]
		if !ok || a.Directory != v.Directory || !v.Directory && (a.Size != v.Size) {
			return ErrConflict
		}
		if !v.Directory {
			if strings.HasPrefix(v.Hash, "sha256:") {
				if a.Hash != strings.TrimPrefix(v.Hash, "sha256:") {
					return ErrConflict
				}
				continue
			}
			h, expected, e := peerHasher(v.Hash)
			if e != nil {
				return e
			}
			f, e := root.Open(filepath.Join(path, rel))
			if e != nil {
				return e
			}
			_, e = io.Copy(h, moveReader{ctx: ctx, reader: f})
			f.Close()
			if e != nil {
				return e
			}
			if hex.EncodeToString(h.Sum(nil)) != expected {
				return ErrConflict
			}
		}
	}
	return nil
}
func (s *Service) verifyPeerDestination(ctx context.Context, j *Job) error {
	if j.Request.Destination.Source == "drive" {
		parent, e := s.driveParent(ctx, j.Request.Destination.Path, j.Connection)
		if e != nil {
			return e
		}
		parents := map[string]string{".": parent.ID}
		for _, f := range j.Manifest {
			if f.Directory {
				parents[f.To] = f.TargetID
			}
		}
		for _, f := range j.Manifest {
			m, e := s.driveMeta(ctx, f.TargetID, j.Connection)
			if e != nil {
				return e
			}
			if m.Trashed || m.Name != filepath.Base(f.To) || !contains(m.Parents, parents[filepath.Dir(f.To)]) || m.Properties["filebeansOperation"] != operationProperty(j.ID, f.To) {
				return ErrConflict
			}
			if !f.Directory {
				if m.Size != fmt.Sprint(f.Size) {
					return ErrConflict
				}
				if strings.HasPrefix(f.Hash, "sha256:") {
					if m.SHA256 != strings.TrimPrefix(f.Hash, "sha256:") {
						return ErrConflict
					}
				} else if strings.HasPrefix(f.Hash, "md5:") {
					if m.MD5 != strings.TrimPrefix(f.Hash, "md5:") {
						return ErrConflict
					}
				} else {
					return ErrUnsupported
				}
			}
		}
		return nil
	}
	r, e := s.root(j.Request.Destination.Source)
	if e != nil {
		return e
	}
	defer r.Close()
	for _, p := range j.PeerPlan {
		name := strings.Split(p.Files[0].To, "/")[0]
		if j.Request.DestinationName != "" {
			name = j.Request.DestinationName
		}
		if e = s.verifyPeerTree(ctx, r, filepath.Join(j.Request.Destination.Path, name), p.Files); e != nil {
			return e
		}
	}
	return nil
}
