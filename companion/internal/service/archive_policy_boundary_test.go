package service

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestLockedArchiveBoundaryAllowsUnrelatedMoves(t *testing.T) {
	for _, protectedArchive := range []bool{false, true} {
		t.Run(map[bool]string{false: "ordinary-archive", true: "protected-archive"}[protectedArchive], func(t *testing.T) {
			s := testVaultService()
			original := []byte(`{"vaultRecord":1,"id":"locked-link","sources":["Protected"],"nonce":"","ciphertext":""}`)
			hash := sha256.Sum256(original)
			policy := archivePolicy{Link: "locked-link", Revision: hex.EncodeToString(hash[:]), Library: ArchiveEndpoint{Source: "Protected", Vault: "archive", Path: "/secret-library", Relative: "secret-library"}, Target: archiveTarget{Backend: "ordinary", Base: "/archive"}}
			if protectedArchive {
				policy.Target = archiveTarget{Backend: "Protected", VaultID: "archive", Base: "/secret-archive", Relative: "secret-archive"}
			}
			plain, err := json.Marshal(policy)
			if err != nil {
				t.Fatal(err)
			}
			encoded, err := s.encodeArchivePolicy("policy-locked-link", policy, plain)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(encoded), "secret-library") || strings.Contains(string(encoded), "secret-archive") {
				t.Fatal("private path leaked")
			}
			s.vaults.states[0].unlocked = false
			s.db, err = sql.Open("locked-preview-policy", string(encoded))
			if err != nil {
				t.Fatal(err)
			}
			defer s.db.Close()
			req := CopyRequest{Operation: "move", Items: []Item{{Source: "ordinary", Path: "/from/file"}}, Destination: Item{Source: "ordinary", Path: "/to"}}
			if err = s.guardArchiveJob(req); err != nil {
				t.Fatal("unrelated move blocked", err)
			}
			if err = s.guardArchiveRemoval("ordinary", "/from/file"); err != nil {
				t.Fatal("unrelated removal blocked", err)
			}
			if id, err := s.archiveMoveIntent("ordinary", "/from/file", "/to/file"); err != nil || id != "" {
				t.Fatal("unrelated rename blocked", id, err)
			}
			if !protectedArchive {
				if err = s.guardArchiveWrite("ordinary", "/archive/file", true); !errors.Is(err, ErrArchiveReadOnly) {
					t.Fatal("ordinary archive lost protection", err)
				}
				if err = s.guardArchiveRemoval("ordinary", "/"); err == nil {
					t.Fatal("archive ancestor became removable")
				}
			}
			if _, err = s.decodeRecord("policy-locked-link", "archivePolicy", encoded); !errors.Is(err, ErrVaultLocked) {
				t.Fatal("private routing became readable", err)
			}
		})
	}
}
