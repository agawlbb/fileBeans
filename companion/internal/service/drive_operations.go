package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
)

func (s *Service) driveDo(ctx context.Context, method, path string, q url.Values, value any, connection string) (*http.Response, error) {
	if method != "GET" {
		if e := s.requireDriveWrite(connection); e != nil {
			return nil, e
		}
	}
	g, e := s.token(ctx, connection)
	if e != nil {
		return nil, e
	}
	var body io.Reader
	if value != nil {
		b, er := json.Marshal(value)
		if er != nil {
			return nil, er
		}
		body = bytes.NewReader(b)
	}
	r, e := http.NewRequestWithContext(ctx, method, "https://www.googleapis.com/drive/v3/"+path+"?"+q.Encode(), body)
	if e != nil {
		return nil, e
	}
	r.Header.Set("Authorization", "Bearer "+g.Access)
	if value != nil {
		r.Header.Set("Content-Type", "application/json")
	}
	response, e := googleHTTP.Do(r)
	if e != nil {
		return nil, e
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, driveFailure(response)
	}
	return response, nil
}
func (s *Service) driveJSON(ctx context.Context, method, path string, q url.Values, value any, connection string, result any) error {
	response, e := s.driveDo(ctx, method, path, q, value, connection)
	if e != nil {
		return e
	}
	defer response.Body.Close()
	if result == nil {
		_, e = io.Copy(io.Discard, io.LimitReader(response.Body, 4*1024*1024))
		return e
	}
	return json.NewDecoder(io.LimitReader(response.Body, 4*1024*1024)).Decode(result)
}
func metadataQuery() url.Values {
	return url.Values{"supportsAllDrives": {"true"}, "fields": {driveFields}}
}
func (s *Service) driveGenerateID(ctx context.Context, connection string) (string, error) {
	var value struct {
		IDs []string `json:"ids"`
	}
	e := s.driveJSON(ctx, "GET", "files/generateIds", url.Values{"count": {"1"}, "space": {"drive"}}, nil, connection, &value)
	if e != nil {
		return "", e
	}
	if len(value.IDs) != 1 || !driveID(value.IDs[0]) {
		return "", ErrUnsafe
	}
	return value.IDs[0], nil
}

var driveFilenamePublication sync.Mutex

func (s *Service) driveNameConflict(ctx context.Context, parent, name, exclude, connection string, directory ...bool) error {
	if !driveID(parent) || !validName(name) {
		return ErrUnsafe
	}
	q := url.Values{"q": {"trashed=false and '" + parent + "' in parents"}, "pageSize": {"100"}, "fields": {"nextPageToken,files(id,name,mimeType)"}, "supportsAllDrives": {"true"}, "includeItemsFromAllDrives": {"true"}}
	for {
		var result DriveList
		if e := s.driveJSON(ctx, "GET", "files", q, nil, connection, &result); e != nil {
			return e
		}
		for _, f := range result.Files {
			if f.ID != exclude && (f.Name == name || ((len(directory) == 0 || !directory[0]) && f.Mime != "application/vnd.google-apps.folder" && filenameKey(f.Name) == filenameKey(name))) {
				return ErrConflict
			}
		}
		if result.Next == "" {
			return nil
		}
		q.Set("pageToken", result.Next)
	}
}
func (s *Service) driveParent(ctx context.Context, id, connection string) (DriveItem, error) {
	item, e := s.driveMeta(ctx, id, connection)
	if e != nil {
		return item, e
	}
	if item.Mime != "application/vnd.google-apps.folder" || item.Trashed || !item.Capabilities.AddChildren {
		return item, ErrDrivePermission
	}
	return item, nil
}
func isDriveRoot(d DriveItem) bool {
	return d.Mime == "application/vnd.google-apps.folder" && (len(d.Parents) == 0 || d.ID == d.DriveID)
}
func operationProperty(jobID, path string) string {
	h := sha256.Sum256([]byte(jobID + ":" + path))
	return hex.EncodeToString(h[:])
}
func (s *Service) findDriveOperation(ctx context.Context, parent, operation, connection string) (DriveItem, error) {
	var result DriveList
	q := url.Values{"q": {"trashed=false and '" + parent + "' in parents and appProperties has { key='filebeansOperation' and value='" + operation + "' }"}, "fields": {"files(" + driveFields + ")"}, "pageSize": {"2"}, "supportsAllDrives": {"true"}, "includeItemsFromAllDrives": {"true"}}
	if e := s.driveJSON(ctx, "GET", "files", q, nil, connection, &result); e != nil {
		return DriveItem{}, e
	}
	if len(result.Files) > 1 {
		return DriveItem{}, ErrConflict
	}
	if len(result.Files) == 0 {
		return DriveItem{}, os.ErrNotExist
	}
	return result.Files[0], nil
}

