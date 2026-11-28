package service

// The mount broker is a separate process. It accepts source-relative locations,
// never caller-supplied executable names, mount options, or absolute host paths.
import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
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
	ProtectionSync     *BrokerProtectionSyncConfig `json:"protectionSync,omitempty"`
	FilesystemConsumer bool                        `json:"filesystemConsumer,omitempty"`
	LocationBackups    []string                    `json:"locationBackups,omitempty"`
	State              string                      `json:"state"`
	Runtime            string                      `json:"runtime"`
	Roots              []Root                      `json:"roots"`
	Gocryptfs          string                      `json:"gocryptfs"`
	Fusermount         string                      `json:"fusermount"`
	ControlUID         uint32                      `json:"controlUID"`
	ReaderUID          uint32                      `json:"readerUID"`
	GID                int                         `json:"gid"`
}

type brokerEntry struct {
	Location      *sealedLocation `json:"location,omitempty"`
	Parked        bool            `json:"parked,omitempty"`
	Parking       bool            `json:"parking,omitempty"`
	Trashed       int64           `json:"trashed,omitempty"`
	OriginalPath  string          `json:"originalPath,omitempty"`
	PreviousPaths []string        `json:"previousPaths,omitempty"`
	NextPath      string          `json:"nextPath,omitempty"`
	NextTrashed   int64           `json:"nextTrashed,omitempty"`
	Purging       bool            `json:"purging,omitempty"`
	Initialized   bool            `json:"initialized,omitempty"`
	ID            string          `json:"id"`
	Source        string          `json:"source"`
	Path          string          `json:"path"`
	RequestHash   string          `json:"requestHash"`
	Device        uint64          `json:"device"`
	Inode         uint64          `json:"inode"`
}

type brokerRegistry struct {
	Version int           `json:"version"`
	Entries []brokerEntry `json:"entries"`
}

type BrokerVault struct {
	Trashed       int64    `json:"trashed,omitempty"`
	OriginalPath  string   `json:"originalPath,omitempty"`
	PreviousPaths []string `json:"previousPaths,omitempty"`
	Device        uint64   `json:"device,omitempty"`
	Inode         uint64   `json:"inode,omitempty"`
	ID            string   `json:"id"`
	Source        string   `json:"source"`
	Path          string   `json:"path"`
	Name          string   `json:"name"`
	State         string   `json:"state"`
	Initialized   bool     `json:"initialized"`
}

type brokerSnapshot struct {
	UnavailableSources []string      `json:"unavailableSources,omitempty"`
	Version            int           `json:"version"`
	Vaults             []BrokerVault `json:"vaults"`
}

type mountBroker struct {
	protectionSync        *brokerProtectionSync
	unavailableSources    map[string]error
	datasetActivity       time.Time
	locationKey           []byte
	privateLocationsSaved map[string]bool
	mu                    sync.Mutex
	config                BrokerConfig
	registry              brokerRegistry
	manager               *vaultManager
	// Injected only by unit tests. The daemon always uses Linux mounts.
	mountFiles    func(string, string) error
	unmountFiles  func(string) error
	consumerReady time.Time
	lockedCheck   func(brokerEntry) bool // synthetic unit tests only; nil inspects kernel mounts
	publicPins    map[string]*os.File
}

func validateBrokerConfig(c BrokerConfig) error {
	if p := c.ProtectionSync; p != nil {
		if !nodeIDPattern.MatchString(p.Group) || !nodeIDPattern.MatchString(p.Server) || !nodeIDPattern.MatchString(p.Peer) || p.Server == p.Peer {
			return ErrUnsafe
		}
	}
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
	return newMountBrokerWithChecks(c, nil)
}

