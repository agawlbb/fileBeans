package service

// Vaults use the standard gocryptfs forward format. No fileBeans-specific
// encryption is applied to file contents or names.
import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"golang.org/x/sys/unix"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

var ErrVaultLocked = errors.New("protected folder is locked")
var ErrVaultBusy = errors.New("protected folder is busy")
var ErrVaultKey = errors.New("encryption key does not match")
var errVault = errors.New("vault operation failed; check protected-folder setup")

type VaultConfig struct {
	ID     string `json:"id"`
	Source string `json:"source"`
	Cipher string `json:"cipher"`
	Mount  string `json:"mount"`
	// For remote ciphertext, verify the underlying SMB mount before access.
	BackingMount  string `json:"backingMount,omitempty"`
	BackingSource string `json:"backingSource,omitempty"`
	Gocryptfs     string `json:"gocryptfs"`
	Fusermount    string `json:"fusermount"`
	// Fixed administrator-owned executables. Invoked with the vault ID only.
	// Start must expose only Mount/files, with ALL client state in Mount/private.
	// Stop must stop these clients, including streams/indexers, and flush state.
	StartClient string `json:"startClient"`
	StopClient  string `json:"stopClient"`
}
type vaultState struct {
	config   VaultConfig
	cmd      *exec.Cmd
	done     chan struct{}
	unlocked bool
	fault    bool
}
type vaultManager struct {
	mu          sync.Mutex
	states      []*vaultState
	metadataKey []byte
	fingerprint [32]byte
	keyFile     string                 // public SHA-256 key identifier only; never key material
	mountCheck  func(VaultConfig) bool // nil uses the kernel mount table
	lockFile    *os.File
}

func (m *vaultManager) isMounted(v VaultConfig) bool {
	if m.mountCheck != nil {
		return m.mountCheck(v)
	}
	return vaultMounted(v)
}

type VaultStatus struct {
	ID          string `json:"id"`
	Source      string `json:"source"`
	State       string `json:"state"`
	Initialized bool   `json:"initialized"`
}

func validateVaults(c Config) error {
	ids, sources := map[string]bool{}, map[string]bool{}
	for _, v := range c.Vaults {
		if !validName(v.ID) || ids[v.ID] || sources[v.Source] || v.Source == "" {
			return ErrUnsafe
		}
		ids[v.ID], sources[v.Source] = true, true
		for _, p := range []string{v.Cipher, v.Mount, v.Gocryptfs, v.Fusermount, v.StartClient, v.StopClient} {
			if !filepath.IsAbs(p) || filepath.Clean(p) != p || p == "/" {
				return ErrUnsafe
			}
		}
		if (v.BackingMount == "") != (v.BackingSource == "") || v.BackingMount != "" && (!filepath.IsAbs(v.BackingMount) || !within(v.Cipher, v.BackingMount)) {
			return ErrUnsafe
		}
		found := false
		for _, r := range c.Roots {
			if r.Name == v.Source {
				found = r.Path == filepath.Join(v.Mount, "files") && r.Mount == ""
			}
			if within(v.Cipher, r.Path) || within(r.Path, v.Cipher) || r.Name != v.Source && (within(v.Mount, r.Path) || within(r.Path, v.Mount)) {
				return ErrUnsafe
			}
		}
		if !found || within(v.Cipher, v.Mount) || within(v.Mount, v.Cipher) || within(c.State, v.Mount) || within(v.Mount, c.State) || within(c.State, v.Cipher) || within(v.Cipher, c.State) {
			return ErrUnsafe
		}
		if c.Normalization.Enabled && c.Normalization.Source == v.Source {
			return errors.New("normalization cannot use protected roots")
		}
		for _, other := range c.Vaults {
			if other.ID == v.ID {
				continue
			}
			for _, a := range []string{v.Mount, v.Cipher} {
				for _, b := range []string{other.Mount, other.Cipher} {
					if within(a, b) || within(b, a) {
						return ErrUnsafe
					}
				}
			}
		}
	}
	return nil
}

