package service

import (
	"context"
	"errors"
	"io"
	"os"
	"strconv"
	"strings"
	"time"
)

var errPiPressure = errors.New("waiting for Home Assistant resources")

const piManifestLimit = 4096

type piPaceKey struct{}

func boundedReadDir(f *os.File, limit int) ([]os.DirEntry, error) {
	var all []os.DirEntry
	for {
		next, e := f.ReadDir(128)
		if len(all)+len(next) > limit {
			return nil, ErrUnsafe
		}
		all = append(all, next...)
		if e == io.EOF {
			return all, nil
		}
		if e != nil {
			return nil, e
		}
	}
}

func (s *Service) runPiOptional(ctx context.Context, run func(context.Context)) {
	if s.config.Profile != "pi" {
		run(ctx)
	}
}

// Fail closed when pressure cannot be measured. A host-wide reserve protects HA;
// the app's cgroup separately bounds all FileBeans processes and filesystem cache.
func piHasHeadroom() bool {
	b, e := os.ReadFile("/proc/meminfo")
	if e != nil {
		return false
	}
	available, e := hostAvailableMemory(b)
	if e != nil || available < 768<<20 {
		return false
	}
	b, e = os.ReadFile("/proc/loadavg")
	if e != nil {
		return false
	}
	fields := strings.Fields(string(b))
	if len(fields) < 1 {
		return false
	}
	load, e := strconv.ParseFloat(fields[0], 64)
	if e != nil || load >= 3 {
		return false
	}
	if b, e = os.ReadFile("/proc/pressure/io"); e == nil {
		for _, line := range strings.Split(string(b), "\n") {
			if strings.HasPrefix(line, "full ") {
				for _, field := range strings.Fields(line) {
					if strings.HasPrefix(field, "avg10=") {
						v, e := strconv.ParseFloat(strings.TrimPrefix(field, "avg10="), 64)
						if e != nil || v >= 10 {
							return false
						}
					}
				}
			}
		}
	}
	return true
}

func (s *Service) piWait(ctx context.Context) error {
	if s.config.Profile != "pi" {
		return nil
	}
	for !piHasHeadroom() {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
	return nil
}

// Each Pi stream yields between 256 KiB chunks; aggregate bulk concurrency is one.
func (s *Service) piPace(ctx context.Context, bytes int) error {
	if e := s.piWait(ctx); e != nil {
		return e
	}
	if s.config.Profile != "pi" {
		return nil
	}
	timer := time.NewTimer(time.Duration(bytes) * time.Second / (12 << 20))
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
