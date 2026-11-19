package service

import (
	"context"
	"filebeans/companion/protection"
	"os"
	"time"
)

// StartFilesystem is called only by the HTTP service, never the normalizer.
// Replacement mode must be explicitly enabled on both service and broker.
func (s *Service) StartFilesystem(ctx context.Context) error {
	if !s.config.Filesystem.Enabled {
		return nil
	}
	for _, path := range []string{s.config.Filesystem.FFmpeg, s.config.Filesystem.PDFToPPM} {
		st, e := os.Stat(path)
		if e != nil || !st.Mode().IsRegular() || st.Mode()&0111 == 0 {
			return ErrUnsupported
		}
	}
	if s.vaults == nil || s.vaults.remote == nil {
		return nil
	}
	if os.Geteuid() == 0 {
		return ErrUnsafe
	}
	if e := s.vaults.guard.RequirePrimaryGroup(); e != nil {
		return e
	}
	lease, e := s.vaults.guard.Acquire()
	if e != nil {
		return protectionError(e)
	}
	lease.Close()
	ping := func() error {
		return s.vaults.remote.request("POST", "consumer/ready", map[string]any{"version": protection.Protocol, "features": protection.ConsumerFeatures}, "", nil)
	}
	if e := ping(); e != nil {
		return e
	}
	go func() {
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				_ = ping()
			}
		}
	}()
	return nil
}
