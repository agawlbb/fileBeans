package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"golang.org/x/sys/unix"
	"image"
	"image/color"
	"image/jpeg"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func filesystemFixture(t *testing.T) (*Service, string) {
	t.Helper()
	base := t.TempDir()
	s := &Service{config: Config{Token: strings.Repeat("t", 32), State: t.TempDir(), Roots: []Root{{Name: "samba", Path: base}}, Filesystem: FilesystemConfig{Enabled: true, FFmpeg: "/usr/bin/ffmpeg", PDFToPPM: "/usr/bin/pdftoppm"}}}
	return s, base
}
func fsRequest(t *testing.T, s *Service, method, route, p string, data io.Reader, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	q := url.Values{"source": {"samba"}, "path": {p}}
	req := httptest.NewRequest(method, "/v1/fs/"+route+"?"+q.Encode(), data)
	req.Header.Set("Authorization", "Bearer "+s.config.Token)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	out := httptest.NewRecorder()
	s.Handler().ServeHTTP(out, req)
	return out
}
func TestFilesystemLifecycleAndConflicts(t *testing.T) {
	s, base := filesystemFixture(t)
	for _, tc := range []struct {
		method, route, path, content string
		status                       int
	}{
		{"POST", "folders", "/Docs", "", 201}, {"POST", "folders", "/Docs", "", 409},
		{"POST", "content", "/Docs/a+b.txt", "original", 201}, {"POST", "content", "/Docs/a+b.txt", "bad", 409},
		{"PUT", "content", "/Docs/a+b.txt", "bad", 428},
	} {
		if got := fsRequest(t, s, tc.method, tc.route, tc.path, strings.NewReader(tc.content), nil); got.Code != tc.status {
			t.Fatalf("%+v: %d %s", tc, got.Code, got.Body.String())
		}
	}
	info := fsRequest(t, s, "GET", "resources", "/Docs/a+b.txt", nil, nil)
	var entry filesystemEntry
	if e := json.Unmarshal(info.Body.Bytes(), &entry); e != nil {
		t.Fatal(e)
	}
	if entry.Name != "a+b.txt" || entry.Revision == "" {
		t.Fatal(entry)
	}
	etag := `"` + entry.Revision + `"`
	got := fsRequest(t, s, "PUT", "content", "/Docs/a+b.txt", strings.NewReader("edited"), map[string]string{"If-Match": etag})
	if got.Code != 201 {
		t.Fatal(got.Code, got.Body.String())
	}
	got = fsRequest(t, s, "PUT", "content", "/Docs/a+b.txt", strings.NewReader("stale"), map[string]string{"If-Match": etag})
	if got.Code != 412 {
		t.Fatal(got.Code)
	}
	data, _ := os.ReadFile(filepath.Join(base, "Docs/a+b.txt"))
	if string(data) != "edited" {
		t.Fatal(string(data))
	}
	listing := fsRequest(t, s, "GET", "resources", "/Docs", nil, nil)
	if strings.Contains(listing.Body.String(), thumbnailDirectory) {
		t.Fatal("private files listed")
	}
	move := `{"source":"samba","from":"/Docs/a+b.txt","to":"/Docs/renamed.txt"}`
	got = fsRequest(t, s, "POST", "move", "/", strings.NewReader(move), nil)
	if got.Code != 204 {
		t.Fatal(got.Code, got.Body.String())
	}
	if _, e := os.Stat(filepath.Join(base, "Docs/renamed.txt")); e != nil {
		t.Fatal(e)
	}
}
func TestFilesystemRangeAndBoundary(t *testing.T) {
	s, base := filesystemFixture(t)
	os.WriteFile(filepath.Join(base, "movie.mp4"), []byte("0123456789"), 0660)
	out := fsRequest(t, s, "GET", "content", "/movie.mp4", nil, map[string]string{"Range": "bytes=3-6"})
	if out.Code != 206 || out.Body.String() != "3456" || out.Header().Get("Content-Range") != "bytes 3-6/10" {
		t.Fatal(out.Code, out.Body.String(), out.Header())
	}
	if out := fsRequest(t, s, "GET", "content", "/movie.mp4", nil, map[string]string{"Range": "bytes=20-30"}); out.Code != 416 {
		t.Fatal(out.Code)
	}
	outside := t.TempDir()
	os.WriteFile(filepath.Join(outside, "private"), []byte("secret"), 0600)
	os.Symlink(outside, filepath.Join(base, "link"))
	for _, p := range []string{"/../escape", "/link/private", "/.filebeans-stage/x", "/.filebeans-vaults/x", "/Docs/.filebeans-thumbnails/x"} {
		if got := fsRequest(t, s, "GET", "content", p, nil, nil); got.Code == 200 {
			t.Fatalf("exposed %s", p)
		}
	}
	req := httptest.NewRequest("GET", "/v1/fs/content?source=upload&path=/private", nil)
	req.Header.Set("Authorization", "Bearer "+s.config.Token)
	out = httptest.NewRecorder()
	s.Handler().ServeHTTP(out, req)
	if out.Code != 400 {
		t.Fatal(out.Code)
	}
	req = httptest.NewRequest("GET", "/v1/fs/sources", nil)
	out = httptest.NewRecorder()
	s.Handler().ServeHTTP(out, req)
	if out.Code != 401 {
		t.Fatal(out.Code)
	}
	s.config.Filesystem.Enabled = false
	if got := fsRequest(t, s, "GET", "resources", "/", nil, nil); got.Code != 404 {
		t.Fatal("filesystem not opt-in")
	}
}
func TestFilesystemConcurrentCreateAndReplace(t *testing.T) {
	s, base := filesystemFixture(t)
	var wg sync.WaitGroup
	codes := make(chan int, 2)
	for _, text := range []string{"one", "two"} {
		wg.Add(1)
		go func(text string) {
			defer wg.Done()
			codes <- fsRequest(t, s, "POST", "content", "/same.txt", strings.NewReader(text), nil).Code
		}(text)
	}
	wg.Wait()
	close(codes)
	statuses := map[int]int{}
	for code := range codes {
		statuses[code]++
	}
	if statuses[201] != 1 || statuses[409] != 1 {
		t.Fatal(statuses)
	}
	info := fsRequest(t, s, "GET", "resources", "/same.txt", nil, nil)
	var entry filesystemEntry
	json.Unmarshal(info.Body.Bytes(), &entry)
	codes = make(chan int, 2)
	for _, text := range []string{"three", "four"} {
		wg.Add(1)
		go func(text string) {
			defer wg.Done()
			codes <- fsRequest(t, s, "PUT", "content", "/same.txt", strings.NewReader(text), map[string]string{"If-Match": `"` + entry.Revision + `"`}).Code
		}(text)
	}
	wg.Wait()
	close(codes)
	statuses = map[int]int{}
	for code := range codes {
		statuses[code]++
	}
	if statuses[201] != 1 || statuses[412] != 1 {
		t.Fatal(statuses)
	}
	temps, _ := os.ReadDir(filepath.Join(base, thumbnailDirectory))
	if len(temps) != 0 {
		t.Fatal("leaked upload files", temps)
	}
}
func TestFilesystemThumbnailsCacheAndInvalidate(t *testing.T) {
	s, base := filesystemFixture(t)
	img := image.NewRGBA(image.Rect(0, 0, 640, 480))
	img.Set(0, 0, color.White)
	var original bytes.Buffer
	jpeg.Encode(&original, img, nil)
	os.WriteFile(filepath.Join(base, "photo.jpg"), original.Bytes(), 0660)
	out := fsRequest(t, s, "GET", "thumbnail", "/photo.jpg", nil, nil)
	if out.Code != 200 {
		t.Fatal(out.Code, out.Body.String())
	}
	config, _, e := image.DecodeConfig(bytes.NewReader(out.Body.Bytes()))
	if e != nil || config.Width != 320 || config.Height != 240 {
		t.Fatal(config, e)
	}
	cached := fsRequest(t, s, "GET", "thumbnail", "/photo.jpg", nil, nil)
	if !bytes.Equal(cached.Body.Bytes(), out.Body.Bytes()) {
		t.Fatal("cache differs")
	}
	files, _ := os.ReadDir(filepath.Join(base, thumbnailDirectory))
	if len(files) != 1 {
		t.Fatal(files)
	}
	os.WriteFile(filepath.Join(base, "photo.jpg"), append(original.Bytes(), 0), 0660)
	out = fsRequest(t, s, "GET", "thumbnail", "/photo.jpg", nil, nil)
	if out.Code != 200 {
		t.Fatal(out.Code)
	}
	files, _ = os.ReadDir(filepath.Join(base, thumbnailDirectory))
	if len(files) != 2 {
		t.Fatal("revision did not invalidate", files)
	}
	os.WriteFile(filepath.Join(base, "notes.txt"), []byte("Hello\nfileBeans"), 0660)
	out = fsRequest(t, s, "GET", "thumbnail", "/notes.txt", nil, nil)
	if out.Code != 200 {
		t.Fatal(out.Code)
	}
	if out := fsRequest(t, s, "GET", "content", "/"+thumbnailDirectory+"/"+files[0].Name(), nil, nil); out.Code == http.StatusOK {
		t.Fatal("cache directly exposed")
	}
}

