package service

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestCopyStagePinnedDirectories(t *testing.T) {
	base := t.TempDir()
	root, err := os.OpenRoot(base)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	if err := mkdir(root, "stage"); err != nil {
		t.Fatal(err)
	}
	stage, err := newCopyStage(root, "stage")
	if err != nil {
		t.Fatal(err)
	}
	defer stage.close()
	dir, err := stage.directory("folder")
	if err != nil {
		t.Fatal(err)
	}
	dir.Close()
	// Replace an already cached parent with a symlink. Writes stay on the
	// original directory handle and publication validation rejects the swap.
	if err := os.Rename(filepath.Join(base, "stage/folder"), filepath.Join(base, "original")); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(base, "stage/folder")); err != nil {
		t.Fatal(err)
	}
	file := File{To: "folder/file", Size: 4}
	out, _, err := stage.open(file)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = out.WriteString("safe"); err != nil {
		t.Fatal(err)
	}
	out.Close()
	if _, err := os.Stat(filepath.Join(outside, "file")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("followed replaced parent", err)
	}
	expected := map[string]File{"folder": {To: "folder", Directory: true}, "folder/file": file}
	if err := stage.validate(context.Background(), expected); !errors.Is(err, ErrUnsafe) {
		t.Fatal("published replaced directory", err)
	}
}

func TestCopyStageRejectsAliasesAndSymlinks(t *testing.T) {
	base := t.TempDir()
	root, err := os.OpenRoot(base)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	os.Mkdir(filepath.Join(base, "stage"), 0700)
	stage, err := newCopyStage(root, "stage")
	if err != nil {
		t.Fatal(err)
	}
	defer stage.close()
	out, _, err := stage.open(File{To: "one"})
	if err != nil {
		t.Fatal(err)
	}
	out.Close()
	if err := os.Link(filepath.Join(base, "stage/one"), filepath.Join(base, "stage/two")); err != nil {
		t.Fatal(err)
	}
	if out, _, err := stage.open(File{To: "two"}); !errors.Is(err, ErrConflict) {
		if out != nil {
			out.Close()
		}
		t.Fatal("alias accepted", err)
	}
	os.Symlink("one", filepath.Join(base, "stage/link"))
	if out, _, err := stage.open(File{To: "link"}); !errors.Is(err, ErrUnsafe) {
		if out != nil {
			out.Close()
		}
		t.Fatal("symlink accepted", err)
	}
	os.Symlink("stage", filepath.Join(base, "linked-stage"))
	if linked, err := newCopyStage(root, "linked-stage"); !errors.Is(err, ErrUnsafe) {
		if linked != nil {
			linked.close()
		}
		t.Fatal("stage symlink accepted", err)
	}
}

func TestCopyDirectoryCacheBoundedAndRevalidated(t *testing.T) {
	base := t.TempDir()
	root, err := os.OpenRoot(base)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	os.Mkdir(filepath.Join(base, "stage"), 0700)
	stage, err := newCopyStage(root, "stage")
	if err != nil {
		t.Fatal(err)
	}
	defer stage.close()
	for i := 0; i < 200; i++ {
		dir, err := stage.directory(fmt.Sprintf("folder-%03d", i))
		if err != nil {
			t.Fatal(err)
		}
		dir.Close()
	}
	if len(stage.dirs) > 128 {
		t.Fatal("unbounded directory descriptors", len(stage.dirs))
	}
	if _, ok := stage.dirs["folder-000"]; ok {
		t.Fatal("oldest directory not evicted")
	}
	os.Rename(filepath.Join(base, "stage/folder-000"), filepath.Join(base, "moved"))
	os.Symlink("../moved", filepath.Join(base, "stage/folder-000"))
	if dir, err := stage.directory("folder-000"); !errors.Is(err, ErrUnsafe) {
		if dir != nil {
			dir.Close()
		}
		t.Fatal("evicted path not revalidated", err)
	}
	if err := os.Remove(filepath.Join(base, "stage/folder-000")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(base, "stage/folder-000"), 0700); err != nil {
		t.Fatal(err)
	}
	if dir, err := stage.directory("folder-000"); !errors.Is(err, ErrConflict) {
		if dir != nil {
			dir.Close()
		}
		t.Fatal("replacement directory accepted after eviction", err)
	}
}

