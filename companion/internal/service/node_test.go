package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func testNode(t *testing.T, name string) *Service {
	t.Helper()
	state := t.TempDir()
	root := t.TempDir()
	c := Config{State: state, SQLite: filepath.Join(state, "operations.sqlite"), ServerID: name, Token: strings.Repeat(name[:1], 32), Roots: []Root{{Name: name, Path: root}}, Filesystem: FilesystemConfig{Enabled: true}}
	s, e := New(c)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(s.Close)
	if e = s.Migrate(); e != nil {
		t.Fatal(e)
	}
	return s
}

func TestNodeTLSIdentityPersistsAndRejectsPartialReplacement(t *testing.T) {
	c := Config{Profile: "pi", ServerID: "filebeans-pi", State: t.TempDir()}
	first, e := EnsureNodeTLS(&c)
	if e != nil || len(first) != 64 {
		t.Fatal(first, e)
	}
	second, e := EnsureNodeTLS(&c)
	if e != nil || first != second {
		t.Fatal("identity changed", e)
	}
	pair, e := tls.LoadX509KeyPair(c.TLS.Certificate, c.TLS.Key)
	if e != nil || len(pair.Certificate) != 1 {
		t.Fatal(e)
	}
	st, e := os.Stat(c.TLS.Key)
	if e != nil || st.Mode().Perm() != 0600 {
		t.Fatal("private key permissions", e)
	}
	if e = os.Remove(c.TLS.Certificate); e != nil {
		t.Fatal(e)
	}
	if _, e = EnsureNodeTLS(&c); !errors.Is(e, ErrConflict) {
		t.Fatal("silently replaced existing key", e)
	}
}

func TestFederatedJobSubmissionAndControlKeepOwner(t *testing.T) {
	a := testNode(t, "Pi")
	b := testNode(t, "VPS")
	as := httptest.NewTLSServer(a.Handler())
	defer as.Close()
	bs := httptest.NewTLSServer(b.Handler())
	defer bs.Close()
	a.config.Peers = []PeerConfig{testPeer(bs, "vps", "VPS", b.config.Token)}
	b.config.Peers = []PeerConfig{testPeer(as, "pi", "Pi", a.config.Token)}
	if e := os.WriteFile(filepath.Join(a.config.Roots[0].Path, "file"), []byte("fixture"), 0600); e != nil {
		t.Fatal(e)
	}
	payload := `{"items":[{"source":"Pi","path":"file"}],"destination":{"source":"VPS","path":"."}}`
	call := func(method, path string, payload string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(payload))
		r.Header.Set("Authorization", "Bearer "+a.config.Token)
		r.Header.Set("Idempotency-Key", "stable-submission-key")
		w := httptest.NewRecorder()
		a.Handler().ServeHTTP(w, r)
		return w
	}
	w := call("POST", "/v1/jobs", payload)
	if w.Code != 202 {
		t.Fatal(w.Code, w.Body.String())
	}
	var job Job
	if e := json.Unmarshal(w.Body.Bytes(), &job); e != nil || !strings.HasPrefix(job.ID, "vps~") {
		t.Fatal(job, e)
	}
	w = call("POST", "/v1/jobs", payload)
	if w.Code != 200 {
		t.Fatal("duplicate submission", w.Code, w.Body.String())
	}
	var duplicate Job
	json.Unmarshal(w.Body.Bytes(), &duplicate)
	if duplicate.ID != job.ID {
		t.Fatal("duplicate job created")
	}
	w = call("GET", "/v1/jobs/"+job.ID, "")
	if w.Code != 200 {
		t.Fatal("job routed to wrong owner", w.Code, w.Body.String())
	}
	w = call("GET", "/v1/trash?source=VPS", "")
	if w.Code != 200 || strings.TrimSpace(w.Body.String()) != "[]" {
		t.Fatal("remote array response", w.Code, w.Body.String())
	}
}

