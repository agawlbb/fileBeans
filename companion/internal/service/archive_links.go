package service

import (
	"encoding/json"
	"errors"
	"net/http"
	"path/filepath"
	"sync"
)

var ErrArchiveLinked = errors.New("unlink the archive before deleting or moving this folder across sources")

// Vault-relative endpoints follow broker relocations without exposing locations
// in plaintext records. Pending records recover interrupted ordinary renames.
type ArchiveEndpoint struct {
	Source    string           `json:"source"`
	Path      string           `json:"path"`
	Vault     string           `json:"vault,omitempty"`
	Relative  string           `json:"relative,omitempty"`
	Identity  string           `json:"identity,omitempty"`
	Pending   *ArchiveEndpoint `json:"pending,omitempty"`
	Available bool             `json:"available"`
}
type ArchiveLink struct {
	ID      string          `json:"id"`
	Library ArchiveEndpoint `json:"library"`
	Archive ArchiveEndpoint `json:"archive"`
}

// Separate from job locks: listing and thumbnail policy must not wait for jobs.
var archiveMu sync.Mutex

func (s *Service) archiveLinks() ([]ArchiveLink, error) {
	if s.db == nil {
		return []ArchiveLink{}, nil
	}
	rows, err := s.db.Query("SELECT id,body FROM fb_operations WHERE kind='archiveLink'")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	links := []ArchiveLink{}
	for rows.Next() {
		var id string
		var data []byte
		if err = rows.Scan(&id, &data); err != nil {
			return nil, err
		}
		data, err = s.decodeRecord(id, "archiveLink", data)
		if err != nil {
			return nil, err
		}
		var link ArchiveLink
		if err = json.Unmarshal(data, &link); err != nil {
			return nil, err
		}
		links = append(links, link)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	return links, nil
}
func archivePath(p string) string {
	p, _ = clean(p)
	if p == "." {
		return "/"
	}
	return "/" + p
}
func endpointOverlap(a, b ArchiveEndpoint) bool {
	return a.Source == b.Source && (within(archivePath(a.Path), archivePath(b.Path)) || within(archivePath(b.Path), archivePath(a.Path)))
}
func (s *Service) endpointAt(source, path string) (ArchiveEndpoint, error) {
	p, err := filesystemPath(path)
	if err != nil || p == "." || protected(p) {
		return ArchiveEndpoint{}, ErrUnsafe
	}
	root, err := s.root(source)
	if err != nil {
		return ArchiveEndpoint{}, err
	}
	defer root.Close()
	if err = safe(root, p); err != nil {
		return ArchiveEndpoint{}, err
	}
	st, err := root.Stat(p)
	if err != nil {
		return ArchiveEndpoint{}, err
	}
	if !st.IsDir() {
		return ArchiveEndpoint{}, ErrUnsafe
	}
	if err = validateFilenameTree(root, p); err != nil {
		return ArchiveEndpoint{}, err
	}
	endpoint := ArchiveEndpoint{Source: source, Path: archivePath(p), Identity: moveIdentity(st), Available: true}
	if v := s.vaults.pathVault(source, p); v != nil {
		endpoint.Vault = v.ID
		boundary := v.Path
		if boundary == "" {
			boundary = "/"
		}
		endpoint.Relative, err = filepath.Rel(filepath.Clean(boundary), endpoint.Path)
	}
	return endpoint, err
}
func (s *Service) resolveEndpoint(e ArchiveEndpoint) ArchiveEndpoint {
	e.Available = false
	if e.Vault != "" {
		found := false
		for _, v := range s.vaults.configs() {
			if v.ID == e.Vault && v.Source == e.Source {
				boundary := v.Path
				if boundary == "" {
					boundary = "/"
				}
				e.Path = filepath.Join(boundary, e.Relative)
				found = true
				break
			}
		}
		if !found {
			return e
		}
	}
	root, err := s.root(e.Source)
	if err != nil {
		return e
	}
	defer root.Close()
	p, err := filesystemPath(e.Path)
	if err != nil || safe(root, p) != nil {
		return e
	}
	if st, err := root.Stat(p); err == nil && st.IsDir() && (e.Vault != "" && e.Pending == nil || e.Identity == "" || moveIdentity(st) == e.Identity) {
		e.Available = true
	}
	if !e.Available && e.Pending != nil {
		pending := s.resolveEndpoint(*e.Pending)
		if pending.Available {
			return pending
		}
	}
	return e
}
func (s *Service) resolvedArchiveLinks() ([]ArchiveLink, error) {
	links, err := s.archiveLinks()
	if err != nil {
		return nil, err
	}
	for i := range links {
		links[i].Library = s.resolveEndpoint(links[i].Library)
		links[i].Archive = s.resolveEndpoint(links[i].Archive)
	}
	return links, nil
}
func validateArchiveOverlap(links []ArchiveLink) error {
	var endpoints []ArchiveEndpoint
	for _, link := range links {
		endpoints = append(endpoints, link.Library, link.Archive)
	}
	for i, a := range endpoints {
		for _, b := range endpoints[i+1:] {
			if endpointOverlap(a, b) {
				return ErrConflict
			}
		}
	}
	return nil
}
func (s *Service) archiveRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/archive-links", func(w http.ResponseWriter, r *http.Request) {
		archiveMu.Lock()
		defer archiveMu.Unlock()
		links, err := s.resolvedArchiveLinks()
		if errors.Is(err, ErrVaultLocked) {
			reply(w, 200, []ArchiveLink{})
			return
		}
		if err != nil {
			fail(w, err)
			return
		}
		reply(w, 200, links)
	})
	mux.HandleFunc("POST /v1/archive-links", func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			Library Item `json:"library"`
			Archive Item `json:"archive"`
		}
		if err := body(r, &input); err != nil {
			fail(w, err)
			return
		}
		archiveMu.Lock()
		defer archiveMu.Unlock()
		// Exclude filesystem mutations while validating folder trees and endpoints.
		s.fsMu.Lock()
		defer s.fsMu.Unlock()
		filenamePublication.Lock()
		defer filenamePublication.Unlock()
		a, err := s.endpointAt(input.Library.Source, input.Library.Path)
		if err != nil {
			fail(w, err)
			return
		}
		b, err := s.endpointAt(input.Archive.Source, input.Archive.Path)
		if err != nil {
			fail(w, err)
			return
		}
		links, err := s.resolvedArchiveLinks()
		if err != nil {
			fail(w, err)
			return
		}
		for _, link := range links {
			if link.Library.Source == a.Source && link.Library.Path == a.Path && link.Archive.Source == b.Source && link.Archive.Path == b.Path {
				reply(w, 200, link)
				return
			}
		}
		link := ArchiveLink{ID: ID(), Library: a, Archive: b}
		if err = s.archiveEndpointsIdle(a, b); err != nil {
			fail(w, err)
			return
		}
		if err = validateArchiveOverlap(append(links, link)); err != nil {
			fail(w, err)
			return
		}
		if err = s.insert(link.ID, "archiveLink", "linked", "", link); err != nil {
			fail(w, err)
			return
		}
		reply(w, 201, link)
	})
	mux.HandleFunc("DELETE /v1/archive-links/{id}", func(w http.ResponseWriter, r *http.Request) {
		archiveMu.Lock()
		defer archiveMu.Unlock()
		var link ArchiveLink
		if err := s.read(r.PathValue("id"), "archiveLink", &link); err != nil {
			fail(w, err)
			return
		}
		if tasks, e := s.archiveTasks(); e != nil {
			fail(w, e)
			return
		} else {
			for _, j := range tasks {
				if j.Link == link.ID && j.State != "paired" && j.Kind != "drift" {
					fail(w, ErrConflict)
					return
				}
			}
		}
		if _, err := s.db.Exec("DELETE FROM fb_operations WHERE id=? AND kind='archiveLink'", link.ID); err != nil {
			fail(w, err)
			return
		}
		_, _ = s.db.Exec("DELETE FROM fb_operations WHERE id=? AND kind='archivePolicy'", "policy-"+link.ID)
		w.WriteHeader(204)
	})
}
func archiveMembership(links []ArchiveLink, source, p string) (string, string, bool) {
	for _, link := range links {
		for _, side := range []struct {
			e    ArchiveEndpoint
			role string
		}{{link.Library, "library"}, {link.Archive, "archive"}} {
			if side.e.Source == source && within(archivePath(p), archivePath(side.e.Path)) {
				return link.ID, side.role, archivePath(p) == archivePath(side.e.Path)
			}
		}
	}
	return "", "", false
}
func (s *Service) guardArchiveRemoval(source, p string) error {
	if s.archiveTrusted {
		return nil
	}
	if e := s.guardArchiveWrite(source, p, true); e != nil {
		return e
	}
	links, err := s.archivePolicyLinks()
	if err != nil {
		return err
	}
	for _, link := range links {
		for _, e := range []ArchiveEndpoint{link.Library, link.Archive} {
			if e.Source == source && within(archivePath(e.Path), archivePath(p)) {
				return ErrArchiveLinked
			}
			if e.Pending != nil && e.Pending.Source == source && within(archivePath(e.Pending.Path), archivePath(p)) {
				return ErrArchiveLinked
			}
		}
	}
	return nil
}