func TestFilesystemConsumerReadinessRole(t *testing.T) {
	b := testBroker(t)
	b.config.FilesystemConsumer = true
	control, reader := b.config.ControlUID, b.config.ReaderUID
	payload := `{"version":1,"features":["requestLeases","pathGuard","encryptedPreviews","indexIsolation","metadataIsolation","mutationBoundaries","eventLeaseScopes"]}`
	if out := brokerRequest(b, &reader, "POST", "/v1/consumer/ready", payload); out.Code != 403 {
		t.Fatal("legacy reader registered", out.Code)
	}
	if out := brokerRequest(b, &control, "POST", "/v1/consumer/ready", `{"version":1}`); out.Code != 400 {
		t.Fatal("incomplete hooks registered", out.Code)
	}
	if out := brokerRequest(b, &control, "POST", "/v1/consumer/ready", payload); out.Code != 200 {
		t.Fatal(out.Code, out.Body.String())
	}
}

func TestFilesystemRendererVideoAndPDF(t *testing.T) {
	s, base := filesystemFixture(t)
	ffmpeg := s.config.Filesystem.FFmpeg
	if _, e := os.Stat(ffmpeg); e != nil {
		t.Skip("FFmpeg unavailable")
	}
	// The small committed playback fixture contains a real seekable MP4.
	media, e := os.ReadFile("../../../Tests/Fixtures/range-probe.mp4")
	if e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(filepath.Join(base, "clip.mp4"), media, 0660); e != nil {
		t.Fatal(e)
	}
	out := fsRequest(t, s, "GET", "thumbnail", "/clip.mp4", nil, nil)
	if out.Code != 200 {
		root, _ := os.OpenRoot(base)
		defer root.Close()
		f, _ := root.Open("clip.mp4")
		defer f.Close()
		cacheRoot, cacheDir, cacheError := s.thumbnailCache(root, "samba", "clip.mp4")
		if cacheError != nil {
			t.Fatal(cacheError)
		}
		if cacheRoot != root {
			defer cacheRoot.Close()
		}
		_, err := s.renderPreview(context.Background(), f, "video/mp4", cacheRoot, cacheDir)
		t.Fatal("video", out.Code, out.Body.String(), err)
	}
	if _, _, e = image.DecodeConfig(bytes.NewReader(out.Body.Bytes())); e != nil {
		t.Fatal(e)
	}
	// Generate a minimal valid one-page PDF with accurate cross-reference offsets.
	var pdf bytes.Buffer
	pdf.WriteString("%PDF-1.4\n")
	offsets := []int{0}
	for i, obj := range []string{`<< /Type /Catalog /Pages 2 0 R >>`, `<< /Type /Pages /Kids [3 0 R] /Count 1 >>`, `<< /Type /Page /Parent 2 0 R /MediaBox [0 0 200 200] >>`} {
		offsets = append(offsets, pdf.Len())
		fmt.Fprintf(&pdf, "%d 0 obj\n%s\nendobj\n", i+1, obj)
	}
	start := pdf.Len()
	fmt.Fprintf(&pdf, "xref\n0 4\n0000000000 65535 f \n")
	for _, off := range offsets[1:] {
		fmt.Fprintf(&pdf, "%010d 00000 n \n", off)
	}
	fmt.Fprintf(&pdf, "trailer\n<< /Size 4 /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", start)
	os.WriteFile(filepath.Join(base, "document.pdf"), pdf.Bytes(), 0660)
	out = fsRequest(t, s, "GET", "thumbnail", "/document.pdf", nil, nil)
	if out.Code != 200 {
		t.Fatal("PDF", out.Code, out.Body.String())
	}
	if _, _, e = image.DecodeConfig(bytes.NewReader(out.Body.Bytes())); e != nil {
		t.Fatal(e)
	}
}

