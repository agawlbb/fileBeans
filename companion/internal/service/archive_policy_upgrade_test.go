package service

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestLegacyArchivePolicyAllowsOrdinaryDeleteWithArchiveLocked(t *testing.T) {
	s := testNode(t, "Protected")
	s.vaults = testVaultService().vaults
	ordinary := t.TempDir()
	s.config.Roots = append(s.config.Roots, Root{Name: "ordinary", Path: ordinary})
	link := ArchiveLink{ID: ID(), Library: ArchiveEndpoint{Source: "Protected", Vault: "archive", Path: "/private-library", Relative: "private-library"}, Archive: ArchiveEndpoint{Source: "Other", Vault: "other", Path: "/private-originals", Relative: "private-originals"}}
	if err := s.insert(link.ID, "archiveLink", "linked", "", link); err != nil {
		t.Fatal(err)
	}
	var original []byte
	if err := s.db.QueryRow("SELECT body FROM fb_operations WHERE id=?", link.ID).Scan(&original); err != nil {
		t.Fatal(err)
	}
	revision := sha256.Sum256(original)
	policy := archivePolicy{Link: link.ID, Revision: hex.EncodeToString(revision[:]), Library: link.Library, Target: archiveTarget{Backend: "Other", VaultID: "other", Base: "/private-originals", Relative: "private-originals"}}
	plain, _ := json.Marshal(policy)
	legacy, err := s.encodeRecord("policy-"+link.ID, "archivePolicy", plain)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec("INSERT INTO fb_operations(id,kind,state,body) VALUES(?,'archivePolicy','ready',?)", "policy-"+link.ID, legacy); err != nil {
		t.Fatal(err)
	}
	s.vaults.states[1].unlocked = false
	if _, err = s.archiveLinks(); !errors.Is(err, ErrVaultLocked) {
		t.Fatal("fixture must require the Archive key", err)
	}
	file := filepath.Join(ordinary, "delete-me.txt")
	if err = os.WriteFile(file, []byte("unprotected fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	trashed, err := s.trash(Item{Source: "ordinary", Path: "delete-me.txt"}, ID())
	if err != nil || trashed.State != "retained" {
		t.Fatal("ordinary delete depends on locked Archive", trashed.State, err)
	}
	if _, err = os.Stat(file); !os.IsNotExist(err) {
		t.Fatal("ordinary source not moved to Trash", err)
	}
	var upgraded, retained []byte
	s.db.QueryRow("SELECT body FROM fb_operations WHERE id=?", "policy-"+link.ID).Scan(&upgraded)
	s.db.QueryRow("SELECT body FROM fb_operations WHERE id=?", link.ID).Scan(&retained)
	if !bytes.Equal(original, retained) {
		t.Fatal("original archive ciphertext changed")
	}
	var before, after map[string]json.RawMessage
	json.Unmarshal(legacy, &before)
	json.Unmarshal(upgraded, &after)
	for _, field := range []string{"ciphertext", "nonce", "sources"} {
		if !bytes.Equal(before[field], after[field]) {
			t.Fatal("policy encryption changed", field)
		}
	}
	if len(after["archiveBoundary"]) == 0 || bytes.Contains(after["archiveBoundary"], []byte("private-")) {
		t.Fatal("missing or unredacted boundary")
	}
	s.vaults.states[0].unlocked = false
	if err = s.guardArchiveRemoval("ordinary", "unrelated.txt"); err != nil {
		t.Fatal("unrelated delete requires Library unlock after upgrade", err)
	}
	if err = s.guardArchiveWrite("Other", "private-originals/file", true); !errors.Is(err, ErrArchiveReadOnly) {
		t.Fatal("Archive became writable", err)
	}
	if _, err = s.decodeRecord("policy-"+link.ID, "archivePolicy", upgraded); !errors.Is(err, ErrVaultLocked) {
		t.Fatal("private policy no longer protected", err)
	}
}