// Write both old and intended locations before rename. Resolution checks folder
// identity after interruption; a new unrelated directory cannot inherit the link.
func (s *Service) prepareArchiveRename(source, from, to string) error {
	if e := s.guardArchiveWrite(source, from, true); e != nil {
		return e
	}
	if e := s.guardArchiveWrite(source, to, true); e != nil {
		return e
	}
	links, err := s.archivePolicyLinks()
	if err != nil {
		return err
	}
	candidate := append([]ArchiveLink(nil), links...)
	for i := range candidate {
		for _, e := range []*ArchiveEndpoint{&candidate[i].Library, &candidate[i].Archive} {
			if e.Source == source && within(archivePath(e.Path), archivePath(from)) {
				rel, _ := filepath.Rel(archivePath(from), e.Path)
				e.Path = filepath.Join(archivePath(to), rel)
				if e.Vault != "" {
					for _, v := range s.vaults.configs() {
						if v.ID == e.Vault {
							if !within(e.Path, v.Path) {
								return ErrArchiveLinked
							}
							e.Relative, _ = filepath.Rel(v.Path, e.Path)
						}
					}
				}
			}
		}
	}
	if err = validateArchiveOverlap(candidate); err != nil {
		return err
	}
	for i := range links {
		changed := false
		if links[i].Library.Path != candidate[i].Library.Path {
			next := candidate[i].Library
			next.Pending = nil
			links[i].Library.Pending = &next
			changed = true
		}
		if links[i].Archive.Path != candidate[i].Archive.Path {
			next := candidate[i].Archive
			next.Pending = nil
			links[i].Archive.Pending = &next
			changed = true
		}
		if changed {
			if err = s.save(links[i].ID, "archiveLink", "linked", links[i]); err != nil {
				return err
			}
		}
	}
	return nil
}

