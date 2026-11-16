package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

const copyWorkers = 4
const smallCopyLimit = 4 << 20
const copyCheckpointFiles = 32

// Only this coordinator mutates the shared job. Workers own their File values;
// reports contain only data flushed to the destination and safe to resume.
type copyProgress struct {
	mu        sync.Mutex
	service   *Service
	job       *Job
	dirty     map[int]bool
	last      time.Time
	started   time.Time
	initial   int64
	completed int
}

func (p *copyProgress) report(index int, file File) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	old := p.job.Manifest[index]
	p.job.Done += file.Done - old.Done
	if old.Completed && !file.Completed {
		p.job.ItemsDone--
	}
	if file.Completed && !old.Completed {
		p.job.ItemsDone++
		p.completed++
	}
	p.job.Manifest[index] = file
	p.job.Current = file.To
	p.job.Speed = max(0, float64(p.job.Done-p.initial)/time.Since(p.started).Seconds())
	p.dirty[index] = true
	if p.completed >= copyCheckpointFiles || time.Since(p.last) >= time.Second {
		return p.flushLocked()
	}
	return nil
}
func (p *copyProgress) flush() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.flushLocked()
}
func (p *copyProgress) flushLocked() error {
	if len(p.dirty) == 0 {
		return nil
	}
	// Patch only changed manifest entries instead of reading and rewriting every
	// file's recovery record for every small completion. No schema migration.
	indices := make([]int, 0, len(p.dirty))
	for i := range p.dirty {
		indices = append(indices, i)
	}
	sort.Ints(indices)
	query := "UPDATE fb_operations SET body=JSON_SET(body,'$.done',?,'$.speed',?,'$.current',?,'$.itemsDone',?"
	args := []any{p.job.Done, p.job.Speed, p.job.Current, p.job.ItemsDone}
	for _, i := range indices {
		data, err := json.Marshal(p.job.Manifest[i])
		if err != nil {
			return err
		}
		query += fmt.Sprintf(", '$.manifest[%d]', JSON_EXTRACT(?,'$')", i)
		args = append(args, string(data))
	}
	query += ") WHERE id=? AND kind='copy'"
	args = append(args, p.job.ID)
	p.service.mu.Lock()
	defer p.service.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var cancelled bool
	if err := p.service.db.QueryRowContext(ctx, "SELECT COALESCE(JSON_UNQUOTE(JSON_EXTRACT(body,'$.cancel')),'false') FROM fb_operations WHERE id=? AND kind='copy'", p.job.ID).Scan(&cancelled); err != nil {
		return err
	}
	if cancelled {
		return errCancelled
	}
	if _, err := p.service.db.ExecContext(ctx, query, args...); err != nil {
		return err
	}
	clear(p.dirty)
	p.completed = 0
	p.last = time.Now()
	return nil
}

// Directory handles are pinned, rather than caching a claim that a pathname is
// safe. openat(O_NOFOLLOW) checks each new component atomically. The LRU bounds
// open descriptors even for very large trees; workers duplicate their handles.
type copyDirectory struct {
	file *os.File
	used uint64
}
type copyStage struct {
	mu         sync.Mutex
	root       *os.File
	dirs       map[string]copyDirectory
	claimed    map[stageIdentity]string
	identities map[string]stageIdentity
	clock      uint64
}

func newCopyStage(dst *os.Root, path string) (*copyStage, error) {
	if cleaned, err := clean(path); err != nil || cleaned != path || path == "." || strings.HasPrefix(path, "/") {
		return nil, ErrUnsafe
	}
	root, err := dst.Open(".")
	if err != nil {
		return nil, err
	}
	for _, part := range strings.Split(path, "/") {
		fd, openErr := unix.Openat(int(root.Fd()), part, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		root.Close()
		if openErr != nil {
			return nil, noFollowError(openErr)
		}
		root = os.NewFile(uintptr(fd), path)
	}
	return &copyStage{root: root, dirs: map[string]copyDirectory{}, claimed: map[stageIdentity]string{}, identities: map[string]stageIdentity{}}, nil
}

// Caller holds mu. Remember both directions: aliases must not share an inode,
// and an evicted pathname must not silently reopen a replacement directory.
func (c *copyStage) claim(info fs.FileInfo, path string) error {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return ErrUnsafe
	}
	key := stageIdentity{device: uint64(stat.Dev), inode: uint64(stat.Ino)}
	if previous, ok := c.identities[path]; ok && previous != key {
		return ErrConflict
	}
	if err := claimStage(info, path, c.claimed); err != nil {
		return err
	}
	c.identities[path] = key
	return nil
}

