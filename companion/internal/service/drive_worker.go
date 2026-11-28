package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

func (s *Service) executeDrive(ctx context.Context, j *Job) error {
	if e := s.guardArchiveJob(j.Request); e != nil {
		return e
	}
	if e := s.checkpoint(j); e != nil {
		return e
	}
	if e := s.requireDriveWrite(j.Connection); e != nil {
		return e
	}
	if len(j.Manifest) == 0 {
		j.State = "discovering"
		if e := s.checkpoint(j); e != nil {
			return e
		}
		if e := s.discover(ctx, j); e != nil {
			return e
		}
	}
	j.State = "running"
	j.Error = ""
	if e := s.checkpoint(j); e != nil {
		return e
	}
	destination, e := s.driveParent(ctx, j.Request.Destination.Path, j.Connection)
	if e != nil {
		return e
	}
	// Reconcile publication before touching staging contents after a restart.
	for i := range j.Manifest {
		f := &j.Manifest[i]
		if filepath.Dir(f.To) != "." || f.TargetID == "" {
			continue
		}
		if contains(j.Published, f.To) {
			continue
		}
		if j.Current == "publish:"+f.To {
			item, e := s.driveMeta(ctx, f.TargetID, j.Connection)
			if e != nil {
				return e
			}
			if contains(item.Parents, destination.ID) && item.Name == f.To {
				if item.Properties["filebeansOperation"] != operationProperty(j.ID, f.To) {
					return ErrConflict
				}
				j.Published = append(j.Published, f.To)
				if e = s.checkpoint(j); e != nil {
					return e
				}
			}
		}
	}
	// A crash after staging cleanup must not recreate a deleted generated ID.
	allPublished := len(j.Manifest) > 0
	for _, f := range j.Manifest {
		if filepath.Dir(f.To) == "." && !contains(j.Published, f.To) {
			allPublished = false
		}
	}
	if allPublished {
		return s.cleanupDriveStage(ctx, j)
	}
	if j.RemoteStage == "" {
		id, e := s.driveGenerateID(ctx, j.Connection)
		if e != nil {
			return e
		}
		j.RemoteStage = id
		if e = s.checkpoint(j); e != nil {
			return e
		}
	}
	if _, e = s.driveMeta(ctx, j.RemoteStage, j.Connection); errors.Is(e, os.ErrNotExist) {
		var created DriveItem
		e = s.driveJSON(ctx, "POST", "files", metadataQuery(), map[string]any{"id": j.RemoteStage, "name": "fileBeans transfer " + j.ID, "mimeType": "application/vnd.google-apps.folder", "parents": []string{destination.ID}, "appProperties": map[string]string{"filebeansStage": "true", "filebeansOperation": j.ID}}, j.Connection, &created)
		if e != nil {
			return e
		}
	} else if e != nil {
		return e
	}
	stage, e := s.driveMeta(ctx, j.RemoteStage, j.Connection)
	if e != nil {
		return e
	}
	if stage.Properties["filebeansOperation"] != j.ID || stage.Properties["filebeansStage"] != "true" || !contains(stage.Parents, destination.ID) {
		return ErrConflict
	}
	parents := map[string]string{".": j.RemoteStage}
	for i := range j.Manifest {
		f := &j.Manifest[i]
		if f.Directory && f.TargetID != "" {
			parents[f.To] = f.TargetID
		}
	}
	for i := range j.Manifest {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		f := &j.Manifest[i]
		top := strings.Split(f.To, "/")[0]
		if contains(j.Published, top) {
			continue
		}
		parent := parents[filepath.Dir(f.To)]
		if parent == "" {
			return ErrUnsafe
		}
		j.Current = f.To
		if e = s.checkpoint(j); e != nil {
			return e
		}
		if e = s.ensureDriveCopyItem(ctx, j, f, parent); e != nil {
			return e
		}
		if f.Directory {
			parents[f.To] = f.TargetID
		}
		if !f.Completed {
			f.Completed = true
			j.ItemsDone++
			if e = s.checkpoint(j); e != nil {
				return e
			}
		}
	}
	if len(j.PeerPlan) > 0 {
		for _, p := range j.PeerPlan {
			current, e := s.peerSnapshot(ctx, p.Item)
			if e != nil {
				return e
			}
			if !samePeerFiles(p.Files, current) {
				return ErrConflict
			}
		}
		for _, f := range j.Manifest {
			if !f.Directory {
				m, e := s.driveMeta(ctx, f.TargetID, j.Connection)
				if e != nil {
					return e
				}
				if e = verifyPeerDriveFile(f, m); e != nil {
					return e
				}
			}
		}
	}
	for i := range j.Manifest {
		f := &j.Manifest[i]
		if filepath.Dir(f.To) != "." || contains(j.Published, f.To) {
			continue
		}

		j.Current = "publish:" + f.To
		if e = s.checkpoint(j); e != nil {
			return e
		}
		driveFilenamePublication.Lock()
		e = s.driveNameConflict(ctx, destination.ID, filepath.Base(f.To), f.TargetID, j.Connection, f.Directory)
		if e == nil {
			var published DriveItem
			e = s.driveJSON(ctx, "PATCH", "files/"+f.TargetID, mergeQuery(metadataQuery(), url.Values{"addParents": {destination.ID}, "removeParents": {j.RemoteStage}}), map[string]any{"name": filepath.Base(f.To)}, j.Connection, &published)
		}
		driveFilenamePublication.Unlock()
		if e != nil {
			return e
		}
		j.Published = append(j.Published, f.To)
		if e = s.checkpoint(j); e != nil {
			return e
		}
	}
	return s.cleanupDriveStage(ctx, j)
}
func mergeQuery(a, b url.Values) url.Values {
	for key, values := range b {
		a[key] = values
	}
	return a
}
func (s *Service) ensureDriveCopyItem(ctx context.Context, j *Job, f *File, parent string) error {
	operation := operationProperty(j.ID, f.To)
	if f.TargetID != "" {
		item, e := s.driveMeta(ctx, f.TargetID, j.Connection)
		if e == nil {
			if item.Properties["filebeansOperation"] != operation || !contains(item.Parents, parent) {
				return ErrConflict
			}
			if f.Directory {
				return nil
			}
			if f.Source == "drive" || item.Size == strconv.FormatInt(f.Size, 10) {
				if e = s.verifyCopySource(ctx, j, f); e != nil {
					return e
				}
				j.Done += f.Size - f.Done
				f.Done = f.Size
				return nil
			}
			return ErrConflict
		}
		if !errors.Is(e, os.ErrNotExist) {
			return e
		}
	}
	native := strings.HasPrefix(f.Mime, "application/vnd.google-apps.") && !f.Directory
	if f.Source == "drive" && native {
		if f.Attempted {
			item, e := s.findDriveOperation(ctx, parent, operation, j.Connection)
			if e == nil {
				if e = s.verifyCopySource(ctx, j, f); e != nil {
					return e
				}
				f.TargetID = item.ID
				j.Done += f.Size - f.Done
				f.Done = f.Size
				return s.checkpoint(j)
			}
			if errors.Is(e, os.ErrNotExist) {
				return ErrUncertain
			}
			return e
		}
	} else if f.TargetID == "" {
		id, e := s.driveGenerateID(ctx, j.Connection)
		if e != nil {
			return e
		}
		f.TargetID = id
		if e = s.checkpoint(j); e != nil {
			return e
		}
	}
	properties := map[string]string{"filebeansOperation": operation}
	metadata := map[string]any{"name": filepath.Base(f.To), "parents": []string{parent}, "appProperties": properties}
	if f.TargetID != "" {
		metadata["id"] = f.TargetID
	}
	if f.Directory {
		metadata["mimeType"] = "application/vnd.google-apps.folder"
		var result DriveItem
		return s.driveJSON(ctx, "POST", "files", metadataQuery(), metadata, j.Connection, &result)
	}
	if e := s.verifyCopySource(ctx, j, f); e != nil {
		return e
	}
	if f.Source == "drive" {
		f.Attempted = true
		if e := s.checkpoint(j); e != nil {
			return e
		}
		var result DriveItem
		e := s.driveJSON(ctx, "POST", "files/"+f.From+"/copy", metadataQuery(), metadata, j.Connection, &result)
		if e != nil {
			var retry driveRetry
			if (errors.As(e, &retry) && !retry.ambiguous) || errors.Is(e, ErrDrivePermission) || errors.Is(e, ErrUnsupported) || errors.Is(e, ErrAuthorization) || errors.Is(e, os.ErrNotExist) {
				f.Attempted = false
				if saveErr := s.checkpoint(j); saveErr != nil {
					return saveErr
				}
			}
			return e
		}
		if result.ID == "" {
			return ErrUncertain
		}
		f.TargetID = result.ID
		if e = s.checkpoint(j); e != nil {
			return e
		}
		if e = s.verifyCopySource(ctx, j, f); e != nil {
			return e
		}
		j.Done += f.Size - f.Done
		f.Done = f.Size
		return nil
	}
	if f.Mime == "" {
		f.Mime = mime.TypeByExtension(filepath.Ext(f.To))
		if f.Mime == "" {
			f.Mime = "application/octet-stream"
		}
	}
	metadata["mimeType"] = f.Mime
	if f.Size == 0 {
		var result DriveItem
		e := s.driveJSON(ctx, "POST", "files", metadataQuery(), metadata, j.Connection, &result)
		if e != nil {
			return e
		}
		return s.verifyCopySource(ctx, j, f)
	}
	return s.uploadDriveFile(ctx, j, f, metadata)
}
func (s *Service) verifyCopySource(ctx context.Context, j *Job, f *File) error {
	if f.Directory {
		return nil
	}
	if f.Source == "drive" {
		item, e := s.driveMeta(ctx, f.From, j.Connection)
		if e != nil {
			return e
		}
		if item.Version != f.Version || item.Trashed {
			return ErrConflict
		}
		return nil
	}
	if s.peerFor(f.Source) != nil {
		return s.verifyPeerFile(ctx, *f)
	}
	root, e := s.root(f.Source)
	if e != nil {
		return e
	}
	defer root.Close()
	if e = safe(root, f.From); e != nil {
		return e
	}
	info, e := root.Stat(f.From)
	if e != nil {
		return e
	}
	if version(info) != f.Version {
		return ErrConflict
	}
	return nil
}
func validUploadURL(raw string) bool {
	value, e := url.Parse(raw)
	return e == nil && value.Scheme == "https" && value.Host == "www.googleapis.com" && value.User == nil && value.Fragment == "" && strings.HasPrefix(value.Path, "/upload/drive/v3/files")
}
func (s *Service) uploadRequest(ctx context.Context, method, address, connection string, payload []byte, contentRange, contentType string) (*http.Response, error) {
	if !validUploadURL(address) {
		return nil, ErrUnsafe
	}
	if e := s.requireDriveWrite(connection); e != nil {
		return nil, e
	}
	grant, e := s.token(ctx, connection)
	if e != nil {
		return nil, e
	}
	request, e := http.NewRequestWithContext(ctx, method, address, bytes.NewReader(payload))
	if e != nil {
		return nil, e
	}
	request.Header.Set("Authorization", "Bearer "+grant.Access)
	if contentRange != "" {
		request.Header.Set("Content-Range", contentRange)
	}
	if contentType != "" {
		request.Header.Set("Content-Type", contentType)
	}
	response, e := googleHTTP.Do(request)
	if e != nil {
		return nil, e
	}
	if (response.StatusCode >= 200 && response.StatusCode < 300) || response.StatusCode == 308 {
		return response, nil
	}
	return nil, driveFailure(response)
}
func (s *Service) startDriveUpload(ctx context.Context, j *Job, f *File, metadata map[string]any) error {
	if e := s.requireDriveWrite(j.Connection); e != nil {
		return e
	}
	grant, e := s.token(ctx, j.Connection)
	if e != nil {
		return e
	}
	data, e := json.Marshal(metadata)
	if e != nil {
		return e
	}
	request, e := http.NewRequestWithContext(ctx, "POST", "https://www.googleapis.com/upload/drive/v3/files?uploadType=resumable&supportsAllDrives=true&fields="+url.QueryEscape(driveFields), bytes.NewReader(data))
	if e != nil {
		return e
	}
	request.Header.Set("Authorization", "Bearer "+grant.Access)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Upload-Content-Type", f.Mime)
	request.Header.Set("X-Upload-Content-Length", strconv.FormatInt(f.Size, 10))
	response, e := googleHTTP.Do(request)
	if e != nil {
		return e
	}
	defer response.Body.Close()
	if response.StatusCode != 200 && response.StatusCode != 201 {
		return driveFailure(response)
	}
	location := response.Header.Get("Location")
	if !validUploadURL(location) {
		return ErrUnsafe
	}
	f.Session = location
	return s.checkpoint(j)
}
func uploadOffset(response *http.Response, total int64) (int64, error) {
	if response.StatusCode == 200 || response.StatusCode == 201 {
		return total, nil
	}
	value := response.Header.Get("Range")
	if value == "" {
		return 0, nil
	}
	if !strings.HasPrefix(value, "bytes=0-") {
		return 0, ErrUnsafe
	}
	last, e := strconv.ParseInt(strings.TrimPrefix(value, "bytes=0-"), 10, 64)
	if e != nil || last < 0 || last >= total {
		return 0, ErrUnsafe
	}
	return last + 1, nil
}
func (s *Service) uploadDriveFile(ctx context.Context, j *Job, f *File, metadata map[string]any) error {
	if f.Session == "" {
		if e := s.startDriveUpload(ctx, j, f, metadata); e != nil {
			return e
		}
	}
	status, e := s.uploadRequest(ctx, "PUT", f.Session, j.Connection, nil, fmt.Sprintf("bytes */%d", f.Size), "")
	if errors.Is(e, os.ErrNotExist) {
		f.Session = ""
		j.Done -= f.Done
		f.Done = 0
		if e = s.checkpoint(j); e != nil {
			return e
		}
		return driveRetry{seconds: 5}
	}
	if e != nil {
		return e
	}
	offset, e := uploadOffset(status, f.Size)
	status.Body.Close()
	if e != nil {
		return e
	}
	j.Done += offset - f.Done
	f.Done = offset
	if e = s.checkpoint(j); e != nil {
		return e
	}
	var source *os.File
	if s.peerFor(f.Source) == nil {
		root, er := s.root(f.Source)
		if er != nil {
			return er
		}
		defer root.Close()
		if er = safe(root, f.From); er != nil {
			return er
		}
		source, er = root.Open(f.From)
		if er != nil {
			return er
		}
		defer source.Close()
	}
	chunk := make([]byte, 4*1024*1024)
	for f.Done < f.Size {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if e = s.verifyCopySource(ctx, j, f); e != nil {
			return e
		}
		if e = s.checkpoint(j); e != nil {
			return e
		}
		length := min(int64(len(chunk)), f.Size-f.Done)
		var n int
		var er error
		if source != nil {
			n, er = source.ReadAt(chunk[:length], f.Done)
		} else {
			for int64(n) < length {
				data, e := s.readPeerChunk(ctx, *f, f.Done+int64(n), int(min(256<<10, length-int64(n))))
				if e != nil {
					er = e
					break
				}
				if len(data) == 0 {
					er = io.ErrUnexpectedEOF
					break
				}
				n += copy(chunk[n:], data)
			}
		}
		if er != nil && er != io.EOF {
			return er
		}
		if int64(n) != length {
			return io.ErrUnexpectedEOF
		}
		before := f.Done
		started := time.Now()
		response, er := s.uploadRequest(ctx, "PUT", f.Session, j.Connection, chunk[:n], fmt.Sprintf("bytes %d-%d/%d", before, before+length-1, f.Size), f.Mime)
		if er != nil {
			return er
		}
		next, er := uploadOffset(response, f.Size)
		response.Body.Close()
		if er != nil {
			return er
		}
		if next <= before || next > before+length {
			return ErrUncertain
		}
		f.Done = next
		j.Done += next - before
		j.Speed = float64(next-before) / time.Since(started).Seconds()
		if e = s.checkpoint(j); e != nil {
			return e
		}
	}
	if e = s.verifyCopySource(ctx, j, f); e != nil {
		return e
	}
	result, e := s.driveMeta(ctx, f.TargetID, j.Connection)
	if e != nil {
		return e
	}
	if result.Size != strconv.FormatInt(f.Size, 10) || result.Properties["filebeansOperation"] != operationProperty(j.ID, f.To) {
		return ErrConflict
	}
	return nil
}
func (s *Service) cleanupDriveStage(ctx context.Context, j *Job) error {
	if j.RemoteStage == "" {
		return nil
	}
	if e := s.requireDriveWrite(j.Connection); e != nil {
		return e
	}
	stage, e := s.driveMeta(ctx, j.RemoteStage, j.Connection)
	if errors.Is(e, os.ErrNotExist) {
		return nil
	}
	if e != nil {
		return e
	}
	if stage.Properties["filebeansStage"] != "true" || stage.Properties["filebeansOperation"] != j.ID {
		return ErrConflict
	}
	// An unacknowledged native copy can still exist in staging. Resolve its
	// operation marker before cancellation; never discard an uncertain request.
	parents := map[string]string{".": j.RemoteStage}
	for _, f := range j.Manifest {
		if f.Directory && f.TargetID != "" {
			parents[f.To] = f.TargetID
		}
	}
	for i := range j.Manifest {
		f := &j.Manifest[i]
		if !f.Attempted || f.TargetID != "" {
			continue
		}
		parent := parents[filepath.Dir(f.To)]
		if parent == "" {
			return ErrUnsafe
		}
		item, e := s.findDriveOperation(ctx, parent, operationProperty(j.ID, f.To), j.Connection)
		if errors.Is(e, os.ErrNotExist) {
			return ErrUncertain
		}
		if e != nil {
			return e
		}
		f.TargetID = item.ID
	}
	owned := map[string]bool{}
	for _, f := range j.Manifest {
		if f.TargetID != "" {
			owned[f.TargetID] = true
		}
	}
	var inspect func(string) error
	inspect = func(parent string) error {
		page := ""
		for {
			var list DriveList
			query := url.Values{"q": {"'" + parent + "' in parents"}, "pageSize": {"100"}, "fields": {"nextPageToken,files(" + driveFields + ")"}, "supportsAllDrives": {"true"}, "includeItemsFromAllDrives": {"true"}}
			if page != "" {
				query.Set("pageToken", page)
			}
			e := s.driveJSON(ctx, "GET", "files", query, nil, j.Connection, &list)
			if e != nil {
				return e
			}
			for _, item := range list.Files {
				if !owned[item.ID] {
					return ErrConflict
				}
				if item.Mime == "application/vnd.google-apps.folder" {
					if e = inspect(item.ID); e != nil {
						return e
					}
				}
			}
			page = list.Next
			if page == "" {
				return nil
			}
		}
	}
	if e = inspect(stage.ID); e != nil {
		return e
	}
	return s.driveJSON(ctx, "DELETE", "files/"+stage.ID, url.Values{"supportsAllDrives": {"true"}}, nil, j.Connection, nil)
}
