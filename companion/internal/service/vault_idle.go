package service

import (
	"context"
	"errors"
	"io/fs"
	"log"
	"path/filepath"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"
)

const vaultIdleTimeout = time.Hour

// Watch public decrypted files. Directory polling and private cache writes
// must not extend the inactivity period.
type vaultIdleWatch struct {
	fd    int
	last  time.Time
	dirty bool
}

func newVaultIdleWatch(now time.Time) (*vaultIdleWatch, error) {
	fd, err := unix.InotifyInit1(unix.IN_NONBLOCK | unix.IN_CLOEXEC)
	if err != nil {
		return nil, err
	}
	return &vaultIdleWatch{fd: fd, last: now, dirty: true}, nil
}
func (w *vaultIdleWatch) close() { _ = unix.Close(w.fd) }
func (w *vaultIdleWatch) scan(root string) error {
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() {
			return nil
		}
		_, err = unix.InotifyAddWatch(w.fd, path, unix.IN_ACCESS|unix.IN_OPEN|
			unix.IN_MODIFY|unix.IN_ATTRIB|unix.IN_CLOSE_WRITE|unix.IN_CREATE|
			unix.IN_DELETE|unix.IN_MOVED_FROM|unix.IN_MOVED_TO|unix.IN_DELETE_SELF|
			unix.IN_MOVE_SELF|unix.IN_ONLYDIR|unix.IN_DONT_FOLLOW)
		return err
	})
	w.dirty = err != nil
	return err
}
func (w *vaultIdleWatch) drain(now time.Time) error {
	var buf [64 * 1024]byte
	for {
		n, err := unix.Read(w.fd, buf[:])
		if errors.Is(err, unix.EAGAIN) {
			return nil
		}
		if err != nil {
			return err
		}
		if n == 0 {
			return nil
		}
		for offset := 0; offset+unix.SizeofInotifyEvent <= n; {
			event := (*unix.InotifyEvent)(unsafe.Pointer(&buf[offset]))
			mask := event.Mask
			if mask&(unix.IN_Q_OVERFLOW|unix.IN_IGNORED) != 0 {
				w.dirty = true
				w.last = now // Lost events require a fresh complete idle interval.
			}
			if mask&(unix.IN_MODIFY|unix.IN_ATTRIB|unix.IN_CLOSE_WRITE|
				unix.IN_CREATE|unix.IN_DELETE|unix.IN_MOVED_FROM|unix.IN_MOVED_TO|
				unix.IN_DELETE_SELF|unix.IN_MOVE_SELF) != 0 ||
				mask&unix.IN_ISDIR == 0 && mask&(unix.IN_ACCESS|unix.IN_OPEN) != 0 {
				w.last = now
			}
			if mask&unix.IN_ISDIR != 0 && mask&(unix.IN_CREATE|unix.IN_MOVED_TO|
				unix.IN_MOVED_FROM|unix.IN_DELETE) != 0 {
				w.dirty = true
			}
			offset += unix.SizeofInotifyEvent + int(event.Len)
		}
	}
}
func (w *vaultIdleWatch) expired(now time.Time) bool {
	return now.Sub(w.last) >= vaultIdleTimeout
}

// Called under the broker mutex, just like manual mount transitions.
func (b *mountBroker) pollIdleVaults(watches map[string]*vaultIdleWatch, now time.Time) {
	open := map[string]bool{}
	for _, v := range b.registry.Entries {
		state := b.manager.find(v.ID)
		if state == nil || (!state.unlocked && !mounted(state.config.Mount, "")) {
			continue
		}
		open[v.ID] = true
		w := watches[v.ID]
		if w == nil {
			var err error
			w, err = newVaultIdleWatch(now)
			if err != nil {
				// Resource exhaustion must not disable the lock deadline.
				w = &vaultIdleWatch{fd: -1, last: now, dirty: true}
			}
			watches[v.ID] = w
		}
		var entry brokerEntry
		for _, candidate := range b.registry.Entries {
			if candidate.ID == v.ID {
				entry = candidate
				break
			}
		}
		err := w.drain(now)
		if err == nil && w.dirty {
			watchPath := b.publicPath(entry)
			if entry.Trashed > 0 {
				watchPath = filepath.Join(b.vaultConfig(entry).Mount, "files")
			}
			err = w.scan(watchPath)
		}
		if err == nil {
			_ = w.drain(now)
		}
	}
	// Protection is one session: activity in any protected folder keeps all
	// folders open. An idle session closes all of them under a single gate.
	latest := b.datasetActivity
	for id, w := range watches {
		if open[id] && w.last.After(latest) {
			latest = w.last
		}
	}
	if len(open) > 0 && !latest.IsZero() && now.Sub(latest) >= vaultIdleTimeout {
		if gate, err := b.exclusive(); err == nil {
			err = b.lockAll()
			gate.Close()
			if err == nil {
				for id, w := range watches {
					w.close()
					delete(watches, id)
				}
			}
		}
	}
	if len(open) == 0 && b.registry.Version == privateRegistryVersion {
		pending := false
		for _, e := range b.registry.Entries {
			pending = pending || !e.Parked
		}
		if pending {
			if gate, err := b.exclusive(); err == nil {
				_ = b.lockAll()
				gate.Close()
			}
		}
	}

	for id, w := range watches {
		if !open[id] {
			w.close()
			delete(watches, id)
		}
	}
}

func (b *mountBroker) autoLockVaults(ctx context.Context) {
	log.Print("Protected folder auto-lock enabled: 1h idle")
	watches := map[string]*vaultIdleWatch{}
	defer func() {
		for _, w := range watches {
			w.close()
		}
	}()
	for ctx.Err() == nil {
		b.mu.Lock()
		b.pollIdleVaults(watches, time.Now())
		b.mu.Unlock()
		wait(ctx, 5*time.Second)
	}
}
