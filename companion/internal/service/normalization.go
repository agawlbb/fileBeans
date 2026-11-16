package service

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

const normalizationProfile = "ipad-hevc18-v1"

type NormalizationConfig struct {
	Enabled       bool   `json:"enabled"`
	Source        string `json:"source"`
	Path          string `json:"path"`
	DriveFolder   string `json:"driveFolder,omitempty"`
	MinFreeBytes  uint64 `json:"minFreeBytes,omitempty"`
	StableSeconds int64  `json:"stableSeconds,omitempty"`
	FFmpeg        string `json:"ffmpeg,omitempty"`
	FFprobe       string `json:"ffprobe,omitempty"`
}

func (c NormalizationConfig) defaults() NormalizationConfig {
	if c.Source == "" {
		c.Source = "VPS"
	}
	if c.Path == "" {
		c.Path = "pics"
	}
	if c.MinFreeBytes == 0 {
		c.MinFreeBytes = 2 << 30
	}
	if c.StableSeconds == 0 {
		c.StableSeconds = 60
	}
	if c.FFmpeg == "" {
		c.FFmpeg = "/usr/bin/ffmpeg"
	}
	if c.FFprobe == "" {
		c.FFprobe = "/usr/bin/ffprobe"
	}
	return c
}

type NormalizationRequest struct {
	Source       string `json:"source"`
	Path         string `json:"path"`
	RelativePath string `json:"relativePath,omitempty"`
}
type NormalizationJob struct {
	Recoveries    int                  `json:"recoveries,omitempty"`
	LibrarySource string               `json:"librarySource"`
	LibraryPath   string               `json:"libraryPath"`
	ID            string               `json:"id"`
	Request       NormalizationRequest `json:"request"`
	State         string               `json:"state"`
	Phase         string               `json:"phase"`
	Profile       string               `json:"profile"`
	Batch         string               `json:"batch,omitempty"`
	Connection    string               `json:"connection,omitempty"`
	SourceVersion string               `json:"sourceVersion,omitempty"`
	SourceSize    int64                `json:"sourceSize"`
	ExpectedSHA   string               `json:"expectedSHA,omitempty"`
	ExpectedMD5   string               `json:"expectedMD5,omitempty"`
	SourceHash    string               `json:"sourceHash,omitempty"`
	OutputHash    string               `json:"outputHash,omitempty"`
	OutputPath    string               `json:"outputPath,omitempty"`
	OutputVersion string               `json:"outputVersion,omitempty"`
	OutputSize    int64                `json:"outputSize"`
	RawMoved      bool                 `json:"rawMoved,omitempty"`
	Published     bool                 `json:"published,omitempty"`
	Done          int64                `json:"done"`
	Total         int64                `json:"total"`
	Error         string               `json:"error,omitempty"`
	Next          int64                `json:"next,omitempty"`
	Attempts      int                  `json:"attempts,omitempty"`
	Created       int64                `json:"created"`
}
type NormalizationBatch struct {
	ID         string               `json:"id"`
	Request    NormalizationRequest `json:"request"`
	Connection string               `json:"connection"`
	State      string               `json:"state"`
	Children   []string             `json:"children"`
	Error      string               `json:"error,omitempty"`
}

var errNormalizationSpace = errors.New("normalization disk reserve reached")
var errNormalizationBlocked = errors.New("normalization workspace contains an unpublished original")