func newMountBrokerWithChecks(c BrokerConfig, lockedCheck func(brokerEntry) bool) (*mountBroker, error) {
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
	b.unavailableSources = map[string]error{}
	b.lockedCheck = lockedCheck
	b.publicPins = map[string]*os.File{}
	b.mountFiles = func(from, to string) error { return unix.Mount(from, to, "", unix.MS_BIND, "") }
	b.unmountFiles = func(path string) error { return unix.Unmount(path, 0) }
	ok := false
	defer func() {
		if !ok {
			owner.Close()
			for _, f := range b.publicPins {
				f.Close()
			}
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
		if e = json.Unmarshal(data, &b.registry); e != nil || (b.registry.Version != brokerProtocol && b.registry.Version != privateRegistryVersion) {
			return nil, ErrUnsafe
		}
	} else if !errors.Is(e, os.ErrNotExist) {
		return nil, e
	}
	if b.registry.Version == privateRegistryVersion {
		if e = b.recoverPrivateRegistry(); e != nil {
			return nil, e
		}
		if e = b.publish(); e != nil {
			return nil, e
		}
		ok = true
		return b, nil
	}
	if e = b.recoverPublicChanges(); e != nil {
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
		location := entry.Path
		if entry.Trashed > 0 {
			if location != "/"+brokerStore+"/"+entry.ID+".public" {
				return nil, ErrUnsafe
			}
			location = entry.OriginalPath
		}
		previous := b.registry.Entries[:i]
		if entry.Trashed > 0 {
			previous = nil
		}
		if e = b.validateLocation(entry.Source, location, previous); e != nil {
			return nil, e
		}
		if e = b.recoverLegacyEntry(entry); e != nil {
			b.quarantineSource(entry.Source, e)
		}
	}
	if e = b.publish(); e != nil {
		return nil, e
	}
	ok = true
	return b, nil
}

func (b *mountBroker) recoverLegacyEntry(entry brokerEntry) error {
	if _, err := b.root(entry.Source); err != nil {
		return err
	}
	var e error
	if entry.Trashed > 0 {
		v := b.vaultConfig(entry)
		if mounted(v.Mount, "") {
			return ErrVaultBusy
		}
		b.manager.states = append(b.manager.states, &vaultState{config: v})
		return nil
	}
	if mounted(b.publicPath(entry), "filebeans-locked-"+entry.ID) {
		if e = b.unmountFiles(b.publicPath(entry)); e != nil {
			return ErrVaultBusy
		}
	}
	public, e := b.publicDir(entry)
	if e != nil {
		return e
	}
	public.Close()
	v := b.vaultConfig(entry)
	if mounted(v.Mount, "") || mounted(b.publicPath(entry), "") {
		return ErrVaultBusy
	}
	if e = os.MkdirAll(v.Mount, 0500); e != nil {
		return e
	}
	if e = emptyMount(v.Mount); e != nil {
		return e
	}
	b.manager.states = append(b.manager.states, &vaultState{config: v})
	if e = b.installLockedMount(entry); e != nil {
		return e
	}
	return nil
}

func validBrokerID(id string) bool {
	if len(id) != 32 {
		return false
	}
	_, e := hex.DecodeString(id)
	return e == nil && strings.ToLower(id) == id
}

func (b *mountBroker) root(source string) (Root, error) {
	if b.unavailableSources[source] != nil {
		return Root{}, ErrMount
	}
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
		if filesystemHidden(part) || part == "Trash" {
			return ErrUnsafe
		}
	}
	if _, e = b.configuredRoot(source); e != nil {
		return e
	}
	for _, v := range entries {
		if v.Trashed == 0 && v.Source == source && (within(path, v.Path) || within(v.Path, path)) {
			return ErrConflict
		}
	}
	return nil
}

func (b *mountBroker) publicPath(e brokerEntry) string {
	if e.Parked || e.Path == "" {
		return b.markerPath(e)
	}
	r, _ := b.configuredRoot(e.Source)
	return filepath.Join(r.Path, strings.TrimPrefix(e.Path, "/"))
}

