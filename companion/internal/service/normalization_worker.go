package service

import (
	"context"
	"crypto/md5"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"golang.org/x/sys/unix"
	"io"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"
)

const normWorkspace = ".filebeans-stage/normalization"

var errNormalizationRequeue = errors.New("normalization must return to encoding admission")

func (s *Service) normSave(j *NormalizationJob) error {
	if s.vaults != nil && s.vaults.guard != nil {
		lease, e := waitProtection(s.vaults.guard)
		if e != nil {
			return protectionError(e)
		}
		defer lease.Close()
	}
	return s.save(j.ID, "normalize", j.State, j)
}
func (s *Service) normCheck(ctx context.Context, j *NormalizationJob) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	var n int
	if e := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM fb_operations WHERE id IN (?,?) AND kind='normCancel'", "nc-"+j.ID, "nc-"+j.Batch).Scan(&n); e != nil {
		return e
	}
	if n != 0 {
		return errCancelled
	}
	return nil
}
func (s *Service) normFree(root rootFS, extra uint64) error {
	c := s.config.Normalization.defaults()
	f, e := root.Open(".")
	if e != nil {
		return e
	}
	defer f.Close()
	var st syscall.Statfs_t
	if e = syscall.Fstatfs(int(f.Fd()), &st); e != nil {
		return e
	}
	available := st.Bavail * uint64(st.Bsize)
	if available < c.MinFreeBytes || extra > available-c.MinFreeBytes {
		return errNormalizationSpace
	}
	return nil
}
func (s *Service) normSourceCurrent(ctx context.Context, j *NormalizationJob) error {
	if j.Request.Source == "drive" {
		item, e := s.driveMeta(ctx, j.Request.Path, j.Connection)
		if e != nil {
			return e
		}
		if item.Trashed || item.Version != j.SourceVersion {
			return ErrConflict
		}
		return nil
	}
	root, _, e := s.normalizationRoot()
	if e != nil {
		return e
	}
	defer root.Close()
	if e = safe(root, j.Request.Path); e != nil {
		return e
	}
	st, e := root.Stat(j.Request.Path)
	if e != nil {
		return e
	}
	if version(st) != j.SourceVersion {
		return ErrConflict
	}
	return nil
}
func hashNormFile(root rootFS, path string) (string, error) {
	if e := safe(root, path); e != nil {
		return "", e
	}
	f, e := root.Open(path)
	if e != nil {
		return "", e
	}
	defer f.Close()
	h := sha256.New()
	_, e = io.Copy(h, f)
	return hex.EncodeToString(h.Sum(nil)), e
}
func normWorkspaceFor(j *NormalizationJob) string {
	if j != nil && j.IsolatedWorkspace {
		return filepath.Join(normJobsWorkspace, j.ID)
	}
	return normWorkspace
}

