package service

import (
	"path/filepath"

	"golang.org/x/sys/unix"
)

func renameNoReplace(r rootFS, from, to string, datasetCaptions ...bool) error {
	unlock, err := lockFilenamePublication(r)
	if err != nil {
		return err
	}
	defer unlock()
	if e := checkPublication(r, from, r, to, datasetCaptions...); e != nil {
		return e
	}
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
