package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"

	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

var errCancelled = errors.New("cancelled")

func (s *Service) checkpoint(j *Job) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	var saved Job
	if e := s.read(j.ID, "copy", &saved); e != nil {
		return e
	}
	if saved.Cancel {
		return errCancelled
	}
	return s.save(j.ID, "copy", j.State, j)
}
func (s *Service) discover(ctx context.Context, j *Job) error {
	complete := false
	defer func() {
		if !complete {
			j.Manifest = nil
			j.Total = 0
		}
	}()
	j.Manifest = nil
	j.Total = 0
	j.Done = 0
	dest, e := s.root(j.Request.Destination.Source)
	if e != nil {
		return e
	}
	defer dest.Close()
	p, e := clean(j.Request.Destination.Path)
	if e != nil || protected(p) {
		return ErrUnsafe
	}
	if e = safe(dest, p); e != nil {
		return e
	}
	st, e := dest.Stat(p)
	if e != nil || !st.IsDir() {
		return errors.New("destination folder is unavailable")
	}
	names := map[string]bool{}
	for _, item := range j.Request.Items {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		var files []File
		var name string
		if item.Source == "drive" {
			if j.Connection == "" {
				g, e := s.google()
				if e != nil {
					return e
				}
				j.Connection = g.Connection
			}
			files, name, e = s.driveManifest(ctx, item.Path, j.Connection)
			if e != nil {
				return e
			}
		} else {
			from, er := clean(item.Path)
			if er != nil || from == "." || from == "" || protected(from) {
				return ErrUnsafe
			}
			name = filepath.Base(from)
			if item.Source == j.Request.Destination.Source && within(filepath.Join(p, name), from) {
				return ErrUnsafe
			}
			root, er := s.root(item.Source)
			if er != nil {
				return er
			}
			er = safe(root, from)
			if er == nil {
				er = fs.WalkDir(root.FS(), from, func(path string, d fs.DirEntry, walkErr error) error {
					if walkErr != nil {
						return walkErr
					}
					if ctx.Err() != nil {
						return ctx.Err()
					}
					if d.Type()&os.ModeSymlink != 0 {
						return ErrUnsafe
					}
					info, e := d.Info()
					if e != nil {
						return e
					}
					if !info.IsDir() && !info.Mode().IsRegular() {
						return ErrUnsafe
					}
					rel, e := filepath.Rel(filepath.Dir(from), path)
					if e != nil {
						return e
					}
					files = append(files, File{From: path, To: rel, Size: info.Size(), Version: version(info), Directory: info.IsDir(), Source: item.Source})
					return nil
				})
			}
			root.Close()
			if er != nil {
				return er
			}
		}
		if !validName(name) || names[name] || name == "Trash" || name == ".filebeans-stage" {
			return ErrConflict
		}
		names[name] = true
		if _, e := dest.Lstat(filepath.Join(p, name)); e == nil {
			return ErrConflict
		} else if !errors.Is(e, fs.ErrNotExist) {
			return e
		}
		for _, f := range files {
			if !f.Directory {
				j.Total += f.Size
			}
			j.Manifest = append(j.Manifest, f)
		}
	}
	complete = true
	return nil
}
func (s *Service) runJob(ctx context.Context, j *Job) {
	e := s.execute(ctx, j)
	if ctx.Err() != nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var latest Job
	if s.read(j.ID, "copy", &latest) == nil && latest.Cancel && e != nil {
		e = errCancelled
	}
	if errors.Is(e, errCancelled) {
		if r, er := s.root(j.Request.Destination.Source); er == nil {
			r.RemoveAll(filepath.Join(".filebeans-stage", j.ID))
			r.Close()
		}
		j.State = "cancelled"
		j.Error = ""
	} else if e != nil {
		j.Error = publicError(e)
		j.Speed = 0
		if errors.Is(e, ErrUnsafe) || errors.Is(e, ErrConflict) || errors.Is(e, ErrUnsupported) || errors.Is(e, syscall.EEXIST) || errors.Is(e, syscall.ENOSPC) || errors.Is(e, fs.ErrPermission) || errors.Is(e, fs.ErrNotExist) {
			j.State = "failed"
		} else {
			j.State = "waiting"
			j.Attempts++
			delay := time.Second * 5 * time.Duration(1<<min(j.Attempts, 8))
			if errors.Is(e, ErrAuthorization) {
				delay = time.Minute * 5
			}
			var retry driveRetry
			if errors.As(e, &retry) && time.Duration(retry.seconds)*time.Second > delay {
				delay = time.Duration(retry.seconds) * time.Second
			}
			j.Next = time.Now().Add(delay).Unix()
		}
	} else {
		j.State = "completed"
		j.Speed = 0
		j.Error = ""
	}
	// Failure to save leaves the previous recoverable state, never an invented success.
	_ = s.save(j.ID, "copy", j.State, j)
}
func (s *Service) execute(ctx context.Context, j *Job) error {
	if len(j.Manifest) == 0 {
		j.State = "discovering"
		if e := s.checkpoint(j); e != nil {
			return e
		}
		if e := s.discover(ctx, j); e != nil {
			return e
		}
	}
	j.State = "running"
	j.Error = ""
	if e := s.checkpoint(j); e != nil {
		return e
	}
	dst, e := s.root(j.Request.Destination.Source)
	if e != nil {
		return e
	}
	defer dst.Close()
	stage := filepath.Join(".filebeans-stage", j.ID)
	if e = mkdir(dst, stage); e != nil {
		return e
	}
	published := func(name string) bool {
		for _, v := range j.Published {
			if name == v {
				return true
			}
		}
		return false
	}
	if strings.HasPrefix(j.Current, "publish:") {
		name := strings.TrimPrefix(j.Current, "publish:")
		if !validName(name) {
			return ErrUnsafe
		}
		if _, er := dst.Lstat(filepath.Join(stage, name)); errors.Is(er, fs.ErrNotExist) {
			if _, er = dst.Lstat(filepath.Join(j.Request.Destination.Path, name)); er == nil {
				if !published(name) {
					j.Published = append(j.Published, name)
				}
				if e = s.checkpoint(j); e != nil {
					return e
				}
			} else {
				return er
			}
		}
	}
	for i := range j.Manifest {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		f := &j.Manifest[i]
		top := strings.Split(f.To, "/")[0]
		if published(top) {
			continue
		}
		target := filepath.Join(stage, f.To)
		if f.Directory {
			if e = mkdir(dst, target); e != nil {
				return e
			}
			continue
		}
		j.Current = f.To
		if e = mkdir(dst, filepath.Dir(target)); e != nil {
			return e
		}
		if e = s.copyFile(ctx, j, f, dst, target); e != nil {
			return e
		}
	}
	names := map[string]bool{}
	for _, f := range j.Manifest {
		names[strings.Split(f.To, "/")[0]] = true
	}
	for name := range names {
		if published(name) {
			continue
		}
		j.Current = "publish:" + name
		if e = s.checkpoint(j); e != nil {
			return e
		}
		if e = accessible(dst, filepath.Join(stage, name)); e != nil {
			return e
		}
		if e = renameExclusive(dst, filepath.Join(stage, name), filepath.Join(j.Request.Destination.Path, name)); e != nil {
			return e
		}
		if e = syncDir(dst, j.Request.Destination.Path); e != nil {
			return e
		}
		j.Published = append(j.Published, name)
		if e = s.checkpoint(j); e != nil {
			return e
		}
	}
	_ = dst.RemoveAll(stage)
	return nil
}
func (s *Service) copyFile(ctx context.Context, j *Job, f *File, dst *os.Root, target string) error {
	if e := safe(dst, target); e != nil {
		return e
	}
	out, e := dst.OpenFile(target, os.O_CREATE|os.O_RDWR, 0600)
	if e != nil {
		return e
	}
	defer out.Close()
	st, e := out.Stat()
	if e != nil {
		return e
	}
	offset := min(st.Size(), f.Done)
	var in io.ReadCloser
	if f.Source == "drive" {
		// Verify the saved prefix hash locally and pin the remote version before ranges.
		if offset > 0 {
			h := sha256.New()
			if _, e = io.CopyN(h, out, offset); e != nil {
				return e
			}
			if hex.EncodeToString(h.Sum(nil)) != f.Hash {
				offset = 0
			}
		}
		meta, er := s.driveMeta(ctx, f.From, j.Connection)
		if er != nil {
			return er
		}
		if meta.Version != f.Version {
			return ErrConflict
		}
		if offset == f.Size {
			in = io.NopCloser(strings.NewReader(""))
		} else {
			in, e = s.driveContent(ctx, f.From, offset, j.Connection)
		}
	} else {
		src, er := s.root(f.Source)
		if er != nil {
			return er
		}
		defer src.Close()
		if er = safe(src, f.From); er != nil {
			return er
		}
		input, er := src.Open(f.From)
		if er != nil {
			return er
		}
		info, er := input.Stat()
		if er != nil || version(info) != f.Version {
			input.Close()
			return ErrConflict
		}
		if offset > 0 {
			a, b := sha256.New(), sha256.New()
			_, er = io.CopyN(a, input, offset)
			if er == nil {
				_, er = out.Seek(0, 0)
			}
			if er == nil {
				_, er = io.CopyN(b, out, offset)
			}
			if er != nil || !strings.EqualFold(hex.EncodeToString(a.Sum(nil)), hex.EncodeToString(b.Sum(nil))) {
				offset = 0
			}
		}
		_, e = input.Seek(offset, 0)
		if e != nil {
			input.Close()
			return e
		}
		in = input
	}
	if e != nil {
		return e
	}
	defer in.Close()
	if e = out.Truncate(offset); e != nil {
		return e
	}
	if _, e = out.Seek(offset, 0); e != nil {
		return e
	}
	j.Done += offset - f.Done
	f.Done = offset
	h := sha256.New()
	if offset > 0 {
		if _, e = out.Seek(0, 0); e != nil {
			return e
		}
		if _, e = io.CopyN(h, out, offset); e != nil {
			return e
		}
	}
	if _, e = out.Seek(offset, 0); e != nil {
		return e
	}
	buf := make([]byte, 1024*1024)
	last := time.Now()
	lastDone := f.Done
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		n, er := in.Read(buf)
		if n > 0 {
			if f.Done+int64(n) > f.Size {
				return ErrConflict
			}
			written, we := out.Write(buf[:n])
			if we != nil {
				return we
			}
			if written != n {
				return io.ErrShortWrite
			}
			h.Write(buf[:n])
			f.Done += int64(n)
			j.Done += int64(n)
			if time.Since(last) >= time.Second || f.Done == f.Size {
				if e = out.Sync(); e != nil {
					return e
				}
				f.Hash = hex.EncodeToString(h.Sum(nil))
				j.Speed = float64(f.Done-lastDone) / time.Since(last).Seconds()
				if e = s.checkpoint(j); e != nil {
					return e
				}
				last = time.Now()
				lastDone = f.Done
				if f.Source != "drive" {
					r, re := s.root(f.Source)
					if re != nil {
						return re
					}
					r.Close()
				}
			}
		}
		if er == io.EOF {
			break
		}
		if er != nil {
			return er
		}
	}
	if f.Done != f.Size {
		return io.ErrUnexpectedEOF
	}
	if f.Source == "drive" {
		meta, er := s.driveMeta(ctx, f.From, j.Connection)
		if er != nil {
			return er
		}
		if meta.Version != f.Version {
			return ErrConflict
		}
	} else {
		r, er := s.root(f.Source)
		if er != nil {
			return er
		}
		defer r.Close()
		info, er := r.Stat(f.From)
		if er != nil {
			return er
		}
		if version(info) != f.Version {
			return ErrConflict
		}
	}
	if e = out.Sync(); e != nil {
		return e
	}
	f.Hash = hex.EncodeToString(h.Sum(nil))
	return s.checkpoint(j)
}
func publicError(e error) string {
	switch {
	case errors.Is(e, ErrUnsupported):
		return "Unsupported Drive item or download not permitted"
	case errors.Is(e, ErrUnsafe):
		return "Unsafe or unsupported path"
	case errors.Is(e, ErrConflict), errors.Is(e, syscall.EEXIST):
		return "Destination exists or source changed"
	case errors.Is(e, ErrMount):
		return "Samba mount unavailable; waiting to retry"
	case errors.Is(e, ErrAuthorization):
		return "Reconnect Google Drive"
	case errors.Is(e, syscall.ENOSPC):
		return "Insufficient destination space"
	case errors.Is(e, fs.ErrPermission):
		return "Storage permission denied"
	case errors.Is(e, fs.ErrNotExist):
		return "Source or destination is missing"
	default:
		return "Operation interrupted; check storage, database, or network connectivity"
	}
}
func decodeJob(b []byte) (Job, error) { var j Job; e := json.Unmarshal(b, &j); return j, e }
