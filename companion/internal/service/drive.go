package service

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type Google struct {
	Access     string `json:"access_token"`
	Refresh    string `json:"refresh_token"`
	Expires    int64  `json:"expires"`
	Connection string `json:"connection"`
}
type DriveItem struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Mime         string `json:"mimeType"`
	Size         string `json:"size"`
	Version      string `json:"version"`
	Modified     string `json:"modifiedTime"`
	Capabilities struct {
		Download bool `json:"canDownload"`
	} `json:"capabilities"`
}
type DriveList struct {
	Files []DriveItem `json:"files"`
	Next  string      `json:"nextPageToken"`
}

var googleHTTP = &http.Client{Timeout: 2 * time.Minute, CheckRedirect: func(r *http.Request, via []*http.Request) error { return http.ErrUseLastResponse }}

func privateJSON(path string, v any) error {
	b, e := json.Marshal(v)
	if e != nil {
		return e
	}
	f, e := os.CreateTemp(filepath.Dir(path), ".credential-")
	if e != nil {
		return e
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if e = f.Chmod(0600); e == nil {
		_, e = f.Write(b)
	}
	if e == nil {
		e = f.Sync()
	}
	ce := f.Close()
	if e == nil {
		e = ce
	}
	if e != nil {
		return e
	}
	return os.Rename(tmp, path)
}
func (s *Service) google() (Google, error) {
	var g Google
	b, e := os.ReadFile(filepath.Join(s.config.State, "google.json"))
	if e != nil {
		return g, ErrAuthorization
	}
	e = json.Unmarshal(b, &g)
	if e != nil || g.Refresh == "" {
		return g, ErrAuthorization
	}
	return g, nil
}
func (s *Service) token(ctx context.Context, connection string) (Google, error) {
	s.driveMu.Lock()
	defer s.driveMu.Unlock()
	g, e := s.google()
	if e != nil {
		return g, e
	}
	if connection != "" && connection != g.Connection {
		return g, ErrAuthorization
	}
	if g.Expires > time.Now().Unix()+60 {
		return g, nil
	}
	reply, e := s.tokenRequest(ctx, url.Values{"grant_type": {"refresh_token"}, "refresh_token": {g.Refresh}})
	if e != nil {
		return g, e
	}
	g.Access = reply.Access
	if reply.Refresh != "" {
		g.Refresh = reply.Refresh
	}
	g.Expires = time.Now().Unix() + reply.Lifetime
	return g, privateJSON(filepath.Join(s.config.State, "google.json"), g)
}

type tokenReply struct {
	Access   string `json:"access_token"`
	Refresh  string `json:"refresh_token"`
	Lifetime int64  `json:"expires_in"`
	Scope    string `json:"scope"`
}

func (s *Service) tokenRequest(ctx context.Context, values url.Values) (tokenReply, error) {
	var t tokenReply
	values.Set("client_id", s.config.GoogleClientID)
	values.Set("client_secret", s.config.GoogleClientSecret)
	r, e := http.NewRequestWithContext(ctx, "POST", "https://oauth2.googleapis.com/token", strings.NewReader(values.Encode()))
	if e != nil {
		return t, e
	}
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	res, e := googleHTTP.Do(r)
	if e != nil {
		return t, e
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return t, ErrAuthorization
	}
	e = json.NewDecoder(io.LimitReader(res.Body, 1024*1024)).Decode(&t)
	if e != nil || t.Access == "" {
		return t, ErrAuthorization
	}
	return t, nil
}
func (s *Service) driveRequest(ctx context.Context, path string, q url.Values, connection string) (*http.Response, error) {
	g, e := s.token(ctx, connection)
	if e != nil {
		return nil, e
	}
	r, e := http.NewRequestWithContext(ctx, "GET", "https://www.googleapis.com/drive/v3/"+path+"?"+q.Encode(), nil)
	if e != nil {
		return nil, e
	}
	r.Header.Set("Authorization", "Bearer "+g.Access)
	res, e := googleHTTP.Do(r)
	if e != nil {
		return nil, e
	}
	if res.StatusCode != 200 {
		return nil, driveFailure(res)
	}
	return res, nil
}

const driveFields = "id,name,mimeType,size,version,modifiedTime,capabilities(canDownload)"

func (s *Service) driveMeta(ctx context.Context, id, connection string) (DriveItem, error) {
	var d DriveItem
	if !driveID(id) {
		return d, ErrUnsafe
	}
	r, e := s.driveRequest(ctx, "files/"+id, url.Values{"fields": {driveFields}, "supportsAllDrives": {"true"}}, connection)
	if e != nil {
		return d, e
	}
	defer r.Body.Close()
	e = json.NewDecoder(io.LimitReader(r.Body, 1024*1024)).Decode(&d)
	return d, e
}
func driveID(id string) bool {
	if len(id) == 0 || len(id) > 200 {
		return false
	}
	for _, c := range id {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-') {
			return false
		}
	}
	return true
}
func (s *Service) driveList(ctx context.Context, folder, page, connection string) (DriveList, error) {
	var d DriveList
	q := "trashed=false and "
	switch folder {
	case "shared":
		q += "sharedWithMe=true"
	case "root":
		q += "'root' in parents"
	default:
		if !driveID(folder) {
			return d, ErrUnsafe
		}
		q += "'" + folder + "' in parents"
	}
	params := url.Values{"q": {q}, "fields": {"nextPageToken,files(" + driveFields + ")"}, "pageSize": {"100"}, "orderBy": {"folder,name"}, "supportsAllDrives": {"true"}, "includeItemsFromAllDrives": {"true"}}
	if page != "" {
		params.Set("pageToken", page)
	}
	r, e := s.driveRequest(ctx, "files", params, connection)
	if e != nil {
		return d, e
	}
	defer r.Body.Close()
	e = json.NewDecoder(io.LimitReader(r.Body, 4*1024*1024)).Decode(&d)
	return d, e
}
func (s *Service) driveManifest(ctx context.Context, id, connection string) ([]File, string, error) {
	var files []File
	seen := map[string]bool{}
	top, e := s.driveMeta(ctx, id, connection)
	if e != nil {
		return nil, "", e
	}
	var walk func(DriveItem, string) error
	walk = func(d DriveItem, path string) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if !validName(d.Name) {
			return ErrConflict
		}
		if seen[d.ID] {
			return ErrUnsafe
		}
		seen[d.ID] = true
		dir := d.Mime == "application/vnd.google-apps.folder"
		if !dir && (strings.HasPrefix(d.Mime, "application/vnd.google-apps.") || !d.Capabilities.Download) {
			return ErrUnsupported
		}
		size := int64(0)
		if !dir {
			var e error
			size, e = strconv.ParseInt(d.Size, 10, 64)
			if e != nil || size < 0 {
				return ErrUnsafe
			}
		}
		files = append(files, File{Source: "drive", From: d.ID, To: path, Size: size, Version: d.Version, Directory: dir})
		if dir {
			names := map[string]bool{}
			page := ""
			for {
				list, e := s.driveList(ctx, d.ID, page, connection)
				if e != nil {
					return e
				}
				for _, child := range list.Files {
					if names[child.Name] {
						return ErrConflict
					}
					names[child.Name] = true
					if e = walk(child, filepath.Join(path, child.Name)); e != nil {
						return e
					}
				}
				page = list.Next
				if page == "" {
					break
				}
			}
		}
		return nil
	}
	e = walk(top, top.Name)
	return files, top.Name, e
}

