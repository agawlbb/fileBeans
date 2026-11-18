package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"filebeans/companion/protection"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func testBroker(t *testing.T) *mountBroker {
	t.Helper()
	base := t.TempDir()
	root := filepath.Join(base, "storage")
	if e := os.Mkdir(root, 0700); e != nil {
		t.Fatal(e)
	}
	var lockedCheck func(brokerEntry) bool
	if os.Getenv("FILEBEANS_TEST_FUSE") != "1" {
		lockedCheck = func(brokerEntry) bool { return true }
	}
	b, e := newMountBrokerWithChecks(BrokerConfig{State: filepath.Join(base, "state"), Runtime: filepath.Join(base, "runtime"),
		Roots: []Root{{Name: "disk", Path: root}}, Gocryptfs: "/usr/bin/gocryptfs", Fusermount: "/usr/bin/fusermount3",
		ControlUID: uint32(os.Getuid()), ReaderUID: uint32(os.Getuid() + 1), GID: os.Getgid()}, lockedCheck)
	if e != nil {
		t.Fatal(e)
	}
	if os.Getenv("FILEBEANS_TEST_FUSE") != "1" {
		b.lockedCheck = func(brokerEntry) bool { return true }
	}
	t.Cleanup(func() {
		b.close()
		for _, entry := range b.registry.Entries {
			if mounted(b.publicPath(entry), "filebeans-locked-"+entry.ID) {
				_ = b.unmountFiles(b.publicPath(entry))
			}
		}
	})
	return b
}

func TestBrokerReservationIsDurableAndIdempotent(t *testing.T) {
	b := testBroker(t)
	entry, e := b.reserve("disk", "/", "Private", "synthetic-request-one")
	if e != nil {
		t.Fatal(e)
	}
	retry, e := b.reserve("disk", "/", "Private", "synthetic-request-one")
	if e != nil || retry != entry {
		t.Fatal("retry changed reservation", e)
	}
	if _, e = b.reserve("disk", "/", "Other", "synthetic-request-one"); !errors.Is(e, ErrConflict) {
		t.Fatal("idempotency accepted changed request", e)
	}
	if e = b.publish(); e != nil {
		t.Fatal(e)
	}
	data, e := os.ReadFile(filepath.Join(b.config.State, "registry.json"))
	if e != nil {
		t.Fatal(e)
	}
	if bytes.Contains(data, []byte("synthetic-request-one")) {
		t.Fatal("raw request ID persisted")
	}
	b.close()
	reopened, e := newMountBrokerWithChecks(b.config, b.lockedCheck)
	if e != nil {
		t.Fatal(e)
	}
	defer reopened.close()
	retry, e = reopened.reserve("disk", "/", "Private", "synthetic-request-one")
	if e != nil || retry != entry {
		t.Fatal("restart lost reservation", e)
	}
	status := reopened.statuses()
	if len(status) != 1 || status[0].State != "locked" || status[0].Path != "/Private" {
		t.Fatalf("bad status: %+v", status)
	}
}

func TestBrokerRefusesExistingDataAndOverlappingLocations(t *testing.T) {
	b := testBroker(t)
	r := b.config.Roots[0].Path
	if e := os.Mkdir(filepath.Join(r, "Existing"), 0700); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(filepath.Join(r, "Existing", "keep"), []byte("unchanged"), 0600); e != nil {
		t.Fatal(e)
	}
	if _, e := b.reserve("disk", "/", "Existing", "synthetic-request-one"); !errors.Is(e, ErrConflict) {
		t.Fatal(e)
	}
	if _, e := b.reserve("disk", "/", "Private", "synthetic-request-two"); e != nil {
		t.Fatal(e)
	}
	for _, tc := range []struct{ source, parent, name string }{
		{"disk", "/Private", "Nested"}, {"disk", "/", ".filebeans-vaults"}, {"disk", "/", "Trash"},
		{"disk", "/../", "Escape"}, {"unknown", "/", "New"}, {"disk", "/", "../Escape"},
	} {
		if _, e := b.reserve(tc.source, tc.parent, tc.name, "synthetic-another-request"); e == nil {
			t.Fatalf("accepted %+v", tc)
		}
	}
	data, e := os.ReadFile(filepath.Join(r, "Existing", "keep"))
	if e != nil || string(data) != "unchanged" {
		t.Fatal("existing data changed", e)
	}
}

