package service

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

const privateRegistryVersion = 2

type sealedLocation struct {
	Nonce []byte `json:"nonce"`
	Data  []byte `json:"data"`
}
type vaultLocation struct {
	Path          string   `json:"path"`
	OriginalPath  string   `json:"originalPath,omitempty"`
	PreviousPaths []string `json:"previousPaths,omitempty"`
	NextPath      string   `json:"nextPath,omitempty"`
}

func locationCipher(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}
func (b *mountBroker) markerPath(e brokerEntry) string {
	root, _ := b.configuredRoot(e.Source)
	return filepath.Join(root.Path, brokerStore, e.ID+".public")
}
func locationAAD(e brokerEntry) []byte {
	return []byte("fileBeans folder location v1\x00" + e.ID + "\x00" + e.Source)
}
func clearLocation(e *brokerEntry) {
	e.Path = ""
	e.OriginalPath = ""
	e.PreviousPaths = nil
	e.NextPath = ""
}
func (b *mountBroker) activateLocations(master []byte) error {
	if len(master) != 32 {
		return ErrVaultKey
	}
	if err := b.keyMatches(master); err != nil {
		return err
	}
	mac := hmac.New(sha256.New, master)
	mac.Write([]byte("fileBeans folder locations v1"))
	derived := mac.Sum(nil)
	defer clear(derived)
	a, err := locationCipher(derived)
	if err != nil {
		return err
	}
	// Authenticate every location before changing the live registry.
	entries := append([]brokerEntry(nil), b.registry.Entries...)
	for i := range entries {
		e := &entries[i]
		if e.Location == nil {
			if e.Path == "" {
				return ErrUnsafe
			}
			continue
		}
		if len(e.Location.Nonce) != a.NonceSize() || len(e.Location.Data) < a.Overhead() {
			return ErrUnsafe
		}
		plain, err := a.Open(nil, e.Location.Nonce, e.Location.Data, locationAAD(*e))
		if err != nil {
			return ErrVaultKey
		}
		var value vaultLocation
		err = json.Unmarshal(plain, &value)
		clear(plain)
		if err != nil {
			return ErrUnsafe
		}
		e.Path, e.OriginalPath, e.PreviousPaths, e.NextPath = value.Path, value.OriginalPath, value.PreviousPaths, value.NextPath
	}
	clear(b.locationKey)
	b.locationKey = append([]byte(nil), derived...)
	b.registry.Entries = entries
	for i := range b.registry.Entries {
		e := &b.registry.Entries[i]
		if e.NextPath != "" && e.Parked {
			if e.NextTrashed > 0 {
				if e.NextPath != "/"+brokerStore+"/"+e.ID+".public" {
					return ErrUnsafe
				}
			} else if err := b.validateLocation(e.Source, e.NextPath, nil); err != nil {
				return err
			}
			if e.Trashed == 0 {
				e.PreviousPaths = append(e.PreviousPaths, e.Path)
			}
			e.Path, e.Trashed = e.NextPath, e.NextTrashed
			e.NextPath = ""
			e.NextTrashed = 0
		}
		location := e.Path
		if e.Trashed > 0 {
			if e.Path != "/"+brokerStore+"/"+e.ID+".public" {
				return ErrUnsafe
			}
			location = e.OriginalPath
		}
		for _, old := range e.PreviousPaths {
			if err := b.validateLocation(e.Source, old, nil); err != nil {
				return err
			}
		}
		if err := b.validateLocation(e.Source, location, nil); err != nil {
			return err
		}
	}
	if b.registry.Version == brokerProtocol {
		// Journal encrypted locations before removing legacy named placeholders.
		b.registry.Version = privateRegistryVersion
		if err = b.saveRegistry(); err != nil {
			return err
		}
		for i := range b.registry.Entries {
			e := &b.registry.Entries[i]
			if b.isLockedMount(*e) && mounted(b.publicPath(*e), "filebeans-locked-"+e.ID) {
				if err = b.unmountFiles(b.publicPath(*e)); err != nil {
					return ErrVaultBusy
				}
			}
			if state := b.manager.find(e.ID); state == nil || !state.unlocked {
				if err = b.parkPublic(e); err != nil {
					return err
				}
			}
		}
	}
	return b.sealLocationBackups()
}
func (b *mountBroker) registryForStorage() (brokerRegistry, error) {
	out := brokerRegistry{Version: b.registry.Version, Entries: append([]brokerEntry(nil), b.registry.Entries...)}
	if out.Version != privateRegistryVersion {
		return out, nil
	}
	var a cipher.AEAD
	var err error
	if len(b.locationKey) > 0 {
		a, err = locationCipher(b.locationKey)
		if err != nil {
			return out, err
		}
	}
	for i := range out.Entries {
		e := &out.Entries[i]
		if e.Path != "" {
			if a == nil {
				return out, ErrVaultLocked
			}
			value := vaultLocation{e.Path, e.OriginalPath, e.PreviousPaths, e.NextPath}
			plain, err := json.Marshal(value)
			if err != nil {
				return out, err
			}

			changed := true
			if e.Location != nil {
				if len(e.Location.Nonce) != a.NonceSize() {
					clear(plain)
					return out, ErrUnsafe
				}
				prior, err := a.Open(nil, e.Location.Nonce, e.Location.Data, locationAAD(*e))
				if err != nil {
					clear(plain)
					return out, ErrUnsafe
				}
				changed = !bytes.Equal(prior, plain)
				clear(prior)
			}
			if changed {
				nonce := make([]byte, a.NonceSize())
				if _, err = rand.Read(nonce); err != nil {
					clear(plain)
					return out, err
				}
				e.Location = &sealedLocation{nonce, a.Seal(nil, nonce, plain, locationAAD(*e))}
			}
			clear(plain)
			if state := b.manager.find(e.ID); state != nil && state.unlocked && (changed || !b.privateLocationsSaved[e.ID]) {
				if err := brokerAtomicJSON(filepath.Join(state.config.Mount, "private", "location.json"), value, 0600, b.config.GID); err != nil {
					return out, err
				}
				if b.privateLocationsSaved == nil {
					b.privateLocationsSaved = map[string]bool{}
				}
				b.privateLocationsSaved[e.ID] = true
			}
			b.registry.Entries[i].Location = e.Location
		}
		if e.Location == nil {
			return out, ErrUnsafe
		}
		clearLocation(e)
	}
	return out, nil
}
func (b *mountBroker) renameMarker(e brokerEntry, from, to string) error {
	root, err := b.configuredRoot(e.Source)
	if err != nil {
		return err
	}
	var sourceStat unix.Stat_t
	if unix.Stat(root.Path, &sourceStat) != nil || uint64(sourceStat.Dev) != e.Device {
		return ErrMount
	}
	base, err := os.OpenRoot(root.Path)
	if err != nil {
		return err
	}
	defer base.Close()
	if err = safe(base, filepath.Dir(to)); err != nil {
		return err
	}
	if err = base.MkdirAll(filepath.Dir(to), 0770); err != nil {
		return err
	}
	old, err := brokerOpenDir(root.Path, filepath.Clean(filepath.Dir(from)))
	if err != nil {
		return err
	}
	defer old.Close()
	next, err := brokerOpenDir(root.Path, filepath.Clean(filepath.Dir(to)))
	if err != nil {
		return err
	}
	defer next.Close()
	fd, err := brokerOpenAt(int(old.Fd()), filepath.Base(from))
	if err != nil {
		return err
	}
	var st unix.Stat_t
	err = unix.Fstat(int(fd.Fd()), &st)
	fd.Close()
	if err != nil || uint64(st.Dev) != e.Device || st.Ino != e.Inode {
		return ErrConflict
	}
	if err = unix.Renameat2(int(old.Fd()), filepath.Base(from), int(next.Fd()), filepath.Base(to), unix.RENAME_NOREPLACE); err != nil {
		return err
	}
	for _, parent := range []*os.File{old, next} {
		f, err := os.Open(brokerFD(parent))
		if err != nil {
			return err
		}
		err = f.Sync()
		f.Close()
		if err != nil {
			return err
		}
	}
	return nil
}
func (b *mountBroker) parkPublic(e *brokerEntry) error {
	if e.Parked {
		return nil
	}
	if e.Trashed > 0 {
		e.Parked = true
		return b.saveRegistry()
	}
	root, err := b.configuredRoot(e.Source)
	if err != nil {
		return err
	}
	from, err := filepath.Rel(root.Path, b.publicPath(*e))
	if err != nil {
		return err
	}
	to := filepath.Join(brokerStore, e.ID+".public")
	e.Parking = true
	if err = b.saveRegistry(); err != nil {
		return err
	}
	if err = b.renameMarker(*e, from, to); err != nil {
		return err
	}
	e.Parked = true
	e.Parking = false
	return b.saveRegistry()
}
func (b *mountBroker) unparkPublic(e *brokerEntry) error {
	if !e.Parked || e.Trashed > 0 {
		return nil
	}
	if e.Path == "" {
		return ErrVaultLocked
	}
	from := filepath.Join(brokerStore, e.ID+".public")
	to := strings.TrimPrefix(e.Path, "/")
	// Keep Parked=false durable before exposing the named marker. Startup uses
	// its inode identity to return it to opaque storage if the process dies.
	e.Parked = false
	e.Parking = true
	if err := b.saveRegistry(); err != nil {
		return err
	}
	if err := b.renameMarker(*e, from, to); err != nil {
		e.Parked = true
		e.Parking = false
		_ = b.saveRegistry()
		return err
	}
	e.Parking = false
	return b.saveRegistry()
}

