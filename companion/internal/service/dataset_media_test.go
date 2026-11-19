package service

import (
	"context"
	"encoding/json"
	"errors"
	"image"
	"image/color"
	"image/png"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func datasetTestImage(t *testing.T, s *Service, base string) DatasetInput {
	t.Helper()
	f, e := os.Create(filepath.Join(base, "source.png"))
	if e != nil {
		t.Fatal(e)
	}
	img := image.NewRGBA(image.Rect(0, 0, 120, 80))
	for y := 0; y < 80; y++ {
		for x := 0; x < 120; x++ {
			img.Set(x, y, color.RGBA{uint8(x), uint8(y), 100, 255})
		}
	}
	if e = png.Encode(f, img); e != nil {
		t.Fatal(e)
	}
	f.Close()
	st, _ := os.Stat(filepath.Join(base, "source.png"))
	return DatasetInput{ID: ID(), Source: s.config.Roots[0].Name, Path: "/source.png", Revision: filesystemRevision(st)}
}
func TestDatasetPrepareAndRevision(t *testing.T) {
	if _, e := os.Stat("/usr/bin/ffmpeg"); e != nil {
		t.Skip("ffmpeg required")
	}
	s, base := filesystemFixture(t)
	in := datasetTestImage(t, s, base)
	j := DatasetJob{ID: ID(), Request: DatasetRequest{Operation: "prepare", Model: "qwen", Inputs: []DatasetInput{in}}}
	if e := s.datasetProcess(context.Background(), &j); e != nil {
		t.Fatal(e)
	}
	if j.Info == nil || j.Info.Width != 120 || j.Info.Height != 80 {
		t.Fatal(j.Info)
	}
	if _, e := os.Stat(filepath.Join(base, datasetCache(in, "source.png"), "preview.jpg")); e != nil {
		t.Fatal(e)
	}
	os.WriteFile(filepath.Join(base, "source.png"), []byte("changed"), 0600)
	if e := s.datasetProcess(context.Background(), &j); !errors.Is(e, ErrConflict) {
		t.Fatal("changed source accepted", e)
	}
	w := httptest.NewRecorder()
	datasetFailure(w, ErrVaultLocked)
	if w.Code != 423 || !strings.Contains(w.Body.String(), "FileBeans mobile app") {
		t.Fatal(w.Code, w.Body.String())
	}
}
func TestDatasetVideoPrepare(t *testing.T) {
	if _, e := os.Stat("/usr/bin/ffmpeg"); e != nil {
		t.Skip("ffmpeg required")
	}
	s, base := filesystemFixture(t)
	if out, e := exec.Command("/usr/bin/ffmpeg", "-v", "error", "-f", "lavfi", "-i", "testsrc2=size=160x96:rate=12", "-t", "1", "-c:v", "libx264", "-threads", "1", filepath.Join(base, "source.mp4")).CombinedOutput(); e != nil {
		t.Fatal(e, string(out))
	}
	st, _ := os.Stat(filepath.Join(base, "source.mp4"))
	in := DatasetInput{ID: ID(), Source: "samba", Path: "/source.mp4", Revision: filesystemRevision(st)}
	j := DatasetJob{ID: ID(), Request: DatasetRequest{Operation: "prepare", Model: "wan", Inputs: []DatasetInput{in}}}
	if e := s.datasetProcess(context.Background(), &j); e != nil {
		t.Fatal(e)
	}
	if j.Info.Duration != 1 {
		t.Fatal(j.Info)
	}
	for _, name := range []string{"preview.mp4", "thumb0.jpg", "thumb7.jpg"} {
		if _, e := os.Stat(filepath.Join(base, datasetCache(in, "source.mp4"), name)); e != nil {
			t.Fatal(e)
		}
	}
}
func TestDatasetValidation(t *testing.T) {
	in := DatasetInput{ID: ID(), Source: "disk", Path: "/private/source.mp4", Revision: strings.Repeat("a", 64)}
	req := DatasetRequest{Operation: "render", Model: "wan", Inputs: []DatasetInput{in}, Destination: Item{Source: "disk", Path: "/out"}, Clips: []DatasetEdit{{ID: ID(), VideoID: in.ID, Start: 0, End: 1}}}
	if e := validateDataset(req); e != nil {
		t.Fatal(e)
	}
	for _, p := range []string{"/../outside", "/.filebeans-thumbnails/source.mp4", "/Trash/source.mp4"} {
		bad := req
		bad.Inputs = []DatasetInput{in}
		bad.Inputs[0].Path = p
		if validateDataset(bad) == nil {
			t.Fatal("unsafe path", p)
		}
	}
	req.Clips[0].Crop = &DatasetCrop{Width: 2, Height: 1}
	if validateDataset(req) == nil {
		t.Fatal("oversize crop")
	}
}
func testDatasetDatabase(t *testing.T, s *Service) {
	old := s.config.Filesystem
	s.config.Filesystem = FilesystemConfig{Enabled: true, FFmpeg: "/usr/bin/ffmpeg"}
	testDatasetUpload(t, s)
	testDatasetProtectedActivity(t, s)
	defer func() { s.config.Filesystem = old }()
	base := s.config.Roots[0].Path
	in := datasetTestImage(t, s, base)
	req := DatasetRequest{Operation: "render", Model: "qwen", Inputs: []DatasetInput{in}, Destination: Item{Source: in.Source, Path: "/"}, Clips: []DatasetEdit{{ID: ID(), Control: in.ID, Target: in.ID, Caption: "Synthetic caption", ControlCrop: &DatasetCrop{Width: 0.5, Height: 1}}}}
	b, _ := json.Marshal(req)
	request := httptest.NewRequest("POST", "/v1/datasets/jobs", strings.NewReader(string(b)))
	request.Header.Set("Authorization", "Bearer "+s.config.Token)
	request.Header.Set("Idempotency-Key", ID())
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, request)
	if w.Code != 202 {
		t.Fatal(w.Code, w.Body.String())
	}
	var j DatasetJob
	json.Unmarshal(w.Body.Bytes(), &j)
	if !s.datasetWorker(context.Background()) {
		t.Fatal("worker did not claim")
	}
	if e := s.read(j.ID, "dataset", &j); e != nil || j.State != "ready" {
		t.Fatal(e, j.State, j.Error)
	}
	output := filepath.Join(base, strings.TrimPrefix(j.Result.Path, "/"))
	for _, name := range []string{"control/1.jpg", "target/1.jpg", "target/1.txt"} {
		if _, e := os.Stat(filepath.Join(output, name)); e != nil {
			t.Fatal(e)
		}
	}
	// Recover a crash after exclusive publication without creating another output.
	j.State = "processing"
	if e := s.save(j.ID, "dataset", j.State, j); e != nil {
		t.Fatal(e)
	}
	if !s.datasetWorker(context.Background()) {
		t.Fatal("no recovery")
	}
	if e := s.read(j.ID, "dataset", &j); e != nil || j.State != "ready" {
		t.Fatal("recovery", e, j)
	}
}
