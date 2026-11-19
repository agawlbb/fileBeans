package service

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func testMoveIntegration(t *testing.T, s *Service) {
	src, _ := s.root("samba")
	defer src.Close()
	dst, _ := s.root("vps")
	defer dst.Close()
	t.Run("mixed-rename-and-copy-with-normalized-path", func(t *testing.T) {
		path, err := os.MkdirTemp("/dev/shm", "filebeans-mixed-move-")
		if err != nil {
			t.Skip("second filesystem unavailable")
		}
		defer os.RemoveAll(path)
		previous := s.config.Roots
		s.config.Roots = append(append([]Root{}, previous...), Root{Name: "other", Path: path})
		defer func() { s.config.Roots = previous }()
		if err = src.WriteFile("mixed-native", []byte("rename"), 0600); err != nil {
			t.Fatal(err)
		}
		before, _ := src.Stat("mixed-native")
		if err = os.WriteFile(filepath.Join(path, "mixed-copy"), []byte("copy"), 0600); err != nil {
			t.Fatal(err)
		}
		j := Job{ID: ID(), State: "queued", Request: CopyRequest{Operation: "move", Items: []Item{{Source: "samba", Path: "./mixed-native"}, {Source: "other", Path: "mixed-copy"}}, Destination: Item{Source: "vps", Path: "."}}}
		if err = s.insert(j.ID, "copy", j.State, "", j); err != nil {
			t.Fatal(err)
		}
		s.runJob(context.Background(), &j)
		if j.State != "completed" || !j.Move.Selections[0].Rename || j.Move.Selections[1].Rename {
			t.Fatal(j.State, j.Error)
		}
		after, err := dst.Stat("mixed-native")
		if err != nil || !os.SameFile(before, after) {
			t.Fatal("native selection copied", err)
		}
		if data, err := dst.ReadFile("mixed-copy"); err != nil || string(data) != "copy" {
			t.Fatal("copy destination", err)
		}
		if _, err = os.Stat(filepath.Join(path, "mixed-copy")); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("copy source retained", err)
		}
	})
	t.Run("cross-filesystem-copy-fallback", func(t *testing.T) {
		path, err := os.MkdirTemp("/dev/shm", "filebeans-move-copy-")
		if err != nil {
			t.Skip("second filesystem unavailable")
		}
		defer os.RemoveAll(path)
		previous := s.config.Roots[0].Path
		s.config.Roots[0].Path = path
		defer func() { s.config.Roots[0].Path = previous }()
		if err = src.WriteFile("copy-fallback", []byte("verified copy"), 0600); err != nil {
			t.Fatal(err)
		}
		j := Job{ID: ID(), State: "queued", Request: CopyRequest{Operation: "move", Items: []Item{{Source: "samba", Path: "copy-fallback"}}, Destination: Item{Source: "vps", Path: "."}}}
		if err = s.insert(j.ID, "copy", j.State, "", j); err != nil {
			t.Fatal(err)
		}
		s.runJob(context.Background(), &j)
		if j.State != "completed" || j.Move.Selections[0].Rename {
			t.Fatal(j.State, j.Error)
		}
		if data, err := os.ReadFile(filepath.Join(path, "copy-fallback")); err != nil || string(data) != "verified copy" {
			t.Fatal("destination", err)
		}
		if _, err = src.Stat("copy-fallback"); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("source retained", err)
		}
	})
	seed := func(t *testing.T, name string, published bool) Job {
		t.Helper()
		if e := mkdir(src, filepath.Join(name, "empty")); e != nil {
			t.Fatal(e)
		}
		if e := src.WriteFile(filepath.Join(name, "file"), []byte("original"), 0600); e != nil {
			t.Fatal(e)
		}
		entries, e := snapshotMove(context.Background(), src, name)
		if e != nil {
			t.Fatal(e)
		}
		j := Job{ID: ID(), State: "queued", Request: CopyRequest{Operation: "move", Items: []Item{{Source: "samba", Path: name}}, Destination: Item{Source: "vps", Path: "."}}}
		if published {
			mkdir(dst, filepath.Join(name, "empty"))
			dst.WriteFile(filepath.Join(name, "file"), []byte("original"), 0600)
			j.Move = &MoveState{Published: true, Selections: []MoveSelection{{Source: "samba", Path: name, To: name, Quarantine: filepath.Join(".filebeans-stage", j.ID, "move-0"), Entries: entries}}}
		}
		if e = s.insert(j.ID, "copy", j.State, "", j); e != nil {
			t.Fatal(e)
		}
		return j
	}
	t.Run("real-move-removes-original-and-preserves-empty-directories", func(t *testing.T) {
		j := seed(t, "move-tree", false)
		s.runJob(context.Background(), &j)
		if j.State != "completed" {
			t.Fatal(j.State, j.Error)
		}
		if _, e := src.Stat("move-tree"); !errors.Is(e, os.ErrNotExist) {
			t.Fatal("source retained", e)
		}
		if b, e := dst.ReadFile("move-tree/file"); e != nil || string(b) != "original" {
			t.Fatal("destination", e)
		}
		if _, e := dst.Stat("move-tree/empty"); e != nil {
			t.Fatal(e)
		}
		p := publicJob(j)
		b, _ := json.Marshal(p)
		if p.Move != nil || p.CanCancel == nil || *p.CanCancel {
			t.Fatal("private journal exposed", string(b))
		}
	})

	t.Run("multiple-selection-one-durable-job", func(t *testing.T) {
		request := CopyRequest{Operation: "move", Destination: Item{Source: "vps", Path: "."}}
		for _, name := range []string{"move-many-a", "move-many-b", "move-many-c"} {
			if e := src.WriteFile(name, []byte(name), 0600); e != nil {
				t.Fatal(e)
			}
			request.Items = append(request.Items, Item{Source: "samba", Path: name})
		}
		j := Job{ID: ID(), State: "queued", Request: request}
		if e := s.insert(j.ID, "copy", j.State, ID(), j); e != nil {
			t.Fatal(e)
		}
		s.runJob(context.Background(), &j)
		if j.State != "completed" || len(j.Move.Selections) != 3 {
			t.Fatal(j.State, j.Error)
		}
		for _, item := range request.Items {
			if _, e := src.Stat(item.Path); !errors.Is(e, os.ErrNotExist) {
				t.Fatal("original remains", e)
			}
			if data, e := dst.ReadFile(item.Path); e != nil || string(data) != item.Path {
				t.Fatal("destination", e)
			}
		}
	})
	t.Run("rename-target", func(t *testing.T) {
		j := seed(t, "move-old-name", false)
		j.Request.DestinationName = "move-new-name"
		s.save(j.ID, "copy", j.State, j)
		s.runJob(context.Background(), &j)
		if j.State != "completed" {
			t.Fatal(j.State, j.Error)
		}
		if _, e := dst.Stat("move-new-name/file"); e != nil {
			t.Fatal(e)
		}
	})
	t.Run("existing-target-preserves-both", func(t *testing.T) {
		j := seed(t, "move-existing", false)
		mkdir(dst, "move-existing")
		dst.WriteFile("move-existing/file", []byte("keep"), 0600)
		s.runJob(context.Background(), &j)
		if j.State != "failed" {
			t.Fatal(j.State)
		}
		if b, _ := src.ReadFile("move-existing/file"); string(b) != "original" {
			t.Fatal("source lost")
		}
		if b, _ := dst.ReadFile("move-existing/file"); string(b) != "keep" {
			t.Fatal("target overwritten")
		}
	})
	for _, change := range []string{"source", "target"} {
		t.Run("changed-"+change+"-after-publish", func(t *testing.T) {
			name := "move-changed-" + change
			j := seed(t, name, true)
			root := src
			if change == "target" {
				root = dst
			}
			root.WriteFile(filepath.Join(name, "file"), []byte("changed"), 0600)
			s.runJob(context.Background(), &j)
			if j.State != "failed" {
				t.Fatal(j.State, j.Error)
			}
			if _, e := src.Stat(filepath.Join(name, "file")); e != nil {
				t.Fatal("source lost")
			}
		})
	}
	for _, phase := range []string{"published", "quarantined", "partial-delete", "deleted"} {
		t.Run("restart-"+phase, func(t *testing.T) {
			name := "move-restart-" + phase
			j := seed(t, name, true)
			m := &j.Move.Selections[0]
			if phase != "published" {
				j.Move.Committed = true
				mkdir(src, filepath.Dir(m.Quarantine))
				if e := renameExclusive(src, m.Path, m.Quarantine); e != nil {
					t.Fatal(e)
				}
				// A new occupant at the original path must survive recovery.
				mkdir(src, m.Path)
				src.WriteFile(filepath.Join(m.Path, "new"), []byte("new"), 0600)
			}
			if phase == "partial-delete" || phase == "deleted" {
				m.Removing = true
				src.Remove(filepath.Join(m.Quarantine, "file"))
			}
			if phase == "deleted" {
				src.RemoveAll(m.Quarantine)
			}
			s.save(j.ID, "copy", j.State, j)
			var resumed Job
			if e := s.read(j.ID, "copy", &resumed); e != nil {
				t.Fatal(e)
			}
			s.runJob(context.Background(), &resumed)
			if resumed.State != "completed" {
				t.Fatal(resumed.State, resumed.Error)
			}
			if phase != "published" {
				if b, e := src.ReadFile(filepath.Join(name, "new")); e != nil || string(b) != "new" {
					t.Fatal("new occupant lost", e)
				}
			}
			if _, e := src.Stat(m.Quarantine); !errors.Is(e, os.ErrNotExist) {
				t.Fatal("quarantine retained", e)
			}
		})
	}
	t.Run("cancel-before-commit", func(t *testing.T) {
		j := seed(t, "move-cancelled", true)
		j.Cancel = true
		s.save(j.ID, "copy", j.State, j)
		s.runJob(context.Background(), &j)
		if j.State != "cancelled" {
			t.Fatal(j.State)
		}
		if _, e := src.Stat("move-cancelled/file"); e != nil {
			t.Fatal("source lost", e)
		}
	})
	t.Run("cancel-after-commit-rejected", func(t *testing.T) {
		j := seed(t, "move-committed", true)
		j.Move.Committed = true
		s.save(j.ID, "copy", j.State, j)
		r := httptest.NewRequest("POST", "/v1/jobs/"+j.ID+"/cancel", nil)
		r.Header.Set("Authorization", "Bearer "+s.config.Token)
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, r)
		if w.Code != 409 {
			t.Fatal(w.Code, w.Body.String())
		}
		s.runJob(context.Background(), &j)
		if j.State != "completed" {
			t.Fatal(j.State, j.Error)
		}
	})
}
func TestMoveRequestValidation(t *testing.T) {
	for _, req := range []CopyRequest{
		{Operation: "delete"},
		{Operation: "copy", DestinationName: "renamed"},
		{Operation: "move", Items: []Item{{Source: "vps", Path: "."}}},
		{Operation: "move", Items: []Item{{Source: "vps", Path: "a"}, {Source: "vps", Path: "a/b"}}},
		{Operation: "move", Items: []Item{{Source: "drive", Path: "id"}}},
	} {
		if validateMoveRequest(req) == nil {
			t.Fatal("accepted unsafe move", req)
		}
	}
}
