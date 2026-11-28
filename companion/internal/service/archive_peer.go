package service

// Archive coordination and media processing stay on the library server. Only
// bounded filesystem operations run on the archive owner; no client relays bytes.
import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

type archivePeerRequest struct {
	Owner     string               `json:"owner,omitempty"`
	Records   []migrationOperation `json:"records,omitempty"`
	Operation string               `json:"operation"`
	Source    string               `json:"source"`
	Path      string               `json:"path"`
	Other     string               `json:"other,omitempty"`
	Directory bool                 `json:"directory,omitempty"`
	Size      int64                `json:"size,omitempty"`
	Hash      string               `json:"hash,omitempty"`
	Stage     string               `json:"stage,omitempty"`
	Modified  string               `json:"modified,omitempty"`
	Key       string               `json:"key,omitempty"`
	Data      []byte               `json:"data,omitempty"`
	Endpoint  ArchiveEndpoint      `json:"endpoint,omitempty"`
}
type archivePeerResponse struct {
	Error string          `json:"error,omitempty"`
	Value json.RawMessage `json:"value,omitempty"`
}
type archivePeerInfo struct {
	FileName    string
	Length      int64
	Permissions os.FileMode
	Modified    int64
	Device      uint64
	Inode       uint64
	Links       uint64
}

func (i archivePeerInfo) Name() string       { return i.FileName }
func (i archivePeerInfo) Size() int64        { return i.Length }
func (i archivePeerInfo) Mode() os.FileMode  { return i.Permissions }
func (i archivePeerInfo) ModTime() time.Time { return time.Unix(0, i.Modified) }
func (i archivePeerInfo) IsDir() bool        { return i.Permissions.IsDir() }
func (i archivePeerInfo) Sys() any {
	return &syscall.Stat_t{Dev: i.Device, Ino: i.Inode}
}
func peerInfo(i os.FileInfo) archivePeerInfo {
	out := archivePeerInfo{FileName: i.Name(), Length: i.Size(), Permissions: i.Mode(), Modified: i.ModTime().UnixNano()}
	if st, ok := i.Sys().(*syscall.Stat_t); ok {
		out.Device = uint64(st.Dev)
		out.Inode = st.Ino
		out.Links = uint64(st.Nlink)
	}
	return out
}

type archivePeerRoot struct {
	peer   PeerConfig
	source string
}

var archivePeerClients sync.Map

func archivePeerClient(p PeerConfig) *http.Client {
	key, _ := json.Marshal(p)
	if c, ok := archivePeerClients.Load(string(key)); ok {
		return c.(*http.Client)
	}
	base := peerClient(p)
	transport := base.Transport.(*http.Transport).Clone()
	transport.ResponseHeaderTimeout = 24 * time.Hour
	client := &http.Client{Transport: transport, Timeout: 24 * time.Hour, CheckRedirect: base.CheckRedirect}
	c, loaded := archivePeerClients.LoadOrStore(string(key), client)
	if loaded {
		transport.CloseIdleConnections()
	}
	return c.(*http.Client)
}

func (p *archivePeerRoot) Name() string { return "archive-peer:" + p.peer.ID + ":" + p.source }
func (p *archivePeerRoot) Close() error { return nil }

// Remote descriptors must never be handed to an encoder or local syscall.
func (p *archivePeerRoot) Open(string) (*os.File, error) { return nil, ErrUnsupported }
func (p *archivePeerRoot) OpenFile(string, int, os.FileMode) (*os.File, error) {
	return nil, ErrUnsupported
}
func (p *archivePeerRoot) FS() fs.FS { return archivePeerFS{p} }

type archivePeerFS struct{ p *archivePeerRoot }