func TestFilesystemRejectsSpecialFilesAndInterruptedUploads(t *testing.T) {
	s, base := filesystemFixture(t)
	if e := unix.Mkfifo(filepath.Join(base, "pipe"), 0600); e != nil {
		t.Fatal(e)
	}
	for _, route := range []string{"content", "thumbnail"} {
		if out := fsRequest(t, s, "GET", route, "/pipe", nil, nil); out.Code != 400 {
			t.Fatal(route, out.Code)
		}
	}
	out := fsRequest(t, s, "POST", "content", "/partial.txt", brokenFilesystemBody{}, nil)
	if out.Code == 201 {
		t.Fatal("published incomplete upload")
	}
	if _, e := os.Stat(filepath.Join(base, "partial.txt")); !os.IsNotExist(e) {
		t.Fatal("incomplete file published", e)
	}
	temps, _ := os.ReadDir(filepath.Join(base, thumbnailDirectory))
	if len(temps) != 0 {
		t.Fatal("incomplete upload leaked", temps)
	}
	if out := fsRequest(t, s, "GET", "resources", "/.FILEBEANS-THUMBNAILS/secret", nil, nil); out.Code != 400 {
		t.Fatal("case alias exposed", out.Code)
	}
}

type brokenFilesystemBody struct{}

