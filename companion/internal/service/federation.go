package service

// Explicit server-to-server jobs use configured, authenticated peers. Ordinary
// client browsing and server management always stay within the addressed server.
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
)

func splitPeerSource(source string) (string, string, bool) {
	parts := strings.SplitN(source, ":", 3)
	if len(parts) == 3 && parts[0] == "peer" && parts[1] != "" && parts[2] != "" {
		return parts[1], parts[2], true
	}
	return "", source, false
}
func peerSourceFor(source, owner string) string {
	id, root, ok := splitPeerSource(source)
	if ok && id == owner {
		return root
	}
	return source
}
func (s *Service) peerFor(source string) *PeerConfig {
	if source == "drive" {
		return nil
	}
	owner, root, qualified := splitPeerSource(source)
	if !qualified {
		for _, r := range s.config.Roots {
			if r.Name == source {
				return nil
			}
		}
	}
	var found *PeerConfig
	for i := range s.config.Peers {
		p := &s.config.Peers[i]
		if qualified && p.ID != owner {
			continue
		}
		for _, name := range p.Sources {
			if name == root {
				if found != nil {
					return nil
				}
				found = p
			}
		}
	}
	return found
}
func (s *Service) normalizeTransferLocation(item Item) (Item, error) {
	if item.ServerID == "" || item.ServerID == s.config.ServerID {
		item.ServerID = ""
		return item, nil
	}
	source := "peer:" + item.ServerID + ":" + item.Source
	if s.peerFor(source) == nil {
		return item, ErrUnsupported
	}
	item.Source = source
	item.ServerID = ""
	return item, nil
}
func publicTransferLocation(item Item) Item {
	if owner, source, ok := splitPeerSource(item.Source); ok {
		item.Source = source
		item.ServerID = owner
	}
	return item
}