func normKey(parts ...string) string {
	h := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return "norm:" + hex.EncodeToString(h[:])[:59]
}
func (s *Service) normalizationRoot() (*os.Root, NormalizationConfig, error) {
	c := s.config.Normalization.defaults()
	if !c.Enabled {
		return nil, c, ErrUnsupported
	}
	for _, r := range s.config.Roots {
		if r.Name == c.Source && r.Mount != "" {
			return nil, c, ErrUnsafe
		}
	}
	root, e := s.root(c.Source)
	return root, c, e
}
func (s *Service) enqueueNormalization(req NormalizationRequest, batch, connection string, meta *DriveItem) (NormalizationJob, error) {
	c := s.config.Normalization.defaults()
	j := NormalizationJob{ID: ID(), Request: req, State: "queued", Phase: "queued", Profile: normalizationProfile, LibrarySource: c.Source, LibraryPath: c.Path, Batch: batch, Connection: connection, Created: time.Now().Unix()}
	if req.Source == "drive" {
		if meta == nil || meta.Trashed || !meta.Capabilities.Download || strings.HasPrefix(meta.Mime, "application/vnd.google-apps.") {
			return j, ErrUnsupported
		}
		if !validName(filepath.Base(req.RelativePath)) {
			return j, ErrUnsafe
		}
		rel, e := clean(req.RelativePath)
		if e != nil || rel == "." {
			return j, ErrUnsafe
		}
		j.Request.RelativePath = rel
		size, e := parseDriveSize(meta.Size)
		if e != nil {
			return j, e
		}
		j.SourceSize = size
		j.SourceVersion = meta.Version
		j.ExpectedSHA = meta.SHA256
		j.ExpectedMD5 = meta.MD5
	} else {
		if req.RelativePath != "" {
			return j, ErrUnsafe
		}
		if req.Source != c.Source {
			return j, ErrUnsafe
		}
		path, e := clean(req.Path)
		if e != nil || !within(path, c.Path) || path == c.Path || protected(path) {
			return j, ErrUnsafe
		}
		j.Request.Path = path
		root, _, e := s.normalizationRoot()
		if e != nil {
			return j, e
		}
		defer root.Close()
		if e = safe(root, path); e != nil {
			return j, e
		}
		st, e := root.Stat(path)
		if e != nil {
			return j, e
		}
		if !st.Mode().IsRegular() {
			return j, ErrUnsafe
		}
		j.SourceVersion = version(st)
		j.SourceSize = st.Size()
	}
	key := normKey(c.Source, c.Path, req.Source, j.Request.Path, j.Request.RelativePath, j.Connection, j.SourceVersion, j.Profile)
	var old NormalizationJob
	if e := s.existing(key, "normalize", &old); e == nil {
		return old, nil
	} else if !errors.Is(e, sql.ErrNoRows) {
		return j, e
	}
	if e := s.insert(j.ID, "normalize", j.State, key, j); e != nil {
		if s.existing(key, "normalize", &old) == nil {
			return old, nil
		}
		return j, e
	}
	return j, nil
}
func parseDriveSize(v string) (int64, error) {
	n, e := strconv.ParseInt(v, 10, 64)
	if e != nil || n < 0 {
		return 0, ErrUnsupported
	}
	return n, nil
}