func (s *Service) normStagePath(jobs ...*NormalizationJob) string {
	workspace := normWorkspace
	if len(jobs) != 0 {
		workspace = normWorkspaceFor(jobs[0])
	}
	for _, r := range s.config.Roots {
		if r.Name == s.config.Normalization.defaults().Source {
			return filepath.Join(r.Path, workspace)
		}
	}
	return ""
}
func (s *Service) normPrepare(root rootFS, j *NormalizationJob) error {
	if j.IsolatedWorkspace {
		id, err := hex.DecodeString(j.ID)
		if err != nil || len(id) != 16 {
			return ErrUnsafe
		}
	}
	if e := mkdir(root, normWorkspaceFor(j)); e != nil {
		return e
	}
	if e := root.Chmod(normWorkspaceFor(j), 0700); e != nil {
		return e
	}
	marker := filepath.Join(s.normStagePath(j), "owner.json")
	if data, e := os.ReadFile(marker); e == nil {
		var owner string
		if json.Unmarshal(data, &owner) != nil || owner != j.ID {
			return errNormalizationBlocked
		}
	} else if !errors.Is(e, os.ErrNotExist) {
		return e
	} else {
		list, e := os.ReadDir(s.normStagePath(j))
		if e != nil {
			return e
		}
		if len(list) != 0 {
			return errNormalizationBlocked
		}
		if e = privateJSON(marker, j.ID); e != nil {
			return e
		}
	}
	return nil
}
func (s *Service) normCleanup(root rootFS, j *NormalizationJob) error {
	data, markerError := root.ReadFile(filepath.Join(normWorkspaceFor(j), "owner.json"))
	if errors.Is(markerError, os.ErrNotExist) {
		return nil
	}
	if markerError != nil {
		return markerError
	}
	var owner string
	if json.Unmarshal(data, &owner) != nil || owner != j.ID {
		return errNormalizationBlocked
	}
	raw := filepath.Join(normWorkspaceFor(j), "original")
	if _, e := root.Stat(raw); e == nil {
		h, e := hashNormFile(root, raw)
		if e != nil {
			return e
		}
		if j.Published {
			// A committed publication may subsequently be edited/deleted by
			// the user. Never resurrect it or retain the old input for that.
			if h != j.SourceHash {
				return errNormalizationBlocked
			}
			if e = root.Remove(raw); e != nil {
				return e
			}
		} else {
			if j.Request.Source == "drive" {
				return errNormalizationBlocked
			}
			if e = renameExclusive(root, raw, j.Request.Path); e != nil {
				return errNormalizationBlocked
			}
			j.RawMoved = false
			if e = s.normSave(j); e != nil {
				return e
			}
		}
	} else if !errors.Is(e, os.ErrNotExist) {
		return e
	}
	// Only this worker's fixed scratch files may be removed.
	entries, e := os.ReadDir(s.normStagePath(j))
	if errors.Is(e, os.ErrNotExist) {
		return nil
	}
	if e != nil {
		return e
	}
	for _, entry := range entries {
		if entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
			return errNormalizationBlocked
		}
		name := entry.Name()
		if name != "owner.json" && name != "input" && !strings.HasPrefix(name, "output.") {
			return errNormalizationBlocked
		}
	}
	for _, entry := range entries {
		if entry.Name() != "owner.json" {
			if e = root.Remove(filepath.Join(normWorkspaceFor(j), entry.Name())); e != nil {
				return e
			}
		}
	}
	if e = root.Remove(filepath.Join(normWorkspaceFor(j), "owner.json")); e != nil && !errors.Is(e, os.ErrNotExist) {
		return e
	}
	if e = syncDir(root, normWorkspaceFor(j)); e != nil {
		return e
	}
	if j.IsolatedWorkspace {
		return root.Remove(normWorkspaceFor(j))
	}
	return nil
}
func (s *Service) recoverNormOwner(ctx context.Context, lease *sql.Conn) error {
	root, _, e := s.normalizationRoot()
	if e != nil {
		return e
	}
	defer root.Close()
	data, e := root.ReadFile(filepath.Join(normWorkspace, "owner.json"))
	if errors.Is(e, os.ErrNotExist) {
		return nil
	}
	if e != nil {
		return e
	}
	var id string
	if json.Unmarshal(data, &id) != nil {
		return errNormalizationBlocked
	}
	return s.recoverNormJob(ctx, lease, id, false)
}