func TestBrokerRejectsSymlinksAndReplacedPlaceholders(t *testing.T) {
	b := testBroker(t)
	r := b.config.Roots[0].Path
	out := t.TempDir()
	if e := os.Symlink(out, filepath.Join(r, "link")); e != nil {
		t.Fatal(e)
	}
	if _, e := b.reserve("disk", "/link", "Escaped", "synthetic-request-one"); e == nil {
		t.Fatal("followed parent symlink")
	}
	if _, e := os.Stat(filepath.Join(out, "Escaped")); !errors.Is(e, os.ErrNotExist) {
		t.Fatal("wrote outside root", e)
	}
	entry, e := b.reserve("disk", "/", "Private", "synthetic-request-two")
	if e != nil {
		t.Fatal(e)
	}
	if e = os.Rename(filepath.Join(r, "Private"), filepath.Join(r, "Old")); e != nil {
		t.Fatal(e)
	}
	if e = os.Mkdir(filepath.Join(r, "Private"), 0500); e != nil {
		t.Fatal(e)
	}
	if f, e := b.publicDir(entry); e == nil {
		f.Close()
		t.Fatal("accepted replacement directory")
	}
}

func TestBrokerTransitionRefusesLiveReaders(t *testing.T) {
	b := testBroker(t)
	reader, e := os.Open(filepath.Join(b.config.Runtime, "gate"))
	if e != nil {
		t.Fatal(e)
	}
	defer reader.Close()
	if e = unix.Flock(int(reader.Fd()), unix.LOCK_SH); e != nil {
		t.Fatal(e)
	}
	if f, e := b.exclusive(); !errors.Is(e, ErrVaultBusy) {
		if f != nil {
			f.Close()
		}
		t.Fatal("transition ignored active reader", e)
	}
	reader.Close()
	f, e := b.exclusive()
	if e != nil {
		t.Fatal("drained readers still block transition", e)
	}
	f.Close()
}

func brokerRequest(b *mountBroker, uid *uint32, method, path, payload string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(payload))
	if uid != nil {
		r = r.WithContext(context.WithValue(r.Context(), brokerPeer{}, *uid))
	}
	w := httptest.NewRecorder()
	b.handler().ServeHTTP(w, r)
	return w
}

