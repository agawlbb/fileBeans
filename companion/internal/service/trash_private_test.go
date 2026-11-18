package service

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func testPrivateTrash(t *testing.T, s *Service) {
	previous := s.vaults
	defer func() { s.vaults = previous }()
	base := s.config.Roots[1].Path
	for _, dir := range []string{"files", "private"} {
		if e := os.Mkdir(filepath.Join(base, dir), 0770); e != nil {
			t.Fatal(e)
		}
		defer os.RemoveAll(filepath.Join(base, dir))
	}
	v := VaultConfig{ID: ID(), Source: "samba", Path: "/files", Mount: base}
	s.vaults = &vaultManager{states: []*vaultState{{config: v, unlocked: true}}, metadataKey: bytes.Repeat([]byte{1}, 32), mountCheck: func(VaultConfig) bool { return true }}
	ids := []string{}
	defer func() {
		for _, id := range ids {
			s.db.Exec("DELETE FROM fb_operations WHERE id=?", id)
		}
	}()
	write := func(path string) {
		t.Helper()
		if e := os.MkdirAll(filepath.Dir(filepath.Join(base, path)), 0770); e != nil {
			t.Fatal(e)
		}
		if e := os.WriteFile(filepath.Join(base, path), []byte("retained private bytes"), 0660); e != nil {
			t.Fatal(e)
		}
	}
	t.Run("new-private-delete-lock-restore-purge", func(t *testing.T) {
		write("files/secret-name")
		entry, e := s.trash(Item{Source: "samba", Path: "files/secret-name"}, ID())
		if e != nil {
			t.Fatal(e)
		}
		ids = append(ids, entry.ID)
		if entry.Vault != v.ID || entry.Storage != filepath.Join("trash", entry.ID) {
			t.Fatal("wrong storage")
		}
		if _, e = os.Stat(filepath.Join(base, "files", "Trash")); !errors.Is(e, os.ErrNotExist) {
			t.Fatal("visible Trash created", e)
		}
		if data, e := os.ReadFile(filepath.Join(base, "private", entry.Storage, entry.Name)); e != nil || string(data) != "retained private bytes" {
			t.Fatal(e)
		}
		var stored []byte
		s.db.QueryRow("SELECT body FROM fb_operations WHERE id=?", entry.ID).Scan(&stored)
		if bytes.Contains(stored, []byte("secret-name")) {
			t.Fatal("filename leaked to SQL")
		}
		s.vaults.states[0].unlocked = false
		records, e := s.records("trash")
		if e != nil {
			t.Fatal(e)
		}
		for _, record := range records {
			if bytes.Contains(record, []byte(entry.ID)) {
				t.Fatal("locked entry listed")
			}
		}
		s.vaults.states[0].unlocked = true
		if e = s.restore(entry.ID); e != nil {
			t.Fatal(e)
		}
		if _, e = os.Stat(filepath.Join(base, "files", "secret-name")); e != nil {
			t.Fatal(e)
		}
		entry, e = s.trash(Item{Source: "samba", Path: "files/secret-name"}, ID())
		if e != nil {
			t.Fatal(e)
		}
		ids = append(ids, entry.ID)
		if e = s.purge(&entry); e != nil {
			t.Fatal(e)
		}
	})
	for _, phase := range []string{"legacy", "intent", "renamed"} {
		t.Run("migration-"+phase, func(t *testing.T) {
			id := ID()
			entry := Trash{ID: id, Source: "samba", Original: "files/migrated-" + phase, Name: "migrated-" + phase, State: "retained", Storage: filepath.Join("files", "Trash", id), Expires: time.Now().Add(time.Hour).Unix()}
			write(filepath.Join(entry.Storage, entry.Name))
			if phase != "legacy" {
				entry.Vault = v.ID
				entry.LegacyStorage = entry.Storage
				entry.Storage = filepath.Join("trash", id)
				if phase == "renamed" {
					os.MkdirAll(filepath.Join(base, "private", "trash"), 0770)
					if e := os.Rename(filepath.Join(base, "files", "Trash", id), filepath.Join(base, "private", entry.Storage)); e != nil {
						t.Fatal(e)
					}
				}
			}
			if e := s.insert(id, "trash", entry.State, ID(), entry); e != nil {
				t.Fatal(e)
			}
			ids = append(ids, id)
			if e := s.migrateProtectedTrash(&entry); e != nil {
				t.Fatal(e)
			}
			if entry.Vault != v.ID || entry.LegacyStorage != "" {
				t.Fatal("migration journal unfinished")
			}
			if e := s.restore(id); e != nil {
				t.Fatal(e)
			}
			if data, e := os.ReadFile(filepath.Join(base, entry.Original)); e != nil || string(data) != "retained private bytes" {
				t.Fatal("restore", e)
			}
		})
	}
	t.Run("unknown-legacy-content-preserved", func(t *testing.T) {
		write("files/Trash/untracked")
		root, e := os.OpenRoot(filepath.Join(base, "files"))
		if e != nil {
			t.Fatal(e)
		}
		defer root.Close()
		if e = removeEmptyTrashRoot(root); !errors.Is(e, ErrConflict) {
			t.Fatal("nonempty root accepted", e)
		}
		if _, e = root.Stat("Trash/untracked"); e != nil {
			t.Fatal("untracked file removed", e)
		}
	})
}
