package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
)

type MoveEntry struct {
	Path      string `json:"path"`
	Directory bool   `json:"directory"`
	Size      int64  `json:"size,omitempty"`
	Hash      string `json:"hash,omitempty"`
	Version   string `json:"version"`
	Identity  string `json:"identity"`
}
type MoveSelection struct {
	Rename             bool        `json:"rename,omitempty"`
	Renamed            bool        `json:"renamed,omitempty"`
	RenameIdentity     string      `json:"renameIdentity,omitempty"`
	DriveEntries       []File      `json:"driveEntries,omitempty"`
	DestinationEntries []MoveEntry `json:"destinationEntries,omitempty"`
	DestinationDrive   []File      `json:"destinationDrive,omitempty"`
	DestinationID      string      `json:"destinationId,omitempty"`
	Source             string      `json:"source"`
	Path               string      `json:"path"`
	To                 string      `json:"to"`
	Quarantine         string      `json:"quarantine"`
	Entries            []MoveEntry `json:"entries"`
	Removing           bool        `json:"removing,omitempty"`
	Removed            bool        `json:"removed,omitempty"`
}
type MoveState struct {
	Selections []MoveSelection `json:"selections"`
	Published  bool            `json:"published,omitempty"`
	Committed  bool            `json:"committed,omitempty"`
}

func validateMoveRequest(req CopyRequest) error {
	if req.Operation != "" && req.Operation != "copy" && req.Operation != "move" {
		return ErrUnsafe
	}
	if req.DestinationName != "" && (req.Operation != "move" || len(req.Items) != 1 || !validName(req.DestinationName) || protected(req.DestinationName)) {
		return ErrUnsafe
	}
	if req.Operation != "move" {
		return nil
	}
	if hasDrive(req) && req.DestinationName != "" {
		return ErrUnsafe
	}
	if req.Destination.Source == "" || req.Destination.Source == "upload" {
		return ErrUnsafe
	}
	for i, item := range req.Items {
		if item.Source == "drive" {
			if req.Destination.Source == "drive" || !driveID(item.Path) || item.Path == "root" {
				return ErrUnsafe
			}
			for _, other := range req.Items[:i] {
				if other.Source == "drive" && other.Path == item.Path {
					return ErrUnsafe
				}
			}
			continue
		}
		p, e := clean(item.Path)
		if e != nil || p == "." || protected(p) || item.Source == "drive" || item.Source == "upload" {
			return ErrUnsafe
		}
		for _, other := range req.Items[:i] {
			q, e := clean(other.Path)
			if e != nil || item.Source == other.Source && (within(p, q) || within(q, p)) {
				return ErrUnsafe
			}
		}
	}
	return nil
}

// Snapshot both bytes and identity. Destination comparisons deliberately ignore
// inode/mtime and directory sizes, which differ across filesystems.
func snapshotMove(ctx context.Context, root rootFS, base string) ([]MoveEntry, error) {
	return inventoryMove(ctx, root, base, true)
}

func inventoryMove(ctx context.Context, root rootFS, base string, hash bool) ([]MoveEntry, error) {
	if e := safe(root, base); e != nil {
		return nil, e
	}
	if guarded, ok := root.(*guardedRoot); ok {
		if e := guarded.tree(base); e != nil {
			return nil, e
		}
	}
	entries := []MoveEntry{}
	var walk func(string, string) error
	walk = func(path, relative string) error {
		if e := ctx.Err(); e != nil {
			return e
		}
		st, e := root.Lstat(path)
		if e != nil {
			return e
		}
		if !st.IsDir() && !st.Mode().IsRegular() {
			return ErrUnsafe
		}
		entry := MoveEntry{Path: relative, Directory: st.IsDir(), Version: version(st)}
		if stat, ok := st.Sys().(*syscall.Stat_t); ok {
			entry.Identity = fmt.Sprintf("%d:%d", stat.Dev, stat.Ino)
		}
		if st.IsDir() {
			entries = append(entries, entry)
			f, e := root.Open(path)
			if e != nil {
				return e
			}
			children, e := f.ReadDir(-1)
			closeErr := f.Close()
			if e != nil {
				return e
			}
			if closeErr != nil {
				return closeErr
			}
			sort.Slice(children, func(i, j int) bool { return children[i].Name() < children[j].Name() })
			for _, child := range children {
				if strings.EqualFold(child.Name(), thumbnailDirectory) {
					continue
				}
				if child.Name() == brokerStore || child.Name() == ".filebeans-stage" {
					return ErrUnsafe
				}
				if e = walk(filepath.Join(path, child.Name()), filepath.Join(relative, child.Name())); e != nil {
					return e
				}
			}
			return nil
		}
		entry.Size = st.Size()
		if !hash {
			entries = append(entries, entry)
			return nil
		}
		f, e := root.Open(path)
		if e != nil {
			return e
		}
		h := sha256.New()
		n, e := io.Copy(h, moveReader{ctx: ctx, reader: f})
		after, statErr := f.Stat()
		closeErr := f.Close()
		if e != nil {
			return e
		}
		if statErr != nil {
			return statErr
		}
		if closeErr != nil {
			return closeErr
		}
		if n != st.Size() || version(after) != entry.Version || !os.SameFile(st, after) {
			return ErrConflict
		}
		entry.Size = n
		entry.Hash = hex.EncodeToString(h.Sum(nil))
		entries = append(entries, entry)
		return nil
	}
	if e := walk(base, "."); e != nil {
		return nil, e
	}
	return entries, nil
}

type moveReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r moveReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(p)
}

// Content is verified against the destination once. Source revalidation uses
// identity, size and version rather than repeatedly reading all source bytes.
func sameMoveMetadata(expected, actual []MoveEntry, subset bool) bool {
	if !subset && len(expected) != len(actual) {
		return false
	}
	byPath := map[string]MoveEntry{}
	for _, entry := range expected {
		byPath[entry.Path] = entry
	}
	for _, entry := range actual {
		old, ok := byPath[entry.Path]
		if !ok || old.Directory != entry.Directory || old.Identity != entry.Identity || old.Size != entry.Size {
			return false
		}
		if (!subset || !entry.Directory) && old.Version != entry.Version {
			return false
		}
	}
	return true
}

func sameMoveData(expected, actual []MoveEntry, subset bool) bool {
	if !subset && len(expected) != len(actual) {
		return false
	}
	byPath := map[string]MoveEntry{}
	for _, e := range expected {
		byPath[e.Path] = e
	}
	for _, a := range actual {
		e, ok := byPath[a.Path]
		if !ok || e.Directory != a.Directory || !a.Directory && (e.Size != a.Size || e.Hash != a.Hash) {
			return false
		}
	}
	return true
}

func (s *Service) saveMove(j *Job) error {
	if j.Move == nil || !j.Move.Committed {
		return s.checkpoint(j)
	}
	// The verified move has committed. Cancellation can no longer turn it into
	// a copy or abandon source cleanup; a process restart resumes this journal.
	s.mu.Lock()
	defer s.mu.Unlock()
	var latest Job
	if e := s.read(j.ID, "copy", &latest); e != nil {
		return e
	}
	if latest.Cancel && (latest.Move == nil || !latest.Move.Committed) {
		j.Move.Committed = false
		return errCancelled
	}
	j.Cancel = false
	return s.save(j.ID, "copy", j.State, j)
}

func (s *Service) executeMove(ctx context.Context, j *Job) error {
	if e := validateMoveRequest(j.Request); e != nil {
		return e
	}
	if hasDrive(j.Request) {
		return s.executeDriveMove(ctx, j)
	}
	if j.Move != nil && j.Move.Committed {
		ctx = context.WithoutCancel(ctx)
	}
	dst, e := s.root(j.Request.Destination.Source)
	if e != nil {
		return e
	}
	defer dst.Close()
	destination, e := clean(j.Request.Destination.Path)
	if e != nil {
		return e
	}
	if j.Move == nil {
		state := &MoveState{}
		for i, item := range j.Request.Items {
			path, e := clean(item.Path)
			if e != nil {
				return e
			}
			name := filepath.Base(path)
			if j.Request.DestinationName != "" {
				name = j.Request.DestinationName
			}
			if item.Source == j.Request.Destination.Source && within(filepath.Join(destination, name), path) {
				return ErrUnsafe
			}
			src, e := s.root(item.Source)
			if e != nil {
				return e
			}
			selection := MoveSelection{Source: item.Source, Path: path, To: name}
			e = planRename(src, path, dst, filepath.Join(destination, name), &selection)
			var entries []MoveEntry
			if e == nil && !selection.Rename {
				entries, e = snapshotMove(ctx, src, path)
			}
			src.Close()
			if e != nil {
				return e
			}
			prefix := ""
			if v := s.vaults.pathVault(item.Source, path); v != nil {
				prefix = strings.TrimPrefix(v.Path, "/")
			}
			q := filepath.Join(prefix, ".filebeans-stage", j.ID, fmt.Sprintf("move-%d", i))
			selection.Quarantine, selection.Entries = q, entries
			for _, other := range state.Selections {
				if strings.EqualFold(other.To, name) {
					return ErrConflict
				}
			}
			state.Selections = append(state.Selections, selection)
		}
		j.Move = state
		if e = s.saveMove(j); e != nil {
			return e
		}
	}
	for i := range j.Move.Selections {
		selection := &j.Move.Selections[i]
		if !selection.Rename {
			continue
		}
		if e = ctx.Err(); e != nil {
			return e
		}
		if e = s.checkpoint(j); e != nil {
			return e
		}
		src, err := s.root(selection.Source)
		if err != nil {
			return err
		}
		err = resumeRename(src, dst, destination, selection)
		if errors.Is(err, errMoveCopyRequired) {
			selection.Rename = false
			selection.RenameIdentity = ""
			selection.Entries, err = snapshotMove(ctx, src, selection.Path)
		}
		src.Close()
		if err != nil {
			return err
		}
		if e = s.saveMove(j); e != nil {
			return e
		}
	}
	if !j.Move.Published {
		needsCopy := false
		for _, selection := range j.Move.Selections {
			needsCopy = needsCopy || !selection.Rename
		}
		if needsCopy {
			e = s.executeRclone(ctx, j)
		}
		if e != nil {
			return e
		}
		j.Move.Published = true
		if e = s.saveMove(j); e != nil {
			return e
		}
	}
	for _, selection := range j.Move.Selections {
		if selection.Rename {
			continue
		}
		actual, e := snapshotMove(ctx, dst, filepath.Join(destination, selection.To))
		if e != nil {
			return e
		}
		if !sameMoveData(selection.Entries, actual, false) {
			return ErrConflict
		}
		if e = syncMoveTree(dst, filepath.Join(destination, selection.To), actual); e != nil {
			return e
		}
	}
	if !j.Move.Committed {
		// Revalidate every original before committing removal of any selection.
		for _, selection := range j.Move.Selections {
			if selection.Rename {
				continue
			}
			src, e := s.root(selection.Source)
			if e != nil {
				return e
			}
			current, e := inventoryMove(ctx, src, selection.Path, false)
			src.Close()
			if e != nil {
				return e
			}
			if !sameMoveMetadata(selection.Entries, current, false) {
				return ErrConflict
			}
		}
		if e = s.checkpoint(j); e != nil {
			return e
		}
		j.Move.Committed = true
		if e = s.saveMove(j); e != nil {
			return e
		}
	}
	// Cleanup is a committed transaction, not a cancellable second operation.
	cleanupCtx := context.WithoutCancel(ctx)
	for i := range j.Move.Selections {
		selection := &j.Move.Selections[i]
		if selection.Rename {
			continue
		}
		if selection.Removed {
			continue
		}
		if e = s.finishMoveSelection(cleanupCtx, j, selection); e != nil {
			return e
		}
	}
	return nil
}

