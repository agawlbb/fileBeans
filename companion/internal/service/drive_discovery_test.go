package service

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func discoveryService(t *testing.T) *Service {
	t.Helper()
	s := &Service{config: Config{State: t.TempDir()}}
	if err := privateJSON(filepath.Join(s.config.State, "google.json"), Google{Access: "fixture", Refresh: "fixture", Connection: "discovery-test", Expires: time.Now().Add(time.Hour).Unix()}); err != nil {
		t.Fatal(err)
	}
	return s
}
func discoveryFolder(id string) DriveItem {
	return DriveItem{ID: id, Name: id, Mime: "application/vnd.google-apps.folder"}
}

func TestDriveDiscoveryOverlapsAndKeepsParentFirstOrder(t *testing.T) {
	s := discoveryService(t)
	previous := googleHTTP
	defer func() { googleHTTP = previous }()
	var active, maximum atomic.Int32
	allStarted := make(chan struct{})
	googleHTTP = &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
		query := r.URL.Query().Get("q")
		if strings.Contains(query, "'root' in parents") {
			// Pagination must keep the sibling order even when child responses arrive
			// in a different order. The coordinator assembles a deterministic tree.
			if r.URL.Query().Get("pageToken") == "next" {
				return jsonResponse(200, map[string]any{"files": []DriveItem{discoveryFolder("c"), discoveryFolder("d")}}), nil
			}
			return jsonResponse(200, map[string]any{"files": []DriveItem{discoveryFolder("a"), discoveryFolder("b")}, "nextPageToken": "next"}), nil
		}
		n := active.Add(1)
		defer active.Add(-1)
		for old := maximum.Load(); n > old; old = maximum.Load() {
			if maximum.CompareAndSwap(old, n) {
				break
			}
		}
		if n == copyWorkers {
			close(allStarted)
		}
		select {
		case <-allStarted:
		case <-r.Context().Done():
			return nil, r.Context().Err()
		}
		id := ""
		for _, candidate := range []string{"a", "b", "c", "d"} {
			if strings.Contains(query, "'"+candidate+"' in parents") {
				id = candidate
			}
		}
		if id == "" {
			return nil, fmt.Errorf("unexpected query")
		}
		item := DriveItem{ID: id + "-file", Name: "file", Size: "1", Version: "1", Capabilities: DriveCapabilities{Download: true}}
		return jsonResponse(200, map[string]any{"files": []DriveItem{item}}), nil
	})}
	ctx, stop := context.WithTimeout(context.Background(), 3*time.Second)
	defer stop()
	files, err := s.discoverDriveTree(ctx, discoveryFolder("root"), "discovery-test", false)
	if err != nil {
		t.Fatal(err)
	}
	paths := []string{}
	for _, file := range files {
		paths = append(paths, file.To)
	}
	want := []string{"root", "root/a", "root/a/file", "root/b", "root/b/file", "root/c", "root/c/file", "root/d", "root/d/file"}
	if !reflect.DeepEqual(paths, want) {
		t.Fatal(paths)
	}
	if maximum.Load() != copyWorkers {
		t.Fatal("folder requests did not overlap", maximum.Load())
	}
}

func TestDriveDiscoveryRejectsIncompleteAndDuplicateListings(t *testing.T) {
	for _, fixture := range []struct {
		name     string
		response map[string]any
		want     error
	}{
		{"duplicate-name", map[string]any{"files": []DriveItem{discoveryFolder("same"), {ID: "different", Name: "same", Mime: "application/vnd.google-apps.folder"}}}, ErrConflict},
		{"cycle", map[string]any{"files": []DriveItem{discoveryFolder("root")}}, ErrUnsafe},
		{"incomplete", map[string]any{"incompleteSearch": true}, nil},
		{"repeated-page", map[string]any{"nextPageToken": "again"}, ErrUnsafe},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			s := discoveryService(t)
			previous := googleHTTP
			defer func() { googleHTTP = previous }()
			googleHTTP = &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) { return jsonResponse(200, fixture.response), nil })}
			files, err := s.discoverDriveTree(context.Background(), discoveryFolder("root"), "discovery-test", false)
			if err == nil || files != nil || (fixture.want != nil && !errors.Is(err, fixture.want)) {
				t.Fatal(files, err)
			}
		})
	}
}

func TestDriveDiscoveryCancellationStopsAllListings(t *testing.T) {
	s := discoveryService(t)
	previous := googleHTTP
	defer func() { googleHTTP = previous }()
	started := make(chan struct{}, copyWorkers)
	var stopped atomic.Int32
	googleHTTP = &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
		if strings.Contains(r.URL.Query().Get("q"), "'root' in parents") {
			children := []DriveItem{}
			for i := 0; i < copyWorkers; i++ {
				children = append(children, discoveryFolder(fmt.Sprintf("folder-%d", i)))
			}
			return jsonResponse(200, map[string]any{"files": children}), nil
		}
		started <- struct{}{}
		<-r.Context().Done()
		stopped.Add(1)
		return nil, r.Context().Err()
	})}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := s.discoverDriveTree(ctx, discoveryFolder("root"), "discovery-test", false)
		done <- err
	}()
	defer cancel()
	for i := 0; i < copyWorkers; i++ {
		select {
		case <-started:
		case <-time.After(3 * time.Second):
			t.Fatal("workers not started")
		}
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("discovery did not stop")
	}
	if stopped.Load() != copyWorkers {
		t.Fatal("discovery leaked requests", stopped.Load())
	}
}

func TestCopyValidationRejectsReplacedAndUnexpectedFiles(t *testing.T) {
	for _, replacement := range []bool{false, true} {
		t.Run(fmt.Sprint(replacement), func(t *testing.T) {
			base := t.TempDir()
			root, _ := os.OpenRoot(base)
			defer root.Close()
			os.Mkdir(filepath.Join(base, "stage"), 0700)
			stage, err := newCopyStage(root, "stage")
			if err != nil {
				t.Fatal(err)
			}
			defer stage.close()
			file := File{To: "copied", Size: 4}
			out, _, err := stage.open(file)
			if err != nil {
				t.Fatal(err)
			}
			out.WriteString("data")
			out.Close()
			if replacement {
				os.Rename(filepath.Join(base, "stage/copied"), filepath.Join(base, "old"))
				os.WriteFile(filepath.Join(base, "stage/copied"), []byte("fake"), 0600)
			} else {
				os.WriteFile(filepath.Join(base, "stage/unlisted"), nil, 0600)
			}
			if err := stage.validate(context.Background(), map[string]File{"copied": file}); !errors.Is(err, ErrConflict) {
				t.Fatal("unexpected staging data accepted", err)
			}
		})
	}
}