func TestPeerRejectsDuplicateDestinations(t *testing.T) {
	req := CopyRequest{Items: []Item{{Source: "Pi", Path: "a/file"}, {Source: "VPS", Path: "b/file"}}, Destination: Item{Source: "Pi", Path: "."}}
	plan := []PeerSelection{{Item: req.Items[0], Files: []File{{Source: "Pi", From: "a/file", To: "file", Hash: "sha256:" + strings.Repeat("0", 64)}}}, {Item: req.Items[1], Files: []File{{Source: "VPS", From: "b/file", To: "file", Hash: "sha256:" + strings.Repeat("0", 64)}}}}
	if e := validatePeerPlan(req, plan); !errors.Is(e, ErrConflict) {
		t.Fatal("duplicate names can omit a selection", e)
	}
}

func TestSourceMetadataMigrationPreservesTrashAndRefusesActiveJobs(t *testing.T) {
	s := testNode(t, "Pi")
	target := testNode(t, "Pi")
	bundle := filepath.Join(t.TempDir(), "bundle.json")
	raw := []byte(`{"version":1,"source":"Pi","entries":[{"id":"` + strings.Repeat("a", 32) + `","source":"Pi"}]}`)
	if e := os.WriteFile(bundle, raw, 0600); e != nil {
		t.Fatal(e)
	}
	trash := Trash{ID: ID(), Source: "Pi", Original: "file", Name: "file", State: "retained"}
	if e := s.insert(trash.ID, "trash", trash.State, "retained-trash-operation", trash); e != nil {
		t.Fatal(e)
	}
	output := filepath.Join(t.TempDir(), "operations.json")
	if e := MigrationOperations(s.config, bundle, output, false); e != nil {
		t.Fatal(e)
	}
	if e := MigrationOperations(target.config, bundle, output, true); e != nil {
		t.Fatal(e)
	}
	if e := MigrationOperations(target.config, bundle, output, true); e != nil {
		t.Fatal("import recovery", e)
	}
	var imported Trash
	if e := target.read(trash.ID, "trash", &imported); e != nil || imported.Original != trash.Original {
		t.Fatal(imported, e)
	}
	j := Job{ID: ID(), State: "running", Request: CopyRequest{Items: []Item{{Source: "Pi", Path: "file"}}, Destination: Item{Source: "Pi", Path: "dest"}}}
	if e := s.insert(j.ID, "copy", j.State, "active-transfer-key", j); e != nil {
		t.Fatal(e)
	}
	if e := MigrationOperations(s.config, bundle, filepath.Join(t.TempDir(), "refused.json"), false); e == nil {
		t.Fatal("active source transfer allowed during migration")
	}
}
func testPeer(p *httptest.Server, id, source, token string) PeerConfig {
	h := sha256.Sum256(p.Certificate().Raw)
	return PeerConfig{ID: id, URL: p.URL, Token: token, Sources: []string{source}, Fingerprint: hex.EncodeToString(h[:])}
}
func TestSQLiteDurableJobsAndUniqueness(t *testing.T) {
	s := testNode(t, "Pi")
	j := Job{ID: ID(), State: "queued", Request: CopyRequest{Items: []Item{{Source: "Pi", Path: "file"}}, Destination: Item{Source: "Pi", Path: "dest"}}}
	if e := s.insert(j.ID, "copy", j.State, "unique-submission-key", j); e != nil {
		t.Fatal(e)
	}
	if e := s.insert(ID(), "copy", j.State, "unique-submission-key", j); e == nil {
		t.Fatal("duplicate idempotency key accepted")
	}
	var saved Job
	if e := s.existing("unique-submission-key", "copy", &saved); e != nil || saved.ID != j.ID {
		t.Fatal(saved, e)
	}
	if e := s.db.Close(); e != nil {
		t.Fatal(e)
	}
	var e error
	s.db, e = openSQLite(s.config.SQLite)
	if e != nil {
		t.Fatal(e)
	}
	if e = s.read(j.ID, "copy", &saved); e != nil || saved.Request.Items[0].Path != "file" {
		t.Fatal(saved, e)
	}
	if _, e = s.archivePolicyLinks(); e != nil {
		t.Fatal("SQLite archive policy query", e)
	}
	if e = s.migrateVaultRecords(); e != nil {
		t.Fatal("SQLite record migration", e)
	}
}
func TestPeerCopyMoveAndRecovery(t *testing.T) {
	for _, operation := range []string{"copy", "move"} {
		t.Run(operation, func(t *testing.T) {
			a := testNode(t, "Pi")
			b := testNode(t, "VPS")
			server := httptest.NewTLSServer(a.Handler())
			defer server.Close()
			b.config.Peers = []PeerConfig{testPeer(server, "pi", "Pi", a.config.Token)}
			source := filepath.Join(a.config.Roots[0].Path, "folder")
			if e := os.Mkdir(source, 0700); e != nil {
				t.Fatal(e)
			}
			data := bytes.Repeat([]byte("bounded peer transfer\n"), 40000)
			if e := os.WriteFile(filepath.Join(source, "file.txt"), data, 0600); e != nil {
				t.Fatal(e)
			}
			if e := os.Mkdir(filepath.Join(source, "empty"), 0700); e != nil {
				t.Fatal(e)
			}
			j := Job{ID: ID(), State: "queued", Request: CopyRequest{Operation: operation, Items: []Item{{Source: "Pi", Path: "folder"}}, Destination: Item{Source: "VPS", Path: "."}}}
			if e := b.insert(j.ID, "copy", j.State, "test-submission-key", j); e != nil {
				t.Fatal(e)
			}
			if e := b.executePeer(context.Background(), &j); e != nil {
				t.Fatal(e)
			}
			actual, e := os.ReadFile(filepath.Join(b.config.Roots[0].Path, "folder", "file.txt"))
			if e != nil || !bytes.Equal(data, actual) {
				t.Fatal("destination mismatch", e)
			}
			// Recovery must not overwrite, recopy or repeat source deletion.
			if e = b.executePeer(context.Background(), &j); e != nil {
				t.Fatal("idempotent recovery", e)
			}
			_, e = os.Stat(source)
			if operation == "move" && !errors.Is(e, os.ErrNotExist) {
				t.Fatal("move source remains", e)
			}
			if operation == "copy" && e != nil {
				t.Fatal("copy removed source", e)
			}
		})
	}
}
func TestPeerRejectsChangedSourceAndCertificate(t *testing.T) {
	a := testNode(t, "Pi")
	server := httptest.NewTLSServer(a.Handler())
	defer server.Close()
	b := testNode(t, "VPS")
	peer := testPeer(server, "pi", "Pi", a.config.Token)
	b.config.Peers = []PeerConfig{peer}
	file := filepath.Join(a.config.Roots[0].Path, "file")
	os.WriteFile(file, []byte("before"), 0600)
	files, e := b.peerSnapshot(context.Background(), Item{Source: "Pi", Path: "file"})
	if e != nil {
		t.Fatal(e)
	}
	os.WriteFile(file, []byte("after"), 0600)
	if e = b.removePeerSource(context.Background(), PeerSelection{Item: Item{Source: "Pi", Path: "file"}, Files: files}, strings.Repeat("a", 64)); !errors.Is(e, ErrConflict) {
		t.Fatal("changed source removed", e)
	}
	if _, e = os.Stat(file); e != nil {
		t.Fatal("source lost", e)
	}
	peer.Fingerprint = strings.Repeat("0", 64)
	b.config.Peers = []PeerConfig{peer}
	if _, e = b.peerSnapshot(context.Background(), Item{Source: "Pi", Path: "file"}); e == nil {
		t.Fatal("wrong certificate accepted")
	}
}
func TestIndependentSourcesAndOfflineLocalAccess(t *testing.T) {
	a := testNode(t, "Pi")
	b := testNode(t, "VPS")
	server := httptest.NewTLSServer(b.Handler())
	defer server.Close()
	a.config.Peers = []PeerConfig{testPeer(server, "vps", "VPS", b.config.Token)}
	r := httptest.NewRequest("GET", "/v1/fs/sources", nil)
	r.Header.Set("Authorization", "Bearer "+a.config.Token)
	w := httptest.NewRecorder()
	a.Handler().ServeHTTP(w, r)
	var result struct {
		Scopes []struct {
			Name string `json:"name"`
		} `json:"scopes"`
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &result) != nil || len(result.Scopes) != 1 || result.Scopes[0].Name != "Pi" {
		t.Fatal(w.Code, w.Body.String())
	}
	server.Close()
	w = httptest.NewRecorder()
	a.Handler().ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatal("offline peer blocks local sources", w.Code)
	}
}
func TestPeerSnapshotLimit(t *testing.T) {
	s := testNode(t, "Pi")
	root := s.config.Roots[0].Path
	// A single huge directory must fail before allocating an unbounded inventory.
	for i := 0; i < piManifestLimit; i++ {
		if e := os.WriteFile(filepath.Join(root, ID()), nil, 0600); e != nil {
			t.Fatal(e)
		}
	}
	f, e := os.Open(root)
	if e != nil {
		t.Fatal(e)
	}
	defer f.Close()
	if _, e = boundedReadDir(f, 128); !errors.Is(e, ErrUnsafe) {
		t.Fatal("unbounded directory accepted", e)
	}
}

