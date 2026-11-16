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
	file, e := os.OpenFile(filepath.Join(s.config.State, ".google.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if e != nil {
		s.driveMu.Unlock()
		return nil, e
	}
	for {
		e = unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB)
		if e == nil {
			return func() { unix.Flock(int(file.Fd()), unix.LOCK_UN); file.Close(); s.driveMu.Unlock() }, nil
		}
		if e != unix.EWOULDBLOCK && e != unix.EAGAIN {
			file.Close()
			s.driveMu.Unlock()
			return nil, e
		}
		select {
		case <-ctx.Done():
			file.Close()
			s.driveMu.Unlock()
			return nil, ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
	}
}
