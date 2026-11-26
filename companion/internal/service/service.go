package service

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"os"
	"sync"
	"sync/atomic"
	"time"
)

type Service struct {
	archiveEpoch     atomic.Uint64
	archiveStatusMu  sync.Mutex
	archiveActive    map[string]*archiveActivity
	archiveMemory    archiveMemoryGuard
	archiveTrusted   bool                 // private maintenance instance; never used by HTTP handlers
	datasetDiskCheck func(*os.File) error // injected only for synthetic filesystem capacity tests
	fsMu             sync.Mutex           // serialize API mutation publication and revision checks
	vaultGate        sync.RWMutex         // filesystem operations must finish before an unmount
	vaults           *vaultManager
	config           Config
	db               *sql.DB
	mu               sync.Mutex
	driveMu          sync.Mutex
	copyCancels      map[string]context.CancelFunc // guarded by mu
}

func ID() string {
	b := make([]byte, 16)
	if _, e := rand.Read(b); e != nil {
		panic(e)
	}
	return hex.EncodeToString(b)
}
func New(c Config) (*Service, error) {
	d, e := openDB(c.DSN)
	if e != nil {
		return nil, e
	}
	if e = os.MkdirAll(c.State, 0700); e != nil {
		d.Close()
		return nil, e
	}
	v, e := newVaultManager(c)
	if e != nil {
		d.Close()
		return nil, e
	}
	return &Service{config: c, db: d, vaults: v}, nil
}
func (s *Service) Close() {
	if s.vaults != nil {
		s.vaults.close()
	}
	s.db.Close()
}
func (s *Service) Migrate() error { _, e := s.db.Exec(Schema); return e }
func (s *Service) Run(ctx context.Context) {
	// A database advisory lock prevents two workers publishing the same job.
	for ctx.Err() == nil {
		conn, e := s.db.Conn(ctx)
		if e != nil {
			wait(ctx, 5*time.Second)
			continue
		}
		var got int
		e = conn.QueryRowContext(ctx, "SELECT GET_LOCK('filebeans-companion-worker',0)").Scan(&got)
		if e != nil || got != 1 {
			conn.Close()
			wait(ctx, 5*time.Second)
			continue
		}
		s.loop(ctx, conn)
		conn.ExecContext(context.Background(), "SELECT RELEASE_LOCK('filebeans-companion-worker')")
		conn.Close()
		wait(ctx, 5*time.Second)
	}
}
func (s *Service) loop(ctx context.Context, lease *sql.Conn) {
	workerCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	heartbeatDone := make(chan struct{})
	go func() {
		defer close(heartbeatDone)
		for workerCtx.Err() == nil {
			probe, stop := context.WithTimeout(workerCtx, 5*time.Second)
			e := lease.PingContext(probe)
			stop()
			if e != nil {
				cancel()
				return
			}
			wait(workerCtx, time.Second)
		}
	}()
	defer func() { cancel(); <-heartbeatDone }()
	ctx = workerCtx
	archiveDone := make(chan struct{})
	go func() { defer close(archiveDone); s.archiveSyncLoop(ctx) }()
	defer func() { cancel(); <-archiveDone }()
	thumbnailDone := make(chan struct{})
	go func() { defer close(thumbnailDone); s.thumbnailBackfill(ctx) }()
	defer func() { cancel(); <-thumbnailDone }()
	datasetDone := make(chan struct{})
	go func() {
		defer close(datasetDone)
		for ctx.Err() == nil {
			if !s.datasetWorker(ctx) {
				wait(ctx, time.Second)
			}
		}
	}()
	defer func() { cancel(); <-datasetDone }()
	maintenanceDone := make(chan struct{})
	go func() { defer close(maintenanceDone); s.maintenance(ctx) }()
	defer func() { cancel(); <-maintenanceDone }()
	for ctx.Err() == nil {
		s.mu.Lock()
		data, e := s.records("copy")
		var selected *Job
		if e == nil {
			for i := len(data) - 1; i >= 0; i-- {
				var j Job
				if json.Unmarshal(data[i], &j) != nil {
					continue
				}
				if j.State == "queued" || j.State == "preparing" || j.State == "running" || j.State == "discovering" || j.State == "waiting" {
					if j.Next <= time.Now().Unix() {
						selected = &j
						break
					}
				}
			}
		}
		s.mu.Unlock()
		if selected != nil {
			s.runJob(ctx, selected)
		} else {
			wait(ctx, time.Second)
		}
	}
}

// Maintenance is independent of the serial copy worker, including long transfers.
func (s *Service) maintenance(ctx context.Context) {
	for ctx.Err() == nil {
		s.vaultGate.RLock()
		s.mu.Lock()
		for _, root := range s.config.Roots {
			if r, e := s.root(root.Name); e == nil {
				_ = mkdir(r, "Trash")
				_ = accessibleDirectory(r, "Trash")
				r.Close()
			}
		}
		s.reconcileTrash()
		if s.vaults != nil && s.vaults.remote != nil {
			for _, v := range s.vaults.status() {
				if v.Trashed > 0 && v.Trashed+10*24*3600 <= time.Now().Unix() && s.guardArchiveVaultWrite(v.ID) == nil {
					_ = s.vaults.remote.folderAction(v.ID, "purge", "")
				}
			}
		}
		s.mu.Unlock()
		s.vaultGate.RUnlock()
		wait(ctx, time.Hour)
	}
}

func wait(ctx context.Context, d time.Duration) {
	select {
	case <-ctx.Done():
	case <-time.After(d):
	}
}