func TestRemoteProtectedRecordsHideNamesAndRequireSourceUnlock(t *testing.T) {
	source := testNode(t, "Pi")
	dest := testNode(t, "VPS")
	id := strings.Repeat("a", 32)
	source.vaults = &vaultManager{}
	dest.vaults = &vaultManager{}
	t.Cleanup(func() { source.vaults = nil; dest.vaults = nil })
	source.vaults.states = []*vaultState{{config: VaultConfig{ID: id, Source: "Pi", Path: "/private", Cipher: t.TempDir()}, unlocked: true}}
	source.vaults.mountCheck = func(VaultConfig) bool { return true }
	dest.vaults.metadataKey = bytes.Repeat([]byte{7}, 32)
	server := httptest.NewTLSServer(source.Handler())
	defer server.Close()
	dest.config.Peers = []PeerConfig{testPeer(server, "pi", "Pi", source.config.Token)}
	item := Item{Source: "Pi", Path: "private/sensitive-name.txt"}
	j := Job{ID: ID(), State: "queued", Request: CopyRequest{Items: []Item{item}, Destination: Item{Source: "VPS", Path: "."}}, PeerPlan: []PeerSelection{{Item: item, Files: []File{{Source: "Pi", From: item.Path, To: "sensitive-name.txt", Vault: id}}}}}
	if e := dest.insert(j.ID, "copy", j.State, "protected-peer-key", j); e != nil {
		t.Fatal(e)
	}
	var raw []byte
	if e := dest.db.QueryRow("SELECT body FROM fb_operations WHERE id=?", j.ID).Scan(&raw); e != nil {
		t.Fatal(e)
	}
	if bytes.Contains(raw, []byte("sensitive-name")) {
		t.Fatal("remote protected name written in plaintext")
	}
	var loaded Job
	if e := dest.read(j.ID, "copy", &loaded); e != nil {
		t.Fatal(e)
	}
	source.vaults.mu.Lock()
	source.vaults.states[0].unlocked = false
	source.vaults.mu.Unlock()
	if e := dest.read(j.ID, "copy", &loaded); !errors.Is(e, ErrVaultLocked) {
		t.Fatal("locked source records readable", e)
	}
	// Committed cleanup can seal completion after the original vault locks.
	j.State = "completed"
	if e := dest.save(j.ID, "copy", j.State, j); e != nil {
		t.Fatal("could not journal committed cleanup", e)
	}
	if e := dest.read(j.ID, "copy", &loaded); !errors.Is(e, ErrVaultLocked) {
		t.Fatal("completion exposed locked source names", e)
	}
}

