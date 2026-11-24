package service

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestProtectionWaitDoesNotBlockBrokerReaders(t *testing.T) {
	b := testBroker(t)
	held, err := os.Open(filepath.Join(b.config.Runtime, "gate"))
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()
	if err = unix.Flock(int(held.Fd()), unix.LOCK_SH); err != nil {
		t.Fatal(err)
	}
	result := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		r := httptest.NewRequest("POST", "/v1/protection/lock", nil)
		r = r.WithContext(context.WithValue(r.Context(), brokerPeer{}, b.config.ControlUID))
		w := httptest.NewRecorder()
		b.handler().ServeHTTP(w, r)
		result <- w
	}()
	time.Sleep(50 * time.Millisecond)
	select {
	case <-result:
		t.Fatal("transition ignored active lease")
	default:
	}
	ready := make(chan struct{})
	go func() {
		r := httptest.NewRequest("GET", "/v1/ready", nil)
		r = r.WithContext(context.WithValue(r.Context(), brokerPeer{}, b.config.ReaderUID))
		b.handler().ServeHTTP(httptest.NewRecorder(), r)
		close(ready)
	}()
	select {
	case <-ready:
	case <-time.After(time.Second):
		t.Fatal("wait holds broker mutex")
	}
	held.Close()
	select {
	case w := <-result:
		if w.Code != 200 {
			t.Fatal(w.Code, w.Body.String())
		}
		if w.Body.String() != "{\"state\":\"locked\"}\n" {
			t.Fatal("unexpected aggregate response", w.Body.String())
		}
	case <-time.After(time.Second):
		t.Fatal("lock did not resume after lease release")
	}
}

// Creation competes with folder enumeration and thumbnail requests for the
// same gate. A short read must delay creation, not reject it immediately.
func TestCreationWaitDoesNotBlockBrokerReaders(t *testing.T) {
	b := testBroker(t)
	b.consumerReady = time.Now()
	held, err := os.Open(filepath.Join(b.config.Runtime, "gate"))
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()
	if err = unix.Flock(int(held.Fd()), unix.LOCK_SH); err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(map[string]string{"source": "missing", "parent": "/", "name": "New", "key": recoveryCode(bytes.Repeat([]byte{0x42}, 32))})
	if err != nil {
		t.Fatal(err)
	}
	result := make(chan *httptest.ResponseRecorder, 1)
	go func() { result <- brokerRequest(b, &b.config.ControlUID, "POST", "/v1/vaults", string(payload)) }()
	time.Sleep(75 * time.Millisecond)
	select {
	case w := <-result:
		t.Fatalf("creation rejected a temporary reader: HTTP %d %s", w.Code, w.Body.String())
	default:
	}
	ready := make(chan *httptest.ResponseRecorder, 1)
	go func() { ready <- brokerRequest(b, &b.config.ReaderUID, "GET", "/v1/ready", "") }()
	select {
	case w := <-ready:
		if w.Code != 200 {
			t.Fatal(w.Code, w.Body.String())
		}
	case <-time.After(time.Second):
		t.Fatal("creation wait held broker mutex")
	}
	held.Close()
	select {
	case w := <-result:
		// An unknown source intentionally stops before any mounts or user data
		// are created, after proving the real HTTP creation handler resumed.
		if w.Code != 400 {
			t.Fatalf("creation did not reach source validation: %d %s", w.Code, w.Body.String())
		}
	case <-time.After(time.Second):
		t.Fatal("creation did not resume after the reader finished")
	}
	if len(b.registry.Entries) != 0 {
		t.Fatal("invalid source created a reservation")
	}
}

func TestCreationWaitHonorsCancellation(t *testing.T) {
	b := testBroker(t)
	b.consumerReady = time.Now()
	held, err := os.Open(filepath.Join(b.config.Runtime, "gate"))
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()
	if err = unix.Flock(int(held.Fd()), unix.LOCK_SH); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	payload, _ := json.Marshal(map[string]string{"source": "disk", "parent": "/", "name": "New", "key": recoveryCode(bytes.Repeat([]byte{0x42}, 32))})
	request := httptest.NewRequest("POST", "/v1/vaults", bytes.NewReader(payload))
	request = request.WithContext(context.WithValue(ctx, brokerPeer{}, b.config.ControlUID))
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() { w := httptest.NewRecorder(); b.handler().ServeHTTP(w, request); done <- w }()
	time.Sleep(75 * time.Millisecond)
	select {
	case w := <-done:
		t.Fatalf("creation returned before cancellation: HTTP %d", w.Code)
	default:
	}
	cancel()
	select {
	case w := <-done:
		if w.Code < 400 {
			t.Fatal("cancelled creation succeeded")
		}
	case <-time.After(time.Second):
		t.Fatal("cancelled creation did not stop")
	}
	if len(b.registry.Entries) != 0 {
		t.Fatal("cancelled creation created a reservation")
	}
}

func TestProtectionWaitHonorsCancellation(t *testing.T) {
	b := testBroker(t)
	held, err := os.Open(filepath.Join(b.config.Runtime, "gate"))
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()
	if err = unix.Flock(int(held.Fd()), unix.LOCK_SH); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if gate, err := b.exclusiveContext(ctx); err == nil {
		gate.Close()
		t.Fatal("canceled transition acquired gate")
	}
}