func (s *Service) recoverNormJob(ctx context.Context, lease *sql.Conn, id string, isolated bool) error {
	root, _, e := s.normalizationRoot()
	if e != nil {
		return e
	}
	defer root.Close()
	lock := "filebeans-norm-" + id
	var acquired int
	if e = lease.QueryRowContext(ctx, "SELECT GET_LOCK(?,0)", lock).Scan(&acquired); e != nil || acquired != 1 {
		return errNormalizationBlocked
	}
	defer lease.ExecContext(context.Background(), "SELECT RELEASE_LOCK(?)", lock)
	var j NormalizationJob
	if e = s.read(id, "normalize", &j); e != nil {
		return errNormalizationBlocked
	}
	if j.ID != id || j.IsolatedWorkspace != isolated {
		return errNormalizationBlocked
	}
	if normalizationInternalJob(j) {
		return s.excludeNormalization(&j)
	}
	// Active owner always runs before another job or scanner. Publication intents
	// are reconciled before cancellation or scratch cleanup.
	if j.State == "completed" || j.State == "cancelled" || j.State == "failed" {
		return s.normCleanup(root, &j)
	}
	j.Recoveries++
	if j.Recoveries > 3 && j.Phase != "publishing" && !j.Published {
		if e = s.normCleanup(root, &j); e != nil {
			return e
		}
		j.State = "failed"
		j.Error = "The worker restarted repeatedly during this file. The source is retained. Check worker memory/resources, then Retry."
		return s.normSave(&j)
	}
	// Reconcile publication intents synchronously, but never encode inside recovery.
	// Unpublished scratch is restartable and must re-enter normal admission,
	// including picture priority, stage concurrency, disk and memory budgets.
	if !j.Published && (j.Phase != "publishing" || j.OutputHash == "") {
		if e = s.normCleanup(root, &j); e != nil {
			return e
		}
		j.State = "queued"
		j.Phase = "queued"
		j.Done, j.Total = 0, 0
		j.Error, j.OutputHash = "", ""
		j.RawMoved = false
		return s.normSave(&j)
	}
	if e = s.normSave(&j); e != nil {
		return e
	}
	return s.runNormalizationJob(ctx, &j)
}

