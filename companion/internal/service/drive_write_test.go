package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
)

func fixtureGrant(t *testing.T, s *Service, full bool) {
	t.Helper()
	grant := Google{Access: "fixture", Refresh: "fixture-refresh", Expires: time.Now().Add(time.Hour).Unix(), Connection: "fixture-account", Account: "opaque-account"}
	if full {
		grant.Scopes = []string{driveScope}
	}
	if e := privateJSON(filepath.Join(s.config.State, "google.json"), grant); e != nil {
		t.Fatal(e)
	}
}
func jsonResponse(status int, value any) *http.Response {
	b, _ := json.Marshal(value)
	return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(b))}
}
func TestDriveConsentUpgrade(t *testing.T) {
	s := &Service{config: Config{State: t.TempDir(), GoogleClientID: "fixture", GoogleClientSecret: "fixture", GoogleRedirect: "https://example.test/oauth/callback"}}
	fixtureGrant(t, s, false)
	previous := googleHTTP
	defer func() { googleHTTP = previous }()
	account := "opaque-account"
	scope := driveScope
	googleHTTP = &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host == "oauth2.googleapis.com" {
			return jsonResponse(200, map[string]any{"access_token": "upgraded", "refresh_token": "new-fixture", "expires_in": 3600, "scope": scope}), nil
		}
		if r.Header.Get("Authorization") == "Bearer fixture" {
			return jsonResponse(200, map[string]any{"user": map[string]string{"permissionId": "opaque-account"}}), nil
		}
		return jsonResponse(200, map[string]any{"user": map[string]string{"permissionId": account}}), nil
	})}
	consent := func() error {
		address, e := s.authURL()
		if e != nil {
			return e
		}
		u, _ := url.Parse(address)
		if u.Query().Get("scope") != driveScope {
			t.Fatal("full scope not requested")
		}
		return s.callback(context.Background(), u.Query().Get("state"), "fixture-code")
	}
	if e := s.requireDriveWrite(""); !errors.Is(e, ErrDriveWrite) {
		t.Fatal(e)
	}
	scope = "https://www.googleapis.com/auth/drive.readonly"
	if e := consent(); !errors.Is(e, ErrDriveWrite) {
		t.Fatal("incomplete consent accepted", e)
	}
	old, _ := s.google()
	if old.Access != "fixture" {
		t.Fatal("old grant lost")
	}
	scope = driveScope
	if e := consent(); e != nil {
		t.Fatal(e)
	}
	grant, _ := s.google()
	if !grant.writable() || grant.Connection != "fixture-account" {
		t.Fatal("same-account jobs invalidated")
	}
	account = "another-account"
	if e := consent(); e != nil {
		t.Fatal(e)
	}
	grant, _ = s.google()
	if grant.Connection == "fixture-account" {
		t.Fatal("account switch not isolated")
	}
}

func TestDriveEmptyPages(t *testing.T) {
	s := &Service{config: Config{State: t.TempDir(), Token: strings.Repeat("t", 32)}}
	fixtureGrant(t, s, false)
	previous := googleHTTP
	defer func() { googleHTTP = previous }()
	googleHTTP = &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) { return jsonResponse(200, map[string]any{}), nil })}
	for route, field := range map[string]string{"/v1/drive/files?folder=root": "files", "/v1/drive/trash": "files", "/v1/drive/drives": "drives"} {
		r := httptest.NewRequest("GET", route, nil)
		r.Header.Set("Authorization", "Bearer "+s.config.Token)
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, r)
		var body map[string]json.RawMessage
		json.Unmarshal(w.Body.Bytes(), &body)
		if w.Code != 200 || string(body[field]) != "[]" {
			t.Fatal("invalid empty page", route, w.Code, w.Body.String())
		}
	}
}

