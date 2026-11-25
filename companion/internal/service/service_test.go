package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"
)

func TestPathBoundaries(t *testing.T) {
	for _, p := range []string{"../outside", "/ok/../bad", "a\\b", "a\x00b"} {
		if _, e := clean(p); e == nil {
			t.Errorf("accepted %q", p)
		}
	}
	for _, p := range []string{"Trash", "/Trash/a", "/.filebeans-stage/id"} {
		if !protected(p) {
			t.Errorf("unprotected %s", p)
		}
	}
	base := t.TempDir()
	outside := t.TempDir()
	if e := os.Symlink(outside, filepath.Join(base, "link")); e != nil {
		t.Fatal(e)
	}
	r, e := os.OpenRoot(base)
	if e != nil {
		t.Fatal(e)
	}
	defer r.Close()
	if e = safe(r, "link/file"); !errors.Is(e, ErrUnsafe) {
		t.Fatal("symlink accepted", e)
	}
	os.WriteFile(filepath.Join(base, "a"), []byte("a"), 0600)
	os.WriteFile(filepath.Join(base, "b"), []byte("b"), 0600)
	if e = renameExclusive(r, "a", "b"); e == nil {
		t.Fatal("overwrote destination")
	}
	data, _ := os.ReadFile(filepath.Join(base, "b"))
	if string(data) != "b" {
		t.Fatal("destination changed")
	}
	s := &Service{config: Config{Roots: []Root{{Name: "share", Path: base, Mount: "/no-such-mount", MountSource: "//fixture/share"}}}}
	if _, e = s.root("share"); !errors.Is(e, ErrMount) {
		t.Fatal("missing mount accepted", e)
	}
}
func TestAuthAndOAuth(t *testing.T) {
	s := &Service{config: Config{Token: strings.Repeat("t", 32), State: t.TempDir(), GoogleClientID: "fixture", GoogleClientSecret: "secret", GoogleRedirect: "https://example.test/oauth/callback"}}
	request := httptest.NewRequest("GET", "/v1/capabilities", nil)
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, request)
	if w.Code != 401 {
		t.Fatal(w.Code)
	}
	u, e := s.authURL()
	if e != nil || !strings.Contains(u, "code_challenge_method=S256") {
		t.Fatal("PKCE missing", e)
	}
	if e = s.callback(context.Background(), "wrong", "fixture"); !errors.Is(e, ErrAuthorization) {
		t.Fatal("invalid state accepted")
	}
	request.Header.Set("Authorization", "Bearer "+s.config.Token)
	w = httptest.NewRecorder()
	s.Handler().ServeHTTP(w, request)
	if w.Code != 200 {
		t.Fatal(w.Code)
	}
}