func TestBrokerSocketRolesAndReadiness(t *testing.T) {
	b := testBroker(t)
	control, reader, stranger := b.config.ControlUID, b.config.ReaderUID, uint32(os.Getuid()+2)
	if w := brokerRequest(b, &reader, "GET", "/v1/metadata-key", ""); w.Code != 403 {
		t.Fatal("reader received metadata key", w.Code)
	}
	if w := brokerRequest(b, &control, "GET", "/v1/metadata-key", ""); w.Code != 409 {
		t.Fatal("key available with no open vault", w.Code)
	}
	for _, uid := range []*uint32{nil, &stranger} {
		if w := brokerRequest(b, uid, "GET", "/v1/vaults", ""); w.Code != 403 {
			t.Fatal("unauthorized peer accepted", w.Code)
		}
	}
	if w := brokerRequest(b, &reader, "POST", "/v1/vaults", "{}"); w.Code != 403 {
		t.Fatal("reader may create", w.Code)
	}
	if w := brokerRequest(b, &control, "POST", "/v1/consumer/ready", `{"version":1}`); w.Code != 403 {
		t.Fatal("control may claim reader readiness", w.Code)
	}
	if w := brokerRequest(b, &reader, "POST", "/v1/consumer/ready", `{"version":2}`); w.Code != 400 {
		t.Fatal("unsupported protocol accepted", w.Code)
	}
	if w := brokerRequest(b, &reader, "POST", "/v1/consumer/ready", `{"version":1}`); w.Code != 400 {
		t.Fatal("incomplete adapter enabled creation", w.Code)
	}
	readyBody, _ := json.Marshal(map[string]any{"version": 1, "features": protection.ConsumerFeatures})
	if w := brokerRequest(b, &reader, "POST", "/v1/consumer/ready", string(readyBody)); w.Code != 200 {
		t.Fatal(w.Code)
	}
	if time.Since(b.consumerReady) > time.Second {
		t.Fatal("readiness not recorded")
	}
	w := brokerRequest(b, &control, "GET", "/v1/ready", "")
	var status struct {
		Ready bool `json:"ready"`
	}
	if e := json.Unmarshal(w.Body.Bytes(), &status); e != nil || !status.Ready {
		t.Fatal("ready consumer missing", e)
	}
	b.consumerReady = time.Now().Add(-time.Minute)
	w = brokerRequest(b, &control, "GET", "/v1/ready", "")
	if e := json.Unmarshal(w.Body.Bytes(), &status); e != nil || status.Ready {
		t.Fatal("expired consumer accepted", e)
	}
	if w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("status cacheable")
	}
}

func TestBrokerRejectsWrongKeyBeforeReserving(t *testing.T) {
	b := testBroker(t)
	key := bytes.Repeat([]byte{0x42}, 32)
	if e := os.WriteFile(b.manager.keyFile, []byte(strings.Repeat("0", 64)), 0600); e != nil {
		t.Fatal(e)
	}
	payload, _ := json.Marshal(map[string]string{"source": "disk", "parent": "/", "name": "Private", "key": recoveryCode(key)})
	control := b.config.ControlUID
	w := brokerRequest(b, &control, http.MethodPost, "/v1/vaults", string(payload))
	if w.Code != 409 {
		t.Fatal("wrong key accepted", w.Code)
	}
	if len(b.registry.Entries) != 0 {
		t.Fatal("wrong key reserved a folder")
	}
	if strings.Contains(w.Body.String(), recoveryCode(key)) {
		t.Fatal("key leaked in error")
	}
}

