package service

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"path/filepath"
	"strings"
	"time"
)

func trashDirectory(t Trash) string {
	if t.Storage != "" {
		return t.Storage
	}
	return filepath.Join("Trash", t.ID)
}
func trashPath(t Trash) string { return filepath.Join(trashDirectory(t), t.Name) }
func (s *Service) trash(item Item, key string) (Trash, error) {
	var t Trash
	if !s.archiveTrusted {
		archiveMu.Lock()
		defer archiveMu.Unlock()
	}
	if e := s.guardArchiveRemoval(item.Source, item.Path); e != nil {
		return t, e
	}
	if s.vaults != nil && s.vaults.remote != nil {
		for _, v := range s.vaults.status() {
			if v.Source == item.Source && v.Trashed == 0 && filepath.Clean(v.Path) == filepath.Clean("/"+strings.TrimPrefix(item.Path, "/")) {
				if err := s.vaults.remote.folderAction(v.ID, "trash", ""); err != nil {
					return t, err
				}
				v.Trashed = time.Now().Unix()
				v.OriginalPath = v.Path
				return vaultTrash(v), nil
			}
		}
	}
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
	if guarded, ok := r.(*guardedRoot); ok {
		if e = guarded.tree(p); e != nil {
			return t, e
		}
	}
	if _, e = r.Stat(p); e != nil {
		return t, e
	}
	now := time.Now().Unix()
	t = Trash{ID: ID(), Source: item.Source, Original: p, Name: filepath.Base(p), Deleted: now, Expires: now + 10*24*3600, State: "trashing"}
	if v := s.vaults.pathVault(item.Source, p); v != nil && v.Path != "" {
		t.Vault = v.ID
		t.Storage = filepath.Join("trash", t.ID)
	}
	if e = s.insert(t.ID, "trash", t.State, key, t); e != nil {
		return t, e
	}
	if e = s.archiveTrashIntent(t); e != nil {
		return t, e
	}
	a, e := s.accessTrash(t)
	if e != nil {
		return t, e
	}
	defer a.close()
	if e = retainTrashAccess(a, t); e != nil {
		return t, e
	}
	t.State = "retained"
	e = s.save(t.ID, "trash", t.State, t)
	if e == nil {
		e = s.archiveTrashCache(t, true)
	}
	return t, e
}
func (s *Service) restore(id string) error {
	if !s.archiveTrusted {
		archiveMu.Lock()
		defer archiveMu.Unlock()
	}
	if strings.HasPrefix(id, "vault-") && s.vaults != nil && s.vaults.remote != nil {
		if e := s.guardArchiveVaultWrite(strings.TrimPrefix(id, "vault-")); e != nil {
			return e
		}
		return s.vaults.remote.folderAction(strings.TrimPrefix(id, "vault-"), "restore", "")
	}
	var t Trash
	if e := s.read(id, "trash", &t); e != nil {
		return e
	}
	if e := s.guardArchiveTrash(t); e != nil {
		return e
	}
	if t.State != "retained" {
		return ErrConflict
	}
	if e := s.migrateProtectedTrash(&t); e != nil {
		return e
	}
	a, e := s.accessTrash(t)
	if e != nil {
		return e
	}
	defer a.close()
	r := a.files
	original := a.original
	if e = safe(r, original); e != nil {
		return e
	}
	if _, e = r.Lstat(original); e == nil {
		return ErrConflict
	} else if !errors.Is(e, fs.ErrNotExist) {
		return e
	}
	if e = mkdir(r, filepath.Dir(original)); e != nil {
		return e
	}
	if e = s.archiveTrashCache(t, false); e != nil {
		return e
	}
	t.State = "restoring"
	if e = s.save(t.ID, "trash", t.State, t); e != nil {
		return e
	}
	if e = renameRoots(a.bin, trashPath(t), r, original); e != nil {
		t.State = "retained"
		t.Error = publicError(e)
		_ = s.save(t.ID, "trash", t.State, t)
		return e
	}
	if e = syncDir(r, filepath.Dir(original)); e != nil {
		return e
	}
	t.State = "restored"
	if e = s.save(t.ID, "trash", t.State, t); e != nil {
		return e
	}
	if e = s.archiveTrashState(t.ID, "restore"); e != nil {
		return e
	}
	return a.bin.RemoveAll(trashDirectory(t))
}
func (s *Service) purge(t *Trash) error {
	if !s.archiveTrusted {
		archiveMu.Lock()
		defer archiveMu.Unlock()
	}
	if e := s.guardArchivePurge(*t); e != nil {
		return e
	}
	if e := s.guardArchiveTrash(*t); e != nil {
		return e
	}
	if strings.HasPrefix(t.ID, "vault-") && s.vaults != nil && s.vaults.remote != nil {
		return s.vaults.remote.folderAction(strings.TrimPrefix(t.ID, "vault-"), "purge", "")
	}
	if e := s.migrateProtectedTrash(t); e != nil {
		return e
	}
	a, e := s.accessTrash(*t)
	if e != nil {
		return e
	}
	defer a.close()
	r := a.bin
	if e = safe(r, trashDirectory(*t)); e != nil {
		return e
	}
	t.State = "purging"
	if e = s.save(t.ID, "trash", t.State, t); e != nil {
		return e
	}
	if e = r.RemoveAll(trashDirectory(*t)); e != nil {
		return e
	}
	if e = syncDir(r, filepath.Dir(trashDirectory(*t))); e != nil {
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
		s.cleanupFinishedTrash(t)
		if t.State == "purged" || t.State == "restored" || t.State == "missing" {
			continue
		}
		if e := s.migrateProtectedTrash(&t); e != nil {
			continue
		}
		a, e := s.accessTrash(t)
		if e != nil {
			continue
		}
		if e = safe(a.bin, trashPath(t)); e != nil {
			a.close()
			continue
		}
		_, oldErr := a.files.Lstat(a.original)
		_, trashErr := a.bin.Lstat(trashPath(t))
		a.close()
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
	for _, v := range s.vaults.configs() {
		if v.Path == "" {
			continue
		}
		descriptor := Trash{Source: v.Source, Vault: v.ID, Original: filepath.Join(v.Path, ".trash-cleanup")}
		a, e := s.accessTrash(descriptor)
		if e != nil {
			continue
		}
		_ = removeEmptyTrashRoot(a.files)
		a.close()
	}

}

// retainTrashFile is the filesystem half of the journaled delete operation.
// The caller persists intent first and retention only after durable publication.
func retainTrashFile(r rootFS, t Trash) error {
	if e := mkdir(r, trashDirectory(t)); e != nil {
		return e
	}
	if e := accessibleDirectory(r, filepath.Dir(trashDirectory(t))); e != nil {
		return e
	}
	if e := accessibleDirectory(r, trashDirectory(t)); e != nil {
		return e
	}
	if e := renameExclusive(r, t.Original, trashPath(t)); e != nil {
		return e
	}
	if e := syncDir(r, filepath.Dir(t.Original)); e != nil {
		return e
	}
	if e := syncDir(r, filepath.Dir(trashPath(t))); e != nil {
		return e
	}
	return nil
}

// Listing is a read operation. Filesystem recovery/expiration runs in
// maintenance rather than blocking every mobile poll on remote storage.
func (s *Service) retainedTrash(source string) ([][]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	query := "SELECT id,body FROM fb_operations WHERE kind='trash' AND state IN ('retained','purging') ORDER BY updated_at DESC"
	rows, err := s.db.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out [][]byte
	for rows.Next() {
		var id string
		var data []byte
		if err = rows.Scan(&id, &data); err != nil {
			return nil, err
		}
		data, err = s.decodeRecord(id, "trash", data)
		if errors.Is(err, ErrVaultLocked) {
			continue
		}
		if err != nil {
			return nil, err
		}
		var entry Trash
		if err = json.Unmarshal(data, &entry); err != nil {
			return nil, err
		}
		if source == "" || entry.Source == source {
			out = append(out, data)
		}
	}
	return out, rows.Err()
}
