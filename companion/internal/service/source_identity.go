package service

import (
	"path/filepath"

	"golang.org/x/sys/unix"
)

// Retry storage that was absent during startup. This runs under the same gate
// as mount transitions, so a returning share never requires an app restart.
func (b *mountBroker) retryUnavailableSources() {
	if len(b.unavailableSources) == 0 || b.registry.Version != privateRegistryVersion {
		return
	}
	gate, err := b.exclusive()
	if err != nil {
		return
	}
	defer gate.Close()
	sources := make([]string, 0, len(b.unavailableSources))
	for source := range b.unavailableSources {
		sources = append(sources, source)
	}
	recovered := false
	for _, source := range sources {
		previous := b.unavailableSources[source]
		delete(b.unavailableSources, source)
		root, err := b.root(source)
		if err == nil && root.Mount != "" {
			if mountedFilesystem(root.Mount, root.MountSource, "cifs") {
				err = b.reconcileCIFSDevices(root)
			}
		}
		if err == nil {
			for i := range b.registry.Entries {
				entry := &b.registry.Entries[i]
				if entry.Source != source {
					continue
				}
				state := b.manager.find(entry.ID)
				if entry.Purging || state == nil || state.unlocked || state.cmd != nil {
					err = ErrVaultBusy
					break
				}
				if err = b.recoverClosedMarker(entry); err != nil {
					break
				}
				clearLocation(entry)
				state.fault = false
			}
		}
		if err == nil {
			err = b.saveRegistry()
		}
		if err != nil {
			b.unavailableSources[source] = previous
		} else {
			recovered = true
		}
	}
	if recovered {
		_ = b.publish()
	}
}

// Linux assigns CIFS a new device number after remount/reboot. It is not a
// persistent share identity. Reconcile it only against the configured kernel
// mount and every durable marker inode, before performing any recovery writes.
func (b *mountBroker) reconcileSourceDevices() {
	for _, root := range b.config.Roots {
		if root.Mount == "" {
			continue
		}
		if _, err := b.root(root.Name); err != nil {
			b.quarantineSource(root.Name, err)
			continue
		}
		if !mountedFilesystem(root.Mount, root.MountSource, "cifs") {
			continue
		}
		if err := b.reconcileCIFSDevices(root); err != nil {
			b.quarantineSource(root.Name, err)
		}
	}
}

func (b *mountBroker) reconcileCIFSDevices(root Root) error {
	var base unix.Stat_t
	if err := unix.Stat(root.Path, &base); err != nil {
		return err
	}
	changed := false
	for _, entry := range b.registry.Entries {
		if entry.Source != root.Name || entry.Device == uint64(base.Dev) {
			continue
		}
		changed = true
		// Only opaque parked markers can be reconciled without decrypting names
		// or guessing which directory represents a private location.
		if !entry.Parked || entry.Parking || entry.Purging || mounted(b.vaultConfig(entry).Mount, "") {
			return ErrConflict
		}
	}
	if !changed {
		return nil
	}
	// Validate the entire source first; never accept a partial marker match.
	for _, entry := range b.registry.Entries {
		if entry.Source != root.Name {
			continue
		}
		if !entry.Parked || entry.Parking || entry.Purging {
			return ErrConflict
		}
		marker, err := brokerOpenDir(root.Path, filepath.Join(brokerStore, entry.ID+".public"))
		if err != nil {
			return err
		}
		var st unix.Stat_t
		check := unix.Fstat(int(marker.Fd()), &st)
		marker.Close()
		if check != nil || st.Mode&unix.S_IFMT != unix.S_IFDIR || st.Dev != base.Dev || st.Ino != entry.Inode {
			return ErrConflict
		}
	}
	for i := range b.registry.Entries {
		if b.registry.Entries[i].Source == root.Name {
			b.registry.Entries[i].Device = uint64(base.Dev)
		}
	}
	return nil
}