func (c *copyStage) close() {
	for _, dir := range c.dirs {
		dir.file.Close()
	}
	c.root.Close()
}
func noFollowError(err error) error {
	if errors.Is(err, unix.ELOOP) || errors.Is(err, unix.ENOTDIR) {
		return ErrUnsafe
	}
	return err
}
func duplicateDirectory(file *os.File) (*os.File, error) {
	fd, err := unix.FcntlInt(file.Fd(), unix.F_DUPFD_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), file.Name()), nil
}
func (c *copyStage) directory(path string) (*os.File, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.directoryLocked(path)
}
func (c *copyStage) directoryLocked(path string) (*os.File, error) {
	if path == "." {
		return duplicateDirectory(c.root)
	}
	if cleaned, err := clean(path); err != nil || cleaned != path || strings.HasPrefix(path, "/") {
		return nil, ErrUnsafe
	}
	if dir, ok := c.dirs[path]; ok {
		c.clock++
		dir.used = c.clock
		c.dirs[path] = dir
		return duplicateDirectory(dir.file)
	}
	parent, err := c.directoryLocked(filepath.Dir(path))
	if err != nil {
		return nil, err
	}
	defer parent.Close()
	name := filepath.Base(path)
	fd, err := unix.Openat(int(parent.Fd()), name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if errors.Is(err, unix.ENOENT) {
		if err = unix.Mkdirat(int(parent.Fd()), name, 0700); err != nil && !errors.Is(err, unix.EEXIST) {
			return nil, err
		}
		fd, err = unix.Openat(int(parent.Fd()), name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	}
	if err != nil {
		return nil, noFollowError(err)
	}
	file := os.NewFile(uintptr(fd), path)
	info, err := file.Stat()
	if err == nil {
		err = c.claim(info, path)
	}
	if err == nil {
		err = file.Chmod(0770)
	}
	if err != nil {
		file.Close()
		return nil, err
	}
	if len(c.dirs) >= 128 {
		oldest := ""
		var age uint64 = ^uint64(0)
		for key, value := range c.dirs {
			if value.used < age {
				oldest = key
				age = value.used
			}
		}
		c.dirs[oldest].file.Close()
		delete(c.dirs, oldest)
	}
	c.clock++
	c.dirs[path] = copyDirectory{file: file, used: c.clock}
	return duplicateDirectory(file)
}
func (c *copyStage) open(file File) (*os.File, fs.FileInfo, error) {
	parent, err := c.directory(filepath.Dir(file.To))
	if err != nil {
		return nil, nil, err
	}
	defer parent.Close()
	if !validName(filepath.Base(file.To)) {
		return nil, nil, ErrUnsafe
	}
	fd, err := unix.Openat(int(parent.Fd()), filepath.Base(file.To), unix.O_CREAT|unix.O_RDWR|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0600)
	if err != nil {
		return nil, nil, noFollowError(err)
	}
	out := os.NewFile(uintptr(fd), file.To)
	info, err := out.Stat()
	if err == nil && !info.Mode().IsRegular() {
		err = ErrUnsafe
	}
	if err == nil {
		c.mu.Lock()
		err = c.claim(info, file.To)
		c.mu.Unlock()
	}
	if err != nil {
		out.Close()
		return nil, nil, err
	}
	return out, info, nil
}

// A single no-follow validation replaces the old chmod traversal and repeated
// ancestor checks. Reject unlisted/replaced objects before any publication.
func (c *copyStage) validate(ctx context.Context, expected map[string]File) error {
	var walk func(*os.File, string) error
	walk = func(dir *os.File, path string) error {
		entries, err := dir.ReadDir(-1)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			if err := ctx.Err(); err != nil {
				return err
			}
			rel := filepath.Join(path, entry.Name())
			file, ok := expected[rel]
			if !ok {
				return ErrConflict
			}
			// Raw openat descriptors do not carry an absolute pathname for
			// DirEntry.Info. Anchor lstat to the pinned descriptor itself.
			info, err := os.Lstat(fmt.Sprintf("/proc/self/fd/%d/%s", dir.Fd(), entry.Name()))
			if err != nil {
				return err
			}
			if info.Mode()&os.ModeSymlink != 0 {
				return ErrUnsafe
			}
			if info.IsDir() != file.Directory || (!file.Directory && (!info.Mode().IsRegular() || info.Size() != file.Size)) {
				return ErrConflict
			}
			// The inode must still be the object that was copied through a pinned fd.
			if err = claimExisting(info, rel, c.claimed); err != nil {
				return err
			}
			delete(expected, rel)
			if file.Directory {
				fd, err := unix.Openat(int(dir.Fd()), entry.Name(), unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
				if err != nil {
					return noFollowError(err)
				}
				child := os.NewFile(uintptr(fd), rel)
				childInfo, err := child.Stat()
				if err == nil {
					err = claimExisting(childInfo, rel, c.claimed)
				}
				if err == nil {
					err = walk(child, rel)
				}
				child.Close()
				if err != nil {
					return err
				}
			}
		}
		return nil
	}
	// Dup shares the directory offset: reopen "." for an independent traversal.
	fd, err := unix.Openat(int(c.root.Fd()), ".", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	root := os.NewFile(uintptr(fd), "stage")
	defer root.Close()
	if err = walk(root, ""); err != nil {
		return err
	}
	if len(expected) != 0 {
		return ErrConflict
	}
	return nil
}
func claimExisting(info fs.FileInfo, path string, claimed map[stageIdentity]string) error {
	before := len(claimed)
	if err := claimStage(info, path, claimed); err != nil {
		return err
	}
	if len(claimed) != before {
		return ErrConflict
	}
	return nil
}

func (s *Service) copyManifest(ctx context.Context, j *Job, dst *os.Root, stage string) error {
	return s.copyManifestWorkers(ctx, j, dst, stage, copyWorkers)
}

func (s *Service) copyManifestWorkers(ctx context.Context, j *Job, dst *os.Root, stage string, workerCount int) error {
	if workerCount < 1 || workerCount > copyWorkers {
		return ErrUnsafe
	}

	staging, err := newCopyStage(dst, stage)
	if err != nil {
		return err
	}
	defer staging.close()
	progress := &copyProgress{service: s, job: j, dirty: map[int]bool{}, last: time.Now(), started: time.Now(), initial: j.Done}
	workCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	tasks := make(chan int)
	large := make(chan struct{}, 1)
	var workers sync.WaitGroup
	var first error
	var once sync.Once
	failed := func(err error) { once.Do(func() { first = err; cancel() }) }
	expected := map[string]File{}
	indices := []int{}
	j.ItemsDone = 0
	for i, f := range j.Manifest {
		if f.Completed {
			j.ItemsDone++
		}
		if contains(j.Published, strings.Split(f.To, "/")[0]) {
			continue
		}
		if _, ok := expected[f.To]; ok {
			return ErrConflict
		}
		expected[f.To] = f
		indices = append(indices, i)
	}
	// Workers snapshot immutable task descriptions before starting. Shared
	// progress is accessed only by the coordinator under its mutex.
	files := append([]File(nil), j.Manifest...)
	for worker := 0; worker < workerCount; worker++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for index := range tasks {
				if workCtx.Err() != nil {
					return
				}
				file := files[index]
				if file.Directory {
					dir, err := staging.directory(file.To)
					if err == nil {
						err = dir.Close()
					}
					if err == nil {
						file.Completed = true
						err = progress.report(index, file)
					}
					if err != nil {
						failed(err)
						return
					}
					continue
				}
				big := file.Size > smallCopyLimit
				if big {
					select {
					case large <- struct{}{}:
					case <-workCtx.Done():
						return
					}
				}
				out, info, err := staging.open(file)
				if err == nil {
					err = s.copyFile(workCtx, j.Connection, &file, out, info, func() error { return progress.report(index, file) })
					if closeErr := out.Close(); err == nil {
						err = closeErr
					}
				}
				if big {
					<-large
				}
				if err != nil {
					failed(err)
					return
				}
			}
		}()
	}
send:
	for _, index := range indices {
		select {
		case tasks <- index:
		case <-workCtx.Done():
			break send
		}
	}
	close(tasks)
	workers.Wait()
	if first != nil {
		return first
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := progress.flush(); err != nil {
		return err
	}
	// Ensure the stage pathname still refers to our pinned root before renames.
	current, err := dst.Stat(stage)
	if err != nil {
		return err
	}
	pinned, err := staging.root.Stat()
	if err != nil {
		return err
	}
	if !os.SameFile(current, pinned) {
		return ErrConflict
	}
	return staging.validate(ctx, expected)
}
