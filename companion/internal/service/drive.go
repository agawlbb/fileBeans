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
	ClientID   string   `json:"client_id,omitempty"`
	Access     string   `json:"access_token"`
	Refresh    string   `json:"refresh_token"`
	Expires    int64    `json:"expires"`
	Connection string   `json:"connection"`
	Scopes     []string `json:"scopes,omitempty"`
	Account    string   `json:"account,omitempty"`
}
type DriveCapabilities struct {
	Download       bool `json:"canDownload"`
	Copy           bool `json:"canCopy"`
	Rename         bool `json:"canRename"`
	Edit           bool `json:"canEdit"`
	MoveWithin     bool `json:"canMoveItemWithinDrive"`
	MoveOut        bool `json:"canMoveItemOutOfDrive"`
	AddChildren    bool `json:"canAddChildren"`
	Trash          bool `json:"canTrash"`
	Untrash        bool `json:"canUntrash"`
	Delete         bool `json:"canDelete"`
	DeleteChildren bool `json:"canDeleteChildren"`
}
type DriveItem struct {
	SHA256            string            `json:"sha256Checksum,omitempty"`
	MD5               string            `json:"md5Checksum,omitempty"`
	ID                string            `json:"id"`
	Name              string            `json:"name"`
	Mime              string            `json:"mimeType"`
	Size              string            `json:"size"`
	Version           string            `json:"version"`
	Modified          string            `json:"modifiedTime"`
	Parents           []string          `json:"parents,omitempty"`
	DriveID           string            `json:"driveId,omitempty"`
	Trashed           bool              `json:"trashed"`
	ExplicitlyTrashed bool              `json:"explicitlyTrashed"`
	HasThumbnail      bool              `json:"hasThumbnail"`
	Thumbnail         string            `json:"thumbnailLink,omitempty"`
	WebURL            string            `json:"webViewLink,omitempty"`
	Properties        map[string]string `json:"appProperties,omitempty"`
	Capabilities      DriveCapabilities `json:"capabilities"`
}

const driveScope = "https://www.googleapis.com/auth/drive"

func (g Google) writable() bool {
	for _, scope := range g.Scopes {
		if scope == driveScope {
			return true
		}
	}
	return false
}
func (s *Service) requireDriveWrite(connection string) error {
	g, e := s.googleForConnection(connection)
	if e != nil {
		return e
	}
	if !g.writable() {
		return ErrDriveWrite
	}
	if connection != "" && g.Connection != connection {
		return ErrDriveAccount
	}
	return nil
}

type DriveList struct {
	Incomplete bool        `json:"incompleteSearch,omitempty"`
	Files      []DriveItem `json:"files"`
	Next       string      `json:"nextPageToken"`
}

// Google omits empty arrays; keep the app API stable for empty folders/trash.
func (d DriveList) MarshalJSON() ([]byte, error) {
	type value DriveList
	if d.Files == nil {
		d.Files = []DriveItem{}
	}
	return json.Marshal(value(d))
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
	unlock, lockError := s.lockGoogle(ctx)
	if lockError != nil {
		return Google{}, lockError
	}
	defer unlock()
	g, e := s.googleForConnection(connection)
	if e != nil {
		return g, e
	}
	if connection != "" && connection != g.Connection {
		return g, ErrDriveAccount
	}
	if g.Expires > time.Now().Unix()+60 {
		return g, nil
	}
	var reply tokenReply
	if strings.HasPrefix(connection, "native:") {
		values := url.Values{"grant_type": {"refresh_token"}, "refresh_token": {g.Refresh}, "client_id": {g.ClientID}}
		request, err := http.NewRequestWithContext(ctx, "POST", "https://oauth2.googleapis.com/token", strings.NewReader(values.Encode()))
		if err != nil {
			return g, err
		}
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		response, err := googleHTTP.Do(request)
		if err != nil {
			return g, err
		}
		defer response.Body.Close()
		if response.StatusCode != 200 {
			return g, ErrAuthorization
		}
		e = json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&reply)
		if reply.Access == "" {
			e = ErrAuthorization
		}
	} else {
		reply, e = s.tokenRequest(ctx, url.Values{"grant_type": {"refresh_token"}, "refresh_token": {g.Refresh}})
	}
	if e != nil {
		return g, e
	}
	g.Access = reply.Access
	if reply.Refresh != "" {
		g.Refresh = reply.Refresh
	}
	g.Expires = time.Now().Unix() + reply.Lifetime
	if reply.Scope != "" {
		g.Scopes = strings.Fields(reply.Scope)
	}
	if strings.HasPrefix(connection, "native:") {
		_, deadline, err := s.nativeGoogle(connection)
		if err != nil {
			return g, err
		}
		return g, s.saveNativeGoogle(connection, g, deadline)
	}
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

