package service

import (
	"context"
	"io"
	"os"
	"path/filepath"
)

// Compare names only. Encoded files deliberately differ in bytes and extension.
// Extra archive names are never interpreted as permission to delete originals.
func archiveNames(ctx context.Context, root rootFS, base string) (map[string]bool, error) {
	if p, ok := root.(*archivePeerRoot); ok {
		var out map[string]bool
		e := p.callContext(ctx, archivePeerRequest{Operation: "names", Path: base}, &out)
		return out, e
	}
	var err error
	base, err = clean(base)
	if err != nil {
		return nil, err
	}
	names := map[string]bool{}
	remaining := 100000
	if ctx.Value(peerInventoryKey{}) != nil {
		remaining = piManifestLimit
	}
	var walk func(string, int) error
	walk = func(rel string, depth int) error {
		if e := ctx.Err(); e != nil {
			return e
		}
		if remaining <= 0 || depth > 64 {
			return ErrUnsafe
		}
		path := filepath.Join(base, rel)
		if e := safe(root, path); e != nil {
			return e
		}
		f, e := root.Open(path)
		if e != nil {
			return e
		}
		entries, e := f.ReadDir(remaining + 1)
		f.Close()
		if e != nil && e != io.EOF {
			return e
		}
		for _, entry := range entries {
			remaining--
			if remaining <= 0 {
				return ErrUnsafe
			}
			cp := filepath.Join(rel, entry.Name())
			if filesystemHidden(cp) {
				continue
			}
			if entry.Type()&os.ModeSymlink != 0 {
				return ErrUnsafe
			}
			if entry.IsDir() {
				if e = walk(cp, depth+1); e != nil {
					return e
				}
			} else {
				names[filepath.Join(filepath.Dir(cp), archiveStem(entry.Name()))] = true
			}
		}
		return nil
	}
	e := walk(".", 0)
	return names, e
}
func archiveDrift(ctx context.Context, library rootFS, base string, archive rootFS, other string) (bool, error) {
	left, e := archiveNames(ctx, library, base)
	if e != nil {
		return false, e
	}
	right, e := archiveNames(ctx, archive, other)
	if e != nil {
		return false, e
	}
	for name := range right {
		if !left[name] {
			return true, nil
		}
	}
	return false, nil
}
