package service

import (
	"errors"
	"filebeans/companion/protection"
	"golang.org/x/sys/unix"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// Private filesystem operations retain the same shared-group directory modes
// as ordinary guarded roots, including parents recreated during restore.
type sharedTrashRoot struct{ *os.Root }

func (r *sharedTrashRoot) MkdirAll(path string, mode os.FileMode) error {
	return r.Root.MkdirAll(path, (mode|0070)&^0007)
}

type trashAccess struct {
	files, bin rootFS
	original   string
	close      func()
}

func renameRoots(from rootFS, old string, to rootFS, next string) error {
	if e := safe(from, old); e != nil {
		return e
	}
	if e := safe(to, next); e != nil {
		return e
	}
	a, e := from.Open(filepath.Dir(old))
	if e != nil {
		return e
	}
	defer a.Close()
	b, e := to.Open(filepath.Dir(next))
	if e != nil {
		return e
	}
	defer b.Close()
	return unix.Renameat2(int(a.Fd()), filepath.Base(old), int(b.Fd()), filepath.Base(next), unix.RENAME_NOREPLACE)
}
func (s *Service) accessTrash(t Trash) (*trashAccess, error) {
	if t.Vault == "" {
		r, e := s.root(t.Source)
		if e != nil {
			return nil, e
		}
		return &trashAccess{files: r, bin: r, original: t.Original, close: func() { r.Close() }}, nil
	}
	var config *VaultConfig
	for _, v := range s.vaults.configs() {
		if v.ID == t.Vault && v.Source == t.Source {
			v := v
			config = &v
			break
		}
	}
	if config == nil || config.Path == "" {
		return nil, ErrVaultLocked
	}
	var lease *protection.Lease
	if s.vaults.guard != nil {
		var e error
		lease, e = s.vaults.guard.Acquire()
		if e != nil {
			return nil, protectionError(e)
		}
		var public string
		for _, root := range s.config.Roots {
			if root.Name == t.Source {
				public = filepath.Join(root.Path, t.Original)
			}
		}
		if e = lease.Check(public, false); e != nil {
			lease.Close()
			return nil, protectionError(e)
		}
	} else if e := s.vaults.checkRoot(t.Source); e != nil {
		return nil, e
	}
	release := func() {
		if lease != nil {
			lease.Close()
		}
	}
	original, e := clean(t.Original)
	if e != nil {
		release()
		return nil, e
	}
	prefix := strings.TrimPrefix(config.Path, "/")
	if !within(original, prefix) || original == prefix {
		release()
		return nil, ErrUnsafe
	}
	original = strings.TrimPrefix(original, prefix+"/")
	files, e := os.OpenRoot(filepath.Join(config.Mount, "files"))
	if e != nil {
		release()
		return nil, e
	}
	bin, e := os.OpenRoot(filepath.Join(config.Mount, "private"))
	if e != nil {
		files.Close()
		release()
		return nil, e
	}
	return &trashAccess{files: &sharedTrashRoot{files}, bin: &sharedTrashRoot{bin}, original: original, close: func() { files.Close(); bin.Close(); release() }}, nil
}
func retainTrashAccess(a *trashAccess, t Trash) error {
	if e := mkdir(a.bin, trashDirectory(t)); e != nil {
		return e
	}
	for _, p := range []string{filepath.Dir(trashDirectory(t)), trashDirectory(t)} {
		if e := accessibleDirectory(a.bin, p); e != nil {
			return e
		}
	}
	if e := renameRoots(a.files, a.original, a.bin, trashPath(t)); e != nil {
		return e
	}
	if e := syncDir(a.files, filepath.Dir(a.original)); e != nil {
		return e
	}
	return syncDir(a.bin, trashDirectory(t))
}

// Journal the new location first. A restart reconciles either side of the
// no-overwrite rename, and never removes a directory with untracked contents.
func (s *Service) migrateProtectedTrash(t *Trash) error {
	if t.Vault == "" {
		if t.State != "retained" {
			return nil
		}
		v := s.vaults.pathVault(t.Source, t.Original)
		if v == nil || v.Path == "" || t.Storage == "" {
			return nil
		}
		expected := filepath.Join(strings.TrimPrefix(v.Path, "/"), "Trash", t.ID)
		if t.Storage != expected {
			return ErrUnsafe
		}
		t.Vault = v.ID
		t.LegacyStorage = t.Storage
		t.Storage = filepath.Join("trash", t.ID)
		if e := s.save(t.ID, "trash", t.State, t); e != nil {
			return e
		}
	}
	if t.LegacyStorage == "" {
		return nil
	}
	a, e := s.accessTrash(*t)
	if e != nil {
		return e
	}
	defer a.close()
	old := filepath.Join("Trash", t.ID)
	if e = mkdir(a.bin, "trash"); e != nil {
		return e
	}
	if e = accessibleDirectory(a.bin, "trash"); e != nil {
		return e
	}
	_, oldErr := a.files.Lstat(old)
	_, newErr := a.bin.Lstat(t.Storage)
	switch {
	case oldErr == nil && errors.Is(newErr, os.ErrNotExist):
		if e = renameRoots(a.files, old, a.bin, t.Storage); e != nil {
			return e
		}
	case errors.Is(oldErr, os.ErrNotExist) && newErr == nil:
	case oldErr != nil:
		return oldErr
	default:
		return ErrConflict
	}
	if e = syncDir(a.files, "Trash"); e != nil {
		return e
	}
	if e = syncDir(a.bin, "trash"); e != nil {
		return e
	}
	t.LegacyStorage = ""
	if e = s.save(t.ID, "trash", t.State, t); e != nil {
		return e
	}
	_ = removeEmptyTrashRoot(a.files)
	return nil
}

// Remove only an empty directory, never retained files. ReadDir checks intent;
// os.Root.Remove uses unlink/rmdir and atomically refuses a nonempty directory.
func removeEmptyTrashRoot(root rootFS) error { return removeEmptyDirectory(root, "Trash") }

func removeEmptyDirectory(root rootFS, path string) error {
	if e := safe(root, path); e != nil {
		return e
	}
	st, e := root.Lstat(path)
	if errors.Is(e, os.ErrNotExist) {
		return nil
	}
	if e != nil {
		return e
	}
	if !st.IsDir() || st.Mode()&os.ModeSymlink != 0 {
		return ErrUnsafe
	}
	dir, e := root.Open(path)
	if e != nil {
		return e
	}
	entries, readErr := dir.ReadDir(1)
	closeErr := dir.Close()
	if len(entries) != 0 {
		return ErrConflict
	}
	if readErr != nil && !errors.Is(readErr, io.EOF) {
		return readErr
	}
	if closeErr != nil {
		return closeErr
	}
	return root.Remove(path)
}

// Only recorded legacy IDs are eligible, and only when their directories are
// empty. Unexpected files and all retained entries are left for migration.
func (s *Service) cleanupFinishedTrash(t Trash) {
	if t.State != "failed" && t.State != "restored" && t.State != "purged" && t.State != "missing" {
		return
	}
	if t.Vault != "" {
		a, e := s.accessTrash(t)
		if e == nil {
			_ = removeEmptyDirectory(a.bin, trashDirectory(t))
			a.close()
		}
		return
	}
	v := s.vaults.pathVault(t.Source, t.Original)
	if v == nil || v.Path == "" || t.Storage != filepath.Join(strings.TrimPrefix(v.Path, "/"), "Trash", t.ID) {
		return
	}
	descriptor := t
	descriptor.Vault = v.ID
	a, e := s.accessTrash(descriptor)
	if e != nil {
		return
	}
	defer a.close()
	_ = removeEmptyDirectory(a.files, filepath.Join("Trash", t.ID))
}
