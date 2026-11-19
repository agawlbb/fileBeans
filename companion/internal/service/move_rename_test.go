package service

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

type failMoveSyncRoot struct {
	rootFS
	opens int
}

func (r *failMoveSyncRoot) Open(path string) (*os.File, error) {
	r.opens++
	if r.opens == 2 {
		return nil, syscall.EOPNOTSUPP
	}
	return r.rootFS.Open(path)
}

func TestMoveRenameSyncFailureKeepsIntent(t *testing.T) {
	r, err := os.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	r.MkdirAll("destination", 0700)
	r.WriteFile("file", []byte("content"), 0600)
	m := MoveSelection{Path: "file", To: "file"}
	if err = planRename(r, "file", r, "destination/file", &m); err != nil {
		t.Fatal(err)
	}
	dst := &failMoveSyncRoot{rootFS: r}
	err = resumeRename(r, dst, "destination", &m)
	if !errors.Is(err, syscall.EOPNOTSUPP) || errors.Is(err, errMoveCopyRequired) {
		t.Fatal("sync failure must retain rename intent", err)
	}
	if err = resumeRename(r, r, "destination", &m); err != nil || !m.Renamed {
		t.Fatal("rename recovery failed", err)
	}
}

func TestMoveRenameAndRecovery(t *testing.T) {
	for _, phase := range []string{"fresh", "renamed-before-checkpoint", "replacement-source", "conflicting-target"} {
		t.Run(phase, func(t *testing.T) {
			root, err := os.OpenRoot(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer root.Close()
			if err = root.MkdirAll("destination", 0700); err != nil {
				t.Fatal(err)
			}
			// A large sparse file makes accidental content scanning expensive.
			file, err := root.OpenFile("large", os.O_CREATE|os.O_WRONLY, 0600)
			if err != nil {
				t.Fatal(err)
			}
			if err = file.Truncate(1 << 40); err != nil {
				t.Fatal(err)
			}
			file.Close()
			before, _ := root.Stat("large")
			m := MoveSelection{Path: "large", To: "large"}
			if err = planRename(root, "large", root, "destination/large", &m); err != nil || !m.Rename {
				t.Fatal(err, m)
			}
			if phase == "renamed-before-checkpoint" || phase == "replacement-source" {
				if err = renameRoots(root, "large", root, "destination/large"); err != nil {
					t.Fatal(err)
				}
			}
			if phase == "replacement-source" {
				root.WriteFile("large", []byte("new occupant"), 0600)
			}
			if phase == "conflicting-target" {
				root.WriteFile("destination/large", []byte("keep"), 0600)
			}
			err = resumeRename(root, root, "destination", &m)
			if phase == "conflicting-target" {
				if !errors.Is(err, ErrConflict) {
					t.Fatal(err)
				}
				if data, _ := root.ReadFile("destination/large"); string(data) != "keep" {
					t.Fatal("overwrote target")
				}
				if _, err = root.Stat("large"); err != nil {
					t.Fatal("lost source", err)
				}
				return
			}
			if err != nil || !m.Renamed {
				t.Fatal(err)
			}
			after, err := root.Stat("destination/large")
			if err != nil || !os.SameFile(before, after) {
				t.Fatal("move copied file", err)
			}
			if phase == "replacement-source" {
				if data, _ := root.ReadFile("large"); string(data) != "new occupant" {
					t.Fatal("replacement source lost")
				}
			} else if _, err = root.Stat("large"); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("source retained", err)
			}
		})
	}
}

func TestMoveCrossFilesystemPlan(t *testing.T) {
	path, err := os.MkdirTemp("/dev/shm", "filebeans-move-")
	if err != nil {
		t.Skip("second filesystem unavailable")
	}
	defer os.RemoveAll(path)
	src, err := os.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()
	dst, err := os.OpenRoot(path)
	if err != nil {
		t.Fatal(err)
	}
	defer dst.Close()
	src.WriteFile("file", []byte("content"), 0600)
	m := MoveSelection{}
	if err = planRename(src, "file", dst, "file", &m); err != nil {
		t.Fatal(err)
	}
	if m.Rename {
		t.Fatal("cross-filesystem rename planned")
	}
}

func TestMoveSourceMetadataRevalidation(t *testing.T) {
	r, err := os.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	r.MkdirAll("folder", 0700)
	r.WriteFile("folder/file", []byte("content"), 0600)
	expected, err := snapshotMove(context.Background(), r, "folder")
	if err != nil {
		t.Fatal(err)
	}
	if err = renameRoots(r, "folder", r, "quarantine"); err != nil {
		t.Fatal(err)
	}
	current, err := inventoryMove(context.Background(), r, "quarantine", false)
	if err != nil || !sameMoveMetadata(expected, current, false) {
		t.Fatal("rename changed metadata", err)
	}
	r.WriteFile(filepath.Join("quarantine", "file"), []byte("edited content"), 0600)
	current, err = inventoryMove(context.Background(), r, "quarantine", false)
	if err != nil || sameMoveMetadata(expected, current, false) {
		t.Fatal("source edit missed", err)
	}
}
