package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"syscall"
	"time"
)

type migrationOperation struct {
	ID    string          `json:"id"`
	Kind  string          `json:"kind"`
	State string          `json:"state"`
	Key   sql.NullString  `json:"key"`
	Body  json.RawMessage `json:"body"`
}
type migrationOperations struct {
	ArchiveMirrors bool                 `json:"archiveMirrors,omitempty"`
	Version        int                  `json:"version"`
	Source         string               `json:"source"`
	Vaults         []string             `json:"vaults"`
	Records        []migrationOperation `json:"records"`
}

// Export only existing stored ciphertext for protected records, never decrypting
// or exporting recovery keys. Active transfers and archive relationships must
// be resolved before ownership changes. Trash stays on the existing disk.
func MigrationOperations(c Config, bundlePath, path string, importing bool, preserveArchives ...bool) error {
	preserve := len(preserveArchives) == 1 && preserveArchives[0]
	raw, e := os.ReadFile(bundlePath)
	if e != nil {
		return e
	}
	var bundle struct {
		Version int           `json:"version"`
		Source  string        `json:"source"`
		Entries []brokerEntry `json:"entries"`
	}
	if json.Unmarshal(raw, &bundle) != nil || bundle.Version != 1 || bundle.Source == "" {
		return ErrUnsafe
	}
	ids := map[string]bool{}
	out := migrationOperations{Version: 1, Source: bundle.Source}
	for _, v := range bundle.Entries {
		if v.Source != bundle.Source {
			return ErrUnsafe
		}
		ids["vault:"+v.ID] = true
		out.Vaults = append(out.Vaults, v.ID)
	}
	var d *sql.DB
	if c.SQLite != "" {
		d, e = openSQLite(c.SQLite)
	} else {
		d, e = openDB(c.DSN)
	}
	if e != nil {
		return e
	}
	defer d.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if importing {
		if c.SQLite == "" {
			return ErrUnsafe
		}
		raw, e = os.ReadFile(path)
		if e != nil {
			return e
		}
		if len(raw) > 32<<20 {
			return ErrUnsafe
		}
		var input migrationOperations
		if json.Unmarshal(raw, &input) != nil || input.Version != 1 || input.Source != bundle.Source || len(input.Vaults) != len(out.Vaults) {
			return ErrUnsafe
		}
		for _, v := range input.Vaults {
			if !ids["vault:"+v] {
				return ErrUnsafe
			}
		}
		if input.ArchiveMirrors && !preserve {
			return errors.New("archive mirrors require explicit preservation")
		}
		if e = validateArchiveMirrors(input.Records); e != nil {
			return e
		}
		tx, e := d.BeginTx(ctx, nil)
		if e != nil {
			return e
		}
		defer tx.Rollback()
		for _, r := range input.Records {
			mirror := input.ArchiveMirrors && (r.Kind == "archiveLink" || r.Kind == "archivePolicy")
			if (r.Kind != "trash" && !mirror) || r.ID == "" || !json.Valid(r.Body) || (!mirror && !migrationTouches(r.Body, bundle.Source, ids)) {
				return ErrUnsafe
			}
			var old []byte
			er := tx.QueryRowContext(ctx, "SELECT body FROM fb_operations WHERE id=?", r.ID).Scan(&old)
			if er == nil {
				if !bytes.Equal(old, r.Body) {
					return ErrConflict
				}
				continue
			}
			if !errors.Is(er, sql.ErrNoRows) {
				return er
			}
			var key any
			if r.Key.Valid {
				key = r.Key.String
			}
			if _, e = tx.ExecContext(ctx, "INSERT INTO fb_operations(id,kind,state,request_key,body) VALUES(?,?,?,?,?)", r.ID, r.Kind, r.State, key, []byte(r.Body)); e != nil {
				return e
			}
		}
		return tx.Commit()
	}
	rows, e := d.QueryContext(ctx, "SELECT id,kind,state,request_key,body FROM fb_operations")
	if e != nil {
		return e
	}
	defer rows.Close()
	var records []migrationOperation
	for rows.Next() {
		var r migrationOperation
		if e = rows.Scan(&r.ID, &r.Kind, &r.State, &r.Key, &r.Body); e != nil {
			return e
		}
		records = append(records, r)
	}
	if e = rows.Err(); e != nil {
		return e
	}
	rows.Close()
	mirrors := map[string]bool{}
	if preserve {
		for _, r := range records {
			if r.Kind == "archiveLink" && migrationTouches(r.Body, bundle.Source, ids) {
				mirrors[r.ID] = true
				mirrors["policy-"+r.ID] = true
			}
		}
	}
	for _, r := range records {
		if mirrors[r.ID] && (r.Kind == "archiveLink" || r.Kind == "archivePolicy") {
			out.Records = append(out.Records, r)
			out.ArchiveMirrors = true
			continue
		}
		if !migrationTouches(r.Body, bundle.Source, ids) {
			continue
		}
		if r.Kind == "copy" && r.State != "completed" && r.State != "cancelled" {
			return errors.New("drain source transfers before migration")
		}
		if r.Kind == "archiveLink" || r.Kind == "archivePolicy" {
			return errors.New("unlink source archive relationships before migration")
		}
		if r.Kind == "hosting" && r.State == "active" {
			return errors.New("revoke source public links before migration")
		}
		if r.Kind == "trash" {
			// Terminal failed/missing history has no automatic expiry or cleanup
			// intent. Preserve it verbatim rather than deleting history or files.
			if r.State != "retained" && r.State != "restored" && r.State != "purged" && r.State != "failed" && r.State != "missing" {
				return errors.New("finish trash recovery before migration")
			}
			out.Records = append(out.Records, r)
		}
	}
	if e = validateArchiveMirrors(out.Records); e != nil {
		return e
	}
	raw, e = json.Marshal(out)
	if e != nil {
		return e
	}
	f, e := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY|syscall.O_NOFOLLOW, 0600)
	if e != nil {
		return e
	}
	_, e = f.Write(raw)
	if e == nil {
		e = f.Sync()
	}
	closeErr := f.Close()
	if e != nil {
		return e
	}
	return closeErr
}