func testCopyPerformance(t *testing.T, s *Service) {
	previous := googleHTTP
	defer func() { googleHTTP = previous }()
	if err := privateJSON(filepath.Join(s.config.State, "google.json"), Google{Access: "fixture", Refresh: "fixture", Expires: time.Now().Add(time.Hour).Unix(), Connection: "performance-test"}); err != nil {
		t.Fatal(err)
	}
	const fileCount = 128
	payload := bytes.Repeat([]byte("small file\n"), 64)
	manifest := []File{{To: "tree", Directory: true, Source: "drive"}}
	for folder := 0; folder < 8; folder++ {
		dir := fmt.Sprintf("tree/folder-%02d", folder)
		manifest = append(manifest, File{To: dir, Directory: true, Source: "drive"})
		for index := 0; index < fileCount/8; index++ {
			name := fmt.Sprintf("file-%02d-%02d", folder, index)
			manifest = append(manifest, File{From: name, To: dir + "/" + name, Size: int64(len(payload)), Source: "drive", Version: "1"})
		}
	}
	var active, maximum, calls atomic.Int32
	googleHTTP = &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
		calls.Add(1)
		n := active.Add(1)
		defer active.Add(-1)
		for old := maximum.Load(); n > old; old = maximum.Load() {
			if maximum.CompareAndSwap(old, n) {
				break
			}
		}
		select {
		case <-time.After(8 * time.Millisecond):
		case <-r.Context().Done():
			return nil, r.Context().Err()
		}
		var body io.ReadCloser
		if r.URL.Query().Get("alt") == "media" {
			body = io.NopCloser(bytes.NewReader(payload))
		} else {
			data := fmt.Sprintf(`{"id":%q,"version":"1","size":"%d","capabilities":{"canDownload":true}}`, filepath.Base(r.URL.Path), len(payload))
			body = io.NopCloser(strings.NewReader(data))
		}
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: body}, nil
	})}
	var durations [2]time.Duration
	for run, workerCount := range []int{1, copyWorkers} {
		maximum.Store(0)
		calls.Store(0)
		j := Job{ID: ID(), State: "running", Connection: "performance-test", Manifest: append([]File(nil), manifest...), Total: int64(fileCount * len(payload))}
		if err := s.insert(j.ID, "copy", j.State, "", j); err != nil {
			t.Fatal(err)
		}
		root, err := s.root("vps")
		if err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(".filebeans-stage", j.ID)
		if err = mkdir(root, path); err != nil {
			t.Fatal(err)
		}
		start := time.Now()
		err = s.copyManifestWorkers(context.Background(), &j, root, path, workerCount)
		durations[run] = time.Since(start)
		if err != nil {
			root.Close()
			t.Fatal(err)
		}
		if j.Done != j.Total || j.ItemsDone != len(manifest) {
			t.Fatalf("incorrect aggregate progress: %d/%d bytes, %d/%d items", j.Done, j.Total, j.ItemsDone, len(manifest))
		}
		if maximum.Load() > int32(workerCount) || (workerCount > 1 && maximum.Load() < 2) {
			t.Fatal("incorrect concurrency", maximum.Load())
		}
		if calls.Load() != fileCount*3 {
			t.Fatal("source verification requests missing", calls.Load())
		}
		var saved Job
		if err = s.read(j.ID, "copy", &saved); err != nil {
			t.Fatal(err)
		}
		if saved.Done != j.Total || saved.ItemsDone != len(manifest) {
			t.Fatal("final progress not persisted")
		}
		for _, file := range j.Manifest {
			if !file.Completed {
				t.Fatal("incomplete file", file.To)
			}
			info, err := root.Stat(filepath.Join(path, file.To))
			if err != nil {
				t.Fatal(err)
			}
			wantMode := os.FileMode(0660)
			if file.Directory {
				wantMode = 0770
			}
			if info.Mode().Perm() != wantMode {
				t.Fatalf("wrong published permissions for %s: %o", file.To, info.Mode().Perm())
			}
			if file.Directory {
				continue
			}
			data, err := root.ReadFile(filepath.Join(path, file.To))
			if err != nil || !bytes.Equal(data, payload) {
				t.Fatal("copy mismatch", file.To, err)
			}
		}
		root.RemoveAll(path)
		root.Close()
	}
	t.Logf("128 tiny files, simulated 8ms per Drive request: serial=%s four workers=%s speedup=%.2fx", durations[0], durations[1], float64(durations[0])/float64(durations[1]))

	t.Run("restart-recopies-uncheckpointed-files", func(t *testing.T) {
		j := Job{ID: ID(), State: "running", Connection: "performance-test", Manifest: append([]File(nil), manifest...), Total: int64(fileCount * len(payload))}
		if err := s.insert(j.ID, "copy", j.State, "", j); err != nil {
			t.Fatal(err)
		}
		root, err := s.root("vps")
		if err != nil {
			t.Fatal(err)
		}
		defer root.Close()
		stage := filepath.Join(".filebeans-stage", j.ID)
		file := manifest[2]
		target := filepath.Join(stage, file.To)
		if err := mkdir(root, filepath.Dir(target)); err != nil {
			t.Fatal(err)
		}
		// The process died after a write but before its batch checkpoint. Those
		// bytes must not be trusted on the next attempt.
		if err := os.WriteFile(filepath.Join(root.Name(), target), []byte("uncommitted bytes"), 0600); err != nil {
			t.Fatal(err)
		}
		var reloaded Job
		if err := s.read(j.ID, "copy", &reloaded); err != nil {
			t.Fatal(err)
		}
		if err := s.copyManifest(context.Background(), &reloaded, root, stage); err != nil {
			t.Fatal(err)
		}
		got, err := root.ReadFile(target)
		if err != nil || !bytes.Equal(got, payload) {
			t.Fatal("recovery kept uncommitted content", err)
		}
		if reloaded.Done != reloaded.Total {
			t.Fatal("incorrect recovered byte count")
		}
		root.RemoveAll(stage)
	})
	t.Run("source-changed-during-download", func(t *testing.T) {
		transport := googleHTTP
		defer func() { googleHTTP = transport }()
		var metadata atomic.Int32
		googleHTTP = &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
			body := io.NopCloser(bytes.NewReader(payload))
			if r.URL.Query().Get("alt") != "media" {
				body = io.NopCloser(strings.NewReader(fmt.Sprintf(`{"version":"%d"}`, metadata.Add(1))))
			}
			return &http.Response{StatusCode: 200, Header: http.Header{}, Body: body}, nil
		})}
		j := Job{ID: ID(), State: "queued", Connection: "performance-test", Manifest: []File{{From: "changing-file", To: "changed-drive-file", Source: "drive", Size: int64(len(payload)), Version: "1"}}}
		j.Request.Destination = Item{Source: "vps", Path: "."}
		if err := s.insert(j.ID, "copy", j.State, "", j); err != nil {
			t.Fatal(err)
		}
		s.runJob(context.Background(), &j)
		if j.State != "failed" {
			t.Fatal("source change accepted", j.State, j.Error)
		}
		root, _ := s.root("vps")
		defer root.Close()
		if _, err := root.Stat("changed-drive-file"); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("changed source published", err)
		}
	})
	t.Run("batch-patches-and-recovery", func(t *testing.T) {
		j := Job{ID: ID(), State: "running", Manifest: append([]File(nil), manifest...)}
		if err := s.insert(j.ID, "copy", j.State, "", j); err != nil {
			t.Fatal(err)
		}
		p := &copyProgress{service: s, job: &j, dirty: map[int]bool{}, last: time.Now(), started: time.Now()}
		for index := 0; index < copyCheckpointFiles-1; index++ {
			file := j.Manifest[index]
			file.Done = file.Size
			file.Completed = true
			if err := p.report(index, file); err != nil {
				t.Fatal(err)
			}
		}
		var saved Job
		s.read(j.ID, "copy", &saved)
		if saved.ItemsDone != 0 {
			t.Fatal("wrote full checkpoint for each small file")
		}
		// Crash before the batch flush: reloaded state still has the old durable
		// prefix, so unrecorded destination bytes are safely copied again.
		if saved.Manifest[1].Completed {
			t.Fatal("unflushed progress was persisted")
		}
		file := j.Manifest[copyCheckpointFiles-1]
		file.Done = file.Size
		file.Completed = true
		if err := p.report(copyCheckpointFiles-1, file); err != nil {
			t.Fatal(err)
		}
		if err := s.read(j.ID, "copy", &saved); err != nil {
			t.Fatal(err)
		}
		if saved.ItemsDone != copyCheckpointFiles || !saved.Manifest[copyCheckpointFiles-1].Completed || saved.Manifest[copyCheckpointFiles].Completed {
			t.Fatal("batch update changed wrong manifest entries")
		}
		saved.Cancel = true
		s.save(j.ID, "copy", saved.State, saved)
		file = j.Manifest[copyCheckpointFiles]
		file.Completed = true
		if err := p.report(copyCheckpointFiles, file); err != nil {
			t.Fatal(err)
		}
		if err := p.flush(); !errors.Is(err, errCancelled) {
			t.Fatal("cancel flag overwritten by progress", err)
		}
	})

	for _, testCase := range []struct {
		name        string
		size        int64
		concurrency int
	}{
		{"cancel-all-in-flight-downloads", 100, copyWorkers},
		{"large-files-stay-serial-and-cancel", smallCopyLimit + 1, 1},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			started := make(chan struct{}, copyWorkers)
			googleHTTP = &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
				var body io.ReadCloser = io.NopCloser(strings.NewReader(`{"version":"1"}`))
				if r.URL.Query().Get("alt") == "media" {
					body = &poolCancelBody{ctx: r.Context(), started: started}
				}
				return &http.Response{StatusCode: 200, Header: http.Header{}, Body: body}, nil
			})}
			j := Job{ID: ID(), State: "queued", Connection: "performance-test"}
			for i := 0; i < copyWorkers; i++ {
				j.Manifest = append(j.Manifest, File{From: fmt.Sprintf("blocked-%d", i), To: fmt.Sprintf("blocked-%d", i), Source: "drive", Size: testCase.size, Version: "1"})
			}
			j.Request.Destination = Item{Source: "vps", Path: "."}
			if err := s.insert(j.ID, "copy", j.State, "", j); err != nil {
				t.Fatal(err)
			}
			ctx, stop := context.WithCancel(context.Background())
			done := make(chan struct{})
			go func() { defer close(done); s.runJob(ctx, &j) }()
			defer func() { stop(); <-done }()
			for i := 0; i < testCase.concurrency; i++ {
				select {
				case <-started:
				case <-time.After(3 * time.Second):
					t.Fatal("copy workers did not overlap")
				}
			}
			if testCase.concurrency == 1 {
				select {
				case <-started:
					t.Fatal("large downloads overlapped")
				case <-time.After(30 * time.Millisecond):
				}
			}
			request := httptest.NewRequest("POST", "/v1/jobs/"+j.ID+"/cancel", nil)
			request.Header.Set("Authorization", "Bearer "+s.config.Token)
			response := httptest.NewRecorder()
			s.Handler().ServeHTTP(response, request)
			if response.Code != 200 {
				t.Fatal(response.Code)
			}
			select {
			case <-done:
			case <-time.After(3 * time.Second):
				t.Fatal("workers did not stop")
			}
			if j.State != "cancelled" {
				t.Fatal(j.State, j.Error)
			}
			root, _ := s.root("vps")
			defer root.Close()
			if _, err := root.Stat(filepath.Join(".filebeans-stage", j.ID)); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("staging was not cleaned", err)
			}
		})
	}
}

type poolCancelBody struct {
	ctx     context.Context
	started chan struct{}
}

func (b *poolCancelBody) Read(p []byte) (int, error) {
	b.started <- struct{}{}
	<-b.ctx.Done()
	return 0, b.ctx.Err()
}
func (b *poolCancelBody) Close() error { return nil }
