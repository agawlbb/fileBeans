package service

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

func TestCrossDriveMoveValidation(t *testing.T) {
	for _, req := range []CopyRequest{
		{Operation: "move", Items: []Item{{Source: "vps", Path: "photo.jpg"}}, Destination: Item{Source: "drive", Path: "root"}},
		{Operation: "move", Items: []Item{{Source: "drive", Path: "file-id"}}, Destination: Item{Source: "vps", Path: "."}},
	} {
		if err := validateMoveRequest(req); err != nil {
			t.Fatal(err)
		}
	}
	if err := validateMoveRequest(CopyRequest{Operation: "move", Items: []Item{{Source: "drive", Path: "a"}}, Destination: Item{Source: "drive", Path: "b"}}); err == nil {
		t.Fatal("Drive-internal jobs should use native reparenting")
	}
}

func TestDriveMoveDestinationMetadataChanges(t *testing.T) {
	for _, change := range []string{"version", "folder-version", "checksum", "size", "name", "parent", "missing-checksum", "legacy-checksum"} {
		t.Run(change, func(t *testing.T) {
			s := &Service{config: Config{State: t.TempDir()}}
			fixtureGrant(t, s, true)
			cloud := newCloudFixture(t)
			cloud.add("target", "target.dat", "application/octet-stream", false)
			item := cloud.items["target"]
			item.SHA256 = "original-content"
			if change == "folder-version" {
				item.Mime = "application/vnd.google-apps.folder"
				item.SHA256 = ""
			}
			if change == "missing-checksum" || change == "legacy-checksum" {
				item.SHA256 = ""
			}
			cloud.items[item.ID] = item
			previous := googleHTTP
			googleHTTP = &http.Client{Transport: roundTrip(cloud.serve)}
			defer func() { googleHTTP = previous }()
			ctx := context.Background()
			saved, _, err := s.driveTree(ctx, item.ID, "fixture-account", true)
			if err != nil {
				t.Fatal(err)
			}
			selection := MoveSelection{DestinationID: item.ID, DestinationDrive: saved}
			job := Job{Connection: "fixture-account", Request: CopyRequest{Destination: Item{Source: "drive", Path: "root"}}}
			item.Version = "2"
			switch change {
			case "checksum":
				item.SHA256 = "changed-content"
			case "size":
				item.Size = "7"
			case "name":
				item.Name = "renamed.dat"
			case "parent":
				item.Parents = []string{"elsewhere"}
			case "legacy-checksum":
				item.SHA256 = "newly-available"
			}
			cloud.items[item.ID] = item
			err = s.checkDriveMoveDestination(ctx, &job, &selection)
			if change == "version" || change == "folder-version" {
				if err != nil {
					t.Fatal("metadata-only version change blocked move", err)
				}
			} else if !errors.Is(err, ErrConflict) {
				t.Fatal("changed or unverifiable destination accepted", err)
			}
		})
	}
}

