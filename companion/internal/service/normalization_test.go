package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"golang.org/x/sys/unix"
	"image"
	"image/color"
	"image/gif"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func normFixturePNG(t *testing.T, alpha bool) []byte {
	t.Helper()
	im := image.NewNRGBA(image.Rect(0, 0, 32, 24))
	for y := 0; y < 24; y++ {
		for x := 0; x < 32; x++ {
			a := uint8(255)
			if alpha && x < 8 {
				a = 80
			}
			im.SetNRGBA(x, y, color.NRGBA{uint8(x * 7), uint8(y * 9), 123, a})
		}
	}
	var b bytes.Buffer
	if e := png.Encode(&b, im); e != nil {
		t.Fatal(e)
	}
	return b.Bytes()
}
func normFixtureVideo(t *testing.T) []byte {
	t.Helper()
	if _, e := exec.LookPath("ffmpeg"); e != nil {
		t.Skip("ffmpeg required for normalization integration tests")
	}
	p := filepath.Join(t.TempDir(), "source.mp4")
	c := exec.Command("ffmpeg", "-v", "error", "-nostdin", "-f", "lavfi", "-i", "testsrc2=size=96x64:rate=10", "-f", "lavfi", "-i", "sine=frequency=440:sample_rate=44100", "-t", "1.2", "-c:v", "mpeg4", "-c:a", "aac", p)
	if out, e := c.CombinedOutput(); e != nil {
		t.Fatalf("fixture: %v %s", e, out)
	}
	b, e := os.ReadFile(p)
	if e != nil {
		t.Fatal(e)
	}
	return b
}
func writeNormSource(t *testing.T, s *Service, path string, data []byte) NormalizationJob {
	t.Helper()
	root, _, e := s.normalizationRoot()
	if e != nil {
		t.Fatal(e)
	}
	defer root.Close()
	if e = mkdir(root, filepath.Dir(path)); e != nil {
		t.Fatal(e)
	}
	if e = root.WriteFile(path, data, 0600); e != nil {
		t.Fatal(e)
	}
	j, e := s.enqueueNormalization(NormalizationRequest{Source: s.config.Normalization.Source, Path: path}, "", "", nil)
	if e != nil {
		t.Fatal(e)
	}
	return j
}
func normHash(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }
func testNormalizationWorkflow(t *testing.T, s *Service) {
	before := s.config
	defer func() { s.config = before }()
	s.config.Roots = []Root{{Name: "vps", Path: t.TempDir()}, {Name: "samba", Path: "/unmounted-samba-never-read"}}
	s.config.Normalization = NormalizationConfig{Enabled: true, Source: "vps", Path: "pics", MinFreeBytes: 1 << 20, StableSeconds: 1}.defaults()
	ctx := context.Background()
	base := s.config.Roots[0].Path
	previous := googleHTTP
	defer func() { googleHTTP = previous }()
	fixtureGrant(t, s, true)
	f := newCloudFixture(t)
	googleHTTP = &http.Client{Transport: roundTrip(f.serve)}
	pngBytes := normFixturePNG(t, false)
	video := normFixtureVideo(t)
	run := func(t *testing.T, j *NormalizationJob) {
		t.Helper()
		if e := s.runNormalizationJob(ctx, j); e != nil {
			t.Fatal(e)
		}
		if j.State != "completed" {
			t.Fatal(j.State, j.Phase, j.Error)
		}
	}
	t.Run("local-video-and-no-reconversion", func(t *testing.T) {
		j := writeNormSource(t, s, "pics/movie.mp4", video)
		run(t, &j)
		originalModified, e := time.Parse(time.RFC3339Nano, j.SourceModified)
		if e != nil {
			t.Fatal(e)
		}
		published, e := os.Stat(filepath.Join(base, j.OutputPath))
		if e != nil || !published.ModTime().Equal(originalModified) {
			t.Fatal("original date not preserved", e)
		}
		// An administrative timestamp correction must not cause re-encoding.
		corrected := time.Unix(1539447530, 123456789)
		if e = os.Chtimes(filepath.Join(base, j.OutputPath), corrected, corrected); e != nil {
			t.Fatal(e)
		}
		if !j.Published || j.OutputSize == 0 || j.OutputHash == j.SourceHash {
			t.Fatal("video not normalized")
		}
		if e := s.scanNormalization(); e != nil {
			t.Fatal(e)
		}
		jobs, _ := s.normalizationJobs()
		for _, other := range jobs {
			if other.ID != j.ID && other.Request.Source == "vps" && other.Request.Path == j.OutputPath {
				t.Fatal("generated output requeued")
			}
		}
	})
	t.Run("photo-extension-and-alpha", func(t *testing.T) {
		j := writeNormSource(t, s, "pics/photo.png", pngBytes)
		run(t, &j)
		if j.OutputPath != "pics/photo.jpg" {
			t.Fatal(j.OutputPath)
		}
		if _, e := os.Stat(filepath.Join(base, "pics/photo.png")); !errors.Is(e, os.ErrNotExist) {
			t.Fatal("input not replaced")
		}
		j = writeNormSource(t, s, "pics/alpha.png", normFixturePNG(t, true))
		run(t, &j)
		if j.OutputPath != "pics/alpha.png" || j.SourceHash != j.OutputHash {
			t.Fatal("alpha/unchanged PNG lost")
		}
	})
	t.Run("source-and-destination-boundaries", func(t *testing.T) {
		for _, req := range []NormalizationRequest{{Source: "samba", Path: "pics/file"}, {Source: "vps", Path: "elsewhere/file"}, {Source: "vps", Path: "pics/../secret"}} {
			if _, e := s.enqueueNormalization(req, "", "", nil); !errors.Is(e, ErrUnsafe) {
				t.Fatal("invalid source accepted", req, e)
			}
		}
		outside := t.TempDir()
		os.WriteFile(filepath.Join(outside, "file.png"), pngBytes, 0600)
		os.Symlink(outside, filepath.Join(base, "pics/link"))
		if _, e := s.enqueueNormalization(NormalizationRequest{Source: "vps", Path: "pics/link/file.png"}, "", "", nil); !errors.Is(e, ErrUnsafe) {
			t.Fatal("symlink accepted", e)
		}
		os.Remove(filepath.Join(base, "pics/link"))
	})
	t.Run("source-changes-are-preserved", func(t *testing.T) {
		j := writeNormSource(t, s, "pics/changed.png", pngBytes)
		newer := append(append([]byte{}, pngBytes...), []byte("new content")...)
		os.WriteFile(filepath.Join(base, j.Request.Path), newer, 0600)
		s.runNormalizationJob(ctx, &j)
		got, _ := os.ReadFile(filepath.Join(base, j.Request.Path))
		if j.State != "failed" || !bytes.Equal(got, newer) {
			t.Fatal("changed source was replaced", j.State)
		}
	})
	t.Run("animation-is-not-flattened", func(t *testing.T) {
		palette := color.Palette{color.Black, color.White}
		a := image.NewPaletted(image.Rect(0, 0, 32, 24), palette)
		b := image.NewPaletted(a.Rect, palette)
		b.SetColorIndex(2, 2, 1)
		var data bytes.Buffer
		if e := gif.EncodeAll(&data, &gif.GIF{Image: []*image.Paletted{a, b}, Delay: []int{10, 10}, LoopCount: 0}); e != nil {
			t.Fatal(e)
		}
		j := writeNormSource(t, s, "pics/animation.gif", data.Bytes())
		run(t, &j)
		got, _ := os.ReadFile(filepath.Join(base, j.OutputPath))
		if !bytes.Equal(got, data.Bytes()) {
			t.Fatal("animation was modified")
		}
	})
	t.Run("collision-keeps-both-inputs", func(t *testing.T) {
		j := writeNormSource(t, s, "pics/collision.png", pngBytes)
		os.WriteFile(filepath.Join(base, "pics/collision.jpg"), []byte("existing"), 0600)
		s.runNormalizationJob(ctx, &j)
		if j.State != "failed" {
			t.Fatal(j.State, j.Error)
		}
		a, _ := os.ReadFile(filepath.Join(base, "pics/collision.png"))
		b, _ := os.ReadFile(filepath.Join(base, "pics/collision.jpg"))
		if !bytes.Equal(a, pngBytes) || string(b) != "existing" {
			t.Fatal("collision destroyed content")
		}
	})
	t.Run("publication-crash-recovery", func(t *testing.T) {
		jpeg, _ := os.ReadFile(filepath.Join(base, "pics/photo.jpg"))
		for _, published := range []bool{false, true} {
			name := "recover-before"
			if published {
				name = "recover-after"
			}
			j := writeNormSource(t, s, "pics/"+name+".png", pngBytes)
			j.SourceModified = "" // Pre-upgrade job; recover from the pinned local version.
			j.State = "running"
			j.Phase = "publishing"
			j.SourceHash = normHash(pngBytes)
			j.OutputHash = normHash(jpeg)
			j.OutputPath = "pics/" + name + ".jpg"
			j.OutputSize = int64(len(jpeg))
			s.normSave(&j)
			root, _, _ := s.normalizationRoot()
			if e := s.normPrepare(root, &j); e != nil {
				t.Fatal(e)
			}
			root.WriteFile(filepath.Join(normWorkspace, "output.jpg"), jpeg, 0600)
			if e := renameExclusive(root, j.Request.Path, filepath.Join(normWorkspace, "original")); e != nil {
				t.Fatal(e)
			}
			if published {
				if e := renameExclusive(root, filepath.Join(normWorkspace, "output.jpg"), j.OutputPath); e != nil {
					t.Fatal(e)
				}
			}
			root.Close()
			run(t, &j)
			if j.OutputVersion == "" {
				t.Fatal("publication not recovered")
			}
			st, e := os.Stat(filepath.Join(base, j.OutputPath))
			if e != nil || j.OutputVersion != version(st) || st.ModTime().UTC().Format(time.RFC3339Nano) != j.SourceModified {
				t.Fatal("recovery lost source date", e)
			}
		}
	})
	t.Run("restart-requeues-before-encoding", func(t *testing.T) {
		root, _, e := s.normalizationRoot()
		if e != nil {
			t.Fatal(e)
		}
		defer root.Close()
		conn, e := s.db.Conn(ctx)
		if e != nil {
			t.Fatal(e)
		}
		defer conn.Close()
		for _, isolated := range []bool{false, true} {
			for _, publication := range []bool{false, true} {
				name := "legacy"
				if isolated {
					name = "isolated"
				}
				if publication {
					name += "-publication"
				}
				j := writeNormSource(t, s, "pics/restart-"+name+".png", pngBytes)
				j.IsolatedWorkspace = isolated
				j.State, j.Phase = "running", "converting"
				if e = s.normSave(&j); e != nil {
					t.Fatal(e)
				}
				if e = s.normPrepare(root, &j); e != nil {
					t.Fatal(e)
				}
				if publication {
					j.Phase = "publishing"
					j.OutputHash = normHash([]byte("lost output"))
					j.OutputPath = "pics/restart-" + name + ".jpg"
					if e = s.normSave(&j); e != nil {
						t.Fatal(e)
					}
				} else {
					if e = root.WriteFile(filepath.Join(normWorkspaceFor(&j), "output.jpg"), []byte("partial"), 0600); e != nil {
						t.Fatal(e)
					}
				}
				if e = s.recoverNormWorkspaces(ctx, conn); e != nil {
					t.Fatal(e)
				}
				if e = s.read(j.ID, "normalize", &j); e != nil {
					t.Fatal(e)
				}
				if j.State != "queued" || j.Phase != "queued" || j.Recoveries != 1 || j.Published {
					t.Fatal("recovery encoded instead of requeueing", j)
				}
				if _, e = root.Stat(j.Request.Path); e != nil {
					t.Fatal("source missing", e)
				}
				run(t, &j)
			}
		}
	})
	t.Run("committed-output-is-not-resurrected", func(t *testing.T) {
		j := writeNormSource(t, s, "pics/committed.png", normFixturePNG(t, true))
		run(t, &j)
		os.Remove(filepath.Join(base, j.OutputPath))
		j.State = "running"
		j.Phase = "publishing"
		s.normSave(&j)
		run(t, &j)
		if _, e := os.Stat(filepath.Join(base, j.OutputPath)); !errors.Is(e, os.ErrNotExist) {
			t.Fatal("user deletion was undone")
		}
	})
	t.Run("backup-lock-excludes-publication-gap", func(t *testing.T) {
		jpeg, _ := os.ReadFile(filepath.Join(base, "pics/photo.jpg"))
		j := writeNormSource(t, s, "pics/backup-lock.png", pngBytes)
		j.Phase = "publishing"
		j.SourceHash = normHash(pngBytes)
		j.OutputHash = normHash(jpeg)
		j.OutputPath = "pics/backup-lock.jpg"
		s.normSave(&j)
		root, _, _ := s.normalizationRoot()
		defer root.Close()
		s.normPrepare(root, &j)
		root.WriteFile(filepath.Join(normWorkspace, "output.jpg"), jpeg, 0600)
		unlock, e := lockPrivateFile(ctx, filepath.Join(s.config.State, ".normalization-publish.lock"), unix.LOCK_EX)
		if e != nil {
			t.Fatal(e)
		}
		done := make(chan error, 1)
		go func() { done <- s.runNormalizationJob(ctx, &j) }()
		select {
		case e := <-done:
			unlock()
			t.Fatal("publication bypassed backup lock", e)
		case <-time.After(100 * time.Millisecond):
		}
		if _, e := os.Stat(filepath.Join(base, j.Request.Path)); e != nil {
			unlock()
			t.Fatal("backup lost visible original", e)
		}
		unlock()
		if e := <-done; e != nil {
			t.Fatal(e)
		}
		if e := s.normCleanup(root, &j); e != nil {
			t.Fatal(e)
		}
		j.State = "completed"
		s.normSave(&j)
	})
	t.Run("failed-publication-restores-original", func(t *testing.T) {
		j := writeNormSource(t, s, "pics/restore.png", pngBytes)
		root, _, _ := s.normalizationRoot()
		defer root.Close()
		s.normPrepare(root, &j)
		j.SourceHash = normHash(pngBytes)
		renameExclusive(root, j.Request.Path, filepath.Join(normWorkspace, "original"))
		if e := s.normCleanup(root, &j); e != nil {
			t.Fatal(e)
		}
		got, _ := root.ReadFile(j.Request.Path)
		if !bytes.Equal(got, pngBytes) {
			t.Fatal("raw original lost")
		}
	})
	t.Run("workspace-owner-fencing", func(t *testing.T) {
		a := writeNormSource(t, s, "pics/owner-a.png", pngBytes)
		b := writeNormSource(t, s, "pics/owner-b.png", pngBytes)
		root, _, _ := s.normalizationRoot()
		defer root.Close()
		s.normPrepare(root, &a)
		root.WriteFile(filepath.Join(normWorkspace, "input"), []byte("owner-data"), 0600)
		if e := s.normCleanup(root, &b); !errors.Is(e, errNormalizationBlocked) {
			t.Fatal("another job cleaned owner workspace", e)
		}
		got, _ := root.ReadFile(filepath.Join(normWorkspace, "input"))
		if string(got) != "owner-data" {
			t.Fatal("owner data removed")
		}
		if e := s.normCleanup(root, &a); e != nil {
			t.Fatal(e)
		}
	})
	t.Run("drive-checksum-and-cancellation", func(t *testing.T) {
		f.add("drive-photo", "remote.png", "image/png", false)
		item := f.items["drive-photo"]
		item.Size = fmtInt(len(pngBytes))
		item.SHA256 = strings.Repeat("0", 64)
		f.items[item.ID] = item
		f.payloads[item.ID] = pngBytes
		j, e := s.enqueueNormalization(NormalizationRequest{Source: "drive", Path: item.ID, RelativePath: "bad-checksum.png"}, "", "fixture-account", &item)
		if e != nil {
			t.Fatal(e)
		}
		s.runNormalizationJob(ctx, &j)
		if j.State != "failed" || j.Published {
			t.Fatal("bad checksum accepted", j.State)
		}
		item.Version = "2"
		item.SHA256 = normHash(pngBytes)
		f.items[item.ID] = item
		j, e = s.enqueueNormalization(NormalizationRequest{Source: "drive", Path: item.ID, RelativePath: "cancelled.png"}, "", "fixture-account", &item)
		if e != nil {
			t.Fatal(e)
		}
		s.db.Exec("INSERT INTO fb_operations(id,kind,state,body) VALUES(?,'normCancel','requested','{}')", "nc-"+j.ID)
		s.runNormalizationJob(ctx, &j)
		if j.State != "cancelled" || j.Published {
			t.Fatal("cancellation ignored", j.State)
		}
	})
	t.Run("legacy-extensionless-picture-classification", func(t *testing.T) {
		f.add("extensionless-image", "no-extension", "image/png", false)
		item := f.items["extensionless-image"]
		item.Modified = "2018-10-13T16:18:50.123456789Z"
		item.Size = fmtInt(len(pngBytes))
		item.SHA256 = normHash(pngBytes)
		f.items[item.ID] = item
		f.payloads[item.ID] = pngBytes
		j, e := s.enqueueNormalization(NormalizationRequest{Source: "drive", Path: item.ID, RelativePath: "no-extension"}, "", "fixture-account", &item)
		if e != nil {
			t.Fatal(e)
		}
		if j.SourceMIME != "image/png" {
			t.Fatal("new job did not retain source MIME")
		}
		j.SourceMIME = "" // Simulate the existing archive's older queue records.
		if e = s.normSave(&j); e != nil {
			t.Fatal(e)
		}
		jobs := []NormalizationJob{j}
		if s.classifyNormalizationJobs(ctx, jobs) || !orderNormalizationJobs(jobs) || jobs[0].SourceMIME != "image/png" {
			t.Fatal("legacy extensionless picture missed")
		}
		if e = s.read(j.ID, "normalize", &j); e != nil {
			t.Fatal(e)
		}
		run(t, &j)
		st, e := os.Stat(filepath.Join(base, j.OutputPath))
		if e != nil || st.ModTime().UTC().Format(time.RFC3339Nano) != item.Modified {
			t.Fatal("Drive date not preserved", e)
		}
	})
	t.Run("inventory-is-metadata-only-and-cancellable", func(t *testing.T) {
		f.add("album", "Album", "application/vnd.google-apps.folder", false)
		f.add("album-file", "image.png", "image/png", false)
		item := f.items["album-file"]
		item.Parents = []string{"album"}
		item.Size = fmtInt(len(pngBytes))
		f.items[item.ID] = item
		f.payloads[item.ID] = pngBytes
		f.add("album-native", "Document", "application/vnd.google-apps.document", false)
		item = f.items["album-native"]
		item.Parents = []string{"album"}
		f.items[item.ID] = item
		googleHTTP = &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
			if r.URL.Query().Get("alt") == "media" {
				t.Fatal("inventory prefetched content")
			}
			return f.serve(r)
		})}
		b := NormalizationBatch{ID: ID(), State: "queued", Connection: "fixture-account", Request: NormalizationRequest{Source: "drive", Path: "album"}, Children: []string{}}
		if e := s.insert(b.ID, "normBatch", b.State, ID(), b); e != nil {
			t.Fatal(e)
		}
		if e := s.discoverNormalization(ctx, &b); e != nil {
			t.Fatal(e)
		}
		if len(b.Children) != 2 || b.State != "discovered" {
			t.Fatal("inventory missing entries", b)
		}
		failures := 0
		for _, id := range b.Children {
			var j NormalizationJob
			s.read(id, "normalize", &j)
			if j.State == "failed" {
				failures++
			}
		}
		if failures != 1 {
			t.Fatal("unsupported item not reported")
		}
		r := httptest.NewRequest("POST", "/v1/normalization/batches/"+b.ID+"/cancel", nil)
		r.Header.Set("Authorization", "Bearer "+s.config.Token)
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, r)
		if w.Code != 202 {
			t.Fatal(w.Code, w.Body.String())
		}
		for _, id := range b.Children {
			var j NormalizationJob
			s.read(id, "normalize", &j)
			if e := s.normCheck(ctx, &j); !errors.Is(e, errCancelled) {
				t.Fatal("batch cancellation missed a child", e)
			}
		}
	})
	t.Run("pipeline-drive-download-convert-cleanup", func(t *testing.T) {
		// Use the actual advisory-lock worker; each download must own a private, empty input.
		for _, name := range []string{"owner-a.png", "owner-b.png", "collision.png", "collision.jpg", "restore.png"} {
			os.Remove(filepath.Join(base, "pics", name))
		}
		oldJobs, _ := s.normalizationJobs()
		for _, job := range oldJobs {
			if job.State == "queued" || job.State == "waiting" {
				job.State = "cancelled"
				s.normSave(&job)
			}
		}
		var downloads atomic.Int32
		googleHTTP = &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
			if r.URL.Query().Get("alt") == "media" {
				jobs, e := s.normalizationJobs()
				if e != nil {
					t.Error(e)
				}
				found := false
				for _, job := range jobs {
					if job.Request.Path != filepath.Base(r.URL.Path) || job.Phase != "inspecting" {
						continue
					}
					if !job.IsolatedWorkspace {
						t.Error("download did not use isolated scratch")
					}
					st, e := os.Stat(filepath.Join(base, normWorkspaceFor(&job), "input"))
					if e != nil || st.Size() != 0 {
						t.Error("download input is missing or reused")
					}
					found = true
				}
				if !found {
					t.Error("download has no matching workspace owner")
				}
				downloads.Add(1)
			}
			return f.serve(r)
		})}
		ids := []string{}
		for _, id := range []string{"serial-one", "serial-two"} {
			f.add(id, id+".png", "image/png", false)
			item := f.items[id]
			item.Size = fmtInt(len(pngBytes))
			item.SHA256 = normHash(pngBytes)
			f.items[id] = item
			f.payloads[id] = pngBytes
			j, e := s.enqueueNormalization(NormalizationRequest{Source: "drive", Path: id, RelativePath: id + ".png"}, "", "fixture-account", &item)
			if e != nil {
				t.Fatal(e)
			}
			ids = append(ids, j.ID)
		}
		oldStable := s.config.Normalization.StableSeconds
		s.config.Normalization.StableSeconds = 3600
		workerCtx, cancel := context.WithCancel(ctx)
		// The shared VM development container may have unrelated builds using RAM.
		pipeline := newNormPipeline()
		pipeline.memory = func() (uint64, error) { return 100 << 20, nil }
		workerCtx = context.WithValue(workerCtx, normPipelineKey{}, pipeline)
		done := make(chan struct{})
		go func() { defer close(done); s.RunNormalization(workerCtx) }()
		defer func() { cancel(); <-done; s.config.Normalization.StableSeconds = oldStable }()
		deadline := time.Now().Add(30 * time.Second)
		for time.Now().Before(deadline) {
			complete := 0
			for _, id := range ids {
				var j NormalizationJob
				s.read(id, "normalize", &j)
				if j.State == "failed" {
					t.Fatal(j.Error)
				}
				if j.State == "completed" {
					complete++
				}
			}
			if complete == 2 {
				if downloads.Load() != 2 {
					t.Fatal("unexpected downloads", downloads.Load())
				}
				return
			}
			time.Sleep(30 * time.Millisecond)
		}
		t.Fatal("pipeline worker did not finish")
	})
	googleHTTP = &http.Client{Transport: roundTrip(f.serve)}
	t.Run("space-check-precedes-content-download", func(t *testing.T) {
		old := s.config.Normalization.MinFreeBytes
		s.config.Normalization.MinFreeBytes = ^uint64(0)
		defer func() { s.config.Normalization.MinFreeBytes = old }()
		f.add("no-space", "space.png", "image/png", false)
		meta := f.items["no-space"]
		meta.Size = fmtInt(len(pngBytes))
		f.items[meta.ID] = meta
		f.payloads[meta.ID] = pngBytes
		googleHTTP = &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
			if r.URL.Query().Get("alt") == "media" {
				t.Fatal("download started without disk reserve")
			}
			return f.serve(r)
		})}
		j, e := s.enqueueNormalization(NormalizationRequest{Source: "drive", Path: meta.ID, RelativePath: "space.png"}, "", "fixture-account", &meta)
		if e != nil {
			t.Fatal(e)
		}
		s.runNormalizationJob(ctx, &j)
		if j.State != "waiting" || j.Error != normalizationError(errNormalizationSpace) {
			t.Fatal(j.State, j.Error)
		}
	})
}
func fmtInt(n int) string { b, _ := json.Marshal(n); return string(b) }

// Ensure tool output remains bounded even for malformed media/tool behavior.
func TestNormalizationToolOutputLimit(t *testing.T) {
	b := limitedBuffer{limit: 4}
	if _, e := b.Write([]byte("12345")); e == nil {
		t.Fatal("unbounded tool output")
	}
	_ = io.EOF
}

func TestGoogleGrantLockAcrossProcesses(t *testing.T) {
	a := &Service{config: Config{State: t.TempDir()}}
	b := &Service{config: a.config}
	unlock, e := a.lockGoogle(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()
	if release, e := b.lockGoogle(ctx); e == nil {
		release()
		unlock()
		t.Fatal("second service bypassed grant lock")
	}
	unlock()
	release, e := b.lockGoogle(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	release()
}
