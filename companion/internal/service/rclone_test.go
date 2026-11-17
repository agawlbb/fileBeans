package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func testRcloneBinary(t *testing.T) string {
	t.Helper()
	path := os.Getenv("FILEBEANS_TEST_RCLONE")
	if path == "" {
		path = "rclone"
	}
	binary, err := exec.LookPath(path)
	if err != nil {
		t.Fatal("install the pinned rclone test dependency:", err)
	}
	return binary
}
func quoteShell(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'" }

func useRcloneFixture(t *testing.T, s *Service, payload []byte, mode string) string {
	t.Helper()
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	data := filepath.Join(dir, "payload")
	audit := filepath.Join(dir, "audit")
	if err = os.WriteFile(data, payload, 0600); err != nil {
		t.Fatal(err)
	}
	wrapper := filepath.Join(dir, "rclone")
	script := "#!/bin/sh\nFB_RCLONE_DATA=" + quoteShell(data) + " FB_RCLONE_AUDIT=" + quoteShell(audit) + " FB_RCLONE_MODE=" + quoteShell(mode) + " exec " + quoteShell(binary) + " -test.run=TestRcloneSubprocessHelper -- \"$@\"\n"
	if err = os.WriteFile(wrapper, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	previous := s.config.Rclone
	s.config.Rclone = wrapper
	t.Cleanup(func() { s.config.Rclone = previous })
	return audit
}

// A subprocess fixture checks the Drive adapter without talking to Google's API.
// Real filesystem transfers below always use the actual rclone executable.
func TestRcloneSubprocessHelper(t *testing.T) {
	mode := os.Getenv("FB_RCLONE_MODE")
	if mode == "" {
		return
	}
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGTERM)
	args := os.Args
	config := ""
	for i, arg := range args {
		if arg == "--config" && i+1 < len(args) {
			config = args[i+1]
		}
	}
	info, err := os.Stat(config)
	if err != nil || info.Mode().Perm() != 0600 {
		os.Exit(20)
	}
	var token map[string]string
	if json.Unmarshal([]byte(os.Getenv("RCLONE_CONFIG_FROM_TOKEN")), &token) != nil {
		os.Exit(21)
	}
	for _, key := range []string{"access_token", "refresh_token"} {
		if value := token[key]; value != "" && strings.Contains(strings.Join(args, " "), value) {
			os.Exit(22)
		}
	}
	if os.Getenv("RCLONE_CONFIG") != "" {
		os.Exit(23)
	}
	if err = os.WriteFile(os.Getenv("FB_RCLONE_AUDIT"), []byte("ready"), 0600); err != nil {
		os.Exit(24)
	}
	if mode == "error" {
		event := map[string]any{"level": "error", "object": "broken-file", "msg": "download interrupted " + token["access_token"] + " " + token["refresh_token"]}
		json.NewEncoder(os.Stderr).Encode(event)
		os.Exit(5)
	}
	if mode == "block" {
		json.NewEncoder(os.Stderr).Encode(map[string]any{"stats": map[string]any{"bytes": 1, "totalBytes": 100}})
		ticker := time.NewTicker(100 * time.Millisecond)
		for {
			select {
			case <-signals:
				os.Exit(130)
			case <-ticker.C:
				json.NewEncoder(os.Stderr).Encode(map[string]any{"stats": map[string]any{"bytes": 1, "totalBytes": 100}})
			}
		}
	}
	destination := strings.TrimPrefix(args[len(args)-1], "to:")
	if os.Getenv("RCLONE_CONFIG_FROM_ROOT_FOLDER_ID") != "" {
		destination = filepath.Join(destination, "copied.txt")
	}
	if err = os.MkdirAll(filepath.Dir(destination), 0770); err != nil {
		os.Exit(25)
	}
	data, err := os.ReadFile(os.Getenv("FB_RCLONE_DATA"))
	if err != nil {
		os.Exit(26)
	}
	if err = os.WriteFile(destination, data, 0660); err != nil {
		os.Exit(27)
	}
	json.NewEncoder(os.Stderr).Encode(map[string]any{"stats": map[string]any{"bytes": len(data), "totalBytes": len(data), "transfers": 1, "totalTransfers": 1}})
	os.Exit(0)
}

func testRcloneIntegration(t *testing.T, s *Service) {
	submit := func(source, path string) Job {
		t.Helper()
		j := Job{ID: ID(), State: "queued", Request: CopyRequest{Items: []Item{{Source: source, Path: path}}, Destination: Item{Source: "vps", Path: "."}}}
		if source == "drive" {
			grant, err := s.google()
			if err != nil {
				t.Fatal(err)
			}
			j.Connection = grant.Connection
		}
		if err := s.insert(j.ID, "copy", j.State, "", j); err != nil {
			t.Fatal(err)
		}
		return j
	}
	root, _ := s.root("samba")
	defer root.Close()
	target, _ := s.root("vps")
	defer target.Close()
	t.Run("real-tool-small-files-empty-dirs-and-retry", func(t *testing.T) {
		const count = 512
		for i := 0; i < count; i++ {
			path := fmt.Sprintf("rclone-tree/deep/project/.git/objects/%02d/file-%04d", i/32, i)
			if err := mkdir(root, filepath.Dir(path)); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(root.Name(), path), []byte(fmt.Sprintf("payload-%04d", i)), 0600); err != nil {
				t.Fatal(err)
			}
		}
		mkdir(root, "rclone-tree/empty")
		j := submit("samba", "rclone-tree")
		stage := filepath.Join(".filebeans-stage", j.ID, "rclone-tree")
		mkdir(target, filepath.Join(stage, "deep/project/.git/objects/00"))
		// A previous attempt left an incomplete file and a stale source deletion.
		os.WriteFile(filepath.Join(target.Name(), stage, "deep/project/.git/objects/00/file-0000"), []byte("bad"), 0600)
		os.WriteFile(filepath.Join(target.Name(), stage, "stale"), []byte("removed at source"), 0600)
		j.State = "failed"
		s.save(j.ID, "copy", j.State, j)
		request := httptest.NewRequest("POST", "/v1/jobs/"+j.ID+"/retry", nil)
		request.Header.Set("Authorization", "Bearer "+s.config.Token)
		response := httptest.NewRecorder()
		s.Handler().ServeHTTP(response, request)
		if response.Code != 200 {
			t.Fatal(response.Code, response.Body.String())
		}
		if _, err := target.Stat(filepath.Join(stage, "stale")); err != nil {
			t.Fatal("Retry discarded staging", err)
		}
		if err := s.read(j.ID, "copy", &j); err != nil {
			t.Fatal(err)
		}
		start := time.Now()
		s.runJob(context.Background(), &j)
		if j.State != "completed" {
			t.Fatal(j.State, j.Error)
		}
		if j.Engine != "rclone" || len(j.Manifest) != 0 || len(j.RclonePlan) != 1 {
			t.Fatal("per-file engine still in use")
		}
		for i := 0; i < count; i++ {
			path := fmt.Sprintf("rclone-tree/deep/project/.git/objects/%02d/file-%04d", i/32, i)
			got, err := target.ReadFile(path)
			if err != nil || string(got) != fmt.Sprintf("payload-%04d", i) {
				t.Fatal("copy mismatch", path, err)
			}
		}
		if _, err := target.Stat("rclone-tree/empty"); err != nil {
			t.Fatal("empty directory missing", err)
		}
		if _, err := target.Stat("rclone-tree/stale"); err != nil {
			t.Fatal("ordinary copy deleted an existing staged file", err)
		}
		t.Logf("real rclone: %d tiny files reconciled and published in %s", count, time.Since(start))
	})
	t.Run("existing-destination-never-overwritten", func(t *testing.T) {
		os.WriteFile(filepath.Join(root.Name(), "no-overwrite"), []byte("new"), 0600)
		os.WriteFile(filepath.Join(target.Name(), "no-overwrite"), []byte("keep"), 0600)
		j := submit("samba", "no-overwrite")
		s.runJob(context.Background(), &j)
		got, _ := target.ReadFile("no-overwrite")
		if j.State != "failed" || string(got) != "keep" {
			t.Fatal("existing destination changed", j.State)
		}
	})
	t.Run("nested-symlink-not-followed-or-published", func(t *testing.T) {
		mkdir(root, "unsafe-tree")
		outside := filepath.Join(t.TempDir(), "secret")
		os.WriteFile(outside, []byte("private"), 0600)
		os.Symlink(outside, filepath.Join(root.Name(), "unsafe-tree/link"))
		j := submit("samba", "unsafe-tree")
		s.runJob(context.Background(), &j)
		if j.State != "failed" {
			t.Fatal("source link silently omitted", j.State, j.Error)
		}
		if _, err := target.Stat("unsafe-tree"); !os.IsNotExist(err) {
			t.Fatal("unsafe tree published")
		}
	})
	privateJSON(filepath.Join(s.config.State, "google.json"), Google{Access: "fixture-access-secret", Refresh: "fixture-refresh-secret", Expires: time.Now().Add(time.Hour).Unix(), Connection: "rclone-account"})
	previousHTTP := googleHTTP
	defer func() { googleHTTP = previousHTTP }()
	googleHTTP = &http.Client{Transport: roundTrip(func(request *http.Request) (*http.Response, error) {
		if request.URL.Query().Get("alt") == "media" {
			t.Error("companion still downloads file data itself")
		}
		return jsonResponse(200, map[string]any{"id": "fixture-drive-id", "name": "drive-fixture", "mimeType": "application/octet-stream", "capabilities": map[string]bool{"canDownload": true}}), nil
	})}
	t.Run("drive-error-has-detail-and-redacts-credentials", func(t *testing.T) {
		useRcloneFixture(t, s, nil, "error")
		j := submit("drive", "fixture-drive-id")
		s.runJob(context.Background(), &j)
		if j.State != "failed" || !strings.Contains(j.Error, "broken-file") {
			t.Fatal("rclone error hidden", j.State, j.Error)
		}
		if strings.Contains(j.Error, "fixture-access-secret") || strings.Contains(j.Error, "fixture-refresh-secret") {
			t.Fatal("credential leak")
		}
	})
	t.Run("cancellation-stops-child-before-cleanup", func(t *testing.T) {
		audit := useRcloneFixture(t, s, nil, "block")
		j := submit("drive", "fixture-drive-id")
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		go func() { defer close(done); s.runJob(ctx, &j) }()
		defer func() { cancel(); <-done }()
		deadline := time.Now().Add(5 * time.Second)
		for {
			if _, err := os.Stat(audit); err == nil {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("child did not start")
			}
			time.Sleep(10 * time.Millisecond)
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
		case <-time.After(6 * time.Second):
			t.Fatal("child did not stop")
		}
		if j.State != "cancelled" {
			t.Fatal(j.State, j.Error)
		}
		if _, err := target.Stat(filepath.Join(".filebeans-stage", j.ID)); !os.IsNotExist(err) {
			t.Fatal("staging not cleaned after child stopped", err)
		}
	})
	t.Run("account-switch-stops-in-flight-copy", func(t *testing.T) {
		original, _ := os.ReadFile(filepath.Join(s.config.State, "google.json"))
		defer os.WriteFile(filepath.Join(s.config.State, "google.json"), original, 0600)
		audit := useRcloneFixture(t, s, nil, "block")
		j := submit("drive", "fixture-drive-id")
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		go func() { defer close(done); s.runJob(ctx, &j) }()
		defer func() { cancel(); <-done }()
		deadline := time.Now().Add(5 * time.Second)
		for {
			if _, err := os.Stat(audit); err == nil {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("child did not start")
			}
			time.Sleep(10 * time.Millisecond)
		}
		privateJSON(filepath.Join(s.config.State, "google.json"), Google{Access: "new-account", Refresh: "new-account", Expires: time.Now().Add(time.Hour).Unix(), Connection: "different-account"})
		select {
		case <-done:
		case <-time.After(4 * time.Second):
			t.Fatal("account change did not stop copy")
		}
		if j.State != "failed" || !strings.Contains(j.Error, "account changed") {
			t.Fatal(j.State, j.Error)
		}
	})
	t.Run("drive-file-id-and-private-config", func(t *testing.T) {
		original, _ := os.ReadFile(filepath.Join(s.config.State, "google.json"))
		payload := []byte("drive-payload")
		audit := useRcloneFixture(t, s, payload, "copy")
		j := submit("drive", "fixture-drive-id")
		s.runJob(context.Background(), &j)
		if j.State != "completed" {
			t.Fatal(j.State, j.Error)
		}
		data, err := target.ReadFile("drive-fixture")
		if err != nil || !bytes.Equal(data, payload) {
			t.Fatal("Drive data mismatch", err)
		}
		if _, err := os.Stat(audit); err != nil {
			t.Fatal("child not exercised")
		}
		after, _ := os.ReadFile(filepath.Join(s.config.State, "google.json"))
		if !bytes.Equal(original, after) {
			t.Fatal("saved Drive connection changed")
		}
		leftovers, _ := filepath.Glob(filepath.Join(s.config.State, "rclone-*"))
		if len(leftovers) != 0 {
			t.Fatal("private process credentials left behind", leftovers)
		}
	})
}
