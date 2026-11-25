package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestArchiveTaskProtectionAndRetry(t *testing.T) {
	s := testVaultService()
	s.vaults.states[1].unlocked = false
	link := ArchiveLink{ID: ID(), Library: ArchiveEndpoint{Source: "Protected", Path: "/pics"}, Archive: ArchiveEndpoint{Source: "Other", Path: "/originals"}}
	j := newArchiveTask(link, "ingest", "private/photo.png")
	plain, _ := json.Marshal(j)
	sealed, e := s.encodeRecord(j.ID, archiveTaskKind, plain)
	if e != nil {
		t.Fatal(e)
	}
	if bytes.Contains(sealed, []byte("private")) || bytes.Contains(sealed, []byte("originals")) {
		t.Fatal("plaintext metadata")
	}
	decoded, e := s.decodeRecord(j.ID, archiveTaskKind, sealed)
	if e != nil || !bytes.Equal(decoded, plain) {
		t.Fatal("offline archive blocked Library queue", e)
	}
	for i, want := range []time.Duration{time.Minute, 5 * time.Minute, 15 * time.Minute, 30 * time.Minute, 30 * time.Minute} {
		if got := archiveRetry(i); got != want {
			t.Fatal(i, got)
		}
	}
	if archiveQuiet != 5*time.Minute {
		t.Fatal("quiet period changed")
	}
	if !archiveTouches("folder/photo.jpg", "folder/photo.png", false) || archiveTouches("other/photo.jpg", "folder/photo.png", false) {
		t.Fatal("stem routing")
	}
}

type archiveSyncFixture struct {
	s                    *Service
	link                 ArchiveLink
	vps, archive, prefix string
}

