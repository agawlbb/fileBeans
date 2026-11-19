package service

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
)

type datasetUpload struct {
	ID       string `json:"id"`
	State    string `json:"state"`
	Location Item   `json:"location"`
	Hash     string `json:"hash"`
	Revision string `json:"revision,omitempty"`
}

func fileSHA(f *os.File) (string, error) {
	if _, e := f.Seek(0, 0); e != nil {
		return "", e
	}
	h := sha256.New()
	_, e := io.Copy(h, f)
	return hex.EncodeToString(h.Sum(nil)), e
}
func (s *Service) datasetUpload(w http.ResponseWriter, r *http.Request) {
	hash := r.Header.Get("X-Content-SHA256")
	decoded, e := hex.DecodeString(hash)
	if key(r) == "" || e != nil || len(decoded) != 32 || r.ContentLength < 1 || r.ContentLength > 2<<30 {
		datasetFailure(w, ErrUnsafe)
		return
	}
	root, p, e := s.filesystemRoot(r)
	if e != nil {
		datasetFailure(w, e)
		return
	}
	defer root.Close()
	if p == "." || protected(p) {
		datasetFailure(w, ErrUnsafe)
		return
	}
	item := Item{Source: r.URL.Query().Get("source"), Path: "/" + p}
	s.mu.Lock()
	var j datasetUpload
	e = s.existing(key(r), "dataset-upload", &j)
	if errors.Is(e, sql.ErrNoRows) {
		if _, e = root.Stat(p); !os.IsNotExist(e) {
			s.mu.Unlock()
			datasetFailure(w, ErrConflict)
			return
		}
		j = datasetUpload{ID: ID(), State: "uploading", Location: item, Hash: hash}
		e = s.insert(j.ID, "dataset-upload", j.State, key(r), j)
	}
	s.mu.Unlock()
	if e != nil {
		datasetFailure(w, e)
		return
	}
	if j.Location != item || j.Hash != hash {
		datasetFailure(w, ErrConflict)
		return
	}
	confirm := func() error {
		f, e := filesystemOpenFile(root, p)
		if e != nil {
			return e
		}
		defer f.Close()
		digest, e := fileSHA(f)
		if e != nil {
			return e
		}
		if digest != hash {
			return ErrConflict
		}
		st, e := f.Stat()
		if e != nil {
			return e
		}
		j.State = "ready"
		j.Revision = filesystemRevision(st)
		s.mu.Lock()
		e = s.save(j.ID, "dataset-upload", j.State, j)
		s.mu.Unlock()
		return e
	}
	if _, e = root.Stat(p); e == nil {
		if e = confirm(); e != nil {
			datasetFailure(w, e)
			return
		}
		reply(w, 200, j)
		return
	} else if !os.IsNotExist(e) {
		datasetFailure(w, e)
		return
	}
	dir := filepath.Join(filepath.Dir(p), thumbnailDirectory)
	if e = root.MkdirAll(dir, 0700); e != nil {
		datasetFailure(w, e)
		return
	}
	temp := filepath.Join(dir, "upload-"+ID())
	f, e := root.OpenFile(temp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0660)
	if e != nil {
		datasetFailure(w, e)
		return
	}
	defer root.Remove(temp)
	h := sha256.New()
	n, e := io.Copy(io.MultiWriter(f, h), io.LimitReader(filesystemReader{r.Context(), r.Body, http.NewResponseController(w)}, r.ContentLength+1))
	if e == nil && (n != r.ContentLength || hex.EncodeToString(h.Sum(nil)) != hash) {
		e = ErrConflict
	}
	if e == nil {
		e = f.Sync()
	}
	ce := f.Close()
	if e == nil {
		e = ce
	}
	if e == nil {
		e = r.Context().Err()
	}
	if e != nil {
		datasetFailure(w, e)
		return
	}
	s.fsMu.Lock()
	e = renameExclusive(root, temp, p)
	s.fsMu.Unlock()
	if e != nil && !os.IsExist(e) {
		datasetFailure(w, e)
		return
	}
	if e = confirm(); e != nil {
		datasetFailure(w, e)
		return
	}
	reply(w, 201, j)
}