// Internal RPCs use the recipient's own source names. Qualified names are only
// the submitting server's transfer coordinates, not a public browsing namespace.
func rewritePeerSources(value any, owner string) {
	switch v := value.(type) {
	case map[string]any:
		if source, ok := v["source"].(string); ok {
			v["source"] = peerSourceFor(source, owner)
		}
		for _, child := range v {
			rewritePeerSources(child, owner)
		}
	case []any:
		for _, child := range v {
			rewritePeerSources(child, owner)
		}
	}
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
	query := url.Values{}
	for key, values := range q {
		for _, value := range values {
			if key == "source" {
				value = peerSourceFor(value, p.ID)
			}
			query.Add(key, value)
		}
	}
	u.RawQuery = query.Encode()
	r, e := http.NewRequestWithContext(ctx, method, u.String(), b)
	if e != nil {
		return nil, e
	}
	r.Header.Set("Authorization", "Bearer "+p.Token)
	r.Header.Set("X-FileBeans-Peer", "1")
	if p.ArchiveKey != "" {
		r.Header.Set("X-FileBeans-Archive-Key", p.ArchiveKey)
	}
	client := peerClient(p)
	if path == "/v1/peer/archive" || path == "/v1/peer/archive-content" {
		client = archivePeerClient(p)
	}
	return client.Do(r)
}
func peerJSON(ctx context.Context, p PeerConfig, path string, in, out any) error {
	b, e := json.Marshal(in)
	if e != nil {
		return e
	}
	if request, ok := in.(archivePeerRequest); ok {
		// Mirror records carry hashes of their original JSON bytes. Never run
		// them through an untyped JSON round trip or rewrite sealed metadata.
		request.Source = peerSourceFor(request.Source, p.ID)
		var endpoint func(*ArchiveEndpoint)
		endpoint = func(e *ArchiveEndpoint) {
			e.Source = peerSourceFor(e.Source, p.ID)
			if e.Pending != nil {
				endpoint(e.Pending)
			}
		}
		endpoint(&request.Endpoint)
		b, e = json.Marshal(request)
	} else {
		var value any
		if json.Unmarshal(b, &value) != nil {
			return ErrUnsafe
		}
		rewritePeerSources(value, p.ID)
		b, e = json.Marshal(value)
	}
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
	if e = json.NewDecoder(io.LimitReader(resp.Body, 16<<20)).Decode(out); e != nil {
		return e
	}
	if item, ok := in.(Item); ok {
		if files, ok := out.(*[]File); ok {
			for i := range *files {
				if (*files)[i].Source == peerSourceFor(item.Source, p.ID) {
					(*files)[i].Source = item.Source
				}
			}
		}
	}
	return nil
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
	if r.Method == http.MethodPost && r.URL.Path == "/v1/jobs" {
		var request CopyRequest
		if json.NewDecoder(io.LimitReader(r.Body, (1<<20)+1)).Decode(&request) != nil {
			fail(w, ErrUnsafe)
			return
		}
		request.Destination.Source = peerSourceFor(request.Destination.Source, p.ID)
		for i := range request.Items {
			owner, source, qualified := splitPeerSource(request.Items[i].Source)
			if !qualified {
				request.Items[i].ServerID = ""
				for _, source := range p.Sources {
					if source == request.Items[i].Source {
						request.Items[i].ServerID = s.config.ServerID
					}
				}
			} else {
				request.Items[i].ServerID = owner
				request.Items[i].Source = source
			}
		}
		raw, err := json.Marshal(request)
		if err != nil {
			fail(w, err)
			return
		}
		out.Body = io.NopCloser(bytes.NewReader(raw))
		out.ContentLength = int64(len(raw))
	}
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

// Client APIs belong to this server. A configured peer is an internal transfer
// destination, not an additional browsing namespace or authentication provider.
func (s *Service) routeExplicitPeerOperations(local http.Handler, w http.ResponseWriter, r *http.Request) {
	if len(s.config.Peers) == 0 || r.Header.Get("X-FileBeans-Peer") != "" || strings.HasPrefix(r.URL.Path, "/v1/peer/") {
		local.ServeHTTP(w, r)
		return
	}
	if r.Method == http.MethodPost && r.URL.Path == "/v1/jobs" {
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
		destination, err := s.normalizeTransferLocation(req.Destination)
		if err != nil {
			fail(w, err)
			return
		}
		req.Destination = destination
		for i := range req.Items {
			item, err := s.normalizeTransferLocation(req.Items[i])
			if err != nil {
				fail(w, err)
				return
			}
			req.Items[i] = item
		}
		raw, err := json.Marshal(req)
		if err != nil {
			fail(w, err)
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(raw))
		r.ContentLength = int64(len(raw))
		if p := s.peerFor(req.Destination.Source); p != nil {
			if req.DriveGrant != nil {
				fail(w, ErrUnsafe)
				return
			}
			s.proxyPeer(*p, w, r)
			return
		}
	}
	// Only an explicitly submitted transfer's owner-qualified ID can route to
	// another server. Vaults, Trash and file paths never select another owner.
	const jobs = "/v1/jobs/"
	if strings.HasPrefix(r.URL.Path, jobs) {
		rest := strings.TrimPrefix(r.URL.Path, jobs)
		for _, p := range s.config.Peers {
			if strings.HasPrefix(rest, p.ID+"~") {
				copy := r.Clone(r.Context())
				u := *r.URL
				copy.URL = &u
				copy.URL.Path = jobs + strings.TrimPrefix(rest, p.ID+"~")
				s.proxyPeer(p, w, copy)
				return
			}
		}
	}
	// The preserved cross-server archive is coordinated by its library owner.
	// A link-specific unlink is explicit; browsing/status lists remain local.
	if r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, "/v1/archive-links/") && s.config.ArchiveCoordinator != "" {
		id := strings.TrimPrefix(r.URL.Path, "/v1/archive-links/")
		var found int
		if e := s.db.QueryRowContext(r.Context(), "SELECT COUNT(*) FROM fb_operations WHERE id=? AND kind='archiveLink'", id).Scan(&found); e != nil || found != 1 {
			fail(w, ErrUnsafe)
			return
		}
		for _, p := range s.config.Peers {
			if p.ID == s.config.ArchiveCoordinator {
				s.proxyPeer(p, w, r)
				return
			}
		}
		fail(w, ErrMount)
		return
	}
	if r.Method == http.MethodGet && r.URL.Path == "/v1/capabilities" {
		recorder := httptest.NewRecorder()
		local.ServeHTTP(recorder, r)
		var data map[string]any
		if recorder.Code != http.StatusOK || json.Unmarshal(recorder.Body.Bytes(), &data) != nil {
			fail(w, ErrMount)
			return
		}
		features, _ := data["features"].([]any)
		found := false
		for _, feature := range features {
			if feature == "peerTransfersV1" {
				found = true
			}
		}
		if !found {
			features = append(features, "peerTransfersV1")
		}
		data["features"] = features
		reply(w, http.StatusOK, data)
		return
	}
	local.ServeHTTP(w, r)
}