// A streaming request has an idle deadline rather than a total duration limit.
// Large files can download for hours without a forced restart every two minutes.
type driveBody struct {
	io.ReadCloser
	timer  *time.Timer
	cancel context.CancelFunc
}

func (b *driveBody) Read(p []byte) (int, error) {
	n, e := b.ReadCloser.Read(p)
	if n > 0 {
		b.timer.Reset(90 * time.Second)
	}
	return n, e
}
func (b *driveBody) Close() error { b.timer.Stop(); b.cancel(); return b.ReadCloser.Close() }
func (s *Service) driveContent(ctx context.Context, id string, offset int64, connection string) (io.ReadCloser, error) {
	g, e := s.token(ctx, connection)
	if e != nil {
		return nil, e
	}
	downloadCtx, cancel := context.WithCancel(ctx)
	timer := time.AfterFunc(90*time.Second, cancel)
	cleanup := func() { timer.Stop(); cancel() }
	r, e := http.NewRequestWithContext(downloadCtx, "GET", "https://www.googleapis.com/drive/v3/files/"+id+"?alt=media&supportsAllDrives=true", nil)
	if e != nil {
		cleanup()
		return nil, e
	}
	r.Header.Set("Authorization", "Bearer "+g.Access)
	r.Header.Set("Accept-Encoding", "identity")
	if offset > 0 {
		r.Header.Set("Range", fmt.Sprintf("bytes=%d-", offset))
	}
	client := *googleHTTP
	client.Timeout = 0
	res, e := client.Do(r)
	if e != nil {
		cleanup()
		return nil, e
	}
	if res.StatusCode != 200 && res.StatusCode != 206 {
		e = driveFailure(res)
		cleanup()
		return nil, e
	}
	if offset > 0 && (res.StatusCode != 206 || !strings.HasPrefix(res.Header.Get("Content-Range"), fmt.Sprintf("bytes %d-", offset))) {
		res.Body.Close()
		cleanup()
		return nil, errors.New("Drive range unavailable")
	}
	return &driveBody{ReadCloser: res.Body, timer: timer, cancel: cancel}, nil
}

