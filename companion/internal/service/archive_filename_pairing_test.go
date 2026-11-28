package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

func filenamePairFixture(t *testing.T) (*Service, *Service, ArchiveLink) {
	t.Helper()
	library, archive, _ := archivePeers(t)
	for _, s := range []*Service{library, archive} {
		if err := os.MkdirAll(filepath.Join(s.config.Roots[0].Path, "photos"), 0700); err != nil {
			t.Fatal(err)
		}
	}
	link := ArchiveLink{ID: ID(), Library: ArchiveEndpoint{Source: "VPS", Path: "photos"}, Archive: ArchiveEndpoint{Source: "Samba", Path: "photos"}}
	if err := library.insert(link.ID, "archiveLink", "linked", "", link); err != nil {
		t.Fatal(err)
	}
	library.cacheArchivePolicies([]ArchiveLink{link})
	return library, archive, link
}

func TestArchiveIngestDeduplicatesFilenameAcrossMountsAndExtensions(t *testing.T) {
	library, _, link := filenamePairFixture(t)
	path := filepath.Join(library.config.Roots[0].Path, "photos/photo.jpg")
	if err := os.WriteFile(path, []byte("existing output"), 0600); err != nil {
		t.Fatal(err)
	}
	old := newArchiveTask(link, "ingest", "photo.png")
	old.State = "backup"
	old.Identity = "87:123"
	old.Output = "photo.jpg"
	old.Relative = "photo.jpg"
	if err := library.insert(old.ID, archiveTaskKind, old.State, "legacy-key", old); err != nil {
		t.Fatal(err)
	}
	if err := library.archiveAdded("VPS", "photos/photo.jpg"); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(path, filepath.Join(filepath.Dir(path), "photo.jpeg")); err != nil {
		t.Fatal(err)
	}
	if err := library.archiveAdded("VPS", "photos/photo.jpeg"); err != nil {
		t.Fatal(err)
	}
	tasks, err := library.archiveTasks()
	if err != nil || len(tasks) != 1 || tasks[0].ID != old.ID {
		t.Fatalf("filename duplicated across remount/extension: %+v %v", tasks, err)
	}
}

func TestArchiveWaitingBaselineSkipsEncodingForDifferentOriginalBytes(t *testing.T) {
	library, archive, link := filenamePairFixture(t)
	vps := filepath.Join(library.config.Roots[0].Path, "photos/photo.jpg")
	master := filepath.Join(archive.config.Roots[0].Path, "photos/photo.png")
	os.WriteFile(vps, []byte("encoded VPS version"), 0600)
	os.WriteFile(master, []byte("Pi master original"), 0600)
	// Model a legacy intent admitted while its archive was not visible.
	j := newArchiveTask(link, "ingest", "photo.jpg")
	j.Identity = "old-mount:1"
	j.Next = time.Now().Unix()
	if err := library.insert(j.ID, archiveTaskKind, j.State, "legacy-waiting", j); err != nil {
		t.Fatal(err)
	}
	if err := library.runArchiveIngest(context.Background(), &j, true); err != nil {
		t.Fatal("existing filename was processed", err)
	}
	tasks, err := library.archiveTasks()
	if err != nil || len(tasks) != 0 {
		t.Fatal("paired intent retained", tasks, err)
	}
	for path, want := range map[string]string{vps: "encoded VPS version", master: "Pi master original"} {
		got, err := os.ReadFile(path)
		if err != nil || string(got) != want {
			t.Fatal("paired bytes changed", path, err)
		}
	}
	if _, err := os.Stat(filepath.Join(library.config.Roots[0].Path, "photos", archiveStage, j.ID)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("paired file created processing stage", err)
	}
}

func TestArchiveWaitingPairPreservesInterruptedRetention(t *testing.T) {
	library, archive, link := filenamePairFixture(t)
	os.WriteFile(filepath.Join(library.config.Roots[0].Path, "photos/photo.jpg"), []byte("visible"), 0600)
	os.WriteFile(filepath.Join(archive.config.Roots[0].Path, "photos/photo.png"), []byte("master"), 0600)
	j := newArchiveTask(link, "ingest", "photo.jpg")
	raw := filepath.Join(library.config.Roots[0].Path, "photos", archiveStage, j.ID, "original")
	os.MkdirAll(filepath.Dir(raw), 0700)
	os.WriteFile(raw, []byte("retained input"), 0600)
	library.insert(j.ID, archiveTaskKind, j.State, "retained", j)
	if err := library.runArchiveIngest(context.Background(), &j, true); !errors.Is(err, ErrConflict) {
		t.Fatal("interrupted retention not protected", err)
	}
	got, err := os.ReadFile(raw)
	if err != nil || string(got) != "retained input" {
		t.Fatal("retained bytes lost", err)
	}
	tasks, err := library.archiveTasks()
	if err != nil || len(tasks) != 1 {
		t.Fatal("retention journal removed", err)
	}
}

