package service

import (
	"context"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/sys/unix"
)

// The API and normalization worker share a grant. A process-local mutex alone
// cannot prevent a token refresh from overwriting a concurrent account switch.
func (s *Service) lockGoogle(ctx context.Context) (func(), error) {
	s.driveMu.Lock()
	unlock, e := lockPrivateFile(ctx, filepath.Join(s.config.State, ".google.lock"), unix.LOCK_EX)
	if e != nil {
		s.driveMu.Unlock()
		return nil, e
	}
	return func() { unlock(); s.driveMu.Unlock() }, nil
}

func lockPrivateFile(ctx context.Context, path string, mode int) (func(), error) {
	file, e := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if e != nil {
		return nil, e
	}
	for {
		e = unix.Flock(int(file.Fd()), mode|unix.LOCK_NB)
		if e == nil {
			return func() { unix.Flock(int(file.Fd()), unix.LOCK_UN); file.Close() }, nil
		}
		if e != unix.EWOULDBLOCK && e != unix.EAGAIN {
			file.Close()
			return nil, e
		}
		select {
		case <-ctx.Done():
			file.Close()
			return nil, ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
	}
}
