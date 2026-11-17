package service

// The mount broker is a separate process. It accepts source-relative locations,
// never caller-supplied executable names, mount options, or absolute host paths.
import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"golang.org/x/sys/unix"
)

const brokerStore = ".filebeans-vaults"
const brokerProtocol = 1

type BrokerConfig struct {
	State      string `json:"state"`
	Runtime    string `json:"runtime"`
	Roots      []Root `json:"roots"`
	Gocryptfs  string `json:"gocryptfs"`
	Fusermount string `json:"fusermount"`
	ControlUID uint32 `json:"controlUID"`
	ReaderUID  uint32 `json:"readerUID"`
	GID        int    `json:"gid"`
}

type brokerEntry struct {
	ID          string `json:"id"`
	Source      string `json:"source"`
	Path        string `json:"path"`
	RequestHash string `json:"requestHash"`
	Device      uint64 `json:"device"`
	Inode       uint64 `json:"inode"`
}

type brokerRegistry struct {
	Version int           `json:"version"`
	Entries []brokerEntry `json:"entries"`
}

type BrokerVault struct {
	ID          string `json:"id"`
	Source      string `json:"source"`
	Path        string `json:"path"`
	Name        string `json:"name"`
	State       string `json:"state"`
	Initialized bool   `json:"initialized"`
}

type brokerSnapshot struct {
	Version int           `json:"version"`
	Vaults  []BrokerVault `json:"vaults"`
}

type mountBroker struct {
	mu       sync.Mutex
	config   BrokerConfig
	registry brokerRegistry
	manager  *vaultManager
	// Injected only by unit tests. The daemon always uses Linux mounts.
	mountFiles    func(string, string) error
	unmountFiles  func(string) error
	consumerReady time.Time
}

func validateBrokerConfig(c BrokerConfig) error {
	if len(c.Roots) == 0 || c.GID < 0 || c.ControlUID == c.ReaderUID {
		return ErrUnsafe
	}
	for _, p := range []string{c.State, c.Runtime, c.Gocryptfs, c.Fusermount} {
		if !filepath.IsAbs(p) || filepath.Clean(p) != p || p == "/" {
			return ErrUnsafe
		}
	}
	if within(c.State, c.Runtime) || within(c.Runtime, c.State) {
		return ErrUnsafe
	}
	seen := map[string]bool{}
	for _, r := range c.Roots {
		if !validName(r.Name) || r.Name == "upload" || r.Name == "drive" || seen[r.Name] ||
			!filepath.IsAbs(r.Path) || filepath.Clean(r.Path) != r.Path || r.Path == "/" ||
			(r.Mount == "") != (r.MountSource == "") {
			return ErrUnsafe
		}
		seen[r.Name] = true
		for _, p := range []string{c.State, c.Runtime} {
			if within(p, r.Path) || within(r.Path, p) {
				return ErrUnsafe
			}
		}
		for _, other := range c.Roots {
			if r.Name != other.Name && (within(r.Path, other.Path) || within(other.Path, r.Path)) {
				return ErrUnsafe
			}
		}
	}
	return nil
}

