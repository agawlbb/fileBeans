package service

import (
	"errors"
	"filebeans/companion/protection"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

var errMoveCopyRequired = errors.New("move requires copying")

func moveIdentity(info os.FileInfo) string {
	if st, ok := info.Sys().(*syscall.Stat_t); ok {
		return fmt.Sprintf("%d:%d", st.Dev, st.Ino)
	}
	return ""
}

// Choose rename using metadata only. Protection boundaries still require a copy,
// even when the backing devices happen to match.
func planRename(src rootFS, path string, dst rootFS, target string, m *MoveSelection) error {
	for _, side := range []struct {
		root rootFS
		path string
	}{{src, path}, {dst, target}} {
		if err := safe(side.root, side.path); err != nil {
			return err
		}
		if guarded, ok := side.root.(*guardedRoot); ok {
			if err := guarded.tree(side.path); err != nil {
				return err
			}
		}
	}
	if _, err := dst.Lstat(target); err == nil {
		return ErrConflict
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	info, err := src.Lstat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() && !info.Mode().IsRegular() {
		return ErrUnsafe
	}
	parent, err := dst.Stat(filepath.Dir(target))
	if err != nil {
		return err
	}
	if !parent.IsDir() {
		return ErrUnsafe
	}
	if guarded, ok := src.(*guardedRoot); ok {
		if err := guarded.lease.Mutation(filepath.Join(src.Name(), path), filepath.Join(dst.Name(), target)); err != nil {
			if errors.Is(err, protection.ErrBoundary) {
				return nil
			}
			return protectionError(err)
		}
	}
	a, aOK := info.Sys().(*syscall.Stat_t)
	b, bOK := parent.Sys().(*syscall.Stat_t)
	if aOK && bOK && a.Dev == b.Dev {
		m.Rename = true
		m.RenameIdentity = moveIdentity(info)
	}
	return nil
}

// Persist intent before rename. Inode identity recovers a completed rename even
// if a new file has since appeared at the original pathname.
func resumeRename(src, dst rootFS, destination string, m *MoveSelection) error {
	target := filepath.Join(destination, m.To)
	if m.Renamed {
		return nil
	}
	if m.RenameIdentity == "" {
		return ErrConflict
	}
	if info, err := dst.Lstat(target); err == nil {
		if moveIdentity(info) != m.RenameIdentity {
			return ErrConflict
		}
		if original, err := src.Lstat(m.Path); err == nil && moveIdentity(original) == m.RenameIdentity {
			return ErrConflict
		} else if err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	} else {
		if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		info, err := src.Lstat(m.Path)
		if err != nil {
			return err
		}
		if moveIdentity(info) != m.RenameIdentity {
			return ErrConflict
		}
		check := MoveSelection{}
		if err = planRename(src, m.Path, dst, target, &check); err != nil {
			return err
		}
		if !check.Rename {
			return errMoveCopyRequired
		}
		if err = renameRoots(src, m.Path, dst, target); err != nil {
			if errors.Is(err, syscall.EXDEV) || errors.Is(err, syscall.EOPNOTSUPP) || errors.Is(err, syscall.ENOSYS) {
				return errMoveCopyRequired
			}
			return err
		}
	}
	if err := syncDir(dst, destination); err != nil {
		return err
	}
	if err := syncDir(src, filepath.Dir(m.Path)); err != nil {
		return err
	}
	m.Renamed = true
	m.Removed = true
	return nil
}
