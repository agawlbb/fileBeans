package service

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// FileBeans owns selection boundaries and publication. Rclone owns the data
// transfer, traversal, checksums, concurrency, retries and partial files.
type copyEngineError struct{ detail string }

func (e *copyEngineError) Error() string { return "Copy failed: " + e.detail }

type rcloneEvent struct {
	Level   string `json:"level"`
	Message string `json:"msg"`
	Object  string `json:"object"`
	Stats   *struct {
		Bytes          int64   `json:"bytes"`
		TotalBytes     int64   `json:"totalBytes"`
		Speed          float64 `json:"speed"`
		Transfers      int     `json:"transfers"`
		TotalTransfers int     `json:"totalTransfers"`
		Checks         int     `json:"checks"`
		TotalChecks    int     `json:"totalChecks"`
		Transferring   []struct {
			Name string `json:"name"`
		} `json:"transferring"`
	} `json:"stats"`
}

func (s *Service) executeRclone(ctx context.Context, j *Job) error {
	if err := s.checkpoint(j); err != nil {
		return err
	}
	dst, err := s.root(j.Request.Destination.Source)
	if err != nil {
		return err
	}
	defer dst.Close()
	destination, err := clean(j.Request.Destination.Path)
	if err != nil || protected(destination) {
		return ErrUnsafe
	}
	if err = safe(dst, destination); err != nil {
		return err
	}
	info, err := dst.Stat(destination)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return ErrUnsafe
	}
	stage := s.stagePath(j)
	if err = mkdir(dst, stage); err != nil {
		return err
	}
	// Recover a rename acknowledged by storage but not yet by the database.
	if strings.HasPrefix(j.Current, "publish:") {
		name := strings.TrimPrefix(j.Current, "publish:")
		if !validName(name) {
			return ErrUnsafe
		}
		if _, err = dst.Lstat(filepath.Join(stage, name)); errors.Is(err, fs.ErrNotExist) {
			if _, err = dst.Lstat(filepath.Join(destination, name)); err != nil {
				return err
			}
			if !contains(j.Published, name) {
				j.Published = append(j.Published, name)
			}
		} else if err != nil {
			return err
		}
	}
	if j.Engine != "rclone" {
		j.State = "discovering"
		if err = s.checkpoint(j); err != nil {
			return err
		}
		plan := make([]File, 0, len(j.Request.Items))
		old := map[string]File{}
		for _, f := range j.Manifest {
			if filepath.Dir(f.To) == "." {
				old[f.Source+"\x00"+strings.TrimPrefix(f.From, "/")] = f
			}
		}
		for _, item := range j.Request.Items {
			f := File{From: item.Path, Source: item.Source}
			previous, ok := old[item.Source+"\x00"+strings.TrimPrefix(item.Path, "/")]
			if ok && contains(j.Published, previous.To) {
				plan = append(plan, previous)
				continue
			}
			if item.Source == "drive" {
				if j.Connection == "" {
					grant, err := s.google()
					if err != nil {
						return err
					}
					j.Connection = grant.Connection
				}
				meta, err := s.driveMeta(ctx, item.Path, j.Connection)
				if err != nil {
					return err
				}
				f.DriveID = meta.DriveID
				f.To = meta.Name
				f.Directory = meta.Mime == "application/vnd.google-apps.folder"
				if !f.Directory && (strings.HasPrefix(meta.Mime, "application/vnd.google-apps.") || !meta.Capabilities.Download) {
					return ErrUnsupported
				}
			} else {
				f.From, err = clean(item.Path)
				if err != nil || f.From == "." || protected(f.From) {
					return ErrUnsafe
				}
				if item.Source == j.Request.Destination.Source && within(filepath.Join(destination, filepath.Base(f.From)), f.From) {
					return ErrUnsafe
				}
				root, err := s.root(item.Source)
				if err != nil {
					return err
				}
				err = safe(root, f.From)
				if guarded, ok := root.(*guardedRoot); err == nil && ok {
					err = guarded.readTree(f.From)
				}
				var stat fs.FileInfo
				if err == nil {
					stat, err = root.Stat(f.From)
				}
				root.Close()
				if err != nil {
					return err
				}
				if !stat.IsDir() && !stat.Mode().IsRegular() {
					return ErrUnsafe
				}
				f.To = filepath.Base(f.From)
				f.Directory = stat.IsDir()
			}
			if j.Request.DestinationName != "" {
				f.To = j.Request.DestinationName
			}
			if !validName(f.To) || f.To == "Trash" || f.To == ".filebeans-stage" {
				return ErrUnsafe
			}
			for _, other := range plan {
				if strings.EqualFold(f.To, other.To) {
					return ErrConflict
				}
			}
			plan = append(plan, f)
		}
		// Old per-file recovery records are replaced with one entry per selection;
		// the existing staging itself is retained for rclone to reconcile.
		j.RclonePlan = plan
		j.Manifest = nil
		j.Engine = "rclone"
	}
	for _, f := range j.RclonePlan {
		if contains(j.Published, f.To) {
			continue
		}
		if _, err = dst.Lstat(filepath.Join(destination, f.To)); err == nil {
			return ErrConflict
		} else if !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}
	j.Done = 0
	j.Total = 0
	j.ItemsDone = 0
	j.ItemsTotal = 0
	j.State = "running"
	j.Error = ""
	j.ProgressUnit = "bytes"
	if err = s.checkpoint(j); err != nil {
		return err
	}
	for i := range j.RclonePlan {
		f := &j.RclonePlan[i]
		if contains(j.Published, f.To) {
			continue
		}
		if err = ctx.Err(); err != nil {
			return err
		}
		j.Current = f.To
		// Rclone's own comparison reconciles completed and interrupted staging on
		// every attempt. Never assume a database flag makes filesystem bytes valid.
		if err = s.runRclone(ctx, j, *f, dst, stage); err != nil {
			return err
		}
		j.Current = "publish:" + f.To
		if err = s.checkpoint(j); err != nil {
			return err
		}
		if err = renameExclusive(dst, filepath.Join(stage, f.To), filepath.Join(destination, f.To)); err != nil {
			return err
		}
		if err = syncDir(dst, destination); err != nil {
			return err
		}
		j.Published = append(j.Published, f.To)
		if err = s.checkpoint(j); err != nil {
			return err
		}
	}
	return dst.RemoveAll(stage)
}