func newMountBroker(c BrokerConfig) (*mountBroker, error) {
	if e := validateBrokerConfig(c); e != nil {
		return nil, e
	}
	for _, p := range []string{c.State, c.Runtime, filepath.Join(c.Runtime, "mounts")} {
		if e := os.MkdirAll(p, 0700); e != nil {
			return nil, e
		}
		resolved, e := filepath.EvalSymlinks(p)
		if e != nil || resolved != p {
			return nil, ErrUnsafe
		}
		var st unix.Stat_t
		if unix.Stat(p, &st) != nil || st.Uid != uint32(os.Geteuid()) || st.Mode&0022 != 0 {
			return nil, ErrUnsafe
		}
	}
	for _, p := range []string{c.Runtime, filepath.Join(c.Runtime, "mounts")} {
		if e := os.Chown(p, -1, c.GID); e != nil {
			return nil, e
		}
		if e := os.Chmod(p, 0750); e != nil {
			return nil, e
		}
	}
	owner, e := os.OpenFile(filepath.Join(c.State, "broker.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if e != nil {
		return nil, e
	}
	if e = unix.Flock(int(owner.Fd()), unix.LOCK_EX|unix.LOCK_NB); e != nil {
		owner.Close()
		return nil, ErrVaultBusy
	}
	b := &mountBroker{config: c, registry: brokerRegistry{Version: brokerProtocol}, manager: &vaultManager{
		keyFile: filepath.Join(c.State, "vault-key-id"), lockFile: owner,
	}}
	b.mountFiles = func(from, to string) error { return unix.Mount(from, to, "", unix.MS_BIND, "") }
	b.unmountFiles = func(path string) error { return unix.Unmount(path, 0) }
	ok := false
	defer func() {
		if !ok {
			owner.Close()
		}
	}()
	gate, e := os.OpenFile(filepath.Join(c.Runtime, "gate"), os.O_CREATE|os.O_RDWR, 0640)
	if e != nil {
		return nil, e
	}
	e = gate.Chown(-1, c.GID)
	if e == nil {
		e = gate.Chmod(0640)
	}
	gate.Close()
	if e != nil {
		return nil, e
	}
	data, e := os.ReadFile(filepath.Join(c.State, "registry.json"))
	if e == nil {
		if e = json.Unmarshal(data, &b.registry); e != nil || b.registry.Version != brokerProtocol {
			return nil, ErrUnsafe
		}
	} else if !errors.Is(e, os.ErrNotExist) {
		return nil, e
	}
	ids, requests := map[string]bool{}, map[string]bool{}
	for i, entry := range b.registry.Entries {
		if !validBrokerID(entry.ID) || ids[entry.ID] || requests[entry.RequestHash] || len(entry.RequestHash) != 64 {
			return nil, ErrUnsafe
		}
		ids[entry.ID], requests[entry.RequestHash] = true, true
		if _, e = hex.DecodeString(entry.RequestHash); e != nil {
			return nil, ErrUnsafe
		}
		if e = b.validateLocation(entry.Source, entry.Path, b.registry.Entries[:i]); e != nil {
			return nil, e
		}
		public, e := b.publicDir(entry)
		if e != nil {
			return nil, e
		}
		public.Close()
		v := b.vaultConfig(entry)
		if mounted(v.Mount, "") || mounted(b.publicPath(entry), "") {
			return nil, ErrVaultBusy
		}
		if e = os.MkdirAll(v.Mount, 0500); e != nil {
			return nil, e
		}
		if e = emptyMount(v.Mount); e != nil {
			return nil, e
		}
		b.manager.states = append(b.manager.states, &vaultState{config: v})
	}
	if e = b.publish(); e != nil {
		return nil, e
	}
	ok = true
	return b, nil
}

func validBrokerID(id string) bool {
	if len(id) != 32 {
		return false
	}
	_, e := hex.DecodeString(id)
	return e == nil && strings.ToLower(id) == id
}

func (b *mountBroker) root(source string) (Root, error) {
	r, e := b.configuredRoot(source)
	if e != nil {
		return Root{}, e
	}
	if r.Mount != "" && !mounted(r.Mount, r.MountSource) {
		return Root{}, ErrMount
	}
	resolved, e := filepath.EvalSymlinks(r.Path)
	if e != nil || resolved != r.Path {
		return Root{}, ErrUnsafe
	}
	return r, nil
}

func (b *mountBroker) configuredRoot(source string) (Root, error) {
	for _, r := range b.config.Roots {
		if r.Name == source {
			return r, nil
		}
	}
	return Root{}, ErrUnsafe
}

// Openat2 pins the directory and rejects every symlink component, including a
// concurrent replacement. All mount targets are subsequently addressed by FD.
func brokerOpenDir(root, path string) (*os.File, error) {
	base, e := unix.Open(root, unix.O_PATH|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if e != nil {
		return nil, e
	}
	defer unix.Close(base)
	return brokerOpenAt(base, path)
}

func brokerOpenAt(base int, path string) (*os.File, error) {
	fd, e := unix.Openat2(base, path, &unix.OpenHow{Flags: unix.O_PATH | unix.O_DIRECTORY | unix.O_CLOEXEC,
		Resolve: unix.RESOLVE_BENEATH | unix.RESOLVE_NO_SYMLINKS})
	if e != nil {
		return nil, ErrUnsafe
	}
	return os.NewFile(uintptr(fd), path), nil
}

func (b *mountBroker) validateLocation(source, path string, entries []brokerEntry) error {
	p, e := clean(path)
	if e != nil || p == "." || path != "/"+p {
		return ErrUnsafe
	}
	for _, part := range strings.Split(p, "/") {
		if part == brokerStore || part == ".filebeans-stage" || part == "Trash" {
			return ErrUnsafe
		}
	}
	if _, e = b.root(source); e != nil {
		return e
	}
	for _, v := range entries {
		if v.Source == source && (within(path, v.Path) || within(v.Path, path)) {
			return ErrConflict
		}
	}
	return nil
}

func (b *mountBroker) publicPath(e brokerEntry) string {
	r, _ := b.configuredRoot(e.Source)
	return filepath.Join(r.Path, strings.TrimPrefix(e.Path, "/"))
}

func (b *mountBroker) publicDir(entry brokerEntry) (*os.File, error) {
	r, e := b.root(entry.Source)
	if e != nil {
		return nil, e
	}
	f, e := brokerOpenDir(r.Path, strings.TrimPrefix(entry.Path, "/"))
	if e != nil {
		return nil, e
	}
	var st unix.Stat_t
	if unix.Fstat(int(f.Fd()), &st) != nil || st.Ino != entry.Inode || uint64(st.Dev) != entry.Device {
		f.Close()
		return nil, ErrConflict
	}
	return f, nil
}

func (b *mountBroker) vaultConfig(e brokerEntry) VaultConfig {
	r, _ := b.configuredRoot(e.Source)
	return VaultConfig{ID: e.ID, Source: e.Source, Cipher: filepath.Join(r.Path, brokerStore, e.ID),
		Mount: filepath.Join(b.config.Runtime, "mounts", e.ID), Gocryptfs: b.config.Gocryptfs,
		Fusermount: b.config.Fusermount, BackingMount: r.Mount, BackingSource: r.MountSource,
		StartClient: "/bin/true", StopClient: "/bin/true"}
}

func brokerFD(f *os.File) string { return fmt.Sprintf("/proc/self/fd/%d", f.Fd()) }

func brokerAtomicJSON(path string, value any, mode os.FileMode, gid int) error {
	data, e := json.Marshal(value)
	if e != nil {
		return e
	}
	f, e := os.CreateTemp(filepath.Dir(path), ".broker-")
	if e != nil {
		return e
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if e = f.Chmod(mode); e != nil {
		return e
	}
	if e = f.Chown(-1, gid); e != nil {
		return e
	}
	if _, e = f.Write(data); e != nil {
		return e
	}
	if e = f.Sync(); e != nil {
		return e
	}
	if e = f.Close(); e != nil {
		return e
	}
	if e = os.Rename(f.Name(), path); e != nil {
		return e
	}
	dir, e := os.Open(filepath.Dir(path))
	if e != nil {
		return e
	}
	defer dir.Close()
	return dir.Sync()
}

func (b *mountBroker) saveRegistry() error {
	return brokerAtomicJSON(filepath.Join(b.config.State, "registry.json"), b.registry, 0600, b.config.GID)
}

func (b *mountBroker) statuses() []BrokerVault {
	states := b.manager.status()
	out := make([]BrokerVault, 0, len(b.registry.Entries))
	for _, e := range b.registry.Entries {
		v := BrokerVault{ID: e.ID, Source: e.Source, Path: e.Path, Name: filepath.Base(e.Path), State: "locked"}
		if _, err := b.root(e.Source); err != nil {
			v.State = "error"
			out = append(out, v)
			continue
		}
		for _, state := range states {
			if state.ID == e.ID {
				v.State, v.Initialized = state.State, state.Initialized
			}
		}
		publicMounted := mounted(b.publicPath(e), "filebeans-vault-"+e.ID)
		if v.State == "unlocked" && !publicMounted || v.State == "locked" && mounted(b.publicPath(e), "") {
			v.State = "error"
		}
		out = append(out, v)
	}
	return out
}

func (b *mountBroker) publish() error {
	return brokerAtomicJSON(filepath.Join(b.config.Runtime, "status.json"), brokerSnapshot{brokerProtocol, b.statuses()}, 0640, b.config.GID)
}

// Every cooperating reader holds a shared lock for its entire filesystem
// operation. A transition fails busy instead of waiting behind a long stream.
func (b *mountBroker) exclusive() (*os.File, error) {
	f, e := os.OpenFile(filepath.Join(b.config.Runtime, "gate"), os.O_RDWR, 0)
	if e != nil {
		return nil, e
	}
	if e = unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); e != nil {
		f.Close()
		return nil, ErrVaultBusy
	}
	return f, nil
}

func (b *mountBroker) reserve(source, parent, name, request string) (brokerEntry, error) {
	if !validName(name) || len(request) < 16 || len(request) > 64 {
		return brokerEntry{}, ErrUnsafe
	}
	p, e := clean(parent)
	if e != nil {
		return brokerEntry{}, e
	}
	path := "/" + strings.TrimPrefix(filepath.Join(p, name), "./")
	hash := sha256.Sum256([]byte(request))
	requestHash := hex.EncodeToString(hash[:])
	for _, entry := range b.registry.Entries {
		if entry.RequestHash == requestHash {
			if entry.Source != source || entry.Path != path {
				return brokerEntry{}, ErrConflict
			}
			return entry, nil
		}
	}
	if e = b.validateLocation(source, path, b.registry.Entries); e != nil {
		return brokerEntry{}, e
	}
	r, e := b.root(source)
	if e != nil {
		return brokerEntry{}, e
	}
	parentFD, e := brokerOpenDir(r.Path, p)
	if e != nil {
		return brokerEntry{}, e
	}
	defer parentFD.Close()
	// Device/inode checks also catch case-folded SMB aliases of a registered
	// placeholder. A lexical prefix check alone is insufficient on CIFS.
	baseFD, e := brokerOpenDir(r.Path, ".")
	if e != nil {
		return brokerEntry{}, e
	}
	defer baseFD.Close()
	var baseStat unix.Stat_t
	if unix.Fstat(int(baseFD.Fd()), &baseStat) != nil {
		return brokerEntry{}, ErrUnsafe
	}
	current := ""
	for _, part := range strings.Split(p, "/") {
		current = filepath.Join(current, part)
		ancestor, e := brokerOpenAt(int(baseFD.Fd()), current)
		if e != nil {
			return brokerEntry{}, e
		}
		var st unix.Stat_t
		e = unix.Fstat(int(ancestor.Fd()), &st)
		ancestor.Close()
		if e != nil || st.Dev != baseStat.Dev {
			return brokerEntry{}, ErrConflict
		}
		for _, existing := range b.registry.Entries {
			if existing.Source == source && uint64(st.Dev) == existing.Device && st.Ino == existing.Inode {
				return brokerEntry{}, ErrConflict
			}
		}
	}
	if e = unix.Mkdirat(int(parentFD.Fd()), name, 0500); e != nil {
		if errors.Is(e, os.ErrExist) {
			return brokerEntry{}, ErrConflict
		}
		return brokerEntry{}, e
	}
	parentSync, e := os.Open(brokerFD(parentFD))
	if e != nil {
		return brokerEntry{}, e
	}
	e = parentSync.Sync()
	parentSync.Close()
	if e != nil {
		return brokerEntry{}, e
	}
	public, e := brokerOpenAt(int(parentFD.Fd()), name)
	if e != nil {
		return brokerEntry{}, e
	}
	defer public.Close()
	var st unix.Stat_t
	if e = unix.Fstat(int(public.Fd()), &st); e != nil {
		return brokerEntry{}, e
	}
	entry := brokerEntry{ID: ID(), Source: source, Path: path, RequestHash: requestHash, Device: uint64(st.Dev), Inode: st.Ino}
	// Persist identity before generating any ciphertext. Interrupted reservations
	// remain owned by this request and can be resumed without overwriting data.
	b.registry.Entries = append(b.registry.Entries, entry)
	if e = b.saveRegistry(); e != nil {
		b.registry.Entries = b.registry.Entries[:len(b.registry.Entries)-1]
		return brokerEntry{}, e
	}
	v := b.vaultConfig(entry)
	b.manager.states = append(b.manager.states, &vaultState{config: v})
	return entry, nil
}

func (b *mountBroker) prepare(entry brokerEntry) error {
	r, e := b.root(entry.Source)
	if e != nil {
		return e
	}
	root, e := os.OpenRoot(r.Path)
	if e != nil {
		return e
	}
	defer root.Close()
	if e = safe(root, brokerStore); e != nil {
		return e
	}
	if e = root.MkdirAll(brokerStore, 0700); e != nil {
		return e
	}
	dir, e := brokerOpenDir(r.Path, brokerStore)
	if e != nil {
		return e
	}
	defer dir.Close()
	var st unix.Stat_t
	if unix.Fstat(int(dir.Fd()), &st) != nil || st.Uid != uint32(os.Geteuid()) || st.Mode&0022 != 0 {
		return ErrUnsafe
	}
	if e = unix.Mkdirat(int(dir.Fd()), entry.ID, 0700); e != nil && !errors.Is(e, os.ErrExist) {
		return e
	}
	v := b.vaultConfig(entry)
	if e = os.MkdirAll(v.Mount, 0500); e != nil {
		return e
	}
	return emptyMount(v.Mount)
}

func (b *mountBroker) open(entry brokerEntry, key []byte) error {
	if len(key) != 32 {
		return ErrUnsafe
	}
	if e := b.keyMatches(key); e != nil {
		return e
	}
	v := b.manager.find(entry.ID)
	if v == nil {
		return ErrUnsafe
	}
	if v.unlocked {
		if mounted(b.publicPath(entry), "filebeans-vault-"+entry.ID) {
			return nil
		}
		return errVault
	}
	public, e := b.publicDir(entry)
	if e != nil {
		return e
	}
	defer public.Close()
	if mounted(b.publicPath(entry), "") {
		return ErrVaultBusy
	}
	if e = b.prepare(entry); e != nil {
		return e
	}
	_, confErr := os.Stat(filepath.Join(v.config.Cipher, "gocryptfs.conf"))
	_, pendingErr := os.Stat(b.manager.initializationFile(entry.ID))
	initialize := errors.Is(confErr, os.ErrNotExist) || pendingErr == nil
	// Publish the protected boundary before any plaintext becomes visible to
	// filesystem watchers or scanners in other processes.
	if e = b.publish(); e != nil {
		return e
	}
	if e = b.manager.unlock(entry.ID, key, initialize); e != nil {
		return fmt.Errorf("mount private filesystem: %w", e)
	}
	rollback := func() { _ = b.manager.lock(entry.ID) }
	for _, relative := range []string{"files", "private", "private/cache", "private/tmp"} {
		dir, err := brokerOpenDir(v.config.Mount, relative)
		if err != nil {
			rollback()
			return err
		}
		e = unix.Fchownat(int(dir.Fd()), "", int(b.config.ReaderUID), b.config.GID, unix.AT_EMPTY_PATH|unix.AT_SYMLINK_NOFOLLOW)
		if e == nil {
			e = os.Chmod(brokerFD(dir), 0770)
		}
		dir.Close()
		if e != nil {
			rollback()
			return e
		}
	}
	if e = os.Chown(v.config.Mount, -1, b.config.GID); e != nil {
		rollback()
		return e
	}
	if e = os.Chmod(v.config.Mount, 0710); e != nil {
		rollback()
		return e
	}
	if e = b.mountFiles(filepath.Join(v.config.Mount, "files"), brokerFD(public)); e != nil {
		rollback()
		return fmt.Errorf("%w: public bind: %v", errVault, e)
	}
	if e = b.publish(); e != nil {
		_ = b.unmountFiles(b.publicPath(entry))
		rollback()
		return errVault
	}
	return nil
}

func (b *mountBroker) lock(entry brokerEntry) error {
	// Locking must remain possible when remote ciphertext storage goes offline.
	// Kernel mount identities, not backing-store availability, govern unmounting.
	if _, e := b.configuredRoot(entry.Source); e != nil {
		return e
	}
	if mounted(b.publicPath(entry), "") {
		if !mounted(b.publicPath(entry), "filebeans-vault-"+entry.ID) {
			return ErrUnsafe
		}
		if e := b.unmountFiles(b.publicPath(entry)); e != nil {
			return ErrVaultBusy
		}
	}
	if e := b.manager.lock(entry.ID); e != nil {
		_ = b.publish()
		return e
	}
	return b.publish()
}

func (b *mountBroker) close() {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, entry := range b.registry.Entries {
		_ = b.lock(entry)
	}
	b.manager.close()
}
