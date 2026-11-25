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
	rows, e := s.db.Query("SELECT l.id,l.body,p.body FROM fb_operations l LEFT JOIN fb_operations p ON p.id=CONCAT('policy-',l.id) AND p.kind='archivePolicy' WHERE l.kind='archiveLink'")
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
		links = append(links, ArchiveLink{ID: id, Library: p.Library, Archive: ArchiveEndpoint{Source: p.Target.Backend, Path: p.Target.Base, Vault: p.Target.VaultID, Relative: p.Target.Relative, Identity: p.Target.Identity}})
	}
	return links, rows.Err()
}
func (s *Service) cacheArchivePolicies(links []ArchiveLink) {
	if s.db == nil {
		return
	}
	for _, link := range links {
		if link.Library.Pending != nil || link.Archive.Pending != nil {
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
		encoded, e = s.encodeRecord(id, "archivePolicy", encoded)
		if e != nil {
			continue
		}
		_, _ = s.db.Exec("INSERT INTO fb_operations (id,kind,state,body) VALUES (?,'archivePolicy','ready',?) ON DUPLICATE KEY UPDATE body=VALUES(body)", id, encoded)
	}
}