func syncFixture(t *testing.T, parent *Service) *archiveSyncFixture {
	t.Helper()
	cfg := parent.config
	cfg.Roots = append([]Root(nil), cfg.Roots...)
	cfg.Filesystem = FilesystemConfig{Enabled: true, FFmpeg: "/usr/bin/ffmpeg", PDFToPPM: "/usr/bin/pdftoppm"}
	s := &Service{db: parent.db, config: cfg}
	prefix := "phase-three-" + ID()
	library := "/" + prefix + "/library"
	archive := "/" + prefix + "/archive"
	f := &archiveSyncFixture{s: s, prefix: prefix, vps: filepath.Join(cfg.Roots[0].Path, library), archive: filepath.Join(cfg.Roots[1].Path, archive)}
	for _, p := range []string{f.vps, f.archive} {
		if e := os.MkdirAll(p, 0770); e != nil {
			t.Fatal(e)
		}
	}
	w := archiveRequest(s, "POST", "/v1/archive-links", `{"library":{"source":"vps","path":"`+library+`"},"archive":{"source":"samba","path":"`+archive+`"}}`)
	if w.Code != 201 {
		t.Fatal(w.Code, w.Body.String())
	}
	if e := json.Unmarshal(w.Body.Bytes(), &f.link); e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() {
		tasks, _ := s.archiveTasks()
		for _, j := range tasks {
			if j.Link == f.link.ID {
				s.removeArchiveTask(&j)
			}
		}
		records, _ := s.records("trash")
		for _, b := range records {
			var j Trash
			json.Unmarshal(b, &j)
			if strings.HasPrefix(j.Original, prefix+"/") {
				s.db.Exec("DELETE FROM fb_operations WHERE id=?", j.ID)
			}
		}
		s.db.Exec("DELETE FROM fb_operations WHERE id IN (?,?)", f.link.ID, "policy-"+f.link.ID)
		for _, r := range cfg.Roots {
			os.RemoveAll(filepath.Join(r.Path, prefix))
		}
	})
	return f
}
func (f *archiveSyncFixture) photo(t *testing.T, name string) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 96, 64))
	draw.Draw(img, img.Bounds(), image.NewUniform(color.RGBA{R: 24, G: 80, B: 170, A: 255}), image.Point{}, draw.Src)
	var data bytes.Buffer
	if e := png.Encode(&data, img); e != nil {
		t.Fatal(e)
	}
	p := filepath.Join(f.vps, name)
	os.MkdirAll(filepath.Dir(p), 0770)
	if e := os.WriteFile(p, data.Bytes(), 0660); e != nil {
		t.Fatal(e)
	}
	if e := f.s.archiveAdded("vps", filepath.Join(f.link.Library.Path, name)); e != nil {
		t.Fatal(e)
	}
	return data.Bytes()
}
func (f *archiveSyncFixture) tasks(t *testing.T) []archiveTask {
	t.Helper()
	all, e := f.s.archiveTasks()
	if e != nil {
		t.Fatal(e)
	}
	out := []archiveTask{}
	for _, j := range all {
		if j.Link == f.link.ID {
			out = append(out, j)
		}
	}
	return out
}
func (f *archiveSyncFixture) ingest(t *testing.T) archiveTask {
	t.Helper()
	for _, j := range f.tasks(t) {
		if j.Kind == "ingest" {
			return j
		}
	}
	t.Fatal("no ingest task")
	return archiveTask{}
}
func (f *archiveSyncFixture) encode(t *testing.T) archiveTask {
	t.Helper()
	j := f.ingest(t)
	j.Next = 0
	if e := f.s.saveArchiveTask(&j); e != nil {
		t.Fatal(e)
	}
	if e := f.s.runArchiveIngest(context.Background(), &j, true); e != nil {
		t.Fatal("encode", e)
	}
	return j
}
func (f *archiveSyncFixture) drain(t *testing.T) {
	t.Helper()
	var last error
	for pass := 0; pass < 8; pass++ {
		pending := 0
		for _, j := range f.tasks(t) {
			if j.State == "paired" {
				continue
			}
			pending++
			var e error
			if j.Kind == "ingest" && (j.State == "waiting" || j.State == "publishing" || j.State == "cleanup") {
				e = f.s.runArchiveIngest(context.Background(), &j, true)
			} else {
				e = f.s.runArchiveTask(context.Background(), &j)
			}
			if e != nil {
				last = e
			}
		}
		if pending == 0 {
			return
		}
	}
	t.Fatalf("queue did not settle: %v; tasks=%+v", last, f.tasks(t))
}
func (f *archiveSyncFixture) move(t *testing.T, from, to string) {
	t.Helper()
	os.MkdirAll(filepath.Dir(filepath.Join(f.vps, to)), 0770)
	w := archiveRequest(f.s, "POST", "/v1/fs/move", `{"source":"vps","from":"`+filepath.Join(f.link.Library.Path, from)+`","to":"`+filepath.Join(f.link.Library.Path, to)+`"}`)
	if w.Code != 204 {
		t.Fatal("move", w.Code, w.Body.String())
	}
}
func testArchiveSyncDatabase(t *testing.T, parent *Service) {
	for _, tool := range []string{"ffmpeg", "ffprobe", "prlimit"} {
		if _, e := exec.LookPath(tool); e != nil {
			t.Fatal("missing phase-three tool", tool)
		}
	}
	t.Run("offline-encode-restart-move-backup", func(t *testing.T) {
		f := syncFixture(t, parent)
		original := f.photo(t, "photo.png")
		j := f.ingest(t)
		if delta := time.Until(time.Unix(j.Next, 0)); delta < 299*time.Second || delta > 301*time.Second {
			t.Fatal("quiet period", delta)
		}
		online := f.s.config.Roots[1].Path
		f.s.config.Roots[1].Path = online + "-offline"
		j = f.encode(t)
		if j.State != "backup" || j.Relative != "photo.jpg" {
			t.Fatal(j)
		}
		raw := filepath.Join(f.vps, archiveStage, j.ID, "original")
		if data, e := os.ReadFile(raw); e != nil || !bytes.Equal(data, original) {
			t.Fatal("original not retained", e)
		}
		if _, e := os.Stat(filepath.Join(f.vps, "photo.jpg")); e != nil {
			t.Fatal("encoded copy not published", e)
		}
		e := f.s.runArchiveTask(context.Background(), &j)
		if e == nil {
			t.Fatal("offline backup succeeded")
		}
		f.s.failArchiveTask(&j, e)
		j = f.ingest(t)
		if j.Attempts != 1 || time.Until(time.Unix(j.Next, 0)) < 58*time.Second {
			t.Fatal("retry missing", j)
		}
		w := archiveRequest(f.s, "GET", "/v1/archive-sync/status", "")
		if w.Code != 200 || !strings.Contains(w.Body.String(), `"pendingOriginals":1`) {
			t.Fatal(w.Code, w.Body.String())
		}
		f.s = &Service{db: f.s.db, config: f.s.config}
		f.move(t, "photo.jpg", "nested/renamed.jpg")
		j = f.ingest(t)
		if j.Original != "nested/renamed.png" {
			t.Fatal("pending destination not moved", j)
		}
		f.s.config.Roots[1].Path = online
		f.drain(t)
		if data, e := os.ReadFile(filepath.Join(f.archive, "nested/renamed.png")); e != nil || !bytes.Equal(data, original) {
			t.Fatal("archive original differs", e)
		}
		if _, e := os.Stat(raw); !os.IsNotExist(e) {
			t.Fatal("retained stage not removed", e)
		}
		if caches, e := os.ReadDir(filepath.Join(f.archive, "nested", thumbnailDirectory)); e != nil || len(caches) != 1 {
			t.Fatal("archive thumbnail missing", e)
		}
	})
	t.Run("paired-moves-trash-and-restore", func(t *testing.T) {
		f := syncFixture(t, parent)
		original := f.photo(t, "a/photo.png")
		f.encode(t)
		f.drain(t)
		f.move(t, "a/photo.jpg", "a/renamed.jpg")
		f.move(t, "a", "b")
		f.drain(t)
		if data, e := os.ReadFile(filepath.Join(f.archive, "b/renamed.png")); e != nil || !bytes.Equal(data, original) {
			t.Fatal("paired move", e)
		}
		tr, e := f.s.trash(Item{Source: "vps", Path: filepath.Join(f.link.Library.Path, "b")}, ID())
		if e != nil {
			t.Fatal(e)
		}
		f.drain(t)
		if _, e := os.Stat(filepath.Join(f.archive, "b")); !os.IsNotExist(e) {
			t.Fatal("archive not trashed", e)
		}
		if e = f.s.restore(tr.ID); e != nil {
			t.Fatal(e)
		}
		f.drain(t)
		if data, e := os.ReadFile(filepath.Join(f.archive, "b/renamed.png")); e != nil || !bytes.Equal(data, original) {
			t.Fatal("paired restore", e)
		}
	})
	t.Run("extension-only-rename-keeps-original", func(t *testing.T) {
		f := syncFixture(t, parent)
		original := f.photo(t, "photo.png")
		f.encode(t)
		f.drain(t)
		f.move(t, "photo.jpg", "photo.jpeg")
		f.drain(t)
		if data, e := os.ReadFile(filepath.Join(f.archive, "photo.png")); e != nil || !bytes.Equal(data, original) {
			t.Fatal("extension-only rename changed original", e)
		}
		if len(f.tasks(t)) != 0 {
			t.Fatal("extension-only rename left pending work")
		}
	})

	t.Run("trash-before-delay-and-reuse-name", func(t *testing.T) {
		f := syncFixture(t, parent)
		old := f.photo(t, "photo.png")
		tr, e := f.s.trash(Item{Source: "vps", Path: filepath.Join(f.link.Library.Path, "photo.png")}, ID())
		if e != nil {
			t.Fatal(e)
		}
		if e = f.s.purge(&tr); e == nil {
			t.Fatal("unbacked original could be purged")
		}
		f.photo(t, "photo.png")
		f.drain(t)
		if len(f.tasks(t)) != 1 || f.tasks(t)[0].State != "paired" {
			t.Fatal("trash queue", f.tasks(t))
		}
		pair := f.tasks(t)[0]
		var archived Trash
		if e = f.s.read(pair.ArchiveTrashID, "trash", &archived); e != nil {
			t.Fatal(e)
		}
		access, e := f.s.archiveWriter().accessTrash(archived)
		if e != nil {
			t.Fatal(e)
		}
		defer access.close()
		data, e := access.bin.ReadFile(trashPath(archived))
		if e != nil || !bytes.Equal(data, old) {
			t.Fatal("old original not safely trashed", e)
		}
		if _, e = os.Stat(filepath.Join(f.archive, "photo.png")); e != nil {
			t.Fatal("replacement original not archived", e)
		}
	})
	t.Run("publishing-recovery", func(t *testing.T) {
		f := syncFixture(t, parent)
		original := f.photo(t, "photo.png")
		j := f.ingest(t)
		base := filepath.Join(f.vps, archiveStage, j.ID)
		os.MkdirAll(base, 0770)
		os.WriteFile(filepath.Join(base, "output.png"), original, 0660)
		r, e := f.s.root("vps")
		if e != nil {
			t.Fatal(e)
		}
		defer r.Close()
		j.Hash, e = archiveHash(r, filepath.Join(f.link.Library.Path, j.Relative))
		if e != nil {
			t.Fatal(e)
		}
		j.OutputHash = j.Hash
		j.Output = "photo.png"
		j.State = "publishing"
		if e = f.s.saveArchiveTask(&j); e != nil {
			t.Fatal(e)
		}
		os.Rename(filepath.Join(f.vps, "photo.png"), filepath.Join(base, "original"))
		if e = f.s.runArchiveIngest(context.Background(), &j, true); e != nil || j.State != "backup" {
			t.Fatal("publication did not recover", e, j)
		}
		f.drain(t)
	})
	t.Run("video-profile-and-original", func(t *testing.T) {
		f := syncFixture(t, parent)
		path := filepath.Join(f.vps, "clip.mov")
		cmd := exec.Command("/usr/bin/ffmpeg", "-v", "error", "-nostdin", "-f", "lavfi", "-i", "color=c=blue:s=160x120:r=10:d=1", "-f", "lavfi", "-i", "sine=frequency=440:duration=1", "-t", "1", "-c:v", "libx264", "-pix_fmt", "yuv420p", "-threads", "1", "-c:a", "aac", "-fs", "1048576", path)
		if out, e := cmd.CombinedOutput(); e != nil {
			t.Fatal(e, string(out))
		}
		original, e := os.ReadFile(path)
		if e != nil {
			t.Fatal(e)
		}
		if e = f.s.archiveAdded("vps", filepath.Join(f.link.Library.Path, "clip.mov")); e != nil {
			t.Fatal(e)
		}
		j := f.encode(t)
		if j.Relative != "clip.mp4" {
			t.Fatal(j)
		}
		f.drain(t)
		if data, e := os.ReadFile(filepath.Join(f.archive, "clip.mov")); e != nil || !bytes.Equal(data, original) {
			t.Fatal("video original not preserved", e)
		}
		file, e := os.Open(filepath.Join(f.vps, "clip.mp4"))
		if e != nil {
			t.Fatal(e)
		}
		defer file.Close()
		info, e := f.s.probeMedia(context.Background(), file, false)
		if e != nil {
			t.Fatal(e)
		}
		plan, e := makeMediaPlan(info, "clip.mp4")
		if e != nil || plan.Video.Codec != "hevc" || plan.Audio == nil || plan.Audio.Codec != "aac" || plan.Audio.Channels != 2 {
			t.Fatal("video profile", e, plan)
		}
	})
	t.Run("original-retained-on-archive-conflict", func(t *testing.T) {
		f := syncFixture(t, parent)
		original := f.photo(t, "photo.png")
		j := f.encode(t)
		os.WriteFile(filepath.Join(f.archive, "photo.png"), []byte("foreign original"), 0660)
		if e := f.s.runArchiveTask(context.Background(), &j); !errors.Is(e, ErrConflict) {
			t.Fatal(e)
		}
		if data, e := os.ReadFile(filepath.Join(f.vps, archiveStage, j.ID, "original")); e != nil || !bytes.Equal(data, original) {
			t.Fatal("original released after conflict", e)
		}
	})
	t.Run("empty-folder-moves-before-archive", func(t *testing.T) {
		f := syncFixture(t, parent)
		os.Mkdir(filepath.Join(f.vps, "new"), 0770)
		if e := f.s.archiveDirectoryAdded("vps", filepath.Join(f.link.Library.Path, "new")); e != nil {
			t.Fatal(e)
		}
		f.move(t, "new", "second")
		f.move(t, "second", "third")
		f.drain(t)
		if _, e := os.Stat(filepath.Join(f.archive, "third")); e != nil {
			t.Fatal(e)
		}
		for _, old := range []string{"new", "second"} {
			if _, e := os.Stat(filepath.Join(f.archive, old)); !os.IsNotExist(e) {
				t.Fatal("stale directory", old, e)
			}
		}
	})
	t.Run("queue-and-policy-readable-with-archive-locked", func(t *testing.T) {
		f := syncFixture(t, parent)
		manager := testVaultService().vaults
		manager.states[0].config.Source = "vps"
		manager.states[0].config.Path = f.link.Library.Path
		manager.states[1].config.Source = "samba"
		manager.states[1].config.Path = f.link.Archive.Path
		f.s.vaults = manager
		f.link.Library.Vault = manager.states[0].config.ID
		f.link.Library.Relative = "."
		f.link.Archive.Vault = manager.states[1].config.ID
		f.link.Archive.Relative = "."
		if e := f.s.save(f.link.ID, "archiveLink", "linked", f.link); e != nil {
			t.Fatal(e)
		}
		if _, e := f.s.archivePolicyLinks(); e != nil {
			t.Fatal(e)
		}
		manager.states[1].unlocked = false
		if _, e := f.s.archivePolicyLinks(); e != nil {
			t.Fatal("cached routing requires archive key", e)
		}
		f.photo(t, "offline.png")
		j := f.ingest(t)
		var stored []byte
		if e := f.s.db.QueryRow("SELECT body FROM fb_operations WHERE id=?", j.ID).Scan(&stored); e != nil {
			t.Fatal(e)
		}
		if bytes.Contains(stored, []byte("offline.png")) {
			t.Fatal("original filename leaked")
		}
		if _, e := f.s.db.Exec("UPDATE fb_operations SET body=CONCAT(body,' ') WHERE id=?", f.link.ID); e != nil {
			t.Fatal(e)
		}
		if _, e := f.s.archivePolicyLinks(); e == nil {
			t.Fatal("stale routing accepted after link change")
		}
		manager.states[1].unlocked = true
	})
	t.Run("drift-never-deletes-original", func(t *testing.T) {
		f := syncFixture(t, parent)
		f.photo(t, "new.png")
		os.WriteFile(filepath.Join(f.archive, "external.png"), []byte("original"), 0660)
		f.s.discoverArchiveMedia(context.Background())
		found := false
		for _, j := range f.tasks(t) {
			if j.Kind == "drift" {
				found = true
			}
		}
		if !found {
			t.Fatal("external discrepancy not reported")
		}
		if data, _ := os.ReadFile(filepath.Join(f.archive, "external.png")); string(data) != "original" {
			t.Fatal("external original changed")
		}
	})
}

func TestArchiveCopyBoundsAndCollision(t *testing.T) {
	left, _ := os.OpenRoot(t.TempDir())
	defer left.Close()
	right, _ := os.OpenRoot(t.TempDir())
	defer right.Close()
	left.WriteFile("original", []byte("original"), 0660)
	right.WriteFile("destination", []byte("foreign"), 0660)
	s := &Service{}
	if e := s.copyArchiveOriginal(context.Background(), left, "original", right, "destination", "", ".stage/copy"); !errors.Is(e, ErrConflict) {
		t.Fatal(e)
	}
	if data, _ := right.ReadFile("destination"); string(data) != "foreign" {
		t.Fatal("collision overwritten")
	}
	if _, e := archiveSpace(left, ".", 1<<62); !errors.Is(e, errNormalizationSpace) {
		t.Fatal("reserve not enforced", e)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if e := s.copyArchiveOriginal(ctx, left, "original", right, "new", "", ".stage/copy"); !errors.Is(e, context.Canceled) {
		t.Fatal("copy ignored cancellation", e)
	}
}