const driveFields = "sha256Checksum,md5Checksum,id,name,mimeType,size,version,modifiedTime,parents,driveId,trashed,explicitlyTrashed,hasThumbnail,thumbnailLink,webViewLink,appProperties,capabilities(canDownload,canCopy,canRename,canEdit,canMoveItemWithinDrive,canMoveItemOutOfDrive,canAddChildren,canTrash,canUntrash,canDelete,canDeleteChildren)"

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
	q := "trashed=false and not (appProperties has { key='filebeansStage' and value='true' }) and "
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
	params := url.Values{"q": {q}, "fields": {"incompleteSearch,nextPageToken,files(" + driveFields + ")"}, "pageSize": {"100"}, "orderBy": {"folder,name"}, "supportsAllDrives": {"true"}, "includeItemsFromAllDrives": {"true"}}
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
	return s.driveTree(ctx, id, connection, false)
}
func (s *Service) driveTree(ctx context.Context, id, connection string, withinDrive bool) ([]File, string, error) {
	top, err := s.driveMeta(ctx, id, connection)
	if err != nil {
		return nil, "", err
	}
	files, err := s.discoverDriveTree(ctx, top, connection, withinDrive)
	return files, top.Name, err
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
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	unlock, lockError := s.lockGoogle(ctx)
	if lockError != nil {
		return "", lockError
	}
	defer unlock()
	state := oauthState{State: ID(), Verifier: ID() + ID(), Expires: time.Now().Add(10 * time.Minute).Unix()}
	if e := privateJSON(filepath.Join(s.config.State, "oauth-state.json"), state); e != nil {
		return "", e
	}
	hash := sha256.Sum256([]byte(state.Verifier))
	q := url.Values{"client_id": {s.config.GoogleClientID}, "redirect_uri": {s.config.GoogleRedirect}, "response_type": {"code"}, "scope": {driveScope}, "access_type": {"offline"}, "prompt": {"consent"}, "state": {state.State}, "code_challenge": {base64.RawURLEncoding.EncodeToString(hash[:])}, "code_challenge_method": {"S256"}}
	return "https://accounts.google.com/o/oauth2/v2/auth?" + q.Encode(), nil
}
func (s *Service) callback(ctx context.Context, state, code string) error {
	unlock, lockError := s.lockGoogle(ctx)
	if lockError != nil {
		return lockError
	}
	defer unlock()
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
	grant := Google{Access: t.Access, Refresh: t.Refresh, Expires: time.Now().Unix() + t.Lifetime, Connection: ID(), Scopes: strings.Fields(t.Scope)}
	if grant.Refresh == "" || !grant.writable() {
		return ErrDriveWrite
	}
	// Use Google's opaque account identifier, never an embedded account/email.
	account, e := googleAccount(ctx, grant.Access)
	if e != nil {
		return e
	}
	grant.Account = account
	// Retain opaque identity nonces, never tokens, so explicitly reconnecting
	// an earlier account can resume/cancel its jobs after an account switch.
	identitiesPath := filepath.Join(s.config.State, "google-accounts.json")
	identities := map[string]string{}
	if data, er := os.ReadFile(identitiesPath); er == nil {
		if json.Unmarshal(data, &identities) != nil || identities == nil {
			return ErrAuthorization
		}
	} else if !errors.Is(er, os.ErrNotExist) {
		return er
	}
	if old, er := s.google(); er == nil {
		oldAccount := old.Account
		if oldAccount == "" {
			access := old.Access
			if old.Expires <= time.Now().Unix()+60 {
				if refreshed, er := s.tokenRequest(ctx, url.Values{"grant_type": {"refresh_token"}, "refresh_token": {old.Refresh}}); er == nil {
					access = refreshed.Access
				}
			}
			oldAccount, _ = googleAccount(ctx, access)
		}
		if oldAccount != "" && old.Connection != "" {
			identities[oldAccount] = old.Connection
		}
	}
	if known := identities[account]; known != "" {
		grant.Connection = known
	}
	identities[account] = grant.Connection
	if e = privateJSON(identitiesPath, identities); e != nil {
		return e
	}

	return privateJSON(filepath.Join(s.config.State, "google.json"), grant)
}

// Keep provider bodies private; expose only known categories to the worker.
type driveRetry struct {
	seconds   int64
	ambiguous bool
}

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
	if res.StatusCode == 404 || res.StatusCode == 410 {
		return os.ErrNotExist
	}
	retry := res.StatusCode == 429 || res.StatusCode >= 500
	for _, v := range payload.Error.Errors {
		if v.Reason == "rateLimitExceeded" || v.Reason == "userRateLimitExceeded" || v.Reason == "downloadQuotaExceeded" {
			retry = true
		}
	}
	if !retry {
		if res.StatusCode == 403 {
			return ErrDrivePermission
		}
		if res.StatusCode == 409 {
			return ErrConflict
		}
		return ErrUnsupported
	}
	seconds, _ := strconv.ParseInt(res.Header.Get("Retry-After"), 10, 64)
	if seconds == 0 {
		if at, e := http.ParseTime(res.Header.Get("Retry-After")); e == nil {
			seconds = int64(time.Until(at).Seconds())
		}
	}
	return driveRetry{seconds: max(5, min(seconds, 86400)), ambiguous: res.StatusCode >= 500}
}

func googleAccount(ctx context.Context, access string) (string, error) {
	r, e := http.NewRequestWithContext(ctx, "GET", "https://www.googleapis.com/drive/v3/about?fields=user(permissionId)", nil)
	if e != nil {
		return "", e
	}
	r.Header.Set("Authorization", "Bearer "+access)
	response, e := googleHTTP.Do(r)
	if e != nil {
		return "", e
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		return "", ErrAuthorization
	}
	var value struct {
		User struct {
			ID string `json:"permissionId"`
		} `json:"user"`
	}
	if e = json.NewDecoder(io.LimitReader(response.Body, 65536)).Decode(&value); e != nil {
		return "", e
	}
	if value.User.ID == "" {
		return "", ErrAuthorization
	}
	return value.User.ID, nil
}
