package service

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"golang.org/x/sys/unix"
	"golang.org/x/text/cases"
	"golang.org/x/text/unicode/norm"
)

// Serialize checks and publication across upload, restore, move and copy workers.
// External filesystem writers cannot participate in this application policy.
var filenamePublication sync.Mutex

func filenameKey(name string) string {
	ext := filepath.Ext(name)
	if ext != name {
		name = strings.TrimSuffix(name, ext)
	}
	return cases.Fold().String(norm.NFC.String(name))
}
func filenameConflict(root rootFS, target, exclude string, directory bool) error {
	if protected(target) || target == "." {
		return nil
	}
	parent, err := root.Open(filepath.Dir(target))
	if err != nil {
		return err
	}
	defer parent.Close()
	entries, err := parent.ReadDir(-1)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		p := filepath.Join(filepath.Dir(target), entry.Name())
		if p == exclude || filesystemHidden(p) {
			continue
		}
		// Directories keep their ordinary full-name rules. Files reserve their stem.
		if entry.Name() == filepath.Base(target) || (!directory && !entry.IsDir() && filenameKey(entry.Name()) == filenameKey(filepath.Base(target))) {
			return ErrConflict
		}
	}
	return nil
}

// Scan names only: linking never downloads file contents, even on Samba.
func validateFilenameTree(root rootFS, path string, datasetCaptions ...bool) error {
	info, err := root.Stat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return nil
	}
	return fs.WalkDir(root.FS(), path, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		// Staged folders still need validation; exclude only hidden descendants.
		relative, err := filepath.Rel(path, p)
		if err != nil {
			return err
		}
		if relative != "." && filesystemHidden(relative) {
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if d.Type()&os.ModeSymlink != 0 {
			return ErrUnsafe
		}
		if !d.IsDir() {
			return nil
		}
		dir, err := root.Open(p)
		if err != nil {
			return err
		}
		entries, err := dir.ReadDir(-1)
		dir.Close()
		if err != nil {
			return err
		}
		seen := map[string][]string{}
		for _, entry := range entries {
			if entry.IsDir() || filesystemHidden(entry.Name()) {
				continue
			}
			key := filenameKey(entry.Name())
			if len(seen[key]) > 0 && !(len(datasetCaptions) > 0 && datasetCaptions[0] && len(seen[key]) == 1 && datasetCaptionPair(seen[key][0], entry.Name())) {
				return ErrConflict
			}
			seen[key] = append(seen[key], entry.Name())
		}
		return nil
	})
}
func checkPublication(src rootFS, from string, dst rootFS, to string, datasetCaptions ...bool) error {
	if protected(to) {
		return nil
	}
	info, err := src.Stat(from)
	if err != nil {
		return err
	}
	exclude := ""
	if src.Name() == dst.Name() {
		exclude = from
	}
	if err = filenameConflict(dst, to, exclude, info.IsDir()); err != nil {
		return err
	}
	// Relocating an existing directory preserves its sibling sets. New copied
	// or generated trees arrive through hidden staging and must be validated.
	if info.IsDir() && filesystemHidden(from) {
		return validateFilenameTree(src, from, datasetCaptions...)
	}
	return nil
}
func filenameReplacement(root rootFS, path string) error {
	err := filenameConflict(root, path, path, false)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

// The normalizer runs in a separate process. A source-local sidecar also
// serializes its publication with the API; it contains no filenames or media.
func lockFilenamePublication(root rootFS) (func(), error) {
	filenamePublication.Lock()
	failed := func(err error) (func(), error) { filenamePublication.Unlock(); return nil, err }
	if err := mkdir(root, ".filebeans-stage"); err != nil {
		return failed(err)
	}
	file, err := root.OpenFile(".filebeans-stage/filename-policy.lock", os.O_CREATE|os.O_RDWR, 0660)
	if err != nil {
		return failed(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	for {
		err = unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB)
		if err == nil {
			cancel()
			return func() { unix.Flock(int(file.Fd()), unix.LOCK_UN); file.Close(); filenamePublication.Unlock() }, nil
		}
		if err != unix.EWOULDBLOCK && err != unix.EAGAIN {
			file.Close()
			cancel()
			return failed(err)
		}
		select {
		case <-ctx.Done():
			file.Close()
			cancel()
			return failed(ctx.Err())
		case <-time.After(25 * time.Millisecond):
		}
	}
}
func validateManifestFilenames(files []File) error {
	seen := map[string]bool{}
	for _, file := range files {
		if file.Directory {
			continue
		}
		key := filepath.Dir(file.To) + "/" + filenameKey(filepath.Base(file.To))
		if seen[key] {
			return ErrConflict
		}
		seen[key] = true
	}
	return nil
}

// Only the trusted dataset renderer opts in to this generation-time exception.
// It does not weaken upload/rename policy in the exported directory.
func datasetCaptionPair(a, b string) bool {
	x, y := strings.ToLower(filepath.Ext(a)), strings.ToLower(filepath.Ext(b))
	media := func(ext string) bool { return ext == ".jpg" || ext == ".mp4" }
	return x == ".txt" && media(y) || y == ".txt" && media(x)
}
