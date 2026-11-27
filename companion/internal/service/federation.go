package service

// Federation routes the existing API to the owner of a source. It does not
// mount remote storage or permit a request to select an arbitrary upstream URL.
import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"time"
)

func (s *Service) peerFor(source string) *PeerConfig {
	for i := range s.config.Peers {
		for _, n := range s.config.Peers[i].Sources {
			if source == n {
				return &s.config.Peers[i]
			}
		}
	}
	return nil
}
func (s *Service) crossPeer(r CopyRequest) bool {
	for _, i := range r.Items {
		if s.peerFor(i.Source) != nil {
			return true
		}
	}
	return false
}
func peerRequest(ctx context.Context, p PeerConfig, method, path string, q url.Values, b io.Reader) (*http.Response, error) {
	u, e := url.Parse(p.URL)
	if e != nil {
		return nil, e
	}
	u.Path = strings.TrimRight(u.Path, "/") + path
	u.RawQuery = q.Encode()
	r, e := http.NewRequestWithContext(ctx, method, u.String(), b)
	if e != nil {
		return nil, e
	}
	r.Header.Set("Authorization", "Bearer "+p.Token)
	r.Header.Set("X-FileBeans-Peer", "1")
	return peerClient(p).Do(r)
}
func peerJSON(ctx context.Context, p PeerConfig, path string, in, out any) error {
	b, e := json.Marshal(in)
	if e != nil {
		return e
	}
	resp, e := peerRequest(ctx, p, "POST", path, nil, bytes.NewReader(b))
	if e != nil {
		return e
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return peerError(resp.StatusCode)
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 16<<20)).Decode(out)
}
func peerError(status int) error {
	switch status {
	case 400:
		return ErrUnsafe
	case 401, 403:
		return ErrAuthorization
	case 404:
		return ErrMount
	case 409, 412:
		return ErrConflict
	}
	return errors.New("peer unavailable")
}
func (s *Service) proxyPeer(p PeerConfig, w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("X-FileBeans-Peer") != "" {
		fail(w, ErrUnsafe)
		return
	}
	u, e := url.Parse(p.URL)
	if e != nil {
		fail(w, e)
		return
	}
	u.Path = strings.TrimRight(u.Path, "/") + r.URL.Path
	u.RawQuery = r.URL.RawQuery
	out := r.Clone(r.Context())
	out.URL = u
	out.RequestURI = ""
	out.Host = u.Host
	out.Header = r.Header.Clone()
	out.Header.Set("Authorization", "Bearer "+p.Token)
	out.Header.Set("X-FileBeans-Peer", "1")
	resp, e := peerClient(p).Do(out)
	if e != nil {
		fail(w, e)
		return
	}
	defer resp.Body.Close()
	for k, v := range resp.Header {
		if k != "Set-Cookie" && k != "Connection" {
			w.Header()[k] = v
		}
	}
	if strings.HasPrefix(r.URL.Path, "/v1/jobs") || strings.HasPrefix(r.URL.Path, "/v1/trash") || strings.HasPrefix(r.URL.Path, "/v1/vaults") {
		if strings.HasPrefix(resp.Header.Get("Content-Type"), "application/json") {
			var value any
			if e = json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&value); e != nil {
				fail(w, e)
				return
			}
			prefixID := func(v any) {
				if m, ok := v.(map[string]any); ok {
					if id, ok := m["id"].(string); ok {
						m["id"] = p.ID + "~" + id
					}
				}
			}
			switch v := value.(type) {
			case map[string]any:
				prefixID(v)
			case []any:
				for _, item := range v {
					prefixID(item)
				}
			}
			w.Header().Del("Content-Length")
			reply(w, resp.StatusCode, value)
			return
		}
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}

