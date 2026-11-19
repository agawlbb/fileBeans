package service

// Dataset jobs keep source media in FileBeans. Requests/results use the existing
// encrypted operation journal and filesystem broker; no new unlock mechanism.
import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"golang.org/x/sys/unix"
	"image/jpeg"
	"image/png"
	"io"
	"math"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

type DatasetInput struct {
	ID       string `json:"id"`
	Source   string `json:"source"`
	Path     string `json:"path"`
	Revision string `json:"revision"`
}
type DatasetCrop struct {
	X      float64 `json:"x"`
	Y      float64 `json:"y"`
	Width  float64 `json:"width"`
	Height float64 `json:"height"`
}
type DatasetEdit struct {
	ID          string       `json:"id"`
	VideoID     string       `json:"videoID,omitempty"`
	Control     string       `json:"control,omitempty"`
	Target      string       `json:"target,omitempty"`
	Start       float64      `json:"start"`
	End         float64      `json:"end"`
	Crop        *DatasetCrop `json:"crop,omitempty"`
	ControlCrop *DatasetCrop `json:"controlCrop,omitempty"`
	TargetCrop  *DatasetCrop `json:"targetCrop,omitempty"`
	Caption     string       `json:"caption"`
}
type DatasetRequest struct {
	Operation   string         `json:"operation"`
	Model       string         `json:"model"`
	Inputs      []DatasetInput `json:"inputs"`
	Clips       []DatasetEdit  `json:"clips,omitempty"`
	Destination Item           `json:"destination"`
}
type DatasetInfo struct {
	Width    int     `json:"width"`
	Height   int     `json:"height"`
	Duration float64 `json:"duration"`
	Caption  string  `json:"caption"`
}
type DatasetJob struct {
	ID      string         `json:"id"`
	State   string         `json:"state"`
	Error   string         `json:"error,omitempty"`
	Code    string         `json:"code,omitempty"`
	Request DatasetRequest `json:"request"`
	Info    *DatasetInfo   `json:"info,omitempty"`
	Result  *Item          `json:"result,omitempty"`
}