type cloudFixture struct {
	t            *testing.T
	items        map[string]DriveItem
	payloads     map[string][]byte
	serial       int
	upload       DriveItem
	received     []byte
	lostChunk    bool
	loseNative   bool
	copied       int
	patches      int
	trashQueries int
}

func newCloudFixture(t *testing.T) *cloudFixture {
	f := &cloudFixture{t: t, items: map[string]DriveItem{}, payloads: map[string][]byte{}}
	f.items["root"] = DriveItem{ID: "root", Name: "My Drive", Mime: "application/vnd.google-apps.folder", Capabilities: allDrivePermissions()}
	return f
}
func allDrivePermissions() DriveCapabilities {
	return DriveCapabilities{Download: true, Copy: true, Rename: true, Edit: true, MoveWithin: true, MoveOut: true, AddChildren: true, Trash: true, Untrash: true, Delete: true, DeleteChildren: true}
}
func (f *cloudFixture) nextID() string { f.serial++; return fmt.Sprintf("generated-%d", f.serial) }
func (f *cloudFixture) add(id, name, mime string, trashed bool) {
	f.items[id] = DriveItem{ID: id, Name: name, Mime: mime, Version: "1", Size: "6", Parents: []string{"root"}, Trashed: trashed, Capabilities: allDrivePermissions()}
	f.payloads[id] = []byte("abcdef")
}
func (f *cloudFixture) serve(r *http.Request) (*http.Response, error) {
	if r.Header.Get("Authorization") != "Bearer fixture" {
		f.t.Fatal("Google request lost authorization")
	}
	path := strings.TrimPrefix(r.URL.Path, "/drive/v3/")
	q := r.URL.Query()
	if strings.HasPrefix(r.URL.Path, "/upload/") {
		if r.Method == "POST" {
			if e := json.NewDecoder(r.Body).Decode(&f.upload); e != nil {
				f.t.Fatal(e)
			}
			f.received = nil
			res := jsonResponse(200, nil)
			res.Header.Set("Location", "https://www.googleapis.com/upload/drive/v3/files?upload_id=fixture")
			return res, nil
		}
		cr := r.Header.Get("Content-Range")
		if !strings.HasPrefix(cr, "bytes */") {
			data, _ := io.ReadAll(r.Body)
			var start, end, total int
			fmt.Sscanf(cr, "bytes %d-%d/%d", &start, &end, &total)
			if start != len(f.received) || end-start+1 != len(data) {
				f.t.Fatal("wrong resumed offset")
			}
			f.received = append(f.received, data...)
			if len(f.received) == total {
				f.upload.Size = strconv.Itoa(total)
				f.upload.Capabilities = allDrivePermissions()
				f.items[f.upload.ID] = f.upload
				f.payloads[f.upload.ID] = append([]byte{}, f.received...)
			}
			if f.lostChunk {
				f.lostChunk = false
				return nil, io.ErrUnexpectedEOF
			}
		}
		if _, ok := f.items[f.upload.ID]; ok {
			return jsonResponse(200, f.upload), nil
		}
		res := jsonResponse(308, nil)
		if len(f.received) > 0 {
			res.Header.Set("Range", fmt.Sprintf("bytes=0-%d", len(f.received)-1))
		}
		return res, nil
	}
	if path == "files/generateIds" {
		return jsonResponse(200, map[string]any{"ids": []string{f.nextID()}}), nil
	}
	if path == "files" && r.Method == "GET" {
		query := q.Get("q")
		values := []DriveItem{}
		if strings.Contains(query, "trashed=true") {
			f.trashQueries++
		}
		parentPattern := regexp.MustCompile(`'([^']+)' in parents`)
		namePattern := regexp.MustCompile(`name='([^']*)'`)
		opPattern := regexp.MustCompile(`key='filebeansOperation' and value='([^']+)'`)
		for _, item := range f.items {
			if strings.Contains(query, "trashed=false") && item.Trashed {
				continue
			}
			if strings.Contains(query, "trashed=true") && !item.Trashed {
				continue
			}
			if m := parentPattern.FindStringSubmatch(query); m != nil && !contains(item.Parents, m[1]) {
				continue
			}
			if m := namePattern.FindStringSubmatch(query); m != nil && item.Name != m[1] {
				continue
			}
			if m := opPattern.FindStringSubmatch(query); m != nil && item.Properties["filebeansOperation"] != m[1] {
				continue
			}
			if strings.Contains(query, "filebeansStage") && item.Properties["filebeansStage"] == "true" {
				continue
			}
			if q.Get("driveId") != "" && item.DriveID != q.Get("driveId") {
				continue
			}
			if strings.Contains(query, "'me' in owners") && item.DriveID != "" {
				continue
			}
			values = append(values, item)
		}
		return jsonResponse(200, DriveList{Files: values}), nil
	}
	if path == "files" && r.Method == "POST" {
		var item DriveItem
		json.NewDecoder(r.Body).Decode(&item)
		if _, ok := f.items[item.ID]; ok {
			return jsonResponse(409, nil), nil
		}
		item.Version = "1"
		item.Capabilities = allDrivePermissions()
		if item.Mime != "application/vnd.google-apps.folder" {
			item.Size = "0"
		}
		f.items[item.ID] = item
		return jsonResponse(200, item), nil
	}
	id := strings.TrimPrefix(path, "files/")
	copying := strings.HasSuffix(id, "/copy")
	if copying {
		id = strings.TrimSuffix(id, "/copy")
	}
	item, ok := f.items[id]
	if !ok {
		return jsonResponse(404, nil), nil
	}
	if copying {
		var target DriveItem
		json.NewDecoder(r.Body).Decode(&target)
		if target.ID == "" {
			target.ID = f.nextID()
		}
		target.Mime = item.Mime
		target.Size = item.Size
		target.Version = "1"
		target.Capabilities = allDrivePermissions()
		f.items[target.ID] = target
		f.payloads[target.ID] = f.payloads[id]
		f.copied++
		if f.loseNative {
			f.loseNative = false
			return nil, io.ErrUnexpectedEOF
		}
		return jsonResponse(200, target), nil
	}
	if r.Method == "GET" && q.Get("alt") == "media" {
		data := f.payloads[id]
		if r.Header.Get("Range") != "" {
			var from, to int
			fmt.Sscanf(r.Header.Get("Range"), "bytes=%d-%d", &from, &to)
			if to == 0 {
				to = len(data) - 1
			}
			if to >= len(data) {
				to = len(data) - 1
			}
			res := &http.Response{StatusCode: 206, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(data[from : to+1]))}
			res.Header.Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", from, to, len(data)))
			res.Header.Set("Content-Length", strconv.Itoa(to-from+1))
			return res, nil
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(data))}, nil
	}
	if r.Method == "GET" {
		return jsonResponse(200, item), nil
	}
	if r.Method == "PATCH" {
		patch := map[string]json.RawMessage{}
		json.NewDecoder(r.Body).Decode(&patch)
		if p, ok := patch["name"]; ok {
			json.Unmarshal(p, &item.Name)
		}
		if p, ok := patch["trashed"]; ok {
			json.Unmarshal(p, &item.Trashed)
		}
		if q.Get("addParents") != "" {
			item.Parents = []string{q.Get("addParents")}
		}
		v, _ := strconv.Atoi(item.Version)
		item.Version = strconv.Itoa(v + 1)
		f.items[id] = item
		f.patches++
		return jsonResponse(200, item), nil
	}
	if r.Method == "DELETE" {
		for child, d := range f.items {
			if contains(d.Parents, id) {
				delete(f.items, child)
			}
		}
		delete(f.items, id)
		return jsonResponse(204, nil), nil
	}
	f.t.Fatalf("unexpected Google route %s %s", r.Method, r.URL.Path)
	return nil, nil
}
func testDriveWriteWorkflow(t *testing.T, s *Service) {
	fixtureGrant(t, s, true)
	previous := googleHTTP
	defer func() { googleHTTP = previous }()
	f := newCloudFixture(t)
	googleHTTP = &http.Client{Transport: roundTrip(f.serve)}
	ctx := context.Background()
	call := func(method, route, key string, data []byte, headers map[string]string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, route, bytes.NewReader(data))
		r.Header.Set("Authorization", "Bearer "+s.config.Token)
		if key != "" {
			r.Header.Set("Idempotency-Key", key)
		}
		for k, v := range headers {
			r.Header.Set(k, v)
		}
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, r)
		return w
	}
	submit := func(req CopyRequest) Job {
		t.Helper()
		data, _ := json.Marshal(req)
		w := call("POST", "/v1/jobs", ID(), data, nil)
		if w.Code != 202 {
			t.Fatal(w.Code, w.Body.String())
		}
		var j Job
		json.Unmarshal(w.Body.Bytes(), &j)
		return j
	}
	t.Run("read-only-write-denied", func(t *testing.T) {
		fixtureGrant(t, s, false)
		w := call("POST", "/v1/drive/mutations", ID(), []byte(`{"action":"trash","fileId":"root"}`), nil)
		if w.Code != 403 {
			t.Fatal(w.Code)
		}
		fixtureGrant(t, s, true)
	})
	t.Run("mutation-idempotency-conflict-and-trash", func(t *testing.T) {
		f.add("mutable", "photo.jpg", "image/jpeg", false)
		req := DriveMutation{Action: "rename", FileID: "mutable", Name: "renamed.jpg", Version: "1"}
		key := ID()
		item, e := s.driveMutation(ctx, req, key)
		if e != nil || item.Name != "renamed.jpg" {
			t.Fatal(e)
		}
		before := f.patches
		if _, e = s.driveMutation(ctx, req, key); e != nil || f.patches != before {
			t.Fatal("duplicate mutation", e)
		}
		req.Name = "other.jpg"
		if _, e = s.driveMutation(ctx, req, key); !errors.Is(e, ErrConflict) {
			t.Fatal("key reuse accepted", e)
		}
		if _, e = s.driveMutation(ctx, DriveMutation{Action: "trash", FileID: "mutable", Version: "1"}, ID()); !errors.Is(e, ErrConflict) {
			t.Fatal("stale version accepted", e)
		}
		if _, e = s.driveMutation(ctx, DriveMutation{Action: "trash", FileID: "mutable", Version: item.Version}, ID()); e != nil || !f.items["mutable"].Trashed {
			t.Fatal(e)
		}
		if _, e = s.driveMutation(ctx, DriveMutation{Action: "restore", FileID: "mutable"}, ID()); e != nil || f.items["mutable"].Trashed {
			t.Fatal(e)
		}
		f.add("collision", "existing.jpg", "image/jpeg", false)
		if _, e = s.driveMutation(ctx, DriveMutation{Action: "rename", FileID: "mutable", Name: "existing.jpg"}, ID()); !errors.Is(e, ErrConflict) {
			t.Fatal("overwrite accepted", e)
		}
		if _, e = s.driveMutation(ctx, DriveMutation{Action: "trash", FileID: "root"}, ID()); !errors.Is(e, ErrUnsafe) {
			t.Fatal("root deletion accepted", e)
		}
	})
	t.Run("empty-trash-snapshot-and-confirmation", func(t *testing.T) {
		f.add("external-trash", "external.jpg", "image/jpeg", true)
		w := call("POST", "/v1/drive/mutations", ID(), []byte(`{"action":"empty","scope":"root"}`), nil)
		if w.Code != 400 {
			t.Fatal("missing permanent confirmation", w.Code)
		}
		record := driveMutationRecord{ID: ID(), Connection: "fixture-account", State: "pending", Request: DriveMutation{Action: "empty", Scope: "root"}, SnapshotReady: true, Snapshot: []string{"external-trash"}}
		key := ID()
		if e := s.insert(record.ID, "driveMutation", record.State, key, record); e != nil {
			t.Fatal(e)
		}
		f.add("later-trash", "later.jpg", "image/jpeg", true)
		if _, e := s.driveMutation(ctx, record.Request, key); e != nil {
			t.Fatal(e)
		}
		if _, ok := f.items["external-trash"]; ok {
			t.Fatal("original trash retained")
		}
		if _, ok := f.items["later-trash"]; !ok {
			t.Fatal("new trash deleted")
		}
		empty := driveMutationRecord{ID: ID(), Connection: "fixture-account", State: "pending", Request: DriveMutation{Action: "empty", Scope: "root"}, SnapshotReady: true, Snapshot: []string{}}
		key = ID()
		s.insert(empty.ID, "driveMutation", empty.State, key, empty)
		s.driveMutation(ctx, empty.Request, key)
		if _, ok := f.items["later-trash"]; !ok {
			t.Fatal("empty snapshot broadened")
		}
		list, e := s.driveTrash(ctx, "root", "", "fixture-account")
		if e != nil || len(list.Files) != 1 {
			t.Fatal("external trash missing", e)
		}
	})
	t.Run("folders-moves-and-shared-trash-scope", func(t *testing.T) {
		folder, e := s.driveMutation(ctx, DriveMutation{Action: "folder", Parent: "root", Name: "Move target"}, ID())
		if e != nil {
			t.Fatal(e)
		}
		f.add("moving", "moving.jpg", "image/jpeg", false)
		if _, e = s.driveMutation(ctx, DriveMutation{Action: "move", FileID: "moving", Parent: folder.ID}, ID()); e != nil || !contains(f.items["moving"].Parents, folder.ID) {
			t.Fatal("move failed", e)
		}
		child, e := s.driveMutation(ctx, DriveMutation{Action: "folder", Parent: folder.ID, Name: "Child"}, ID())
		if e != nil {
			t.Fatal(e)
		}
		if _, e = s.driveMutation(ctx, DriveMutation{Action: "move", FileID: folder.ID, Parent: child.ID}, ID()); !errors.Is(e, ErrUnsafe) {
			t.Fatal("folder cycle accepted", e)
		}
		f.add("denied", "denied.jpg", "image/jpeg", false)
		item := f.items["denied"]
		item.Capabilities = DriveCapabilities{}
		f.items["denied"] = item
		if _, e = s.driveMutation(ctx, DriveMutation{Action: "trash", FileID: "denied"}, ID()); !errors.Is(e, ErrDrivePermission) {
			t.Fatal("permission ignored", e)
		}
		f.add("shared-trash", "shared.jpg", "image/jpeg", true)
		item = f.items["shared-trash"]
		item.DriveID = "team-drive"
		f.items[item.ID] = item
		list, e := s.driveTrash(ctx, "team-drive", "", "fixture-account")
		if e != nil || len(list.Files) != 1 || list.Files[0].ID != "shared-trash" {
			t.Fatal("shared trash scope broadened", e)
		}
		if _, e = s.driveMutation(ctx, DriveMutation{Action: "empty", Scope: "team-drive"}, ID()); e != nil {
			t.Fatal(e)
		}
		if _, ok := f.items["later-trash"]; !ok {
			t.Fatal("My Drive trash deleted by shared-drive empty")
		}
	})
	t.Run("local-drive-resume-after-lost-chunk", func(t *testing.T) {
		data := bytes.Repeat([]byte("upload"), 900000)
		os.WriteFile(filepath.Join(s.config.Roots[0].Path, "cloud-video.mp4"), data, 0600)
		j := submit(CopyRequest{Items: []Item{{Source: "vps", Path: "cloud-video.mp4"}}, Destination: Item{Source: "drive", Path: "root"}})
		f.lostChunk = true
		s.runJob(ctx, &j)
		if j.State != "waiting" {
			t.Fatal(j.State, j.Error)
		}
		var resumed Job
		s.read(j.ID, "copy", &resumed)
		s.runJob(ctx, &resumed)
		if resumed.State != "completed" || resumed.Done != int64(len(data)) {
			t.Fatal(resumed.State, resumed.Error)
		}
		if len(resumed.Manifest) != 1 || !bytes.Equal(f.payloads[resumed.Manifest[0].TargetID], data) {
			t.Fatal("resumable data mismatch")
		}
		// Crash between cleanup and final job save: retry must not recreate the stage ID.
		resumed.State = "running"
		s.save(resumed.ID, "copy", resumed.State, resumed)
		s.runJob(ctx, &resumed)
		if resumed.State != "completed" {
			t.Fatal("post-cleanup restart", resumed.Error)
		}
	})
	t.Run("cancellation-preserves-external-staging-contents", func(t *testing.T) {
		os.WriteFile(filepath.Join(s.config.Roots[0].Path, "cleanup-check.dat"), []byte("fixture"), 0600)
		j := submit(CopyRequest{Items: []Item{{Source: "vps", Path: "cleanup-check.dat"}}, Destination: Item{Source: "drive", Path: "root"}})
		f.lostChunk = true
		s.runJob(ctx, &j)
		if j.State != "waiting" {
			t.Fatal(j.State, j.Error)
		}
		f.add("external-in-stage", "external.dat", "application/octet-stream", true)
		item := f.items["external-in-stage"]
		item.Parents = []string{j.RemoteStage}
		f.items[item.ID] = item
		j.Cancel = true
		s.save(j.ID, "copy", j.State, j)
		s.runJob(ctx, &j)
		if j.State != "waiting" {
			t.Fatal("cleanup ignored external trashed child", j.State)
		}
		if _, ok := f.items[item.ID]; !ok {
			t.Fatal("external data deleted")
		}
		item.Parents = []string{"root"}
		f.items[item.ID] = item
		s.runJob(ctx, &j)
		if j.State != "cancelled" {
			t.Fatal(j.State, j.Error)
		}
	})
	t.Run("native-copy-lost-response-and-export-deferred", func(t *testing.T) {
		f.add("native", "Document", "application/vnd.google-apps.document", false)
		f.add("copies", "Copies", "application/vnd.google-apps.folder", false)
		j := submit(CopyRequest{Items: []Item{{Source: "drive", Path: "native"}}, Destination: Item{Source: "drive", Path: "copies"}})
		f.loseNative = true
		before := f.copied
		s.runJob(ctx, &j)
		if j.State != "waiting" {
			t.Fatal(j.State, j.Error)
		}
		var resumed Job
		s.read(j.ID, "copy", &resumed)
		s.runJob(ctx, &resumed)
		if resumed.State != "completed" || f.copied != before+1 {
			t.Fatal("duplicate or lost native copy", resumed.State, resumed.Error)
		}
		local := submit(CopyRequest{Items: []Item{{Source: "drive", Path: "native"}}, Destination: Item{Source: "vps", Path: "/"}})
		s.runJob(ctx, &local)
		if local.State != "failed" {
			t.Fatal("native export unexpectedly accepted")
		}
	})
	t.Run("cancel-native-copy-after-lost-response", func(t *testing.T) {
		f.add("cancel-native", "Cancel document", "application/vnd.google-apps.document", false)
		j := submit(CopyRequest{Items: []Item{{Source: "drive", Path: "cancel-native"}}, Destination: Item{Source: "drive", Path: "copies"}})
		f.loseNative = true
		s.runJob(ctx, &j)
		if j.State != "waiting" {
			t.Fatal(j.State, j.Error)
		}
		j.Cancel = true
		s.save(j.ID, "copy", j.State, j)
		s.runJob(ctx, &j)
		if j.State != "cancelled" {
			t.Fatal("uncertain staged copy not reconciled for cancellation", j.State, j.Error)
		}
		if _, ok := f.items[j.RemoteStage]; ok {
			t.Fatal("cancelled stage retained")
		}
		if _, ok := f.items["cancel-native"]; !ok {
			t.Fatal("source was removed")
		}
	})
	t.Run("phone-upload-idempotent-and-recovered", func(t *testing.T) {
		key := ID()
		body := []byte(`{"name":"phone.jpg","parent":"root","size":6}`)
		w := call("POST", "/v1/drive/uploads", key, body, nil)
		if w.Code != 201 {
			t.Fatal(w.Code, w.Body.String())
		}
		var u Upload
		json.Unmarshal(w.Body.Bytes(), &u)
		w = call("POST", "/v1/drive/uploads", key, body, nil)
		var again Upload
		json.Unmarshal(w.Body.Bytes(), &again)
		if again.ID != u.ID {
			t.Fatal("duplicate upload")
		}
		route := "/v1/drive/uploads/" + u.ID
		w = call("PUT", route, "", []byte("abc"), map[string]string{"Content-Range": "bytes 0-2/6"})
		if w.Code != 200 {
			t.Fatal(w.Code, w.Body.String())
		}
		w = call("PUT", route, "", []byte("abc"), map[string]string{"Content-Range": "bytes 0-2/6"})
		if w.Code != 200 {
			t.Fatal("lost ack not idempotent", w.Code)
		}
		w = call("PUT", route, "", []byte("bad"), map[string]string{"Content-Range": "bytes 0-2/6"})
		if w.Code != 409 {
			t.Fatal("changed chunk accepted", w.Code)
		}
		// Simulate fsync finishing before the database checkpoint.
		file, e := os.OpenFile(filepath.Join(s.config.State, "uploads", u.ID, u.Name), os.O_WRONLY, 0600)
		if e != nil {
			t.Fatal(e)
		}
		file.WriteAt([]byte("def"), 3)
		file.Sync()
		file.Close()
		w = call("GET", route, "", nil, nil)
		json.Unmarshal(w.Body.Bytes(), &u)
		if w.Code != 200 || u.Received != 6 || u.JobID == "" {
			t.Fatal("upload not recovered", w.Code, w.Body.String())
		}
		var j Job
		s.read(u.JobID, "copy", &j)
		s.runJob(ctx, &j)
		if j.State != "completed" {
			t.Fatal(j.State, j.Error)
		}
		if _, e = os.Stat(filepath.Join(s.config.State, "uploads", u.ID)); !errors.Is(e, os.ErrNotExist) {
			t.Fatal("completed spool retained", e)
		}
	})
	t.Run("media-range-and-thumbnail-isolation", func(t *testing.T) {
		f.add("media", "video.mp4", "video/mp4", false)
		w := call("GET", "/v1/drive/files/media/content", "", nil, map[string]string{"Range": "bytes=2-4"})
		if w.Code != 206 || w.Body.String() != "cde" || w.Header().Get("Content-Range") != "bytes 2-4/6" {
			t.Fatal("range broken", w.Code, w.Body.String())
		}
		w = call("GET", "/v1/drive/files/media/content", "", nil, map[string]string{"Range": "bytes=9-10"})
		if w.Code != 416 {
			t.Fatal("invalid range accepted", w.Code)
		}
		item := f.items["media"]
		item.Thumbnail = "https://attacker.test/private"
		f.items["media"] = item
		w = call("GET", "/v1/drive/files/media/thumbnail", "", nil, nil)
		if w.Code != 400 {
			t.Fatal("external thumbnail URL accepted", w.Code)
		}
		w = call("GET", "/v1/drive/files/media", "", nil, nil)
		if strings.Contains(w.Body.String(), "attacker.test") {
			t.Fatal("thumbnail URL leaked")
		}
	})
}
