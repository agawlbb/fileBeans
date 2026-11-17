package service

import (
	"bufio"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

func clean(path string) (string, error) {
	if strings.ContainsRune(path, 0) || strings.Contains(path, "\\") {
		return "", ErrUnsafe
	}
	parts := strings.Split(path, "/")
	for _, p := range parts {
		if p == ".." {
			return "", ErrUnsafe
		}
	}
	p := strings.TrimPrefix(filepath.Clean("/"+path), "/")
	if p == "" {
		p = "."
	}
	return p, nil
}
func validName(name string) bool {
	return name != "" && name != "." && name != ".." && !strings.ContainsAny(name, "/\\\x00")
}
func (s *Service) root(name string) (rootFS, error) {
	if s.vaults != nil {
		if e := s.vaults.checkRoot(name); e != nil {
			return nil, e
		}
	}
	if name == "upload" {
		return os.OpenRoot(filepath.Join(s.config.State, "uploads"))
	}
	var config *Root
	for i := range s.config.Roots {
		if s.config.Roots[i].Name == name {
			config = &s.config.Roots[i]
			break
		}
	}
	if config == nil {
		return nil, ErrUnsafe
	}
	if config.Mount != "" {
		f, e := os.Open("/proc/self/mountinfo")
		if e != nil {
			return nil, e
		}
		defer f.Close()
		found := false
		scan := bufio.NewScanner(f)
		decode := func(v string) string {
			r := strings.NewReplacer("\\040", " ", "\\011", "\t", "\\134", "\\")
			return r.Replace(v)
		}
		for scan.Scan() {
			p := strings.Split(scan.Text(), " - ")
			if len(p) != 2 {
				continue
			}
			a, b := strings.Fields(p[0]), strings.Fields(p[1])
			if len(a) > 4 && len(b) > 1 && decode(a[4]) == config.Mount && decode(b[1]) == config.MountSource {
				found = true
			}
		}
		if e = scan.Err(); e != nil {
			return nil, e
		}
		if !found || !within(config.Path, config.Mount) {
			return nil, ErrMount
		}
	}
	info, e := os.Lstat(config.Path)
	if e != nil {
		return nil, e
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return nil, ErrUnsafe
	}
	root, e := os.OpenRoot(config.Path)
	if e != nil {
		return nil, e
	}
	return s.guarded(root)
}

// os.Root confines operations even if an intermediate directory is changed concurrently.
// Reject symlinks as well; they are not copyable objects in this service.
func safe(r rootFS, path string) error {
	p, e := clean(path)
	if e != nil {
		return e
	}
	if p == "" || p == "." {
		return nil
	}
	current := ""
	for _, part := range strings.Split(p, "/") {
		current = filepath.Join(current, part)
		st, e := r.Lstat(current)
		if errors.Is(e, fs.ErrNotExist) {
			return nil
		}
		if e != nil {
			return e
		}
		if st.Mode()&os.ModeSymlink != 0 {
			return ErrUnsafe
		}
	}
	return nil
}
func protected(path string) bool {
	p, _ := clean(path)
	return p == "Trash" || strings.HasPrefix(p, "Trash/") || p == ".filebeans-stage" || strings.HasPrefix(p, ".filebeans-stage/")
}
func version(st fs.FileInfo) string { return fmt.Sprintf("%d:%d", st.Size(), st.ModTime().UnixNano()) }
func mkdir(r rootFS, path string) error {
	if e := safe(r, path); e != nil {
		return e
	}
	return r.MkdirAll(path, 0700)
}
func syncDir(r rootFS, path string) error {
	f, e := r.Open(path)
	if e != nil {
		return e
	}
	defer f.Close()
	return f.Sync()
}
func renameExclusive(r rootFS, from, to string) error {
	if guarded, ok := r.(*guardedRoot); ok {
		if e := guarded.mutation(from, to); e != nil {
			return e
		}
	}
	if e := safe(r, from); e != nil {
		return e
	}
	if e := safe(r, to); e != nil {
		return e
	}
	return renameNoReplace(r, from, to)
}
