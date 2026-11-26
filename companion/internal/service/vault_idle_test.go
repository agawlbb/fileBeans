package service

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestVaultIdlePublicFileActivity(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "file")
	if err := os.WriteFile(path, []byte("contents"), 0600); err != nil {
		t.Fatal(err)
	}
	start := time.Unix(10000, 0)
	w, err := newVaultIdleWatch(start)
	if err != nil {
		t.Fatal(err)
	}
	defer w.close()
	if err = w.scan(root); err != nil {
		t.Fatal(err)
	}
	if err = w.drain(start); err != nil {
		t.Fatal(err)
	}
	// Repeated background directory listings must still allow auto lock.
	for i := 1; i <= 60; i++ {
		if _, err = os.ReadDir(root); err != nil {
			t.Fatal(err)
		}
		if err = w.drain(start.Add(time.Duration(i) * time.Minute)); err != nil {
			t.Fatal(err)
		}
	}
	if !w.expired(start.Add(time.Hour)) {
		t.Fatal("directory polling extended timer")
	}
	// An actual file read resets only this folder's timer.
	accessed := start.Add(time.Hour)
	if _, err = os.ReadFile(path); err != nil {
		t.Fatal(err)
	}
	if err = w.drain(accessed); err != nil {
		t.Fatal(err)
	}
	if w.expired(accessed.Add(time.Hour - time.Nanosecond)) {
		t.Fatal("locked too early")
	}
	if !w.expired(accessed.Add(time.Hour)) {
		t.Fatal("did not expire at one hour")
	}
	if err = os.WriteFile(path, []byte("changed"), 0600); err != nil {
		t.Fatal(err)
	}
	changed := accessed.Add(30 * time.Minute)
	if err = w.drain(changed); err != nil {
		t.Fatal(err)
	}
	if !w.last.Equal(changed) {
		t.Fatal("write did not reset timer")
	}
}

func TestVaultIdleIgnoresInternalCachesOvernight(t *testing.T) {
	root := t.TempDir()
	visible := filepath.Join(root, "photos")
	if err := os.Mkdir(visible, 0700); err != nil {
		t.Fatal(err)
	}
	cache := filepath.Join(visible, thumbnailDirectory)
	if err := os.Mkdir(cache, 0700); err != nil {
		t.Fatal(err)
	}
	preview := filepath.Join(cache, "preview.jpg")
	if err := os.WriteFile(preview, []byte("cached"), 0600); err != nil {
		t.Fatal(err)
	}
	start := time.Unix(10000, 0)
	w, err := newVaultIdleWatch(start)
	if err != nil {
		t.Fatal(err)
	}
	defer w.close()
	if err = w.scan(root); err != nil {
		t.Fatal(err)
	}
	if err = w.drain(start); err != nil {
		t.Fatal(err)
	}
	// Automatic backfill reads cached previews every fifteen minutes.
	for elapsed := 15 * time.Minute; elapsed <= 12*time.Hour; elapsed += 15 * time.Minute {
		if _, err = os.ReadFile(preview); err != nil {
			t.Fatal(err)
		}
		if err = w.drain(start.Add(elapsed)); err != nil {
			t.Fatal(err)
		}
		if !w.last.Equal(start) {
			t.Fatal("background cache read extended idle deadline")
		}
	}
	// Internal directories created after watching starts must also be ignored.
	for _, name := range []string{thumbnailDirectory, ".FILEBEANS-THUMBNAILS", ".filebeans-stage"} {
		path := filepath.Join(root, name)
		if err = os.Mkdir(path, 0700); err != nil {
			t.Fatal(err)
		}
		if err = w.drain(start.Add(12 * time.Hour)); err != nil {
			t.Fatal(err)
		}
		if err = w.scan(root); err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(filepath.Join(path, "internal"), []byte("background"), 0600); err != nil {
			t.Fatal(err)
		}
		if err = w.drain(start.Add(12 * time.Hour)); err != nil {
			t.Fatal(err)
		}
	}
	if !w.expired(start.Add(time.Hour)) || !w.last.Equal(start) {
		t.Fatal("internal work prevented one-hour idle expiry")
	}
	// Public files still renew the deadline after rescanning.
	if err = os.WriteFile(filepath.Join(visible, "photo.jpg"), []byte("public"), 0600); err != nil {
		t.Fatal(err)
	}
	active := start.Add(12 * time.Hour)
	if err = w.drain(active); err != nil || !w.last.Equal(active) {
		t.Fatal("public activity did not renew deadline", err)
	}
}

