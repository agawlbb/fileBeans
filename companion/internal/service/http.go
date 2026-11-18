package service

import (
	"crypto/subtle"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

func reply(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func fail(w http.ResponseWriter, e error) {
	status := http.StatusServiceUnavailable
	switch {
	case errors.Is(e, ErrUnsafe):
		status = 400
	case errors.Is(e, ErrConflict):
		status = 409
	case errors.Is(e, sql.ErrNoRows), errors.Is(e, os.ErrNotExist):
		status = 404
	case errors.Is(e, ErrAuthorization), errors.Is(e, ErrDriveWrite), errors.Is(e, ErrDriveAccount), errors.Is(e, ErrDrivePermission):
		status = 403
	}
	if errors.Is(e, ErrVaultLocked) || errors.Is(e, ErrVaultBusy) || errors.Is(e, ErrVaultKey) {
		status = http.StatusConflict
	}
	reply(w, status, map[string]string{"error": publicError(e)})
}
func body(r *http.Request, v any) error {
	dec := json.NewDecoder(io.LimitReader(r.Body, 1024*1024))
	dec.DisallowUnknownFields()
	if e := dec.Decode(v); e != nil {
		return ErrUnsafe
	}
	if e := dec.Decode(new(any)); e != io.EOF {
		return ErrUnsafe
	}
	return nil
}
func key(r *http.Request) string {
	k := r.Header.Get("Idempotency-Key")
	if len(k) < 16 || len(k) > 64 {
		return ""
	}
	return k
}
func (s *Service) existing(key, kind string, v any) error {
	var b []byte
	var id string
	e := s.db.QueryRow("SELECT id,body FROM fb_operations WHERE request_key=? AND kind=?", key, kind).Scan(&id, &b)
	if e != nil {
		return e
	}
	b, e = s.decodeRecord(id, kind, b)
	if e != nil {
		return e
	}
	return json.Unmarshal(b, v)
}
func (s *Service) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/capabilities", func(w http.ResponseWriter, r *http.Request) {
		roots := make([]map[string]string, 0)
		for _, root := range s.config.Roots {
			roots = append(roots, map[string]string{"source": root.Name, "trash": "/Trash"})
		}
		g, e := s.google()
		features := []string{"protectedFolderMoves", "driveWrite", "driveDestinations", "driveUploads", "driveThumbnails", "driveMedia", "normalization"}
		if s.vaults != nil && s.vaults.remote != nil && s.vaults.remote.ready() {
			features = append(features, "protectedFoldersCreate")
		}
		reply(w, 200, map[string]any{"version": 1, "roots": roots, "driveConnected": e == nil, "driveWriteAccess": e == nil && g.writable(), "normalizationEnabled": s.config.Normalization.Enabled, "features": features})
	})
	mux.HandleFunc("POST /v1/jobs", func(w http.ResponseWriter, r *http.Request) {
		var req CopyRequest
		if e := body(r, &req); e != nil {
			fail(w, e)
			return
		}
		if e := validateMoveRequest(req); e != nil {
			fail(w, e)
			return
		}
		if req.Destination.Source == "drive" {
			if !driveID(req.Destination.Path) {
				fail(w, ErrUnsafe)
				return
			}
			if e := s.requireDriveWrite(""); e != nil {
				fail(w, e)
				return
			}
		} else {
			destination, err := clean(req.Destination.Path)
			if err != nil || protected(destination) || req.Destination.Source == "upload" {
				fail(w, ErrUnsafe)
				return
			}
			req.Destination.Path = destination
		}
		for i := range req.Items {
			if req.Items[i].Source != "drive" {
				p, err := clean(req.Items[i].Path)
				if err != nil {
					fail(w, err)
					return
				}
				req.Items[i].Path = p
			}
		}
		k := key(r)
		if k == "" || len(req.Items) == 0 || len(req.Items) > 100 {
			fail(w, ErrUnsafe)
			return
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		var old Job
		if e := s.existing(k, "copy", &old); e == nil {
			a, _ := json.Marshal(old.Request)
			b, _ := json.Marshal(req)
			if string(a) != string(b) {
				fail(w, ErrConflict)
				return
			}
			old.Manifest = nil
			reply(w, 200, publicJob(old))
			return
		} else if !errors.Is(e, sql.ErrNoRows) {
			fail(w, e)
			return
		}
		var e error
		if req.Destination.Source != "drive" {
			root, er := s.root(req.Destination.Source)
			if er != nil {
				fail(w, er)
				return
			}
			if guarded, ok := root.(*guardedRoot); ok {
				if er = guarded.check(req.Destination.Path); er != nil {
					root.Close()
					fail(w, er)
					return
				}
			}
			root.Close()
		}
		for _, item := range req.Items {
			if item.Source == "upload" {
				fail(w, ErrUnsafe)
				return
			}
			if item.Source == "drive" {
				if !driveID(item.Path) {
					fail(w, ErrUnsafe)
					return
				}
			} else {
				p, e := clean(item.Path)
				if e != nil || protected(p) {
					fail(w, ErrUnsafe)
					return
				}
				root, e := s.root(item.Source)
				if e != nil {
					fail(w, e)
					return
				}
				if guarded, ok := root.(*guardedRoot); ok {
					if e = guarded.check(item.Path); e != nil {
						root.Close()
						fail(w, e)
						return
					}
				}
				root.Close()
			}
		}
		j := Job{ID: ID(), State: "queued", Request: req}
		if hasDrive(req) {
			g, e := s.google()
			if e != nil {
				fail(w, e)
				return
			}
			j.Connection = g.Connection
		}
		if e = s.insert(j.ID, "copy", j.State, k, j); e != nil {
			fail(w, e)
			return
		}
		reply(w, 202, publicJob(j))
	})
	mux.HandleFunc("GET /v1/jobs", func(w http.ResponseWriter, r *http.Request) {
		records, e := s.records("copy")
		if e != nil {
			fail(w, e)
			return
		}
		out := []Job{}
		for _, b := range records {
			j, e := decodeJob(b)
			if e != nil {
				fail(w, e)
				return
			}
			j.Manifest = nil
			out = append(out, publicJob(j))
		}
		reply(w, 200, out)
	})
	mux.HandleFunc("GET /v1/jobs/{id}", func(w http.ResponseWriter, r *http.Request) {
		var j Job
		if e := s.read(r.PathValue("id"), "copy", &j); e != nil {
			fail(w, e)
			return
		}
		j.Manifest = nil
		reply(w, 200, publicJob(j))
	})
	mux.HandleFunc("POST /v1/jobs/{id}/cancel", func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		var j Job
		if e := s.read(r.PathValue("id"), "copy", &j); e != nil {
			fail(w, e)
			return
		}
		if j.State == "completed" || j.State == "cancelled" {
			reply(w, 200, publicJob(j))
			return
		}
		if j.Move != nil && j.Move.Committed {
			fail(w, ErrConflict)
			return
		}
		j.Cancel = true
		// A job that has never started has no staged data to clean up. Do not
		// make its cancellation wait behind a running transfer.
		if j.State == "queued" && j.Engine == "" && len(j.Manifest) == 0 && j.RemoteStage == "" {
			j.State = "cancelled"
			j.Speed = 0
		}
		if j.State == "failed" {
			j.State = "queued"
		}
		j.Next = 0
		if e := s.save(j.ID, "copy", j.State, j); e != nil {
			fail(w, e)
			return
		}
		if stop := s.copyCancels[j.ID]; stop != nil {
			stop() // Interrupt in-flight Drive HTTP reads immediately.
		}
		if j.State == "cancelled" {
			s.cleanupJobUploads(&j)
		}
		j.Manifest = nil
		reply(w, 200, publicJob(j))
	})
	mux.HandleFunc("POST /v1/jobs/{id}/retry", func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		var j Job
		if e := s.read(r.PathValue("id"), "copy", &j); e != nil {
			fail(w, e)
			return
		}
		if j.State != "failed" || (len(j.Published) != 0 && j.Move == nil) {
			fail(w, ErrConflict)
			return
		}
		if j.Request.Destination.Source == "drive" {
			if e := s.requireDriveWrite(j.Connection); e != nil {
				fail(w, e)
				return
			}
			if e := s.cleanupDriveStage(r.Context(), &j); e != nil {
				fail(w, e)
				return
			}
			j.RemoteStage = ""
		}
		// Rclone reconciles its private staging on retry. Keep completed files rather
		// than deleting the entire copy and downloading everything again.
		if j.Request.Destination.Source == "drive" {
			j.Manifest = nil
		}
		j.ItemsDone = 0
		j.ItemsTotal = 0
		j.Done = 0
		j.Total = 0
		j.Error = ""
		j.Cancel = false
		j.Next = 0
		j.State = "queued"
		if e := s.save(j.ID, "copy", j.State, j); e != nil {
			fail(w, e)
			return
		}
		reply(w, 200, publicJob(j))
	})
	mux.HandleFunc("POST /v1/trash", func(w http.ResponseWriter, r *http.Request) {
		var item Item
		if e := body(r, &item); e != nil {
			fail(w, e)
			return
		}
		k := key(r)
		if k == "" {
			fail(w, ErrUnsafe)
			return
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		var old Trash
		if e := s.existing(k, "trash", &old); e == nil {
			p, _ := clean(item.Path)
			if old.Source != item.Source || old.Original != p {
				fail(w, ErrConflict)
				return
			}
			if old.State == "trashing" {
				fail(w, errors.New("delete recovery pending"))
				return
			}
			reply(w, 200, old)
			return
		} else if !errors.Is(e, sql.ErrNoRows) {
			fail(w, e)
			return
		}
		t, e := s.trash(item, k)
		if e != nil {
			fail(w, e)
			return
		}
		reply(w, 201, t)
	})
	mux.HandleFunc("GET /v1/trash", func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.reconcileTrash()
		records, e := s.records("trash")
		if e != nil {
			fail(w, e)
			return
		}
		out := []Trash{}
		for _, b := range records {
			var t Trash
			if e = json.Unmarshal(b, &t); e != nil {
				fail(w, e)
				return
			}
			if t.State == "retained" || t.State == "purging" {
				out = append(out, t)
			}
		}
		reply(w, 200, out)
	})
	mux.HandleFunc("POST /v1/trash/{id}/restore", func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		if e := s.restore(r.PathValue("id")); e != nil {
			fail(w, e)
			return
		}
		reply(w, 200, map[string]bool{"restored": true})
	})
	mux.HandleFunc("POST /v1/trash/{id}/purge", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Confirm-Permanent") != "yes" {
			fail(w, ErrUnsafe)
			return
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		var t Trash
		if e := s.read(r.PathValue("id"), "trash", &t); e != nil {
			fail(w, e)
			return
		}
		if t.State != "retained" && t.State != "purging" {
			fail(w, ErrConflict)
			return
		}
		if e := s.purge(&t); e != nil {
			fail(w, e)
			return
		}
		reply(w, 200, map[string]bool{"purged": true})
	})
	mux.HandleFunc("POST /v1/trash/empty", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Confirm-Permanent") != "yes" {
			fail(w, ErrUnsafe)
			return
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		records, e := s.records("trash")
		if e != nil {
			fail(w, e)
			return
		}
		for _, b := range records {
			var t Trash
			if e = json.Unmarshal(b, &t); e != nil {
				fail(w, e)
				return
			}
			if t.State == "retained" || t.State == "purging" {
				if e = s.purge(&t); e != nil {
					fail(w, e)
					return
				}
			}
		}
		reply(w, 200, map[string]bool{"purged": true})
	})
	mux.HandleFunc("GET /v1/drive/files", func(w http.ResponseWriter, r *http.Request) {
		d, e := s.driveList(r.Context(), r.URL.Query().Get("folder"), r.URL.Query().Get("page"), "")
		if e != nil {
			fail(w, e)
			return
		}
		for i := range d.Files {
			d.Files[i].Thumbnail = ""
		}
		reply(w, 200, d)
	})
	mux.HandleFunc("GET /v1/drive/drives", func(w http.ResponseWriter, r *http.Request) {
		q := url.Values{"pageSize": {"100"}, "fields": {"nextPageToken,drives(id,name)"}}
		if page := r.URL.Query().Get("page"); page != "" {
			q.Set("pageToken", page)
		}
		response, e := s.driveRequest(r.Context(), "drives", q, "")
		if e != nil {
			fail(w, e)
			return
		}
		defer response.Body.Close()
		var result map[string]any
		if e = json.NewDecoder(io.LimitReader(response.Body, 4*1024*1024)).Decode(&result); e != nil {
			fail(w, e)
			return
		}
		if result == nil {
			result = map[string]any{}
		}
		if result["drives"] == nil {
			result["drives"] = []any{}
		}
		reply(w, 200, result)
	})
	mux.HandleFunc("POST /v1/drive/connect", func(w http.ResponseWriter, r *http.Request) {
		u, e := s.authURL()
		if e != nil {
			fail(w, e)
			return
		}
		reply(w, 200, map[string]string{"url": u})
	})
	mux.HandleFunc("POST /v1/drive/disconnect", func(w http.ResponseWriter, r *http.Request) {
		unlock, e := s.lockGoogle(r.Context())
		if e != nil {
			fail(w, e)
			return
		}
		defer unlock()
		for _, name := range []string{"google.json", "oauth-state.json"} {
			if e := os.Remove(filepath.Join(s.config.State, name)); e != nil && !errors.Is(e, os.ErrNotExist) {
				fail(w, e)
				return
			}
		}
		reply(w, 200, map[string]bool{"disconnected": true})
	})
	s.driveRoutes(mux)
	s.normalizationRoutes(mux)
	s.vaultRoutes(mux)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		if r.Method == "GET" && r.URL.Path == "/oauth/callback" {
			if e := s.callback(r.Context(), r.URL.Query().Get("state"), r.URL.Query().Get("code")); e != nil {
				fail(w, e)
				return
			}
			w.Header().Set("Content-Type", "text/plain")
			w.Write([]byte("Google Drive connected. Return to fileBeans."))
			return
		}
		token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if subtle.ConstantTimeCompare([]byte(token), []byte(s.config.Token)) != 1 {
			reply(w, 401, map[string]string{"error": "Authentication required"})
			return
		}
		if !strings.HasPrefix(r.URL.Path, "/v1/vaults") {
			s.vaultGate.RLock()
			defer s.vaultGate.RUnlock()
		}
		mux.ServeHTTP(w, r)
	})
}
func hasDrive(r CopyRequest) bool {
	if r.Destination.Source == "drive" {
		return true
	}
	for _, i := range r.Items {
		if i.Source == "drive" {
			return true
		}
	}
	return false
}

func publicJob(j Job) Job {
	if j.Move != nil {
		allowed := !j.Move.Committed
		j.CanCancel = &allowed
	}
	j.Move = nil
	j.Manifest = nil
	j.RclonePlan = nil
	j.RemoteStage = ""
	return j
}
