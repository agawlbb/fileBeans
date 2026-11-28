package service

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

var ErrArchiveReadOnly = errors.New("linked archive is read-only")

type archiveReadOnlyError struct{ Library ArchiveEndpoint }

func (e *archiveReadOnlyError) Error() string {
	source := e.Library.Source
	if _, root, qualified := splitPeerSource(source); qualified {
		source = root
	}
	if e.Library.Path == "" || e.Library.Path == "." || e.Library.Path == "/" {
		return fmt.Sprintf("Make changes in the linked folder on %s.", source)
	}
	return fmt.Sprintf("Make changes in %s %s.", source, archivePath(e.Library.Path))
}
func (e *archiveReadOnlyError) Unwrap() error { return ErrArchiveReadOnly }

// A recursive mutation of an ancestor also changes the linked archive.
func archiveWritePolicy(links []ArchiveLink, source, p string, recursive bool) error {
	p = archivePath(p)
	for _, link := range links {
		candidates := []ArchiveEndpoint{link.Archive}
		if link.Archive.Pending != nil {
			candidates = append(candidates, *link.Archive.Pending)
		}
		for _, endpoint := range candidates {
			if endpoint.Source == source && (within(p, archivePath(endpoint.Path)) || recursive && within(archivePath(endpoint.Path), p)) {
				return &archiveReadOnlyError{Library: link.Library}
			}
		}
	}
	return nil
}

// Resolve protected locations from the broker snapshot without statting every
// endpoint. Unrelated VPS writes and thumbnail requests must not probe Samba.
func (s *Service) archivePolicyLinks() ([]ArchiveLink, error) {
	links, e := s.cachedArchivePolicies()
	if e != nil {
		links, e = s.archiveLinks()
		if e == nil {
			s.cacheArchivePolicies(links)
		}
	}
	if e != nil {
		return nil, e
	}
	configs := s.vaults.configs()
	resolve := func(endpoint *ArchiveEndpoint) {
		if endpoint.Vault == "" {
			return
		}
		for _, v := range configs {
			if v.ID == endpoint.Vault && v.Source == endpoint.Source {
				endpoint.Path = filepath.Join(v.Path, endpoint.Relative)
				break
			}
		}
	}
	for i := range links {
		resolve(&links[i].Library)
		resolve(&links[i].Archive)
		if links[i].Library.Pending != nil {
			resolve(links[i].Library.Pending)
		}
		if links[i].Archive.Pending != nil {
			resolve(links[i].Archive.Pending)
		}
	}
	return links, nil
}
func (s *Service) guardArchiveWrite(source, p string, recursive bool) error {
	if s.archiveTrusted {
		return nil
	}
	if source == "drive" || source == "upload" {
		return nil
	}
	links, e := s.archivePolicyLinks()
	if e != nil {
		return e
	}
	return archiveWritePolicy(links, source, p, recursive)
}

// Check filesystem identity too: CIFS case aliases and in-root aliases must not
// bypass an ordinary (unencrypted) archive's lexical read-only boundary.
func archiveAliasPolicy(root rootFS, links []ArchiveLink, source, p string, recursive bool) error {
	p, e := clean(p)
	if e != nil {
		return e
	}
	for _, link := range links {
		if link.Archive.Source != source {
			continue
		}
		archive, e := clean(link.Archive.Path)
		if e != nil {
			return e
		}
		info, e := root.Stat(archive)
		if e != nil {
			continue
		}
		for ancestor := p; ; ancestor = filepath.Dir(ancestor) {
			current, e := root.Stat(ancestor)
			if e == nil && os.SameFile(current, info) {
				return &archiveReadOnlyError{Library: link.Library}
			}
			if ancestor == "." {
				break
			}
		}
		if recursive {
			target, e := root.Stat(p)
			if e != nil {
				continue
			}
			for ancestor := archive; ; ancestor = filepath.Dir(ancestor) {
				current, e := root.Stat(ancestor)
				if e == nil && os.SameFile(current, target) {
					return &archiveReadOnlyError{Library: link.Library}
				}
				if ancestor == "." {
					break
				}
			}
		}
	}
	return nil
}
func archiveCachePath(p string) bool {
	for _, part := range strings.Split(filepath.ToSlash(p), "/") {
		if strings.EqualFold(part, thumbnailDirectory) {
			return true
		}
	}
	return false
}

type archiveWriteCheck func(string, bool) error
type archiveMutationRoot interface{ checkArchiveWrite(string, bool) error }

