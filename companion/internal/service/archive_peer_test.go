package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func archivePeers(t *testing.T) (*Service, *Service, *archivePeerRoot) {
	t.Helper()
	library := testNode(t, "VPS")
	archive := testNode(t, "Samba")
	archive.config.ArchivePeerKey = strings.Repeat("server-only-", 4)
	archive.config.Profile = "pi"
	archive.config.ArchiveCoordinator = "VPS"
	server := httptest.NewTLSServer(archive.Handler())
	t.Cleanup(server.Close)
	libraryServer := httptest.NewTLSServer(library.Handler())
	t.Cleanup(libraryServer.Close)
	archive.config.Peers = []PeerConfig{testPeer(libraryServer, "VPS", "VPS", library.config.Token)}
	p := testPeer(server, "pi", "Samba", archive.config.Token)
	p.ArchiveKey = archive.config.ArchivePeerKey
	library.config.Peers = []PeerConfig{p}
	return library, archive, &archivePeerRoot{peer: p, source: "Samba"}
}
func TestArchivePeerPublicationRetryConflictAndAuthentication(t *testing.T) {
	library, archive, remote := archivePeers(t)
	data := bytes.Repeat([]byte("retained-original"), 40000)
	if e := os.WriteFile(filepath.Join(library.config.Roots[0].Path, "original"), data, 0600); e != nil {
		t.Fatal(e)
	}
	src, e := library.root("VPS")
	if e != nil {
		t.Fatal(e)
	}
	defer src.Close()
	stage := filepath.Join("originals", archiveStage, ID(), "copy")
	for i := 0; i < 2; i++ {
		if e = library.copyArchiveOriginal(context.Background(), src, "original", remote, "originals/photo.png", "", stage); e != nil {
			t.Fatal(i, e)
		}
	}
	got, e := os.ReadFile(filepath.Join(archive.config.Roots[0].Path, "originals/photo.png"))
	if e != nil || !bytes.Equal(got, data) {
		t.Fatal("publication", e)
	}
	if _, e = os.Stat(filepath.Join(archive.config.Roots[0].Path, stage)); !os.IsNotExist(e) {
		t.Fatal("staging retained", e)
	}
	if e = os.WriteFile(filepath.Join(library.config.Roots[0].Path, "original"), []byte("different"), 0600); e != nil {
		t.Fatal(e)
	}
	if e = library.copyArchiveOriginal(context.Background(), src, "original", remote, "originals/photo.png", "", stage); !errors.Is(e, ErrConflict) {
		t.Fatal("destination replaced", e)
	}
	got, _ = os.ReadFile(filepath.Join(archive.config.Roots[0].Path, "originals/photo.png"))
	if !bytes.Equal(got, data) {
		t.Fatal("conflict changed original")
	}
	bad := *remote
	bad.peer.ArchiveKey = ""
	if e = bad.RemoveAll("originals"); !errors.Is(e, ErrAuthorization) {
		t.Fatal("client token granted archive privilege", e)
	}
	if e = remote.RemoveAll("."); !errors.Is(e, ErrUnsafe) {
		t.Fatal("source root removed", e)
	}
	if e = remote.MkdirAll("../escape", 0700); !errors.Is(e, ErrUnsafe) {
		t.Fatal("path escaped", e)
	}
}
func TestArchivePeerBadHashAndShortStreamLeaveOriginalUntouched(t *testing.T) {
	_, archive, remote := archivePeers(t)
	for _, tc := range []struct {
		size int64
		data string
		hash string
	}{{8, "short", strings.Repeat("0", 64)}, {4, "data", strings.Repeat("0", 64)}} {
		in := archivePeerRequest{Source: "Samba", Path: "archive/picture.png", Stage: filepath.Join("archive", archiveStage, ID(), "copy"), Size: tc.size, Hash: tc.hash}
		b, _ := json.Marshal(in)
		resp, e := peerRequest(context.Background(), remote.peer, "POST", "/v1/peer/archive-content", nil, ioReader(b, tc.data))
		if e != nil {
			t.Fatal(e)
		}
		var out archivePeerResponse
		e = json.NewDecoder(resp.Body).Decode(&out)
		resp.Body.Close()
		if e != nil || out.Error == "" {
			t.Fatal("invalid stream published", e, out)
		}
		for _, p := range []string{in.Path, in.Stage} {
			if _, e = os.Stat(filepath.Join(archive.config.Roots[0].Path, p)); !os.IsNotExist(e) {
				t.Fatal("partial publication", p, e)
			}
		}
	}
}
func ioReader(metadata []byte, data string) *bytes.Reader {
	return bytes.NewReader(append(append(metadata, '\n'), []byte(data)...))
}

