package service

import (
	"context"
	"encoding/json"
	"errors"
	"filebeans/companion/protection"
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

func TestHostingFileBoundaries(t *testing.T) {
	s, base := filesystemFixture(t)
	os.MkdirAll(filepath.Join(base, "Private"), 0700)
	os.WriteFile(filepath.Join(base, "Private", "secret.txt"), []byte("secret"), 0600)
	os.WriteFile(filepath.Join(base, "report.txt"), []byte("report"), 0600)
	os.Symlink("Private/secret.txt", filepath.Join(base, "alias.txt"))
	for _, path := range []string{"/", "/Private", "/alias.txt", "/../secret.txt", "/Trash/report.txt", "/.filebeans-stage/report.txt", "/.filebeans-vaults/report.txt", "/.filebeans-thumbnails/report.txt"} {
		if _, err := s.prepareHosting(context.Background(), hostingRequest{Item: Item{Source: "samba", Path: path}}); err == nil {
			t.Fatalf("hosted unsafe path %s", path)
		}
	}
	for _, source := range []string{"upload", "unknown"} {
		if _, err := s.prepareHosting(context.Background(), hostingRequest{Item: Item{Source: source, Path: "/report.txt"}}); err == nil {
			t.Fatal("hosted internal/unknown source", source)
		}
	}
	for _, state := range []bool{false, true} {
		s.vaults = &vaultManager{states: []*vaultState{{config: VaultConfig{Source: "samba", Path: "/Private"}, unlocked: state}}}
		if _, err := s.prepareHosting(context.Background(), hostingRequest{Item: Item{Source: "samba", Path: "/Private/secret.txt"}}); err == nil {
			t.Fatal("hosted protected file", state)
		}
	}
	s.vaults = nil
	s.config.Vaults = []VaultConfig{{Source: "samba", Path: "/Private"}}
	if _, err := s.prepareHosting(context.Background(), hostingRequest{Item: Item{Source: "samba", Path: "/Private/secret.txt"}}); err == nil {
		t.Fatal("hosted configured protected file")
	}
	if _, err := s.prepareHosting(context.Background(), hostingRequest{Item: Item{Source: "samba", Path: "/report.txt"}}); err != nil {
		t.Fatal("ordinary file rejected", err)
	}
	for _, expires := range []int64{-1, time.Now().Unix() - 1, 1 << 62} {
		if err := hostingExpiry(expires); err == nil {
			t.Fatal("invalid expiry", expires)
		}
	}
	for _, name := range []string{"report.pdf", "Résumé + notes #1?.txt", "图片.jpg", strings.Repeat("a", 200) + ".pdf"} {
		slug := hostingSlug(name)
		if !hostingID(slug) || len(slug) > 36 {
			t.Fatal("invalid readable slug", slug)
		}
	}
}

func TestHostingBrokerProtection(t *testing.T) {
	if os.Getuid() != 0 {
		t.Skip("root-owned broker snapshot fixture required")
	}
	s, base := filesystemFixture(t)
	runtime := t.TempDir()
	os.MkdirAll(filepath.Join(base, "Private"), 0700)
	os.WriteFile(filepath.Join(base, "Private", "secret.txt"), []byte("secret"), 0600)
	os.WriteFile(filepath.Join(base, "report.txt"), []byte("report"), 0600)
	os.WriteFile(filepath.Join(runtime, "gate"), nil, 0640)
	guard, err := protection.New(runtime, map[string]string{"samba": base})
	if err != nil {
		t.Fatal(err)
	}
	s.vaults = &vaultManager{guard: guard, remote: newBrokerClient(runtime, s.config.Roots)}
	for _, state := range []string{"locked", "unlocked", "error"} {
		snapshot := protection.Snapshot{Version: protection.Protocol, Vaults: []protection.Vault{{ID: strings.Repeat("a", 32), Source: "samba", Path: "/Private", State: state, Initialized: true}}}
		data, _ := json.Marshal(snapshot)
		os.WriteFile(filepath.Join(runtime, "status.json"), data, 0640)
		if _, err := s.prepareHosting(context.Background(), hostingRequest{Item: Item{Source: "samba", Path: "/Private/secret.txt"}}); err == nil {
			t.Fatal("hosted broker protected file", state)
		}
		if _, err := s.prepareHosting(context.Background(), hostingRequest{Item: Item{Source: "samba", Path: "/report.txt"}}); err != nil {
			t.Fatal("broker ordinary file rejected", state, err)
		}
	}
	os.Remove(filepath.Join(runtime, "status.json"))
	if _, err := s.prepareHosting(context.Background(), hostingRequest{Item: Item{Source: "samba", Path: "/report.txt"}}); err == nil {
		t.Fatal("missing broker snapshot did not fail closed")
	}
}

func hostingCall(s *Service, method, path, payload string, authenticated bool, headers map[string]string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(payload))
	if authenticated {
		r.Header.Set("Authorization", "Bearer "+s.config.Token)
	}
	for key, value := range headers {
		r.Header.Set(key, value)
	}
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	return w
}

