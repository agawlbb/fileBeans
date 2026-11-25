package service

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestThumbnailBackfillLocalNestedAndCached(t *testing.T) {
	s, base := filesystemFixture(t)
	s.config.Roots[0].Name = "VPS"
	for _, directory := range []string{"nested/deep", "Trash", thumbnailDirectory} {
		if err := os.MkdirAll(filepath.Join(base, directory), 0700); err != nil {
			t.Fatal(err)
		}
	}
	for _, path := range []string{"nested/deep/readme.txt", "Trash/ignored.txt", thumbnailDirectory + "/ignored.txt"} {
		if err := os.WriteFile(filepath.Join(base, path), []byte("thumbnail backfill"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	outside := filepath.Join(t.TempDir(), "outside.txt")
	os.WriteFile(outside, []byte("outside"), 0600)
	os.Symlink(outside, filepath.Join(base, "link.txt"))
	first := s.thumbnailBackfillPass(context.Background())
	if first.generated != 1 || first.unavailable != 0 || first.cached != 0 {
		t.Fatal(first)
	}
	media := filepath.Join(base, "nested/deep/readme.txt")
	info, _ := os.Stat(media)
	cache := filepath.Join(filepath.Dir(media), thumbnailDirectory, previewCacheName(media, filesystemRevision(info)))
	before, err := os.Stat(cache)
	if err != nil {
		t.Fatal(err)
	}
	os.Chmod(media, 0000)
	t.Cleanup(func() { os.Chmod(media, 0600) })
	second := s.thumbnailBackfillPass(context.Background())
	after, err := os.Stat(cache)
	if err != nil || second.cached != 1 || second.generated != 0 || second.unavailable != 0 || !before.ModTime().Equal(after.ModTime()) {
		t.Fatal(second, err)
	}
	for _, ignored := range []string{"Trash/" + thumbnailDirectory, thumbnailDirectory + "/" + thumbnailDirectory} {
		if _, err := os.Stat(filepath.Join(base, ignored)); !os.IsNotExist(err) {
			t.Fatal("generated in excluded directory", ignored, err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if counts := s.thumbnailBackfillPass(ctx); counts != (thumbnailBackfillCounts{}) {
		t.Fatal(counts)
	}
}

func TestThumbnailBackfillNeverVisitsSamba(t *testing.T) {
	s, base := filesystemFixture(t)
	os.WriteFile(filepath.Join(base, "original.txt"), []byte("no network thumbnail generation"), 0600)
	if counts := s.thumbnailBackfillPass(context.Background()); counts != (thumbnailBackfillCounts{}) {
		t.Fatal(counts)
	}
	if _, err := os.Stat(filepath.Join(base, thumbnailDirectory)); !os.IsNotExist(err) {
		t.Fatal("Samba cache created", err)
	}
}
