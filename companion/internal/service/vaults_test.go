package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func recoveryCode(key []byte) string {
	prefix := "FBK1:" + hex.EncodeToString(key)
	sum := sha256.Sum256([]byte(prefix))
	return prefix + ":" + hex.EncodeToString(sum[:4])
}
func TestVaultRecoveryCode(t *testing.T) {
	key := bytes.Repeat([]byte{0x42}, 32)
	code := recoveryCode(key)
	decoded, e := parseVaultKey(" \n" + code + "\n")
	if e != nil || !bytes.Equal(decoded, key) {
		t.Fatal("portable master key did not round-trip", e)
	}
	for _, bad := range []string{"", hex.EncodeToString(key), code + "x", strings.Replace(code, "FBK1", "FBK2", 1), code[:len(code)-1] + "z", strings.Replace(code, "4242", "42ff", 1)} {
		if _, e := parseVaultKey(bad); e == nil {
			t.Fatalf("accepted invalid code %q", bad)
		}
	}
}
func testVaultService() *Service {
	m := &vaultManager{metadataKey: bytes.Repeat([]byte{7}, 32), mountCheck: func(VaultConfig) bool { return true }, states: []*vaultState{
		{config: VaultConfig{ID: "archive", Source: "Protected", Mount: "/not-a-real-vault", StopClient: "/bin/true"}, unlocked: true},
		{config: VaultConfig{ID: "other", Source: "Other", Mount: "/not-another-real-vault", StopClient: "/bin/true"}, unlocked: true},
	}}
	return &Service{vaults: m}
}
func TestProtectedRecordsAreAuthenticatedAndUnavailableWhenLocked(t *testing.T) {
	s := testVaultService()
	plain := []byte(`{"request":{"items":[{"source":"Protected","path":"private/family.jpg"}],"destination":{"source":"public","path":"exports"}},"error":"private/family.jpg failed"}`)
	sealed, e := s.encodeRecord("job1", "copy", plain)
	if e != nil {
		t.Fatal(e)
	}
	if bytes.Contains(sealed, []byte("family")) || bytes.Contains(sealed, []byte("exports")) {
		t.Fatal("plaintext metadata leaked")
	}
	decoded, e := s.decodeRecord("job1", "copy", sealed)
	if e != nil || !bytes.Equal(decoded, plain) {
		t.Fatal("round trip failed", e)
	}
	second, _ := s.encodeRecord("job1", "copy", plain)
	if bytes.Equal(sealed, second) {
		t.Fatal("nonce reused")
	}
	for _, pair := range [][2]string{{"job2", "copy"}, {"job1", "trash"}} {
		if _, e = s.decodeRecord(pair[0], pair[1], sealed); e == nil {
			t.Fatal("record substitution accepted")
		}
	}
	var envelope sealedRecord
	json.Unmarshal(sealed, &envelope)
	envelope.Ciphertext[0] ^= 1
	corrupt, _ := json.Marshal(envelope)
	if _, e = s.decodeRecord("job1", "copy", corrupt); e == nil {
		t.Fatal("tampered ciphertext accepted")
	}
	envelope.Nonce = nil
	corrupt, _ = json.Marshal(envelope)
	if _, e = s.decodeRecord("job1", "copy", corrupt); e == nil {
		t.Fatal("invalid nonce accepted")
	}
	s.vaults.states[0].unlocked = false
	if _, e = s.decodeRecord("job1", "copy", sealed); !errors.Is(e, ErrVaultLocked) {
		t.Fatal("locked vault disclosed metadata", e)
	}
	if _, e = s.encodeRecord("job1", "copy", plain); !errors.Is(e, ErrVaultLocked) {
		t.Fatal("locked record could be written", e)
	}
	if _, e = s.decodeRecord("job1", "copy", plain); !errors.Is(e, ErrVaultLocked) {
		t.Fatal("legacy plaintext record disclosed", e)
	}
}
func TestOrdinaryRecordsRemainCompatible(t *testing.T) {
	for _, s := range []*Service{{}, testVaultService()} {
		plain := []byte(`{"source":"ordinary","path":"photo.jpg"}`)
		encoded, e := s.encodeRecord("id", "trash", plain)
		if e != nil || !bytes.Equal(encoded, plain) {
			t.Fatal(e)
		}
		decoded, e := s.decodeRecord("id", "trash", encoded)
		if e != nil || !bytes.Equal(decoded, plain) {
			t.Fatal(e)
		}
	}
}
func TestVaultLockKeyLifetimeAndFailure(t *testing.T) {
	s := testVaultService()
	m := s.vaults
	if e := m.lock("archive"); e != nil {
		t.Fatal(e)
	}
	// In this synthetic test there are no real mounts or child processes.
	if m.metadataKey != nil {
		t.Fatal("key retained after last mount disappeared")
	}
	m.metadataKey = bytes.Repeat([]byte{7}, 32)
	m.states[1].config.StopClient = "/bin/false"
	if e := m.lock("other"); e == nil {
		t.Fatal("failed client stop reported successful lock")
	}
	if !m.states[1].unlocked || !m.states[1].fault || m.metadataKey == nil {
		t.Fatal("failed lock discarded live state")
	}
	if e := m.checkRoot("Other"); !errors.Is(e, ErrVaultLocked) {
		t.Fatal("faulted root remained available")
	}
	if e := m.lock("missing"); !errors.Is(e, ErrUnsafe) {
		t.Fatal(e)
	}
}
func TestVaultRootFailsClosedWithoutMount(t *testing.T) {
	s := testVaultService()
	s.vaults.mountCheck = func(VaultConfig) bool { return false }
	if e := s.vaults.checkRoot("Protected"); !errors.Is(e, ErrVaultLocked) {
		t.Fatal(e)
	}
	if e := s.vaults.checkRoot("ordinary"); e != nil {
		t.Fatal(e)
	}
}
func TestVaultRoutesAuthenticateAndRejectSecretsInURLs(t *testing.T) {
	s := &Service{config: Config{Token: strings.Repeat("t", 32)}}
	for _, method := range []string{"GET", "POST"} {
		r := httptest.NewRequest(method, "/v1/vaults/archive/unlock?key=invalid", nil)
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, r)
		if w.Code != 401 || strings.Contains(w.Body.String(), "invalid") {
			t.Fatal("unauthenticated vault access", w.Code)
		}
	}
	r := httptest.NewRequest("GET", "/v1/vaults", nil)
	r.Header.Set("Authorization", "Bearer "+s.config.Token)
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	if w.Code != 200 || strings.TrimSpace(w.Body.String()) != "[]" {
		t.Fatal(w.Code, w.Body.String())
	}
}
func TestVaultConfigurationBoundaries(t *testing.T) {
	v := VaultConfig{ID: "archive", Source: "Protected", Cipher: "/cipher/archive", Mount: "/vault/archive", Gocryptfs: "/bin/gocryptfs", Fusermount: "/bin/fusermount3", StartClient: "/bin/start-vault", StopClient: "/bin/stop-vault"}
	c := Config{State: "/state", Roots: []Root{{Name: "Protected", Path: "/vault/archive/files"}}, Vaults: []VaultConfig{v}}
	if e := validateVaults(c); e != nil {
		t.Fatal(e)
	}
	for _, change := range []func(*Config){
		func(c *Config) { c.Roots[0].Path = "/vault/archive" },
		func(c *Config) { c.Vaults[0].Cipher = "/vault/archive/cipher" },
		func(c *Config) { c.Vaults[0].StartClient = "" },
		func(c *Config) { c.Vaults = append(c.Vaults, c.Vaults[0]) },
		func(c *Config) { c.Roots = append(c.Roots, Root{Name: "public", Path: "/cipher"}) },
		func(c *Config) { c.Normalization = NormalizationConfig{Enabled: true, Source: "Protected"} },
		func(c *Config) { c.Vaults[0].BackingMount = "/samba" },
	} {
		copy := c
		copy.Roots = append([]Root(nil), c.Roots...)
		copy.Vaults = append([]VaultConfig(nil), c.Vaults...)
		change(&copy)
		if e := validateVaults(copy); e == nil {
			t.Fatal("unsafe configuration accepted")
		}
	}
}
func TestEmptyVaultMountDoesNotContainWritableFallback(t *testing.T) {
	path := t.TempDir()
	if e := emptyMount(path); e != nil {
		t.Fatal(e)
	}
	defer os.Chmod(path, 0700)
	st, _ := os.Stat(path)
	if st.Mode().Perm()&0222 != 0 {
		t.Fatal("unmounted mountpoint writable")
	}
	link := filepath.Join(t.TempDir(), "alias")
	if e := os.Symlink(path, link); e != nil {
		t.Fatal(e)
	}
	if e := emptyMount(link); !errors.Is(e, ErrUnsafe) {
		t.Fatal("symlink mountpoint accepted")
	}
}