func TestHostingDatabase(t *testing.T) {
	config := os.Getenv("FILEBEANS_TEST_CONFIG")
	if config == "" {
		t.Skip("set FILEBEANS_TEST_CONFIG to a VM .test-config JSON file")
	}
	if !strings.HasPrefix(filepath.Clean(config), "/home/dev/projects/.test-config/") {
		t.Fatal("only VM test configuration is allowed")
	}
	var cfg struct {
		Host     string
		Port     int
		User     string
		Password string
		Database string
	}
	data, err := os.ReadFile(config)
	if err != nil || json.Unmarshal(data, &cfg) != nil {
		t.Fatal("test config unavailable")
	}
	dsn := mysql.NewConfig()
	dsn.User = cfg.User
	dsn.Passwd = cfg.Password
	dsn.Net = "tcp"
	dsn.Addr = cfg.Host + ":" + strconv.Itoa(cfg.Port)
	dsn.DBName = cfg.Database
	db, err := openDB(dsn.FormatDSN())
	if err != nil {
		t.Fatal("test database unavailable")
	}
	defer db.Close()
	var count int
	if err = db.QueryRow("SELECT COUNT(*) FROM information_schema.tables WHERE table_schema=DATABASE() AND table_name='fb_operations'").Scan(&count); err != nil || count != 0 {
		t.Fatal("refusing to modify pre-existing fb_operations table", err)
	}
	s, base := filesystemFixture(t)
	s.db = db
	if err = s.Migrate(); err != nil {
		t.Fatal(err)
	}
	defer db.Exec("DROP TABLE fb_operations")
	os.WriteFile(filepath.Join(base, "Résumé + notes #1?.txt"), []byte("0123456789"), 0600)
	payload := `{"item":{"source":"samba","path":"/Résumé + notes #1?.txt"},"expires":0}`
	for _, method := range []string{"GET", "POST", "PATCH", "DELETE"} {
		path := "/v1/hosting"
		if method == "PATCH" || method == "DELETE" {
			path += "/file"
		}
		if got := hostingCall(s, method, path, payload, false, nil); got.Code != 401 {
			t.Fatal("anonymous management", method, got.Code)
		}
	}
	create := func() Hosting {
		t.Helper()
		w := hostingCall(s, "POST", "/v1/hosting", payload, true, nil)
		if w.Code != 201 {
			t.Fatal(w.Code, w.Body.String())
		}
		var h Hosting
		if err := json.Unmarshal(w.Body.Bytes(), &h); err != nil {
			t.Fatal(err)
		}
		return h
	}
	h := create()
	collision := create()
	if h.ID == collision.ID || !strings.HasSuffix(collision.ID, "-2") || !strings.HasSuffix(h.URLPath, "R%C3%A9sum%C3%A9%20+%20notes%20%231%3F.txt") {
		t.Fatal("readable collision or escaped filename", h, collision)
	}
	// Simulate a process restart with the same persistent database.
	restarted := &Service{db: db, config: s.config}
	if got := hostingCall(restarted, "GET", h.URLPath, "", false, nil); got.Code != 200 || got.Body.String() != "0123456789" || !strings.HasPrefix(got.Header().Get("Content-Disposition"), "attachment;") || got.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("public attachment", got.Code, got.Body.String(), got.Header())
	}
	if got := hostingCall(s, "HEAD", h.URLPath, "", false, nil); got.Code != 200 || got.Body.Len() != 0 || got.Header().Get("Content-Length") != "10" {
		t.Fatal("HEAD", got.Code, got.Header())
	}
	if got := hostingCall(s, "GET", h.URLPath, "", false, map[string]string{"Range": "bytes=3-5"}); got.Code != 206 || got.Body.String() != "345" {
		t.Fatal("range", got.Code, got.Body.String())
	}
	for _, route := range []string{"/h/" + h.ID + "/wrong.txt", "/h/missing/file.txt", "/v1/fs/content?source=samba&path=/Résumé.txt"} {
		got := hostingCall(s, "GET", route, "", false, nil)
		if got.Code == 200 {
			t.Fatal("exposed unrelated file", route)
		}
	}
	h.Expires = time.Now().Unix() - 1
	if err := s.save(hostingKey(h.ID), "hosting", "active", h); err != nil {
		t.Fatal(err)
	}
	if got := hostingCall(s, "GET", h.URLPath, "", false, nil); got.Code != 404 {
		t.Fatal("expired link served", got.Code)
	}
	got := hostingCall(s, "GET", "/v1/hosting", "", true, nil)
	if got.Code != 200 || !strings.Contains(got.Body.String(), `"state":"expired"`) {
		t.Fatal("expired management entry", got.Code, got.Body.String())
	}
	for _, expires := range []int64{time.Now().Add(time.Hour).Unix(), 0} {
		got = hostingCall(s, "PATCH", "/v1/hosting/"+h.ID, fmt.Sprintf(`{"expires":%d}`, expires), true, nil)
		if got.Code != 200 {
			t.Fatal("renew", got.Code, got.Body.String())
		}
	}
	for i := 0; i < 2; i++ {
		if got = hostingCall(s, "DELETE", "/v1/hosting/"+h.ID, "", true, nil); got.Code != 204 {
			t.Fatal("stop hosting", got.Code, got.Body.String())
		}
	}
	if got = hostingCall(s, "GET", h.URLPath, "", false, nil); got.Code != 404 {
		t.Fatal("revoked link served", got.Code)
	}
	if got = hostingCall(s, "PATCH", "/v1/hosting/"+h.ID, `{"expires":0}`, true, nil); got.Code != 404 {
		t.Fatal("revoked link revived", got.Code)
	}
	third := create()
	if third.ID == h.ID || third.ID == collision.ID {
		t.Fatal("reserved slug reused")
	}
	// An existing ordinary link must fail when its path becomes protected.
	s.config.Vaults = []VaultConfig{{Source: "samba", Path: "/Résumé + notes #1?.txt"}}
	if got = hostingCall(s, "GET", collision.URLPath, "", false, nil); got.Code != 404 {
		t.Fatal("now-protected link served", got.Code)
	}
	if got = hostingCall(s, "POST", "/v1/hosting", payload, true, nil); got.Code == 201 {
		t.Fatal("forged protected request accepted")
	}
	s.config.Vaults = nil
	// Replacing the path cannot expose a different file, even with identical bytes/time.
	st, _ := os.Stat(filepath.Join(base, "Résumé + notes #1?.txt"))
	os.WriteFile(filepath.Join(base, "replacement"), []byte("0123456789"), 0600)
	os.Chtimes(filepath.Join(base, "replacement"), st.ModTime(), st.ModTime())
	os.Rename(filepath.Join(base, "replacement"), filepath.Join(base, "Résumé + notes #1?.txt"))
	if got = hostingCall(s, "GET", collision.URLPath, "", false, nil); got.Code != 404 {
		t.Fatal("replacement exposed", got.Code)
	}
	t.Run("drive", func(t *testing.T) { testHostingDrive(t, s) })
}

