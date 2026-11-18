package service

import (
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

const normJobsWorkspace = ".filebeans-stage/normalization-jobs"
const normMemoryAdmission = uint64(13 << 28) // 3.25 GiB; leave room below the 4 GiB limit.
const normMemoryStop = uint64(7 << 29)       // 3.5 GiB
var errNormalizationMemory = errors.New("normalization memory budget reached")

type normPipelineKey struct{}
type normTask struct {
	cancel  context.CancelCauseFunc
	reserve uint64
	order   uint64
}

// Only the coordinator admits jobs. Stage gates allow one preparation/download
// ahead, two encoders, and one validator, without sharing mutable job records.
type normPipeline struct {
	mu       sync.Mutex
	active   map[string]normTask
	sequence uint64
	serial   bool
	blocked  bool
	gates    map[string]chan struct{}
	workers  sync.WaitGroup
	memory   func() (uint64, error)
}

func newNormPipeline() *normPipeline {
	return &normPipeline{active: make(map[string]normTask), memory: normMemoryUsage,
		gates: map[string]chan struct{}{"prepare": make(chan struct{}, 1), "encode": make(chan struct{}, 2), "validate": make(chan struct{}, 1)}}
}
func (p *normPipeline) count() int { p.mu.Lock(); defer p.mu.Unlock(); return len(p.active) }
func (p *normPipeline) contains(id string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	_, ok := p.active[id]
	return ok
}
func (p *normPipeline) admit() bool {
	p.pressure()
	used, err := p.memory()
	p.mu.Lock()
	defer p.mu.Unlock()
	// If cgroup accounting is unavailable, run alone rather than assuming headroom.
	limit := 4
	if p.serial || err != nil {
		limit = 1
	}
	return !p.blocked && len(p.active) < limit && (err != nil || used < normMemoryAdmission)
}
func (p *normPipeline) pressure() {
	used, err := p.memory()
	if err != nil || used <= normMemoryStop {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.serial {
		log.Print("Normalization memory pressure: switching to sequential execution")
	}
	p.serial = true
	var newest normTask
	for _, task := range p.active {
		if task.order > newest.order {
			newest = task
		}
	}
	if newest.cancel != nil {
		newest.cancel(errNormalizationMemory)
	}
}

// Docker's working-set accounting excludes reclaimable inactive file cache.
// Buffered media I/O must not make a healthy encoder appear out of heap memory.
func normMemoryUsage() (uint64, error) {
	data, err := os.ReadFile("/sys/fs/cgroup/memory.current")
	if err != nil {
		return 0, err
	}
	current, err := strconv.ParseUint(strings.TrimSpace(string(data)), 10, 64)
	if err != nil {
		return 0, err
	}
	data, err = os.ReadFile("/sys/fs/cgroup/memory.stat")
	if err != nil {
		return 0, err
	}
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[0] == "inactive_file" {
			inactive, err := strconv.ParseUint(fields[1], 10, 64)
			if err != nil {
				return 0, err
			}
			if inactive < current {
				return current - inactive, nil
			}
			return 0, nil
		}
	}
	return current, nil
}
func normStage(ctx context.Context, stage string) (func(), error) {
	p, ok := ctx.Value(normPipelineKey{}).(*normPipeline)
	if !ok {
		return func() {}, nil
	} // Legacy recovery and focused single-job tests.
	gate := p.gates[stage]
	select {
	case gate <- struct{}{}:
		var once sync.Once
		return func() { once.Do(func() { <-gate }) }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (s *Service) startNormJob(ctx context.Context, lease *sql.Conn, p *normPipeline, job NormalizationJob) error {
	if !p.admit() {
		return errNormalizationMemory
	}
	// The global lease holds every active per-job lock, leaving the pool free for progress queries.
	conn := lease
	var err error
	lock := "filebeans-norm-" + job.ID
	var got int
	if err = conn.QueryRowContext(ctx, "SELECT GET_LOCK(?,0)", lock).Scan(&got); err != nil || got != 1 {
		return errNormalizationBlocked
	}
	release := func() { conn.ExecContext(context.Background(), "SELECT RELEASE_LOCK(?)", lock) }
	if err = s.read(job.ID, "normalize", &job); err != nil {
		release()
		return err
	}
	if !normRunnable(job) {
		release()
		return ErrConflict
	}
	if job.SourceSize < 0 {
		release()
		return ErrUnsafe
	}
	reserve := uint64(job.SourceSize)
	if job.Request.Source == "drive" {
		if reserve > ^uint64(0)/2 {
			release()
			return ErrUnsafe
		}
		reserve *= 2
	}
	p.mu.Lock()
	total := reserve
	for _, task := range p.active {
		if task.reserve > ^uint64(0)-total {
			p.mu.Unlock()
			release()
			return ErrUnsafe
		}
		total += task.reserve
	}
	root, _, err := s.normalizationRoot()
	if err == nil {
		err = s.normFree(root, total)
		root.Close()
	}
	if err != nil {
		alone := len(p.active) == 0
		p.mu.Unlock()
		if alone && errors.Is(err, errNormalizationSpace) {
			job.State = "waiting"
			job.Error = normalizationError(err)
			job.Next = time.Now().Add(time.Minute).Unix()
			_ = s.normSave(&job)
		}
		release()
		return err
	}
	taskCtx, cancel := context.WithCancelCause(ctx)
	p.sequence++
	p.active[job.ID] = normTask{cancel: cancel, reserve: reserve, order: p.sequence}
	p.mu.Unlock()
	// Set and persist the workspace choice before creating any scratch files.
	job.IsolatedWorkspace = true
	job.State = "running"
	if err = s.normSave(&job); err != nil {
		p.mu.Lock()
		delete(p.active, job.ID)
		p.mu.Unlock()
		cancel(nil)
		release()
		return err
	}
	p.workers.Add(1)
	go func() {
		defer p.workers.Done()
		defer cancel(nil)
		started := time.Now()
		_ = s.runNormalizationJob(taskCtx, &job)
		used, _ := p.memory()
		log.Printf("Normalization job %s ended in %s after %s; working memory %d MiB", job.ID, job.State, time.Since(started).Round(time.Second), used>>20)
		release()
		p.mu.Lock()
		if job.State == "waiting" && job.Error == normalizationError(errNormalizationBlocked) {
			p.blocked = true
		}
		delete(p.active, job.ID)
		p.mu.Unlock()
	}()
	return nil
}
func normRunnable(j NormalizationJob) bool {
	return j.State == "queued" || j.State == "running" || j.State == "waiting"
}

// Drain legacy scratch first, then recover isolated owners before new admissions.
// Unknown files/owners block the queue; nothing is recursively deleted.
func (s *Service) recoverNormWorkspaces(ctx context.Context, lease *sql.Conn) error {
	if err := s.recoverNormOwner(ctx, lease); err != nil {
		return err
	}
	root, _, err := s.normalizationRoot()
	if err != nil {
		return err
	}
	defer root.Close()
	if err = safe(root, normJobsWorkspace); err != nil {
		return err
	}
	entries, err := os.ReadDir(filepath.Join(filepath.Dir(s.normStagePath()), "normalization-jobs"))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, entry := range entries {
		id, err := hex.DecodeString(entry.Name())
		if err != nil || len(id) != 16 || !entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
			return errNormalizationBlocked
		}
		dir := filepath.Join(normJobsWorkspace, entry.Name())
		data, err := root.ReadFile(filepath.Join(dir, "owner.json"))
		if errors.Is(err, os.ErrNotExist) {
			// A crash before owner creation or after cleanup can leave an empty directory.
			if err = root.Remove(dir); err != nil {
				return errNormalizationBlocked
			}
			continue
		}
		var owner string
		if err != nil || json.Unmarshal(data, &owner) != nil || owner != entry.Name() {
			return errNormalizationBlocked
		}
		if err = s.recoverNormJob(ctx, lease, owner, true); err != nil {
			return err
		}
	}
	if p, ok := ctx.Value(normPipelineKey{}).(*normPipeline); ok {
		p.mu.Lock()
		p.blocked = false
		p.mu.Unlock()
	}
	return nil
}