func datasetCropValid(c *DatasetCrop) bool {
	if c == nil {
		return true
	}
	for _, n := range []float64{c.X, c.Y, c.Width, c.Height} {
		if math.IsNaN(n) || math.IsInf(n, 0) {
			return false
		}
	}
	return c.X >= 0 && c.Y >= 0 && c.Width >= 0.05 && c.Height >= 0.05 && c.X+c.Width <= 1.000001 && c.Y+c.Height <= 1.000001
}
func validateDataset(req DatasetRequest) error {
	if req.Model != "wan" && req.Model != "qwen" {
		return ErrUnsafe
	}
	if req.Operation != "prepare" && req.Operation != "render" {
		return ErrUnsafe
	}
	if len(req.Inputs) < 1 || len(req.Inputs) > 500 {
		return ErrUnsafe
	}
	ids := map[string]bool{}
	for _, in := range req.Inputs {
		p, e := filesystemPath(in.Path)
		if e != nil || protected(p) || in.Source == "upload" || in.Source == "drive" || in.Source == "" || !validBrokerID(in.ID) || len(in.Revision) != 64 || ids[in.ID] {
			return ErrUnsafe
		}
		ids[in.ID] = true
	}
	if req.Operation == "prepare" {
		if len(req.Inputs) != 1 || len(req.Clips) != 0 {
			return ErrUnsafe
		}
		return nil
	}
	p, e := filesystemPath(req.Destination.Path)
	if e != nil || protected(p) || req.Destination.Source == "upload" || req.Destination.Source == "drive" || req.Destination.Source == "" || len(req.Clips) < 1 || len(req.Clips) > 500 {
		return ErrUnsafe
	}
	for _, c := range req.Clips {
		if !validBrokerID(c.ID) || !datasetCropValid(c.Crop) || !datasetCropValid(c.ControlCrop) || !datasetCropValid(c.TargetCrop) || len(c.Caption) > 1<<20 || !utf8.ValidString(c.Caption) {
			return ErrUnsafe
		}
		if req.Model == "wan" {
			if !ids[c.VideoID] || math.IsNaN(c.Start) || math.IsNaN(c.End) || math.IsInf(c.Start, 0) || math.IsInf(c.End, 0) || c.Start < 0 || c.End-c.Start < 0.1 {
				return ErrUnsafe
			}
		} else if !ids[c.Control] || !ids[c.Target] {
			return ErrUnsafe
		}
	}
	return nil
}
func datasetFailure(w http.ResponseWriter, e error) {
	if errors.Is(e, ErrVaultLocked) {
		reply(w, 423, map[string]string{"code": "locked", "error": "Unlock protected content in a FileBeans mobile app, then retry."})
		return
	}
	if errors.Is(e, ErrConflict) {
		reply(w, 409, map[string]string{"code": "source_changed", "error": "Source changed. Relink it and review its edits."})
		return
	}
	filesystemFail(w, e)
}
func (s *Service) datasetRoutes(mux *http.ServeMux) {
	if !s.config.Filesystem.Enabled {
		return
	}
	mux.HandleFunc("POST /v1/datasets/uploads", s.datasetUpload)
	mux.HandleFunc("POST /v1/datasets/activity", func(w http.ResponseWriter, r *http.Request) {
		var items []Item
		if body(r, &items) != nil || len(items) > 501 {
			datasetFailure(w, ErrUnsafe)
			return
		}
		for _, item := range items {
			if e := s.datasetActivity(item); e != nil {
				datasetFailure(w, e)
				return
			}
		}
		reply(w, 200, map[string]bool{"active": true})
	})
	mux.HandleFunc("POST /v1/datasets/jobs", func(w http.ResponseWriter, r *http.Request) {
		var req DatasetRequest
		if body(r, &req) != nil || validateDataset(req) != nil || key(r) == "" {
			datasetFailure(w, ErrUnsafe)
			return
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		var old DatasetJob
		if e := s.existing(key(r), "dataset", &old); e == nil {
			a, _ := json.Marshal(old.Request)
			b, _ := json.Marshal(req)
			if string(a) != string(b) {
				datasetFailure(w, ErrConflict)
				return
			}
			reply(w, 200, old)
			return
		} else if !errors.Is(e, sql.ErrNoRows) {
			datasetFailure(w, e)
			return
		}
		j := DatasetJob{ID: ID(), State: "queued", Request: req}
		if e := s.insert(j.ID, "dataset", j.State, key(r), j); e != nil {
			datasetFailure(w, e)
			return
		}
		reply(w, 202, j)
	})
	mux.HandleFunc("GET /v1/datasets/jobs/{id}", func(w http.ResponseWriter, r *http.Request) {
		var j DatasetJob
		s.mu.Lock()
		e := s.read(r.PathValue("id"), "dataset", &j)
		s.mu.Unlock()
		if e != nil {
			datasetFailure(w, e)
			return
		}
		reply(w, 200, j)
	})
	mux.HandleFunc("POST /v1/datasets/jobs/{id}/{action}", func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		var j DatasetJob
		if e := s.read(r.PathValue("id"), "dataset", &j); e != nil {
			datasetFailure(w, e)
			return
		}
		switch r.PathValue("action") {
		case "cancel":
			if j.State != "ready" {
				j.State = "cancelled"
			}
			if cancel := s.copyCancels[j.ID]; cancel != nil {
				cancel()
			}
		case "retry":
			if j.State == "failed" || j.State == "blocked" || j.State == "cancelled" {
				j.State = "queued"
				j.Error = ""
				j.Code = ""
			}
		default:
			datasetFailure(w, ErrUnsafe)
			return
		}
		if e := s.save(j.ID, "dataset", j.State, j); e != nil {
			datasetFailure(w, e)
			return
		}
		reply(w, 200, j)
	})
	mux.HandleFunc("GET /v1/datasets/jobs/{id}/media/{kind}", s.datasetMedia)
}

