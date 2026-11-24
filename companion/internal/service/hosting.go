package service

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-sql-driver/mysql"
)

// Hosting references an ordinary original file, never a public copy of protected data.
// Revoked identifiers remain reserved so an old URL cannot expose a different file.
type Hosting struct {
	ID         string `json:"id"`
	Item       Item   `json:"item"`
	Name       string `json:"name"`
	Revision   string `json:"revision"`
	Identity   string `json:"identity,omitempty"`
	Connection string `json:"connection,omitempty"`
	Created    int64  `json:"created"`
	Expires    int64  `json:"expires"`
	State      string `json:"state"`
	URLPath    string `json:"urlPath"`
}

type hostingRequest struct {
	Item    Item  `json:"item"`
	Expires int64 `json:"expires"`
}

var hostingMu sync.Mutex

func hostingID(id string) bool {
	if len(id) == 0 || len(id) > 48 {
		return false
	}
	for _, c := range id {
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-') {
			return false
		}
	}
	return true
}
func hostingName(name string) bool { return validName(name) && !strings.ContainsAny(name, "\r\n") }
func hostingSlug(name string) string {
	stem := strings.TrimSuffix(name, filepath.Ext(name))
	var b strings.Builder
	for _, c := range strings.ToLower(stem) {
		if b.Len() >= 36 {
			break
		}
		if c >= 'a' && c <= 'z' || c >= '0' && c <= '9' {
			b.WriteRune(c)
		} else if b.Len() > 0 && !strings.HasSuffix(b.String(), "-") {
			b.WriteByte('-')
		}
	}
	slug := strings.Trim(b.String(), "-")
	if slug == "" {
		slug = "file"
	}
	return slug
}
func hostingExpiry(expires int64) error {
	now := time.Now().Unix()
	if expires != 0 && (expires <= now || expires > now+10*365*24*60*60) {
		return ErrUnsafe
	}
	return nil
}
func hostingPublic(h Hosting) Hosting {
	h.URLPath = "/h/" + h.ID + "/" + url.PathEscape(h.Name)
	if h.State == "active" && h.Expires != 0 && h.Expires <= time.Now().Unix() {
		h.State = "expired"
	}
	return h
}
func hostingKey(id string) string { return "hosting:" + id }

// ordinaryOnly checks the acquired broker snapshot, even while a vault is unlocked.
// Keep this root/lease alive through the download to prevent topology changes.
func (s *Service) hostingRoot(item Item) (rootFS, string, error) {
	p, err := filesystemPath(item.Path)
	if err != nil || p == "." || protected(p) {
		return nil, "", ErrUnsafe
	}
	known := false
	for _, root := range s.config.Roots {
		if root.Name == item.Source {
			known = true
			break
		}
	}
	if !known {
		return nil, "", ErrUnsafe
	}
	for _, v := range s.config.Vaults {
		boundary := v.Path
		if boundary == "" {
			boundary = "/"
		}
		if v.Source == item.Source && within("/"+p, boundary) {
			return nil, "", ErrUnsafe
		}
	}
	if s.vaults.pathVault(item.Source, p) != nil {
		return nil, "", ErrUnsafe
	}
	root, err := s.root(item.Source)
	if err != nil {
		return nil, "", err
	}
	if guarded, ok := root.(*guardedRoot); ok {
		guarded.ordinaryOnly = true
	}
	if err = safe(root, p); err != nil {
		root.Close()
		return nil, "", err
	}
	return root, p, nil
}
func (s *Service) prepareHosting(ctx context.Context, req hostingRequest) (Hosting, error) {
	if err := hostingExpiry(req.Expires); err != nil {
		return Hosting{}, err
	}
	h := Hosting{Item: req.Item, Expires: req.Expires, Created: time.Now().Unix(), State: "active"}
	h.Item.Name = ""
	if req.Item.Source == "drive" {
		g, err := s.token(ctx, "")
		if err != nil {
			return h, err
		}
		if g.Connection == "" {
			return h, ErrDriveAccount
		}
		meta, err := s.driveMeta(ctx, req.Item.Path, g.Connection)
		if err != nil {
			return h, err
		}
		if meta.Trashed || !meta.Capabilities.Download || strings.HasPrefix(meta.Mime, "application/vnd.google-apps.") || meta.Version == "" {
			return h, ErrUnsafe
		}
		h.Name, h.Revision, h.Connection = meta.Name, meta.Version, g.Connection
	} else {
		root, p, err := s.hostingRoot(req.Item)
		if err != nil {
			return h, err
		}
		defer root.Close()
		f, err := filesystemOpenFile(root, p)
		if err != nil {
			return h, err
		}
		defer f.Close()
		st, err := f.Stat()
		if err != nil {
			return h, err
		}
		h.Item.Path = "/" + p
		h.Name, h.Revision, h.Identity = st.Name(), filesystemRevision(st), moveIdentity(st)
	}
	if !hostingName(h.Name) {
		return h, ErrUnsafe
	}
	return h, nil
}