func TestBrokerRealFilesystemLifecycle(t *testing.T) {
	if os.Getenv("FILEBEANS_TEST_FUSE") != "1" {
		t.Skip("isolated synthetic FUSE runtime required")
	}
	b := testBroker(t)
	b.config.Gocryptfs = os.Getenv("FILEBEANS_TEST_GOCRYPTFS")
	var e error
	b.config.Fusermount, e = exec.LookPath("fusermount3")
	if e != nil {
		t.Fatal(e)
	}
	// Only the isolated test runtime accepts synthetic keys while the VM host
	// has swap. There is no configuration or environment bypass in the daemon.
	b.manager.memoryCheck = func() error { return nil }
	var processLog bytes.Buffer
	b.manager.processLog = &processLog
	key := bytes.Repeat([]byte{0x42}, 32)
	entry, e := b.reserve("disk", "/", "Private", "synthetic-request-one")
	if e != nil {
		t.Fatal(e)
	}
	if e = b.open(entry, key); e != nil {
		t.Fatal("create/unlock", e, processLog.String())
	}
	public := b.publicPath(entry)
	if e = os.WriteFile(filepath.Join(public, "secret-name.txt"), []byte("secret-content"), 0600); e != nil {
		t.Fatal(e)
	}
	private := filepath.Join(b.vaultConfig(entry).Mount, "private", "cache", "thumbnail")
	if e = os.WriteFile(private, []byte("secret-thumbnail"), 0600); e != nil {
		t.Fatal(e)
	}
	// Exercise the unprivileged companion adapter against the actual mount and
	// socket protocol. The synthetic server supplies its fixed test peer UID.
	listener, e := net.Listen("unix", filepath.Join(b.config.Runtime, "broker.sock"))
	if e != nil {
		t.Fatal(e)
	}
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b.handler().ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), brokerPeer{}, b.config.ControlUID)))
	})}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { _ = server.Close() })
	guard, e := protection.New(b.config.Runtime, map[string]string{"disk": b.config.Roots[0].Path})
	if e != nil {
		t.Fatal(e)
	}
	s := &Service{config: Config{Roots: b.config.Roots}, vaults: &vaultManager{remote: newBrokerClient(b.config.Runtime, b.config.Roots), guard: guard}}
	alias := filepath.Join(b.config.Roots[0].Path, "Alternate")
	if e = os.Mkdir(alias, 0500); e != nil {
		t.Fatal(e)
	}
	if e = b.mountFiles(public, alias); e != nil {
		t.Fatal(e)
	}
	lease, e := guard.Acquire()
	if e != nil {
		_ = b.unmountFiles(alias)
		t.Fatal(e)
	}
	_, aliasErr := lease.CacheDirectory(filepath.Join(alias, "secret-name.txt"))
	lease.Close()
	if e = b.unmountFiles(alias); e != nil {
		t.Fatal(e)
	}
	if !errors.Is(aliasErr, protection.ErrBoundary) {
		t.Fatal("alternate mount selected an ordinary cache", aliasErr)
	}
	t.Cleanup(s.vaults.remote.close)
	root, e := s.root("disk")
	if e != nil {
		t.Fatal(e)
	}
	if _, e = root.Open(".filebeans-vaults"); !errors.Is(e, ErrUnsafe) {
		root.Close()
		t.Fatal("ciphertext store exposed", e)
	}
	if e = renameExclusive(root, "Private", "Renamed"); !errors.Is(e, ErrUnsafe) {
		root.Close()
		t.Fatal("mountpoint renamed", e)
	}
	job := &Job{ID: "synthetic", Request: CopyRequest{Destination: Item{Source: "disk", Path: "/Private"}}}
	if stage := s.stagePath(job); stage != "Private/.filebeans-stage/synthetic" {
		root.Close()
		t.Fatal("plaintext staging selected", stage)
	}
	if gate, e := b.exclusive(); !errors.Is(e, ErrVaultBusy) {
		if gate != nil {
			gate.Close()
		}
		root.Close()
		t.Fatal("companion root did not hold lease", e)
	}
	root.Close()
	if labels, e := s.recordSources([]byte(`{"source":"disk","path":"/Normal/file.txt"}`)); e != nil || len(labels) != 0 {
		t.Fatal("ordinary history unnecessarily protected", labels, e)
	}
	record := []byte(`{"source":"disk","path":"/Private/secret-name.txt"}`)
	sealed, e := s.encodeRecord("synthetic-record", "copy", record)
	if e != nil || bytes.Contains(sealed, []byte("secret-name")) {
		t.Fatal("protected history not sealed", e)
	}
	plain, e := s.decodeRecord("synthetic-record", "copy", sealed)
	if e != nil || !bytes.Equal(plain, record) {
		t.Fatal("history cannot be decoded while open", e)
	}
	if status := b.statuses(); len(status) != 1 || status[0].State != "unlocked" {
		t.Fatalf("not unlocked: %+v", status)
	}
	held, e := os.Open(filepath.Join(public, "secret-name.txt"))
	if e != nil {
		t.Fatal(e)
	}
	if e = b.lock(entry); !errors.Is(e, ErrVaultBusy) {
		held.Close()
		t.Fatal("lock succeeded with an open file", e)
	}
	held.Close()
	if e = b.lock(entry); e != nil {
		t.Fatal("lock after reader closed", e)
	}
	if _, e = s.decodeRecord("synthetic-record", "copy", sealed); !errors.Is(e, ErrVaultLocked) {
		t.Fatal("locked history readable", e)
	}
	root, e = s.root("disk")
	if e != nil {
		t.Fatal(e)
	}
	if _, e = root.Open("Private/secret-name.txt"); !errors.Is(e, ErrVaultLocked) {
		root.Close()
		t.Fatal("locked path opened", e)
	}
	root.Close()
	if files, e := os.ReadDir(public); e != nil || len(files) != 0 {
		t.Fatal("locked folder exposes contents", e)
	}
	if e = os.WriteFile(filepath.Join(public, "plaintext-fallback"), []byte("must not be written"), 0600); e == nil {
		t.Fatal("locked mount accepted plaintext fallback writes")
	}
	if _, e = os.ReadFile(private); !errors.Is(e, os.ErrNotExist) {
		t.Fatal("private cache survives outside mount", e)
	}
	if b.manager.metadataKey != nil {
		t.Fatal("last lock retained metadata key")
	}
	if e = filepath.Walk(b.config.Roots[0].Path, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if strings.Contains(info.Name(), "secret-name") {
			t.Fatal("plaintext filename on storage")
		}
		if !info.IsDir() {
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			if bytes.Contains(data, []byte("secret-content")) || bytes.Contains(data, []byte("secret-thumbnail")) {
				t.Fatal("plaintext on storage")
			}
		}
		return nil
	}); e != nil {
		t.Fatal(e)
	}
	if e = b.open(entry, key); e != nil {
		t.Fatal("reopen", e)
	}
	if data, e := os.ReadFile(filepath.Join(public, "secret-name.txt")); e != nil || string(data) != "secret-content" {
		t.Fatal("content lost", e)
	}
	if data, e := os.ReadFile(private); e != nil || string(data) != "secret-thumbnail" {
		t.Fatal("private cache lost", e)
	}
	readyBody, _ := json.Marshal(map[string]any{"version": brokerProtocol, "features": protection.ConsumerFeatures})
	reader := b.config.ReaderUID
	if w := brokerRequest(b, &reader, "POST", "/v1/consumer/ready", string(readyBody)); w.Code != 200 {
		t.Fatal("consumer registration", w.Code)
	}
	created, e := s.vaults.remote.create("disk", "/", "Another", key, "synthetic-request-two")
	if e != nil || created.State != "unlocked" || created.Path != "/Another" {
		t.Fatal("create API", created, e)
	}
	retried, e := s.vaults.remote.create("disk", "/", "Another", key, "synthetic-request-two")
	if e != nil || retried.ID != created.ID {
		t.Fatal("creation retry lost identity", e)
	}
	if _, e = s.vaults.remote.create("disk", "/", "WrongKey", bytes.Repeat([]byte{0x43}, 32), "synthetic-wrong-key"); !errors.Is(e, ErrVaultKey) {
		t.Fatal("wrong creation key accepted", e)
	}
	if _, e = os.Stat(filepath.Join(b.config.Roots[0].Path, "WrongKey")); !errors.Is(e, os.ErrNotExist) {
		t.Fatal("wrong key created a placeholder", e)
	}
	if e = b.lock(entry); e != nil {
		t.Fatal(e)
	}
	if b.manager.metadataKey == nil {
		t.Fatal("locking first folder erased active metadata key")
	}
	if _, e = s.decodeRecord("synthetic-record", "copy", sealed); !errors.Is(e, ErrVaultLocked) {
		t.Fatal("another open vault exposed locked history", e)
	}
	b.config.Roots[0].Mount = "/synthetic-offline-mount"
	b.config.Roots[0].MountSource = "//synthetic/offline"
	if e = s.vaults.remote.action(created.ID, "lock", nil); e != nil {
		t.Fatal(e)
	}
	b.config.Roots[0].Mount = ""
	b.config.Roots[0].MountSource = ""
	if b.manager.metadataKey != nil {
		t.Fatal("last folder retained key")
	}
}
