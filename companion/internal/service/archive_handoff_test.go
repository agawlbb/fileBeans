package service

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestArchiveHandoffKeepsCiphertextCoordinatorAndRestartedWriteGuard(t *testing.T) {
	source := testNode(t, "Pi")
	target := testNode(t, "Pi")
	link := ArchiveLink{ID: ID(), Library: ArchiveEndpoint{Source: "VPS", Path: "/library"}, Archive: ArchiveEndpoint{Source: "Pi", Path: "/archive"}}
	if e := source.insert(link.ID, "archiveLink", "linked", "", link); e != nil {
		t.Fatal(e)
	}
	source.cacheArchivePolicies([]ArchiveLink{link})
	bundle := filepath.Join(t.TempDir(), "bundle.json")
	if e := os.WriteFile(bundle, []byte(`{"version":1,"source":"Pi","entries":[{"id":"`+strings.Repeat("a", 32)+`","source":"Pi"}]}`), 0600); e != nil {
		t.Fatal(e)
	}
	output := filepath.Join(t.TempDir(), "operations.json")
	if e := MigrationOperations(source.config, bundle, output, false); e == nil {
		t.Fatal("implicit mirror accepted")
	}
	if e := MigrationOperations(source.config, bundle, output, false, true); e != nil {
		t.Fatal("preserved export", e)
	}
	if e := MigrationOperations(target.config, bundle, output, true); e == nil {
		t.Fatal("implicit mirror import accepted")
	}
	for i := 0; i < 2; i++ {
		if e := MigrationOperations(target.config, bundle, output, true, true); e != nil {
			t.Fatal("resumable import", i, e)
		}
	}
	for _, id := range []string{link.ID, "policy-" + link.ID} {
		var original, mirror []byte
		if e := source.db.QueryRow("SELECT body FROM fb_operations WHERE id=?", id).Scan(&original); e != nil {
			t.Fatal(e)
		}
		if e := target.db.QueryRow("SELECT body FROM fb_operations WHERE id=?", id).Scan(&mirror); e != nil {
			t.Fatal(e)
		}
		if !bytes.Equal(original, mirror) {
			t.Fatal("handoff rewrote ciphertext", id)
		}
	}
	restarted := &Service{config: target.config, db: target.db, vaults: target.vaults}
	if e := restarted.guardArchiveWrite("Pi", "archive/future/photo.jpg", false); e == nil {
		t.Fatal("archive writable after restart")
	}
	var retained ArchiveLink
	if e := source.read(link.ID, "archiveLink", &retained); e != nil || retained.ID != link.ID {
		t.Fatal("coordinator record removed", e)
	}
	raw, e := os.ReadFile(output)
	if e != nil {
		t.Fatal(e)
	}
	var operations migrationOperations
	if e = json.Unmarshal(raw, &operations); e != nil {
		t.Fatal(e)
	}
	for i := range operations.Records {
		if operations.Records[i].Kind == "archiveLink" {
			var changed map[string]any
			if e = json.Unmarshal(operations.Records[i].Body, &changed); e != nil {
				t.Fatal(e)
			}
			changed["id"] = "tampered"
			operations.Records[i].Body, _ = json.Marshal(changed)
		}
	}
	raw, _ = json.Marshal(operations)
	if e = os.WriteFile(output, raw, 0600); e != nil {
		t.Fatal(e)
	}
	if e = MigrationOperations(testNode(t, "Pi").config, bundle, output, true, true); e == nil {
		t.Fatal("stale write policy accepted")
	}
}

func TestSourceHandoffPreservesTerminalTrashHistoryButRefusesTransitions(t *testing.T) {
	source := testNode(t, "Pi")
	target := testNode(t, "Pi")
	bundle := filepath.Join(t.TempDir(), "bundle.json")
	if e := os.WriteFile(bundle, []byte(`{"version":1,"source":"Pi","entries":[]}`), 0600); e != nil {
		t.Fatal(e)
	}
	for _, state := range []string{"failed", "missing"} {
		entry := Trash{ID: ID(), Source: "Pi", Original: "original-retained", Name: "original-retained", State: state}
		if e := source.insert(entry.ID, "trash", state, "", entry); e != nil {
			t.Fatal(e)
		}
	}
	output := filepath.Join(t.TempDir(), "history.json")
	if e := MigrationOperations(source.config, bundle, output, false); e != nil {
		t.Fatal(e)
	}
	if e := MigrationOperations(target.config, bundle, output, true); e != nil {
		t.Fatal(e)
	}
	for _, state := range []string{"failed", "missing"} {
		var count int
		if e := target.db.QueryRow("SELECT COUNT(*) FROM fb_operations WHERE kind='trash' AND state=?", state).Scan(&count); e != nil || count != 1 {
			t.Fatal("terminal history lost", state, count, e)
		}
	}
	entry := Trash{ID: ID(), Source: "Pi", Original: "original-retained", State: "restoring"}
	if e := source.insert(entry.ID, "trash", entry.State, "", entry); e != nil {
		t.Fatal(e)
	}
	if e := MigrationOperations(source.config, bundle, filepath.Join(t.TempDir(), "refused.json"), false); e == nil {
		t.Fatal("incomplete restore crossed handoff")
	}
}

func TestExplicitPiSchemaInitializationDoesNotRequireRunningBroker(t *testing.T) {
	state := t.TempDir()
	c := Config{State: state, SQLite: filepath.Join(state, "operations.sqlite"), ProtectedRuntime: filepath.Join(t.TempDir(), "broker-not-started")}
	if e := MigrateConfig(c); e != nil {
		t.Fatal("schema required broker", e)
	}
	d, e := openSQLite(c.SQLite)
	if e != nil {
		t.Fatal(e)
	}
	defer d.Close()
	var count int
	if e = d.QueryRow("SELECT COUNT(*) FROM fb_operations").Scan(&count); e != nil || count != 0 {
		t.Fatal(count, e)
	}
	if _, e = os.Stat(c.ProtectedRuntime); !os.IsNotExist(e) {
		t.Fatal("maintenance started broker", e)
	}
}