func testCrossDriveMoves(t *testing.T, s *Service) {
	fixtureGrant(t, s, true)
	previous := googleHTTP
	defer func() { googleHTTP = previous }()
	cloud := newCloudFixture(t)
	googleHTTP = &http.Client{Transport: roundTrip(cloud.serve)}
	ctx := context.Background()
	submit := func(req CopyRequest) Job {
		j := Job{ID: ID(), State: "queued", Connection: "fixture-account", Request: req}
		if err := s.insert(j.ID, "copy", j.State, "", j); err != nil {
			t.Fatal(err)
		}
		return j
	}
	t.Run("drive-metadata-version-change-allows-source-cleanup", func(t *testing.T) {
		name := "metadata-move.dat"
		path := filepath.Join(s.config.Roots[1].Path, name)
		if err := os.WriteFile(path, []byte("abcdef"), 0600); err != nil {
			t.Fatal(err)
		}
		cloud.add("metadata-move", name, "application/octet-stream", false)
		item := cloud.items["metadata-move"]
		item.MD5 = "e80b5017098950fc58aad83c8c14978e"
		cloud.items[item.ID] = item
		destination, _, err := s.driveTree(ctx, item.ID, "fixture-account", true)
		if err != nil {
			t.Fatal(err)
		}
		root, err := s.root("samba")
		if err != nil {
			t.Fatal(err)
		}
		source, err := snapshotMove(ctx, root, name)
		root.Close()
		if err != nil {
			t.Fatal(err)
		}
		j := submit(CopyRequest{Operation: "move", Items: []Item{{Source: "samba", Path: name}}, Destination: Item{Source: "drive", Path: "root"}})
		j.Move = &MoveState{Published: true, Selections: []MoveSelection{{Source: "samba", Path: name, To: name,
			Quarantine: filepath.Join(".filebeans-stage", j.ID, "move-0"), Entries: source,
			DestinationID: item.ID, DestinationDrive: destination}}}
		if err := s.saveMove(&j); err != nil {
			t.Fatal(err)
		}
		item.Version = "5"
		cloud.items[item.ID] = item
		s.runJob(ctx, &j)
		if j.State != "completed" {
			t.Fatal(j.State, j.Error)
		}
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("source retained after verified move", err)
		}
	})
	t.Run("local-to-drive-copy-before-removal-and-restart", func(t *testing.T) {
		path := filepath.Join(s.config.Roots[0].Path, "drive-move.dat")
		os.WriteFile(path, []byte("original"), 0600)
		j := submit(CopyRequest{Operation: "move", Items: []Item{{Source: "vps", Path: "drive-move.dat"}}, Destination: Item{Source: "drive", Path: "root"}})
		cloud.lostChunk = true
		s.runJob(ctx, &j)
		if j.State != "waiting" {
			t.Fatal(j.State, j.Error)
		}
		if _, err := os.Stat(path); err != nil {
			t.Fatal("source removed before copy success", err)
		}
		var resumed Job
		s.read(j.ID, "copy", &resumed)
		s.runJob(ctx, &resumed)
		if resumed.State != "completed" {
			t.Fatal(resumed.State, resumed.Error)
		}
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("source remains", err)
		}
		if string(cloud.payloads[resumed.Move.Selections[0].DestinationID]) != "original" {
			t.Fatal("copy differs")
		}
		s.runJob(ctx, &resumed)
		if resumed.State != "completed" {
			t.Fatal("cleanup replay failed", resumed.Error)
		}
	})
	t.Run("changed-local-source-is-retained", func(t *testing.T) {
		path := filepath.Join(s.config.Roots[0].Path, "changed-drive-move.dat")
		os.WriteFile(path, []byte("original"), 0600)
		j := submit(CopyRequest{Operation: "move", Items: []Item{{Source: "vps", Path: "changed-drive-move.dat"}}, Destination: Item{Source: "drive", Path: "root"}})
		cloud.lostChunk = true
		s.runJob(ctx, &j)
		os.WriteFile(path, []byte("changed"), 0600)
		s.runJob(ctx, &j)
		if j.State != "failed" {
			t.Fatal(j.State, j.Error)
		}
		if data, _ := os.ReadFile(path); string(data) != "changed" {
			t.Fatal("changed source lost")
		}
	})
	t.Run("published-drive-to-local-resumes-trash-only", func(t *testing.T) {
		cloud.add("move-out", "move-out.dat", "application/octet-stream", false)
		path := filepath.Join(s.config.Roots[0].Path, "move-out.dat")
		os.WriteFile(path, []byte("abcdef"), 0600)
		entries, _, err := s.driveTree(ctx, "move-out", "fixture-account", false)
		if err != nil {
			t.Fatal(err)
		}
		root, _ := s.root("vps")
		snapshot, err := snapshotMove(ctx, root, "move-out.dat")
		root.Close()
		if err != nil {
			t.Fatal(err)
		}
		j := submit(CopyRequest{Operation: "move", Items: []Item{{Source: "drive", Path: "move-out"}}, Destination: Item{Source: "vps", Path: "."}})
		j.Move = &MoveState{Published: true, Selections: []MoveSelection{{Source: "drive", Path: "move-out", To: "move-out.dat", DriveEntries: entries, DestinationEntries: snapshot}}}
		if err = s.saveMove(&j); err != nil {
			t.Fatal(err)
		}
		cloud.loseTrash = true
		s.runJob(ctx, &j)
		if j.State != "waiting" || !cloud.items["move-out"].Trashed {
			t.Fatal("lost response did not preserve cleanup journal", j.State, j.Error)
		}
		patches := cloud.patches
		if err = s.read(j.ID, "copy", &j); err != nil {
			t.Fatal(err)
		}
		s.runJob(ctx, &j)
		if j.State != "completed" || !cloud.items["move-out"].Trashed {
			t.Fatal(j.State, j.Error)
		}
		s.runJob(ctx, &j)
		if j.State != "completed" || cloud.patches != patches {
			t.Fatal("duplicate trash on resume", j.Error)
		}
	})
	t.Run("cancelled-copy-retains-local-source", func(t *testing.T) {
		name := "cancel-drive-move.dat"
		path := filepath.Join(s.config.Roots[0].Path, name)
		os.WriteFile(path, []byte("original"), 0600)
		j := submit(CopyRequest{Operation: "move", Items: []Item{{Source: "vps", Path: name}}, Destination: Item{Source: "drive", Path: "root"}})
		cloud.lostChunk = true
		s.runJob(ctx, &j)
		j.Cancel = true
		s.save(j.ID, "copy", j.State, j)
		s.runJob(ctx, &j)
		if j.State != "cancelled" {
			t.Fatal(j.State, j.Error)
		}
		if data, _ := os.ReadFile(path); string(data) != "original" {
			t.Fatal("cancel removed source")
		}
	})
	for _, change := range []string{"source", "destination"} {
		t.Run("changed-"+change+"-prevents-drive-removal", func(t *testing.T) {
			id := "changed-" + change
			name := id + ".dat"
			cloud.add(id, name, "application/octet-stream", false)
			path := filepath.Join(s.config.Roots[0].Path, name)
			os.WriteFile(path, []byte("abcdef"), 0600)
			entries, _, _ := s.driveTree(ctx, id, "fixture-account", false)
			root, _ := s.root("vps")
			snapshot, err := snapshotMove(ctx, root, name)
			root.Close()
			if err != nil {
				t.Fatal(err)
			}
			j := submit(CopyRequest{Operation: "move", Items: []Item{{Source: "drive", Path: id}}, Destination: Item{Source: "vps", Path: "."}})
			j.Move = &MoveState{Published: true, Selections: []MoveSelection{{Source: "drive", Path: id, To: name, DriveEntries: entries, DestinationEntries: snapshot}}}
			s.saveMove(&j)
			if change == "destination" {
				os.WriteFile(path, []byte("changed"), 0600)
			} else {
				item := cloud.items[id]
				item.Version = "2"
				cloud.items[id] = item
			}
			s.runJob(ctx, &j)
			if j.State != "failed" || cloud.items[id].Trashed {
				t.Fatal("modified data removed", j.State, j.Error)
			}
		})
	}
}
