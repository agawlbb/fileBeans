package service

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestPiSambaThumbnailTimestampCompatibility(t *testing.T) {
	for _, filename := range []string{"photo.jpg", "clip.mp4"} {
		t.Run(filename, func(t *testing.T) {
			s, base := filesystemFixture(t)
			s.config.Profile = "pi"
			media := filepath.Join(base, filename)
			if err := os.WriteFile(media, []byte("original must never be decoded"), 0000); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { os.Chmod(media, 0600) })
			stamp := time.Unix(1700000000, 123456789)
			if err := os.Chtimes(media, stamp, stamp); err != nil {
				t.Fatal(err)
			}
			info, err := os.Stat(media)
			if err != nil {
				t.Fatal(err)
			}
			legacyInfo := archivePeerInfo{Length: info.Size(), Modified: stamp.Truncate(100 * time.Nanosecond).UnixNano()}
			legacyRevision := filesystemRevision(legacyInfo)
			if legacyRevision != sambaPreviewRevision(info) || legacyRevision == filesystemRevision(info) {
				t.Fatal("timestamp fixture did not retain sub-100ns precision")
			}
			dir := filepath.Join(base, thumbnailDirectory)
			if err := os.Mkdir(dir, 0700); err != nil {
				t.Fatal(err)
			}
			legacy := filepath.Join(dir, previewCacheName(filename, legacyRevision))
			if err := os.WriteFile(legacy, []byte("imported preview"), 0600); err != nil {
				t.Fatal(err)
			}
			check := func(status int, body string) {
				t.Helper()
				out := fsRequest(t, s, "GET", "thumbnail", "/"+filename, nil, nil)
				if out.Code != status || status == 200 && out.Body.String() != body {
					t.Fatal(out.Code, out.Body.String())
				}
			}
			check(200, "imported preview")
			// A valid current cache always wins, and reads never migrate/rewrite caches.
			current := filepath.Join(dir, previewCacheName(filename, filesystemRevision(info)))
			if err := os.WriteFile(current, []byte("current preview"), 0600); err != nil {
				t.Fatal(err)
			}
			check(200, "current preview")
			os.Remove(current)
			entries, err := os.ReadDir(dir)
			if err != nil || len(entries) != 1 || entries[0].Name() != filepath.Base(legacy) {
				t.Fatal("lookup wrote cache files", entries, err)
			}
			// A genuine change outside Samba precision must not reuse a stale preview.
			changed := stamp.Add(time.Second)
			if err := os.Chtimes(media, changed, changed); err != nil {
				t.Fatal(err)
			}
			check(404, "")
			if err := os.Chtimes(media, stamp, stamp); err != nil {
				t.Fatal(err)
			}
			// Compatibility is Pi-only; other servers retain exact revision checks.
			s.config.Profile = ""
			// Use the cache-only implementation directly to avoid renderer execution.
			request := thumbnailRequest(t.Context(), "samba", "/"+filename)
			cached := &thumbnailSink{header: make(map[string][]string)}
			s.filesystemCachedThumbnail(cached, request)
			if cached.status != 404 {
				t.Fatal("non-Pi accepted legacy timestamp", cached.status)
			}
		})
	}
}

func TestPiSambaLegacyVideoThumbnailWithLockedPolicy(t *testing.T) {
	s, base := filesystemFixture(t)
	s.config.Profile = "pi"
	s.vaults = testVaultService().vaults
	s.vaults.states[0].unlocked = false
	var err error
	s.db, err = sql.Open("locked-preview-policy", "")
	if err != nil {
		t.Fatal(err)
	}
	defer s.db.Close()
	media := filepath.Join(base, "clip.mp4")
	if err = os.WriteFile(media, []byte("never decode"), 0000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(media, 0600) })
	stamp := time.Unix(1700000000, 123456789)
	if err = os.Chtimes(media, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(media)
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(base, thumbnailDirectory)
	os.Mkdir(dir, 0700)
	cache := filepath.Join(dir, previewCacheNameVersion("clip.mp4", sambaPreviewRevision(info), "jpeg-320-v1"))
	if err = os.WriteFile(cache, []byte("legacy video preview"), 0600); err != nil {
		t.Fatal(err)
	}
	out := fsRequest(t, s, "GET", "thumbnail", "/clip.mp4", nil, nil)
	if out.Code != 200 || out.Body.String() != "legacy video preview" {
		t.Fatal(out.Code, out.Body.String())
	}
	// A file's own protected-folder lock still blocks all cache versions.
	s.config.Roots = append(s.config.Roots, Root{Name: "Protected", Path: base})
	out = archiveRequest(s, "GET", "/v1/fs/thumbnail?source=Protected&path=/clip.mp4", "")
	if out.Code != 409 {
		t.Fatal("locked media cache exposed", out.Code, out.Body.String())
	}
}