func (b *mountBroker) publicDir(entry brokerEntry) (*os.File, error) {
	r, e := b.root(entry.Source)
	if e != nil {
		return nil, e
	}
	relative, _ := filepath.Rel(r.Path, b.publicPath(entry))
	f, e := brokerOpenDir(r.Path, relative)
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
	v := VaultConfig{ID: e.ID, Source: e.Source, Cipher: filepath.Join(r.Path, brokerStore, e.ID),
		Mount: filepath.Join(b.config.Runtime, "mounts", e.ID), Gocryptfs: b.config.Gocryptfs,
		Fusermount: b.config.Fusermount, BackingMount: r.Mount, BackingSource: r.MountSource,
		StartClient: "/bin/true", StopClient: "/bin/true"}
	if r.Mount != "" || b.config.FilesystemConsumer {
		v.forceOwner = fmt.Sprintf("%d:%d", b.config.ReaderUID, b.config.GID)
	}
	return v
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
	value, err := b.registryForStorage()
	if err != nil {
		return err
	}
	return brokerAtomicJSON(filepath.Join(b.config.State, "registry.json"), value, 0600, b.config.GID)
}

func (b *mountBroker) statuses() []BrokerVault {
	b.manager.mu.Lock()
	defer b.manager.mu.Unlock()
	out := make([]BrokerVault, 0, len(b.registry.Entries))
	for _, e := range b.registry.Entries {
		v := BrokerVault{Trashed: e.Trashed, OriginalPath: e.OriginalPath, PreviousPaths: e.PreviousPaths, ID: e.ID, Source: e.Source, Path: e.Path, Name: filepath.Base(e.Path), State: "locked", Initialized: e.Initialized, Device: e.Device, Inode: e.Inode}
		if e.Trashed > 0 {
			v.Name = filepath.Base(e.OriginalPath)
		}
		r, err := b.configuredRoot(e.Source)
		if err != nil || b.unavailableSources[e.Source] != nil || r.Mount != "" && !mounted(r.Mount, r.MountSource) {
			v.State = "error"
			v.Path = ""
			v.Name = ""
			v.OriginalPath = ""
			v.PreviousPaths = nil
			out = append(out, v)
			continue
		}
		if state := b.manager.find(e.ID); state != nil {
			if state.fault {
				v.State = "error"
			} else if state.unlocked && b.manager.isMounted(state.config) {
				v.State = "unlocked"
			} else if state.cmd != nil || mounted(state.config.Mount, "") {
				v.State = "error"
			}
		}
		if e.Parked && e.Trashed == 0 && v.State == "unlocked" {
			v.State = "error"
		}
		publicMounted := mounted(b.publicPath(e), "filebeans-vault-"+e.ID)
		if e.Trashed == 0 && !e.Parked && (v.State == "unlocked" && !publicMounted || v.State == "locked" && !b.isLockedMount(e)) {
			v.State = "error"
		}
		if v.State != "unlocked" {
			v.Path = ""
			v.Name = ""
			v.OriginalPath = ""
			v.PreviousPaths = nil
		}
		out = append(out, v)
	}
	return out
}

