package service

import (
	"os"
	"path/filepath"
)

// Endpoint validation never walks an unbounded Pi directory tree. Encoding is
// deliberately absent from this metadata-only operation.
func validateArchivePeerTree(root rootFS, path string) error {
	remaining := piManifestLimit
	var walk func(string, int) error
	walk = func(p string, depth int) error {
		if depth > 64 || remaining <= 0 {
			return ErrUnsafe
		}
		if e := safe(root, p); e != nil {
			return e
		}
		f, e := root.Open(p)
		if e != nil {
			return e
		}
		entries, e := boundedReadDir(f, remaining)
		f.Close()
		if e != nil {
			return e
		}
		remaining -= len(entries)
		seen := map[string]bool{}
		for _, entry := range entries {
			if filesystemHidden(entry.Name()) {
				continue
			}
			if entry.Type()&os.ModeSymlink != 0 {
				return ErrUnsafe
			}
			if entry.IsDir() {
				if e = walk(filepath.Join(p, entry.Name()), depth+1); e != nil {
					return e
				}
				continue
			}
			key := filenameKey(entry.Name())
			if seen[key] {
				return ErrConflict
			}
			seen[key] = true
		}
		return nil
	}
	return walk(path, 0)
}