type DriveMutation struct {
	ID      string `json:"id"`
	Action  string `json:"action"`
	FileID  string `json:"fileId,omitempty"`
	Parent  string `json:"parent,omitempty"`
	Name    string `json:"name,omitempty"`
	Scope   string `json:"scope,omitempty"`
	Version string `json:"version,omitempty"`
}
type driveMutationRecord struct {
	ID            string        `json:"id"`
	Request       DriveMutation `json:"request"`
	Connection    string        `json:"connection"`
	State         string        `json:"state"`
	TargetID      string        `json:"targetId,omitempty"`
	SnapshotReady bool          `json:"snapshotReady"`
	Snapshot      []string      `json:"snapshot,omitempty"`
	Deleted       int           `json:"deleted"`
	Result        DriveItem     `json:"result"`
}

func (s *Service) driveMutation(ctx context.Context, request DriveMutation, key string) (DriveItem, error) {
	return s.driveMutationForConnection(ctx, request, key, "")
}

func (s *Service) driveMutationForConnection(ctx context.Context, request DriveMutation, key, connection string) (DriveItem, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e := s.requireDriveWrite(connection); e != nil {
		return DriveItem{}, e
	}
	var record driveMutationRecord
	e := s.existing(key, "driveMutation", &record)
	if e == nil {
		if connection != "" && record.Connection != connection {
			return DriveItem{}, ErrDriveAccount
		}
		a, _ := json.Marshal(request)
		b, _ := json.Marshal(record.Request)
		if !bytes.Equal(a, b) {
			return DriveItem{}, ErrConflict
		}
		if e = s.requireDriveWrite(record.Connection); e != nil {
			return DriveItem{}, e
		}
		if record.State == "completed" {
			return record.Result, nil
		}
	} else if errors.Is(e, sql.ErrNoRows) {
		g, e := s.googleForConnection(connection)
		if e != nil {
			return DriveItem{}, e
		}
		if connection != "" && g.Connection != connection {
			return DriveItem{}, ErrDriveAccount
		}
		record = driveMutationRecord{ID: ID(), Request: request, Connection: g.Connection, State: "pending"}
		if e = s.insert(record.ID, "driveMutation", record.State, key, record); e != nil {
			return DriveItem{}, e
		}
	} else {
		return DriveItem{}, e
	}
	result, e := s.performDriveMutation(ctx, &record)
	if e != nil {
		return DriveItem{}, e
	}
	record.Result = result
	record.State = "completed"
	return result, s.save(record.ID, "driveMutation", record.State, record)
}
func (s *Service) performDriveMutation(ctx context.Context, record *driveMutationRecord) (DriveItem, error) {
	driveFilenamePublication.Lock()
	defer driveFilenamePublication.Unlock()
	req := record.Request
	connection := record.Connection
	if req.Action == "empty" {
		if !record.SnapshotReady {
			snapshot := []string{}
			page := ""
			for {
				list, e := s.driveTrash(ctx, req.Scope, page, connection)
				if e != nil {
					return DriveItem{}, e
				}
				for _, item := range list.Files {
					if !item.Capabilities.Delete {
						return DriveItem{}, ErrDrivePermission
					}
					snapshot = append(snapshot, item.ID)
				}
				page = list.Next
				if page == "" {
					break
				}
			}
			record.Snapshot = snapshot
			record.SnapshotReady = true
			if e := s.save(record.ID, "driveMutation", record.State, record); e != nil {
				return DriveItem{}, e
			}
		}
		for record.Deleted < len(record.Snapshot) {
			id := record.Snapshot[record.Deleted]
			item, e := s.driveMeta(ctx, id, connection)
			if e == nil {
				if !item.Trashed {
					return DriveItem{}, ErrConflict
				}
				if !item.Capabilities.Delete {
					return DriveItem{}, ErrDrivePermission
				}
				e = s.driveJSON(ctx, "DELETE", "files/"+id, url.Values{"supportsAllDrives": {"true"}}, nil, connection, nil)
			}
			if e != nil && !errors.Is(e, os.ErrNotExist) {
				return DriveItem{}, e
			}
			record.Deleted++
			if e = s.save(record.ID, "driveMutation", record.State, record); e != nil {
				return DriveItem{}, e
			}
		}
		return DriveItem{}, nil
	}
	if req.Action == "folder" {
		if !validName(req.Name) || !driveID(req.Parent) {
			return DriveItem{}, ErrUnsafe
		}
		if _, e := s.driveParent(ctx, req.Parent, connection); e != nil {
			return DriveItem{}, e
		}
		if record.TargetID != "" {
			if item, e := s.driveMeta(ctx, record.TargetID, connection); e == nil {
				if item.Properties["filebeansOperation"] != record.ID {
					return DriveItem{}, ErrConflict
				}
				return item, nil
			} else if !errors.Is(e, os.ErrNotExist) {
				return DriveItem{}, e
			}
		} else {
			id, e := s.driveGenerateID(ctx, connection)
			if e != nil {
				return DriveItem{}, e
			}
			record.TargetID = id
			if e = s.save(record.ID, "driveMutation", record.State, record); e != nil {
				return DriveItem{}, e
			}
		}
		if e := s.driveNameConflict(ctx, req.Parent, req.Name, "", connection, true); e != nil {
			return DriveItem{}, e
		}
		var result DriveItem
		e := s.driveJSON(ctx, "POST", "files", metadataQuery(), map[string]any{"id": record.TargetID, "name": req.Name, "mimeType": "application/vnd.google-apps.folder", "parents": []string{req.Parent}, "appProperties": map[string]string{"filebeansOperation": record.ID}}, connection, &result)
		return result, e
	}
	if !driveID(req.FileID) || req.FileID == "root" {
		return DriveItem{}, ErrUnsafe
	}
	item, e := s.driveMeta(ctx, req.FileID, connection)
	if errors.Is(e, os.ErrNotExist) && req.Action == "purge" && record.State == "applying" {
		return DriveItem{}, nil
	}
	if e != nil {
		return DriveItem{}, e
	}
	if isDriveRoot(item) {
		return DriveItem{}, ErrDrivePermission
	}
	// Detect a completed PATCH after a lost response before comparing old versions.
	if record.State == "applying" {
		switch req.Action {
		case "rename":
			if item.Name == req.Name {
				return item, nil
			}
		case "move":
			if contains(item.Parents, req.Parent) {
				return item, nil
			}
		case "trash":
			if item.Trashed {
				return item, nil
			}
		case "restore":
			if !item.Trashed {
				return item, nil
			}
		}
	}
	if req.Version != "" && req.Version != item.Version {
		return DriveItem{}, ErrConflict
	}
	query := metadataQuery()
	patch := map[string]any{}
	switch req.Action {
	case "rename":
		if !item.Capabilities.Rename {
			return DriveItem{}, ErrDrivePermission
		}
		if !validName(req.Name) {
			return DriveItem{}, ErrUnsafe
		}
		for _, parent := range item.Parents {
			if e = s.driveNameConflict(ctx, parent, req.Name, item.ID, connection, item.Mime == "application/vnd.google-apps.folder"); e != nil {
				return DriveItem{}, e
			}
		}
		patch["name"] = req.Name
	case "move":
		if !driveID(req.Parent) || (!item.Capabilities.MoveWithin && !item.Capabilities.MoveOut) {
			return DriveItem{}, ErrDrivePermission
		}
		if _, e = s.driveParent(ctx, req.Parent, connection); e != nil {
			return DriveItem{}, e
		}
		seen := map[string]bool{}
		next := req.Parent
		for depth := 0; next != ""; depth++ {
			if next == item.ID || seen[next] || depth > 256 {
				return DriveItem{}, ErrUnsafe
			}
			seen[next] = true
			parent, e := s.driveMeta(ctx, next, connection)
			if e != nil {
				return DriveItem{}, e
			}
			next = ""
			if len(parent.Parents) > 0 {
				next = parent.Parents[0]
			}
		}
		if e = s.driveNameConflict(ctx, req.Parent, item.Name, item.ID, connection, item.Mime == "application/vnd.google-apps.folder"); e != nil {
			return DriveItem{}, e
		}
		query.Set("addParents", req.Parent)
		if len(item.Parents) > 0 {
			query.Set("removeParents", strings.Join(item.Parents, ","))
		}
	case "trash":
		if !item.Capabilities.Trash {
			return DriveItem{}, ErrDrivePermission
		}
		patch["trashed"] = true
	case "restore":
		if !item.Trashed || !item.Capabilities.Untrash {
			return DriveItem{}, ErrDrivePermission
		}
		for _, parent := range item.Parents {
			if e = s.driveNameConflict(ctx, parent, item.Name, item.ID, connection, item.Mime == "application/vnd.google-apps.folder"); e != nil {
				return DriveItem{}, e
			}
		}
		patch["trashed"] = false
	case "purge":
		if !item.Trashed || !item.Capabilities.Delete {
			return DriveItem{}, ErrDrivePermission
		}
	default:
		return DriveItem{}, ErrUnsafe
	}
	record.State = "applying"
	if e = s.save(record.ID, "driveMutation", record.State, record); e != nil {
		return DriveItem{}, e
	}
	if req.Action == "purge" {
		return DriveItem{}, s.driveJSON(ctx, "DELETE", "files/"+item.ID, url.Values{"supportsAllDrives": {"true"}}, nil, connection, nil)
	}
	var result DriveItem
	e = s.driveJSON(ctx, "PATCH", "files/"+item.ID, query, patch, connection, &result)
	return result, e
}
func contains(values []string, value string) bool {
	for _, v := range values {
		if v == value {
			return true
		}
	}
	return false
}
func (s *Service) driveTrash(ctx context.Context, scope, page, connection string) (DriveList, error) {
	query := url.Values{"q": {"trashed=true and 'me' in owners"}, "fields": {"nextPageToken,files(" + driveFields + ")"}, "pageSize": {"100"}, "supportsAllDrives": {"true"}, "includeItemsFromAllDrives": {"true"}, "orderBy": {"folder,name"}}
	if scope != "" && scope != "root" {
		if !driveID(scope) {
			return DriveList{}, ErrUnsafe
		}
		query.Set("q", "trashed=true")
		query.Set("corpora", "drive")
		query.Set("driveId", scope)
	}
	if page != "" {
		query.Set("pageToken", page)
	}
	var result DriveList
	e := s.driveJSON(ctx, "GET", "files", query, nil, connection, &result)
	return result, e
}