// Mirrors keep ciphertext byte-for-byte and carry a policy whose revision binds
// it to the same link. The coordinator's authoritative rows are never retired.
func validateArchiveMirrors(records []migrationOperation) error {
	links := map[string][]byte{}
	policies := map[string][]byte{}
	for _, r := range records {
		switch r.Kind {
		case "archiveLink":
			links[r.ID] = r.Body
		case "archivePolicy":
			policies[r.ID] = r.Body
		}
	}
	for id, raw := range links {
		var envelope struct {
			Boundary *archivePolicy `json:"archiveBoundary"`
		}
		policy, ok := policies["policy-"+id]
		h := sha256.Sum256(raw)
		if !ok || json.Unmarshal(policy, &envelope) != nil || envelope.Boundary == nil || envelope.Boundary.Link != id || envelope.Boundary.Revision != hex.EncodeToString(h[:]) {
			return errors.New("archive policy mirror is missing or stale")
		}
	}
	for id := range policies {
		if _, ok := links[strings.TrimPrefix(id, "policy-")]; !ok {
			return ErrUnsafe
		}
	}
	return nil
}

func migrationTouches(raw []byte, source string, ids map[string]bool) bool {
	var v any
	if json.Unmarshal(raw, &v) != nil {
		return false
	}
	var walk func(any) bool
	walk = func(v any) bool {
		switch x := v.(type) {
		case map[string]any:
			if s, ok := x["source"].(string); ok && s == source {
				return true
			}
			if s, ok := x["librarySource"].(string); ok && s == source {
				return true
			}
			if sources, ok := x["sources"].([]any); ok {
				for _, s := range sources {
					if label, ok := s.(string); ok && (ids[label] || label == source || strings.HasPrefix(label, "peer-vault:"+source+"~")) {
						return true
					}
				}
			}
			for _, child := range x {
				if walk(child) {
					return true
				}
			}
		case []any:
			for _, child := range x {
				if walk(child) {
					return true
				}
			}
		}
		return false
	}
	return walk(v)
}
