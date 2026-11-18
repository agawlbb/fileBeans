package service

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

// The journal moves only the empty public mount directory. Ciphertext remains
// in its original store, with its vault identity and recovery format unchanged.
func (b *mountBroker) movePublic(entry *brokerEntry) error {
	root, err := b.root(entry.Source)
	if err != nil {
		return err
	}
	old, err := brokerOpenDir(root.Path, filepath.Clean(strings.TrimPrefix(filepath.Dir(entry.Path), "/")))
	if err != nil {
		return err
	}
	defer old.Close()
	next, err := brokerOpenDir(root.Path, filepath.Clean(strings.TrimPrefix(filepath.Dir(entry.NextPath), "/")))
	if err != nil {
		return err
	}
	defer next.Close()
	// Recovery recognizes a completed rename by the persisted directory identity.
	oldDir, oldErr := brokerOpenAt(int(old.Fd()), filepath.Base(entry.Path))
	if oldErr == nil {
		oldDir.Close()
	}
	nextDir, nextErr := brokerOpenAt(int(next.Fd()), filepath.Base(entry.NextPath))
	if nextErr == nil {
		defer nextDir.Close()
		var st unix.Stat_t
		if oldErr == nil || unix.Fstat(int(nextDir.Fd()), &st) != nil || st.Ino != entry.Inode || uint64(st.Dev) != entry.Device {
			return ErrConflict
		}
	} else {
		if oldErr != nil {
			return ErrConflict
		}
		public, err := b.publicDir(*entry)
		if err != nil {
			return err
		}
		public.Close()
		if err = unix.Renameat2(int(old.Fd()), filepath.Base(entry.Path), int(next.Fd()), filepath.Base(entry.NextPath), unix.RENAME_NOREPLACE); err != nil {
			return err
		}
	}
	for _, parent := range []*os.File{old, next} {
		syncDir, err := os.Open(brokerFD(parent))
		if err != nil {
			return err
		}
		err = syncDir.Sync()
		syncDir.Close()
		if err != nil {
			return err
		}
	}
	if entry.Trashed == 0 {
		entry.PreviousPaths = append(entry.PreviousPaths, entry.Path)
	}
	entry.Path, entry.Trashed = entry.NextPath, entry.NextTrashed
	if b.registry.Version == privateRegistryVersion {
		entry.Parked = entry.Trashed > 0
	}
	entry.NextPath, entry.NextTrashed = "", 0
	return nil
}
func (b *mountBroker) recoverPublicChanges() error {
	changed := false
	kept := b.registry.Entries[:0]
	for _, entry := range b.registry.Entries {
		if entry.Purging {
			if err := b.purgeVaultFiles(entry); err != nil {
				return err
			}
			changed = true
		} else {
			kept = append(kept, entry)
		}
	}
	b.registry.Entries = kept
	for i := range b.registry.Entries {
		e := &b.registry.Entries[i]
		if !validBrokerID(e.ID) {
			return ErrUnsafe
		}
		if e.NextPath != "" {
			if e.NextTrashed > 0 {
				if e.NextPath != "/"+brokerStore+"/"+e.ID+".public" {
					return ErrUnsafe
				}
			} else if err := b.validateLocation(e.Source, e.NextPath, nil); err != nil {
				return err
			}
			if err := b.movePublic(e); err != nil {
				return err
			}
			changed = true
		}
	}
	if changed {
		return b.saveRegistry()
	}
	return nil
}
func (b *mountBroker) folderAction(id, action, path string) error {
	var entry *brokerEntry
	for i := range b.registry.Entries {
		if b.registry.Entries[i].ID == id {
			entry = &b.registry.Entries[i]
			break
		}
	}
	if entry == nil {
		return ErrUnsafe
	}
	state := b.manager.find(id)
	if state == nil {
		return ErrUnsafe
	}
	if action == "purge" {
		if entry.Trashed == 0 {
			return ErrUnsafe
		}
		if err := b.lock(*entry); err != nil {
			return err
		}
		entry.Purging = true
		if err := b.saveRegistry(); err != nil {
			return err
		}
		if err := b.purgeVaultFiles(*entry); err != nil {
			return err
		}
		var err error
		for i, e := range b.registry.Entries {
			if e.ID == id {
				b.registry.Entries = append(b.registry.Entries[:i], b.registry.Entries[i+1:]...)
				break
			}
		}
		for i, s := range b.manager.states {
			if s.config.ID == id {
				b.manager.states = append(b.manager.states[:i], b.manager.states[i+1:]...)
				break
			}
		}
		if err = b.saveRegistry(); err != nil {
			return err
		}
		return b.publish()
	}
	if !state.unlocked {
		return ErrVaultLocked
	}
	nextTrashed := int64(0)
	if action == "trash" {
		if entry.Trashed > 0 {
			return nil
		}
		path = "/" + brokerStore + "/" + entry.ID + ".public"
		nextTrashed = time.Now().Unix()
		entry.OriginalPath = entry.Path
	} else if action == "restore" {
		if entry.Trashed == 0 {
			return ErrConflict
		}
		path = entry.OriginalPath
	} else if action != "relocate" || entry.Trashed > 0 {
		return ErrUnsafe
	}
	if path == entry.Path {
		return nil
	}
	if nextTrashed == 0 {
		if within(path, entry.Path) {
			return ErrUnsafe
		}
		others := []brokerEntry{}
		for _, v := range b.registry.Entries {
			if v.ID != id {
				others = append(others, v)
			}
		}
		if err := b.validateLocation(entry.Source, path, others); err != nil {
			return err
		}
	}
	// Reject an existing destination before unmounting or journaling anything.
	root, err := b.root(entry.Source)
	if err != nil {
		return err
	}
	parent, err := brokerOpenDir(root.Path, filepath.Clean(strings.TrimPrefix(filepath.Dir(path), "/")))
	if err != nil {
		return err
	}
	defer parent.Close()
	base, err := brokerOpenDir(root.Path, ".")
	if err != nil {
		return err
	}
	var baseStat, parentStat unix.Stat_t
	statErr := unix.Fstat(int(base.Fd()), &baseStat)
	base.Close()
	if statErr != nil || unix.Fstat(int(parent.Fd()), &parentStat) != nil || baseStat.Dev != parentStat.Dev {
		return ErrUnsafe
	}
	var st unix.Stat_t
	if err = unix.Fstatat(int(parent.Fd()), filepath.Base(path), &st, unix.AT_SYMLINK_NOFOLLOW); err == nil {
		return ErrConflict
	}
	if !errors.Is(err, unix.ENOENT) {
		return err
	}
	wasPublic := entry.Trashed == 0
	if wasPublic {
		if err = b.unmountFiles(b.publicPath(*entry)); err != nil {
			return ErrVaultBusy
		}
	}
	entry.NextPath, entry.NextTrashed = path, nextTrashed
	if err = b.saveRegistry(); err != nil {
		entry.NextPath = ""
		entry.NextTrashed = 0
		if wasPublic {
			_ = b.mountFiles(filepath.Join(state.config.Mount, "files"), b.publicPath(*entry))
		}
		return err
	}
	if err = b.movePublic(entry); err != nil {
		// A refused rename leaves the original directory in place. Restore its
		// public binding; an already moved marker retains its recovery journal.
		if original, checkErr := b.publicDir(*entry); checkErr == nil {
			original.Close()
			entry.NextPath, entry.NextTrashed = "", 0
			_ = b.saveRegistry()
			if wasPublic {
				_ = b.mountFiles(filepath.Join(state.config.Mount, "files"), b.publicPath(*entry))
			}
		}
		return err
	} // durable journal resumes on restart
	if err = b.saveRegistry(); err != nil {
		return err
	}
	if entry.Trashed == 0 {
		public, err := b.publicDir(*entry)
		if err != nil {
			return err
		}
		defer public.Close()
		if err = b.mountFiles(filepath.Join(state.config.Mount, "files"), brokerFD(public)); err != nil {
			return err
		}
	}
	return b.publish()
}

func (b *mountBroker) purgeVaultFiles(entry brokerEntry) error {
	if !validBrokerID(entry.ID) || entry.Trashed == 0 {
		return ErrUnsafe
	}
	if mounted(b.vaultConfig(entry).Mount, "") {
		return ErrVaultBusy
	}
	root, err := b.root(entry.Source)
	if err != nil {
		return err
	}
	store, err := os.OpenRoot(filepath.Join(root.Path, brokerStore))
	if err != nil {
		return err
	}
	defer store.Close()
	if err = safe(store, entry.ID); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err = store.RemoveAll(entry.ID); err != nil {
		return err
	}
	if err = store.Remove(entry.ID + ".public"); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if pin := b.publicPins[entry.ID]; pin != nil {
		pin.Close()
		delete(b.publicPins, entry.ID)
	}
	return nil
}