type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestDriveReadOnlyAndRanges(t *testing.T) {
	s := &Service{config: Config{State: t.TempDir()}}
	if e := privateJSON(filepath.Join(s.config.State, "google.json"), Google{Access: "fixture", Refresh: "fixture", Expires: time.Now().Add(time.Hour).Unix(), Connection: "one"}); e != nil {
		t.Fatal(e)
	}
	previous := googleHTTP
	defer func() { googleHTTP = previous }()
	googleHTTP = &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
		if r.Method != "GET" {
			t.Fatal("unexpected Drive mutation")
		}
		if r.Header.Get("Authorization") != "Bearer fixture" {
			t.Fatal("missing authorization")
		}
		if r.URL.Query().Get("alt") == "media" {
			if r.Header.Get("Range") != "bytes=3-" {
				t.Fatal("range missing")
			}
			return &http.Response{StatusCode: 206, Header: http.Header{"Content-Range": {"bytes 3-5/6"}}, Body: io.NopCloser(strings.NewReader("def"))}, nil
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"files":[{"id":"file","name":"file.mp4","mimeType":"video/mp4","size":"6","version":"1","capabilities":{"canDownload":true}}],"nextPageToken":"next"}`))}, nil
	})}
	list, e := s.driveList(context.Background(), "root", "", "")
	if e != nil || list.Next != "next" || len(list.Files) != 1 {
		t.Fatal(list, e)
	}
	reader, e := s.driveContent(context.Background(), "file", 3, "one")
	if e != nil {
		t.Fatal(e)
	}
	b, _ := io.ReadAll(reader)
	reader.Close()
	if string(b) != "def" {
		t.Fatal("bad range")
	}
	if _, e = s.token(context.Background(), "different"); !errors.Is(e, ErrDriveAccount) {
		t.Fatal("account switch accepted")
	}
}

// Uses only the explicitly supplied VM .test-config file. Never production DSNs.
func TestDatabaseWorkflow(t *testing.T) {
	path := os.Getenv("FILEBEANS_TEST_CONFIG")
	if path == "" {
		t.Skip("set FILEBEANS_TEST_CONFIG to a VM .test-config JSON file")
	}
	if !strings.HasPrefix(filepath.Clean(path), "/home/dev/projects/.test-config/") {
		t.Fatal("only VM test configuration is allowed")
	}
	var cfg struct {
		Host     string `json:"host"`
		Port     int    `json:"port"`
		User     string `json:"user"`
		Password string `json:"password"`
		Database string `json:"database"`
	}
	b, e := os.ReadFile(path)
	if e != nil {
		t.Fatal("test config unavailable")
	}
	if json.Unmarshal(b, &cfg) != nil {
		t.Fatal("invalid test config")
	}
	dsn := mysql.NewConfig()
	dsn.User = cfg.User
	dsn.Passwd = cfg.Password
	dsn.Net = "tcp"
	dsn.Addr = cfg.Host + ":" + strconv.Itoa(cfg.Port)
	dsn.DBName = cfg.Database
	db, e := openDB(dsn.FormatDSN())
	if e != nil {
		t.Fatal("test database unavailable")
	}
	defer db.Close()
	var count int
	if e = db.QueryRow("SELECT COUNT(*) FROM information_schema.tables WHERE table_schema=DATABASE() AND table_name='fb_operations'").Scan(&count); e != nil {
		t.Fatal(e)
	}
	if count != 0 {
		t.Fatal("refusing to modify a pre-existing fb_operations table")
	}
	s := &Service{db: db, config: Config{Rclone: testRcloneBinary(t), State: t.TempDir(), Token: strings.Repeat("t", 32), Roots: []Root{{Name: "vps", Path: t.TempDir()}, {Name: "samba", Path: t.TempDir()}}}}
	if e = s.Migrate(); e != nil {
		t.Fatal(e)
	}
	defer db.Exec("DROP TABLE fb_operations")
	vps, share := s.config.Roots[0].Path, s.config.Roots[1].Path
	t.Run("archive-sync", func(t *testing.T) { testArchiveSyncDatabase(t, s) })
	t.Run("archive-links", func(t *testing.T) { testArchiveDatabase(t, s) })
	t.Run("dataset-render", func(t *testing.T) { testDatasetDatabase(t, s) })
	t.Run("protected-records", func(t *testing.T) {
		previous := s.vaults
		s.vaults = testVaultService().vaults
		defer func() { s.vaults = previous }()
		j := Job{ID: ID(), State: "completed", Request: CopyRequest{Items: []Item{{Source: "Protected", Path: "private/family.jpg"}}, Destination: Item{Source: "vps", Path: "export"}}}
		if err := s.insert(j.ID, "vault-test", j.State, "vault-test-request-key", j); err != nil {
			t.Fatal(err)
		}
		defer db.Exec("DELETE FROM fb_operations WHERE id=?", j.ID)
		var stored []byte
		if err := db.QueryRow("SELECT body FROM fb_operations WHERE id=?", j.ID).Scan(&stored); err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(stored, []byte("family.jpg")) || bytes.Contains(stored, []byte("export")) {
			t.Fatal("database contains protected plaintext")
		}
		var got Job
		if err := s.read(j.ID, "vault-test", &got); err != nil || got.Request.Items[0].Path != "private/family.jpg" {
			t.Fatal("read failed", err)
		}
		if err := s.existing("vault-test-request-key", "vault-test", &got); err != nil {
			t.Fatal("idempotency lookup failed", err)
		}
		s.vaults.states[0].unlocked = false
		if err := s.read(j.ID, "vault-test", &got); !errors.Is(err, ErrVaultLocked) {
			t.Fatal("locked read accepted", err)
		}
		rows, err := s.records("vault-test")
		if err != nil || len(rows) != 0 {
			t.Fatal("locked record listed", err)
		}
		s.vaults.states[0].unlocked = true
		if err := s.save(j.ID, "vault-test", j.State, j); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec("UPDATE fb_operations SET body=? WHERE id=?", `{"source":"Protected","path":"legacy-private.jpg"}`, j.ID); err != nil {
			t.Fatal(err)
		}
		if err := s.migrateVaultRecords(); err != nil {
			t.Fatal("legacy migration failed", err)
		}
		if err := db.QueryRow("SELECT body FROM fb_operations WHERE id=?", j.ID).Scan(&stored); err != nil || bytes.Contains(stored, []byte("legacy-private")) {
			t.Fatal("legacy metadata left plaintext", err)
		}
	})
	payload := bytes.Repeat([]byte("content\n"), 300000)
	os.MkdirAll(filepath.Join(share, "folder/empty"), 0700)
	os.WriteFile(filepath.Join(share, "folder/video"), payload, 0600)
	submit := func(request CopyRequest, key string) Job {
		t.Helper()
		b, _ := json.Marshal(request)
		r := httptest.NewRequest("POST", "/v1/jobs", bytes.NewReader(b))
		r.Header.Set("Authorization", "Bearer "+s.config.Token)
		r.Header.Set("Idempotency-Key", key)
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, r)
		if w.Code != 202 && w.Code != 200 {
			t.Fatalf("submission status %d: %s", w.Code, w.Body.String())
		}
		var j Job
		if e = json.Unmarshal(w.Body.Bytes(), &j); e != nil {
			t.Fatal(e)
		}
		return j
	}
	cancelJob := func(id string) Job {
		t.Helper()
		r := httptest.NewRequest("POST", "/v1/jobs/"+id+"/cancel", nil)
		r.Header.Set("Authorization", "Bearer "+s.config.Token)
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, r)
		if w.Code != 200 {
			t.Fatalf("cancel status %d: %s", w.Code, w.Body.String())
		}
		var result Job
		if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		return result
	}
	t.Run("queued-cancel-does-not-need-worker", func(t *testing.T) {
		j := submit(CopyRequest{Items: []Item{{Source: "samba", Path: "folder"}}, Destination: Item{Source: "vps", Path: "/"}}, "queued-cancel-fixture")
		result := cancelJob(j.ID)
		if result.State != "cancelled" || !result.Cancel {
			t.Fatalf("cancel not immediate: %+v", result)
		}
		// A worker that selected this job before cancellation must not copy it.
		s.runJob(context.Background(), &j)
		if j.State != "cancelled" {
			t.Fatal(j.State)
		}
		if _, err := os.Stat(filepath.Join(vps, "folder")); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("cancelled job published", err)
		}
	})
	t.Run("move-integration", func(t *testing.T) { testMoveIntegration(t, s) })
	t.Run("cross-drive-moves", func(t *testing.T) { testCrossDriveMoves(t, s) })
	t.Run("rclone-integration", func(t *testing.T) { testRcloneIntegration(t, s) })
	t.Run("copy-and-idempotency", func(t *testing.T) {
		req := CopyRequest{Items: []Item{{Source: "samba", Path: "/folder"}}, Destination: Item{Source: "vps", Path: "/"}}
		j := submit(req, "copy-test-key-0001")
		again := submit(req, "copy-test-key-0001")
		if j.ID != again.ID {
			t.Fatal("duplicate job")
		}
		s.runJob(context.Background(), &j)
		if j.State != "completed" {
			t.Fatal(j.State, j.Error)
		}
		got, e := os.ReadFile(filepath.Join(vps, "folder/video"))
		if e != nil || !bytes.Equal(got, payload) {
			t.Fatal("copy mismatch", e)
		}
		if _, e = os.Stat(filepath.Join(vps, "folder/empty")); e != nil {
			t.Fatal("empty folder missing")
		}
		j = submit(req, "copy-test-key-0002")
		s.runJob(context.Background(), &j)
		if j.State != "failed" {
			t.Fatal("collision not failed", j.State)
		}
	})
	t.Run("resume-and-corrupt-prefix", func(t *testing.T) {
		os.WriteFile(filepath.Join(share, "resume"), payload, 0600)
		j := submit(CopyRequest{Items: []Item{{Source: "samba", Path: "resume"}}, Destination: Item{Source: "vps", Path: "/"}}, "copy-test-key-0003")
		if e = s.discover(context.Background(), &j); e != nil {
			t.Fatal(e)
		}
		stage := filepath.Join(vps, ".filebeans-stage", j.ID)
		os.MkdirAll(stage, 0700)
		os.WriteFile(filepath.Join(stage, "resume"), []byte("wrong prefix"), 0600)
		j.Manifest[0].Done = 12
		j.Done = 12
		if e = s.save(j.ID, "copy", "running", j); e != nil {
			t.Fatal(e)
		}
		// Reload as a freshly restarted process would.
		var reloaded Job
		if e = s.read(j.ID, "copy", &reloaded); e != nil {
			t.Fatal(e)
		}
		s.runJob(context.Background(), &reloaded)
		if reloaded.State != "completed" {
			t.Fatal(reloaded.Error)
		}
		got, _ := os.ReadFile(filepath.Join(vps, "resume"))
		if sha256.Sum256(got) != sha256.Sum256(payload) {
			t.Fatal("corrupt resume")
		}
	})
	t.Run("publish-crash-recovery", func(t *testing.T) {
		os.WriteFile(filepath.Join(share, "published"), payload, 0600)
		j := submit(CopyRequest{Items: []Item{{Source: "samba", Path: "published"}}, Destination: Item{Source: "vps", Path: "/"}}, "copy-test-key-0004")
		if e = s.discover(context.Background(), &j); e != nil {
			t.Fatal(e)
		}
		os.WriteFile(filepath.Join(vps, "published"), payload, 0640)
		j.Current = "publish:published"
		j.Manifest[0].Done = int64(len(payload))
		j.Done = j.Total
		if e = s.save(j.ID, "copy", "running", j); e != nil {
			t.Fatal(e)
		}
		s.runJob(context.Background(), &j)
		if j.State != "completed" {
			t.Fatal(j.State, j.Error)
		}
	})
	t.Run("cancel-before-start", func(t *testing.T) {
		j := submit(CopyRequest{Items: []Item{{Source: "samba", Path: "folder"}}, Destination: Item{Source: "vps", Path: "/"}}, "copy-test-key-0005")
		j.Cancel = true
		s.save(j.ID, "copy", j.State, j)
		s.runJob(context.Background(), &j)
		if j.State != "cancelled" {
			t.Fatal(j.State)
		}
	})
	t.Run("private-trash", func(t *testing.T) { testPrivateTrash(t, s) })
	t.Run("trash-api-files-and-folders", func(t *testing.T) { testTrashAPI(t, s) })
	t.Run("trash-restore-retention", func(t *testing.T) {
		os.WriteFile(filepath.Join(vps, "original"), []byte("original"), 0600)
		item, e := s.trash(Item{Source: "vps", Path: "/original"}, "trash-test-key-0001")
		if e != nil {
			t.Fatal(e)
		}
		if item.Expires-item.Deleted != 864000 {
			t.Fatal("wrong retention")
		}
		if _, e = os.Stat(filepath.Join(vps, "original")); !errors.Is(e, os.ErrNotExist) {
			t.Fatal("original still present")
		}
		os.WriteFile(filepath.Join(vps, "original"), []byte("collision"), 0600)
		if e = s.restore(item.ID); !errors.Is(e, ErrConflict) {
			t.Fatal("restore collision", e)
		}
		os.Remove(filepath.Join(vps, "original"))
		if e = s.restore(item.ID); e != nil {
			t.Fatal(e)
		}
		item, e = s.trash(Item{Source: "vps", Path: "original"}, "trash-test-key-0002")
		if e != nil {
			t.Fatal(e)
		}
		os.MkdirAll(filepath.Join(vps, "Trash/untracked"), 0700)
		os.WriteFile(filepath.Join(vps, "Trash/untracked/keep"), []byte("keep"), 0600)
		s.reconcileTrash()
		if _, e = os.Stat(filepath.Join(vps, trashPath(item))); e != nil {
			t.Fatal("premature purge")
		}
		item.Expires = time.Now().Unix() - 1
		s.save(item.ID, "trash", item.State, item)
		s.reconcileTrash()
		if _, e = os.Stat(filepath.Join(vps, trashPath(item))); !errors.Is(e, os.ErrNotExist) {
			t.Fatal("expired item retained")
		}
		if _, e = os.Stat(filepath.Join(vps, "Trash/untracked/keep")); e != nil {
			t.Fatal("untracked file removed")
		}
	})
	t.Run("trash-intent-recovery", func(t *testing.T) {
		os.WriteFile(filepath.Join(share, "recover"), []byte("safe"), 0600)
		entry := Trash{ID: ID(), Source: "samba", Original: "recover", Name: "recover", State: "trashing", Deleted: time.Now().Unix(), Expires: time.Now().Add(time.Hour).Unix()}
		if e = s.insert(entry.ID, "trash", entry.State, "", entry); e != nil {
			t.Fatal(e)
		}
		os.MkdirAll(filepath.Join(share, "Trash", entry.ID), 0700)
		os.Rename(filepath.Join(share, "recover"), filepath.Join(share, trashPath(entry)))
		s.reconcileTrash()
		if e = s.read(entry.ID, "trash", &entry); e != nil || entry.State != "retained" {
			t.Fatal("trash recovery", e, entry.State)
		}
	})
	t.Run("drive-download-runs-in-worker", func(t *testing.T) {
		useRcloneFixture(t, s, payload, "copy")
		previous := googleHTTP
		defer func() { googleHTTP = previous }()
		privateJSON(filepath.Join(s.config.State, "google.json"), Google{Access: "fixture", Refresh: "fixture", Expires: time.Now().Add(time.Hour).Unix(), Connection: "drive-test"})
		googleHTTP = &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
			if r.Method != "GET" {
				t.Fatal("Drive mutation")
			}
			data := fmt.Sprintf(`{"id":"drive-file","name":"drive-video","mimeType":"video/mp4","size":"%d","version":"1","capabilities":{"canDownload":true}}`, len(payload))
			var reader io.Reader = strings.NewReader(data)
			if r.URL.Query().Get("alt") == "media" {
				reader = bytes.NewReader(payload)
			}
			return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(reader)}, nil
		})}
		j := submit(CopyRequest{Items: []Item{{Source: "drive", Path: "drive-file"}}, Destination: Item{Source: "vps", Path: "/"}}, "copy-test-key-drive")
		s.runJob(context.Background(), &j)
		if j.State != "completed" {
			t.Fatal(j.State, j.Error)
		}
		got, e := os.ReadFile(filepath.Join(vps, "drive-video"))
		if e != nil || !bytes.Equal(got, payload) {
			t.Fatal("Drive copy mismatch", e)
		}
	})

	t.Run("updated-source-is-reconciled-on-retry", func(t *testing.T) {
		os.WriteFile(filepath.Join(share, "changing"), []byte("before"), 0600)
		j := submit(CopyRequest{Items: []Item{{Source: "samba", Path: "changing"}}, Destination: Item{Source: "vps", Path: "/"}}, "copy-test-key-0006")
		if e = s.discover(context.Background(), &j); e != nil {
			t.Fatal(e)
		}
		os.WriteFile(filepath.Join(share, "changing"), []byte("after and different"), 0600)
		s.runJob(context.Background(), &j)
		if j.State != "completed" {
			t.Fatal(j.State, j.Error)
		}
		got, err := os.ReadFile(filepath.Join(vps, "changing"))
		if err != nil || string(got) != "after and different" {
			t.Fatal("updated source not reconciled", err)
		}
	})

	t.Run("missing-mount-retries-without-local-fallback", func(t *testing.T) {
		s.config.Roots = append(s.config.Roots, Root{Name: "offline", Path: share, Mount: "/no-such-mount", MountSource: "//fixture/offline"})
		defer func() { s.config.Roots = s.config.Roots[:2] }()
		j := Job{ID: ID(), State: "queued", Request: CopyRequest{Items: []Item{{Source: "offline", Path: "folder"}}, Destination: Item{Source: "vps", Path: "."}}}
		if e = s.insert(j.ID, "copy", j.State, "", j); e != nil {
			t.Fatal(e)
		}
		s.runJob(context.Background(), &j)
		if j.State != "waiting" || j.Next <= time.Now().Unix() {
			t.Fatal("missing mount was not deferred")
		}
	})
	t.Run("partial-discovery-is-never-executed", func(t *testing.T) {
		s.config.Roots = append(s.config.Roots, Root{Name: "offline", Path: share, Mount: "/no-such-mount", MountSource: "//fixture/offline"})
		defer func() { s.config.Roots = s.config.Roots[:2] }()
		os.WriteFile(filepath.Join(share, "discovery-first"), []byte("keep"), 0600)
		j := Job{ID: ID(), State: "queued", Request: CopyRequest{Items: []Item{{Source: "samba", Path: "discovery-first"}, {Source: "offline", Path: "second"}}, Destination: Item{Source: "vps", Path: "."}}}
		if e = s.insert(j.ID, "copy", j.State, "", j); e != nil {
			t.Fatal(e)
		}
		s.runJob(context.Background(), &j)
		if j.State != "waiting" || len(j.Manifest) != 0 {
			t.Fatal("partial discovery was persisted", j.State, len(j.Manifest))
		}
		if _, e = os.Stat(filepath.Join(vps, "discovery-first")); !errors.Is(e, os.ErrNotExist) {
			t.Fatal("partially discovered job published")
		}
	})

	t.Run("database-outage-preserves-original", func(t *testing.T) {
		os.WriteFile(filepath.Join(vps, "db-outage"), []byte("keep"), 0600)
		closed, e := sql.Open("mysql", dsn.FormatDSN())
		if e != nil {
			t.Fatal(e)
		}
		closed.Close()
		previous := s.db
		s.db = closed
		_, e = s.trash(Item{Source: "vps", Path: "db-outage"}, "trash-test-key-0003")
		s.db = previous
		if e == nil {
			t.Fatal("delete accepted without persistence")
		}
		if data, e := os.ReadFile(filepath.Join(vps, "db-outage")); e != nil || string(data) != "keep" {
			t.Fatal("original changed during outage")
		}
	})

	t.Run("staged-hardlinks-are-replaced-independently", func(t *testing.T) {
		os.WriteFile(filepath.Join(share, "case-one"), []byte("first file"), 0600)
		os.WriteFile(filepath.Join(share, "case-two"), []byte("second file"), 0600)
		j := submit(CopyRequest{Items: []Item{{Source: "samba", Path: "case-one"}, {Source: "samba", Path: "case-two"}}, Destination: Item{Source: "vps", Path: "/"}}, "copy-case-alias-test")
		if e = s.discover(context.Background(), &j); e != nil {
			t.Fatal(e)
		}
		stage := filepath.Join(vps, ".filebeans-stage", j.ID)
		os.MkdirAll(stage, 0700)
		os.WriteFile(filepath.Join(stage, "case-one"), nil, 0600)
		// A shared inode emulates names aliasing on a case-insensitive share.
		if e = os.Link(filepath.Join(stage, "case-one"), filepath.Join(stage, "case-two")); e != nil {
			t.Fatal(e)
		}
		s.runJob(context.Background(), &j)
		if j.State != "completed" {
			t.Fatal(j.State, j.Error)
		}
		one, err := os.ReadFile(filepath.Join(vps, "case-one"))
		if err != nil || string(one) != "first file" {
			t.Fatal("first alias corrupted", err)
		}
		two, err := os.ReadFile(filepath.Join(vps, "case-two"))
		if err != nil || string(two) != "second file" {
			t.Fatal("second alias corrupted", err)
		}
		first, _ := os.Stat(filepath.Join(vps, "case-one"))
		second, _ := os.Stat(filepath.Join(vps, "case-two"))
		if os.SameFile(first, second) {
			t.Fatal("staged hardlink not replaced")
		}
	})

	t.Run("maintenance-expires-without-copy-worker", func(t *testing.T) {
		os.WriteFile(filepath.Join(vps, "maintenance-expiry"), []byte("fixture"), 0600)
		entry, e := s.trash(Item{Source: "vps", Path: "maintenance-expiry"}, "maintenance-test-key")
		if e != nil {
			t.Fatal(e)
		}
		entry.Expires = time.Now().Unix() - 1
		if e = s.save(entry.ID, "trash", entry.State, entry); e != nil {
			t.Fatal(e)
		}
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		go func() { defer close(done); s.maintenance(ctx) }()
		defer func() { cancel(); <-done }()
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			var current Trash
			if e = s.read(entry.ID, "trash", &current); e != nil {
				t.Fatal(e)
			}
			if current.State == "purged" {
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatal("independent maintenance did not expire the retained entry")
	})

	t.Run("permanent-confirmation", func(t *testing.T) {
		r := httptest.NewRequest("POST", "/v1/trash/empty", nil)
		r.Header.Set("Authorization", "Bearer "+s.config.Token)
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, r)
		if w.Code != 400 {
			t.Fatal("purge without confirmation", w.Code)
		}
	})
	t.Run("drive-write-workflows", func(t *testing.T) { testDriveWriteWorkflow(t, s) })

	t.Run("normalization-workflows", func(t *testing.T) { testNormalizationWorkflow(t, s) })

}
