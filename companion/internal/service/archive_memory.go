package service

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	archiveEncoderDataLimit = uint64(1536 << 20)
	archiveHostStartMemory  = uint64(2560 << 20)
	archiveHostStopMemory   = uint64(1024 << 20)
	archiveMemoryRecovery   = 30 * time.Second
)

var errArchiveMemory = errors.New("waiting for memory")

type archiveMemorySample struct {
	available, used, limit uint64
}

type archiveMemoryGuard struct {
	required    uint64 // zero preserves the embedded worker budget
	mu          sync.Mutex
	initialized bool
	blocked     bool
	healthy     time.Time
	read        func() (archiveMemorySample, error) // synthetic samples in tests
}

func hostAvailableMemory(data []byte) (uint64, error) {
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) > 0 && fields[0] == "MemAvailable:" {
			if len(fields) != 3 || fields[2] != "kB" {
				break
			}
			value, err := strconv.ParseUint(fields[1], 10, 64)
			if err != nil || value > ^uint64(0)/1024 {
				break
			}
			return value * 1024, nil
		}
	}
	return 0, errors.New("host memory availability unavailable")
}

func readArchiveMemory() (sample archiveMemorySample, err error) {
	data, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return sample, err
	}
	if sample.available, err = hostAvailableMemory(data); err != nil {
		return sample, err
	}
	if sample.used, err = normMemoryUsage(); err != nil {
		return sample, err
	}
	data, err = os.ReadFile("/sys/fs/cgroup/memory.max")
	if err != nil {
		return sample, err
	}
	sample.limit, err = strconv.ParseUint(strings.TrimSpace(string(data)), 10, 64)
	if err != nil || sample.limit == 0 {
		return sample, fmt.Errorf("encoder container memory limit unavailable")
	}
	return sample, nil
}

func (g *archiveMemoryGuard) sample() (archiveMemorySample, error) {
	if g.read != nil {
		return g.read()
	}
	return readArchiveMemory()
}

func (g *archiveMemoryGuard) canStart(now time.Time, recovering bool) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if !g.initialized {
		g.initialized, g.blocked = true, recovering
	}
	sample, err := g.sample()
	required := g.required
	if required == 0 {
		required = archiveEncoderDataLimit
	}
	if err != nil || sample.available < archiveHostStartMemory || sample.limit <= sample.used || sample.limit-sample.used < required {
		g.blocked, g.healthy = true, time.Time{}
		return false
	}
	if !g.blocked {
		return true
	}
	if g.healthy.IsZero() {
		g.healthy = now
	}
	if now.Sub(g.healthy) < archiveMemoryRecovery {
		return false
	}
	g.blocked, g.healthy = false, time.Time{}
	return true
}

func (g *archiveMemoryGuard) cancelIfPressure(cancel context.CancelCauseFunc) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	sample, err := g.sample()
	if err != nil || sample.limit == 0 || sample.available < archiveHostStopMemory || sample.used > sample.limit-sample.limit/4 {
		g.blocked, g.healthy = true, time.Time{}
		cancel(errArchiveMemory)
		return true
	}
	if sample.available < archiveHostStartMemory {
		g.blocked, g.healthy = true, time.Time{}
	}
	return false
}

func deferArchiveForMemory(task *archiveTask, now time.Time) bool {
	if task.WaitingForMemory && task.Error == "" {
		return false
	}
	task.WaitingForMemory = true
	task.Error = ""
	task.Next = now.Add(5 * time.Second).Unix()
	return true
}
