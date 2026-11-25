package service

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

// Sessions live beside the destination, including metadata and partial bytes.
// No protected filename or plaintext is written to the ordinary operations DB.
type filesystemUploadSession struct {
	ID        string `json:"id"`
	Path      string `json:"path"`
	Size      int64  `json:"size"`
	Received  int64  `json:"received"`
	Completed bool   `json:"completed"`
}

func (s *Service) filesystemUploadSessions(mux *http.ServeMux) {
	mux.HandleFunc("POST /v1/fs/uploads/{id}", s.filesystemUploadSession)
	mux.HandleFunc("GET /v1/fs/uploads/{id}", s.filesystemUploadSession)
	mux.HandleFunc("PUT /v1/fs/uploads/{id}", s.filesystemUploadSession)
	mux.HandleFunc("DELETE /v1/fs/uploads/{id}", s.filesystemUploadSession)
	mux.HandleFunc("POST /v1/fs/uploads/{id}/complete", s.filesystemUploadSession)
}
func uploadSessionID(id string) bool {
	if len(id) != 36 {
		return false
	}
	for _, c := range id {
		if !strings.ContainsRune("0123456789abcdefABCDEF-", c) {
			return false
		}
	}
	return true
}
func (s *Service) filesystemUploadSession(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !uploadSessionID(id) {
		filesystemFail(w, ErrUnsafe)
		return
	}
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
	dir := filepath.Join(filepath.Dir(p), thumbnailDirectory, "upload-"+id)
	if e = safe(root, dir); e != nil {
		filesystemFail(w, e)
		return
	}
	// One service owns each configured root; serialize offsets and publication.
	s.fsMu.Lock()
	defer s.fsMu.Unlock()
	if e = checkArchiveRoot(root, p, false); e != nil {
		filesystemFail(w, e)
		return
	}
	manifest, payload := filepath.Join(dir, "session.json"), filepath.Join(dir, "payload")
	var session filesystemUploadSession
	data, readErr := root.ReadFile(manifest)
	if readErr == nil {
		if e = json.Unmarshal(data, &session); e != nil || session.ID != id || session.Path != p {
			filesystemFail(w, ErrConflict)
			return
		}
	}
	complete := strings.HasSuffix(r.URL.Path, "/complete")
	if r.Method == "POST" && !complete {
		var input struct {
			Size int64 `json:"size"`
		}
		if body(r, &input) != nil || input.Size < 0 {
			filesystemFail(w, ErrUnsafe)
			return
		}
		if readErr == nil {
			if session.Size != input.Size {
				filesystemFail(w, ErrConflict)
				return
			}
		} else {
			if !errors.Is(readErr, os.ErrNotExist) {
				filesystemFail(w, readErr)
				return
			}
			if _, e = root.Stat(p); e == nil {
				filesystemFail(w, ErrConflict)
				return
			} else if !errors.Is(e, os.ErrNotExist) {
				filesystemFail(w, e)
				return
			}
			if e = root.MkdirAll(dir, 0700); e != nil {
				filesystemFail(w, e)
				return
			}
			session = filesystemUploadSession{ID: id, Path: p, Size: input.Size}
			data, _ = json.Marshal(session)
			temporary := filepath.Join(dir, "manifest-"+ID())
			mf, err := root.OpenFile(temporary, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
			if err != nil {
				filesystemFail(w, err)
				return
			}
			defer root.Remove(temporary)
			_, e = mf.Write(data)
			if e == nil {
				e = mf.Sync()
			}
			closeErr := mf.Close()
			if e == nil {
				e = closeErr
			}
			if e == nil {
				e = renameExclusive(root, temporary, manifest)
			}
			if e != nil {
				filesystemFail(w, e)
				return
			}
		}
	} else if readErr != nil {
		filesystemFail(w, readErr)
		return
	}
	if r.Method == "DELETE" {
		if e = root.RemoveAll(dir); e != nil {
			filesystemFail(w, e)
			return
		}
		w.WriteHeader(204)
		return
	}
	f, e := root.OpenFile(payload, os.O_RDWR|os.O_CREATE, 0600)
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
	session.Received = st.Size()
	if target, err := root.Stat(p); err == nil && os.SameFile(st, target) {
		session.Completed = true
	}
	if r.Method == "PUT" {
		var start, end, total int64
		if n, err := fmt.Sscanf(r.Header.Get("Content-Range"), "bytes %d-%d/%d", &start, &end, &total); err != nil || n != 3 ||
			session.Completed || start != session.Received || total != session.Size || end < start || end >= total || end-start >= 8*1024*1024 {
			filesystemFail(w, ErrConflict)
			return
		}
		count := end - start + 1
		if _, e = f.Seek(start, io.SeekStart); e == nil {
			var written int64
			written, e = io.Copy(f, io.LimitReader(filesystemReader{r.Context(), r.Body, http.NewResponseController(w)}, count+1))
			if e == nil && written != count {
				e = ErrUnsafe
			}
		}
		if e == nil {
			e = r.Context().Err()
		}
		if e == nil {
			e = f.Sync()
		}
		if e != nil {
			_ = f.Truncate(start)
			_ = f.Sync()
			filesystemFail(w, e)
			return
		}
		session.Received = end + 1
	}
	if complete {
		if session.Received != session.Size {
			filesystemFail(w, ErrConflict)
			return
		}
		if !session.Completed {
			// Retain the staging inode until acknowledgement: a lost completion
			// response is reconciled by inode identity, never by filename/size.
			a, err := root.Open(dir)
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
			e = filenameConflict(root, p, "", false)
			if e == nil {
				e = unix.Linkat(int(a.Fd()), "payload", int(b.Fd()), filepath.Base(p), 0)
			}
			unlock()
			if e != nil {
				filesystemFail(w, e)
				return
			}
			if e = b.Sync(); e != nil {
				filesystemFail(w, e)
				return
			}
			session.Completed = true
		}
	}
	reply(w, 200, session)
}