// Keep the standard database sentinel available without translating locked journal records.
func (s *Service) datasetActivity(item Item) error {
	p, e := filesystemPath(item.Path)
	if e != nil {
		return e
	}
	if s.vaults == nil || s.vaults.remote == nil {
		return nil
	}
	v := s.vaults.pathVault(item.Source, p)
	if v == nil {
		// Hidden locations must return locked rather than treating a known protected
		// reference as ordinary while the location registry is sealed.
		for _, status := range s.vaults.status() {
			if status.Source == item.Source && status.State != "unlocked" && status.Path == "" {
				return ErrVaultLocked
			}
		}
		return nil
	}
	return s.vaults.remote.request("POST", "dataset-activity", item, "", nil)
}
func (s *Service) datasetInput(in DatasetInput) (rootFS, *os.File, string, error) {
	p, e := filesystemPath(in.Path)
	if e != nil {
		return nil, nil, "", e
	}
	root, e := s.root(in.Source)
	if e != nil {
		return nil, nil, "", e
	}
	bad := func(e error) (rootFS, *os.File, string, error) { root.Close(); return nil, nil, "", e }
	if e = safe(root, p); e != nil {
		return bad(e)
	}
	f, e := filesystemOpenFile(root, p)
	if e != nil {
		return bad(e)
	}
	st, e := f.Stat()
	if e != nil || !st.Mode().IsRegular() || st.Size() <= 0 || st.Size() > 2<<30 {
		f.Close()
		return bad(ErrUnsupported)
	}
	if filesystemRevision(st) != in.Revision {
		f.Close()
		return bad(ErrConflict)
	}
	return root, f, p, nil
}
func datasetCache(in DatasetInput, p string) string {
	h := sha256.Sum256([]byte(in.Path + "\x00" + in.Revision + "\x00dataset-v1"))
	return filepath.Join(filepath.Dir(p), thumbnailDirectory, "dataset-"+hex.EncodeToString(h[:]))
}
func (s *Service) datasetMedia(w http.ResponseWriter, r *http.Request) {
	var j DatasetJob
	s.mu.Lock()
	e := s.read(r.PathValue("id"), "dataset", &j)
	s.mu.Unlock()
	if e != nil {
		datasetFailure(w, e)
		return
	}
	if j.Request.Operation != "prepare" || j.State != "ready" {
		datasetFailure(w, os.ErrNotExist)
		return
	}
	in := j.Request.Inputs[0]
	root, f, p, e := s.datasetInput(in)
	if e != nil {
		datasetFailure(w, e)
		return
	}
	defer root.Close()
	defer f.Close()
	kind := r.PathValue("kind")
	name := "preview.mp4"
	if j.Request.Model == "qwen" {
		name = "preview.jpg"
	}
	if kind != "preview" {
		if len(kind) != 6 || !strings.HasPrefix(kind, "thumb") || kind[5] < '0' || kind[5] > '7' {
			datasetFailure(w, ErrUnsafe)
			return
		}
		name = kind + ".jpg"
	}
	media, e := filesystemOpenFile(root, filepath.Join(datasetCache(in, p), name))
	if e != nil {
		datasetFailure(w, e)
		return
	}
	defer media.Close()
	st, e := media.Stat()
	if e != nil {
		datasetFailure(w, e)
		return
	}
	w.Header().Set("Cache-Control", "private, no-store")
	http.ServeContent(w, r, name, st.ModTime(), media)
}
func (s *Service) datasetWorker(ctx context.Context) bool {
	s.mu.Lock()
	records, e := s.records("dataset")
	var job *DatasetJob
	if e == nil {
		for i := len(records) - 1; i >= 0; i-- {
			var j DatasetJob
			if json.Unmarshal(records[i], &j) == nil && (j.State == "queued" || j.State == "processing") {
				job = &j
				break
			}
		}
	}
	if job == nil {
		s.mu.Unlock()
		return false
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Hour)
	if s.copyCancels == nil {
		s.copyCancels = map[string]context.CancelFunc{}
	}
	s.copyCancels[job.ID] = cancel
	job.State = "processing"
	e = s.save(job.ID, "dataset", job.State, job)
	s.mu.Unlock()
	if e == nil {
		e = s.datasetProcess(ctx, job)
	}
	cancel()
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.copyCancels, job.ID)
	var current DatasetJob
	if s.read(job.ID, "dataset", &current) != nil || current.State == "cancelled" {
		return true
	}
	if e == nil {
		job.State = "ready"
		job.Error = ""
		job.Code = ""
	} else {
		job.State = "failed"
		job.Error = "Dataset processing failed. Check storage and retry."
		job.Code = "processing"
		if errors.Is(e, ErrVaultLocked) {
			job.State = "blocked"
			job.Code = "locked"
			job.Error = "Unlock protected content in a FileBeans mobile app, then retry."
		}
		if errors.Is(e, ErrConflict) {
			job.Code = "source_changed"
			job.Error = "Source changed. Relink it and review its edits."
		}
	}
	_ = s.save(job.ID, "dataset", job.State, job)
	return true
}
func (s *Service) datasetProbe(ctx context.Context, f *os.File, model string) (DatasetInfo, error) {
	info, e := s.probeMedia(ctx, f, false)
	if e != nil {
		return DatasetInfo{}, e
	}
	result := DatasetInfo{}
	for _, v := range info.Streams {
		if v.Type == "video" && v.Disposition.Attached == 0 {
			result.Width = v.Width
			result.Height = v.Height
			for _, d := range v.SideData {
				if d.Rotation%180 != 0 {
					result.Width, result.Height = result.Height, result.Width
				}
			}
			break
		}
	}
	result.Duration, _ = strconv.ParseFloat(info.Format.Duration, 64)
	if result.Width < 1 || result.Height < 1 || result.Width > 30000 || result.Height > 30000 || int64(result.Width)*int64(result.Height) > 100_000_000 {
		return result, ErrUnsupported
	}
	if model == "wan" && (result.Width > 8192 || result.Height > 8192 || int64(result.Width)*int64(result.Height) > 40_000_000 || result.Duration <= 0 || result.Duration > 1800) {
		return result, ErrUnsupported
	}
	return result, nil
}
func datasetFilter(c *DatasetCrop, short int) string {
	parts := []string{"scale=trunc(iw*sar/2)*2:trunc(ih/2)*2", "setsar=1"}
	if c != nil {
		parts = append(parts, fmt.Sprintf("crop=trunc(iw*%.8f/2)*2:trunc(ih*%.8f/2)*2:trunc(iw*%.8f/2)*2:trunc(ih*%.8f/2)*2", c.Width, c.Height, c.X, c.Y))
	}
	if short > 0 {
		parts = append(parts, fmt.Sprintf("scale=w='if(gte(iw,ih),round(%d*iw/ih/2)*2,%d)':h='if(gte(iw,ih),%d,round(%d*ih/iw/2)*2)'", short, short, short, short))
	}
	return strings.Join(parts, ",")
}
func (s *Service) datasetEncode(ctx context.Context, input *os.File, root rootFS, dir, name string, start, end float64, crop *DatasetCrop, mode string) error {
	if e := root.MkdirAll(dir, 0700); e != nil {
		return e
	}
	directory, e := root.Open(dir)
	if e != nil {
		return e
	}
	defer directory.Close()
	minimum := s.config.Filesystem.DatasetMinFreeBytes
	if minimum == 0 {
		minimum = 1 << 30
	}
	var stat unix.Statfs_t
	if s.datasetDiskCheck != nil {
		if e = s.datasetDiskCheck(directory); e != nil {
			return e
		}
	} else if e = unix.Fstatfs(int(directory.Fd()), &stat); e != nil || stat.Bavail*uint64(stat.Bsize) < minimum {
		return ErrUnsupported
	}
	out, e := root.OpenFile(filepath.Join(dir, name), os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
	if e != nil {
		return e
	}
	defer out.Close()
	if _, e = input.Seek(0, 0); e != nil {
		return e
	}
	args := []string{"-nostdin", "-v", "error", "-y", "-threads", "2", "-filter_threads", "1"}
	args = append(args, mediaInputArgs()...)
	if mode == "video" {
		args = append(args, "-ss", fmt.Sprintf("%.6f", start))
	}
	args = append(args, "-i", "/proc/self/fd/3", "-map", "0:v:0", "-an", "-sn", "-dn")
	switch mode {
	case "video", "preview":
		short := 480
		crf := "20"
		if mode == "preview" {
			short = 360
			crf = "28"
		}
		duration := end - start
		if mode == "preview" {
			duration = end
		}
		args = append(args, "-t", fmt.Sprintf("%.6f", duration), "-vf", datasetFilter(crop, short), "-c:v", "libx264", "-threads", "2", "-preset", "veryfast", "-crf", crf, "-pix_fmt", "yuv420p", "-g", "24", "-movflags", "+faststart", "-f", "mp4")
	case "image":
		filter := "scale=iw*sar:ih,setsar=1"
		if crop != nil {
			filter += fmt.Sprintf(",crop=iw*%.8f:ih*%.8f:iw*%.8f:ih*%.8f:exact=1", crop.Width, crop.Height, crop.X, crop.Y)
		}
		// Flatten transparency on white and remove all inherited metadata.
		filter += ",format=rgba,split[fg][bg];[bg]drawbox=c=white:t=fill:replace=1[white];[white][fg]overlay=format=auto,format=yuvj444p"
		args = append(args, "-frames:v", "1", "-vf", filter, "-c:v", "png", "-f", "image2", "-update", "1")
	default:
		args = append(args, "-ss", fmt.Sprintf("%.6f", start), "-frames:v", "1", "-vf", "scale=640:640:force_original_aspect_ratio=decrease", "-q:v", "3", "-pix_fmt", "yuvj420p", "-f", "image2", "-update", "1")
	}
	args = append(args, "-map_metadata", "-1", "/proc/self/fd/5")
	cmd := exec.CommandContext(ctx, s.config.Filesystem.FFmpeg, args...)
	cmd.ExtraFiles = []*os.File{input, directory, out}
	cmd.Dir = fmt.Sprintf("/proc/self/fd/%d", directory.Fd())
	cmd.Env = []string{"PATH=/usr/bin:/bin", "TMPDIR=/proc/self/fd/4", "HOME=/proc/self/fd/4", "XDG_CACHE_HOME=/proc/self/fd/4"}
	var diagnostics limitedBuffer
	diagnostics.limit = 8192
	cmd.Stderr = &diagnostics
	if e = cmd.Run(); e != nil {
		return fmt.Errorf("%w: dataset encoding: %s", e, string(diagnostics.data))
	}
	if mode == "image" {
		if _, e = out.Seek(0, 0); e != nil {
			return e
		}
		image, e := png.Decode(out)
		if e != nil {
			return e
		}
		if e = out.Truncate(0); e != nil {
			return e
		}
		if _, e = out.Seek(0, 0); e != nil {
			return e
		}
		if e = jpeg.Encode(out, image, &jpeg.Options{Quality: 95}); e != nil {
			return e
		}
	}
	st, e := out.Stat()
	if e == nil && st.Size() == 0 && mode == "thumb" && start > 0 {
		_ = out.Close()
		_ = root.Remove(filepath.Join(dir, name))
		return s.datasetEncode(ctx, input, root, dir, name, 0, end, crop, mode)
	}
	if e != nil || st.Size() == 0 {
		return fmt.Errorf("%w: encoder produced empty output", ErrUnsupported)
	}
	return out.Sync()
}
func (s *Service) datasetProcess(ctx context.Context, j *DatasetJob) error {
	type source struct {
		root rootFS
		file *os.File
		path string
		info DatasetInfo
		ref  DatasetInput
	}
	sources := map[string]source{}
	defer func() {
		for _, src := range sources {
			src.file.Close()
			src.root.Close()
		}
	}()
	for _, in := range j.Request.Inputs {
		root, f, p, e := s.datasetInput(in)
		if e != nil {
			return e
		}
		info, e := s.datasetProbe(ctx, f, j.Request.Model)
		sources[in.ID] = source{root, f, p, info, in}
		if e != nil {
			return fmt.Errorf("dataset probe: %w", e)
		}
		if e = s.datasetActivity(Item{Source: in.Source, Path: in.Path}); e != nil {
			return e
		}
	}
	// A bounded job lease prevents unmount during processing. Explicit activity
	// keeps the next idle deadline measured from the operation's final activity.
	done := make(chan struct{})
	defer close(done)
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ctx.Done():
				return
			case <-ticker.C:
				for _, in := range j.Request.Inputs {
					_ = s.datasetActivity(Item{Source: in.Source, Path: in.Path})
				}
			}
		}
	}()
	if j.Request.Operation == "prepare" {
		src := sources[j.Request.Inputs[0].ID]
		cache := datasetCache(src.ref, src.path)
		_ = src.root.RemoveAll(cache)
		if e := src.root.MkdirAll(cache, 0700); e != nil {
			return e
		}
		captionPath := strings.TrimSuffix(src.path, filepath.Ext(src.path)) + ".txt"
		if f, e := filesystemOpenFile(src.root, captionPath); e == nil {
			b, e := io.ReadAll(io.LimitReader(f, (1<<20)+1))
			f.Close()
			if e != nil || len(b) > 1<<20 || !utf8.Valid(b) {
				return ErrUnsupported
			}
			src.info.Caption = string(b)
		} else if !os.IsNotExist(e) {
			return e
		}
		if j.Request.Model == "wan" {
			if e := s.datasetEncode(ctx, src.file, src.root, cache, "preview.mp4", 0, src.info.Duration, nil, "preview"); e != nil {
				return fmt.Errorf("dataset preview: %w", e)
			}
			for i := 0; i < 8; i++ {
				if e := s.datasetEncode(ctx, src.file, src.root, cache, fmt.Sprintf("thumb%d.jpg", i), src.info.Duration*float64(i)/8, 0, nil, "thumb"); e != nil {
					return fmt.Errorf("dataset thumbnail %d: %w", i, e)
				}
			}
		} else {
			if e := s.datasetEncode(ctx, src.file, src.root, cache, "preview.jpg", 0, 0, nil, "thumb"); e != nil {
				return e
			}
		}
		st, e := src.file.Stat()
		if e != nil || filesystemRevision(st) != src.ref.Revision {
			return ErrConflict
		}
		j.Info = &src.info
		return nil
	}
	dest := j.Request.Destination
	root, e := s.root(dest.Source)
	if e != nil {
		return e
	}
	defer root.Close()
	parent, e := filesystemPath(dest.Path)
	if e != nil {
		return e
	}
	if e = safe(root, parent); e != nil {
		return e
	}
	st, e := root.Stat(parent)
	if e != nil || !st.IsDir() {
		return ErrUnsafe
	}
	stage := filepath.Join(parent, ".filebeans-stage", "dataset-"+j.ID)
	final := filepath.Join(parent, "MakeLORA-"+j.ID)
	// A completed exclusive rename is the publication journal, including crashes
	// between publication and saving the ready state.
	if st, e := root.Stat(final); e == nil && st.IsDir() {
		j.Result = &Item{Source: dest.Source, Path: "/" + strings.TrimPrefix(final, "./")}
		return nil
	}
	_ = root.RemoveAll(stage)
	if e = root.MkdirAll(stage, 0700); e != nil {
		return e
	}
	defer root.RemoveAll(stage)
	if j.Request.Model == "qwen" {
		for _, sub := range []string{"control", "target"} {
			if e = root.MkdirAll(filepath.Join(stage, sub), 0700); e != nil {
				return e
			}
		}
	}
	for i, c := range j.Request.Clips {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		encode := func(id, out string, crop *DatasetCrop, start, end float64, mode string) error {
			src := sources[id]
			if mode == "video" {
				if end > src.info.Duration+0.001 {
					return ErrUnsafe
				}
				w, h := float64(src.info.Width), float64(src.info.Height)
				if crop != nil {
					w *= crop.Width
					h *= crop.Height
				}
				if w/h > float64(8192)/480 || h/w > float64(8192)/480 {
					return ErrUnsafe
				}
			}
			scratch := filepath.Join(datasetCache(src.ref, src.path), "render-"+j.ID)
			_ = src.root.RemoveAll(scratch)
			if e := s.datasetEncode(ctx, src.file, src.root, scratch, filepath.Base(out), start, end, crop, mode); e != nil {
				return e
			}
			defer src.root.RemoveAll(scratch)
			file, e := filesystemOpenFile(src.root, filepath.Join(scratch, filepath.Base(out)))
			if e != nil {
				return e
			}
			defer file.Close()
			output, e := root.OpenFile(filepath.Join(stage, out), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0660)
			if e != nil {
				return e
			}
			_, e = io.Copy(output, file)
			if e == nil {
				e = output.Sync()
			}
			ce := output.Close()
			if e == nil {
				e = ce
			}
			return e
		}
		n := strconv.Itoa(i + 1)
		caption := n + ".txt"
		if j.Request.Model == "wan" {
			e = encode(c.VideoID, n+".mp4", c.Crop, c.Start, c.End, "video")
		} else {
			caption = "target/" + n + ".txt"
			e = encode(c.Control, "control/"+n+".jpg", c.ControlCrop, 0, 0, "image")
			if e == nil {
				e = encode(c.Target, "target/"+n+".jpg", c.TargetCrop, 0, 0, "image")
			}
		}
		if e != nil {
			return e
		}
		if e = root.WriteFile(filepath.Join(stage, caption), []byte(c.Caption), 0660); e != nil {
			return e
		}
	}
	for _, src := range sources {
		st, e := src.file.Stat()
		if e != nil || filesystemRevision(st) != src.ref.Revision {
			return ErrConflict
		}
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var current DatasetJob
	if e = s.read(j.ID, "dataset", &current); e != nil {
		return e
	}
	if current.State == "cancelled" {
		return context.Canceled
	}
	if e = renameExclusive(root, stage, final); e != nil {
		return e
	}
	j.Result = &Item{Source: dest.Source, Path: "/" + strings.TrimPrefix(final, "./")}
	return syncDir(root, parent)
}
