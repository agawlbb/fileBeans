package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"filebeans/companion/protection"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFilesystemProtectedLifecycle(t *testing.T)    { testFilesystemProtectedLifecycle(t, false) }
func TestFilesystemProtectedSMBLifecycle(t *testing.T) { testFilesystemProtectedLifecycle(t, true) }
func testFilesystemProtectedLifecycle(t *testing.T, smb bool) {
	if os.Getenv("FILEBEANS_TEST_FUSE") != "1" {
		t.Skip("isolated FUSE runtime required")
	}
	b := testBroker(t)
	physical := b.config.Roots[0].Path
	if smb {
		physical = mountSyntheticSMB(t, b)
	}
	b.config.Gocryptfs = os.Getenv("FILEBEANS_TEST_GOCRYPTFS")
	b.config.Fusermount = "/test-tools/fusermount3"
	b.manager.memoryCheck = func() error { return nil }
	key := bytes.Repeat([]byte{0x42}, 32)
	entry, e := b.reserve("disk", "/", "Private", "synthetic-filesystem-integration")
	if e != nil {
		t.Fatal(e)
	}
	if e = b.open(entry, key); e != nil {
		t.Fatal(e)
	}
	guard, e := protection.New(b.config.Runtime, map[string]string{"disk": b.config.Roots[0].Path})
	if e != nil {
		t.Fatal(e)
	}
	t.Run("move-plan-protection-boundaries", func(t *testing.T) {
		lease, err := guard.Acquire()
		if err != nil {
			t.Fatal(err)
		}
		raw, err := os.OpenRoot(b.config.Roots[0].Path)
		if err != nil {
			lease.Close()
			t.Fatal(err)
		}
		root := &guardedRoot{Root: raw, lease: lease}
		defer root.Close()
		if err = root.WriteFile("ordinary-move", []byte("content"), 0660); err != nil {
			t.Fatal(err)
		}
		defer root.Remove("ordinary-move")
		m := MoveSelection{}
		if err = planRename(root, "ordinary-move", root, "Private/moved", &m); err != nil || m.Rename {
			t.Fatal("protected boundary must copy", err)
		}
		if err = root.WriteFile("Private/internal-move", []byte("content"), 0660); err != nil {
			t.Fatal(err)
		}
		defer root.Remove("Private/internal-move")
		if err = planRename(root, "Private/internal-move", root, "Private/internal-renamed", &m); err != nil || !m.Rename {
			t.Fatal("same vault should rename", err)
		}
		if err = planRename(root, "Private", root, "moved-vault", &MoveSelection{}); err == nil {
			t.Fatal("vault root move admitted")
		}
	})
	s := &Service{config: Config{Token: strings.Repeat("t", 32), State: t.TempDir(), Roots: b.config.Roots, Filesystem: FilesystemConfig{Enabled: true, FFmpeg: "/test-tools/ffmpeg", PDFToPPM: "/test-tools/pdftoppm"}}, vaults: &vaultManager{guard: guard, remote: newBrokerClient(b.config.Runtime, b.config.Roots)}}
	call := func(method, route, p string, payload io.Reader, headers map[string]string) *httptest.ResponseRecorder {
		q := url.Values{"source": {"disk"}, "path": {p}}
		req := httptest.NewRequest(method, "/v1/fs/"+route+"?"+q.Encode(), payload)
		req.Header.Set("Authorization", "Bearer "+s.config.Token)
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		out := httptest.NewRecorder()
		s.Handler().ServeHTTP(out, req)
		return out
	}
	if out := call("POST", "content", "/Private/secret.txt", strings.NewReader("secret-content"), nil); out.Code != 201 {
		t.Fatal(out.Code, out.Body.String())
	}
	out := call("GET", "thumbnail", "/Private/secret.txt", nil, nil)
	if out.Code != 200 {
		t.Fatal(out.Code, out.Body.String())
	}
	expected := append([]byte{}, out.Body.Bytes()...)
	cache := filepath.Join(b.publicPath(entry), thumbnailDirectory)
	if files, e := os.ReadDir(cache); e != nil || len(files) != 1 {
		t.Fatal("encrypted cache missing", files, e)
	}
	media, err := os.ReadFile("/test-tools/range-probe.mp4")
	if err != nil {
		t.Fatal(err)
	}
	out = call("POST", "content", "/Private/movie.mp4", bytes.NewReader(media), nil)
	if out.Code != 201 {
		t.Fatal("protected video upload", out.Code, out.Body.String())
	}
	out = call("GET", "content", "/Private/movie.mp4", nil, map[string]string{"Range": "bytes=16-127"})
	if out.Code != 206 || !bytes.Equal(out.Body.Bytes(), media[16:128]) {
		t.Fatal("protected ranged playback", out.Code)
	}
	out = call("GET", "thumbnail", "/Private/movie.mp4", nil, nil)
	if out.Code != 200 {
		t.Fatal("protected video render", out.Code, out.Body.String())
	}
	if _, err := os.Stat(filepath.Join(s.config.State, "filesystem-thumbnails")); !os.IsNotExist(err) {
		t.Fatal("protected thumbnail spilled into private plaintext state", err)
	}
	// Dataset preparation uses the same encrypted boundary and pinned input.
	s.config.Filesystem.DatasetMinFreeBytes = 1 << 20
	if smb {
		s.datasetDiskCheck = func(*os.File) error { return nil }
	} // The synthetic SMB server does not implement free-space reporting.
	s.config.Normalization.FFprobe = "/test-tools/ffprobe"
	s.vaults.remote.client.Transport = roundTrip(func(req *http.Request) (*http.Response, error) {
		req = req.WithContext(context.WithValue(req.Context(), brokerPeer{}, b.config.ControlUID))
		w := httptest.NewRecorder()
		b.handler().ServeHTTP(w, req)
		return w.Result(), nil
	})
	st, err := os.Stat(filepath.Join(b.publicPath(entry), "movie.mp4"))
	if err != nil {
		t.Fatal(err)
	}
	input := DatasetInput{ID: ID(), Source: "disk", Path: "/Private/movie.mp4", Revision: filesystemRevision(st)}
	input.Vault = entry.ID
	metadata := call("GET", "resources", "/Private/movie.mp4", nil, nil)
	var originalEntry filesystemEntry
	if metadata.Code != 200 || json.Unmarshal(metadata.Body.Bytes(), &originalEntry) != nil || originalEntry.Vault != entry.ID {
		t.Fatal("missing original protection identity", metadata.Code, metadata.Body.String())
	}
	if w := datasetReadRequest(s, input, "info", "GET"); w.Code != 200 {
		t.Fatal("protected original info", w.Code, w.Body.String())
	}
	if w := datasetReadRequest(s, input, "content", "GET"); w.Code != 200 {
		t.Fatal("protected original stream", w.Code, w.Body.String())
	}
	if err = s.datasetActivity(Item{Source: input.Source, Path: input.Path}); err != nil {
		t.Fatal(err)
	}
	if b.datasetActivity.IsZero() {
		t.Fatal("dataset processing did not refresh inactivity")
	}
	// A blocked response must keep its broker lease, including cache hits.
	writer := &filesystemBlockingWriter{ResponseRecorder: httptest.NewRecorder(), started: make(chan struct{}), release: make(chan struct{})}
	req := httptest.NewRequest("GET", "/v1/fs/thumbnail?source=disk&path=/Private/secret.txt", nil)
	req.Header.Set("Authorization", "Bearer "+s.config.Token)
	done := make(chan struct{})
	go func() { defer close(done); s.Handler().ServeHTTP(writer, req) }()
	<-writer.started
	gate, err := b.exclusive()
	if gate != nil {
		gate.Close()
	}
	if !errors.Is(err, ErrVaultBusy) {
		close(writer.release)
		<-done
		t.Fatal("response lost lease", err)
	}
	close(writer.release)
	<-done
	if e = b.lock(entry); e != nil {
		t.Fatal(e)
	}
	for _, route := range []string{"resources", "content", "thumbnail"} {
		if out := call("GET", route, "/Private/secret.txt", nil, nil); out.Code != 409 && out.Code != 404 {
			t.Fatal("locked content exposed", route, out.Code)
		}
	}
	if root, file, _, err := s.datasetInput(input); err == nil {
		file.Close()
		root.Close()
		t.Fatal("locked dataset source exposed")
	}
	for _, endpoint := range []string{"info", "content"} {
		if out := datasetReadRequest(s, input, endpoint, "GET"); out.Code != 423 || !strings.Contains(out.Body.String(), "FileBeans mobile app") {
			t.Fatal("locked original", endpoint, out.Code, out.Body.String())
		}
	}
	// The cache and file contents must not occur in ciphertext or broker state.
	for _, base := range []string{filepath.Join(physical, brokerStore), b.config.State} {
		e = filepath.WalkDir(base, func(p string, d os.DirEntry, e error) error {
			if e != nil {
				return e
			}
			if d.IsDir() {
				return nil
			}
			data, e := os.ReadFile(p)
			if e != nil {
				return e
			}
			if bytes.Contains(data, []byte("secret-content")) || bytes.Contains(data, expected) {
				t.Errorf("plaintext persisted: %s", p)
			}
			return nil
		})
		if e != nil {
			t.Fatal(e)
		}
	}
	if e = b.open(entry, key); e != nil {
		t.Fatal(e)
	}
	out = call("GET", "thumbnail", "/Private/secret.txt", nil, nil)
	if out.Code != 200 || !bytes.Equal(out.Body.Bytes(), expected) {
		t.Fatal("encrypted cache did not survive relock", out.Code)
	}
	// A direct move across the encryption boundary must use the durable job API.
	out = call("POST", "move", "/", strings.NewReader(`{"source":"disk","from":"/Private/secret.txt","to":"/escaped.txt"}`), nil)
	if out.Code == 204 {
		t.Fatal("cross-boundary move bypassed jobs")
	}
	// Missing registry must deny even a cached thumbnail.
	snapshot := filepath.Join(b.config.Runtime, "status.json")
	saved := snapshot + ".saved"
	if e = os.Rename(snapshot, saved); e != nil {
		t.Fatal(e)
	}
	out = call("GET", "thumbnail", "/Private/secret.txt", nil, nil)
	if e = os.Rename(saved, snapshot); e != nil {
		t.Fatal(e)
	}
	if out.Code == 200 {
		t.Fatal("cached content served without broker registry")
	}
	if e = b.lock(entry); e != nil {
		t.Fatal(e)
	}
}

type filesystemBlockingWriter struct {
	*httptest.ResponseRecorder
	started, release chan struct{}
}

func (w *filesystemBlockingWriter) Write(p []byte) (int, error) {
	close(w.started)
	<-w.release
	return w.ResponseRecorder.Write(p)
}