func testHostingDrive(t *testing.T, s *Service) {
	err := privateJSON(filepath.Join(s.config.State, "google.json"), Google{Access: "fixture", Refresh: "fixture", Connection: "one", Expires: time.Now().Add(time.Hour).Unix()})
	if err != nil {
		t.Fatal(err)
	}
	previous := googleHTTP
	defer func() { googleHTTP = previous }()
	version, name, mimeType, canDownload := "1", "report.pdf", "application/pdf", true
	googleHTTP = &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
		if r.Method != "GET" {
			return nil, errors.New("unexpected Drive mutation")
		}
		if r.URL.Query().Get("alt") == "media" {
			return &http.Response{StatusCode: 206, Header: http.Header{"Content-Length": {"3"}, "Content-Range": {"bytes 3-5/10"}}, Body: io.NopCloser(strings.NewReader("345"))}, nil
		}
		data := fmt.Sprintf(`{"id":"drive-file","name":%q,"mimeType":%q,"size":"10","version":%q,"capabilities":{"canDownload":%t}}`, name, mimeType, version, canDownload)
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(data))}, nil
	})}
	payload := `{"item":{"source":"drive","path":"drive-file"},"expires":0}`
	got := hostingCall(s, "POST", "/v1/hosting", payload, true, nil)
	if got.Code != 201 {
		t.Fatal(got.Code, got.Body.String())
	}
	var h Hosting
	json.Unmarshal(got.Body.Bytes(), &h)
	if got = hostingCall(s, "GET", h.URLPath, "", false, map[string]string{"Range": "bytes=3-5"}); got.Code != 206 || got.Body.String() != "345" || !strings.HasPrefix(got.Header().Get("Content-Disposition"), "attachment;") {
		t.Fatal("Drive range download", got.Code, got.Body.String())
	}
	if got = hostingCall(s, "HEAD", h.URLPath, "", false, nil); got.Code != 200 || got.Body.Len() != 0 {
		t.Fatal("Drive HEAD", got.Code)
	}
	version = "2"
	if got = hostingCall(s, "GET", h.URLPath, "", false, nil); got.Code != 404 {
		t.Fatal("changed Drive content served")
	}
	version = "1"
	canDownload = false
	if got = hostingCall(s, "GET", h.URLPath, "", false, nil); got.Code != 404 {
		t.Fatal("Drive revoked capability served")
	}
	canDownload = true
	mimeType = "application/vnd.google-apps.document"
	if got = hostingCall(s, "POST", "/v1/hosting", payload, true, nil); got.Code == 201 {
		t.Fatal("native document hosted")
	}
	mimeType = "application/pdf"
	privateJSON(filepath.Join(s.config.State, "google.json"), Google{Access: "fixture", Refresh: "fixture", Connection: "other", Expires: time.Now().Add(time.Hour).Unix()})
	if got = hostingCall(s, "GET", h.URLPath, "", false, nil); got.Code != 404 {
		t.Fatal("Drive account switch served")
	}
}
