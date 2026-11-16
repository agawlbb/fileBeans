package service

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

func (s *Service) driveRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/drive/files/{id}", func(w http.ResponseWriter, r *http.Request) {
		item, e := s.driveMeta(r.Context(), r.PathValue("id"), "")
		if e != nil {
			fail(w, e)
			return
		}
		item.Thumbnail = ""
		reply(w, 200, item)
	})
	mux.HandleFunc("POST /v1/drive/mutations", func(w http.ResponseWriter, r *http.Request) {
		var req DriveMutation
		if e := body(r, &req); e != nil {
			fail(w, e)
			return
		}
		if key(r) == "" {
			fail(w, ErrUnsafe)
			return
		}
		if (req.Action == "purge" || req.Action == "empty") && r.Header.Get("X-Confirm-Permanent") != "yes" {
			fail(w, ErrUnsafe)
			return
		}
		result, e := s.driveMutation(r.Context(), req, key(r))
		if e != nil {
			fail(w, e)
			return
		}
		result.Thumbnail = ""
		reply(w, 200, result)
	})
	mux.HandleFunc("GET /v1/drive/trash", func(w http.ResponseWriter, r *http.Request) {
		result, e := s.driveTrash(r.Context(), r.URL.Query().Get("scope"), r.URL.Query().Get("page"), "")
		if e != nil {
			fail(w, e)
			return
		}
		for i := range result.Files {
			result.Files[i].Thumbnail = ""
		}
		reply(w, 200, result)
	})
	mux.HandleFunc("GET /v1/drive/files/{id}/thumbnail", s.driveThumbnail)
	mux.HandleFunc("GET /v1/drive/files/{id}/content", s.driveServeContent)
	s.uploadRoutes(mux)
}
func (s *Service) driveThumbnail(w http.ResponseWriter, r *http.Request) {
	item, e := s.driveMeta(r.Context(), r.PathValue("id"), "")
	if e != nil {
		fail(w, e)
		return
	}
	if item.Mime == "application/vnd.google-apps.folder" || item.Thumbnail == "" {
		http.NotFound(w, r)
		return
	}
	address, e := url.Parse(item.Thumbnail)
	if e != nil || !googleAssetURL(address) {
		fail(w, ErrUnsafe)
		return
	}
	grant, e := s.token(r.Context(), "")
	if e != nil {
		fail(w, e)
		return
	}
	request, e := http.NewRequestWithContext(r.Context(), "GET", address.String(), nil)
	if e != nil {
		fail(w, e)
		return
	}
	request.Header.Set("Authorization", "Bearer "+grant.Access)
	client := *googleHTTP
	client.CheckRedirect = func(next *http.Request, via []*http.Request) error {
		if len(via) >= 3 || !googleAssetURL(next.URL) {
			return ErrUnsafe
		}
		next.Header.Set("Authorization", "Bearer "+grant.Access)
		return nil
	}
	response, e := client.Do(request)
	if e != nil {
		fail(w, e)
		return
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		fail(w, ErrUnsupported)
		return
	}
	mime := response.Header.Get("Content-Type")
	if !strings.HasPrefix(mime, "image/") {
		fail(w, ErrUnsupported)
		return
	}
	data, e := io.ReadAll(io.LimitReader(response.Body, 8*1024*1024+1))
	if e != nil {
		fail(w, e)
		return
	}
	if len(data) > 8*1024*1024 {
		fail(w, ErrUnsupported)
		return
	}
	w.Header().Set("Content-Type", mime)
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	w.Write(data)
}
func googleAssetURL(address *url.URL) bool {
	if address.Scheme != "https" || address.User != nil || address.Fragment != "" || (address.Port() != "" && address.Port() != "443") {
		return false
	}
	host := strings.ToLower(address.Hostname())
	return host == "www.googleapis.com" || host == "googleusercontent.com" || strings.HasSuffix(host, ".googleusercontent.com") || host == "google.com" || strings.HasSuffix(host, ".google.com")
}
func (s *Service) driveServeContent(w http.ResponseWriter, r *http.Request) {
	item, e := s.driveMeta(r.Context(), r.PathValue("id"), "")
	if e != nil {
		fail(w, e)
		return
	}
	if !item.Capabilities.Download || strings.HasPrefix(item.Mime, "application/vnd.google-apps.") {
		fail(w, ErrUnsupported)
		return
	}
	size, e := strconv.ParseInt(item.Size, 10, 64)
	if e != nil || size < 0 {
		fail(w, ErrUnsupported)
		return
	}
	w.Header().Set("Accept-Ranges", "bytes")
	w.Header().Set("Content-Type", item.Mime)
	if r.Method == "HEAD" {
		w.Header().Set("Content-Length", item.Size)
		return
	}
	requested := r.Header.Get("Range")
	if requested != "" {
		if !validByteRange(requested, size) {
			w.Header().Set("Content-Range", "bytes */"+item.Size)
			w.WriteHeader(416)
			return
		}
	}
	response, e := s.driveRange(r.Context(), item.ID, requested, "")
	if e != nil {
		fail(w, e)
		return
	}
	defer response.Body.Close()
	for _, header := range []string{"Content-Type", "Content-Length", "Content-Range"} {
		if value := response.Header.Get(header); value != "" {
			w.Header().Set(header, value)
		}
	}
	w.WriteHeader(response.StatusCode)
	_, _ = io.Copy(w, response.Body)
}
func validByteRange(value string, size int64) bool {
	if !strings.HasPrefix(value, "bytes=") || strings.Contains(value, ",") {
		return false
	}
	parts := strings.Split(strings.TrimPrefix(value, "bytes="), "-")
	if len(parts) != 2 {
		return false
	}
	from, e := strconv.ParseInt(parts[0], 10, 64)
	if e != nil || from < 0 || from >= size {
		return false
	}
	if parts[1] == "" {
		return true
	}
	to, e := strconv.ParseInt(parts[1], 10, 64)
	return e == nil && to >= from
}
func (s *Service) driveRange(ctx context.Context, id, requested, connection string) (*http.Response, error) {
	if !driveID(id) {
		return nil, ErrUnsafe
	}
	g, e := s.token(ctx, connection)
	if e != nil {
		return nil, e
	}
	request, e := http.NewRequestWithContext(ctx, "GET", "https://www.googleapis.com/drive/v3/files/"+id+"?alt=media&supportsAllDrives=true", nil)
	if e != nil {
		return nil, e
	}
	request.Header.Set("Authorization", "Bearer "+g.Access)
	request.Header.Set("Accept-Encoding", "identity")
	if requested != "" {
		request.Header.Set("Range", requested)
	}
	client := *googleHTTP
	client.Timeout = 0
	response, e := client.Do(request)
	if e != nil {
		return nil, e
	}
	if response.StatusCode != 200 && response.StatusCode != 206 {
		return nil, driveFailure(response)
	}
	if requested != "" && response.StatusCode != 206 {
		response.Body.Close()
		return nil, ErrUnsupported
	}
	if response.StatusCode == 206 && response.Header.Get("Content-Range") == "" {
		response.Body.Close()
		return nil, ErrUnsupported
	}
	return response, nil
}