// Set FILEBEANS_TEST_GOCRYPTFS to a pinned standalone gocryptfs executable.
// Initialization does not require FUSE; full recovery explicitly does.
func TestGocryptfsMasterKeyInitializationCompatibility(t *testing.T) {
	binary := os.Getenv("FILEBEANS_TEST_GOCRYPTFS")
	if binary == "" {
		t.Skip("set FILEBEANS_TEST_GOCRYPTFS for real-tool compatibility check")
	}
	key := bytes.Repeat([]byte{0x42}, 32)
	cipher := t.TempDir()
	if e := initializeVaultCipher(binary, cipher, key); e != nil {
		t.Fatal(e)
	}
	var e error
	for _, name := range []string{"gocryptfs.conf", "gocryptfs.diriv"} {
		if _, e = os.Stat(filepath.Join(cipher, name)); e != nil {
			t.Fatal(e)
		}
	}
	b, e := os.ReadFile(filepath.Join(cipher, "gocryptfs.conf"))
	if e != nil {
		t.Fatal(e)
	}
	if bytes.Contains(b, []byte(hex.EncodeToString(key))) {
		t.Fatal("raw key persisted in config")
	}
	var config struct {
		Version      int
		FeatureFlags []string
	}
	if json.Unmarshal(b, &config) != nil || config.Version != 2 {
		t.Fatal("unexpected filesystem format")
	}
	for _, flag := range []string{"HKDF", "DirIV", "GCMIV128", "EMENames", "LongNames", "Raw64"} {
		if !strings.Contains(strings.Join(config.FeatureFlags, ","), flag) {
			t.Fatal("missing standard feature", flag)
		}
	}
}