func (s *Service) federated(local http.Handler, w http.ResponseWriter, r *http.Request) {
	if s.config.Profile == "pi" && (strings.HasPrefix(r.URL.Path, "/v1/archive-") || strings.HasPrefix(r.URL.Path, "/v1/datasets/") || strings.HasPrefix(r.URL.Path, "/v1/normalization")) {
		fail(w, ErrUnsupported)
		return
	}
	if len(s.config.Peers) == 0 || r.Header.Get("X-FileBeans-Peer") != "" || strings.HasPrefix(r.URL.Path, "/v1/peer/") {
		local.ServeHTTP(w, r)
		return
	}
	if strings.HasPrefix(r.URL.Path, "/v1/protection") {
		s.federatedProtection(local, w, r)
		return
	}
	source := r.URL.Query().Get("source")
	if strings.HasPrefix(r.URL.Path, "/v1/drive/") {
		source = "drive"
	}
	if r.Method == "POST" && r.URL.Path == "/v1/jobs" {
		b, e := io.ReadAll(io.LimitReader(r.Body, (1<<20)+1))
		if e != nil || len(b) > 1<<20 {
			fail(w, ErrUnsafe)
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(b))
		var req CopyRequest
		if json.Unmarshal(b, &req) != nil {
			fail(w, ErrUnsafe)
			return
		}
		source = req.Destination.Source
	}
	// Route source-bearing JSON writes without consuming upload streams.
	if source == "" && r.Method == "POST" && (r.URL.Path == "/v1/trash" || r.URL.Path == "/v1/fs/move" || r.URL.Path == "/v1/vaults") {
		b, e := io.ReadAll(io.LimitReader(r.Body, (1<<20)+1))
		if e != nil || len(b) > 1<<20 {
			fail(w, ErrUnsafe)
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(b))
		var item Item
		_ = json.Unmarshal(b, &item)
		source = item.Source
	}
	if p := s.peerFor(source); p != nil {
		s.proxyPeer(*p, w, r)
		return
	}
	// Prefix only remote operation IDs; the owning server always sees its original ID.
	for _, kind := range []string{"jobs", "trash", "vaults"} {
		prefix := "/v1/" + kind + "/"
		if strings.HasPrefix(r.URL.Path, prefix) {
			rest := strings.TrimPrefix(r.URL.Path, prefix)
			for _, p := range s.config.Peers {
				if strings.HasPrefix(rest, p.ID+"~") {
					copy := r.Clone(r.Context())
					u := *r.URL
					copy.URL = &u
					copy.URL.Path = prefix + strings.TrimPrefix(rest, p.ID+"~")
					s.proxyPeer(p, w, copy)
					return
				}
			}
		}
	}
	if r.Method == "GET" && (r.URL.Path == "/v1/fs/sources" || r.URL.Path == "/v1/jobs" || r.URL.Path == "/v1/vaults" || r.URL.Path == "/v1/trash") {
		s.aggregate(local, w, r)
		return
	}
	if r.URL.Path == "/v1/capabilities" && r.Method == "GET" {
		rec := httptest.NewRecorder()
		local.ServeHTTP(rec, r)
		var data map[string]any
		if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &data) != nil {
			fail(w, ErrMount)
			return
		}
		data["features"] = append(data["features"].([]any), "peerTransfersV1")
		// Drive authorization is owned by the VPS. A short probe cannot block local use.
		if p := s.peerFor("drive"); p != nil {
			ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
			defer cancel()
			resp, e := peerRequest(ctx, *p, "GET", "/v1/capabilities", nil, nil)
			if e == nil {
				defer resp.Body.Close()
				var remote map[string]any
				if resp.StatusCode == 200 && json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&remote) == nil {
					data["driveConnected"] = remote["driveConnected"]
					data["driveWriteAccess"] = remote["driveWriteAccess"]
				}
			}
		}
		reply(w, 200, data)
		return
	}
	local.ServeHTTP(w, r)
}

func (s *Service) aggregate(local http.Handler, w http.ResponseWriter, r *http.Request) {
	rec := httptest.NewRecorder()
	local.ServeHTTP(rec, r)
	if rec.Code != 200 {
		w.WriteHeader(rec.Code)
		_, _ = w.Write(rec.Body.Bytes())
		return
	}
	object := r.URL.Path == "/v1/fs/sources"
	var items []map[string]any
	if object {
		var d struct {
			Scopes []map[string]any `json:"scopes"`
		}
		if json.Unmarshal(rec.Body.Bytes(), &d) != nil {
			fail(w, ErrMount)
			return
		}
		items = d.Scopes
	} else {
		if json.Unmarshal(rec.Body.Bytes(), &items) != nil {
			fail(w, ErrMount)
			return
		}
	}
	for _, p := range s.config.Peers {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		resp, e := peerRequest(ctx, p, "GET", r.URL.Path, r.URL.Query(), nil)
		if e == nil {
			var remote []map[string]any
			if object {
				var d struct {
					Scopes []map[string]any `json:"scopes"`
				}
				e = json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&d)
				remote = d.Scopes
			} else {
				e = json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&remote)
			}
			resp.Body.Close()
			if e == nil && resp.StatusCode == 200 {
				for _, v := range remote {
					if object {
						name, _ := v["name"].(string)
						if owner := s.peerFor(name); owner == nil || owner.ID != p.ID {
							continue
						}
					} else {
						if source, ok := v["source"].(string); ok {
							if owner := s.peerFor(source); owner == nil || owner.ID != p.ID {
								continue
							}
						}
						if request, ok := v["request"].(map[string]any); ok {
							if destination, ok := request["destination"].(map[string]any); ok {
								if source, ok := destination["source"].(string); ok {
									if owner := s.peerFor(source); owner == nil || owner.ID != p.ID {
										continue
									}
								}
							}
						}
						if id, ok := v["id"].(string); ok {
							v["id"] = p.ID + "~" + id
						}
					}
					items = append(items, v)
				}
			}
		}
		cancel()
	}
	if object {
		seen := map[string]bool{}
		for _, v := range items {
			if n, ok := v["name"].(string); ok {
				seen[n] = true
			}
		}
		for _, p := range s.config.Peers {
			for _, n := range p.Sources {
				if n != "drive" && !seen[n] {
					items = append(items, map[string]any{"name": n, "scope": "/", "permissions": map[string]bool{"view": true, "download": true, "modify": true, "create": true, "delete": true}})
				}
			}
		}
	}
	if items == nil {
		items = []map[string]any{}
	}
	if object {
		reply(w, 200, map[string]any{"version": 1, "scopes": items})
	} else {
		reply(w, 200, items)
	}
}
