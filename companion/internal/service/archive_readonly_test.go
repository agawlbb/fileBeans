package service

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestArchiveReadOnlyPolicy(t *testing.T) {
	link := ArchiveLink{Library: ArchiveEndpoint{Source: "VPS", Path: "/pics", Available: false}, Archive: ArchiveEndpoint{Source: "Samba", Path: "/storage/pics", Available: false, Pending: &ArchiveEndpoint{Source: "Samba", Path: "/pending"}}}
	for _, tc := range []struct {
		source, path       string
		recursive, blocked bool
	}{
		{"Samba", "/storage/pics", false, true}, {"Samba", "/storage/pics/future/new.jpg", false, true}, {"Samba", "/storage", true, true}, {"Samba", "/storage", false, false}, {"Samba", "/pending/a.jpg", false, true}, {"VPS", "/pics/a.jpg", true, false}, {"Samba", "/storage/pics-other/a.jpg", true, false},
	} {
		e := archiveWritePolicy([]ArchiveLink{link}, tc.source, tc.path, tc.recursive)
		if errors.Is(e, ErrArchiveReadOnly) != tc.blocked {
			t.Fatalf("%+v: %v", tc, e)
		}
		if tc.blocked && e.Error() != "Make changes in VPS /pics." {
			t.Fatal(e)
		}
	}
}
func TestArchiveRootsRejectMutationAndRecoverAfterUnlink(t *testing.T) {
	base := t.TempDir()
	os.MkdirAll(filepath.Join(base, "archive/sub"), 0700)
	os.WriteFile(filepath.Join(base, "archive/sub/a.jpg"), []byte("preserve"), 0600)
	os.WriteFile(filepath.Join(base, "outside.jpg"), []byte("outside"), 0600)
	raw, e := os.OpenRoot(base)
	if e != nil {
		t.Fatal(e)
	}
	defer raw.Close()
	links := []ArchiveLink{{Library: ArchiveEndpoint{Source: "VPS", Path: "/pics"}, Archive: ArchiveEndpoint{Source: "Samba", Path: "/archive"}}}
	root := &archiveWriteRoot{rootFS: raw, check: func(p string, recursive bool) error { return archiveWritePolicy(links, "Samba", p, recursive) }}
	operations := map[string]func() error{
		"write": func() error { return root.WriteFile("archive/sub/a.jpg", []byte("bad"), 0600) },
		"open-write": func() error {
			f, e := root.OpenFile("archive/sub/a.jpg", os.O_WRONLY|os.O_TRUNC, 0600)
			if f != nil {
				f.Close()
			}
			return e
		},
		"mkdir":           func() error { return root.MkdirAll("archive/sub/new", 0700) },
		"chmod":           func() error { return root.Chmod("archive/sub/a.jpg", 0777) },
		"remove":          func() error { return root.Remove("archive/sub/a.jpg") },
		"remove-root":     func() error { return root.RemoveAll("archive") },
		"remove-ancestor": func() error { return root.RemoveAll(".") },
		"rename-out":      func() error { return renameExclusive(root, "archive/sub/a.jpg", "moved.jpg") },
		"rename-in":       func() error { return renameRoots(raw, "outside.jpg", root, "archive/new.jpg") },
	}
	for name, operation := range operations {
		t.Run(name, func(t *testing.T) {
			if e := operation(); !errors.Is(e, ErrArchiveReadOnly) {
				t.Fatal(e)
			}
		})
	}
	if b, e := root.ReadFile("archive/sub/a.jpg"); e != nil || string(b) != "preserve" {
		t.Fatal(string(b), e)
	}
	if e = root.WriteFile("normal.jpg", []byte("normal"), 0600); e != nil {
		t.Fatal(e)
	}
	links = nil
	if e = root.WriteFile("archive/sub/new.jpg", []byte("allowed after unlink"), 0600); e != nil {
		t.Fatal(e)
	}
}

func TestArchiveIdentityAliasesRemainReadOnly(t *testing.T) {
	base := t.TempDir()
	os.MkdirAll(filepath.Join(base, "archive/sub"), 0700)
	os.Mkdir(filepath.Join(base, "other"), 0700)
	os.Symlink("archive", filepath.Join(base, "alias"))
	raw, e := os.OpenRoot(base)
	if e != nil {
		t.Fatal(e)
	}
	defer raw.Close()
	links := []ArchiveLink{{Library: ArchiveEndpoint{Source: "VPS", Path: "/pics"}, Archive: ArchiveEndpoint{Source: "Samba", Path: "/archive"}}}
	if e = archiveAliasPolicy(raw, links, "Samba", "alias/sub/future.jpg", false); !errors.Is(e, ErrArchiveReadOnly) {
		t.Fatal("alias bypassed read-only", e)
	}
	if e = archiveAliasPolicy(raw, links, "Samba", "other/new.jpg", false); e != nil {
		t.Fatal("ordinary path blocked", e)
	}
	if e = archiveAliasPolicy(raw, links, "Samba", ".", true); !errors.Is(e, ErrArchiveReadOnly) {
		t.Fatal("ancestor bypassed read-only", e)
	}
}
