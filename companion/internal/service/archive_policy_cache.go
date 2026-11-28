package service

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
)

// Folder routing is owned/encrypted by the Library. The original link record
// retains both protection domains for the UI; this cache permits library writes
// while Samba is unavailable. Its ciphertext hash must match the active link.
type archivePolicy struct {
	Link     string          `json:"link"`
	Revision string          `json:"revision"`
	Library  ArchiveEndpoint `json:"library"`
	Target   archiveTarget   `json:"target"`
}

var errArchivePolicyMiss = errors.New("archive routing cache not ready")

func (s *Service) cachedArchivePolicies() ([]ArchiveLink, error) {
	if s.db == nil {
		return nil, errArchivePolicyMiss
	}
	rows, e := s.db.Query(s.sqlDialect("SELECT l.id,l.body,p.body FROM fb_operations l LEFT JOIN fb_operations p ON p.id=CONCAT('policy-',l.id) AND p.kind='archivePolicy' WHERE l.kind='archiveLink'"))
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	links := []ArchiveLink{}
	for rows.Next() {
		var id string
		var original, sealed []byte
		if e = rows.Scan(&id, &original, &sealed); e != nil {
			return nil, e
		}
		if len(sealed) == 0 {
			return nil, errArchivePolicyMiss
		}
		plain, e := s.decodeRecord("policy-"+id, "archivePolicy", sealed)
		var envelope struct {
			Boundary    *archivePolicy `json:"archiveBoundary"`
			Owner       string         `json:"archiveOwner"`
			LocalSource string         `json:"archiveLocalSource"`
		}
		if json.Unmarshal(sealed, &envelope) != nil {
			return nil, ErrUnsafe
		}
		if errors.Is(e, ErrVaultLocked) && envelope.Boundary != nil {
			plain, e = json.Marshal(envelope.Boundary)
		} else if e == nil && envelope.Boundary == nil {
			// Rebuild older caches while their routing metadata is readable.
			return nil, errArchivePolicyMiss
		}
		if e != nil {
			return nil, e
		}
		var p archivePolicy
		if json.Unmarshal(plain, &p) != nil {
			return nil, ErrUnsafe
		}
		h := sha256.Sum256(original)
		if p.Link != id || p.Revision != hex.EncodeToString(h[:]) {
			return nil, errArchivePolicyMiss
		}
		link := ArchiveLink{ID: id, Library: p.Library, Archive: ArchiveEndpoint{Source: p.Target.Backend, Path: p.Target.Base, Vault: p.Target.VaultID, Relative: p.Target.Relative, Identity: p.Target.Identity}}
		links = append(links, s.archiveMirrorCoordinates(link, envelope.Owner, envelope.LocalSource))
	}
	return links, rows.Err()
}
func (s *Service) cacheArchivePolicies(links []ArchiveLink) {
	if s.db == nil {
		return
	}
	for _, link := range links {
		if s.peerFor(link.Library.Source) != nil || link.Library.Pending != nil || link.Archive.Pending != nil {
			continue
		}
		var original []byte
		if s.db.QueryRow("SELECT body FROM fb_operations WHERE id=? AND kind='archiveLink'", link.ID).Scan(&original) != nil {
			continue
		}
		h := sha256.Sum256(original)
		p := archivePolicy{link.ID, hex.EncodeToString(h[:]), link.Library, archiveTarget{Backend: link.Archive.Source, Base: link.Archive.Path, VaultID: link.Archive.Vault, Relative: link.Archive.Relative, Identity: link.Archive.Identity}}
		id := "policy-" + link.ID
		encoded, e := json.Marshal(p)
		if e != nil {
			continue
		}
		encoded, e = s.encodeArchivePolicy(id, p, encoded)
		if e != nil {
			continue
		}
		_, _ = s.db.Exec(s.sqlDialect("INSERT INTO fb_operations (id,kind,state,body) VALUES (?,'archivePolicy','ready',?) ON DUPLICATE KEY UPDATE body=VALUES(body)"), id, encoded)
	}
}

// Keep ordinary endpoints available to write guards even when the Library is
// locked. Protected endpoints reveal only their public vault identity; the
// broker resolves that to the vault root, conservatively guarding the whole
// locked vault rather than exposing its private relative paths.
func (s *Service) encodeArchivePolicy(id string, policy archivePolicy, plain []byte) ([]byte, error) {
	encoded, err := s.encodeRecord(id, "archivePolicy", plain)
	if err != nil {
		return nil, err
	}
	boundary := policy
	if boundary.Library.Vault != "" {
		boundary.Library.Path = ""
		boundary.Library.Relative = ""
		boundary.Library.Identity = ""
	}
	if boundary.Target.VaultID != "" {
		boundary.Target.Base = ""
		boundary.Target.Relative = ""
		boundary.Target.Identity = ""
	}
	var envelope map[string]json.RawMessage
	if err = json.Unmarshal(encoded, &envelope); err != nil {
		return nil, err
	}
	envelope["archiveBoundary"], err = json.Marshal(boundary)
	if err != nil {
		return nil, err
	}
	return json.Marshal(envelope)
}

// Mirror bytes remain owned by their coordinator. Adapt only runtime routing.
func (s *Service) archiveMirrorCoordinates(link ArchiveLink, owner, localSource string) ArchiveLink {
	if owner == "" && s.config.Profile == "pi" && s.peerFor(link.Library.Source) != nil {
		owner = s.config.ArchiveCoordinator
		localSource = peerSourceFor(link.Archive.Source, s.config.ServerID)
	}
	if owner == "" || owner == s.config.ServerID || localSource == "" {
		return link
	}
	var library func(ArchiveEndpoint) ArchiveEndpoint
	library = func(e ArchiveEndpoint) ArchiveEndpoint {
		e.Source = "peer:" + owner + ":" + peerSourceFor(e.Source, owner)
		if e.Pending != nil {
			pending := library(*e.Pending)
			e.Pending = &pending
		}
		return e
	}
	var archive func(ArchiveEndpoint) ArchiveEndpoint
	archive = func(e ArchiveEndpoint) ArchiveEndpoint {
		e.Source = localSource
		if e.Pending != nil {
			pending := archive(*e.Pending)
			e.Pending = &pending
		}
		return e
	}
	link.Library, link.Archive = library(link.Library), archive(link.Archive)
	return link
}
