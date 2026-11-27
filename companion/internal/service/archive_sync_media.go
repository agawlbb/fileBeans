package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

const archiveStage = ".filebeans-stage/archive-sync"
const archiveOutputLimit = int64(16 << 30)

func archiveSpace(root rootFS, path string, extra int64) (int64, error) {
	if p, ok := root.(*archivePeerRoot); ok {
		var out int64
		e := p.call(archivePeerRequest{Operation: "space", Path: path, Size: extra}, &out)
		return out, e
	}
	f, e := root.Open(path)
	if e != nil {
		return 0, e
	}
	defer f.Close()
	var st syscall.Statfs_t
	if e = syscall.Fstatfs(int(f.Fd()), &st); e != nil {
		return 0, e
	}
	free := st.Bavail * uint64(st.Bsize)
	reserve := uint64(2 << 30)
	if extra < 0 || free < reserve || uint64(extra) > free-reserve {
		return 0, errNormalizationSpace
	}
	available := free - reserve
	if available > math.MaxInt64 {
		available = math.MaxInt64
	}
	return min(int64(available), archiveOutputLimit), nil
}
func archiveHash(root rootFS, path string) (string, error) {
	return archiveHashContext(context.Background(), root, path)
}
func archiveHashContext(ctx context.Context, root rootFS, path string) (string, error) {
	if p, ok := root.(*archivePeerRoot); ok {
		var out string
		e := p.callContext(ctx, archivePeerRequest{Operation: "hash", Path: path}, &out)
		return out, e
	}
	var err error
	path, err = clean(path)
	if err != nil {
		return "", err
	}
	f, e := filesystemOpenFile(root, path)
	if e != nil {
		return "", e
	}
	defer f.Close()
	h := sha256.New()
	_, e = io.Copy(h, archiveReader{ctx, f})
	return hex.EncodeToString(h.Sum(nil)), e
}

type archiveReader struct {
	ctx context.Context
	r   io.Reader
}

func (r archiveReader) Read(p []byte) (int, error) {
	if e := r.ctx.Err(); e != nil {
		return 0, e
	}
	if pace, ok := r.ctx.Value(piPaceKey{}).(func(context.Context, int) error); ok {
		if len(p) > 256<<10 {
			p = p[:256<<10]
		}
		n, e := r.r.Read(p)
		if n > 0 {
			if err := pace(r.ctx, n); err != nil {
				return n, err
			}
		}
		return n, e
	}
	return r.r.Read(p)
}

// A temporary original is bounded to the exact source length. Publication is
// no-overwrite, with a full checksum readback before the VPS retention is released.
func (s *Service) copyArchiveOriginal(ctx context.Context, src rootFS, from string, dst rootFS, to, expected, stage string) error {
	if p, ok := dst.(*archivePeerRoot); ok {
		return p.copyOriginal(ctx, src, from, to, expected, stage)
	}
	input, e := filesystemOpenFile(src, from)
	if e != nil {
		return e
	}
	defer input.Close()
	info, e := input.Stat()
	if e != nil {
		return e
	}
	if expected == "" {
		h := sha256.New()
		if _, e = io.Copy(h, archiveReader{ctx, input}); e != nil {
			return e
		}
		expected = hex.EncodeToString(h.Sum(nil))
		if _, e = input.Seek(0, 0); e != nil {
			return e
		}
	}
	if st, e := dst.Stat(to); e == nil {
		if !st.Mode().IsRegular() || st.Size() != info.Size() {
			return ErrConflict
		}
		hash, e := archiveHashContext(ctx, dst, to)
		if e != nil {
			return e
		}
		if hash != expected {
			return ErrConflict
		}
		return normSetModified(dst, to, info.ModTime().UTC().Format(time.RFC3339Nano))
	} else if !errors.Is(e, os.ErrNotExist) {
		return e
	}
	if _, e = archiveSpace(dst, ".", info.Size()); e != nil {
		return e
	}
	if e = mkdir(dst, filepath.Dir(to)); e != nil {
		return e
	}
	if e = mkdir(dst, filepath.Dir(stage)); e != nil {
		return e
	}
	if e = dst.Remove(stage); e != nil && !errors.Is(e, os.ErrNotExist) {
		return e
	}
	defer dst.Remove(stage)
	out, e := dst.OpenFile(stage, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0660)
	if e != nil {
		return e
	}
	h := sha256.New()
	n, e := io.Copy(io.MultiWriter(out, h), io.LimitReader(archiveReader{ctx, input}, info.Size()+1))
	if e == nil && n != info.Size() {
		e = ErrConflict
	}
	if e == nil {
		e = out.Sync()
	}
	ce := out.Close()
	if e != nil {
		return e
	}
	if ce != nil {
		return ce
	}
	if hex.EncodeToString(h.Sum(nil)) != expected {
		return ErrConflict
	}
	latest, e := input.Stat()
	if e != nil || version(latest) != version(info) {
		return ErrConflict
	}
	hash, e := archiveHashContext(ctx, dst, stage)
	if e != nil {
		return e
	}
	if hash != expected {
		return ErrConflict
	}
	if e = normSetModified(dst, stage, info.ModTime().UTC().Format(time.RFC3339Nano)); e != nil {
		return e
	}
	if e = renameExclusive(dst, stage, to); e != nil {
		return e
	}
	return syncDir(dst, filepath.Dir(to))
}