func TestPeerMovePreservesSourceIfPublishedDestinationChanges(t *testing.T) {
	a := testNode(t, "Pi")
	b := testNode(t, "VPS")
	server := httptest.NewTLSServer(a.Handler())
	defer server.Close()
	b.config.Peers = []PeerConfig{testPeer(server, "pi", "Pi", a.config.Token)}
	source := filepath.Join(a.config.Roots[0].Path, "file")
	if e := os.WriteFile(source, []byte("original"), 0600); e != nil {
		t.Fatal(e)
	}
	item := Item{Source: "Pi", Path: "file"}
	files, e := b.peerSnapshot(context.Background(), item)
	if e != nil {
		t.Fatal(e)
	}
	j := Job{ID: ID(), State: "queued", Request: CopyRequest{Operation: "move", Items: []Item{item}, Destination: Item{Source: "VPS", Path: "."}}, PeerPlan: []PeerSelection{{Item: item, Files: files}}}
	if e = b.insert(j.ID, "copy", j.State, "changed-destination-key", j); e != nil {
		t.Fatal(e)
	}
	if e = b.copyPeerLocal(context.Background(), &j); e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(filepath.Join(b.config.Roots[0].Path, "file"), []byte("modified"), 0600); e != nil {
		t.Fatal(e)
	}
	if e = b.executePeer(context.Background(), &j); !errors.Is(e, ErrConflict) {
		t.Fatal("destination modification ignored", e)
	}
	if data, e := os.ReadFile(source); e != nil || string(data) != "original" {
		t.Fatal("move removed source after destination changed", e)
	}
}