func TestVaultIdleNewDirectoriesAndIndependentTimers(t *testing.T) {
	root := t.TempDir()
	start := time.Unix(10000, 0)
	a, err := newVaultIdleWatch(start)
	if err != nil {
		t.Fatal(err)
	}
	defer a.close()
	b, err := newVaultIdleWatch(start)
	if err != nil {
		t.Fatal(err)
	}
	defer b.close()
	if err = a.scan(root); err != nil {
		t.Fatal(err)
	}
	if err = a.drain(start); err != nil {
		t.Fatal(err)
	}
	nested := filepath.Join(root, "new")
	if err = os.Mkdir(nested, 0700); err != nil {
		t.Fatal(err)
	}
	activity := start.Add(40 * time.Minute)
	if err = a.drain(activity); err != nil {
		t.Fatal(err)
	}
	if !a.dirty {
		t.Fatal("new directory not scheduled for watching")
	}
	if err = a.scan(root); err != nil {
		t.Fatal(err)
	}
	if err = a.drain(activity); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(nested, "file")
	if err = os.WriteFile(path, []byte("new"), 0600); err != nil {
		t.Fatal(err)
	}
	later := start.Add(50 * time.Minute)
	if err = a.drain(later); err != nil {
		t.Fatal(err)
	}
	if !a.last.Equal(later) {
		t.Fatal("nested activity missed")
	}
	if a.expired(start.Add(time.Hour)) || !b.expired(start.Add(time.Hour)) {
		t.Fatal("folder timers are not independent")
	}
	if err = os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err = a.drain(later.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if !a.last.Equal(later.Add(time.Minute)) {
		t.Fatal("delete not counted")
	}
}

func TestBrokerIdleRealFilesystem(t *testing.T)    { testBrokerIdleFilesystem(t, false) }
func TestBrokerIdleRealSMBFilesystem(t *testing.T) { testBrokerIdleFilesystem(t, true) }
func testBrokerIdleFilesystem(t *testing.T, smb bool) {
	if os.Getenv("FILEBEANS_TEST_FUSE") != "1" {
		t.Skip("isolated FUSE runtime required")
	}
	b := testBroker(t)
	if smb {
		mountSyntheticSMB(t, b)
	}
	b.config.Gocryptfs = os.Getenv("FILEBEANS_TEST_GOCRYPTFS")
	b.config.Fusermount = "/test-tools/fusermount3"
	b.manager.memoryCheck = func() error { return nil }
	key := make([]byte, 32)
	for i := range key {
		key[i] = 0x42
	}
	entry, err := b.reserve("disk", "/", "Idle", "synthetic-idle-request")
	if err != nil {
		t.Fatal(err)
	}
	if err = b.open(entry, key); err != nil {
		t.Fatal(err)
	}
	public := b.publicPath(entry)
	file := filepath.Join(public, "contents")
	if err = os.WriteFile(file, []byte("recoverable"), 0600); err != nil {
		t.Fatal(err)
	}
	cacheDir := filepath.Join(public, thumbnailDirectory)
	if err = os.Mkdir(cacheDir, 0700); err != nil {
		t.Fatal(err)
	}
	cache := filepath.Join(cacheDir, "preview.jpg")
	if err = os.WriteFile(cache, []byte("cached preview"), 0600); err != nil {
		t.Fatal(err)
	}
	watches := map[string]*vaultIdleWatch{}
	defer func() {
		for _, w := range watches {
			w.close()
		}
	}()
	start := time.Unix(10000, 0)
	b.pollIdleVaults(watches, start)
	if len(watches) != 1 {
		t.Fatal("unlocked vault not monitored")
	}
	// Private previews and status/listing polling must not prevent locking.
	private := filepath.Join(b.vaultConfig(entry).Mount, "private", "cache", "thumbnail")
	if err = os.WriteFile(private, []byte("cache"), 0600); err != nil {
		t.Fatal(err)
	}
	for i := 1; i < 60; i++ {
		if _, err = os.ReadDir(public); err != nil {
			t.Fatal(err)
		}
		if _, err = os.ReadFile(cache); err != nil {
			t.Fatal(err)
		}
		b.pollIdleVaults(watches, start.Add(time.Duration(i)*time.Minute))
	}
	if !watches[entry.ID].last.Equal(start) {
		t.Fatal("background thumbnail polling extended idle deadline")
	}
	if b.statuses()[0].State != "unlocked" {
		t.Fatal("locked before hour")
	}
	// A file read late in the interval postpones locking for a full hour.
	if _, err = os.ReadFile(file); err != nil {
		t.Fatal(err)
	}
	accessed := start.Add(59 * time.Minute)
	b.pollIdleVaults(watches, accessed)
	b.pollIdleVaults(watches, accessed.Add(time.Hour-time.Nanosecond))
	if b.statuses()[0].State != "unlocked" {
		t.Fatal("file read did not extend deadline")
	}
	// Active operation lease defers locking. Closing it permits the next retry.
	gate, err := os.Open(filepath.Join(b.config.Runtime, "gate"))
	if err != nil {
		t.Fatal(err)
	}
	defer gate.Close()
	if err = unix.Flock(int(gate.Fd()), unix.LOCK_SH|unix.LOCK_NB); err != nil {
		t.Fatal(err)
	}
	deadline := accessed.Add(time.Hour)
	b.pollIdleVaults(watches, deadline)
	if b.statuses()[0].State != "unlocked" {
		t.Fatal("active lease interrupted")
	}
	if err = gate.Close(); err != nil {
		t.Fatal(err)
	}
	b.pollIdleVaults(watches, deadline.Add(5*time.Second))
	if b.statuses()[0].State != "locked" || len(watches) != 0 {
		t.Fatal("idle folder did not lock after busy retry")
	}
	// Existing unlock implementation still recovers the same bytes.
	if err = b.open(entry, key); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(file); err != nil || string(data) != "recoverable" {
		t.Fatal("unlock recovery", err)
	}
	b.pollIdleVaults(watches, deadline.Add(10*time.Second))
	if len(watches) != 1 || watches[entry.ID].expired(deadline.Add(10*time.Second)) {
		t.Fatal("unlock did not start fresh timer")
	}
}