func (s *Service) encodeArchiveMedia(ctx context.Context, input, directory *os.File, plan mediaPlan, limit int64) error {
	if s.config.ArchiveMediaSocket != "" {
		_, err := s.mediaCall(ctx, mediaRequest{Operation: "encode", Plan: plan, Limit: limit}, input, directory)
		return err
	}
	name := "output" + plan.Extension
	if _, e := input.Seek(0, 0); e != nil {
		return e
	}
	if plan.Copy && plan.Image {
		root, e := pinnedListingRoot(directory)
		if e != nil {
			return e
		}
		defer root.Close()
		out, e := root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0660)
		if e != nil {
			return e
		}
		n, e := io.Copy(out, io.LimitReader(archiveReader{ctx, input}, limit+1))
		if e == nil && n > limit {
			e = errNormalizationSpace
		}
		if e == nil {
			e = out.Sync()
		}
		ce := out.Close()
		if e != nil {
			return e
		}
		return ce
	}
	args := []string{"-hide_banner", "-nostdin", "-v", "error", "-threads", "1", "-filter_threads", "1"}
	args = append(args, mediaInputArgs()...)
	args = append(args, "-i", "/proc/self/fd/3")
	encoding := encodeArgs(plan)
	for i, arg := range encoding {
		if arg == "-threads" && i+1 < len(encoding) {
			encoding[i+1] = "1"
		}
		encoding[i] = strings.ReplaceAll(arg, "pools=4:frame-threads=2", "pools=none:frame-threads=1")
	}
	args = append(args, encoding...)
	args = append(args, "-threads", "1")
	args = append(args, "-fs", fmt.Sprint(limit), "/proc/self/fd/4/"+name)
	cmd := s.archiveMediaCommand(ctx, s.config.Normalization.defaults().FFmpeg, args...)
	cmd.ExtraFiles = []*os.File{input, directory}
	cmd.Dir = fmt.Sprintf("/proc/self/fd/%d", directory.Fd())
	cmd.Env = []string{"PATH=/usr/bin:/bin", "TMPDIR=/proc/self/fd/4", "HOME=/proc/self/fd/4"}
	cmd.Stdout = io.Discard
	var diagnostic limitedBuffer
	diagnostic.limit = 4096
	cmd.Stderr = &diagnostic
	if e := cmd.Run(); e != nil {
		_ = unix.Unlinkat(int(directory.Fd()), name, 0)
		if context.Cause(ctx) != nil {
			e = context.Cause(ctx)
		}
		return &archiveEncodingError{cause: e, diagnostic: string(diagnostic.data)}
	}
	return nil
}

type archiveEncodingError struct {
	cause      error
	diagnostic string
}

