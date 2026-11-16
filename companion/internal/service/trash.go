package service

import (
	"encoding/json"
	"errors"
	"io/fs"
	"path/filepath"
	"time"
)

func trashPath(t Trash) string { return filepath.Join("Trash", t.ID, t.Name) }
func (s *Service) trash(item Item, key string) (Trash, error) {
	var t Trash
	p, e := clean(item.Path)
	if e != nil || p == "." || p == "" || protected(p) {
		return t, ErrUnsafe
	}
	r, e := s.root(item.Source)
	if e != nil {
		return t, e
	}
	defer r.Close()
	if e = safe(r, p); e != nil {
		return t, e
	}
	if _, e = r.Stat(p); e != nil {
		return t, e
	}
	now := time.Now().Unix()
	t = Trash{ID: ID(), Source: item.Source, Original: p, Name: filepath.Base(p), Deleted: now, Expires: now + 10*24*3600, State: "trashing"}
	if e = s.insert(t.ID, "trash", t.State, key, t); e != nil {
		return t, e
	}
	if e = mkdir(r, filepath.Join("Trash", t.ID)); e != nil {
		return t, e
	}
	if e = r.Chmod("Trash", 0750); e != nil {
		return t, e
	}
	if e = r.Chmod(filepath.Join("Trash", t.ID), 0750); e != nil {
		return t, e
	}
	if e = renameExclusive(r, p, trashPath(t)); e != nil {
		return t, e
	}
	if e = syncDir(r, filepath.Dir(p)); e != nil {
		return t, e
	}
	if e = syncDir(r, filepath.Dir(trashPath(t))); e != nil {
		return t, e
	}
	t.State = "retained"
	e = s.save(t.ID, "trash", t.State, t)
	return t, e
}
func (s *Service) restore(id string) error {
	var t Trash
	if e := s.read(id, "trash", &t); e != nil {
		return e
	}
	if t.State != "retained" {
		return ErrConflict
	}
	r, e := s.root(t.Source)
	if e != nil {
		return e
	}
	defer r.Close()
	if e = safe(r, t.Original); e != nil {
		return e
	}
	if _, e = r.Lstat(t.Original); e == nil {
		return ErrConflict
	} else if !errors.Is(e, fs.ErrNotExist) {
		return e
	}
	if e = mkdir(r, filepath.Dir(t.Original)); e != nil {
		return e
	}
	t.State = "restoring"
	if e = s.save(t.ID, "trash", t.State, t); e != nil {
		return e
	}
	if e = renameExclusive(r, trashPath(t), t.Original); e != nil {
		t.State = "retained"
		t.Error = publicError(e)
		_ = s.save(t.ID, "trash", t.State, t)
		return e
	}
	if e = syncDir(r, filepath.Dir(t.Original)); e != nil {
		return e
	}
	t.State = "restored"
	if e = s.save(t.ID, "trash", t.State, t); e != nil {
		return e
	}
	return r.RemoveAll(filepath.Join("Trash", t.ID))
}
func (s *Service) purge(t *Trash) error {
	r, e := s.root(t.Source)
	if e != nil {
		return e
	}
	defer r.Close()
	if e = safe(r, filepath.Join("Trash", t.ID)); e != nil {
		return e
	}
	t.State = "purging"
	if e = s.save(t.ID, "trash", t.State, t); e != nil {
		return e
	}
	if e = r.RemoveAll(filepath.Join("Trash", t.ID)); e != nil {
		return e
	}
	if e = syncDir(r, "Trash"); e != nil {
		return e
	}
	t.State = "purged"
	return s.save(t.ID, "trash", t.State, t)
}
func (s *Service) reconcileTrash() {
	records, e := s.records("trash")
	if e != nil {
		return
	}
	for _, b := range records {
		var t Trash
		if json.Unmarshal(b, &t) != nil {
			continue
		}
		if t.State == "purged" || t.State == "restored" || t.State == "missing" {
			continue
		}
		r, e := s.root(t.Source)
		if e != nil {
			continue
		}
		if e = safe(r, trashPath(t)); e != nil {
			r.Close()
			continue
		}
		_, oldErr := r.Lstat(t.Original)
		_, trashErr := r.Lstat(trashPath(t))
		r.Close()
		switch t.State {
		case "trashing":
			if trashErr == nil {
				t.State = "retained"
			} else if errors.Is(trashErr, fs.ErrNotExist) && oldErr == nil {
				// Intent was saved but rename did not complete. Keep the original intact.
				t.State = "failed"
				t.Error = "Delete did not complete; original retained"
			} else {
				t.State = "missing"
			}
		case "restoring":
			if errors.Is(trashErr, fs.ErrNotExist) && oldErr == nil {
				t.State = "restored"
			} else if trashErr == nil {
				t.State = "retained"
			} else {
				t.State = "missing"
			}
		case "retained":
			if errors.Is(trashErr, fs.ErrNotExist) {
				t.State = "missing"
			}
		}
		if e = s.save(t.ID, "trash", t.State, t); e != nil {
			return
		}
		if t.State == "purging" || (t.State == "retained" && t.Expires <= time.Now().Unix()) {
			_ = s.purge(&t)
		}
	}
}
