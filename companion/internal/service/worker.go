package service

import (
	"context"
	"encoding/json"
	"errors"

	"io/fs"
	"os"
	"path/filepath"
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
	if s.config.Profile == "pi" && !piHasHeadroom() {
		return errPiPressure
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
	if e := s.guardArchiveJob(j.Request); e != nil {
		return e
	}
	j.Manifest = nil
	j.Total = 0
	j.Done = 0
	var dest rootFS
	p := ""
	var e error
	if j.Request.Destination.Source == "drive" {
		if e = s.requireDriveWrite(j.Connection); e != nil {
			return e
		}
		if _, e = s.driveParent(ctx, j.Request.Destination.Path, j.Connection); e != nil {
			return e
		}
	} else {
		dest, e = s.root(j.Request.Destination.Source)
		if e != nil {
			return e
		}
		defer dest.Close()
		p, e = clean(j.Request.Destination.Path)
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
			files, name, e = s.driveTree(ctx, item.Path, j.Connection, j.Request.Destination.Source == "drive")
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
			if guarded, ok := root.(*guardedRoot); er == nil && ok {
				er = guarded.readTree(from)
			}
			if er == nil {
				er = fs.WalkDir(root.FS(), from, func(path string, d fs.DirEntry, walkErr error) error {
					if walkErr == nil && filesystemHidden(path) {
						if d.IsDir() {
							return fs.SkipDir
						}
						return nil
					}
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
		if !validName(name) || names[name] || (j.Request.Destination.Source != "drive" && (name == "Trash" || name == ".filebeans-stage")) {
			return ErrConflict
		}
		names[name] = true
		if dest != nil {
			if _, e := dest.Lstat(filepath.Join(p, name)); e == nil {
				return ErrConflict
			} else if !errors.Is(e, fs.ErrNotExist) {
				return e
			}
		} else {
			if e = s.driveNameConflict(ctx, j.Request.Destination.Path, name, "", j.Connection); e != nil {
				return e
			}
		}
		for _, f := range files {
			if !f.Directory {
				j.Total += f.Size
			}
			j.Manifest = append(j.Manifest, f)
		}
	}
	if e = validateManifestFilenames(j.Manifest); e != nil {
		return e
	}
	j.ItemsTotal = len(j.Manifest)
	if j.Request.Destination.Source == "drive" {
		j.ProgressUnit = "bytes"
		if len(j.Request.Items) > 0 && j.Request.Items[0].Source == "drive" {
			j.ProgressUnit = "items"
		}
	}
	complete = true
	return nil
}
func (s *Service) runJob(ctx context.Context, j *Job) {
	if s.config.Profile == "pi" {
		s.piBulk.Lock()
		defer s.piBulk.Unlock()
	}
	var preparationErr error
	if j.Request.Operation == "move" && (j.Move == nil || !j.Move.Committed) {
		j.State = "preparing"
		preparationErr = s.checkpoint(j)
		if preparationErr != nil && !errors.Is(preparationErr, errCancelled) {
			return
		}
	}
	s.vaultGate.RLock()
	defer s.vaultGate.RUnlock()
	if s.vaults != nil && s.vaults.guard != nil {
		lease, e := s.vaults.guard.Acquire()
		if e != nil {
			return
		}
		defer lease.Close()
	}
	copyCtx, stop := context.WithCancel(ctx)
	if s.config.Profile == "pi" {
		copyCtx = context.WithValue(copyCtx, piPaceKey{}, s.piPace)
	}
	s.mu.Lock()
	if s.copyCancels == nil {
		s.copyCancels = make(map[string]context.CancelFunc)
	}
	s.copyCancels[j.ID] = stop
	s.mu.Unlock()
	defer func() {
		stop()
		s.mu.Lock()
		delete(s.copyCancels, j.ID)
		s.mu.Unlock()
	}()
	e := preparationErr
	if e == nil {
		e = s.execute(copyCtx, j)
	}
	if e == nil {
		e = s.archivePublished(j)
	}
	if ctx.Err() != nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var latest Job
	if s.read(j.ID, "copy", &latest) == nil && latest.Cancel && e != nil && (j.Move == nil || !j.Move.Committed) {
		e = errCancelled
	}
	if errors.Is(e, errCancelled) {
		if j.Request.Destination.Source == "drive" {
			if cleanupError := s.cleanupDriveStage(ctx, j); cleanupError != nil {
				j.Error = publicError(cleanupError)
				j.State = "waiting"
				j.Cancel = true
				j.Next = time.Now().Add(time.Minute).Unix()
				_ = s.save(j.ID, "copy", j.State, j)
				return
			}
		} else if r, er := s.root(j.Request.Destination.Source); er == nil {
			r.RemoveAll(s.stagePath(j))
			r.Close()
		}
		j.State = "cancelled"
		j.Error = ""
	} else if e != nil {
		j.Error = publicError(e)
		j.Speed = 0
		var engineFailure *copyEngineError
		if errors.As(e, &engineFailure) || errors.Is(e, ErrDrivePermission) || errors.Is(e, ErrDriveAccount) || errors.Is(e, ErrUnsafe) || errors.Is(e, ErrConflict) || errors.Is(e, ErrUnsupported) || errors.Is(e, syscall.EEXIST) || errors.Is(e, syscall.ENOSPC) || errors.Is(e, syscall.EXDEV) || errors.Is(e, syscall.EBUSY) || errors.Is(e, fs.ErrPermission) || errors.Is(e, fs.ErrNotExist) {
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
	if s.save(j.ID, "copy", j.State, j) == nil && (j.State == "completed" || j.State == "cancelled") {
		s.cleanupJobUploads(j)
	}
}
func (s *Service) execute(ctx context.Context, j *Job) error {
	if e := s.guardArchiveJob(j.Request); e != nil {
		return e
	}
	if s.crossPeer(j.Request) || s.config.Profile == "pi" && j.Request.Operation != "move" {
		return s.executePeer(ctx, j)
	}
	if j.Request.Operation == "move" {
		return s.executeMove(ctx, j)
	}
	if j.Request.Destination.Source == "drive" {
		return s.executeDrive(ctx, j)
	}
	return s.executeRclone(ctx, j)
}
func publicError(e error) string {
	var mediaFailure *mediaWorkerError
	if errors.As(e, &mediaFailure) {
		return mediaFailure.Error()
	}
	var encodingFailure *archiveEncodingError
	if errors.As(e, &encodingFailure) {
		return encodingFailure.publicMessage()
	}
	var archiveError *archiveReadOnlyError
	if errors.As(e, &archiveError) {
		return archiveError.Error()
	}
	var engineFailure *copyEngineError
	if errors.As(e, &engineFailure) {
		return engineFailure.Error()
	}
	switch {
	case errors.Is(e, ErrArchiveLinked):
		return "Unlink the archive before deleting or moving this folder across sources"
	case errors.Is(e, errVault):
		return "Protected-folder setup or mount failed; check server configuration"
	case errors.Is(e, ErrVaultLocked):
		return "Protected folder is locked"
	case errors.Is(e, ErrVaultKey):
		return "The encryption key does not match this server's protected folders"
	case errors.Is(e, ErrVaultBusy):
		return "Protected folder is busy. Try again."
	case errors.Is(e, ErrDriveWrite):
		return "Google Drive needs full access. Choose Upgrade Drive Access and approve the requested permission."
	case errors.Is(e, ErrDriveAccount):
		return "The Google Drive account changed. Cancel this transfer and submit it again for the connected account."
	case errors.Is(e, ErrDrivePermission):
		return "Google Drive does not allow this action for this item. Check its sharing permissions."
	case errors.Is(e, ErrUncertain):
		return "Google has not confirmed the operation. Waiting to reconcile it before retrying."
	case errors.Is(e, ErrUnsupported):
		return "Unsupported Drive item or download not permitted"
	case errors.Is(e, syscall.EXDEV), errors.Is(e, syscall.EBUSY):
		return "Cannot move across a nested mount boundary; choose a folder on the configured storage filesystem"
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