func (e *archiveEncodingError) Error() string {
	return fmt.Sprintf("archive encoding: %v: %s", e.cause, e.diagnostic)
}
func (e *archiveEncodingError) Unwrap() error { return e.cause }
func (e *archiveEncodingError) publicMessage() string {
	if errors.Is(e.cause, errArchiveMemory) {
		return "Waiting for memory"
	}
	diagnostic := strings.ToLower(e.diagnostic)
	if strings.Contains(diagnostic, "memory allocation failure") || strings.Contains(diagnostic, "cannot allocate memory") ||
		strings.Contains(diagnostic, "malloc of size") && strings.Contains(diagnostic, "failed") {
		return "Video encoder ran out of memory"
	}
	if errors.Is(e.cause, context.Canceled) {
		return "Video encoding interrupted; waiting to retry"
	}
	if errors.Is(e.cause, context.DeadlineExceeded) {
		return "Video encoding timed out"
	}
	var exit *exec.ExitError
	if errors.As(e.cause, &exit) {
		if status, ok := exit.Sys().(syscall.WaitStatus); ok && status.Signaled() {
			return "Video encoder crashed"
		}
	}
	return "Video encoding failed"
}
func (s *Service) validateArchiveMedia(ctx context.Context, root rootFS, path string, plan mediaPlan) error {
	f, e := filesystemOpenFile(root, path)
	if e != nil {
		return e
	}
	defer f.Close()
	return s.validateArchiveFile(ctx, f, plan)
}

func (s *Service) validateArchiveFile(ctx context.Context, f *os.File, plan mediaPlan) error {
	if s.config.ArchiveMediaSocket != "" {
		_, err := s.mediaCall(ctx, mediaRequest{Operation: "validate", Plan: plan}, f)
		return err
	}
	info, e := s.probeMedia(ctx, f, false)
	if e != nil {
		return e
	}
	out, e := makeMediaPlan(info, "output"+plan.Extension)
	if e != nil {
		return e
	}
	if plan.Image {
		if !(out.Video.Width == plan.Video.Width && out.Video.Height == plan.Video.Height || out.Video.Width == plan.Video.Height && out.Video.Height == plan.Video.Width) {
			return ErrConflict
		}
	} else {
		short, long := out.Video.Width, out.Video.Height
		if short > long {
			short, long = long, short
		}
		if out.Video.Codec != "hevc" || short <= 0 || short > 1080 || long > 2560 || math.Abs(out.Duration-plan.Duration) > .5 || (out.Audio != nil) != (plan.Audio != nil) {
			return ErrConflict
		}
		if out.Audio != nil && (out.Audio.Codec != "aac" || out.Audio.Channels > 2) {
			return ErrUnsupported
		}
	}
	if _, e = f.Seek(0, 0); e != nil {
		return e
	}
	args := []string{"-v", "error", "-nostdin", "-xerror", "-threads", "1"}
	args = append(args, mediaInputArgs()...)
	args = append(args, "-i", "/proc/self/fd/3", "-map", "0:v:0", "-map", "0:a:0?", "-f", "null", "-")
	cmd := s.archiveMediaCommand(ctx, s.config.Normalization.defaults().FFmpeg, args...)
	cmd.ExtraFiles = []*os.File{f}
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	return cmd.Run()
}
func archiveCommand(ctx context.Context, tool string, args ...string) *exec.Cmd {
	return archiveLimitedCommand(ctx, archiveEncoderDataLimit, tool, args...)
}

func (s *Service) archiveMediaCommand(ctx context.Context, tool string, args ...string) *exec.Cmd {
	if s.mediaWorker {
		return archiveLimitedCommand(ctx, mediaWorkerDataLimit, tool, args...)
	}
	return archiveCommand(ctx, tool, args...)
}