func TestVaultInitializationCanRetryAfterMountFailure(t *testing.T) {
	binary := os.Getenv("FILEBEANS_TEST_GOCRYPTFS")
	if binary == "" {
		t.Skip("set FILEBEANS_TEST_GOCRYPTFS")
	}
	v := VaultConfig{ID: "retry", Source: "Protected", Cipher: t.TempDir(), Gocryptfs: binary}
	m := &vaultManager{keyFile: filepath.Join(t.TempDir(), "vault-key-id"), states: []*vaultState{{config: v}}}
	key := bytes.Repeat([]byte{0x42}, 32)
	if e := m.prepareInitialization(v, key); e != nil {
		t.Fatal(e)
	}
	before, e := os.ReadFile(filepath.Join(v.Cipher, "gocryptfs.diriv"))
	if e != nil {
		t.Fatal(e)
	}
	if e = m.prepareInitialization(v, key); e != nil {
		t.Fatal("retry failed", e)
	}
	after, _ := os.ReadFile(filepath.Join(v.Cipher, "gocryptfs.diriv"))
	if !bytes.Equal(before, after) {
		t.Fatal("retry replaced filesystem metadata")
	}
	if e = m.prepareInitialization(v, bytes.Repeat([]byte{0x43}, 32)); !errors.Is(e, ErrVaultKey) {
		t.Fatal("different master key accepted", e)
	}
	if m.status()[0].Initialized {
		t.Fatal("unfinished initialization advertised as complete")
	}
}

// Explicit opt-in: executes real FUSE mounts with synthetic data only. This is
// independent of fileBeans services, SQL, Keychain, or original server state.
func TestGocryptfsIndependentRecovery(t *testing.T) {
	if os.Getenv("FILEBEANS_TEST_FUSE") != "1" {
		t.Skip("set FILEBEANS_TEST_FUSE=1 in a FUSE-capable test environment")
	}
	binary := os.Getenv("FILEBEANS_TEST_GOCRYPTFS")
	if binary == "" {
		t.Fatal("set FILEBEANS_TEST_GOCRYPTFS")
	}
	fuse, e := exec.LookPath("fusermount3")
	if e != nil {
		t.Fatal(e)
	}
	key := bytes.Repeat([]byte{0x42}, 32)
	code := recoveryCode(key)
	cipher := t.TempDir()
	if e = initializeVaultCipher(binary, cipher, key); e != nil {
		t.Fatal(e)
	}
	mount := func(cipher, path string, key []byte) func() {
		ctx, cancel := context.WithCancel(context.Background())
		cmd := exec.CommandContext(ctx, binary, "-fg", "-nosyslog", "-masterkey=stdin", cipher, path)
		cmd.Stdin = strings.NewReader(hex.EncodeToString(key) + "\n")
		if e := cmd.Start(); e != nil {
			cancel()
			t.Fatal(e)
		}
		done := make(chan struct{})
		go func() { _ = cmd.Wait(); close(done) }()
		stop := func() { _ = exec.Command(fuse, "-u", path).Run(); cancel(); <-done }
		for deadline := time.Now().Add(10 * time.Second); !mounted(path, cipher); {
			select {
			case <-done:
				cancel()
				t.Fatal("gocryptfs mount exited; check /dev/fuse and mount permissions")
			default:
			}
			if time.Now().After(deadline) {
				stop()
				t.Fatal("mount did not appear")
			}
			time.Sleep(25 * time.Millisecond)
		}
		return stop
	}
	first := t.TempDir()
	stop := mount(cipher, first, key)
	data := []byte("independent recovery fixture\n")
	if e = os.MkdirAll(filepath.Join(first, "files", "family"), 0700); e != nil {
		stop()
		t.Fatal(e)
	}
	if e = os.WriteFile(filepath.Join(first, "files", "family", "photo.txt"), data, 0600); e != nil {
		stop()
		t.Fatal(e)
	}
	stop()
	// Simulate having only a backup of ciphertext and the portable recovery code.
	backup := t.TempDir()
	e = filepath.Walk(cipher, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(cipher, path)
		if err != nil {
			return err
		}
		target := filepath.Join(backup, rel)
		if info.IsDir() {
			return os.MkdirAll(target, 0700)
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, b, 0600)
	})
	if e != nil {
		t.Fatal(e)
	}
	recoveredKey, e := parseVaultKey(code)
	if e != nil {
		t.Fatal(e)
	}
	second := t.TempDir()
	stop = mount(backup, second, recoveredKey)
	defer stop()
	got, e := os.ReadFile(filepath.Join(second, "files", "family", "photo.txt"))
	if e != nil || !bytes.Equal(got, data) {
		t.Fatal("independent recovery failed", e)
	}
}