func TestArchivePeerNamesMoveTrashAndRestore(t *testing.T) {
	library, archive, remote := archivePeers(t)
	for _, s := range []*Service{library, archive} {
		if e := os.MkdirAll(filepath.Join(s.config.Roots[0].Path, "photos"), 0700); e != nil {
			t.Fatal(e)
		}
	}
	for _, s := range []*Service{library, archive} {
		if e := os.WriteFile(filepath.Join(s.config.Roots[0].Path, "photos/pic.jpg"), []byte("original"), 0600); e != nil {
			t.Fatal(e)
		}
	}
	link := ArchiveLink{ID: ID(), Library: ArchiveEndpoint{Source: "VPS", Path: "/photos"}, Archive: ArchiveEndpoint{Source: "Samba", Path: "/photos"}}
	if e := library.insert(link.ID, "archiveLink", "linked", "", link); e != nil {
		t.Fatal(e)
	}
	library.cacheArchivePolicies([]ArchiveLink{link})
	root, e := library.root("VPS")
	if e != nil {
		t.Fatal(e)
	}
	defer root.Close()
	if drift, e := archiveDrift(context.Background(), root, "photos", remote, "photos"); e != nil || drift {
		t.Fatal("baseline", drift, e)
	}
	if e = renameExclusive(remote, "photos/pic.jpg", "photos/new.jpg"); e != nil {
		t.Fatal(e)
	}
	if e = renameExclusive(root, "photos/pic.jpg", "photos/new.jpg"); e != nil {
		t.Fatal(e)
	}
	if matched, e := archiveMatch(remote, "photos", "new.png", false); e != nil || matched != "photos/new.jpg" {
		t.Fatal("stem match", matched, e)
	}
	trash, e := library.trash(Item{Source: "VPS", Path: "photos/new.jpg"}, "test-trash")
	if e != nil {
		t.Fatal(e)
	}
	tasks, e := library.archiveTasks()
	if e != nil {
		t.Fatal(e)
	}
	var job *archiveTask
	for i := range tasks {
		if tasks[i].Kind == "trash" && tasks[i].TrashID == trash.ID {
			job = &tasks[i]
		}
	}
	if job == nil {
		t.Fatal("trash not journaled")
	}
	if e = library.runArchiveTrash(context.Background(), job); e != nil {
		t.Fatal("remote pair", e)
	}
	if job.ArchiveTrashID == "" || job.State != "paired" {
		t.Fatal("missing remote receipt")
	}
	var remoteTrash Trash
	if e = archive.read(job.ArchiveTrashID, "trash", &remoteTrash); e != nil || remoteTrash.State != "retained" {
		t.Fatal(remoteTrash, e)
	}
	if e = library.restore(trash.ID); e != nil {
		t.Fatal("library restore", e)
	}
	if e = library.runArchiveTrash(context.Background(), job); e != nil {
		t.Fatal("paired restore", e)
	}
	if b, e := os.ReadFile(filepath.Join(archive.config.Roots[0].Path, "photos/new.jpg")); e != nil || string(b) != "original" {
		t.Fatal("archive restore", e)
	}
}
func TestArchivePeerRPCNeverAvailableWithClientToken(t *testing.T) {
	_, archive, _ := archivePeers(t)
	for _, path := range []string{"/v1/peer/archive", "/v1/peer/archive-content"} {
		r := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{"operation":"remove-all","source":"Samba","path":"photos"}`))
		r.Header.Set("Authorization", "Bearer "+archive.config.Token)
		w := httptest.NewRecorder()
		archive.Handler().ServeHTTP(w, r)
		if w.Code != 401 && w.Code != 403 {
			t.Fatal(path, w.Code)
		}
	}
}

func TestArchivePeerLivePolicyMirrorsAndUnlink(t *testing.T) {
	library, archive, _ := archivePeers(t)
	for _, s := range []*Service{library, archive} {
		if e := os.MkdirAll(filepath.Join(s.config.Roots[0].Path, "photos"), 0700); e != nil {
			t.Fatal(e)
		}
	}
	w := archiveRequest(library, "POST", "/v1/archive-links", `{"library":{"source":"VPS","path":"/photos"},"archive":{"source":"Samba","path":"/photos"}}`)
	if w.Code != 201 {
		t.Fatal("create", w.Code, w.Body.String())
	}
	var link ArchiveLink
	if e := json.Unmarshal(w.Body.Bytes(), &link); e != nil {
		t.Fatal(e)
	}
	root, e := archive.root("Samba")
	if e != nil {
		t.Fatal(e)
	}
	defer root.Close()
	if e = root.WriteFile("photos/forbidden.jpg", []byte("blocked"), 0600); !errors.Is(e, ErrArchiveReadOnly) {
		t.Fatal("mirror write guard", e)
	}
	restarted := &Service{config: archive.config, db: archive.db, vaults: archive.vaults}
	if e = restarted.guardArchiveWrite("Samba", "photos/sub/file", false); !errors.Is(e, ErrArchiveReadOnly) {
		t.Fatal("restart lost guard", e)
	}
	w = archiveRequest(library, "DELETE", "/v1/archive-links/"+link.ID, "")
	if w.Code < 200 || w.Code >= 300 {
		t.Fatal("unlink", w.Code, w.Body.String())
	}
	if e = root.WriteFile("photos/allowed.jpg", []byte("allowed"), 0600); e != nil {
		t.Fatal("stale mirror after unlink", e)
	}
}

func TestArchivePeerIngestKeepsEncodingOnLibraryAndVerifiesOriginal(t *testing.T) {
	library, archive, _ := archivePeers(t)
	library.config.Filesystem.FFmpeg = "/usr/bin/ffmpeg"
	library.config.Normalization.FFmpeg = "/usr/bin/ffmpeg"
	archive.config.Filesystem.FFmpeg = "/archive-owner-must-not-encode"
	// Admission is deterministic; the actual VM container remains capped at 1.5 GiB.
	library.archiveMemory.read = func() (archiveMemorySample, error) {
		return archiveMemorySample{available: 8 << 30, used: 32 << 20, limit: 4 << 30}, nil
	}
	for _, s := range []*Service{library, archive} {
		if e := os.MkdirAll(filepath.Join(s.config.Roots[0].Path, "photos"), 0700); e != nil {
			t.Fatal(e)
		}
	}
	img := image.NewRGBA(image.Rect(0, 0, 96, 64))
	for y := 0; y < 64; y++ {
		for x := 0; x < 96; x++ {
			img.Set(x, y, color.RGBA{uint8(x), uint8(y), 100, 255})
		}
	}
	var original bytes.Buffer
	if e := png.Encode(&original, img); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(filepath.Join(library.config.Roots[0].Path, "photos/source.png"), original.Bytes(), 0600); e != nil {
		t.Fatal(e)
	}
	w := archiveRequest(library, "POST", "/v1/archive-links", `{"library":{"source":"VPS","path":"/photos"},"archive":{"source":"Samba","path":"/photos"}}`)
	if w.Code != 201 {
		t.Fatal(w.Code, w.Body.String())
	}
	if e := library.archiveAdded("VPS", "photos/source.png"); e != nil {
		t.Fatal(e)
	}
	tasks, e := library.archiveTasks()
	if e != nil || len(tasks) != 1 {
		t.Fatal(tasks, e)
	}
	job := tasks[0]
	job.Next = 0
	if e = library.saveArchiveTask(&job); e != nil {
		t.Fatal(e)
	}
	if e = library.runArchiveIngest(context.Background(), &job, true); e != nil {
		t.Fatal("library encode", e)
	}
	if job.State != "backup" {
		t.Fatal("missing retained original", job.State)
	}
	retained := filepath.Join(library.config.Roots[0].Path, "photos", archiveStage, job.ID, "original")
	if b, e := os.ReadFile(retained); e != nil || !bytes.Equal(b, original.Bytes()) {
		t.Fatal("original lost before verification", e)
	}
	if e = library.runArchiveIngest(context.Background(), &job, false); e != nil {
		t.Fatal("server backup", e)
	}
	b, e := os.ReadFile(filepath.Join(archive.config.Roots[0].Path, "photos/source.png"))
	if e != nil || !bytes.Equal(b, original.Bytes()) {
		t.Fatal("original checksum", e)
	}
	if _, e = os.Stat(retained); !os.IsNotExist(e) {
		t.Fatal("retained original cleanup", e)
	}
	links, e := library.archiveLinks()
	if e != nil || len(links) != 1 {
		t.Fatal("archive link removed", e)
	}
	if e = archive.guardArchiveWrite("Samba", "photos/source.png", false); !errors.Is(e, ErrArchiveReadOnly) {
		t.Fatal("archive became writable", e)
	}
	tasks, e = library.archiveTasks()
	if e != nil || len(tasks) != 0 {
		t.Fatal("unfinished ingest", tasks, e)
	}
}

func TestArchiveMirrorKeepsCoordinatorBytesWithEqualRootNames(t *testing.T) {
	library, archive, _ := archivePeers(t)
	library.config.Roots[0].Name = "Files"
	archive.config.Roots[0].Name = "Files"
	library.config.Peers[0].Sources = []string{"Files"}
	archive.config.Peers[0].Sources = []string{"Files"}
	for _, s := range []*Service{library, archive} {
		if err := os.MkdirAll(filepath.Join(s.config.Roots[0].Path, "photos"), 0700); err != nil {
			t.Fatal(err)
		}
	}
	w := archiveRequest(library, "POST", "/v1/archive-links", `{"library":{"source":"Files","path":"/photos"},"archive":{"server":"pi","source":"Files","path":"/photos"}}`)
	if w.Code != 201 {
		t.Fatal(w.Code, w.Body.String())
	}
	var link ArchiveLink
	if err := json.Unmarshal(w.Body.Bytes(), &link); err != nil {
		t.Fatal(err)
	}
	if link.Library.ServerID != library.config.ServerID || link.Archive.ServerID != "pi" || link.Archive.Source != "Files" {
		t.Fatal("public owner coordinates lost", link)
	}
	var original, mirror []byte
	if err := library.db.QueryRow("SELECT body FROM fb_operations WHERE id=?", link.ID).Scan(&original); err != nil {
		t.Fatal(err)
	}
	if err := archive.db.QueryRow("SELECT body FROM fb_operations WHERE id=?", link.ID).Scan(&mirror); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(original, mirror) {
		t.Fatal("mirror changed original link bytes")
	}
	for _, cached := range []bool{false, true} {
		var links []ArchiveLink
		var err error
		if cached {
			links, err = archive.cachedArchivePolicies()
		} else {
			links, err = archive.archiveLinks()
		}
		if err != nil || len(links) != 1 {
			t.Fatal(cached, links, err)
		}
		if links[0].Library.Source != "peer:VPS:Files" || links[0].Archive.Source != "Files" {
			t.Fatal("mirror owner coordinates lost", cached, links)
		}
	}
	if err := archive.guardArchiveWrite("Files", "photos/blocked", false); !errors.Is(err, ErrArchiveReadOnly) {
		t.Fatal("archive write guard bypassed", err)
	}
	if err := library.guardArchiveWrite("Files", "photos/allowed", false); err != nil {
		t.Fatal("library became read-only", err)
	}
}
