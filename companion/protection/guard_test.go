package protection

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

func fixture(t *testing.T) (*Guard, string) {
	t.Helper()
	base := t.TempDir()
	runtime, root := filepath.Join(base, "runtime"), filepath.Join(base, "storage")
	for _, p := range []string{runtime, filepath.Join(root, "Private"), filepath.Join(root, "Normal")} {
		if e := os.MkdirAll(p, 0700); e != nil {
			t.Fatal(e)
		}
	}
	if e := os.WriteFile(filepath.Join(runtime, "gate"), nil, 0640); e != nil {
		t.Fatal(e)
	}
	g, e := New(runtime, map[string]string{"disk": root})
	if e != nil {
		t.Fatal(e)
	}
	g.owner = uint32(os.Getuid())
	g.mounted = func(string, string) bool { return true }
	writeSnapshot(t, g, Snapshot{Protocol, []Vault{{ID: strings.Repeat("a", 32), Source: "disk", Path: "/Private", State: "unlocked", Initialized: true}}})
	return g, root
}

func writeSnapshot(t *testing.T, g *Guard, s Snapshot) {
	t.Helper()
	data, e := json.Marshal(s)
	if e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(filepath.Join(g.runtime, "status.json"), data, 0640); e != nil {
		t.Fatal(e)
	}
}

func TestGuardLocksEntireOperationAndReleases(t *testing.T) {
	g, _ := fixture(t)
	l, e := g.Acquire()
	if e != nil {
		t.Fatal(e)
	}
	f, e := os.OpenFile(filepath.Join(g.runtime, "gate"), os.O_RDWR, 0)
	if e != nil {
		t.Fatal(e)
	}
	defer f.Close()
	if e = unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); e == nil {
		t.Fatal("transition acquired while reader active")
	}
	l.Close()
	if e = unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); e != nil {
		t.Fatal(e)
	}
	if _, e = g.Acquire(); !errors.Is(e, ErrBusy) {
		t.Fatal("reader ignored pending transition", e)
	}
}

func TestGuardSeparatesPublicAndPrivateCache(t *testing.T) {
	g, root := fixture(t)
	l, e := g.Acquire()
	if e != nil {
		t.Fatal(e)
	}
	defer l.Close()
	cache, e := l.CacheDirectory(filepath.Join(root, "Private", "movie.mp4"))
	if e != nil || cache != filepath.Join(g.runtime, "mounts", strings.Repeat("a", 32), "private") {
		t.Fatal(cache, e)
	}
	if cache, e = l.CacheDirectory(filepath.Join(root, "Normal", "movie.mp4")); e != nil || cache != "" {
		t.Fatal("ordinary cache changed", cache, e)
	}
	if g.Indexable(filepath.Join(root, "Private", "movie.mp4")) {
		t.Fatal("protected file eligible for ordinary index")
	}
	if !g.Indexable(filepath.Join(root, "Normal", "movie.mp4")) {
		t.Fatal("ordinary index disabled")
	}
	for _, p := range []string{filepath.Join(root, ".filebeans-vaults", "cipher"), filepath.Join(g.runtime, "mounts"), t.TempDir()} {
		if e = l.Check(p, false); e == nil {
			t.Fatal("private/outside path accepted", p)
		}
	}
}

func TestGuardRejectsLockedOrDisappearedMountBeforeCacheAccess(t *testing.T) {
	g, root := fixture(t)
	s, e := g.snapshot()
	if e != nil {
		t.Fatal(e)
	}
	s.Vaults[0].State = "locked"
	writeSnapshot(t, g, s)
	l, e := g.Acquire()
	if e != nil {
		t.Fatal(e)
	}
	if _, e = l.CacheDirectory(filepath.Join(root, "Private", "movie.mp4")); !errors.Is(e, ErrLocked) {
		t.Fatal("locked cache accessible", e)
	}
	l.Close()
	s.Vaults[0].State = "unlocked"
	writeSnapshot(t, g, s)
	g.mounted = func(string, string) bool { return false }
	l, e = g.Acquire()
	if e != nil {
		t.Fatal(e)
	}
	defer l.Close()
	if e = l.Check(filepath.Join(root, "Private", "movie.mp4"), false); !errors.Is(e, ErrLocked) {
		t.Fatal("missing mount fell back to plaintext", e)
	}
}

func TestGuardMutationBoundariesAndSymlinkAliases(t *testing.T) {
	g, root := fixture(t)
	l, e := g.Acquire()
	if e != nil {
		t.Fatal(e)
	}
	defer l.Close()
	for _, tc := range [][2]string{
		{root, ""}, {filepath.Join(root, "Private"), ""},
		{filepath.Join(root, "Private", "a"), filepath.Join(root, "Normal", "a")},
		{filepath.Join(root, "Normal", "a"), filepath.Join(root, "Private", "a")},
	} {
		if e = l.Mutation(tc[0], tc[1]); !errors.Is(e, ErrBoundary) {
			t.Fatal("boundary allowed", tc, e)
		}
	}
	if e = l.Mutation(filepath.Join(root, "Private", "a"), filepath.Join(root, "Private", "b")); e != nil {
		t.Fatal("internal rename rejected", e)
	}
	if e = os.Symlink(filepath.Join(root, "Private"), filepath.Join(root, "Alias")); e != nil {
		t.Fatal(e)
	}
	if e = l.Check(filepath.Join(root, "Alias", "a"), false); !errors.Is(e, ErrBoundary) {
		t.Fatal("symlink bypassed protection", e)
	}
}

func TestGuardRejectsMissingOrWritableRegistry(t *testing.T) {
	g, root := fixture(t)
	if e := os.Chmod(filepath.Join(g.runtime, "status.json"), 0666); e != nil {
		t.Fatal(e)
	}
	if _, e := g.Acquire(); !errors.Is(e, ErrUnavailable) {
		t.Fatal("writable registry accepted", e)
	}
	if g.Indexable(filepath.Join(root, "Private", "file")) {
		t.Fatal("failed registry enabled indexing")
	}
}