func TestArchiveDiscoveryDoesNotAdmitFilesWhenArchiveDisappears(t *testing.T) {
	library, archive, link := filenamePairFixture(t)
	for _, s := range []*Service{library, archive} {
		os.WriteFile(filepath.Join(s.config.Roots[0].Path, "photos/photo.jpg"), []byte("baseline"), 0600)
	}
	next := archive.Handler()
	parked := false
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var in archivePeerRequest
		if r.URL.Path == "/v1/peer/archive" {
			json.NewDecoder(r.Body).Decode(&in)
			body, _ := json.Marshal(in)
			r.Body = io.NopCloser(bytes.NewReader(body))
		}
		next.ServeHTTP(w, r)
		if in.Operation == "names" && !parked {
			parked = true
			if err := os.Rename(filepath.Join(archive.config.Roots[0].Path, "photos"), filepath.Join(archive.config.Roots[0].Path, "parked")); err != nil {
				t.Error(err)
			}
		}
	}))
	defer server.Close()
	peer := testPeer(server, "pi", "Samba", archive.config.Token)
	peer.ArchiveKey = archive.config.ArchivePeerKey
	library.config.Peers = []PeerConfig{peer}
	library.discoverArchiveMedia(context.Background())
	tasks, err := library.archiveTasks()
	if err != nil {
		t.Fatal(err)
	}
	for _, j := range tasks {
		if j.Kind == "ingest" {
			t.Fatal("unavailable archive treated as missing master", j)
		}
	}
	if !parked {
		t.Fatal("visibility transition not exercised")
	}
	remote := &archivePeerRoot{peer: peer, source: "Samba"}
	if _, err := archiveBaselineMatch(remote, link.Archive, "photo.jpg"); !errors.Is(err, ErrMount) || errors.Is(err, os.ErrNotExist) {
		t.Fatal("missing root not classified as unavailable", err)
	}
}

func TestArchiveWaitingRequiresAvailableArchiveBeforeProcessing(t *testing.T) {
	library, archive, link := filenamePairFixture(t)
	os.WriteFile(filepath.Join(library.config.Roots[0].Path, "photos/photo.jpg"), []byte("existing VPS"), 0600)
	os.Remove(filepath.Join(archive.config.Roots[0].Path, "photos"))
	j := newArchiveTask(link, "ingest", "photo.jpg")
	library.insert(j.ID, archiveTaskKind, j.State, "offline", j)
	if err := library.runArchiveIngest(context.Background(), &j, true); !errors.Is(err, ErrMount) {
		t.Fatal("offline archive allowed encoding", err)
	}
	if _, err := os.Stat(filepath.Join(library.config.Roots[0].Path, "photos", archiveStage, j.ID)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("offline archive created processing stage", err)
	}
}

func TestArchiveBaselineRejectsOrdinaryPlaceholderForProtectedRoot(t *testing.T) {
	_, archive, remote := archivePeers(t)
	os.MkdirAll(filepath.Join(archive.config.Roots[0].Path, "photos"), 0700)
	os.WriteFile(filepath.Join(archive.config.Roots[0].Path, "photos/photo.jpg"), []byte("ordinary residue"), 0600)
	endpoint := ArchiveEndpoint{Source: "Samba", Path: "photos", Vault: "expected-protected-vault"}
	if _, err := archiveBaselineMatch(remote, endpoint, "photo.jpg"); !errors.Is(err, ErrVaultLocked) {
		t.Fatal("unmounted placeholder accepted as protected baseline", err)
	}
}

func TestArchiveDirectoryDeduplicatesFilenameAcrossMounts(t *testing.T) {
	library, _, link := filenamePairFixture(t)
	if err := os.MkdirAll(filepath.Join(library.config.Roots[0].Path, "photos/nested"), 0700); err != nil {
		t.Fatal(err)
	}
	old := newArchiveTask(link, "mkdir", "nested")
	old.State = "mkdir"
	old.Identity = "87:123"
	if err := library.insert(old.ID, archiveTaskKind, old.State, "legacy-directory", old); err != nil {
		t.Fatal(err)
	}
	if err := library.archiveDirectoryAdded("VPS", "photos/nested"); err != nil {
		t.Fatal(err)
	}
	tasks, err := library.archiveTasks()
	if err != nil || len(tasks) != 1 || tasks[0].ID != old.ID {
		t.Fatalf("directory duplicated across remount: %+v %v", tasks, err)
	}
}

func TestArchiveDiscoveryReusesFilenameInventory(t *testing.T) {
	library, archive, _ := filenamePairFixture(t)
	for _, name := range []string{"one", "two", "three"} {
		os.WriteFile(filepath.Join(library.config.Roots[0].Path, "photos", name+".jpg"), []byte("encoded"), 0600)
		os.WriteFile(filepath.Join(archive.config.Roots[0].Path, "photos", name+".png"), []byte("master"), 0600)
	}
	next := archive.Handler()
	var names, matches atomic.Int64
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/peer/archive" {
			var in archivePeerRequest
			json.NewDecoder(r.Body).Decode(&in)
			if in.Operation == "names" {
				names.Add(1)
			}
			if in.Operation == "match" {
				matches.Add(1)
			}
			body, _ := json.Marshal(in)
			r.Body = io.NopCloser(bytes.NewReader(body))
		}
		next.ServeHTTP(w, r)
	}))
	defer server.Close()
	peer := testPeer(server, "pi", "Samba", archive.config.Token)
	peer.ArchiveKey = archive.config.ArchivePeerKey
	library.config.Peers = []PeerConfig{peer}
	library.discoverArchiveMedia(context.Background())
	tasks, err := library.archiveTasks()
	if err != nil || len(tasks) != 0 {
		t.Fatal("baseline admitted", tasks, err)
	}
	if names.Load() != 1 || matches.Load() != 0 {
		t.Fatalf("baseline caused redundant Pi lookups: names=%d matches=%d", names.Load(), matches.Load())
	}
}