func (b *mountBroker) publish() error {
	vaults := b.statuses()
	active := vaults[:0]
	for _, v := range vaults {
		if v.Trashed == 0 && v.State == "unlocked" && v.Path != "" {
			active = append(active, v)
		}
	}
	// If a network outage delays parking, retain a RAM-only deny boundary so
	// no consumer can write plaintext into the temporarily unmounted marker.
	if b.registry.Version == privateRegistryVersion {
		for _, e := range b.registry.Entries {
			if !e.Parked && e.Path != "" && e.Trashed == 0 {
				found := false
				for _, v := range active {
					if v.ID == e.ID {
						found = true
						break
					}
				}
				if !found {
					active = append(active, BrokerVault{ID: e.ID, Source: e.Source, Path: e.Path, State: "locked", Initialized: e.Initialized, Device: e.Device, Inode: e.Inode})
				}
			}
		}
	}
	unavailable := []string{}
	for _, root := range b.config.Roots {
		if _, err := b.root(root.Name); err != nil {
			unavailable = append(unavailable, root.Name)
		}
	}
	return brokerAtomicJSON(filepath.Join(b.config.Runtime, "status.json"), brokerSnapshot{Version: brokerProtocol, Vaults: active, UnavailableSources: unavailable}, 0640, b.config.GID)
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
			if v := b.manager.find(entry.ID); v != nil && !v.unlocked {
				if err := b.installLockedMount(entry); err != nil {
					return brokerEntry{}, err
				}
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
	if b.registry.Version == privateRegistryVersion {
		index := len(b.registry.Entries) - 1
		if e = b.parkPublic(&b.registry.Entries[index]); e != nil {
			return brokerEntry{}, e
		}
		return b.registry.Entries[index], nil
	}
	if e = b.installLockedMount(entry); e != nil {
		return brokerEntry{}, e
	}
	return entry, nil
}

func (b *mountBroker) isLockedMount(entry brokerEntry) bool {
	if b.lockedCheck != nil {
		return b.lockedCheck(entry)
	}
	return mounted(b.publicPath(entry), "filebeans-locked-"+entry.ID)
}

// A read-only kernel mount protects the fallback even when SMB does not honor
// Unix chmod/ownership. The NAS still stores only the ciphertext directory.
func (b *mountBroker) installLockedMount(entry brokerEntry) error {
	if b.isLockedMount(entry) {
		return nil
	}
	if mounted(b.publicPath(entry), "") {
		return ErrVaultBusy
	}
	public := b.publicPins[entry.ID]
	if public == nil {
		r, err := b.configuredRoot(entry.Source)
		if err != nil {
			return err
		}
		var e error
		public, e = brokerOpenDir(r.Path, strings.TrimPrefix(entry.Path, "/"))
		if e != nil {
			return e
		}
		var st unix.Stat_t
		if unix.Fstat(int(public.Fd()), &st) != nil || uint64(st.Dev) != entry.Device || st.Ino != entry.Inode {
			public.Close()
			return ErrConflict
		}
		b.publicPins[entry.ID] = public
	}
	if e := unix.Mount("filebeans-locked-"+entry.ID, brokerFD(public), "tmpfs", unix.MS_RDONLY|unix.MS_NOSUID|unix.MS_NODEV|unix.MS_NOEXEC, "size=4096,mode=0500"); e != nil {
		return errVault
	}
	if !b.isLockedMount(entry) {
		return errVault
	}
	return nil
}

func (b *mountBroker) prepare(entry brokerEntry) error {
	dir, e := b.openPrivateStore(entry.Source)
	if e != nil {
		return e
	}
	defer dir.Close()
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
	if err := b.activateLocations(key); err != nil {
		return err
	}
	for _, candidate := range b.registry.Entries {
		if candidate.ID == entry.ID {
			entry = candidate
			break
		}
	}
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
		if entry.Trashed > 0 || mounted(b.publicPath(entry), "filebeans-vault-"+entry.ID) {
			return nil
		}
		return errVault
	}
	if b.isLockedMount(entry) {
		if e := b.unmountFiles(b.publicPath(entry)); e != nil {
			return ErrVaultBusy
		}
	}
	defer func() {
		if b.registry.Version == brokerProtocol && entry.Trashed == 0 && !mounted(b.publicPath(entry), "filebeans-vault-"+entry.ID) {
			_ = b.installLockedMount(entry)
		}
	}()
	public, e := b.publicDir(entry)
	if e != nil {
		return e
	}
	defer func() { public.Close() }()
	if mounted(b.publicPath(entry), "") && !b.isLockedMount(entry) {
		return ErrVaultBusy
	}
	if e = b.prepare(entry); e != nil {
		return e
	}
	r, _ := b.configuredRoot(entry.Source)
	cipherFD, e := brokerOpenDir(r.Path, filepath.Join(brokerStore, entry.ID))
	if e != nil {
		return e
	}
	defer cipherFD.Close()
	v.config.cipherDirectory = cipherFD
	defer func() { v.config.cipherDirectory = nil }()
	_, confErr := os.Stat(filepath.Join(vaultCipherDirectory(v.config), "gocryptfs.conf"))
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
	for i := range b.registry.Entries {
		if b.registry.Entries[i].ID == entry.ID && !b.registry.Entries[i].Initialized {
			b.registry.Entries[i].Initialized = true
			if e = b.saveRegistry(); e != nil {
				b.registry.Entries[i].Initialized = false
				rollback()
				return e
			}
		}
	}
	for _, relative := range []string{"files", "private", "private/cache", "private/tmp"} {
		dir, err := brokerOpenDir(v.config.Mount, relative)
		if err != nil {
			rollback()
			return err
		}
		if v.config.forceOwner == "" {
			e = unix.Fchownat(int(dir.Fd()), "", int(b.config.ReaderUID), b.config.GID, unix.AT_EMPTY_PATH|unix.AT_SYMLINK_NOFOLLOW)
		}
		if e == nil {
			e = os.Chmod(brokerFD(dir), 0770)
		}
		if v.config.forceOwner != "" {
			var st unix.Stat_t
			if unix.Fstat(int(dir.Fd()), &st) == nil && st.Mode&0770 == 0770 {
				e = nil
			}
		}
		dir.Close()
		if e != nil {
			rollback()
			return e
		}
	}
	if v.config.forceOwner == "" {
		if e = os.Chown(v.config.Mount, -1, b.config.GID); e != nil {
			rollback()
			return e
		}
		if e = os.Chmod(v.config.Mount, 0710); e != nil {
			rollback()
			return e
		}
	}
	if entry.Trashed == 0 {
		for i := range b.registry.Entries {
			if b.registry.Entries[i].ID == entry.ID {
				if e = b.unparkPublic(&b.registry.Entries[i]); e != nil {
					rollback()
					return e
				}
				entry = b.registry.Entries[i]
				break
			}
		}
		public.Close()
		public, e = b.publicDir(entry)
		if e != nil {
			rollback()
			return e
		}
		if e = b.mountFiles(filepath.Join(v.config.Mount, "files"), brokerFD(public)); e != nil {
			rollback()
			return fmt.Errorf("%w: public bind: %v", errVault, e)
		}
	}
	if e = b.publish(); e != nil {
		_ = b.unmountFiles(b.publicPath(entry))
		rollback()
		return errVault
	}
	return nil
}