type oauthState struct {
	State    string `json:"state"`
	Verifier string `json:"verifier"`
	Expires  int64  `json:"expires"`
}

func (s *Service) authURL() (string, error) {
	if s.config.GoogleClientID == "" {
		return "", errors.New("Google OAuth client is not configured")
	}
	s.driveMu.Lock()
	defer s.driveMu.Unlock()
	state := oauthState{State: ID(), Verifier: ID() + ID(), Expires: time.Now().Add(10 * time.Minute).Unix()}
	if e := privateJSON(filepath.Join(s.config.State, "oauth-state.json"), state); e != nil {
		return "", e
	}
	hash := sha256.Sum256([]byte(state.Verifier))
	q := url.Values{"client_id": {s.config.GoogleClientID}, "redirect_uri": {s.config.GoogleRedirect}, "response_type": {"code"}, "scope": {"https://www.googleapis.com/auth/drive.readonly"}, "access_type": {"offline"}, "prompt": {"consent"}, "state": {state.State}, "code_challenge": {base64.RawURLEncoding.EncodeToString(hash[:])}, "code_challenge_method": {"S256"}}
	return "https://accounts.google.com/o/oauth2/v2/auth?" + q.Encode(), nil
}
func (s *Service) callback(ctx context.Context, state, code string) error {
	s.driveMu.Lock()
	defer s.driveMu.Unlock()
	var saved oauthState
	path := filepath.Join(s.config.State, "oauth-state.json")
	b, e := os.ReadFile(path)
	if e != nil {
		return ErrAuthorization
	}
	if json.Unmarshal(b, &saved) != nil || saved.Expires < time.Now().Unix() || saved.State != state || state == "" || code == "" {
		return ErrAuthorization
	}
	if e = os.Remove(path); e != nil {
		return e
	}
	t, e := s.tokenRequest(ctx, url.Values{"grant_type": {"authorization_code"}, "code": {code}, "redirect_uri": {s.config.GoogleRedirect}, "code_verifier": {saved.Verifier}})
	if e != nil {
		return e
	}
	if t.Refresh == "" || !strings.Contains(t.Scope, "https://www.googleapis.com/auth/drive.readonly") {
		return ErrAuthorization
	}
	return privateJSON(filepath.Join(s.config.State, "google.json"), Google{Access: t.Access, Refresh: t.Refresh, Expires: time.Now().Unix() + t.Lifetime, Connection: ID()})
}

// Keep provider bodies private; expose only known categories to the worker.
type driveRetry struct{ seconds int64 }

func (e driveRetry) Error() string { return "Drive temporarily unavailable" }
func driveFailure(res *http.Response) error {
	defer res.Body.Close()
	var payload struct {
		Error struct {
			Errors []struct {
				Reason string `json:"reason"`
			} `json:"errors"`
		} `json:"error"`
	}
	_ = json.NewDecoder(io.LimitReader(res.Body, 65536)).Decode(&payload)
	if res.StatusCode == 401 {
		return ErrAuthorization
	}
	if res.StatusCode == 404 {
		return os.ErrNotExist
	}
	retry := res.StatusCode == 429 || res.StatusCode >= 500
	for _, v := range payload.Error.Errors {
		if v.Reason == "rateLimitExceeded" || v.Reason == "userRateLimitExceeded" || v.Reason == "downloadQuotaExceeded" {
			retry = true
		}
	}
	if !retry {
		return ErrUnsupported
	}
	seconds, _ := strconv.ParseInt(res.Header.Get("Retry-After"), 10, 64)
	if seconds == 0 {
		if at, e := http.ParseTime(res.Header.Get("Retry-After")); e == nil {
			seconds = int64(time.Until(at).Seconds())
		}
	}
	return driveRetry{seconds: max(5, min(seconds, 86400))}
}