// Monitor blocked network reads and encoder/decoder processes, not just their
// progress callbacks. Cancellation remains responsive during a stalled transfer.
func (s *Service) monitorNormalization(parent context.Context, root rootFS, j *NormalizationJob) (context.Context, func() error) {
	ctx, cancel := context.WithCancel(parent)
	done := make(chan struct{})
	failed := make(chan error, 1)
	go func() {
		defer close(done)
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if p, ok := ctx.Value(normPipelineKey{}).(*normPipeline); ok {
					p.pressure()
				}
				e := s.normCheck(ctx, j)
				if e == nil {
					e = s.normFree(root, 0)
				}
				if e != nil {
					failed <- e
					cancel()
					return
				}
			}
		}
	}()
	var once sync.Once
	var result error
	finish := func() error {
		once.Do(func() {
			cancel()
			<-done
			select {
			case result = <-failed:
			default:
			}
		})
		return result
	}
	return ctx, finish
}
func (s *Service) downloadNorm(ctx context.Context, root rootFS, j *NormalizationJob) (file *os.File, err error) {
	if e := s.normSourceCurrent(ctx, j); e != nil {
		return nil, e
	}
	path := filepath.Join(normWorkspaceFor(j), "input")
	f, e := root.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_RDWR, 0600)
	if e != nil {
		return nil, e
	}
	defer func() {
		if err != nil {
			f.Close()
			file = nil
		}
	}()
	downloadCtx, finish := s.monitorNormalization(ctx, root, j)
	defer func() {
		if e := finish(); e != nil {
			err = e
		}
	}()
	reader, e := s.driveContent(downloadCtx, j.Request.Path, 0, j.Connection)
	if e != nil {
		return nil, e
	}
	defer reader.Close()
	sha, md := sha256.New(), md5.New()
	buf := make([]byte, 1<<20)
	j.Phase = "downloading"
	j.Total = j.SourceSize
	j.Done = 0
	if e = s.normSave(j); e != nil {
		return nil, e
	}
	for {
		if e = s.normCheck(ctx, j); e != nil {
			return nil, e
		}
		if e = s.normFree(root, 0); e != nil {
			return nil, e
		}
		n, readErr := reader.Read(buf)
		if n > 0 {
			if j.Done+int64(n) > j.SourceSize {
				return nil, ErrConflict
			}
			if _, e = f.Write(buf[:n]); e != nil {
				return nil, e
			}
			sha.Write(buf[:n])
			md.Write(buf[:n])
			j.Done += int64(n)
			if e = s.normSave(j); e != nil {
				return nil, e
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return nil, readErr
		}
	}
	if j.Done != j.SourceSize {
		return nil, io.ErrUnexpectedEOF
	}
	j.SourceHash = hex.EncodeToString(sha.Sum(nil))
	if j.ExpectedSHA != "" && !strings.EqualFold(j.SourceHash, j.ExpectedSHA) {
		return nil, ErrConflict
	}
	if j.ExpectedMD5 != "" && !strings.EqualFold(hex.EncodeToString(md.Sum(nil)), j.ExpectedMD5) {
		return nil, ErrConflict
	}
	if e = f.Sync(); e != nil {
		return nil, e
	}
	if e = s.normSourceCurrent(ctx, j); e != nil {
		return nil, e
	}
	if e = s.normSave(j); e != nil {
		return nil, e
	}
	return f, nil
}
func (s *Service) normPublish(ctx context.Context, root rootFS, j *NormalizationJob, output string) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if _, e := root.Stat(filepath.Join(normWorkspaceFor(j), "original")); e == nil {
		j.RawMoved = true
	} else if !errors.Is(e, os.ErrNotExist) {
		return e
	}
	// After an interrupted rename, the content fingerprint proves publication.
	if existing, e := hashNormFile(root, j.OutputPath); e == nil {
		if existing == j.OutputHash && j.Phase == "publishing" {
			// A local unchanged input may legitimately equal a pass-through output.
			if j.Request.Source != "drive" && j.OutputPath == j.Request.Path && !j.RawMoved {
				// Before the first rename this is still the original; using it as the
				// already-compliant final file is safe only when no bytes changed.
				if j.SourceHash != j.OutputHash {
					return ErrConflict
				}
			}
			j.Published = true
			st, e := root.Stat(j.OutputPath)
			if e != nil {
				return e
			}
			j.OutputVersion = version(st)
			return s.normSave(j)
		}
		if j.Request.Source == "drive" || j.OutputPath != j.Request.Path {
			return ErrConflict
		}
	} else if !errors.Is(e, os.ErrNotExist) {
		return e
	}
	if j.Request.Source != "drive" {
		raw := filepath.Join(normWorkspaceFor(j), "original")
		if _, e := root.Stat(raw); errors.Is(e, os.ErrNotExist) {
			if e = s.normSourceCurrent(ctx, j); e != nil {
				return e
			}
			if e = renameExclusive(root, j.Request.Path, raw); e != nil {
				return e
			}
		} else if e != nil {
			return e
		}
		// Verify the object actually moved, not just the object observed beforehand.
		actual, e := hashNormFile(root, raw)
		if e != nil {
			return e
		}
		if actual != j.SourceHash {
			return ErrConflict
		}
		j.RawMoved = true
		if e = s.normSave(j); e != nil {
			return e
		}
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if e := renameExclusive(root, output, j.OutputPath); e != nil {
		return e
	}
	if e := syncDir(root, filepath.Dir(j.OutputPath)); e != nil {
		return e
	}
	j.Published = true
	st, e := root.Stat(j.OutputPath)
	if e != nil {
		return e
	}
	j.OutputVersion = version(st)
	return s.normSave(j)
}
func (s *Service) executeNormalization(ctx context.Context, j *NormalizationJob, publicationGuard func() error) error {
	if e := s.guardArchiveWrite(j.Request.Source, j.Request.Path, false); e != nil {
		return e
	}
	if e := s.guardArchiveWrite(j.LibrarySource, j.LibraryPath, false); e != nil {
		return e
	}
	if e := s.guardArchiveWrite(j.LibrarySource, j.OutputPath, false); e != nil {
		return e
	}

	if normalizationInternalJob(*j) {
		return ErrUnsafe
	}
	root, c, e := s.normalizationRoot()
	if e != nil {
		return e
	}
	defer root.Close()
	if j.LibrarySource != c.Source || j.LibraryPath != c.Path {
		return ErrConflict
	}
	if e = s.normPrepare(root, j); e != nil {
		return e
	}
	if j.Published {
		return nil
	}
	// Once publication has begun, reconcile it before honouring cancellation.
	if j.Phase == "publishing" && j.OutputHash != "" {
		output := filepath.Join(normWorkspaceFor(j), "output"+filepath.Ext(j.OutputPath))
		_, outputError := root.Stat(output)
		_, rawError := root.Stat(filepath.Join(normWorkspaceFor(j), "original"))
		existing, existingError := hashNormFile(root, j.OutputPath)
		if errors.Is(outputError, os.ErrNotExist) && errors.Is(rawError, os.ErrNotExist) && (existingError != nil || existing != j.OutputHash) {
			j.Phase = "queued"
			j.OutputHash = ""
			j.RawMoved = false
			if e = s.normSave(j); e != nil {
				return e
			}
			return errNormalizationRequeue
		}
		if e = publicationGuard(); e != nil {
			return e
		}
		if e = s.normPublish(ctx, root, j, output); e != nil {
			return e
		}
		return nil
	}
	releasePrepare, e := normStage(ctx, "prepare")
	if e != nil {
		return e
	}
	defer releasePrepare()
	if e = s.normCheck(ctx, j); e != nil {
		return e
	}
	if e = s.normSourceCurrent(ctx, j); e != nil {
		return e
	}
	reserve := uint64(j.SourceSize)
	if j.Request.Source == "drive" {
		if reserve > ^uint64(0)/2 {
			return ErrUnsafe
		}
		reserve *= 2
	}
	if e = s.normFree(root, reserve); e != nil {
		return e
	}
	// Interrupted pre-publication conversions are restarted, never appended.
	entries, e := os.ReadDir(s.normStagePath(j))
	if e != nil {
		return e
	}
	for _, entry := range entries {
		if entry.Name() != "owner.json" {
			if (entry.Name() != "input" && !strings.HasPrefix(entry.Name(), "output.")) || entry.Name() == "original" || entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
				return errNormalizationBlocked
			}
			if e = root.Remove(filepath.Join(normWorkspaceFor(j), entry.Name())); e != nil {
				return e
			}
		}
	}
	j.State = "running"
	j.Phase = "inspecting"
	j.Error = ""
	j.Done = 0
	j.Total = 0
	if e = s.normSave(j); e != nil {
		return e
	}
	var input *os.File
	if j.Request.Source == "drive" {
		input, e = s.downloadNorm(ctx, root, j)
	} else {
		if e = safe(root, j.Request.Path); e == nil {
			input, e = root.Open(j.Request.Path)
		}
		if e == nil {
			h := sha256.New()
			_, e = io.Copy(h, input)
			j.SourceHash = hex.EncodeToString(h.Sum(nil))
		}
	}
	if e != nil {
		return e
	}
	defer input.Close()
	info, e := s.probeMedia(ctx, input, false)
	if e != nil {
		return e
	}
	plan, e := makeMediaPlan(info, j.Request.Path)
	if j.Request.Source == "drive" {
		plan, e = makeMediaPlan(info, j.Request.RelativePath)
	}
	if e != nil {
		return e
	}
	if plan.Image && (info.Format.Name == "webp_pipe" || info.Format.Name == "png_pipe") {
		counted, e := s.probeMedia(ctx, input, true)
		if e != nil {
			return e
		}
		plan, e = makeMediaPlan(counted, filepath.Base(j.Request.Path))
		if j.Request.Source == "drive" {
			plan, e = makeMediaPlan(counted, j.Request.RelativePath)
		}
		if e != nil {
			return e
		}
	}
	relative := strings.TrimSuffix(j.Request.RelativePath, filepath.Ext(j.Request.RelativePath)) + plan.Extension
	if j.Request.Source == "drive" {
		j.OutputPath = filepath.Join(c.Path, relative)
	} else {
		j.OutputPath = strings.TrimSuffix(j.Request.Path, filepath.Ext(j.Request.Path)) + plan.Extension
	}
	if !within(j.OutputPath, c.Path) || j.OutputPath == c.Path {
		return ErrUnsafe
	}
	if j.OutputPath != j.Request.Path || j.Request.Source == "drive" {
		if _, e = root.Lstat(j.OutputPath); e == nil {
			return ErrConflict
		} else if !errors.Is(e, os.ErrNotExist) {
			return e
		}
	}
	if e = mkdir(root, filepath.Dir(j.OutputPath)); e != nil {
		return e
	}
	if e = normPublicDirs(root, filepath.Dir(j.OutputPath)); e != nil {
		return e
	}
	outputRel := filepath.Join(normWorkspaceFor(j), "output"+plan.Extension)
	outputAbs := filepath.Join(s.normStagePath(j), "output"+plan.Extension)
	j.Phase = "converting"
	j.Total = int64(plan.Duration * 1000)
	j.Done = 0
	if e = s.normSave(j); e != nil {
		return e
	}
	encodeCtx, finishWatch := s.monitorNormalization(ctx, root, j)
	defer finishWatch()
	releaseEncode, e := normStage(encodeCtx, "encode")
	if e != nil {
		return e
	}
	releasePrepare()
	e = s.encodeMedia(encodeCtx, input, outputAbs, plan, func(done int64) error {
		if err := s.normCheck(ctx, j); err != nil {
			return err
		}
		j.Done = min(done, j.Total)
		return s.normSave(j)
	})
	releaseEncode()
	if e != nil {
		if monitorError := finishWatch(); monitorError != nil {
			return monitorError
		}
		return e
	}
	if e = s.normCheck(ctx, j); e != nil {
		return e
	}
	j.Phase = "validating"
	if e = s.normSave(j); e != nil {
		return e
	}
	releaseValidate, e := normStage(encodeCtx, "validate")
	if e != nil {
		return e
	}
	defer releaseValidate()
	// Validation has the same cancellation/disk monitoring as encoding via its context.
	if e = s.validateMedia(encodeCtx, outputAbs, plan); e != nil {
		if monitorError := finishWatch(); monitorError != nil {
			return monitorError
		}
		return e
	}
	if e = finishWatch(); e != nil {
		return e
	}
	if e = s.normSourceCurrent(ctx, j); e != nil {
		return e
	}
	if e = s.normCheck(ctx, j); e != nil {
		return e
	}
	f, e := root.OpenFile(outputRel, os.O_RDWR, 0600)
	if e != nil {
		return e
	}
	e = f.Sync()
	f.Close()
	if e != nil {
		return e
	}
	if e = root.Chmod(outputRel, 0660); e != nil {
		return e
	}
	j.OutputHash, e = hashNormFile(root, outputRel)
	if e != nil {
		return e
	}
	st, e := root.Stat(outputRel)
	if e != nil {
		return e
	}
	j.OutputSize = st.Size()
	j.Phase = "publishing"
	if e = s.normSave(j); e != nil {
		return e
	}
	if e = publicationGuard(); e != nil {
		return e
	}
	return s.normPublish(ctx, root, j, outputRel)
}
func normPublicDirs(root rootFS, path string) error {
	current := ""
	for _, part := range strings.Split(path, "/") {
		current = filepath.Join(current, part)
		if e := accessibleDirectory(root, current); e != nil {
			return e
		}
	}
	return nil
}
func normalizationError(e error) string {
	switch {
	case errors.Is(e, errNormalizationMemory):
		return "Normalization memory budget reached. Retrying without overlapping jobs."
	case errors.Is(e, errNormalizationSpace):
		return "VPS disk reserve reached. Free space; normalization will retry without another download."
	case errors.Is(e, errNormalizationBlocked):
		return "An unpublished original is preserved in the normalization workspace. Resolve the destination conflict before retrying; the queue is paused."
	case errors.Is(e, ErrUnsupported):
		return "Media inspection, encoding, or validation failed. The source is retained; check its format and retry."
	default:
		return publicError(e)
	}
}
func (s *Service) runNormalizationJob(ctx context.Context, j *NormalizationJob) error {
	if j.State == "completed" {
		return nil
	}
	if normalizationInternalJob(*j) {
		return s.excludeNormalization(j)
	}
	var unlockPublication func()
	guard := func() error {
		if unlockPublication != nil {
			return nil
		}
		var e error
		unlockPublication, e = lockPrivateFile(ctx, filepath.Join(s.config.State, ".normalization-publish.lock"), unix.LOCK_SH)
		return e
	}
	defer func() {
		if unlockPublication != nil {
			unlockPublication()
		}
	}()
	e := s.executeNormalization(ctx, j, guard)
	if ctx.Err() != nil {
		if !errors.Is(context.Cause(ctx), errNormalizationMemory) {
			return ctx.Err()
		}
		e = errNormalizationMemory
	}
	root, _, rootErr := s.normalizationRoot()
	if rootErr != nil {
		return rootErr
	}
	defer root.Close()
	if cleanupErr := s.normCleanup(root, j); cleanupErr != nil {
		j.State = "waiting"
		j.Error = normalizationError(cleanupErr)
		j.Next = time.Now().Add(time.Minute).Unix()
		s.normSave(j)
		return cleanupErr
	}
	if errors.Is(e, errNormalizationRequeue) {
		j.State, j.Phase = "queued", "queued"
		j.Done, j.Total = 0, 0
		j.Error = ""
		j.Next = 0
	} else if e == nil {
		j.State = "completed"
		j.Phase = "completed"
		j.Done = j.Total
		j.Error = ""
	} else if errors.Is(e, errCancelled) {
		j.State = "cancelled"
		j.Phase = "cancelled"
		j.Error = ""
	} else {
		j.Error = normalizationError(e)
		j.Attempts++
		if errors.Is(e, errNormalizationSpace) || errors.Is(e, ErrAuthorization) || errors.Is(e, ErrMount) || (!errors.Is(e, ErrUnsafe) && !errors.Is(e, ErrConflict) && !errors.Is(e, ErrUnsupported) && !errors.Is(e, ErrDrivePermission) && !errors.Is(e, ErrDriveAccount) && !errors.Is(e, os.ErrNotExist) && !errors.Is(e, os.ErrPermission)) {
			j.State = "waiting"
			j.Next = time.Now().Add(time.Minute).Unix()
		} else {
			j.State = "failed"
		}
	}
	return s.normSave(j)
}
func (s *Service) RunNormalization(ctx context.Context) {
	// Filesystem fencing remains held while a lost database lease is unwinding.
	// All replicas share state; a successor cannot race an old process cleanup.
	unlock, e := lockPrivateFile(ctx, filepath.Join(s.config.State, ".normalization-worker.lock"), unix.LOCK_EX)
	if e != nil {
		return
	}
	defer unlock()
	for ctx.Err() == nil {
		conn, e := s.db.Conn(ctx)
		if e != nil {
			wait(ctx, 5*time.Second)
			continue
		}
		var got int
		e = conn.QueryRowContext(ctx, "SELECT GET_LOCK('filebeans-normalization-worker',0)").Scan(&got)
		if e == nil && got == 1 {
			s.normalizationLoop(ctx, conn)
		}
		conn.ExecContext(context.Background(), "SELECT RELEASE_LOCK('filebeans-normalization-worker')")
		conn.Close()
		wait(ctx, 5*time.Second)
	}
}
func (s *Service) normalizationLoop(parent context.Context, lease *sql.Conn) {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	pipeline, ok := parent.Value(normPipelineKey{}).(*normPipeline)
	if !ok {
		pipeline = newNormPipeline()
	}
	pipeline.serial = s.config.Normalization.Sequential
	ctx = context.WithValue(ctx, normPipelineKey{}, pipeline)
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				probe, stop := context.WithTimeout(ctx, 5*time.Second)
				e := lease.PingContext(probe)
				stop()
				if e != nil {
					cancel()
					return
				}
			}
		}
	}()
	defer func() { cancel(); <-done }()
	defer func() { cancel(); pipeline.workers.Wait() }()
	lastScan := time.Time{}
	for ctx.Err() == nil {
		if pipeline.count() == 0 {
			if e := s.recoverNormWorkspaces(ctx, lease); e != nil {
				if ctx.Err() != nil {
					return
				}
				log.Print("Normalization workspace recovery is waiting; inspect normalization job status")
				wait(ctx, 10*time.Second)
				continue
			}
		}
		if !pipeline.admit() {
			wait(ctx, time.Second)
			continue
		}
		rows, e := s.records("normBatch")
		if e != nil {
			wait(ctx, 5*time.Second)
			continue
		}
		inventoryPending := false
		for _, row := range rows {
			var b NormalizationBatch
			if json.Unmarshal(row, &b) == nil && b.State == "queued" {
				if e = s.discoverNormalization(ctx, &b); e != nil {
					b.Error = normalizationError(e)
					if errors.Is(e, errCancelled) {
						b.State = "cancelled"
					}
					if errors.Is(e, ErrConflict) || errors.Is(e, ErrUnsafe) {
						b.State = "failed"
					}
					s.save(b.ID, "normBatch", b.State, b)
					wait(ctx, 5*time.Second)
				}
				if b.State == "queued" {
					inventoryPending = true
				}
			}
		}
		if pipeline.count() == 0 && time.Since(lastScan) >= 30*time.Second {
			if e = s.scanNormalization(); e != nil {
				log.Print("Normalization scan deferred; check configured VPS library and database")
			}
			lastScan = time.Now()
		}
		jobs, e := s.normalizationJobs()
		if e != nil {
			wait(ctx, 5*time.Second)
			continue
		}
		for i := range jobs {
			if normalizationInternalJob(jobs[i]) && !pipeline.contains(jobs[i].ID) {
				if err := s.excludeNormalization(&jobs[i]); err != nil {
					jobs[i].State = "waiting"
				}
			}
		}
		classificationPending := s.classifyNormalizationJobs(ctx, jobs)
		picturesPending := orderNormalizationJobs(jobs)
		spaceBlocked := false
		for _, job := range jobs {
			if job.State == "waiting" && job.Error == normalizationError(errNormalizationSpace) && job.Next > time.Now().Unix() && !errors.Is(s.normCheck(ctx, &job), errCancelled) {
				spaceBlocked = true
			}
		}
		if spaceBlocked {
			wait(ctx, time.Second)
			continue
		}
		sort.SliceStable(jobs, func(i, j int) bool {
			return jobs[i].State == "waiting" && jobs[i].Error == normalizationError(errNormalizationSpace) && !(jobs[j].State == "waiting" && jobs[j].Error == normalizationError(errNormalizationSpace))
		})
		selected := false
		for i := range jobs {
			j := &jobs[i]
			if pipeline.contains(j.ID) {
				continue
			}
			if !normRunnable(*j) {
				continue
			}
			if (picturesPending || inventoryPending || classificationPending) && !normalizationPicture(*j) {
				continue
			}
			if j.Next > time.Now().Unix() && !errors.Is(s.normCheck(ctx, j), errCancelled) {
				continue
			}
			if e = s.startNormJob(ctx, lease, pipeline, *j); e != nil {
				continue
			}
			selected = true
			break
		}
		if !selected {
			wait(ctx, time.Second)
		}
	}
}