func (brokenFilesystemBody) Read(p []byte) (int, error) {
	copy(p, "partial")
	return 7, io.ErrUnexpectedEOF
}

func TestFilesystemCacheExcludedFromMoveSnapshot(t *testing.T) {
	_, base := filesystemFixture(t)
	os.MkdirAll(filepath.Join(base, "folder", thumbnailDirectory), 0770)
	os.WriteFile(filepath.Join(base, "folder", "keep.txt"), []byte("keep"), 0660)
	os.WriteFile(filepath.Join(base, "folder", thumbnailDirectory, "cached.jpg"), []byte("disposable"), 0660)
	root, e := os.OpenRoot(base)
	if e != nil {
		t.Fatal(e)
	}
	defer root.Close()
	snapshot, e := snapshotMove(context.Background(), root, "folder")
	if e != nil {
		t.Fatal(e)
	}
	for _, entry := range snapshot {
		if strings.Contains(entry.Path, thumbnailDirectory) {
			t.Fatal("cache entered move manifest")
		}
	}
	if len(snapshot) != 2 {
		t.Fatal(snapshot)
	}
}

func TestFilesystemRejectsCacheAsProtectedLocation(t *testing.T) {
	b := testBroker(t)
	for _, name := range []string{thumbnailDirectory, ".FILEBEANS-THUMBNAILS"} {
		if _, e := b.reserve("disk", "/", name, "synthetic-cache-location-request"); e == nil {
			t.Fatal("reserved private cache as protected folder")
		}
	}
}

