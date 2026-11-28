package service

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

type publicationMetadataFailure struct {
	rootFS
	output string
}

func (r publicationMetadataFailure) Open(p string) (*os.File, error) {
	if p == r.output {
		return nil, os.ErrPermission
	}
	return r.rootFS.Open(p)
}

func TestArchivePublicationMetadataFailureKeepsOriginalVisible(t *testing.T) {
	for _, staged := range []bool{false, true} {
		name := "before-staging"
		if staged {
			name = "older-stranded-job"
		}
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			base := filepath.Join(archiveStage, ID())
			if err := os.MkdirAll(filepath.Join(dir, base), 0770); err != nil {
				t.Fatal(err)
			}
			raw := filepath.Join(base, "original")
			output := filepath.Join(base, "output.jpg")
			from := "photo.jpg"
			original := []byte("exact original bytes")
			originalPath := from
			if staged {
				originalPath = raw
			}
			if err := os.WriteFile(filepath.Join(dir, originalPath), original, 0660); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, output), []byte("encoded bytes"), 0660); err != nil {
				t.Fatal(err)
			}
			root, err := os.OpenRoot(dir)
			if err != nil {
				t.Fatal(err)
			}
			defer root.Close()
			st, _ := root.Stat(originalPath)
			j := archiveTask{Library: ArchiveEndpoint{Path: "."}, Relative: from, Output: from, Version: version(st), State: "publishing"}
			err = (&Service{}).publishArchiveEncoded(publicationMetadataFailure{root, output}, &j, base, raw)
			if !errors.Is(err, os.ErrPermission) {
				t.Fatal("expected metadata failure", err)
			}
			data, err := root.ReadFile(from)
			if err != nil || string(data) != string(original) {
				t.Fatal("original hidden or changed", err)
			}
			if _, err = root.Stat(raw); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("original still staged", err)
			}
			if _, err = root.Stat(output); err != nil {
				t.Fatal("encoded retry output lost", err)
			}
		})
	}
}

func TestArchivePublicationAfterMetadataRepair(t *testing.T) {
	s := testNode(t, "VPS")
	dir := s.config.Roots[0].Path
	base := filepath.Join(archiveStage, ID())
	os.MkdirAll(filepath.Join(dir, base), 0770)
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	raw := filepath.Join(base, "original")
	output := filepath.Join(base, "output.jpg")
	os.WriteFile(filepath.Join(dir, raw), []byte("original"), 0660)
	os.WriteFile(filepath.Join(dir, output), []byte("encoded"), 0660)
	stamp := time.Unix(1539447530, 123456789)
	os.Chtimes(filepath.Join(dir, raw), stamp, stamp)
	st, _ := root.Stat(raw)
	hash, _ := archiveHash(root, output)
	j := archiveTask{ID: ID(), Library: ArchiveEndpoint{Source: "VPS", Path: "."}, Relative: "photo.jpg", Output: "photo.jpg", Version: version(st), OutputHash: hash, State: "publishing"}
	if err = s.publishArchiveEncoded(root, &j, base, raw); err != nil {
		t.Fatal(err)
	}
	st, err = root.Stat("photo.jpg")
	if err != nil || !st.ModTime().Equal(stamp) || j.State != "backup" {
		t.Fatal("publication/date not recovered", err, j.State)
	}
	if data, err := root.ReadFile(raw); err != nil || string(data) != "original" {
		t.Fatal("original not retained", err)
	}
}
