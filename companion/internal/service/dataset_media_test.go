package service

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"net/http/httptest"
	"net/url"
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
func datasetReadRequest(s *Service, in DatasetInput, endpoint, method string) *httptest.ResponseRecorder {
	q := url.Values{"source": {in.Source}, "path": {in.Path}, "revision": {in.Revision}, "vault": {in.Vault}, "model": {"qwen"}}
	req := httptest.NewRequest(method, "/v1/datasets/source/"+endpoint+"?"+q.Encode(), nil)
	req.Header.Set("Authorization", "Bearer "+s.config.Token)
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)
	return w
}
func TestDatasetOriginalInfoAndContent(t *testing.T) {
	s, base := filesystemFixture(t)
	in := datasetTestImage(t, s, base)
	original, _ := os.ReadFile(filepath.Join(base, "source.png"))
	w := datasetReadRequest(s, in, "info", "GET")
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var info DatasetInfo
	json.Unmarshal(w.Body.Bytes(), &info)
	if info.Width != 120 || info.Height != 80 {
		t.Fatal(info)
	}
	w = datasetReadRequest(s, in, "content", "GET")
	if w.Code != 200 || !bytes.Equal(w.Body.Bytes(), original) {
		t.Fatal("original bytes", w.Code)
	}
	q := url.Values{"source": {in.Source}, "path": {in.Path}, "revision": {in.Revision}}
	req := httptest.NewRequest("GET", "/v1/datasets/source/content?"+q.Encode(), nil)
	req.Header.Set("Authorization", "Bearer "+s.config.Token)
	req.Header.Set("Range", "bytes=2-9")
	w = httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)
	if w.Code != 206 || !bytes.Equal(w.Body.Bytes(), original[2:10]) {
		t.Fatal("range", w.Code, w.Body.String())
	}
	w = datasetReadRequest(s, in, "content", "HEAD")
	if w.Code != 200 || w.Body.Len() != 0 {
		t.Fatal("HEAD", w.Code)
	}
	entries, _ := os.ReadDir(base)
	for _, entry := range entries {
		if entry.Name() == thumbnailDirectory || entry.Name() == ".filebeans-stage" {
			t.Fatal("read created media", entry.Name())
		}
	}
	os.WriteFile(filepath.Join(base, "source.png"), []byte("changed"), 0600)
	for _, endpoint := range []string{"info", "content"} {
		if w := datasetReadRequest(s, in, endpoint, "GET"); w.Code != 409 {
			t.Fatal("revision", endpoint, w.Code)
		}
	}
	in.Path = "/.filebeans-stage/secret"
	if w := datasetReadRequest(s, in, "content", "GET"); w.Code != 400 {
		t.Fatal("hidden path", w.Code)
	}
	in.Source = "upload"
	if w := datasetReadRequest(s, in, "content", "GET"); w.Code != 400 {
		t.Fatal("internal upload", w.Code)
	}
}
func TestDatasetVideoOriginal(t *testing.T) {
	s, base := filesystemFixture(t)
	if out, e := exec.Command("/usr/bin/ffmpeg", "-v", "error", "-f", "lavfi", "-i", "testsrc2=size=160x96:rate=12", "-t", "1", "-c:v", "libx264", "-threads", "1", filepath.Join(base, "source.mp4")).CombinedOutput(); e != nil {
		t.Fatal(e, string(out))
	}
	st, _ := os.Stat(filepath.Join(base, "source.mp4"))
	in := DatasetInput{ID: ID(), Source: "samba", Path: "/source.mp4", Revision: filesystemRevision(st)}
	root, f, _, e := s.datasetInput(in)
	if e != nil {
		t.Fatal(e)
	}
	defer root.Close()
	defer f.Close()
	info, e := s.datasetProbe(context.Background(), f, "wan")
	if e != nil || info.Duration != 1 {
		t.Fatal(info, e)
	}
	original, _ := os.ReadFile(filepath.Join(base, "source.mp4"))
	w := datasetReadRequest(s, in, "content", "GET")
	if w.Code != 200 || !bytes.Equal(w.Body.Bytes(), original) {
		t.Fatal("original video", w.Code)
	}
	if e = s.datasetProcess(context.Background(), &DatasetJob{Request: DatasetRequest{Operation: "prepare", Inputs: []DatasetInput{in}}}); e == nil {
		t.Fatal("prepare executed")
	}
	if _, e = os.Stat(filepath.Join(base, thumbnailDirectory)); !os.IsNotExist(e) {
		t.Fatal("preview directory created", e)
	}
}
func TestDatasetPrepareSubmissionRejected(t *testing.T) {
	s, _ := filesystemFixture(t)
	req := httptest.NewRequest("POST", "/v1/datasets/jobs", strings.NewReader(`{"operation":"prepare","model":"wan","inputs":[]}`))
	req.Header.Set("Authorization", "Bearer "+s.config.Token)
	req.Header.Set("Idempotency-Key", ID())
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)
	if w.Code != 400 {
		t.Fatal(w.Code, w.Body.String())
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
	original, _ := os.ReadFile(filepath.Join(base, "source.png"))

	legacy := DatasetJob{ID: ID(), State: "queued", Request: DatasetRequest{Operation: "prepare", Model: "qwen", Inputs: []DatasetInput{in}}}
	if e := s.insert(legacy.ID, "dataset", legacy.State, ID(), legacy); e != nil {
		t.Fatal(e)
	}
	defer s.db.Exec("DELETE FROM fb_operations WHERE id=?", legacy.ID)
	s.datasetWorker(context.Background())
	if e := s.read(legacy.ID, "dataset", &legacy); e != nil || legacy.State != "cancelled" {
		t.Fatal("legacy preparation not retired", legacy, e)
	}
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
	unchanged, _ := os.ReadFile(filepath.Join(base, "source.png"))
	if !bytes.Equal(original, unchanged) {
		t.Fatal("export modified original")
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
