package service

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestArchivePeerExistingOriginalAvoidsUpload(t *testing.T) {
	library, archive, remote := archivePeers(t)
	var uploads atomic.Int64
	next := archive.Handler()
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/peer/archive-content" {
			uploads.Add(1)
		}
		next.ServeHTTP(w, r)
	}))
	defer server.Close()
	p := testPeer(server, "pi", "Samba", archive.config.Token)
	p.ArchiveKey = archive.config.ArchivePeerKey
	remote.peer = p
	remote.source = "peer:pi:Samba"
	data := bytes.Repeat([]byte("original"), 1024)
	srcPath := filepath.Join(library.config.Roots[0].Path, "original")
	dstPath := filepath.Join(archive.config.Roots[0].Path, "photo.png")
	if err := os.WriteFile(srcPath, data, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dstPath, data, 0600); err != nil {
		t.Fatal(err)
	}
	src, err := library.root("VPS")
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()
	stage := filepath.Join(archiveStage, ID(), "copy")
	if err = library.copyArchiveOriginal(context.Background(), src, "original", remote, "photo.png", "", stage); err != nil {
		t.Fatal(err)
	}
	if uploads.Load() != 0 {
		t.Fatal("matching original retransmitted")
	}
	other := bytes.Repeat([]byte("different"), len(data)/9+1)[:len(data)]
	os.WriteFile(dstPath, other, 0600)
	if err = library.copyArchiveOriginal(context.Background(), src, "original", remote, "photo.png", "", stage); !errors.Is(err, ErrConflict) {
		t.Fatal("same-size differing archive accepted", err)
	}
	if uploads.Load() != 0 {
		t.Fatal("conflicting original uploaded")
	}
	got, _ := os.ReadFile(dstPath)
	if !bytes.Equal(got, other) {
		t.Fatal("archive conflict overwritten")
	}
	os.Remove(dstPath)
	alternate := filepath.Join(archive.config.Roots[0].Path, "photo.jpg")
	if err = os.WriteFile(alternate, other, 0600); err != nil {
		t.Fatal(err)
	}
	if err = library.copyArchiveOriginal(context.Background(), src, "original", remote, "photo.png", "", stage); !errors.Is(err, ErrConflict) {
		t.Fatal("alternate archive extension was not recognized", err)
	}
	if uploads.Load() != 0 {
		t.Fatal("conflicting alternate extension retransmitted")
	}
	os.Remove(alternate)
	if err = library.copyArchiveOriginal(context.Background(), src, "original", remote, "photo.png", "", stage); err != nil {
		t.Fatal("qualified-source missing original copy", err)
	}
	if uploads.Load() != 1 {
		t.Fatal("missing original was not copied", uploads.Load())
	}
	got, _ = os.ReadFile(dstPath)
	if !bytes.Equal(got, data) {
		t.Fatal("new original differs")
	}
}

func TestArchivePeerPermissionErrorIsNotReportedAsMountFailure(t *testing.T) {
	w := httptest.NewRecorder()
	archivePeerReply(w, nil, os.ErrPermission)
	if !strings.Contains(w.Body.String(), "permission") {
		t.Fatal(w.Body.String())
	}
	if err := decodeArchivePeer(archivePeerResponse{Error: "permission"}, nil); !errors.Is(err, os.ErrPermission) || errors.Is(err, ErrMount) {
		t.Fatal(err)
	}
}

func TestArchiveBackupRetryAfterRestartVerifiesExistingCopy(t *testing.T) {
	library, archive, _ := archivePeers(t)
	var data bytes.Buffer
	png.Encode(&data, image.NewRGBA(image.Rect(0, 0, 8, 8)))
	link := ArchiveLink{ID: ID(), Library: ArchiveEndpoint{Source: "VPS", Path: "/photos"}, Archive: ArchiveEndpoint{Source: "Samba", Path: "/archive"}}
	for _, pair := range []struct{ base, dir string }{{library.config.Roots[0].Path, "photos"}, {archive.config.Roots[0].Path, "archive"}} {
		os.MkdirAll(filepath.Join(pair.base, pair.dir), 0770)
		os.WriteFile(filepath.Join(pair.base, pair.dir, "photo.png"), data.Bytes(), 0660)
	}
	if err := library.insert(link.ID, "archiveLink", "linked", "", link); err != nil {
		t.Fatal(err)
	}
	library.cacheArchivePolicies([]ArchiveLink{link})
	j := newArchiveTask(link, "ingest", "photo.png")
	j.State = "backup"
	j.Output = "photo.png"
	j.Next = time.Now().Add(time.Hour).Unix()
	j.Error = "Samba mount unavailable; waiting to retry"
	raw := filepath.Join("photos", archiveStage, j.ID, "original")
	os.MkdirAll(filepath.Join(library.config.Roots[0].Path, filepath.Dir(raw)), 0770)
	os.WriteFile(filepath.Join(library.config.Roots[0].Path, raw), data.Bytes(), 0660)
	root, err := library.root("VPS")
	if err != nil {
		t.Fatal(err)
	}
	j.Hash, err = archiveHash(root, raw)
	root.Close()
	if err != nil {
		t.Fatal(err)
	}
	if err = library.insert(j.ID, archiveTaskKind, j.State, "", j); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); library.archiveSyncLoop(ctx) }()
	defer func() { cancel(); <-done }()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		tasks, err := library.archiveTasks()
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, task := range tasks {
			if task.ID == j.ID {
				found = true
			}
		}
		if !found {
			original, err := os.ReadFile(filepath.Join(archive.config.Roots[0].Path, "archive/photo.png"))
			if err != nil || !bytes.Equal(original, data.Bytes()) {
				t.Fatal("verified original missing", err)
			}
			if _, err = os.Stat(filepath.Join(library.config.Roots[0].Path, raw)); !os.IsNotExist(err) {
				t.Fatal("verified retention not released", err)
			}
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("old backup remained stuck behind retry backoff")
}