func (f archivePeerFS) Open(string) (fs.File, error) { return nil, ErrUnsupported }
func (p *archivePeerRoot) call(in archivePeerRequest, out any) error {
	return p.callContext(context.Background(), in, out)
}
func (p *archivePeerRoot) callContext(ctx context.Context, in archivePeerRequest, out any) error {
	timeout := 2 * time.Minute
	if in.Operation == "hash" {
		timeout = 24 * time.Hour
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	in.Source = p.source
	var response archivePeerResponse
	if e := peerJSON(ctx, p.peer, "/v1/peer/archive", in, &response); e != nil {
		return e
	}
	return decodeArchivePeer(response, out)
}
func decodeArchivePeer(r archivePeerResponse, out any) error {
	switch r.Error {
	case "":
	case "missing":
		return os.ErrNotExist
	case "locked":
		return ErrVaultLocked
	case "permission":
		return os.ErrPermission
	case "conflict":
		return ErrConflict
	case "unsafe":
		return ErrUnsafe
	default:
		return ErrMount
	}
	if out != nil {
		return json.Unmarshal(r.Value, out)
	}
	return nil
}
func (p *archivePeerRoot) stat(path, op string) (os.FileInfo, error) {
	var out archivePeerInfo
	e := p.call(archivePeerRequest{Operation: op, Path: path}, &out)
	return out, e
}
func (p *archivePeerRoot) Stat(path string) (os.FileInfo, error)  { return p.stat(path, "stat") }
func (p *archivePeerRoot) Lstat(path string) (os.FileInfo, error) { return p.stat(path, "lstat") }
func (p *archivePeerRoot) ReadFile(path string) ([]byte, error) {
	var out []byte
	e := p.call(archivePeerRequest{Operation: "read", Path: path}, &out)
	return out, e
}
func (p *archivePeerRoot) WriteFile(path string, b []byte, _ os.FileMode) error {
	return p.call(archivePeerRequest{Operation: "write", Path: path, Data: b}, nil)
}
func (p *archivePeerRoot) MkdirAll(path string, _ os.FileMode) error {
	return p.call(archivePeerRequest{Operation: "mkdir", Path: path}, nil)
}
func (p *archivePeerRoot) Chmod(string, os.FileMode) error { return ErrUnsupported }
func (p *archivePeerRoot) Remove(path string) error {
	return p.call(archivePeerRequest{Operation: "remove", Path: path}, nil)
}
func (p *archivePeerRoot) RemoveAll(path string) error {
	return p.call(archivePeerRequest{Operation: "remove-all", Path: path}, nil)
}

func (p *archivePeerRoot) copyOriginal(ctx context.Context, src rootFS, from, to, expected, stage string) error {
	input, e := filesystemOpenFile(src, from)
	if e != nil {
		return e
	}
	defer input.Close()
	info, e := input.Stat()
	if e != nil {
		return e
	}
	if info.Size() < 0 {
		return ErrUnsafe
	}
	if expected == "" {
		h := sha256.New()
		if _, e = io.Copy(h, archiveReader{ctx, input}); e != nil {
			return e
		}
		expected = hex.EncodeToString(h.Sum(nil))
	}
	// Migrated archives may already contain this exact original. Verify it
	// on its owner before sending any bytes or allocating another full copy.
	if st, err := p.Stat(to); err == nil {
		if !st.Mode().IsRegular() || st.Size() != info.Size() {
			return ErrConflict
		}
		hash, err := archiveHashContext(ctx, p, to)
		if err != nil {
			return err
		}
		if hash != expected {
			return ErrConflict
		}
		if err = normSetModified(p, to, info.ModTime().UTC().Format(time.RFC3339Nano)); err != nil {
			return err
		}
		latest, err := input.Stat()
		if err != nil {
			return err
		}
		if version(latest) != version(info) {
			return ErrConflict
		}
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if e = p.callContext(ctx, archivePeerRequest{Operation: "filename", Path: to}, nil); e != nil && !errors.Is(e, os.ErrNotExist) {
		return e
	}
	if _, e = input.Seek(0, 0); e != nil {
		return e
	}
	in := archivePeerRequest{Source: peerSourceFor(p.source, p.peer.ID), Path: to, Stage: stage, Size: info.Size(), Hash: expected, Modified: info.ModTime().UTC().Format(time.RFC3339Nano)}
	metadata, e := json.Marshal(in)
	if e != nil {
		return e
	}
	body := io.MultiReader(bytes.NewReader(append(metadata, '\n')), io.LimitReader(input, info.Size()+1))
	response, e := peerRequest(ctx, p.peer, "POST", "/v1/peer/archive-content", nil, body)
	if e != nil {
		return e
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		return peerError(response.StatusCode)
	}
	var result archivePeerResponse
	if e = json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&result); e != nil {
		return e
	}
	if e = decodeArchivePeer(result, nil); e != nil {
		return e
	}
	latest, e := input.Stat()
	if e != nil {
		return e
	}
	if version(latest) != version(info) {
		return ErrConflict
	}
	return nil
}

func archivePeerReply(w http.ResponseWriter, value any, e error) {
	out := archivePeerResponse{}
	if e != nil {
		switch {
		case errors.Is(e, os.ErrNotExist), errors.Is(e, sql.ErrNoRows):
			out.Error = "missing"
		case errors.Is(e, ErrVaultLocked):
			out.Error = "locked"
		case errors.Is(e, os.ErrPermission):
			out.Error = "permission"
		case errors.Is(e, ErrConflict):
			out.Error = "conflict"
		case errors.Is(e, ErrUnsafe):
			out.Error = "unsafe"
		default:
			out.Error = "unavailable"
		}
	} else {
		out.Value, _ = json.Marshal(value)
	}
	reply(w, 200, out)
}
func (s *Service) archivePeerAuthorized(r *http.Request) bool {
	return len(s.config.ArchivePeerKey) >= 32 && subtle.ConstantTimeCompare([]byte(s.config.ArchivePeerKey), []byte(r.Header.Get("X-FileBeans-Archive-Key"))) == 1
}
func (s *Service) ownsArchiveSource(source string) bool {
	for _, root := range s.config.Roots {
		if root.Name == source {
			return true
		}
	}
	return false
}
func (s *Service) archivePeerRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /v1/peer/archive", func(w http.ResponseWriter, r *http.Request) {
		if !s.archivePeerAuthorized(r) {
			fail(w, ErrAuthorization)
			return
		}
		var in archivePeerRequest
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<20))
		decoder.DisallowUnknownFields()
		err := decoder.Decode(&in)
		var extra any
		if err != nil || decoder.Decode(&extra) != io.EOF || !s.ownsArchiveSource(in.Source) {
			fail(w, ErrUnsafe)
			return
		}
		if in.Operation == "hash" {
			deadline := time.Now().Add(24 * time.Hour)
			_ = http.NewResponseController(w).SetReadDeadline(deadline)
			_ = http.NewResponseController(w).SetWriteDeadline(deadline)
		}
		value, e := s.archivePeerOperation(r.Context(), in)
		archivePeerReply(w, value, e)
	})
	mux.HandleFunc("POST /v1/peer/archive-content", func(w http.ResponseWriter, r *http.Request) {
		if !s.archivePeerAuthorized(r) {
			fail(w, ErrAuthorization)
			return
		}
		deadline := time.Now().Add(24 * time.Hour)
		_ = http.NewResponseController(w).SetReadDeadline(deadline)
		_ = http.NewResponseController(w).SetWriteDeadline(deadline)
		reader := bufio.NewReaderSize(r.Body, 32<<10)
		line, e := reader.ReadSlice('\n')
		var in archivePeerRequest
		if e != nil || json.Unmarshal(line, &in) != nil || !s.ownsArchiveSource(in.Source) {
			fail(w, ErrUnsafe)
			return
		}
		e = s.receiveArchivePeer(r.Context(), in, reader)
		archivePeerReply(w, nil, e)
	})
}
func (s *Service) archivePeerOperation(ctx context.Context, in archivePeerRequest) (any, error) {
	if in.Operation == "mirrors" {
		return nil, s.applyArchiveMirrors(in)
	}
	writer := s.archiveWriter()
	if in.Operation == "endpoint" {
		return s.endpointAt(in.Source, in.Path)
	}
	if in.Operation == "resolve" {
		if in.Endpoint.Source != in.Source || in.Endpoint.Pending != nil && in.Endpoint.Pending.Source != in.Source {
			return nil, ErrUnsafe
		}
		return s.resolveEndpoint(in.Endpoint), nil
	}
	root, e := writer.root(in.Source)
	if e != nil {
		return nil, e
	}
	defer root.Close()
	path, e := clean(in.Path)
	if e != nil {
		return nil, e
	}
	if e = safe(root, path); e != nil {
		return nil, e
	}
	switch in.Operation {
	case "stat", "lstat":
		var st os.FileInfo
		if in.Operation == "stat" {
			st, e = root.Stat(path)
		} else {
			st, e = root.Lstat(path)
		}
		if e != nil {
			return nil, e
		}
		return peerInfo(st), nil
	case "mkdir":
		return nil, mkdir(root, path)
	case "sync":
		return nil, syncDir(root, path)
	case "rename":
		return nil, renameExclusive(root, path, in.Other)
	case "filename":
		return nil, filenameConflict(root, path, in.Other, in.Directory, piManifestLimit)
	case "modified":
		return nil, normSetModified(root, path, in.Other)
	case "match":
		return archiveMatch(root, path, in.Other, in.Directory, piManifestLimit)
	case "names":
		return archiveNames(context.WithValue(ctx, peerInventoryKey{}, true), root, path)
	case "space":
		return archiveSpace(root, path, in.Size)
	case "hash":
		if s.config.Profile == "pi" {
			s.piBulk.Lock()
			defer s.piBulk.Unlock()
		}
		return archiveHashContext(context.WithValue(ctx, piPaceKey{}, s.piPace), root, path)
	case "remove", "remove-all":
		if path == "." {
			return nil, ErrUnsafe
		}
		if in.Operation == "remove" {
			return nil, root.Remove(path)
		}
		return nil, root.RemoveAll(path)
	case "read":
		f, e := filesystemOpenFile(root, path)
		if e != nil {
			return nil, e
		}
		defer f.Close()
		b, e := io.ReadAll(io.LimitReader(f, (2<<20)+1))
		if len(b) > 2<<20 {
			return nil, ErrUnsafe
		}
		return b, e
	case "write":
		if len(in.Data) > 2<<20 || !archiveCachePath(path) {
			return nil, ErrUnsafe
		}
		f, e := root.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0660)
		if e != nil {
			return nil, e
		}
		_, e = f.Write(in.Data)
		if e == nil {
			e = f.Sync()
		}
		ce := f.Close()
		if e == nil {
			e = ce
		}
		return nil, e
	case "trash":
		var old Trash
		if e = writer.existing(in.Key, "trash", &old); e == nil {
			if old.Source != in.Source {
				return nil, ErrUnsafe
			}
			if old.State != "retained" {
				access, err := writer.accessTrash(old)
				if err != nil {
					return nil, err
				}
				defer access.close()
				if _, err = access.bin.Stat(trashPath(old)); err != nil {
					if !errors.Is(err, os.ErrNotExist) {
						return nil, err
					}
					if err = retainTrashAccess(access, old); err != nil {
						return nil, err
					}
				}
				old.State = "retained"
				if err = writer.save(old.ID, "trash", old.State, old); err != nil {
					return nil, err
				}
			}
			if err := writer.archiveTrashCache(old, true); err != nil {
				return nil, err
			}
			return old, nil
		}
		return writer.trash(Item{Source: in.Source, Path: path}, in.Key)
	case "trash-key":
		var t Trash
		e := writer.existing(in.Key, "trash", &t)
		if e == nil && t.Source != in.Source {
			return nil, ErrUnsafe
		}
		return t, e
	case "trash-status", "restore", "purge":
		var t Trash
		if e = s.read(in.Key, "trash", &t); e != nil {
			return nil, e
		}
		if t.Source != in.Source {
			return nil, ErrUnsafe
		}
		if in.Operation == "restore" {
			e = writer.restore(t.ID)
		}
		if in.Operation == "purge" {
			e = writer.purge(&t)
		}
		if e != nil {
			return nil, e
		}
		e = s.read(t.ID, "trash", &t)
		return t, e
	}
	return nil, ErrUnsafe
}