func checkArchiveRoot(root rootFS, p string, recursive bool) error {
	if r, ok := root.(archiveMutationRoot); ok {
		return r.checkArchiveWrite(p, recursive)
	}
	return nil
}
func (s *Service) archiveGuarded(root rootFS, source string) rootFS {
	if s.db == nil || s.archiveTrusted {
		return root
	}
	check := func(p string, recursive bool) error {
		// Only server-owned cache paths bypass media read-only policy. Every public
		// filesystem route rejects these reserved paths before touching storage.
		if archiveCachePath(p) {
			return nil
		}
		links, e := s.archivePolicyLinks()
		if e != nil {
			return e
		}
		if e = archiveWritePolicy(links, source, p, recursive); e != nil {
			return e
		}
		return archiveAliasPolicy(root, links, source, p, recursive)
	}
	if guarded, ok := root.(*guardedRoot); ok {
		guarded.archiveWrite = check
		return guarded
	}
	return &archiveWriteRoot{rootFS: root, check: check}
}

// Ordinary sources also need write policy, without impersonating a protected
// root or changing the existing protection lease/type assertions.
type archiveWriteRoot struct {
	rootFS
	check archiveWriteCheck
}

func (r *archiveWriteRoot) checkArchiveWrite(p string, recursive bool) error {
	return r.check(p, recursive)
}
func (r *archiveWriteRoot) OpenFile(p string, flag int, mode os.FileMode) (*os.File, error) {
	if flag&(os.O_WRONLY|os.O_RDWR|os.O_CREATE|os.O_TRUNC|os.O_APPEND) != 0 {
		if e := r.check(p, false); e != nil {
			return nil, e
		}
	}
	return r.rootFS.OpenFile(p, flag, mode)
}
func (r *archiveWriteRoot) WriteFile(p string, b []byte, mode os.FileMode) error {
	if e := r.check(p, false); e != nil {
		return e
	}
	return r.rootFS.WriteFile(p, b, mode)
}
func (r *archiveWriteRoot) MkdirAll(p string, mode os.FileMode) error {
	if e := r.check(p, false); e != nil {
		return e
	}
	return r.rootFS.MkdirAll(p, mode)
}
func (r *archiveWriteRoot) Chmod(p string, mode os.FileMode) error {
	if e := r.check(p, true); e != nil {
		return e
	}
	return r.rootFS.Chmod(p, mode)
}
func (r *archiveWriteRoot) Remove(p string) error {
	if e := r.check(p, true); e != nil {
		return e
	}
	return r.rootFS.Remove(p)
}
func (r *archiveWriteRoot) RemoveAll(p string) error {
	if e := r.check(p, true); e != nil {
		return e
	}
	return r.rootFS.RemoveAll(p)
}

// Check logical intent before receiving uploads, discovering media, or queuing
// work; low-level root checks repeat this at actual publication/execution time.
func (s *Service) guardArchiveJob(req CopyRequest) error {
	if req.Operation == "move" && !s.archiveTrusted {
		links, e := s.archivePolicyLinks()
		if e != nil {
			return e
		}
		for _, link := range links {
			for _, item := range req.Items {
				if item.Source == link.Library.Source {
					if _, ok := archiveRelative(link.Library.Path, item.Path); ok {
						name := filepath.Base(item.Path)
						if req.DestinationName != "" {
							name = req.DestinationName
						}
						if req.Destination.Source != item.Source {
							return ErrArchiveLinked
						}
						if _, ok = archiveRelative(link.Library.Path, filepath.Join(req.Destination.Path, name)); !ok {
							return ErrArchiveLinked
						}
					}
				}
			}
		}
	}
	if e := s.guardArchiveWrite(req.Destination.Source, req.Destination.Path, false); e != nil {
		return e
	}
	if req.Destination.Source != "drive" {
		for _, item := range req.Items {
			if item.Source == "drive" {
				continue
			}
			name := filepath.Base(item.Path)
			if req.DestinationName != "" {
				name = req.DestinationName
			}
			if e := s.guardArchiveWrite(req.Destination.Source, filepath.Join(req.Destination.Path, name), true); e != nil {
				return e
			}
		}
	}
	if req.Operation == "move" {
		for _, item := range req.Items {
			if e := s.guardArchiveWrite(item.Source, item.Path, true); e != nil {
				return e
			}
		}
	}
	return nil
}

func (s *Service) guardArchiveVaultWrite(id string) error {
	links, e := s.archivePolicyLinks()
	if e != nil {
		return e
	}
	for _, link := range links {
		if link.Archive.Vault == id {
			return &archiveReadOnlyError{Library: link.Library}
		}
	}
	return nil
}
func (s *Service) guardArchiveTrash(t Trash) error {
	if s.archiveTrusted {
		return nil
	}
	if strings.HasPrefix(t.ID, "vault-") {
		return s.guardArchiveVaultWrite(strings.TrimPrefix(t.ID, "vault-"))
	}
	p := t.Original
	if t.Vault != "" && s.vaults != nil {
		for _, v := range s.vaults.configs() {
			if v.ID != t.Vault {
				continue
			}
			for _, old := range v.PreviousPaths {
				old = strings.TrimPrefix(old, "/")
				if within(p, old) {
					p = filepath.Join(v.Path, strings.TrimPrefix(strings.TrimPrefix(p, old), "/"))
					break
				}
			}
		}
	}
	return s.guardArchiveWrite(t.Source, p, true)
}