func (s *Service) finishMoveSelection(ctx context.Context, j *Job, m *MoveSelection) error {
	src, e := s.root(m.Source)
	if e != nil {
		return e
	}
	defer src.Close()
	// executeMove verified every destination in this attempt before committing.
	return removeMoveSource(ctx, src, m, func() error { return s.saveMove(j) })
}

func finishMoveSource(ctx context.Context, src, dst rootFS, destination string, m *MoveSelection, persist func() error) error {
	actual, e := snapshotMove(ctx, dst, filepath.Join(destination, m.To))
	if e != nil {
		return e
	}
	if !sameMoveData(m.Entries, actual, false) {
		return ErrConflict
	}
	return removeMoveSource(ctx, src, m, persist)
}

func removeMoveSource(ctx context.Context, src rootFS, m *MoveSelection, persist func() error) error {
	var e error
	_, quarantineErr := src.Lstat(m.Quarantine)
	if errors.Is(quarantineErr, os.ErrNotExist) && !m.Removing {
		current, e := inventoryMove(ctx, src, m.Path, false)
		if e != nil {
			return e
		}
		if !sameMoveMetadata(m.Entries, current, false) {
			return ErrConflict
		}
		if e = mkdir(src, filepath.Dir(m.Quarantine)); e != nil {
			return e
		}
		if e = renameExclusive(src, m.Path, m.Quarantine); e != nil {
			return e
		}
		if e = syncDir(src, filepath.Dir(m.Path)); e != nil {
			return e
		}
		if e = syncDir(src, filepath.Dir(m.Quarantine)); e != nil {
			return e
		}
	} else if quarantineErr != nil && !errors.Is(quarantineErr, os.ErrNotExist) {
		return quarantineErr
	}
	current, e := inventoryMove(ctx, src, m.Quarantine, false)
	if !errors.Is(e, os.ErrNotExist) || !m.Removing {
		if e != nil {
			return e
		}
		if !sameMoveMetadata(m.Entries, current, m.Removing) {
			// A source modified in the rename window must be put back, never
			// overwritten by the older published copy or silently discarded.
			if restore := renameExclusive(src, m.Quarantine, m.Path); restore != nil {
				return errors.New("Move recovery pending; original retained")
			}
			_ = syncDir(src, filepath.Dir(m.Path))
			return ErrConflict
		}
		m.Removing = true
		if e = persist(); e != nil {
			return e
		}
		if e = src.RemoveAll(m.Quarantine); e != nil {
			return errors.New("Move cleanup pending")
		}
	}
	if e = syncDir(src, filepath.Dir(m.Quarantine)); e != nil {
		return e
	}
	m.Removed = true
	if e = persist(); e != nil {
		return e
	}
	// Leave any other staging untouched; remove this now-empty directory only.
	_ = src.Remove(filepath.Dir(m.Quarantine))
	return nil
}

// Persist file contents and directory entries before committing source removal.
func syncMoveTree(root rootFS, base string, entries []MoveEntry) error {
	for i := len(entries) - 1; i >= 0; i-- {
		f, e := root.Open(filepath.Join(base, entries[i].Path))
		if e != nil {
			return e
		}
		e = f.Sync()
		ce := f.Close()
		if e != nil {
			return e
		}
		if ce != nil {
			return ce
		}
	}
	return syncDir(root, filepath.Dir(base))
}