func TestBrokerEncryptedLocations(t *testing.T)    { testBrokerEncryptedLocations(t, false) }
func TestBrokerEncryptedSMBLocations(t *testing.T) { testBrokerEncryptedLocations(t, true) }
func testBrokerEncryptedLocations(t *testing.T, smb bool) {
	if os.Getenv("FILEBEANS_TEST_FUSE") != "1" {
		t.Skip("isolated FUSE runtime required")
	}
	b := testBroker(t)
	if smb {
		mountSyntheticSMB(t, b)
	}
	b.config.Gocryptfs = os.Getenv("FILEBEANS_TEST_GOCRYPTFS")
	b.config.Fusermount = "/test-tools/fusermount3"
	b.manager.memoryCheck = func() error { return nil }
	key := bytes.Repeat([]byte{0x42}, 32)
	name := "Confidential-folder-name"
	entry, err := b.reserve("disk", "/", name, "synthetic-encrypted-locations")
	if err != nil {
		t.Fatal(err)
	}
	if err = b.open(entry, key); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(b.config.Roots[0].Path, name)
	if err = os.WriteFile(filepath.Join(path, "payload"), []byte("recoverable content"), 0660); err != nil {
		t.Fatal(err)
	}
	metadata, err := os.ReadFile(filepath.Join(b.vaultConfig(entry).Mount, "private", "location.json"))
	if err != nil || !bytes.Contains(metadata, []byte(name)) {
		t.Fatal("independent encrypted location recovery copy missing", err)
	}
	registry, err := os.ReadFile(filepath.Join(b.config.State, "registry.json"))
	if err != nil || bytes.Contains(registry, []byte(name)) {
		t.Fatal("registry stores plaintext while unlocked", err)
	}
	if err = b.lockAll(); err != nil {
		t.Fatal(err)
	}
	if len(b.locationKey) != 0 || len(b.manager.metadataKey) != 0 {
		t.Fatal("locked session retained keys")
	}
	for _, file := range []string{filepath.Join(b.config.State, "registry.json"), filepath.Join(b.config.Runtime, "status.json")} {
		data, err := os.ReadFile(file)
		if err != nil || bytes.Contains(data, []byte(name)) {
			t.Fatal("locked metadata reveals name", file, err)
		}
	}
	if _, err = os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("named mountpoint survives locking", err)
	}
	statuses, _ := json.Marshal(b.statuses())
	if bytes.Contains(statuses, []byte(name)) {
		t.Fatal("locked API reveals names")
	}
	b.close()
	restarted, err := newMountBroker(b.config)
	if err != nil {
		t.Fatal("restart", err)
	}
	t.Cleanup(func() { restarted.close() })
	restarted.manager.memoryCheck = func() error { return nil }
	restarted.consumerReady = time.Now()
	if restarted.protectionStatus().State != "locked" {
		t.Fatal("restart did not start locked")
	}
	if err = restarted.unlockAll(key); err != nil {
		t.Fatal("unlock after restart", err)
	}
	data, err := os.ReadFile(filepath.Join(path, "payload"))
	if err != nil || string(data) != "recoverable content" {
		t.Fatal("data after restart", err)
	}
	if restarted.protectionStatus().State != "unlocked" {
		t.Fatal("global unlock incomplete")
	}
	if err = restarted.lockAll(); err != nil {
		t.Fatal(err)
	}
}

func TestLocationCipherRejectsTamperedNonceWithoutPanicking(t *testing.T) {
	b := testBroker(t)
	_, err := b.reserve("disk", "/", "Sensitive-name", "synthetic-location-tamper")
	if err != nil {
		t.Fatal(err)
	}
	// Unit fixtures run without the daemon's root capabilities.
	if err = os.Chmod(filepath.Join(b.config.Roots[0].Path, "Sensitive-name"), 0700); err != nil {
		t.Fatal(err)
	}
	key := bytes.Repeat([]byte{0x42}, 32)
	if err = b.activateLocations(key); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(b.config.State, "registry.json"))
	if err != nil || bytes.Contains(data, []byte("Sensitive-name")) {
		t.Fatal("location was not encrypted", err)
	}
	b.forgetClosedLocations()
	b.registry.Entries[0].Location.Nonce = []byte{1}
	if err = b.activateLocations(key); err == nil {
		t.Fatal("malformed nonce accepted")
	}
	if len(b.locationKey) != 0 {
		t.Fatal("failed authentication retained a location key")
	}
}

func TestBrokerGlobalInactivity(t *testing.T) {
	if os.Getenv("FILEBEANS_TEST_FUSE") != "1" {
		t.Skip("isolated FUSE runtime required")
	}
	b := testBroker(t)
	b.config.Gocryptfs = os.Getenv("FILEBEANS_TEST_GOCRYPTFS")
	b.config.Fusermount = "/test-tools/fusermount3"
	b.manager.memoryCheck = func() error { return nil }
	key := bytes.Repeat([]byte{0x42}, 32)
	for _, name := range []string{"First", "Second"} {
		e, err := b.reserve("disk", "/", name, "synthetic-global-idle-"+name)
		if err != nil {
			t.Fatal(err)
		}
		if err = b.open(e, key); err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(filepath.Join(b.config.Roots[0].Path, name, "data"), []byte("payload"), 0660); err != nil {
			t.Fatal(err)
		}
	}
	watches := map[string]*vaultIdleWatch{}
	defer func() {
		for _, w := range watches {
			w.close()
		}
	}()
	start := time.Unix(10000, 0)
	b.pollIdleVaults(watches, start)
	if _, err := os.ReadFile(filepath.Join(b.config.Roots[0].Path, "Second", "data")); err != nil {
		t.Fatal(err)
	}
	b.pollIdleVaults(watches, start.Add(59*time.Minute))
	b.pollIdleVaults(watches, start.Add(time.Hour))
	if b.protectionStatus().State != "unlocked" {
		t.Fatal("activity did not keep the entire session open")
	}
	b.pollIdleVaults(watches, start.Add(119*time.Minute))
	if b.protectionStatus().State != "locked" {
		t.Fatal("idle timeout did not lock the entire session")
	}
	if len(b.locationKey) != 0 {
		t.Fatal("global idle lock retained location key")
	}
}