func (s *Service) normalizationJobs() ([]NormalizationJob, error) {
	rows, e := s.records("normalize")
	if e != nil {
		return nil, e
	}
	out := []NormalizationJob{}
	for _, row := range rows {
		var j NormalizationJob
		if e = json.Unmarshal(row, &j); e != nil {
			return nil, e
		}
		out = append(out, j)
	}
	return out, nil
}
func publicNormalization(j NormalizationJob) NormalizationJob {
	j.Connection = ""
	j.ExpectedSHA = ""
	j.ExpectedMD5 = ""
	j.SourceHash = ""
	j.OutputHash = ""
	return j
}
func (s *Service) normalizationRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/normalization/status", func(w http.ResponseWriter, r *http.Request) {
		c := s.config.Normalization.defaults()
		reply(w, 200, map[string]any{"enabled": c.Enabled, "source": c.Source, "path": "/" + c.Path, "profile": normalizationProfile, "driveFolder": c.DriveFolder, "sequential": true})
	})
	mux.HandleFunc("GET /v1/normalization/jobs", func(w http.ResponseWriter, r *http.Request) {
		jobs, e := s.normalizationJobs()
		if e != nil {
			fail(w, e)
			return
		}
		for i := range jobs {
			jobs[i] = publicNormalization(jobs[i])
		}
		reply(w, 200, jobs)
	})
	mux.HandleFunc("GET /v1/normalization/jobs/{id}", func(w http.ResponseWriter, r *http.Request) {
		var j NormalizationJob
		if e := s.read(r.PathValue("id"), "normalize", &j); e != nil {
			fail(w, e)
			return
		}
		reply(w, 200, publicNormalization(j))
	})
	mux.HandleFunc("POST /v1/normalization/jobs", func(w http.ResponseWriter, r *http.Request) {
		var req NormalizationRequest
		if e := body(r, &req); e != nil {
			fail(w, e)
			return
		}
		root, _, e := s.normalizationRoot()
		if e != nil {
			fail(w, e)
			return
		}
		root.Close()
		if req.Source == "drive" {
			if !driveID(req.Path) {
				fail(w, ErrUnsafe)
				return
			}
			g, e := s.google()
			if e != nil {
				fail(w, e)
				return
			}
			meta, e := s.driveMeta(r.Context(), req.Path, g.Connection)
			if e != nil {
				fail(w, e)
				return
			}
			if meta.Mime == "application/vnd.google-apps.folder" {
				if req.RelativePath != "" {
					fail(w, ErrUnsafe)
					return
				}
				if key(r) == "" {
					fail(w, ErrUnsafe)
					return
				}
				batchKey := normKey("batch", key(r))
				var b NormalizationBatch
				if e = s.existing(batchKey, "normBatch", &b); errors.Is(e, sql.ErrNoRows) {
					b = NormalizationBatch{ID: ID(), Request: req, Connection: g.Connection, State: "queued", Children: []string{}}
					e = s.insert(b.ID, "normBatch", b.State, batchKey, b)
				}
				if e != nil {
					fail(w, e)
					return
				}
				if b.Connection != g.Connection || b.Request != req {
					fail(w, ErrConflict)
					return
				}
				reply(w, 202, map[string]string{"batchId": b.ID, "state": b.State})
				return
			}
			if req.RelativePath == "" {
				req.RelativePath = meta.Name
			}
			j, e := s.enqueueNormalization(req, "", g.Connection, &meta)
			if e != nil {
				fail(w, e)
				return
			}
			reply(w, 202, publicNormalization(j))
			return
		}
		j, e := s.enqueueNormalization(req, "", "", nil)
		if e != nil {
			fail(w, e)
			return
		}
		reply(w, 202, publicNormalization(j))
	})
	mux.HandleFunc("POST /v1/normalization/batches/{id}/cancel", func(w http.ResponseWriter, r *http.Request) {
		var b NormalizationBatch
		if e := s.read(r.PathValue("id"), "normBatch", &b); e != nil {
			fail(w, e)
			return
		}
		ids := append([]string{b.ID}, b.Children...)
		for _, id := range ids {
			if _, e := s.db.Exec("INSERT IGNORE INTO fb_operations(id,kind,state,body) VALUES(?,'normCancel','requested','{}')", "nc-"+id); e != nil {
				fail(w, e)
				return
			}
		}
		reply(w, 202, map[string]string{"state": "cancellationRequested"})
	})
	mux.HandleFunc("GET /v1/normalization/batches", func(w http.ResponseWriter, r *http.Request) {
		rows, e := s.records("normBatch")
		if e != nil {
			fail(w, e)
			return
		}
		out := []map[string]any{}
		jobs, e := s.normalizationJobs()
		if e != nil {
			fail(w, e)
			return
		}
		for _, row := range rows {
			var b NormalizationBatch
			if e = json.Unmarshal(row, &b); e != nil {
				fail(w, e)
				return
			}
			done, failed, active := 0, 0, 0
			for _, j := range jobs {
				if contains(b.Children, j.ID) {
					switch j.State {
					case "completed":
						done++
					case "failed", "cancelled":
						failed++
					default:
						active++
					}
				}
			}
			state := b.State
			if b.State == "discovered" {
				state = "running"
				if active == 0 {
					state = "completed"
					if failed > 0 {
						state = "needsAttention"
					}
				}
			}
			if errors.Is(s.normCheck(r.Context(), &NormalizationJob{ID: b.ID}), errCancelled) {
				state = "cancelling"
				if active == 0 {
					state = "cancelled"
				}
			}
			out = append(out, map[string]any{"id": b.ID, "state": state, "total": len(b.Children), "completed": done, "failed": failed, "error": b.Error})
		}
		reply(w, 200, out)
	})
	mux.HandleFunc("POST /v1/normalization/jobs/{id}/cancel", func(w http.ResponseWriter, r *http.Request) {
		var j NormalizationJob
		if e := s.read(r.PathValue("id"), "normalize", &j); e != nil {
			fail(w, e)
			return
		}
		// Separate immutable intent avoids losing cancellation to a worker checkpoint.
		id := "nc-" + j.ID
		_, e := s.db.Exec("INSERT IGNORE INTO fb_operations(id,kind,state,body) VALUES(?, 'normCancel','requested','{}')", id)
		if e != nil {
			fail(w, e)
			return
		}
		if _, e = s.db.Exec("UPDATE fb_operations SET state=IF(state='failed','queued',state),body=JSON_SET(body,'$.state',IF(state='failed','queued',state),'$.next',0) WHERE id=? AND kind='normalize' AND state IN ('failed','waiting')", j.ID); e != nil {
			fail(w, e)
			return
		}
		reply(w, 202, map[string]string{"state": "cancellationRequested"})
	})
	mux.HandleFunc("POST /v1/normalization/jobs/{id}/retry", func(w http.ResponseWriter, r *http.Request) {
		conn, e := s.db.Conn(r.Context())
		if e != nil {
			fail(w, e)
			return
		}
		defer conn.Close()
		var got int
		lock := "filebeans-norm-" + r.PathValue("id")
		if e = conn.QueryRowContext(r.Context(), "SELECT GET_LOCK(?,0)", lock).Scan(&got); e != nil || got != 1 {
			fail(w, ErrConflict)
			return
		}
		defer conn.ExecContext(context.Background(), "SELECT RELEASE_LOCK(?)", lock)
		var j NormalizationJob
		if e = s.read(r.PathValue("id"), "normalize", &j); e != nil {
			fail(w, e)
			return
		}
		if j.State != "failed" && j.State != "cancelled" {
			fail(w, ErrConflict)
			return
		}
		if _, e = s.db.Exec("DELETE FROM fb_operations WHERE id=? AND kind='normCancel'", "nc-"+j.ID); e != nil {
			fail(w, e)
			return
		}
		j.State = "queued"
		j.Recoveries = 0
		if !j.Published {
			j.Phase = "queued"
			j.OutputHash = ""
			j.RawMoved = false
		}
		j.Error = ""
		j.Next = 0
		if e = s.save(j.ID, "normalize", j.State, j); e != nil {
			fail(w, e)
			return
		}
		reply(w, 202, publicNormalization(j))
	})
}
func (s *Service) discoverNormalization(ctx context.Context, b *NormalizationBatch) error {
	root, c, e := s.normalizationRoot()
	if e != nil {
		return e
	}
	defer root.Close()
	if e = mkdir(root, c.Path); e != nil {
		return e
	}
	if e = normPublicDirs(root, c.Path); e != nil {
		return e
	}
	// Inventory is metadata only. No content request occurs until one file is selected.
	type entry struct {
		meta DriveItem
		path string
	}
	files := []entry{}
	dirs := []string{}
	seen := map[string]bool{}
	names := map[string]bool{}
	var walk func(string, string) error
	walk = func(id, prefix string) error {
		if e := s.normCheck(ctx, &NormalizationJob{ID: b.ID}); e != nil {
			return e
		}
		if seen[id] {
			return ErrConflict
		}
		seen[id] = true
		page := ""
		for {
			list, e := s.driveList(ctx, id, page, b.Connection)
			if e != nil {
				return e
			}
			if list.Incomplete {
				return errors.New("Drive listing incomplete; retrying inventory")
			}
			for _, f := range list.Files {
				if !validName(f.Name) {
					return ErrUnsafe
				}
				path := filepath.Join(prefix, f.Name)
				if names[path] {
					return ErrConflict
				}
				names[path] = true
				if f.Mime == "application/vnd.google-apps.folder" {
					dirs = append(dirs, path)
					if e = walk(f.ID, path); e != nil {
						return e
					}
				} else {
					files = append(files, entry{f, path})
				}
			}
			page = list.Next
			if page == "" {
				return nil
			}
		}
	}
	if e = walk(b.Request.Path, ""); e != nil {
		return e
	}
	sort.Slice(files, func(i, j int) bool { return files[i].path < files[j].path })
	for _, dir := range dirs {
		path := filepath.Join(c.Path, dir)
		if e = mkdir(root, path); e != nil {
			return e
		}
		if e = normPublicDirs(root, path); e != nil {
			return e
		}
	}
	for _, f := range files {
		if e := s.normCheck(ctx, &NormalizationJob{ID: b.ID}); e != nil {
			return e
		}
		req := NormalizationRequest{Source: "drive", Path: f.meta.ID, RelativePath: f.path}
		j, e := s.enqueueNormalization(req, b.ID, b.Connection, &f.meta)
		if e != nil {
			// Unsupported objects are visible failed entries, not silently omitted.
			key := normKey("unsupported", b.ID, f.meta.ID)
			j = NormalizationJob{ID: ID(), Request: req, Batch: b.ID, State: "failed", Phase: "inventory", Profile: normalizationProfile, LibrarySource: c.Source, LibraryPath: c.Path, Error: publicError(e), Created: time.Now().Unix()}
			var old NormalizationJob
			if s.existing(key, "normalize", &old) == nil {
				j = old
			} else if e = s.insert(j.ID, "normalize", j.State, key, j); e != nil {
				return e
			}
		}
		if !contains(b.Children, j.ID) {
			b.Children = append(b.Children, j.ID)
		}
		if e = s.save(b.ID, "normBatch", b.State, b); e != nil {
			return e
		}
	}
	b.State = "discovered"
	b.Error = ""
	return s.save(b.ID, "normBatch", b.State, b)
}
func (s *Service) scanNormalization() error {
	root, c, e := s.normalizationRoot()
	if e != nil {
		return e
	}
	defer root.Close()
	st, e := root.Stat(c.Path)
	if errors.Is(e, os.ErrNotExist) {
		return nil
	}
	if e != nil {
		return e
	}
	if !st.IsDir() {
		return ErrUnsafe
	}
	jobs, e := s.normalizationJobs()
	if e != nil {
		return e
	}
	known := map[string]bool{}
	for _, j := range jobs {
		if j.Published && j.OutputPath != "" {
			known[j.OutputPath+":"+j.OutputVersion] = true
		}
	}
	return fs.WalkDir(root.FS(), c.Path, func(path string, d fs.DirEntry, e error) error {
		if e != nil {
			return e
		}
		if d.IsDir() {
			return nil
		}
		if e = safe(root, path); e != nil {
			return e
		}
		info, e := d.Info()
		if e != nil {
			return e
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		if known[path+":"+version(info)] || time.Since(info.ModTime()) < time.Duration(c.StableSeconds)*time.Second {
			return nil
		}
		_, e = s.enqueueNormalization(NormalizationRequest{Source: c.Source, Path: path}, "", "", nil)
		return e
	})
}
func accessibleDirectory(root *os.Root, path string) error {
	if e := safe(root, path); e != nil {
		return e
	}
	return root.Chmod(path, 0770)
}
