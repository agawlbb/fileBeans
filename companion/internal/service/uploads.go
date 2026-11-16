package service

import (
	"bytes"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

type Upload struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Parent     string `json:"parent"`
	Size       int64  `json:"size"`
	Received   int64  `json:"received"`
	Connection string `json:"connection"`
	State      string `json:"state"`
	JobID      string `json:"jobId,omitempty"`
}

func (s *Service) uploadRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /v1/drive/uploads", func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			Name   string `json:"name"`
			Parent string `json:"parent"`
			Size   int64  `json:"size"`
		}
		if e := body(r, &input); e != nil {
			fail(w, e)
			return
		}
		if !validName(input.Name) || !driveID(input.Parent) || input.Size < 0 || input.Size > 2*1024*1024*1024*1024 || key(r) == "" {
			fail(w, ErrUnsafe)
			return
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		if e := s.requireDriveWrite(""); e != nil {
			fail(w, e)
			return
		}
		var upload Upload
		if e := s.existing(key(r), "upload", &upload); e == nil {
			if upload.Name != input.Name || upload.Parent != input.Parent || upload.Size != input.Size {
				fail(w, ErrConflict)
				return
			}
		} else if errors.Is(e, sql.ErrNoRows) {
			if _, e = s.driveParent(r.Context(), input.Parent, ""); e != nil {
				fail(w, e)
				return
			}
			if e = s.driveNameConflict(r.Context(), input.Parent, input.Name, "", ""); e != nil {
				fail(w, e)
				return
			}
			grant, e := s.google()
			if e != nil {
				fail(w, e)
				return
			}
			upload = Upload{ID: ID(), Name: input.Name, Parent: input.Parent, Size: input.Size, Connection: grant.Connection, State: "receiving"}
			if e = s.insert(upload.ID, "upload", upload.State, key(r), upload); e != nil {
				fail(w, e)
				return
			}
		} else {
			fail(w, e)
			return
		}
		if upload.Size == 0 && upload.State == "receiving" {
			if e := s.uploadFile(&upload); e != nil {
				fail(w, e)
				return
			}
			if e := s.enqueueUpload(&upload); e != nil {
				fail(w, e)
				return
			}
		}
		reply(w, 201, publicUpload(upload))
	})
	mux.HandleFunc("GET /v1/drive/uploads/{id}", func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		var upload Upload
		if e := s.read(r.PathValue("id"), "upload", &upload); e != nil {
			fail(w, e)
			return
		}
		if upload.State == "receiving" {
			if e := s.uploadFile(&upload); e != nil {
				fail(w, e)
				return
			}
			if upload.Received == upload.Size {
				if e := s.enqueueUpload(&upload); e != nil {
					fail(w, e)
					return
				}
			}
		}
		reply(w, 200, publicUpload(upload))
	})
	mux.HandleFunc("PUT /v1/drive/uploads/{id}", func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		var upload Upload
		if e := s.read(r.PathValue("id"), "upload", &upload); e != nil {
			fail(w, e)
			return
		}
		if e := s.requireDriveWrite(upload.Connection); e != nil {
			fail(w, e)
			return
		}
		if upload.State != "receiving" {
			reply(w, 200, publicUpload(upload))
			return
		}
		var from, to, total int64
		n, e := fmt.Sscanf(r.Header.Get("Content-Range"), "bytes %d-%d/%d", &from, &to, &total)
		if e != nil || n != 3 || from < 0 || to < from || to-from+1 > 4*1024*1024 || total != upload.Size || to >= total {
			fail(w, ErrUnsafe)
			return
		}
		data, e := io.ReadAll(io.LimitReader(r.Body, 4*1024*1024+1))
		if e != nil || int64(len(data)) != to-from+1 {
			fail(w, ErrUnsafe)
			return
		}
		if e = s.uploadFile(&upload); e != nil {
			fail(w, e)
			return
		}
		root, e := s.root("upload")
		if e != nil {
			fail(w, e)
			return
		}
		defer root.Close()
		file, e := root.OpenFile(filepath.Join(upload.ID, upload.Name), os.O_RDWR, 0600)
		if e != nil {
			fail(w, e)
			return
		}
		defer file.Close()
		if from < upload.Received {
			existing := make([]byte, len(data))
			count, er := file.ReadAt(existing, from)
			if er != nil || count != len(data) || !bytes.Equal(existing, data) {
				fail(w, ErrConflict)
				return
			}
		} else {
			if from != upload.Received {
				fail(w, ErrConflict)
				return
			}
			count, er := file.WriteAt(data, from)
			if er != nil {
				fail(w, er)
				return
			}
			if count != len(data) {
				fail(w, io.ErrShortWrite)
				return
			}
			if e = file.Sync(); e != nil {
				fail(w, e)
				return
			}
			upload.Received += int64(count)
			if e := s.save(upload.ID, "upload", upload.State, upload); e != nil {
				fail(w, e)
				return
			}
		}
		if upload.Received == upload.Size {
			if e = s.enqueueUpload(&upload); e != nil {
				fail(w, e)
				return
			}
		}
		reply(w, 200, publicUpload(upload))
	})
	mux.HandleFunc("POST /v1/drive/uploads/{id}/cancel", func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		var upload Upload
		if e := s.read(r.PathValue("id"), "upload", &upload); e != nil {
			fail(w, e)
			return
		}
		if upload.JobID != "" {
			var job Job
			if e := s.read(upload.JobID, "copy", &job); e != nil {
				fail(w, e)
				return
			}
			if job.State != "completed" && job.State != "cancelled" {
				job.Cancel = true
				job.State = "queued"
				job.Next = 0
				if e := s.save(job.ID, "copy", job.State, job); e != nil {
					fail(w, e)
					return
				}
			}
		} else {
			upload.State = "cancelled"
			if e := s.save(upload.ID, "upload", upload.State, upload); e != nil {
				fail(w, e)
				return
			}
			if root, e := s.root("upload"); e == nil {
				root.RemoveAll(upload.ID)
				root.Close()
			}
		}
		reply(w, 200, publicUpload(upload))
	})
}
func publicUpload(upload Upload) Upload { upload.Connection = ""; return upload }
func (s *Service) uploadFile(upload *Upload) error {
	if upload.State != "receiving" {
		return nil
	}
	directory := filepath.Join(s.config.State, "uploads")
	if e := os.MkdirAll(directory, 0700); e != nil {
		return e
	}
	root, e := os.OpenRoot(directory)
	if e != nil {
		return e
	}
	defer root.Close()
	if e = mkdir(root, upload.ID); e != nil {
		return e
	}
	file, e := root.OpenFile(filepath.Join(upload.ID, upload.Name), os.O_CREATE|os.O_RDWR, 0600)
	if e != nil {
		return e
	}
	defer file.Close()
	stat, e := file.Stat()
	if e != nil {
		return e
	}
	if stat.Size() > upload.Size {
		return ErrConflict
	}
	upload.Received = stat.Size()
	return nil
}
func (s *Service) enqueueUpload(upload *Upload) error {
	if upload.Received != upload.Size {
		return ErrConflict
	}
	requestKey := "upload-job:" + upload.ID
	var job Job
	e := s.existing(requestKey, "copy", &job)
	if errors.Is(e, sql.ErrNoRows) {
		job = Job{ID: ID(), State: "queued", Connection: upload.Connection, Request: CopyRequest{Items: []Item{{Source: "upload", Path: filepath.Join(upload.ID, upload.Name)}}, Destination: Item{Source: "drive", Path: upload.Parent}}}
		e = s.insert(job.ID, "copy", job.State, requestKey, job)
	}
	if e != nil {
		return e
	}
	upload.JobID = job.ID
	upload.State = "queued"
	return s.save(upload.ID, "upload", upload.State, upload)
}
func (s *Service) cleanupJobUploads(job *Job) {
	for _, item := range job.Request.Items {
		if item.Source != "upload" {
			continue
		}
		parts := strings.Split(item.Path, "/")
		if len(parts) != 2 {
			continue
		}
		var upload Upload
		if s.read(parts[0], "upload", &upload) != nil || upload.JobID != job.ID {
			continue
		}
		upload.State = job.State
		if s.save(upload.ID, "upload", upload.State, upload) != nil {
			continue
		}
		if root, e := s.root("upload"); e == nil {
			root.RemoveAll(upload.ID)
			root.Close()
		}
	}
}