func (s *Service) runRclone(ctx context.Context, j *Job, f File, dst rootFS, stage string) error {
	binary := s.config.Rclone
	if binary == "" {
		binary = "rclone"
	}
	if _, err := exec.LookPath(binary); err != nil {
		return &copyEngineError{"rclone is not installed on the server"}
	}
	workspace := s.config.State
	if s.vaults != nil {
		// Configuration, caches and any rclone temporary bytes for a protected
		// transfer belong on an encrypted filesystem, even for exports.
		for _, v := range s.vaults.configs() {
			matches := func(source, path string) bool {
				p, e := clean(path)
				return e == nil && source == v.Source && (v.Path == "" || within("/"+p, v.Path))
			}
			touched := matches(j.Request.Destination.Source, j.Request.Destination.Path)
			for _, item := range j.Request.Items {
				touched = touched || matches(item.Source, item.Path)
			}
			if touched {
				if err := s.vaults.checkRoot(v.Source); err != nil {
					return err
				}
				workspace = filepath.Join(v.Mount, "private", "tmp")
				break
			}
		}
	}
	private, err := os.MkdirTemp(workspace, "rclone-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(private)
	config := filepath.Join(private, "rclone.conf")
	if err = os.WriteFile(config, nil, 0600); err != nil {
		return err
	}
	if err = safe(dst, stage); err != nil {
		return err
	}
	target, err := dst.Open(stage)
	if err != nil {
		return err
	}
	defer target.Close()
	files := []*os.File{target}
	env := []string{}
	for _, key := range []string{"PATH", "TZ", "SSL_CERT_FILE", "SSL_CERT_DIR", "HTTP_PROXY", "HTTPS_PROXY", "NO_PROXY"} {
		if value, ok := os.LookupEnv(key); ok {
			env = append(env, key+"="+value)
		}
	}
	env = append(env, "RCLONE_CONFIG_TO_TYPE=local", "XDG_CONFIG_HOME="+private, "XDG_CACHE_HOME="+private)
	for _, root := range s.config.Roots {
		if root.Name == j.Request.Destination.Source && root.Mount != "" {
			env = append(env, "RCLONE_CONFIG_TO_CASE_INSENSITIVE=true")
		}
	}
	args := []string{"--config", config, "--cache-dir", private, "--temp-dir", private, "--use-json-log", "--stats", "1s", "--stats-log-level", "NOTICE", "--log-level", "INFO", "--fast-list"}
	// CIFS can expose fixed ownership through FUSE. Group writers can write
	// bytes but cannot set another UID's timestamps. Content verification remains
	// authoritative; do not make protected transfers depend on chtimes.
	if s.vaults.pathVault(j.Request.Destination.Source, j.Request.Destination.Path) != nil {
		args = append(args, "--local-no-set-modtime")
	}
	// Copy into private staging. Rclone's ordinary copy semantics preserve
	// existing staged files and never delete files that disappeared at source.
	operation := "copy"
	if !f.Directory {
		operation = "copyto"
	}
	destination := "to:/proc/self/fd/3/" + f.To
	var source string
	secrets := []string{s.config.Token}
	if f.Source == "drive" {
		grant, err := s.token(ctx, j.Connection)
		if err != nil {
			return err
		}
		token, _ := json.Marshal(map[string]any{"access_token": grant.Access, "refresh_token": grant.Refresh, "token_type": "Bearer", "expiry": time.Unix(grant.Expires, 0).UTC().Format(time.RFC3339)})
		secrets = append(secrets, grant.Access, grant.Refresh, s.config.GoogleClientSecret)
		env = append(env, "RCLONE_CONFIG_FROM_TYPE=drive", "RCLONE_CONFIG_FROM_CLIENT_ID="+s.config.GoogleClientID, "RCLONE_CONFIG_FROM_CLIENT_SECRET="+s.config.GoogleClientSecret, "RCLONE_CONFIG_FROM_TOKEN="+string(token), "RCLONE_CONFIG_FROM_SHOW_ALL_GDOCS=true")
		if f.DriveID != "" {
			env = append(env, "RCLONE_CONFIG_FROM_TEAM_DRIVE="+f.DriveID)
		}
		if f.Directory {
			env = append(env, "RCLONE_CONFIG_FROM_ROOT_FOLDER_ID="+f.From)
			source = "from:"
		} else {
			// copyid is rclone's supported ID-based operation; no ambiguous pathname
			// lookup or per-file metadata wrapper is required.
			args = append(args, "backend", "copyid", "from:", "--", f.From, destination)
		}
	} else {
		root, err := s.root(f.Source)
		if err != nil {
			return err
		}
		err = safe(root, f.From)
		if guarded, ok := root.(*guardedRoot); err == nil && ok {
			err = guarded.readTree(f.From)
		}
		var handle *os.File
		if err == nil {
			handle, err = root.Open(".")
		}
		root.Close()
		if err != nil {
			return err
		}
		defer handle.Close()
		files = append(files, handle)
		source = "/proc/self/fd/4/" + f.From
	}
	if source != "" {
		args = append(args, operation)
		if f.Directory {
			args = append(args, "--create-empty-src-dirs")
		}
		args = append(args, "--", source, destination)
	}
	child, cancel := context.WithCancel(ctx)
	defer cancel()
	command := exec.CommandContext(child, binary, args...)
	command.Dir = private
	command.Env = env
	command.ExtraFiles = files
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	command.Cancel = func() error { return syscall.Kill(-command.Process.Pid, syscall.SIGTERM) }
	command.WaitDelay = 5 * time.Second
	pipe, err := command.StderrPipe()
	if err != nil {
		return err
	}
	if err = command.Start(); err != nil {
		return &copyEngineError{"rclone could not start"}
	}
	baseDone, baseTotal, baseItems := j.Done, j.Total, j.ItemsDone
	scanner := bufio.NewScanner(pipe)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	var reportErr error
	detail := "rclone stopped; retry to continue the staged copy"
	last := time.Time{}
	for scanner.Scan() {
		var event rcloneEvent
		if json.Unmarshal(scanner.Bytes(), &event) != nil {
			if strings.HasPrefix(detail, "rclone stopped") && len(scanner.Bytes()) > 0 {
				detail = redactCopyError(scanner.Text(), secrets)
			}
			continue
		}
		if event.Level == "error" || event.Level == "fatal" {
			message := event.Message
			if event.Object != "" {
				message = event.Object + ": " + message
			}
			if event.Object != "" || strings.HasPrefix(detail, "rclone stopped") {
				detail = redactCopyError(message, secrets)
			}
		}
		lower := strings.ToLower(event.Message)
		// Rclone normally warns and skips these. A copy must not silently omit a
		// symlink or one of two source objects with the same destination name.
		if strings.Contains(lower, "duplicate object") || strings.Contains(lower, "duplicate directory") || strings.Contains(lower, "can't follow symlink") {
			reportErr = &copyEngineError{redactCopyError(event.Object+": "+event.Message, secrets)}
			cancel()
		}
		if event.Stats != nil {
			stats := event.Stats
			j.Done = baseDone + stats.Bytes
			j.Total = baseTotal + stats.TotalBytes
			j.Speed = stats.Speed
			j.ItemsDone = baseItems + stats.Transfers + stats.Checks
			j.ItemsTotal = baseItems + stats.TotalTransfers + stats.TotalChecks
			if len(stats.Transferring) > 0 {
				j.Current = f.To + "/" + stats.Transferring[0].Name
			} else {
				j.Current = f.To
			}
			if time.Since(last) >= time.Second && reportErr == nil {
				if f.Source == "drive" {
					grant, err := s.google()
					if err != nil {
						reportErr = err
					} else if grant.Connection != j.Connection {
						reportErr = ErrDriveAccount
					}
				}
				if reportErr == nil {
					reportErr = s.checkpoint(j)
				}
				if reportErr != nil {
					cancel()
				}
				last = time.Now()
			}
		}
	}
	scanErr := scanner.Err()
	if scanErr != nil {
		cancel()
	}
	waitErr := command.Wait()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if reportErr != nil {
		return reportErr
	}
	if scanErr != nil {
		return &copyEngineError{"rclone progress stream was interrupted"}
	}
	if f.Source == "drive" {
		grant, err := s.google()
		if err != nil {
			return err
		}
		if grant.Connection != j.Connection {
			return ErrDriveAccount
		}
	}
	if waitErr != nil {
		return &copyEngineError{detail}
	}
	return s.checkpoint(j)
}

func redactCopyError(message string, secrets []string) string {
	for _, secret := range secrets {
		if secret != "" {
			message = strings.ReplaceAll(message, secret, "[redacted]")
		}
	}
	message = strings.Join(strings.Fields(message), " ")
	if len(message) > 700 {
		message = message[:700]
	}
	return message
}