func newVaultManager(c Config) (*vaultManager, error) {
	if len(c.Vaults) == 0 {
		return nil, nil
	}
	if e := validateVaults(c); e != nil {
		return nil, e
	}
	// With managed Go heaps and gocryptfs child processes, mlock of one buffer
	// cannot guarantee that all secret copies are unswappable. Fail closed unless
	// swap is disabled for the host, and disable core dumps before receiving keys.
	if e := vaultMemoryPolicy(); e != nil {
		return nil, e
	}
	m := &vaultManager{keyFile: filepath.Join(c.State, "vault-key-id")}
	lock, e := os.OpenFile(filepath.Join(c.State, "vaults.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if e != nil {
		return nil, e
	}
	if e = unix.Flock(int(lock.Fd()), unix.LOCK_EX|unix.LOCK_NB); e != nil {
		lock.Close()
		return nil, errors.New("another companion owns the vaults")
	}
	m.lockFile = lock
	ready := false
	defer func() {
		if !ready {
			lock.Close()
		}
	}()
	for _, v := range c.Vaults {
		if mounted(v.Mount, "") {
			return nil, errors.New("stale vault mount: stop its clients and unmount before starting companion")
		}
		if e := emptyMount(v.Mount); e != nil {
			return nil, e
		}
		if e := vaultHook(v.StopClient, v.ID); e != nil {
			return nil, e
		}
		m.states = append(m.states, &vaultState{config: v})
	}
	ready = true
	return m, nil
}
func vaultMemoryPolicy() error {
	b, e := os.ReadFile("/proc/swaps")
	if e != nil {
		return e
	}
	if len(strings.Fields(string(b))) != 5 {
		return errors.New("protected folders require host swap disabled")
	}
	if e = syscall.Setrlimit(syscall.RLIMIT_CORE, &syscall.Rlimit{}); e != nil {
		return e
	}
	if e = unix.Prctl(unix.PR_SET_DUMPABLE, 0, 0, 0, 0); e != nil {
		return e
	}
	return nil
}
func emptyMount(path string) error {
	resolved, e := filepath.EvalSymlinks(path)
	if e != nil || resolved != path {
		return ErrUnsafe
	}
	entries, e := os.ReadDir(path)
	if e != nil {
		return e
	}
	if len(entries) != 0 {
		return ErrConflict
	}
	// Owner-readable, non-writable fallback, even when Quantum's group has access to
	// mounted files. Never create the public files directory outside the mount.
	return os.Chmod(path, 0500)
}
func mounted(path, source string) bool {
	b, e := os.ReadFile("/proc/self/mountinfo")
	if e != nil {
		// A failed mount inspection must never report a successful lock.
		return source == ""
	}
	decode := strings.NewReplacer("\\040", " ", "\\011", "\t", "\\012", "\n", "\\134", "\\")
	for _, line := range strings.Split(string(b), "\n") {
		p := strings.SplitN(line, " - ", 2)
		if len(p) != 2 {
			continue
		}
		a, b := strings.Fields(p[0]), strings.Fields(p[1])
		if len(a) > 4 && len(b) > 1 && decode.Replace(a[4]) == path && (source == "" || decode.Replace(b[1]) == source) {
			return true
		}
	}
	return false
}
func vaultMounted(v VaultConfig) bool { return mounted(v.Mount, "filebeans-vault-"+v.ID) }
func vaultHook(binary, id string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, id)
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	if cmd.Run() != nil {
		return errVault
	}
	return nil
}
func parseVaultKey(code string) ([]byte, error) {
	parts := strings.Split(strings.TrimSpace(code), ":")
	if len(parts) != 3 || parts[0] != "FBK1" || len(parts[1]) != 64 || len(parts[2]) != 8 {
		return nil, ErrUnsafe
	}
	b, e := hex.DecodeString(parts[1])
	if e != nil || parts[1] != strings.ToLower(parts[1]) {
		return nil, ErrUnsafe
	}
	h := sha256.Sum256([]byte("FBK1:" + parts[1]))
	if subtle.ConstantTimeCompare([]byte(parts[2]), []byte(hex.EncodeToString(h[:4]))) != 1 {
		clear(b)
		return nil, ErrUnsafe
	}
	return b, nil
}
func (m *vaultManager) find(id string) *vaultState {
	for _, v := range m.states {
		if v.config.ID == id {
			return v
		}
	}
	return nil
}
func (m *vaultManager) status() []VaultStatus {
	if m == nil {
		return []VaultStatus{}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []VaultStatus{}
	for _, v := range m.states {
		state := "locked"
		if v.fault {
			state = "error"
		} else if v.unlocked && m.isMounted(v.config) {
			state = "unlocked"
		} else if v.cmd != nil || mounted(v.config.Mount, "") || v.fault {
			state = "error"
		}
		_, e := os.Stat(filepath.Join(v.config.Cipher, "gocryptfs.conf"))
		_, pending := os.Stat(m.initializationFile(v.config.ID))
		out = append(out, VaultStatus{v.config.ID, v.config.Source, state, e == nil && errors.Is(pending, os.ErrNotExist)})
	}
	return out
}
func (m *vaultManager) checkRoot(source string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, v := range m.states {
		if v.config.Source == source && (!v.unlocked || v.fault || !m.isMounted(v.config)) {
			return ErrVaultLocked
		}
	}
	return nil
}
func (m *vaultManager) protectedSource(source string) bool {
	if m == nil {
		return false
	}
	for _, v := range m.states {
		if v.config.Source == source {
			return true
		}
	}
	return false
}
func (m *vaultManager) unlock(id string, key []byte, initialize bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	v := m.find(id)
	if v == nil {
		return ErrUnsafe
	}
	if e := vaultMemoryPolicy(); e != nil {
		return errVault
	}
	fingerprint := sha256.Sum256(key)
	if m.keyFile != "" {
		known, e := os.ReadFile(m.keyFile)
		if e == nil && !hmac.Equal(known, []byte(hex.EncodeToString(fingerprint[:]))) {
			return ErrVaultKey
		}
		if e != nil && !errors.Is(e, os.ErrNotExist) {
			return errVault
		}
	}
	if m.metadataKey != nil && !hmac.Equal(fingerprint[:], m.fingerprint[:]) {
		return ErrVaultKey
	}
	if v.unlocked {
		if vaultMounted(v.config) {
			return nil
		}
		return errVault
	}
	if v.cmd != nil || mounted(v.config.Mount, "") {
		return ErrVaultBusy
	}
	if v.config.BackingMount != "" && !mounted(v.config.BackingMount, v.config.BackingSource) {
		return ErrMount
	}
	resolved, e := filepath.EvalSymlinks(v.config.Cipher)
	if e != nil || resolved != v.config.Cipher {
		return ErrUnsafe
	}
	if e = emptyMount(v.config.Mount); e != nil {
		return e
	}
	if initialize {
		if e = m.prepareInitialization(v.config, key); e != nil {
			return e
		}
	}
	cmd := exec.Command(v.config.Gocryptfs, "-fg", "-nosyslog", "-allow_other", "-noexec", "-fsname=filebeans-vault-"+v.config.ID, "-masterkey=stdin", v.config.Cipher, v.config.Mount)
	input := make([]byte, 65)
	hex.Encode(input[:64], key)
	input[64] = '\n'
	defer clear(input)
	stdin, e := cmd.StdinPipe()
	if e != nil {
		return errVault
	}
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	// Killing companion must not leave an orphan with a usable key.
	cmd.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGKILL}
	if cmd.Start() != nil {
		stdin.Close()
		return errVault
	}
	v.cmd = cmd
	v.done = make(chan struct{})
	go func() { _ = cmd.Wait(); close(v.done) }()
	_, e = stdin.Write(input)
	clear(input)
	stdin.Close()
	if e != nil {
		_ = cmd.Process.Kill()
		<-v.done
		v.cmd = nil
		return errVault
	}
	deadline := time.Now().Add(10 * time.Second)
	for !vaultMounted(v.config) && time.Now().Before(deadline) {
		select {
		case <-v.done:
			v.cmd = nil
			return errVault
		case <-time.After(30 * time.Millisecond):
		}
	}
	rollback := func() { _ = vaultHook(v.config.StopClient, v.config.ID); _ = unmountVault(v); v.fault = true }
	if !vaultMounted(v.config) {
		rollback()
		return errVault
	}
	marker := filepath.Join(v.config.Mount, "private", "identity")
	if initialize {
		for _, name := range []string{"files", "private", "private/cache", "private/tmp"} {
			if e = os.MkdirAll(filepath.Join(v.config.Mount, name), 0700); e != nil {
				rollback()
				return errVault
			}
		}
		e = os.WriteFile(marker, []byte("fileBeans vault v1\n"+v.config.ID+"\n"), 0600)
		if e == nil {
			e = os.WriteFile(filepath.Join(v.config.Cipher, "RECOVERY.txt"), []byte(vaultRecovery), 0600)
		}
		if e != nil {
			rollback()
			return errVault
		}
	}
	b, e := os.ReadFile(marker)
	if e != nil || string(b) != "fileBeans vault v1\n"+v.config.ID+"\n" {
		rollback()
		return errVault
	}
	if m.keyFile != "" {
		f, e := os.OpenFile(m.keyFile, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if e == nil {
			_, e = f.WriteString(hex.EncodeToString(fingerprint[:]))
			if e == nil {
				e = f.Sync()
			}
			f.Close()
		}
		if e != nil && !errors.Is(e, os.ErrExist) {
			rollback()
			return errVault
		}
	}
	if initialize {
		if e = os.Remove(m.initializationFile(v.config.ID)); e != nil && !errors.Is(e, os.ErrNotExist) {
			rollback()
			return errVault
		}
	}
	v.unlocked = true
	v.fault = false
	if m.metadataKey == nil {
		mac := hmac.New(sha256.New, key)
		mac.Write([]byte("fileBeans metadata v1"))
		m.metadataKey = mac.Sum(nil)
		m.fingerprint = fingerprint
	}
	return nil
}

func (m *vaultManager) initializationFile(id string) string {
	return filepath.Join(filepath.Dir(m.keyFile), "vault-initializing-"+id)
}
func (m *vaultManager) prepareInitialization(v VaultConfig, key []byte) error {
	h := sha256.Sum256(key)
	identifier := []byte(hex.EncodeToString(h[:]))
	for _, other := range m.states {
		known, e := os.ReadFile(m.initializationFile(other.config.ID))
		if e == nil && !hmac.Equal(known, identifier) {
			return ErrVaultKey
		}
		if e != nil && !errors.Is(e, os.ErrNotExist) {
			return errVault
		}
	}
	entries, e := os.ReadDir(v.Cipher)
	if e != nil {
		return e
	}
	pending := m.initializationFile(v.ID)
	_, e = os.Stat(pending)
	if errors.Is(e, os.ErrNotExist) {
		if len(entries) != 0 {
			return ErrConflict
		}
		if e = os.WriteFile(pending, identifier, 0600); e != nil {
			return e
		}
	} else if e != nil {
		return e
	}
	// A mount failure must not strand a newly initialized empty vault. Retry
	// initialization with the same key, retaining its standard config/diriv.
	_, e = os.Stat(filepath.Join(v.Cipher, "gocryptfs.conf"))
	if e == nil {
		return nil
	}
	if !errors.Is(e, os.ErrNotExist) || len(entries) != 0 {
		return ErrConflict
	}
	return initializeVaultCipher(v.Gocryptfs, v.Cipher, key)
}
func unmountVault(v *vaultState) error {
	if mounted(v.config.Mount, "") {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		// No force or lazy unmount: busy handles must prevent successful locking.
		cmd := exec.CommandContext(ctx, v.config.Fusermount, "-u", v.config.Mount)
		cmd.Stdout = io.Discard
		cmd.Stderr = io.Discard
		if cmd.Run() != nil || mounted(v.config.Mount, "") {
			return ErrVaultBusy
		}
	}
	if v.cmd != nil {
		select {
		case <-v.done:
		case <-time.After(time.Second):
			_ = v.cmd.Process.Kill()
			<-v.done
		}
		v.cmd = nil
	}
	v.unlocked = false
	return nil
}
func (m *vaultManager) lock(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	v := m.find(id)
	if v == nil {
		return ErrUnsafe
	}
	if e := vaultHook(v.config.StopClient, v.config.ID); e != nil {
		v.fault = true
		return e
	}
	if e := unmountVault(v); e != nil {
		v.fault = true
		return e
	}
	v.fault = false
	anyOpen := false
	for _, other := range m.states {
		if other.cmd != nil || mounted(other.config.Mount, "") {
			anyOpen = true
		}
	}
	if !anyOpen {
		clear(m.metadataKey)
		m.metadataKey = nil
		clear(m.fingerprint[:])
	}
	return nil
}
func (m *vaultManager) close() {
	for _, v := range m.states {
		_ = m.lock(v.config.ID)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	clear(m.metadataKey)
	m.metadataKey = nil
	if m.lockFile != nil {
		m.lockFile.Close()
		m.lockFile = nil
	}
}

func (s *Service) vaultRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/vaults", func(w http.ResponseWriter, r *http.Request) { reply(w, 200, s.vaults.status()) })
	for _, action := range []string{"initialize", "unlock", "lock"} {
		mux.HandleFunc("POST /v1/vaults/{id}/"+action, func(w http.ResponseWriter, r *http.Request) {
			if s.vaults == nil {
				fail(w, ErrUnsafe)
				return
			}
			// Cancel workers first. The caller retries after their current I/O has
			// drained; TryLock avoids holding an HTTP connection indefinitely.
			if action == "lock" {
				s.mu.Lock()
				for _, stop := range s.copyCancels {
					stop()
				}
				s.mu.Unlock()
			}
			if !s.vaultGate.TryLock() {
				fail(w, ErrVaultBusy)
				return
			}
			defer s.vaultGate.Unlock()
			id := r.PathValue("id")
			if action == "lock" {
				if e := s.vaults.lock(id); e != nil {
					fail(w, e)
					return
				}
			} else {
				var req struct {
					Key string `json:"key"`
				}
				if e := body(r, &req); e != nil {
					fail(w, e)
					return
				}
				key, e := parseVaultKey(req.Key)
				req.Key = ""
				if e != nil {
					fail(w, e)
					return
				}
				defer clear(key)
				if action == "initialize" && r.Header.Get("X-Confirm-Permanent") != "yes" {
					fail(w, ErrUnsafe)
					return
				}
				if e = s.vaults.unlock(id, key, action == "initialize"); e != nil {
					fail(w, e)
					return
				}
				if e = s.migrateVaultRecords(); e != nil {
					_ = s.vaults.lock(id)
					fail(w, errVault)
					return
				}
				v := s.vaults.find(id)
				if e = vaultHook(v.config.StartClient, id); e != nil {
					_ = s.vaults.lock(id)
					fail(w, e)
					return
				}
			}
			reply(w, 200, s.vaults.status())
		})
	}
}

const vaultRecovery = `fileBeans vault, standard gocryptfs forward format (default AES-GCM/HKDF settings).
Keep the ENTIRE ciphertext directory, including gocryptfs.conf, gocryptfs.diriv,
and all long-name metadata. fileBeans and the original server are not required.
The FBK1 recovery code is FBK1:<64 hexadecimal master-key characters>:<checksum>.
The middle field is the actual 256-bit gocryptfs master key. Never put it in argv.
On another Linux machine with gocryptfs installed, create an empty mount directory:
  gocryptfs -masterkey=stdin /path/to/ciphertext /path/to/empty-mount
Enter ONLY the middle field followed by Enter. Your files are under mount/files.
  fusermount3 -u /path/to/empty-mount
All vaults created with the same recovery code share that master key.
Do not change format settings. Never discard filesystem metadata.
`

// Keep json imported here for the common protected-record classification.
func (s *Service) sensitiveRecord(b []byte) bool {
	if s.vaults == nil {
		return false
	}
	var value any
	if json.Unmarshal(b, &value) != nil {
		return true
	}
	var walk func(any) bool
	walk = func(v any) bool {
		switch x := v.(type) {
		case map[string]any:
			for k, v := range x {
				if k == "source" || k == "librarySource" {
					if source, ok := v.(string); ok && s.vaults.protectedSource(source) {
						return true
					}
				}
				if walk(v) {
					return true
				}
			}
		case []any:
			for _, v := range x {
				if walk(v) {
					return true
				}
			}
		}
		return false
	}
	return walk(value)
}

// gocryptfs reads the discarded config password first, then the master key,
// one line at a time from stdin. Neither secret is passed in argv or a file.
func initializeVaultCipher(binary, directory string, key []byte) error {
	password := make([]byte, 32)
	if _, e := rand.Read(password); e != nil {
		return e
	}
	defer clear(password)
	input := []byte(hex.EncodeToString(password) + "\n" + hex.EncodeToString(key) + "\n")
	defer clear(input)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, "-init", "-masterkey=stdin", directory)
	cmd.Stdin = bytes.NewReader(input)
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	if cmd.Run() != nil {
		return errVault
	}
	return nil
}
