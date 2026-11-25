package service

import (
	"errors"
	"os"
	"path/filepath"
)

// Keep the derivative with its Trash payload. Folder caches already travel in
// the directory rename; individual files need their cached JPEG moved/rekeyed.
func (s *Service) archiveTrashCache(t Trash, retaining bool) error {
	if !s.archiveTrusted {
		links, e := s.archivePolicyLinks()
		if e != nil {
			return e
		}
		linked := false
		for _, link := range links {
			if link.Library.Source == t.Source {
				_, linked = archiveRelative(link.Library.Path, t.Original)
				if linked {
					break
				}
			}
		}
		if !linked {
			return nil
		}
	}
	a, e := s.accessTrash(t)
	if e != nil {
		return e
	}
	defer a.close()
	info, e := a.bin.Stat(trashPath(t))
	if errors.Is(e, os.ErrNotExist) {
		return nil
	}
	if e != nil {
		return e
	}
	if !info.Mode().IsRegular() {
		return nil
	}
	public := filepath.Join(filepath.Dir(a.original), thumbnailDirectory, previewCacheName(a.original, filesystemRevision(info)))
	private := filepath.Join(trashDirectory(t), thumbnailDirectory, previewCacheName(t.Name, filesystemRevision(info)))
	from, to := a.files, a.bin
	old, next := public, private
	if !retaining {
		from, to = a.bin, a.files
		old, next = private, public
	}
	if _, e = from.Stat(old); errors.Is(e, os.ErrNotExist) {
		return nil
	} else if e != nil {
		return e
	}
	if _, e = to.Stat(next); e == nil {
		return nil
	} else if !errors.Is(e, os.ErrNotExist) {
		return e
	}
	if e = mkdir(to, filepath.Dir(next)); e != nil {
		return e
	}
	if e = renameRoots(from, old, to, next); e != nil {
		return e
	}
	return syncDir(to, filepath.Dir(next))
}
