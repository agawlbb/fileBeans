package service

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
)

// A successful archive-link mutation includes durable read-only policy on the
// destination. If delivery fails, keep the coordinator's record and return an
// error; retry/periodic synchronization repairs the conservative state.
func (s *Service) archiveMirrorHandler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.config.Profile == "pi" || !strings.HasPrefix(r.URL.Path, "/v1/archive-links") || r.Method == http.MethodGet {
			next.ServeHTTP(w, r)
			return
		}
		rec := httptest.NewRecorder()
		next.ServeHTTP(rec, r)
		if rec.Code >= 200 && rec.Code < 300 {
			if e := s.syncArchiveMirrors(r.Context()); e != nil {
				fail(w, e)
				return
			}
		}
		for k, v := range rec.Header() {
			w.Header()[k] = v
		}
		w.WriteHeader(rec.Code)
		_, _ = w.Write(rec.Body.Bytes())
	})
}
func (s *Service) syncArchiveMirrors(ctx context.Context) error {
	if s.config.Profile == "pi" {
		return nil
	}
	needed := false
	for _, p := range s.config.Peers {
		needed = needed || p.ArchiveKey != ""
	}
	if !needed {
		return nil
	}
	archiveMu.Lock()
	defer archiveMu.Unlock()
	if _, e := s.archivePolicyLinks(); e != nil {
		return e
	}
	rows, e := s.db.QueryContext(ctx, "SELECT id,kind,state,request_key,body FROM fb_operations WHERE kind IN ('archiveLink','archivePolicy')")
	if e != nil {
		return e
	}
	var records []migrationOperation
	for rows.Next() {
		var record migrationOperation
		if e = rows.Scan(&record.ID, &record.Kind, &record.State, &record.Key, &record.Body); e != nil {
			rows.Close()
			return e
		}
		records = append(records, record)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return e
	}
	byID := map[string]migrationOperation{}
	for _, record := range records {
		byID[record.ID] = record
	}
	for _, p := range s.config.Peers {
		if p.ArchiveKey == "" {
			continue
		}
		for _, source := range p.Sources {
			if source == "drive" {
				continue
			}
			selected := []migrationOperation{}
			for _, record := range records {
				if record.Kind != "archivePolicy" {
					continue
				}
				var envelope struct {
					Boundary *archivePolicy `json:"archiveBoundary"`
				}
				if json.Unmarshal(record.Body, &envelope) != nil || envelope.Boundary == nil {
					return ErrUnsafe
				}
				if envelope.Boundary.Target.Backend != source {
					continue
				}
				link, ok := byID[envelope.Boundary.Link]
				if !ok {
					return ErrUnsafe
				}
				selected = append(selected, link, record)
			}
			if e = validateArchiveMirrors(selected); e != nil {
				return e
			}
			remote := archivePeerRoot{peer: p, source: source}
			if e = remote.callContext(ctx, archivePeerRequest{Operation: "mirrors", Owner: s.config.ServerID, Records: selected}, nil); e != nil {
				return e
			}
		}
	}
	return nil
}
func (s *Service) applyArchiveMirrors(in archivePeerRequest) error {
	if s.config.Profile != "pi" || s.config.SQLite == "" || s.config.ArchiveCoordinator == "" || in.Owner != s.config.ArchiveCoordinator || len(in.Records) > 128 {
		return ErrUnsafe
	}
	if e := validateArchiveMirrors(in.Records); e != nil {
		return e
	}
	ids := map[string]bool{}
	for _, record := range in.Records {
		if record.Kind != "archiveLink" && record.Kind != "archivePolicy" {
			return ErrUnsafe
		}
		ids[record.ID] = true
		if record.Kind == "archivePolicy" {
			var envelope struct {
				Boundary *archivePolicy `json:"archiveBoundary"`
			}
			if json.Unmarshal(record.Body, &envelope) != nil || envelope.Boundary == nil || envelope.Boundary.Target.Backend != in.Source || s.peerFor(envelope.Boundary.Library.Source) == nil {
				return ErrUnsafe
			}
		}
	}
	s.fsMu.Lock()
	defer s.fsMu.Unlock()
	tx, e := s.db.Begin()
	if e != nil {
		return e
	}
	defer tx.Rollback()
	rows, e := tx.Query("SELECT id,body FROM fb_operations WHERE kind='archivePolicy'")
	if e != nil {
		return e
	}
	var obsolete []string
	for rows.Next() {
		var id string
		var raw []byte
		if e = rows.Scan(&id, &raw); e != nil {
			rows.Close()
			return e
		}
		var envelope struct {
			Boundary *archivePolicy `json:"archiveBoundary"`
		}
		if json.Unmarshal(raw, &envelope) != nil || envelope.Boundary == nil {
			rows.Close()
			return ErrUnsafe
		}
		if envelope.Boundary.Target.Backend == in.Source && !ids[id] {
			obsolete = append(obsolete, id, envelope.Boundary.Link)
		}
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return e
	}
	for _, id := range obsolete {
		if _, e = tx.Exec("DELETE FROM fb_operations WHERE id=? AND kind IN ('archiveLink','archivePolicy')", id); e != nil {
			return e
		}
	}
	for _, record := range in.Records {
		if _, e = tx.Exec("INSERT INTO fb_operations(id,kind,state,body) VALUES(?,?,?,?) ON CONFLICT(id) DO UPDATE SET state=excluded.state,body=excluded.body WHERE fb_operations.kind=excluded.kind", record.ID, record.Kind, record.State, []byte(record.Body)); e != nil {
			return e
		}
	}
	return tx.Commit()
}
