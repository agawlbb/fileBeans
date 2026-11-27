package service

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func syntheticNativeGoogle() Google {
	return Google{Access: "test-native-access-token", Refresh: "test-native-refresh-token", Expires: time.Now().Add(time.Hour).Unix(), ClientID: "12345-native.apps.googleusercontent.com", Account: "google-account-1", Scopes: []string{driveScope}}
}

func TestNativeDriveGrantIsBoundToServerAndEncryptedAtRest(t *testing.T) {
	a, b := testNode(t, "Pi"), testNode(t, "VPS")
	grant := syntheticNativeGoogle()
	envelope, e := a.sealNativeDriveGrant(grant)
	if e != nil {
		t.Fatal(e)
	}
	opened, e := a.openNativeDriveGrant(envelope)
	if e != nil || opened.Account != grant.Account {
		t.Fatal(opened, e)
	}
	if _, e = b.openNativeDriveGrant(envelope); e == nil {
		t.Fatal("another server decrypted the grant")
	}
	broken := envelope
	broken.Sealed = append([]byte(nil), envelope.Sealed...)
	broken.Sealed[len(broken.Sealed)-1] ^= 1
	if _, e = a.openNativeDriveGrant(broken); e == nil {
		t.Fatal("tampering accepted")
	}
	key, e := a.transferPublicKey()
	if e != nil {
		t.Fatal(e)
	}
	again, e := a.transferPublicKey()
	if e != nil || again != key {
		t.Fatal("key changed", e)
	}
	connection := "native:" + ID()
	if e = a.saveNativeGoogle(connection, grant, time.Now().Add(time.Hour).Unix()); e != nil {
		t.Fatal(e)
	}
	raw, e := os.ReadFile(filepath.Join(a.config.State, "native-drive-grants", strings.TrimPrefix(connection, "native:")+".json"))
	if e != nil {
		t.Fatal(e)
	}
	if bytes.Contains(raw, []byte(grant.Access)) || bytes.Contains(raw, []byte(grant.Refresh)) {
		t.Fatal("plaintext credential persisted")
	}
	if e = a.requireDriveWrite(connection); e != nil {
		t.Fatal("job requires legacy server account", e)
	}
	if _, e = a.google(); e == nil {
		t.Fatal("native job replaced the global account")
	}
	a.removeNativeGoogle(connection)
	if _, _, e = a.nativeGoogle(connection); e == nil {
		t.Fatal("finished grant still readable")
	}
}

func TestNativeDriveJobSubmissionDoesNotPublishCredentials(t *testing.T) {
	s := testNode(t, "Pi")
	if e := os.WriteFile(filepath.Join(s.config.Roots[0].Path, "fixture"), []byte("payload"), 0600); e != nil {
		t.Fatal(e)
	}
	grant := syntheticNativeGoogle()
	envelope, e := s.sealNativeDriveGrant(grant)
	if e != nil {
		t.Fatal(e)
	}
	request := CopyRequest{Items: []Item{{Source: "Pi", Path: "fixture"}}, Destination: Item{Source: "drive", Path: "root"}, DriveAccount: grant.Account, DriveGrant: &envelope}
	call := func(method, path, key string, request any) *httptest.ResponseRecorder {
		var raw []byte
		if request != nil {
			raw, _ = json.Marshal(request)
		}
		r := httptest.NewRequest(method, path, bytes.NewReader(raw))
		r.Header.Set("Authorization", "Bearer "+s.config.Token)
		if key != "" {
			r.Header.Set("Idempotency-Key", key)
		}
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, r)
		return w
	}
	w := call("POST", "/v1/jobs", "native-job-stable-key", request)
	if w.Code != 202 {
		t.Fatal(w.Code, w.Body.String())
	}
	var public Job
	if json.Unmarshal(w.Body.Bytes(), &public) != nil {
		t.Fatal(w.Body.String())
	}
	if public.Connection != "" || public.Request.DriveGrant != nil || bytes.Contains(w.Body.Bytes(), []byte(grant.Refresh)) {
		t.Fatal("credential disclosed", w.Body.String())
	}
	var stored Job
	if e = s.read(public.ID, "copy", &stored); e != nil {
		t.Fatal(e)
	}
	if !strings.HasPrefix(stored.Connection, "native:") || stored.Request.DriveGrant != nil {
		t.Fatal("unscoped job")
	}
	raw, e := s.db.Query("SELECT body FROM fb_operations WHERE id=?", stored.ID)
	if e != nil {
		t.Fatal(e)
	}
	defer raw.Close()
	if !raw.Next() {
		t.Fatal("job missing")
	}
	var body []byte
	if e = raw.Scan(&body); e != nil {
		t.Fatal(e)
	}
	if bytes.Contains(body, []byte(grant.Access)) || bytes.Contains(body, []byte(grant.Refresh)) || bytes.Contains(body, []byte(`"driveGrant"`)) {
		t.Fatal("grant entered operation journal")
	}
	raw.Close()
	second, e := s.sealNativeDriveGrant(grant)
	if e != nil {
		t.Fatal(e)
	}
	request.DriveGrant = &second
	w = call("POST", "/v1/jobs", "native-job-stable-key", request)
	if w.Code != 200 {
		t.Fatal("reencrypted idempotent replay failed", w.Code, w.Body.String())
	}
	request.DriveAccount = "different-account"
	w = call("POST", "/v1/jobs", "native-job-other-key", request)
	if w.Code < 400 {
		t.Fatal("account mismatch accepted")
	}
	w = call("POST", "/v1/jobs/"+stored.ID+"/cancel", "", nil)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	if _, _, e = s.nativeGoogle(stored.Connection); e == nil {
		t.Fatal("cancelled grant retained")
	}
}