func TestFilesystemPicsUsesAdjacentThumbnailCache(t *testing.T) {
	s, base := filesystemFixture(t)
	for _, dir := range []string{"pics", "pics/nested", "elsewhere"} {
		if e := os.MkdirAll(filepath.Join(base, dir), 0770); e != nil {
			t.Fatal(e)
		}
		original := []byte("preview " + dir)
		path := filepath.Join(base, dir, "same.txt")
		if e := os.WriteFile(path, original, 0660); e != nil {
			t.Fatal(e)
		}
		first := fsRequest(t, s, "GET", "thumbnail", "/"+dir+"/same.txt", nil, nil)
		again := fsRequest(t, s, "GET", "thumbnail", "/"+dir+"/same.txt", nil, nil)
		if first.Code != 200 || again.Code != 200 || !bytes.Equal(first.Body.Bytes(), again.Body.Bytes()) {
			t.Fatal(dir, first.Code, again.Code)
		}
		entries, e := os.ReadDir(filepath.Join(base, dir, thumbnailDirectory))
		if e != nil || len(entries) != 1 {
			t.Fatal(dir, entries, e)
		}
		data, e := os.ReadFile(path)
		if e != nil || !bytes.Equal(data, original) {
			t.Fatal("original changed", e)
		}
	}
	if _, e := os.Stat(filepath.Join(s.config.State, "filesystem-thumbnails")); !os.IsNotExist(e) {
		t.Fatal("unexpected external cache", e)
	}
}

// Keep the production PR_SET_DUMPABLE policy isolated to a helper process so
// it cannot change the process policy of unrelated tests or their helpers.
func TestFilesystemRendererProtectedProcess(t *testing.T) {
	if os.Getenv("FILEBEANS_TEST_PROTECTED_RENDERER") != "1" {
		executable, e := os.Executable()
		if e != nil {
			t.Fatal(e)
		}
		child := exec.Command(executable, "-test.run=^TestFilesystemRendererProtectedProcess$", "-test.v")
		child.Env = append(os.Environ(), "FILEBEANS_TEST_PROTECTED_RENDERER=1")
		if output, e := child.CombinedOutput(); e != nil {
			t.Fatalf("protected renderer failed: %v\n%s", e, output)
		}
		return
	}
	if e := unix.Prctl(unix.PR_SET_DUMPABLE, 0, 0, 0, 0); e != nil {
		t.Fatal(e)
	}
	TestFilesystemRendererVideoAndPDF(t)
	dumpable, e := unix.PrctlRetInt(unix.PR_GET_DUMPABLE, 0, 0, 0, 0)
	if e != nil || dumpable != 0 {
		t.Fatal("renderer weakened process privacy", dumpable, e)
	}
}

func TestPinnedListingRootSurvivesParentReplacement(t *testing.T) {
	base := t.TempDir()
	folder := filepath.Join(base, "folder")
	outside := filepath.Join(base, "outside")
	if e := os.Mkdir(folder, 0700); e != nil {
		t.Fatal(e)
	}
	if e := os.Mkdir(outside, 0700); e != nil {
		t.Fatal(e)
	}
	os.WriteFile(filepath.Join(folder, "entry.txt"), []byte("yes"), 0600)
	os.WriteFile(filepath.Join(outside, "entry.txt"), []byte("outside-secret"), 0600)
	dir, e := os.Open(folder)
	if e != nil {
		t.Fatal(e)
	}
	defer dir.Close()
	root, e := pinnedListingRoot(dir)
	if e != nil {
		t.Fatal(e)
	}
	defer root.Close()
	if e = os.Rename(folder, folder+"-moved"); e != nil {
		t.Fatal(e)
	}
	if e = os.Symlink(outside, folder); e != nil {
		t.Fatal(e)
	}
	info, e := root.Lstat("entry.txt")
	if e != nil || info.Size() != 3 {
		t.Fatalf("parent replacement redirected metadata: %v %v", info, e)
	}
	if _, e = root.Open("../outside/entry.txt"); e == nil {
		t.Fatal("pinned root escaped")
	}
	if e = os.Symlink(filepath.Join(outside, "entry.txt"), filepath.Join(folder+"-moved", "link")); e != nil {
		t.Fatal(e)
	}
	info, e = root.Lstat("link")
	if e != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatal("symlink metadata followed", e)
	}
	if _, e = root.Open("link"); e == nil {
		t.Fatal("symlink escaped pinned directory")
	}
}
