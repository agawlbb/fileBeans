package service

import (
	"filebeans/companion/protection"
	"io/fs"
	"os"
	"path/filepath"
)

// rootFS also accepts an ordinary os.Root for existing callers and tests.
type rootFS interface {
	Name() string
	Close() error
	Open(string) (*os.File, error)
	OpenFile(string, int, os.FileMode) (*os.File, error)
	Stat(string) (os.FileInfo, error)
	Lstat(string) (os.FileInfo, error)
	ReadFile(string) ([]byte, error)
	WriteFile(string, []byte, os.FileMode) error
	MkdirAll(string, os.FileMode) error
	Chmod(string, os.FileMode) error
	Remove(string) error
	RemoveAll(string) error
	FS() fs.FS
}

type guardedRoot struct {
	*os.Root
	lease *protection.Lease
}

func protectionError(e error) error {
	switch e {
	case nil:
		return nil
	case protection.ErrLocked:
		return ErrVaultLocked
	case protection.ErrBusy:
		return ErrVaultBusy
	case protection.ErrBoundary, protection.ErrHidden:
		return ErrUnsafe
	default:
		return errVault
	}
}

func (r *guardedRoot) path(name string) (string, error) {
	p, e := clean(name)
	if e != nil {
		return "", e
	}
	return filepath.Join(r.Name(), p), nil
}
func (r *guardedRoot) check(name string) error {
	p, e := r.path(name)
	if e != nil {
		return e
	}
	return protectionError(r.lease.Check(p, false))
}
func (r *guardedRoot) mutation(from, to string) error {
	a, e := r.path(from)
	if e != nil {
		return e
	}
	b := ""
	if to != "" {
		b, e = r.path(to)
		if e != nil {
			return e
		}
	}
	return protectionError(r.lease.Mutation(a, b))
}
func (r *guardedRoot) tree(name string) error {
	return r.mutation(name, "")
}
func (r *guardedRoot) Close() error {
	e := r.Root.Close()
	_ = r.lease.Close()
	return e
}
func (r *guardedRoot) Open(name string) (*os.File, error) {
	if e := r.check(name); e != nil {
		return nil, e
	}
	return r.Root.Open(name)
}
func (r *guardedRoot) OpenFile(name string, flag int, mode os.FileMode) (*os.File, error) {
	if e := r.check(name); e != nil {
		return nil, e
	}
	if flag&(os.O_WRONLY|os.O_RDWR|os.O_CREATE|os.O_TRUNC|os.O_APPEND) != 0 {
		if e := r.mutation(name, ""); e != nil {
			return nil, e
		}
	}
	return r.Root.OpenFile(name, flag, mode)
}
func (r *guardedRoot) Stat(name string) (os.FileInfo, error) {
	if e := r.check(name); e != nil {
		return nil, e
	}
	return r.Root.Stat(name)
}
func (r *guardedRoot) Lstat(name string) (os.FileInfo, error) {
	if e := r.check(name); e != nil {
		return nil, e
	}
	return r.Root.Lstat(name)
}
func (r *guardedRoot) ReadFile(name string) ([]byte, error) {
	if e := r.check(name); e != nil {
		return nil, e
	}
	return r.Root.ReadFile(name)
}
func (r *guardedRoot) WriteFile(name string, data []byte, mode os.FileMode) error {
	if e := r.mutation(name, ""); e != nil {
		return e
	}
	return r.Root.WriteFile(name, data, mode)
}
func (r *guardedRoot) MkdirAll(name string, mode os.FileMode) error {
	if e := r.check(name); e != nil {
		return e
	}
	return r.Root.MkdirAll(name, mode)
}
func (r *guardedRoot) Chmod(name string, mode os.FileMode) error {
	if e := r.mutation(name, ""); e != nil {
		return e
	}
	return r.Root.Chmod(name, mode)
}
func (r *guardedRoot) Remove(name string) error {
	if e := r.mutation(name, ""); e != nil {
		return e
	}
	return r.Root.Remove(name)
}
func (r *guardedRoot) RemoveAll(name string) error {
	if e := r.mutation(name, ""); e != nil {
		return e
	}
	return r.Root.RemoveAll(name)
}
func (r *guardedRoot) FS() fs.FS { return guardedFS{r} }

type guardedFS struct{ r *guardedRoot }

func (f guardedFS) Open(name string) (fs.File, error) { return f.r.Open(name) }
func (f guardedFS) ReadDir(name string) ([]fs.DirEntry, error) {
	d, e := f.r.Open(name)
	if e != nil {
		return nil, e
	}
	defer d.Close()
	entries, e := d.ReadDir(-1)
	if e != nil {
		return nil, e
	}
	visible := entries[:0]
	for _, entry := range entries {
		if entry.Name() == brokerStore || entry.Name() == ".filebeans-stage" {
			continue
		}
		// The scanner callback decides whether to skip a protected directory;
		// retaining its entry lets it distinguish a boundary from an empty tree.
		visible = append(visible, entry)
	}
	return visible, nil
}

func (s *Service) guarded(root *os.Root) (rootFS, error) {
	if s.vaults == nil || s.vaults.guard == nil {
		return root, nil
	}
	lease, e := s.vaults.guard.Acquire()
	if e != nil {
		root.Close()
		return nil, protectionError(e)
	}
	return &guardedRoot{Root: root, lease: lease}, nil
}