func (b *mountBroker) recoverClosedMarker(e *brokerEntry) error {
	root, err := b.configuredRoot(e.Source)
	if err != nil {
		return err
	}
	if mounted(b.vaultConfig(*e).Mount, "") {
		return ErrVaultBusy
	}
	var baseStat unix.Stat_t
	if unix.Stat(root.Path, &baseStat) != nil || uint64(baseStat.Dev) != e.Device {
		return ErrMount
	}
	marker := b.markerPath(*e)
	var st unix.Stat_t
	if unix.Lstat(marker, &st) == nil {
		if uint64(st.Dev) != e.Device || st.Ino != e.Inode {
			return ErrConflict
		}
	} else {
		found := ""
		err = filepath.WalkDir(root.Path, func(path string, d fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if !d.IsDir() {
				return nil
			}
			if path == filepath.Join(root.Path, brokerStore) {
				return filepath.SkipDir
			}
			var value unix.Stat_t
			if unix.Lstat(path, &value) != nil {
				return ErrUnsafe
			}
			if uint64(value.Dev) == e.Device && value.Ino == e.Inode {
				found = path
				return fs.SkipAll
			}
			if value.Dev != baseStat.Dev {
				return filepath.SkipDir
			}
			return nil
		})
		if err != nil && !errors.Is(err, fs.SkipAll) {
			return err
		}
		if found == "" {
			return ErrConflict
		}
		from, err := filepath.Rel(root.Path, found)
		if err != nil {
			return err
		}
		if err = b.renameMarker(*e, from, filepath.Join(brokerStore, e.ID+".public")); err != nil {
			return err
		}
	}
	e.Parked = true
	e.Parking = false
	return nil
}
func (b *mountBroker) recoverPrivateRegistry() error {
	kept := b.registry.Entries[:0]
	for _, e := range b.registry.Entries {
		if e.Purging {
			if err := b.purgeVaultFiles(e); err != nil {
				return err
			}
		} else {
			kept = append(kept, e)
		}
	}
	b.registry.Entries = kept
	ids, requests := map[string]bool{}, map[string]bool{}
	for i := range b.registry.Entries {
		e := &b.registry.Entries[i]
		if !validBrokerID(e.ID) || len(e.RequestHash) != 64 || ids[e.ID] || requests[e.RequestHash] || e.Inode == 0 {
			return ErrUnsafe
		}
		ids[e.ID] = true
		requests[e.RequestHash] = true
		if err := b.recoverClosedMarker(e); err != nil {
			return err
		}
		clearLocation(e)
		b.manager.states = append(b.manager.states, &vaultState{config: b.vaultConfig(*e)})
	}
	return b.saveRegistry()
}
func (b *mountBroker) forgetClosedLocations() {
	for _, s := range b.manager.states {
		if s.unlocked || mounted(s.config.Mount, "") {
			return
		}
	}
	if b.registry.Version == privateRegistryVersion {
		for _, entry := range b.registry.Entries {
			if !entry.Parked {
				return
			}
		}
	}
	clear(b.locationKey)
	b.locationKey = nil
	if b.registry.Version == privateRegistryVersion {
		for i := range b.registry.Entries {
			clearLocation(&b.registry.Entries[i])
		}
	}
}

// Preserve explicitly configured historical snapshots, but encrypt their
// sensitive fields with the same recoverable, domain-separated location key.
func (b *mountBroker) sealLocationBackups() error {
	for _, path := range b.config.LocationBackups {
		if err := brokerTrustedExecutable(path, false); err != nil {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		var previous brokerRegistry
		err = json.Unmarshal(data, &previous)
		clear(data)
		if err != nil {
			return ErrUnsafe
		}
		if previous.Version == privateRegistryVersion {
			continue
		}
		if previous.Version != brokerProtocol {
			return ErrUnsafe
		}
		for _, entry := range previous.Entries {
			if !validBrokerID(entry.ID) || entry.Source == "" || entry.Path == "" {
				return ErrUnsafe
			}
		}
		previous.Version = privateRegistryVersion
		snapshot := &mountBroker{registry: previous, locationKey: b.locationKey, manager: &vaultManager{}, config: b.config}
		sealed, err := snapshot.registryForStorage()
		if err != nil {
			return err
		}
		if err = brokerAtomicJSON(path, sealed, 0600, b.config.GID); err != nil {
			return err
		}
	}
	return nil
}