// Suppression is checked before opening media, including for older clients.
func (s *Service) archiveThumbnailSuppressed(source, p string) (bool, error) {
	links, err := s.archivePolicyLinks()
	if errors.Is(err, ErrVaultLocked) {
		// Unknown routing must not render archive originals. Existing caches
		// remain readable under the requested file's own protection checks.
		return true, nil
	}
	if err != nil {
		return true, err
	}
	_, role, _ := archiveMembership(links, source, p)
	return role == "archive", nil
}

func (s *Service) finishArchiveRename() error {
	links, err := s.archivePolicyLinks()
	if err != nil {
		return err
	}
	for _, link := range links {
		changed := false
		if link.Library.Pending != nil {
			link.Library = s.resolveEndpoint(link.Library)
			changed = true
		}
		if link.Archive.Pending != nil {
			link.Archive = s.resolveEndpoint(link.Archive)
			changed = true
		}
		if !changed {
			continue
		}
		if err = s.save(link.ID, "archiveLink", "linked", link); err != nil {
			return err
		}
	}
	return nil
}
func (s *Service) validateArchiveVaultRelocation(id, path string) error {
	links, err := s.resolvedArchiveLinks()
	if err != nil {
		return err
	}
	for i := range links {
		if links[i].Archive.Vault == id {
			return &archiveReadOnlyError{Library: links[i].Library}
		}
		for _, e := range []*ArchiveEndpoint{&links[i].Library, &links[i].Archive} {
			if e.Vault == id {
				e.Path = filepath.Join(path, e.Relative)
			}
		}
	}
	return validateArchiveOverlap(links)
}

func (s *Service) archiveEndpointsIdle(endpoints ...ArchiveEndpoint) error {
	records, err := s.records("copy")
	if err != nil {
		return err
	}
	for _, data := range records {
		var job Job
		if err = json.Unmarshal(data, &job); err != nil {
			return err
		}
		switch job.State {
		case "completed", "cancelled", "failed":
			continue
		}
		for _, endpoint := range endpoints {
			if endpointOverlap(endpoint, ArchiveEndpoint{Source: job.Request.Destination.Source, Path: job.Request.Destination.Path}) {
				return ErrVaultBusy
			}
		}
		if job.Request.Operation != "move" {
			continue
		}
		for _, item := range job.Request.Items {
			for _, endpoint := range endpoints {
				if endpointOverlap(endpoint, ArchiveEndpoint{Source: item.Source, Path: item.Path}) {
					return ErrVaultBusy
				}
			}
		}
	}
	return nil
}