func (s *Service) receiveArchivePeer(ctx context.Context, in archivePeerRequest, input io.Reader) error {
	ctx = context.WithValue(ctx, piPaceKey{}, s.piPace)
	if s.config.Profile == "pi" {
		s.piBulk.Lock()
		defer s.piBulk.Unlock()
	}
	if in.Size < 0 || len(in.Hash) != 64 {
		return ErrUnsafe
	}
	if _, e := hex.DecodeString(in.Hash); e != nil {
		return ErrUnsafe
	}
	path, e := clean(in.Path)
	if e != nil || path == "." {
		return ErrUnsafe
	}
	stage, e := clean(in.Stage)
	if e != nil || stage == "." || stage == path || !strings.Contains("/"+stage, "/"+archiveStage+"/") {
		return ErrUnsafe
	}
	root, e := s.archiveWriter().root(in.Source)
	if e != nil {
		return e
	}
	defer root.Close()
	if e = safe(root, path); e != nil {
		return e
	}
	if e = safe(root, stage); e != nil {
		return e
	}
	if _, e = archiveSpace(root, ".", in.Size); e != nil {
		return e
	}
	if e = mkdir(root, filepath.Dir(stage)); e != nil {
		return e
	}
	if e = root.Remove(stage); e != nil && !errors.Is(e, os.ErrNotExist) {
		return e
	}
	defer root.Remove(stage)
	out, e := root.OpenFile(stage, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0660)
	if e != nil {
		return e
	}
	h := sha256.New()
	buffer := make([]byte, 256<<10)
	remaining := in.Size
	for e == nil && remaining > 0 {
		var n int
		n, e = io.ReadFull(input, buffer[:min(int64(len(buffer)), remaining)])
		if e == nil {
			e = s.piPace(ctx, n)
		}
		if e == nil {
			_, e = out.Write(buffer[:n])
			h.Write(buffer[:n])
			remaining -= int64(n)
		}
	}
	if e == nil {
		var b [1]byte
		n, extra := input.Read(b[:])
		if n != 0 || extra != io.EOF {
			e = ErrConflict
		}
	}
	if e == nil && hex.EncodeToString(h.Sum(nil)) != in.Hash {
		e = ErrConflict
	}
	if e == nil {
		e = out.Sync()
	}
	ce := out.Close()
	if e == nil {
		e = ce
	}
	if e != nil {
		return e
	}
	readback, e := archiveHashContext(ctx, root, stage)
	if e != nil {
		return e
	}
	if readback != in.Hash {
		return ErrConflict
	}
	if e = normSetModified(root, stage, in.Modified); e != nil {
		return e
	}
	if st, err := root.Stat(path); err == nil {
		if !st.Mode().IsRegular() || st.Size() != in.Size {
			return ErrConflict
		}
		hash, err := archiveHashContext(ctx, root, path)
		if err != nil {
			return err
		}
		if hash != in.Hash {
			return ErrConflict
		}
		return normSetModified(root, path, in.Modified)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if e = mkdir(root, filepath.Dir(path)); e != nil {
		return e
	}
	if e = renameExclusive(root, stage, path); e != nil {
		return e
	}
	return syncDir(root, filepath.Dir(path))
}