func TestServerClientAPIsNeverDiscoverOrRelayPeerStorage(t *testing.T) {
	local := testNode(t, "Pi")
	var calls atomic.Int32
	remote := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		reply(w, 200, map[string]any{"driveConnected": true, "driveWriteAccess": true, "scopes": []map[string]string{{"name": "VPS"}}})
	}))
	defer remote.Close()
	peer := testPeer(remote, "vps", "VPS", strings.Repeat("x", 32))
	peer.Sources = append(peer.Sources, "drive")
	local.config.Peers = []PeerConfig{peer}
	for _, route := range []string{"/v1/fs/sources", "/v1/jobs", "/v1/vaults", "/v1/trash", "/v1/capabilities", "/v1/archive-links", "/v1/archive-sync/status"} {
		r := httptest.NewRequest("GET", route, nil)
		r.Header.Set("Authorization", "Bearer "+local.config.Token)
		w := httptest.NewRecorder()
		local.Handler().ServeHTTP(w, r)
		if w.Code != 200 {
			t.Fatalf("%s: %d %s", route, w.Code, w.Body.String())
		}
		if route == "/v1/capabilities" {
			var c struct {
				DriveConnected bool   `json:"driveConnected"`
				ServerID       string `json:"serverId"`
			}
			if json.Unmarshal(w.Body.Bytes(), &c) != nil || c.DriveConnected || c.ServerID != "Pi" {
				t.Fatal("remote capabilities leaked", w.Body.String())
			}
		}
	}
	for _, test := range []struct{ method, path, body string }{
		{"GET", "/v1/fs/resources?source=VPS&path=/", ""},
		{"POST", "/v1/fs/folders?source=VPS&path=/unexpected", ""},
		{"GET", "/v1/drive/files?folder=root", ""},
		{"GET", "/v1/vaults/vps~unknown", ""},
		{"POST", "/v1/vaults/vps~unknown/unlock", `{"key":"not-a-key"}`},
		{"POST", "/v1/trash/vps~unknown/restore", `{}`},
		{"POST", "/v1/protection/unlock", `{"key":"not-a-key"}`},
	} {
		r := httptest.NewRequest(test.method, test.path, strings.NewReader(test.body))
		r.Header.Set("Authorization", "Bearer "+local.config.Token)
		w := httptest.NewRecorder()
		local.Handler().ServeHTTP(w, r)
		if w.Code >= 200 && w.Code < 300 {
			t.Fatalf("unexpected peer access: %s %s: %d", test.method, test.path, w.Code)
		}
	}
	if calls.Load() != 0 {
		t.Fatalf("ordinary client APIs contacted a peer %d times", calls.Load())
	}
}
