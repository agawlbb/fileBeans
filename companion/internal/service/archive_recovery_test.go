package service

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestArchiveRecoveryPreservesBothVersionsAndOnlySelectedJobs(t *testing.T) {
	library, archive, _ := archivePeers(t)
	link := ArchiveLink{ID: ID(), Library: ArchiveEndpoint{Source: "VPS", Path: "/photos"}, Archive: ArchiveEndpoint{Source: "Samba", Path: "/photos"}}
	for _, s := range []*Service{library, archive} {
		os.MkdirAll(filepath.Join(s.config.Roots[0].Path, "photos"), 0770)
	}
	library.insert(link.ID, "archiveLink", "linked", "", link)
	library.cacheArchivePolicies([]ArchiveLink{link})
	original := []byte("canonical Pi original")
	retained := []byte("different VPS retained version")
	visible := []byte("current encoded VPS file")
	os.WriteFile(filepath.Join(archive.config.Roots[0].Path, "photos/photo.jpg"), original, 0660)
	os.WriteFile(filepath.Join(library.config.Roots[0].Path, "photos/photo.jpg"), visible, 0660)
	j := newArchiveTask(link, "ingest", "photo.jpg")
	j.State = "backup"
	j.Output = "photo.jpg"
	raw := filepath.Join("photos", archiveStage, j.ID, "original")
	os.MkdirAll(filepath.Join(library.config.Roots[0].Path, filepath.Dir(raw)), 0770)
	os.WriteFile(filepath.Join(library.config.Roots[0].Path, raw), retained, 0660)
	root, _ := library.root("VPS")
	j.Hash, _ = archiveHash(root, raw)
	root.Close()
	library.insert(j.ID, archiveTaskKind, j.State, "", j)
	untouched := newArchiveTask(link, "ingest", "unrelated.jpg")
	untouched.Next = time.Now().Add(time.Hour).Unix()
	library.insert(untouched.ID, archiveTaskKind, untouched.State, "", untouched)
	report, err := library.ReconcileArchiveRetentions(context.Background(), ArchiveRepairPlan{Link: link.ID, Tasks: []string{j.ID}})
	if err != nil || report.Completed != 1 || report.PreservedVersions != 1 || report.Remaining != 0 {
		t.Fatal(err, report)
	}
	got, _ := os.ReadFile(filepath.Join(archive.config.Roots[0].Path, "photos/photo.jpg"))
	if !bytes.Equal(got, original) {
		t.Fatal("canonical original changed")
	}
	got, _ = os.ReadFile(filepath.Join(library.config.Roots[0].Path, "photos/photo.jpg"))
	if !bytes.Equal(got, visible) {
		t.Fatal("visible encoded file changed")
	}
	dir := filepath.Join(archive.config.Roots[0].Path, "photos/.filebeans-stage/archive-recovery", j.ID)
	got, err = os.ReadFile(filepath.Join(dir, "photo.jpg"))
	if err != nil || !bytes.Equal(got, retained) {
		t.Fatal("differing version lost", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "details.json"))
	if err != nil || !json.Valid(data) {
		t.Fatal("recovery metadata absent", err)
	}
	if _, err = os.Stat(filepath.Join(library.config.Roots[0].Path, raw)); !os.IsNotExist(err) {
		t.Fatal("verified retention not released", err)
	}
	var found archiveTask
	if err = library.read(untouched.ID, archiveTaskKind, &found); err != nil {
		t.Fatal("unselected intent changed", err)
	}
}

func TestArchiveRecoveryFailureRetainsVPSBytes(t *testing.T) {
	library, archive, _ := archivePeers(t)
	link := ArchiveLink{ID: ID(), Library: ArchiveEndpoint{Source: "VPS", Path: "/photos"}, Archive: ArchiveEndpoint{Source: "Samba", Path: "/photos"}}
	os.MkdirAll(filepath.Join(library.config.Roots[0].Path, "photos"), 0770)
	os.MkdirAll(filepath.Join(archive.config.Roots[0].Path, "photos"), 0770)
	library.insert(link.ID, "archiveLink", "linked", "", link)
	library.cacheArchivePolicies([]ArchiveLink{link})
	j := newArchiveTask(link, "ingest", "photo.jpg")
	j.State = "backup"
	j.Hash = "invalid-source-hash"
	raw := filepath.Join("photos", archiveStage, j.ID, "original")
	os.MkdirAll(filepath.Join(library.config.Roots[0].Path, filepath.Dir(raw)), 0770)
	os.WriteFile(filepath.Join(library.config.Roots[0].Path, raw), []byte("retained"), 0660)
	os.WriteFile(filepath.Join(archive.config.Roots[0].Path, "photos/photo.jpg"), []byte("canonical"), 0660)
	library.insert(j.ID, archiveTaskKind, j.State, "", j)
	report, err := library.ReconcileArchiveRetentions(context.Background(), ArchiveRepairPlan{Link: link.ID, Tasks: []string{j.ID}})
	if err != nil || report.Remaining != 1 || report.Completed != 0 {
		t.Fatal(err, report)
	}
	if data, err := os.ReadFile(filepath.Join(library.config.Roots[0].Path, raw)); err != nil || string(data) != "retained" {
		t.Fatal("unverified retention lost", err)
	}
}

func TestArchiveRecoveryClearsOnlyUnchangedUnstartedDiscovery(t *testing.T) {
	library, archive, _ := archivePeers(t)
	link := ArchiveLink{ID: ID(), Library: ArchiveEndpoint{Source: "VPS", Path: "/photos"}, Archive: ArchiveEndpoint{Source: "Samba", Path: "/photos"}}
	for _, s := range []*Service{library, archive} {
		os.MkdirAll(filepath.Join(s.config.Roots[0].Path, "photos"), 0770)
	}
	library.insert(link.ID, "archiveLink", "linked", "", link)
	library.cacheArchivePolicies([]ArchiveLink{link})
	ids := []string{}
	for _, name := range []string{"paired.jpg", "changed.jpg", "started.jpg"} {
		local := filepath.Join(library.config.Roots[0].Path, "photos", name)
		os.WriteFile(local, []byte("visible encoded"), 0660)
		os.WriteFile(filepath.Join(archive.config.Roots[0].Path, "photos", name), []byte("archive original"), 0660)
		j := newArchiveTask(link, "ingest", name)
		st, _ := os.Stat(local)
		j.Version = version(st)
		if name == "changed.jpg" {
			j.Version = "stale"
		}
		if name == "started.jpg" {
			os.MkdirAll(filepath.Join(library.config.Roots[0].Path, "photos", archiveStage, j.ID), 0770)
		}
		library.insert(j.ID, archiveTaskKind, j.State, "", j)
		ids = append(ids, j.ID)
	}
	report, err := library.ReconcileArchiveRetentions(context.Background(), ArchiveRepairPlan{Link: link.ID, Tasks: ids})
	if err != nil || report.Completed != 1 || report.ClearedDiscovery != 1 || report.Remaining != 2 {
		t.Fatal(err, report)
	}
	for _, name := range []string{"paired.jpg", "changed.jpg", "started.jpg"} {
		got, _ := os.ReadFile(filepath.Join(library.config.Roots[0].Path, "photos", name))
		if string(got) != "visible encoded" {
			t.Fatal("visible file changed")
		}
		got, _ = os.ReadFile(filepath.Join(archive.config.Roots[0].Path, "photos", name))
		if string(got) != "archive original" {
			t.Fatal("archive original changed")
		}
	}
}