func archiveLimitedCommand(ctx context.Context, dataLimit uint64, tool string, args ...string) *exec.Cmd {
	// Prefer this restartable child over the API/mail/database if a sudden
	// spike reaches the kernel OOM killer before the one-second monitor reacts.
	priority := `read score < /proc/self/oom_score_adj || exit 1; if [ "$score" -lt 500 ]; then echo 500 > /proc/self/oom_score_adj || exit 1; fi; exec /usr/bin/nice -n 10 /usr/bin/ionice -c 3 "$@"`
	command := exec.CommandContext(ctx, "/usr/bin/prlimit", append([]string{"--as=2147483648", fmt.Sprintf("--data=%d", dataLimit), "--core=0", "--", "/bin/sh", "-c", priority, "archive-worker", tool}, args...)...)
	command.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGKILL}
	return command
}
func (s *Service) runArchiveIngest(ctx context.Context, j *archiveTask, encodeOnly bool) error {
	if e := validateArchiveTask(j); e != nil {
		return e
	}
	if j.State == "waiting" {
		if err := s.archiveAdmission(ctx, j.WaitingForMemory); err != nil {
			return err
		}
		j.WaitingForMemory = false
	}
	r, e := s.root(j.Library.Source)
	if e != nil {
		return e
	}
	defer r.Close()
	if e = archiveCheckRoot(r, j.Library.Path, j.Library.Vault, j.Library.Identity); e != nil {
		return e
	}
	base := filepath.Join(j.Library.Path, archiveStage, j.ID)
	raw := filepath.Join(base, "original")
	if j.State == "cleanup" {
		if e = r.RemoveAll(base); e != nil {
			return e
		}
		return s.removeArchiveTask(j)
	}
	var trash *archiveTask
	tasks, e := s.archiveTasks()
	if e != nil {
		return e
	}
	for i := range tasks {
		a := &tasks[i]
		if a.Link == j.Link && a.Kind == "trash" && a.TrashID == j.TrashID {
			var t Trash
			if s.read(a.TrashID, "trash", &t) == nil && t.State == "retained" {
				trash = a
				break
			}
		}
	}
	if (j.State == "waiting" || j.State == "trash-backup") && trash != nil {
		if encodeOnly {
			j.State = "trash-backup"
			j.Next = 0
			return s.saveArchiveTask(j)
		}
		// No reason to transcode a file already in Trash. Its exact original is
		// backed up, and VPS Trash cannot expire until that backup succeeds.
		var t Trash
		if e = s.read(trash.TrashID, "trash", &t); e != nil {
			return e
		}
		access, e := s.accessTrash(t)
		if e != nil {
			return e
		}
		defer access.close()
		from := trashPath(t)
		if trash.Directory {
			rel, _ := filepath.Rel(trash.Relative, j.Relative)
			from = filepath.Join(from, rel)
		}
		a, e := s.archiveWriter().root(j.Target.Backend)
		if e != nil {
			return e
		}
		defer a.Close()
		stopActivity := s.startArchiveActivity(j.ID, "copying")
		defer stopActivity()
		if e = s.copyArchiveOriginal(ctx, access.bin, from, a, filepath.Join(j.Target.Base, j.Original), "", filepath.Join(j.Target.Base, archiveStage, j.ID, "copy")); e != nil {
			return e
		}
		if e = a.RemoveAll(filepath.Join(j.Target.Base, archiveStage, j.ID)); e != nil {
			return e
		}
		return s.removeArchiveTask(j)
	}
	if j.State == "trash-backup" && trash == nil {
		j.State = "waiting"
		j.Next = time.Now().Add(archiveQuiet).Unix()
		return s.saveArchiveTask(j)
	}
	if j.State == "waiting" {
		path := filepath.Join(j.Library.Path, j.Relative)
		st, e := r.Stat(path)
		if errors.Is(e, os.ErrNotExist) {
			return ErrConflict
		}
		if e != nil {
			return e
		}
		if version(st) != j.Version {
			j.Version = version(st)
			j.Identity = moveIdentity(st)
			j.Size = st.Size()
			j.Next = time.Now().Add(archiveQuiet).Unix()
			j.Error = ""
			return s.saveArchiveTask(j)
		}
		if e = filenameConflict(r, path, path, false); e != nil {
			return e
		}
		if stat, ok := st.Sys().(*syscall.Stat_t); ok && stat.Nlink > 1 {
			return ErrConflict
		}
		limit, e := archiveSpace(r, j.Library.Path, 0)
		if e != nil {
			return e
		}
		if e = mkdir(r, base); e != nil {
			return e
		}
		if _, e = r.Stat(raw); e == nil {
			return ErrConflict
		} else if !errors.Is(e, os.ErrNotExist) {
			return e
		}
		staged, e := r.Open(base)
		if e != nil {
			return e
		}
		entries, e := staged.ReadDir(16)
		staged.Close()
		if e != nil && e != io.EOF {
			return e
		}
		for _, entry := range entries {
			if strings.HasPrefix(entry.Name(), "output.") {
				if e = r.Remove(filepath.Join(base, entry.Name())); e != nil {
					return e
				}
			}
		}
		input, e := filesystemOpenFile(r, path)
		if e != nil {
			return e
		}
		defer input.Close()
		stopActivity := s.startArchiveActivity(j.ID, "transcoding")
		defer stopActivity()
		probe, stop := context.WithTimeout(ctx, time.Minute)
		info, e := s.probeArchiveMedia(probe, input, false)
		stop()
		if e != nil {
			return e
		}
		plan, e := makeMediaPlan(info, j.Relative)
		if e != nil {
			return e
		}
		if plan.Image && (info.Format.Name == "png_pipe" || info.Format.Name == "webp_pipe") {
			probe, stop := context.WithTimeout(ctx, 2*time.Minute)
			counted, err := s.probeArchiveMedia(probe, input, true)
			stop()
			if err != nil {
				return err
			}
			plan, e = makeMediaPlan(counted, j.Relative)
			if e != nil {
				return e
			}
		}
		output := filepath.Join(base, "output"+plan.Extension)
		_ = r.Remove(output)
		directory, e := r.Open(base)
		if e != nil {
			return e
		}
		defer directory.Close()
		deadline, stopEncode := context.WithTimeout(ctx, 24*time.Hour)
		defer stopEncode()
		encode, cancel := context.WithCancelCause(deadline)
		defer cancel(nil)
		monitorDone := make(chan struct{})
		monitorStopped := make(chan struct{})
		go func() {
			defer close(monitorStopped)
			ticker := time.NewTicker(time.Second)
			defer ticker.Stop()
			for {
				select {
				case <-monitorDone:
					return
				case <-encode.Done():
					return
				case <-ticker.C:
					if s.config.ArchiveMediaSocket == "" && s.archiveMemory.cancelIfPressure(cancel) {
						return
					}
					latest, err := r.Stat(path)
					if err != nil || version(latest) != j.Version {
						cancel(nil)
						return
					}
					var current archiveTask
					if s.read(j.ID, archiveTaskKind, &current) != nil || current.Relative != j.Relative || current.State != "waiting" {
						cancel(nil)
						return
					}
					if _, err := archiveSpace(r, j.Library.Path, 0); err != nil {
						cancel(nil)
						return
					}
				}
			}
		}()
		e = s.encodeArchiveMedia(encode, input, directory, plan, limit)
		if e == nil {
			e = s.validateArchiveMedia(encode, r, output, plan)
		}
		close(monitorDone)
		<-monitorStopped
		if errors.Is(context.Cause(encode), errArchiveMemory) {
			e = errArchiveMemory
		}
		if e != nil {
			_ = r.Remove(output)
			return e
		}
		j.Hash, e = archiveHash(r, path)
		if e != nil {
			_ = r.Remove(output)
			return e
		}
		j.OutputHash, e = archiveHash(r, output)
		if e != nil {
			return e
		}
		j.Output = strings.TrimSuffix(j.Relative, filepath.Ext(j.Relative)) + plan.Extension
		archiveMu.Lock()
		s.fsMu.Lock()
		var current archiveTask
		e = s.read(j.ID, archiveTaskKind, &current)
		latest, statErr := r.Stat(path)
		if e == nil && (current.Relative != j.Relative || current.Version != j.Version || statErr != nil || version(latest) != j.Version) {
			e = ErrConflict
		}
		if e == nil {
			j.State = "publishing"
			j.Error = ""
			e = s.saveArchiveTask(j)
		}
		if e == nil {
			e = s.publishArchiveEncoded(r, j, base, raw)
		}
		s.fsMu.Unlock()
		archiveMu.Unlock()
		if e != nil {
			return e
		}
	}
	if j.State == "publishing" {
		archiveMu.Lock()
		s.fsMu.Lock()
		e = s.publishArchiveEncoded(r, j, base, raw)
		s.fsMu.Unlock()
		archiveMu.Unlock()
		if e != nil {
			return e
		}
	}
	if encodeOnly {
		return nil
	}
	if j.State != "backup" {
		return ErrConflict
	}
	for _, prior := range tasks {
		if prior.Link != j.Link || prior.State == "paired" {
			continue
		}
		if prior.Kind == "trash" && prior.TrashID == j.TrashID {
			continue
		}
		if (prior.Kind == "move" || prior.Kind == "trash") && (archiveTouches(j.Original, prior.Relative, prior.Directory) || prior.Kind == "move" && archiveTouches(j.Original, prior.To, prior.Directory)) {
			return ErrConflict
		}
	}
	a, e := s.archiveWriter().root(j.Target.Backend)
	if e != nil {
		return e
	}
	defer a.Close()
	if e = archiveCheckRoot(a, j.Target.Base, j.Target.VaultID, j.Target.Identity); e != nil {
		return e
	}
	// Refresh current routing just before copy. A concurrent move is journaled;
	// its archive action runs after this serial worker finishes the backup.
	var current archiveTask
	if e = s.read(j.ID, archiveTaskKind, &current); e != nil {
		return e
	}
	s.resolveTask(&current)
	target := filepath.Join(current.Target.Base, current.Original)
	stopActivity := s.startArchiveActivity(j.ID, "copying")
	defer stopActivity()
	if e = s.copyArchiveOriginal(ctx, r, raw, a, target, j.Hash, filepath.Join(j.Target.Base, archiveStage, j.ID, "copy")); e != nil {
		return e
	}
	var latestTask archiveTask
	if e = s.read(j.ID, archiveTaskKind, &latestTask); e != nil {
		return e
	}
	if latestTask.TrashID == "" {
		if e = s.archiveThumbnail(ctx, &current, r, a, filepath.Join(current.Library.Path, current.Relative), target); e != nil {
			return e
		}
	}
	if e = a.RemoveAll(filepath.Join(j.Target.Base, archiveStage, j.ID)); e != nil {
		return e
	}
	// Original deletion is permitted only after checksum-verified publication
	// and thumbnail publication. Interrupted cleanup remains a recoverable job.
	archiveMu.Lock()
	defer archiveMu.Unlock()
	j.State = "cleanup"
	j.Error = ""
	if e = s.saveArchiveTask(j); e != nil {
		return e
	}
	if e = r.Remove(raw); e != nil && !errors.Is(e, os.ErrNotExist) {
		return e
	}
	if e = r.RemoveAll(base); e != nil {
		return e
	}
	return s.removeArchiveTask(j)
}
func (s *Service) publishArchiveEncoded(root rootFS, j *archiveTask, base, raw string) error {
	from := filepath.Join(j.Library.Path, j.Relative)
	to := filepath.Join(j.Library.Path, j.Output)
	output := filepath.Join(base, "output"+filepath.Ext(j.Output))
	if _, e := root.Stat(raw); errors.Is(e, os.ErrNotExist) {
		st, e := root.Stat(from)
		if e != nil {
			return e
		}
		if version(st) != j.Version {
			return ErrConflict
		}
		if e = renameExclusive(root, from, raw); e != nil {
			return e
		}
		if e = syncDir(root, filepath.Dir(from)); e != nil {
			return e
		}
	} else if e != nil {
		return e
	}
	original, e := root.Stat(raw)
	if e != nil {
		return e
	}
	modified := original.ModTime().UTC().Format(time.RFC3339Nano)
	if _, e := root.Stat(to); errors.Is(e, os.ErrNotExist) {
		if e = normSetModified(root, output, modified); e != nil {
			return e
		}
		if e = renameExclusive(root, output, to); e != nil {
			if _, missing := root.Stat(from); errors.Is(missing, os.ErrNotExist) {
				_ = renameExclusive(root, raw, from)
			}
			return e
		}
		if e = syncDir(root, filepath.Dir(to)); e != nil {
			return e
		}
	} else if e != nil {
		return e
	} else {
		hash, e := archiveHash(root, to)
		if e != nil {
			return e
		}
		if hash != j.OutputHash {
			return ErrConflict
		}
		if e = normSetModified(root, to, modified); e != nil {
			return e
		}
	}
	j.Relative = j.Output
	visible, e := root.Stat(to)
	if e != nil {
		return e
	}
	j.Identity = moveIdentity(visible)
	j.State = "backup"
	j.Next = 0
	j.Error = ""
	return s.saveArchiveTask(j)
}
