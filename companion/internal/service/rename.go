package service

import (
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

func renameNoReplace(r *os.Root, from, to string) error {
	// Resolve both parent descriptors through os.Root. Passing only basenames to
	// renameat2 prevents intermediate-path substitution escaping root confinement.
	a, e := r.Open(filepath.Dir(from))
	if e != nil {
		return e
	}
	defer a.Close()
	b, e := r.Open(filepath.Dir(to))
	if e != nil {
		return e
	}
	defer b.Close()
	return unix.Renameat2(int(a.Fd()), filepath.Base(from), int(b.Fd()), filepath.Base(to), unix.RENAME_NOREPLACE)
}
