package web

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"filebeans/companion/protection"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gtsteffaniak/filebrowser/backend/internal/adapters/fs/files"
	"github.com/gtsteffaniak/filebrowser/backend/internal/adapters/fs/fileutils"
	dbsql "github.com/gtsteffaniak/filebrowser/backend/internal/database/sql"
	"github.com/gtsteffaniak/filebrowser/backend/internal/database/users"
	"github.com/gtsteffaniak/filebrowser/backend/internal/ffmpeg"
	"github.com/gtsteffaniak/filebrowser/backend/internal/preview"
	"github.com/gtsteffaniak/filebrowser/backend/internal/utils"
	"github.com/gtsteffaniak/filebrowser/backend/pkg/indexing"
	"github.com/gtsteffaniak/filebrowser/backend/pkg/indexing/iteminfo"
	"github.com/gtsteffaniak/filebrowser/backend/pkg/settings"
	"github.com/gtsteffaniak/filebrowser/backend/pkg/vaultbridge"
)

func TestVaultFilesystemIntegration(t *testing.T) {
	root := os.Getenv("FILEBEANS_VAULT_TEST_ROOT")
	if root == "" {
		t.Skip("synthetic broker orchestrator required")
	}
	if os.Geteuid() == 0 {
		t.Fatal("Quantum integration must run unprivileged")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", filepath.Join(os.Getenv("FILEBEANS_VAULT_RUNTIME"), "broker.sock"))
	}}
	client := &http.Client{Transport: transport, Timeout: 20 * time.Second}
	defer transport.CloseIdleConnections()
	fixture := func(action string, want int) []byte {
		t.Helper()
		resp, e := client.Post("http://broker/fixture/"+action, "application/json", nil)
		if e != nil {
			t.Fatal(e)
		}
		defer resp.Body.Close()
		data, e := io.ReadAll(resp.Body)
		if e != nil || resp.StatusCode != want {
			t.Fatalf("fixture %s: status=%d body=%s error=%v", action, resp.StatusCode, data, e)
		}
		return data
	}
	settings.Config = settings.SetDefaults(false)
	fileutils.SetFsPermissions(0660, 0770)
	if e := vaultbridge.ValidateCreationModes(fileutils.PermFile, fileutils.PermDir); e != nil {
		t.Fatal(e)
	}
	cache := t.TempDir()
	settings.Config.Server.CacheDir = cache
	source := &settings.Source{Name: "uploads", Path: root}
	source.Config.ResolvedRules.NoRules = true
	source.Config.ResolvedRules.IndexingDisabled = true
	settings.Config.Server.SourceMap = map[string]*settings.Source{root: source}
	settings.Config.Server.NameToSource = map[string]*settings.Source{"uploads": source}
	users.SetSourceNameResolver(func(name string) (string, error) {
		if name == "uploads" || name == root {
			return root, nil
		}
		return "", fmt.Errorf("unknown source")
	})
	users.SetSourceConfig(&users.SourceConfigProvider{
		GetSourceByPath: func(path string) (users.SourceInfo, bool) {
			return users.SourceInfo{Name: "uploads", Path: root}, path == root
		},
		GetSourceByName: func(name string) (users.SourceInfo, bool) {
			return users.SourceInfo{Name: "uploads", Path: root}, name == "uploads"
		},
	})
	db, _, e := dbsql.NewIndexDB("vault-test", "OFF", 100, 8, false)
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	indexing.SetIndexDBForTesting(db)
	indexing.Initialize(source, false, true)
	defer indexing.ClearTestIndices()
	idx := indexing.GetIndex("uploads")
	idx.Config.ResolvedRules.IndexingDisabled = false
	perms := users.SourceFilePermissions{View: true, Download: true, Create: true, Modify: true, Delete: true}
	user := &users.User{ID: 1, FrontendUser: users.FrontendUser{Username: "vault-user"}, BackendScopes: []users.BackendScope{{Path: root, Scope: "/", Permissions: perms}}, BackendSourcePermissions: map[string]users.SourceFilePermissions{root: perms}, Version: users.SourcePermissionsMigrationVersion}
	if e = vaultbridge.Start(ctx); e != nil {
		t.Fatal(e)
	}
	var vault protection.Vault
	if e = json.Unmarshal(fixture("create", 201), &vault); e != nil || vault.State != "unlocked" {
		t.Fatal("creation did not leave vault open", e)
	}
	if resp, e := client.Get("http://broker/v1/metadata-key"); e != nil {
		t.Fatal(e)
	} else {
		resp.Body.Close()
		if resp.StatusCode != 403 {
			t.Fatal("Quantum received derived key")
		}
	}
	request := func(method, path string, body io.Reader, headers map[string]string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, path, body)
		for k, v := range headers {
			r.Header.Set(k, v)
		}
		w := httptest.NewRecorder()
		h := vaultbridge.HTTP(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			d := &Context{User: user, Ctx: r.Context()}
			var status int
			var err error
			if r.Method == "PATCH" {
				status, err = ResourcePatchHandler(w, r, d)
			} else if r.Method == "POST" {
				status, err = ResourcePostHandler(w, r, d)
			} else {
				status, err = resourceGetHandler(w, r, d)
			}
			if err != nil {
				http.Error(w, err.Error(), status)
				return
			}
			if status > 0 {
				w.WriteHeader(status)
			}
		}))
		h.ServeHTTP(w, r)
		return w
	}
	listing := request("GET", "/api/resources?source=uploads&path=/", nil, nil)
	if listing.Code != 200 || !bytes.Contains(listing.Body.Bytes(), []byte("Private")) || bytes.Contains(listing.Body.Bytes(), []byte(".filebeans-vaults")) {
		t.Fatalf("parent listing: %d %s", listing.Code, listing.Body.String())
	}
	upload := request("POST", "/api/resources?source=uploads&path=/Private/secret.txt", strings.NewReader("private text"), map[string]string{"X-File-Upload-Session": "vault-upload"})
	if upload.Code >= 300 {
		t.Fatalf("upload: %d %s", upload.Code, upload.Body.String())
	}

	conflict := request("PATCH", "/api/resources", strings.NewReader(`{"action":"move","items":[{"fromSource":"uploads","fromPath":"/Private/secret.txt","toSource":"uploads","toPath":"/export.txt"}]}`), nil)
	if conflict.Code != http.StatusConflict {
		t.Fatalf("boundary move must be conflict: %d %s", conflict.Code, conflict.Body.String())
	}
	if _, err := os.Stat(filepath.Join(root, "Private", "secret.txt")); err != nil {
		t.Fatal("rejected move removed source", err)
	}
	if e := os.Mkdir(filepath.Join(root, "Private", "shared"), 0770); e != nil {
		t.Fatal(e)
	}
	if e := os.Mkdir(filepath.Join(root, "Private", "notshared"), 0700); e != nil {
		t.Fatal(e)
	}
	// Existing trash roots belong to Quantum, while deletes run as companion.
	for _, dir := range []string{"Trash", "Private/Trash"} {
		if e := os.MkdirAll(filepath.Join(root, dir), 0770); e != nil {
			t.Fatal(e)
		}
	}

	for _, prefix := range []string{"", "Private"} {
		if e := os.WriteFile(filepath.Join(root, prefix, "shared-delete-file"), []byte("delete payload"), 0660); e != nil {
			t.Fatal(e)
		}
		if e := os.MkdirAll(filepath.Join(root, prefix, "shared-delete-folder", "empty"), 0770); e != nil {
			t.Fatal(e)
		}
		if e := os.WriteFile(filepath.Join(root, prefix, "shared-delete-folder", "payload"), []byte("delete payload"), 0660); e != nil {
			t.Fatal(e)
		}
	}
	fixture("shared-writer", 200)
	if data, e := os.ReadFile(filepath.Join(root, "Private", "shared", "restored.txt")); e != nil || string(data) != "shared writer" {
		t.Fatal("Quantum cannot read companion output", e)
	}
	file, e := files.FileInfoFaster(utils.FileOptions{Source: "uploads", Path: "/Private/secret.txt", Expand: false, SkipExtendedAttrs: true}, user)
	if e != nil {
		t.Fatal("protected file info", e)
	}
	rangeReq := httptest.NewRequest("GET", "/", nil)
	rangeReq.Header.Set("Range", "bytes=0-6")
	rangeW := httptest.NewRecorder()
	vaultbridge.HTTP(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, e = rawFileHandler(w, r, *file) })).ServeHTTP(rangeW, rangeReq)
	if e != nil || rangeW.Code != 206 || rangeW.Body.String() != "private" {
		t.Fatal("range read failed", rangeW.Code, e)
	}
	imagePath := filepath.Join(root, "Private", "secret.jpg")
	f, e := os.Create(imagePath)
	if e != nil {
		t.Fatal(e)
	}
	img := image.NewRGBA(image.Rect(0, 0, 512, 512))
	for y := 0; y < 512; y++ {
		for x := 0; x < 512; x++ {
			img.Set(x, y, color.RGBA{uint8(x), uint8(y), uint8(x * y), 255})
		}
	}
	if e = jpeg.Encode(f, img, &jpeg.Options{Quality: 90}); e != nil {
		f.Close()
		t.Fatal(e)
	}
	f.Close()
	photo, e := files.FileInfoFaster(utils.FileOptions{Source: "uploads", Path: "/Private/secret.jpg", SkipExtendedAttrs: true}, user)
	if e != nil {
		t.Fatal(e)
	}
	ffmpegPath, e := exec.LookPath("ffmpeg")
	if e != nil {
		t.Fatal("video preview fixture requires ffmpeg", e)
	}
	if e = ffmpeg.Initialize(ctx, ffmpeg.InitOptions{FFmpegPath: ffmpegPath, MaxConcurrent: 1, CacheDir: cache, SkipHWTests: true}); e != nil {
		t.Fatal(e)
	}
	settings.Env.FFmpegAvailable = true
	yes := true
	settings.Config.Integrations.Media.Convert.VideoPreview = map[settings.VideoPreviewType]*bool{settings.MP4VideoPreview: &yes}
	if e = preview.StartPreviewGenerator(1, cache); e != nil {
		t.Fatal(e)
	}
	thumb, e := preview.GetPreviewForFile(ctx, *photo, "small", "", 0)
	if e != nil || len(thumb) < 100 {
		t.Fatal("preview generation", e)
	}
	videoPath := filepath.Join(root, "Private", "private-video.mp4")
	videoCmd := exec.CommandContext(ctx, ffmpegPath, "-nostdin", "-hide_banner", "-loglevel", "error", "-f", "lavfi", "-i", "color=c=blue:s=256x256:r=10", "-t", "2", "-c:v", "mpeg4", "-y", videoPath)
	if output, e := videoCmd.CombinedOutput(); e != nil {
		t.Fatal("video fixture", e, string(output))
	}
	video, e := files.FileInfoFaster(utils.FileOptions{Source: "uploads", Path: "/Private/private-video.mp4", SkipExtendedAttrs: true}, user)
	if e != nil {
		t.Fatal(e)
	}
	video.HasPreview = true
	videoThumb, e := preview.GetPreviewForFile(ctx, *video, "small", "", 50)
	if e != nil || len(videoThumb) < 100 {
		t.Fatal("protected video preview", e)
	}
	contents, e := files.FileInfoFaster(utils.FileOptions{Source: "uploads", Path: "/Private", Expand: true}, user)
	if e != nil {
		t.Fatal("protected folder listing", e)
	}
	for _, name := range []string{"secret.jpg", "private-video.mp4"} {
		found := false
		for _, child := range contents.Files {
			if child.Name == name && child.HasPreview {
				found = true
			}
		}
		if !found {
			t.Fatal("protected directory did not advertise preview", name)
		}
	}
	private, e := vaultbridge.CacheDirectory(imagePath)
	if e != nil || !strings.Contains(private, filepath.Join(vault.ID, "private")) {
		t.Fatal("wrong preview storage", private, e)
	}
	if idx.ShouldSkip(false, "/Private/secret.jpg", false, false, true) == false {
		t.Fatal("protected file is indexable")
	}
	if e = db.BulkInsertItems("uploads", []*iteminfo.FileInfo{{Path: "/Private/secret.jpg", ItemInfo: iteminfo.ItemInfo{Name: "secret.jpg", Type: "image/jpeg"}}, {Path: "/ordinary.txt", ItemInfo: iteminfo.ItemInfo{Name: "ordinary.txt", Type: "text/plain"}}}); e != nil {
		t.Fatal(e)
	}
	var leaked int
	if e = db.QueryRow("SELECT count(*) FROM index_items WHERE path LIKE '%secret%' OR name LIKE '%secret%'").Scan(&leaked); e != nil || leaked != 0 {
		t.Fatal("protected names persisted in ordinary index", leaked, e)
	}
	archive := func(method, selected, token string) *httptest.ResponseRecorder {
		t.Helper()
		w := httptest.NewRecorder()
		r := httptest.NewRequest(method, "/api/raw?algo=zip&archiveToken="+token, nil)
		vaultbridge.HTTP(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			status, err := BuildAndStreamArchive(w, r, &Context{User: user, Ctx: r.Context()}, "uploads", []string{selected})
			if err != nil {
				http.Error(w, err.Error(), status)
			} else if status > 0 {
				w.WriteHeader(status)
			}
		})).ServeHTTP(w, r)
		return w
	}
	spooled := archive("HEAD", "/Private/secret.txt", "")
	token := spooled.Header().Get("X-Archive-Token")
	if spooled.Code != 200 || token == "" {
		t.Fatal("protected archive spool", spooled.Code, spooled.Body.String())
	}
	if path, ok := archiveSpoolPathCache.Get(token); !ok || !strings.HasPrefix(path, filepath.Join(filepath.Dir(private), "tmp", "archives")+string(os.PathSeparator)) {
		t.Fatal("archive spool outside encrypted private storage", path)
	}
	fixture("lock", 200)
	if got := archive("GET", "/Private/secret.txt", token); got.Code < 400 {
		t.Fatal("archive token bypassed lock", got.Code)
	}
	if e = os.WriteFile(filepath.Join(root, "ordinary.txt"), []byte("ordinary"), 0660); e != nil {
		t.Fatal(e)
	}
	if got := archive("GET", "/ordinary.txt", token); got.Code != 410 {
		t.Fatal("archive token accepted a changed selection", got.Code)
	}
	if _, e = preview.GetPreviewForFile(ctx, *video, "small", "", 50); e == nil {
		t.Fatal("video preview bypassed lock", e)
	}
	if _, e = preview.GetPreviewForFile(ctx, *photo, "small", "", 0); e == nil {
		t.Fatal("cached preview bypassed lock", e)
	}
	if _, e = files.FileInfoFaster(utils.FileOptions{Source: "uploads", Path: "/Private/secret.txt"}, user); e == nil {
		t.Fatal("file metadata bypassed lock", e)
	}
	if _, e = os.Stat(private); !errors.Is(e, os.ErrNotExist) && !errors.Is(e, os.ErrPermission) {
		t.Fatal("private cache readable after lock", e)
	}
	listing = request("GET", "/api/resources?source=uploads&path=/", nil, nil)
	if listing.Code != 200 || bytes.Contains(listing.Body.Bytes(), []byte("Private")) {
		t.Fatal("locked folder remains visible", listing.Body.String())
	}
	fixture("unlock", 200)
	if got := archive("GET", "/Private/secret.txt", token); got.Code != 200 || !bytes.Contains(got.Body.Bytes(), []byte("secret.txt")) {
		t.Fatal("archive resume after unlock", got.Code)
	}
	if cached, e := preview.GetPreviewForFile(ctx, *video, "small", "", 50); e != nil || !bytes.Equal(cached, videoThumb) {
		t.Fatal("video cache did not survive lock/unlock", e)
	}
	if cached, e := preview.GetPreviewForFile(ctx, *photo, "small", "", 0); e != nil || !bytes.Equal(cached, thumb) {
		t.Fatal("protected preview did not survive lock/unlock", e)
	}

	t.Run("idle-event-stream-does-not-block-locking", func(t *testing.T) {
		entered := make(chan struct{})
		release := make(chan struct{})
		finished := make(chan error, 1)
		response := httptest.NewRecorder()
		go func() {
			vaultbridge.HTTP(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				close(entered)
				<-release
				_, err := w.Write([]byte("must not deliver stale protected content"))
				finished <- err
			})).ServeHTTP(response, httptest.NewRequest("GET", "/api/events", nil))
		}()
		<-entered
		fixture("lock", 200)
		close(release)
		if err := <-finished; !errors.Is(err, protection.ErrLocked) {
			t.Fatal("stale event delivered after lock", err)
		}
		if response.Body.Len() != 0 {
			t.Fatal("stale protected payload emitted")
		}
		fixture("unlock", 200)
	})
	lease, e := vaultbridge.Read(imagePath, false)
	if e != nil {
		t.Fatal(e)
	}
	fixture("lock", 409)
	lease.Close()
	fixture("lock", 200)
	if e = filepath.Walk(cache, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if bytes.Contains(data, thumb) || bytes.Contains(data, videoThumb) || bytes.Contains(data, []byte("secret.jpg")) || bytes.Contains(data, []byte("private-video.mp4")) {
			t.Fatal("preview or filename leaked into ordinary cache", path)
		}
		return nil
	}); e != nil {
		t.Fatal(e)
	}
}

func TestProtectedErrorsAreConflicts(t *testing.T) {
	for _, err := range []error{protection.ErrBoundary, protection.ErrLocked, protection.ErrBusy, protection.ErrHidden} {
		if got := ErrToStatus(fmt.Errorf("operation: %w", err)); got != http.StatusConflict {
			t.Fatal(got, err)
		}
	}
}