func TestNativeDriveRefreshDoesNotUseServerOAuthClient(t *testing.T) {
	s := testNode(t, "Pi")
	grant := syntheticNativeGoogle()
	grant.Expires = 0
	connection := "native:" + ID()
	if e := s.saveNativeGoogle(connection, grant, time.Now().Add(time.Hour).Unix()); e != nil {
		t.Fatal(e)
	}
	previous := googleHTTP
	t.Cleanup(func() { googleHTTP = previous })
	googleHTTP = &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
		if r.URL.String() != "https://oauth2.googleapis.com/token" {
			t.Fatal(r.URL)
		}
		if e := r.ParseForm(); e != nil {
			t.Fatal(e)
		}
		if r.Form.Get("client_id") != grant.ClientID || r.Form.Get("client_secret") != "" || r.Form.Get("refresh_token") != grant.Refresh {
			t.Fatal("wrong OAuth identity")
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"access_token":"renewed-native-access","expires_in":3600}`)), Header: make(http.Header)}, nil
	})}
	updated, e := s.token(context.Background(), connection)
	if e != nil || updated.Access != "renewed-native-access" {
		t.Fatal(updated, e)
	}
	if _, e = os.Stat(filepath.Join(s.config.State, "google.json")); !os.IsNotExist(e) {
		t.Fatal("global OAuth state changed")
	}
	if e = s.saveNativeGoogle(connection, grant, time.Now().Add(-time.Second).Unix()); e != nil {
		t.Fatal(e)
	}
	s.cleanupExpiredNativeGoogle()
	if _, _, e = s.nativeGoogle(connection); e == nil {
		t.Fatal("expired credential survived cleanup")
	}
}

func TestIndependentPeersCanHaveTheSameRootName(t *testing.T) {
	a, b := testNode(t, "A"), testNode(t, "B")
	a.config.Roots[0].Name = "Files"
	b.config.Roots[0].Name = "Files"
	as := httptest.NewTLSServer(a.Handler())
	defer as.Close()
	bs := httptest.NewTLSServer(b.Handler())
	defer bs.Close()
	a.config.Peers = []PeerConfig{testPeer(bs, "B", "Files", b.config.Token)}
	b.config.Peers = []PeerConfig{testPeer(as, "A", "Files", a.config.Token)}
	if e := a.config.validateNode(); e != nil {
		t.Fatal("peer root collision", e)
	}
	if a.peerFor("Files") != nil || a.peerFor("peer:B:Files") == nil {
		t.Fatal("local root shadowed or explicit peer missing")
	}
	if e := os.WriteFile(filepath.Join(a.config.Roots[0].Path, "source"), []byte("fixture"), 0600); e != nil {
		t.Fatal(e)
	}
	request := CopyRequest{Items: []Item{{Source: "Files", Path: "source"}}, Destination: Item{Source: "Files", Path: ".", ServerID: "B"}}
	raw, _ := json.Marshal(request)
	r := httptest.NewRequest("POST", "/v1/jobs", bytes.NewReader(raw))
	r.Header.Set("Authorization", "Bearer "+a.config.Token)
	r.Header.Set("Idempotency-Key", "same-root-explicit-peer-copy")
	w := httptest.NewRecorder()
	a.Handler().ServeHTTP(w, r)
	if w.Code != 202 {
		t.Fatal(w.Code, w.Body.String())
	}
	var submitted Job
	json.Unmarshal(w.Body.Bytes(), &submitted)
	var job Job
	if e := b.read(strings.TrimPrefix(submitted.ID, "B~"), "copy", &job); e != nil {
		t.Fatal(e)
	}
	if len(job.PeerPlan) != 1 || job.PeerPlan[0].Item.Source != "peer:A:Files" {
		t.Fatal("source owner lost", job.PeerPlan)
	}
	b.runJob(context.Background(), &job)
	if job.State != "completed" {
		t.Fatal(job.State, job.Error)
	}
	data, e := os.ReadFile(filepath.Join(b.config.Roots[0].Path, "source"))
	if e != nil || string(data) != "fixture" {
		t.Fatal(string(data), e)
	}
}