func (b *mountBroker) lock(entry brokerEntry) error {
	for _, current := range b.registry.Entries {
		if current.ID == entry.ID {
			entry = current
			break
		}
	}
	// Locking must remain possible when remote ciphertext storage goes offline.
	// Kernel mount identities, not backing-store availability, govern unmounting.
	if _, e := b.configuredRoot(entry.Source); e != nil {
		return e
	}
	if mounted(b.publicPath(entry), "") && !b.isLockedMount(entry) {
		if !mounted(b.publicPath(entry), "filebeans-vault-"+entry.ID) {
			return ErrUnsafe
		}
		if e := b.unmountFiles(b.publicPath(entry)); e != nil {
			return ErrVaultBusy
		}
	}
	var parkErr error
	if b.registry.Version == privateRegistryVersion {
		for i := range b.registry.Entries {
			if b.registry.Entries[i].ID == entry.ID {
				if b.registry.Entries[i].Path == "" && !b.registry.Entries[i].Parked {
					parkErr = b.recoverClosedMarker(&b.registry.Entries[i])
				} else {
					parkErr = b.parkPublic(&b.registry.Entries[i])
				}
				entry = b.registry.Entries[i]
				break
			}
		}
	}
	// Closing the private mount must still be attempted if storage goes
	// offline before the empty public marker can be parked.
	if err := b.manager.lock(entry.ID); err != nil {
		_ = b.publish()
		return err
	}
	if parkErr != nil {
		_ = b.publish()
		b.forgetClosedLocations()
		return parkErr
	}
	if b.registry.Version == brokerProtocol && entry.Trashed == 0 {
		if e := b.installLockedMount(entry); e != nil {
			_ = b.publish()
			return e
		}
	}
	err := b.publish()
	b.forgetClosedLocations()
	return err
}

func (b *mountBroker) close() {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, entry := range b.registry.Entries {
		_ = b.lock(entry)
	}
	b.manager.close()
	for id, f := range b.publicPins {
		f.Close()
		delete(b.publicPins, id)
	}
}

// Retry quarantined sources independently; availability alone never overrides
// the configured share and marker identities.
func (b *mountBroker) quarantineSource(source string, err error) {
	if b.unavailableSources == nil {
		b.unavailableSources = map[string]error{}
	}
	if b.unavailableSources[source] != nil {
		return
	}
	b.unavailableSources[source] = err
	log.Printf("Storage source %q unavailable; identity checks remain enforced", source)
}