func (s *Service) hostingRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /v1/hosting", func(w http.ResponseWriter, r *http.Request) {
		var req hostingRequest
		if err := body(r, &req); err != nil {
			fail(w, err)
			return
		}
		h, err := s.prepareHosting(r.Context(), req)
		if err != nil {
			fail(w, err)
			return
		}
		stem := hostingSlug(h.Name)
		// The primary key resolves simultaneous name collisions across processes.
		for n := 1; n <= 10000; n++ {
			h.ID = stem
			if n > 1 {
				h.ID += "-" + strconv.Itoa(n)
			}
			err = s.insert(hostingKey(h.ID), "hosting", "active", "", h)
			if err == nil {
				reply(w, 201, hostingPublic(h))
				return
			}
			var duplicate *mysql.MySQLError
			if !errors.As(err, &duplicate) || duplicate.Number != 1062 {
				fail(w, err)
				return
			}
		}
		fail(w, ErrConflict)
	})
	mux.HandleFunc("GET /v1/hosting", func(w http.ResponseWriter, r *http.Request) {
		rows, err := s.db.QueryContext(r.Context(), "SELECT id,body FROM fb_operations WHERE kind='hosting' AND state<>'revoked' ORDER BY updated_at DESC")
		if err != nil {
			fail(w, err)
			return
		}
		defer rows.Close()
		out := []Hosting{}
		for rows.Next() {
			var id string
			var data []byte
			if err = rows.Scan(&id, &data); err != nil {
				fail(w, err)
				return
			}
			data, err = s.decodeRecord(id, "hosting", data)
			if errors.Is(err, ErrVaultLocked) {
				out = append(out, Hosting{ID: strings.TrimPrefix(id, "hosting:"), Name: "Unavailable file", State: "unavailable"})
				continue
			}
			if err != nil {
				fail(w, err)
				return
			}
			var h Hosting
			if err = json.Unmarshal(data, &h); err != nil {
				fail(w, err)
				return
			}
			out = append(out, hostingPublic(h))
		}
		if err = rows.Err(); err != nil {
			fail(w, err)
			return
		}
		reply(w, 200, out)
	})
	mux.HandleFunc("PATCH /v1/hosting/{id}", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		if !hostingID(id) {
			fail(w, ErrUnsafe)
			return
		}
		var req struct {
			Expires int64 `json:"expires"`
		}
		if err := body(r, &req); err != nil {
			fail(w, err)
			return
		}
		if err := hostingExpiry(req.Expires); err != nil {
			fail(w, err)
			return
		}
		hostingMu.Lock()
		defer hostingMu.Unlock()
		h, err := s.readHosting(r.Context(), id)
		if err != nil {
			fail(w, err)
			return
		}
		// Revalidate before renewing or changing expiry, including protection boundaries.
		current, err := s.prepareHosting(r.Context(), hostingRequest{Item: h.Item, Expires: req.Expires})
		if err != nil {
			fail(w, err)
			return
		}
		if current.Name != h.Name || current.Identity != h.Identity || current.Revision != h.Revision || current.Connection != h.Connection {
			fail(w, ErrConflict)
			return
		}
		h.Expires = req.Expires
		if err = s.save(hostingKey(id), "hosting", "active", h); err != nil {
			fail(w, err)
			return
		}
		reply(w, 200, hostingPublic(h))
	})
	mux.HandleFunc("DELETE /v1/hosting/{id}", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		if !hostingID(id) {
			fail(w, ErrUnsafe)
			return
		}
		hostingMu.Lock()
		defer hostingMu.Unlock()
		// No metadata decryption required: stopping hosting must also work while locked.
		result, err := s.db.ExecContext(r.Context(), "UPDATE fb_operations SET state='revoked' WHERE id=? AND kind='hosting'", hostingKey(id))
		if err != nil {
			fail(w, err)
			return
		}
		affected, err := result.RowsAffected()
		if err != nil {
			fail(w, err)
			return
		}
		if affected == 0 {
			var exists int
			if err = s.db.QueryRowContext(r.Context(), "SELECT 1 FROM fb_operations WHERE id=? AND kind='hosting'", hostingKey(id)).Scan(&exists); err != nil {
				fail(w, err)
				return
			}
		}
		w.WriteHeader(204)
	})
}
func (s *Service) readHosting(ctx context.Context, id string) (Hosting, error) {
	var h Hosting
	var data []byte
	err := s.db.QueryRowContext(ctx, "SELECT body FROM fb_operations WHERE id=? AND kind='hosting' AND state='active'", hostingKey(id)).Scan(&data)
	if err != nil {
		return h, err
	}
	data, err = s.decodeRecord(hostingKey(id), "hosting", data)
	if err == nil {
		err = json.Unmarshal(data, &h)
	}
	return h, err
}
func hostingNotFound(w http.ResponseWriter, r *http.Request) { http.NotFound(w, r) }
func (s *Service) publicHosting(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !hostingID(id) {
		hostingNotFound(w, r)
		return
	}
	h, err := s.readHosting(r.Context(), id)
	if err != nil || h.Expires != 0 && h.Expires <= time.Now().Unix() || r.PathValue("filename") != h.Name {
		hostingNotFound(w, r)
		return
	}
	w.Header().Set("X-Robots-Tag", "noindex, nofollow, noarchive")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": h.Name}))
	if h.Item.Source == "drive" {
		s.publicHostingDrive(w, r, h)
		return
	}
	root, p, err := s.hostingRoot(h.Item)
	if err != nil {
		hostingNotFound(w, r)
		return
	}
	defer root.Close()
	f, err := filesystemOpenFile(root, p)
	if err != nil {
		hostingNotFound(w, r)
		return
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || filesystemRevision(st) != h.Revision || moveIdentity(st) != h.Identity {
		hostingNotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(time.Minute))
	http.ServeContent(filesystemWriter{w}, r, h.Name, st.ModTime(), f)
}
func (s *Service) publicHostingDrive(w http.ResponseWriter, r *http.Request, h Hosting) {
	meta, err := s.driveMeta(r.Context(), h.Item.Path, h.Connection)
	if err != nil || meta.Trashed || !meta.Capabilities.Download || strings.HasPrefix(meta.Mime, "application/vnd.google-apps.") || meta.Name != h.Name || meta.Version != h.Revision {
		hostingNotFound(w, r)
		return
	}
	size, err := strconv.ParseInt(meta.Size, 10, 64)
	if err != nil || size < 0 {
		hostingNotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Accept-Ranges", "bytes")
	if r.Method == "HEAD" {
		w.Header().Set("Content-Length", meta.Size)
		return
	}
	requested := r.Header.Get("Range")
	if requested != "" && !validByteRange(requested, size) {
		w.Header().Set("Content-Range", "bytes */"+meta.Size)
		w.WriteHeader(416)
		return
	}
	response, err := s.driveRange(r.Context(), h.Item.Path, requested, h.Connection)
	if err != nil {
		hostingNotFound(w, r)
		return
	}
	defer response.Body.Close()
	for _, name := range []string{"Content-Length", "Content-Range"} {
		if value := response.Header.Get(name); value != "" {
			w.Header().Set(name, value)
		}
	}
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(time.Minute))
	w.WriteHeader(response.StatusCode)
	_, _ = io.Copy(filesystemWriter{w}, response.Body)
}
