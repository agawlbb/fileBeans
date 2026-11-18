package service

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"filebeans/companion/protection"
)

func TestBrokerFolderLifecycle(t *testing.T)    { testBrokerFolderLifecycle(t, false) }
func TestBrokerFolderSMBLifecycle(t *testing.T) { testBrokerFolderLifecycle(t, true) }
func testBrokerFolderLifecycle(t *testing.T, smb bool) {
	if os.Getenv("FILEBEANS_TEST_FUSE") != "1" {
		t.Skip("isolated FUSE runtime required")
	}
	b := testBroker(t)
	if smb {
		mountSyntheticSMB(t, b)
	}
	b.config.Gocryptfs = os.Getenv("FILEBEANS_TEST_GOCRYPTFS")
	b.config.Fusermount = "/test-tools/fusermount3"
	b.manager.memoryCheck = func() error { return nil }
	key := bytes.Repeat([]byte{0x42}, 32)
	entry, err := b.reserve("disk", "/", "Private", "synthetic-folder-lifecycle")
	if err != nil {
		t.Fatal(err)
	}
	if err = b.open(entry, key); err != nil {
		t.Fatal(err)
	}
	public := b.publicPath(entry)
	if err = os.WriteFile(filepath.Join(public, "secret"), []byte("recoverable"), 0660); err != nil {
		t.Fatal(err)
	}
	if err = os.Mkdir(filepath.Join(b.config.Roots[0].Path, "Destination"), 0770); err != nil {
		t.Fatal(err)
	}
	action := func(kind, path string) {
		t.Helper()
		gate, err := b.exclusive()
		if err != nil {
			t.Fatal(err)
		}
		defer gate.Close()
		if err = b.folderAction(entry.ID, kind, path); err != nil {
			t.Fatal(kind, err)
		}
	}
	current := func() brokerEntry {
		for _, v := range b.registry.Entries {
			if v.ID == entry.ID {
				return v
			}
		}
		t.Fatal("entry missing")
		return brokerEntry{}
	}
	guard, err := protection.New(b.config.Runtime, map[string]string{"disk": b.config.Roots[0].Path})
	if err != nil {
		t.Fatal(err)
	}
	lease, err := guard.Acquire()
	if err != nil {
		t.Fatal(err)
	}
	if err = lease.Check(public, true); err != nil {
		t.Fatal("read root rejected", err)
	}
	if _, err = b.exclusive(); err != ErrVaultBusy {
		t.Fatal("active operation not protected", err)
	}
	lease.Close()
	action("relocate", "/Renamed")
	if _, err = os.Stat(public); !os.IsNotExist(err) {
		t.Fatal("old public path remains", err)
	}
	action("relocate", "/Destination/Moved")
	public = b.publicPath(current())
	if data, err := os.ReadFile(filepath.Join(public, "secret")); err != nil || string(data) != "recoverable" {
		t.Fatal("moved content", err)
	}
	if len(current().PreviousPaths) != 2 {
		t.Fatal("restore aliases missing")
	}
	// Previously deleted children still restore into the same encrypted files
	// directory after their protected parent has been renamed and moved.
	service := &Service{config: Config{Roots: b.config.Roots},
		vaults: &vaultManager{guard: guard, remote: newBrokerClient(b.config.Runtime, b.config.Roots)}}
	access, err := service.accessTrash(Trash{ID: "old-child", Source: "disk", Vault: entry.ID, Original: "Private/deleted.txt"})
	if err != nil {
		t.Fatal("old child Trash access", err)
	}
	if access.original != "deleted.txt" {
		t.Fatal("old child restore path", access.original)
	}
	access.close()
	service.vaults.remote.close()

	// A conflict cannot overwrite ordinary destination data or lose the source.
	if err = os.Mkdir(filepath.Join(b.config.Roots[0].Path, "Existing"), 0700); err != nil {
		t.Fatal(err)
	}
	if err = b.folderAction(entry.ID, "relocate", "/Existing"); err != ErrConflict {
		t.Fatal("destination conflict", err)
	}
	if _, err = os.Stat(filepath.Join(public, "secret")); err != nil {
		t.Fatal("conflict damaged source", err)
	}
	action("trash", "")
	if _, err = os.Stat(public); !os.IsNotExist(err) {
		t.Fatal("deleted folder visible", err)
	}
	var snapshot brokerSnapshot
	data, err := os.ReadFile(filepath.Join(b.config.Runtime, "status.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(data, &snapshot); err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Vaults) != 0 {
		t.Fatal("trashed vault leaked into public boundary registry")
	}
	if b.statuses()[0].State != "unlocked" {
		t.Fatal("delete discarded active session")
	}
	// Locked root trash is hidden by the listing policy; ordinary unlock still
	// opens it privately so that Restore can expose it again.
	if err = b.lock(current()); err != nil {
		t.Fatal(err)
	}
	if err = b.open(current(), key); err != nil {
		t.Fatal("unlock trashed", err)
	}
	action("restore", "")
	if data, err := os.ReadFile(filepath.Join(public, "secret")); err != nil || string(data) != "recoverable" {
		t.Fatal("restored content", err)
	}
	// The same private bin survives folder path changes.
	if err = os.WriteFile(filepath.Join(b.vaultConfig(current()).Mount, "private", "cache", "probe"), []byte("cache"), 0600); err != nil {
		t.Fatal(err)
	}
	watches := map[string]*vaultIdleWatch{}
	b.pollIdleVaults(watches, time.Now())
	for _, w := range watches {
		w.close()
	}
	action("trash", "")
	action("purge", "")
	if len(b.registry.Entries) != 0 {
		t.Fatal("purged vault still registered")
	}
	if _, err = os.Stat(filepath.Join(b.config.Roots[0].Path, brokerStore, entry.ID)); !os.IsNotExist(err) {
		t.Fatal("ciphertext remains after purge", err)
	}
}

// Simulate storage completing a rename before the final registry write.
func TestBrokerRecoversInterruptedFolderRename(t *testing.T) {
	b := testBroker(t)
	entry, err := b.reserve("disk", "/", "Before", "synthetic-rename-recovery")
	if err != nil {
		t.Fatal(err)
	}
	entry.NextPath = "/After"
	b.registry.Entries[0] = entry
	if err = b.saveRegistry(); err != nil {
		t.Fatal(err)
	}
	if err = os.Rename(b.publicPath(entry), filepath.Join(b.config.Roots[0].Path, "After")); err != nil {
		t.Fatal(err)
	}
	b.manager.lockFile.Close()
	b.manager.lockFile = nil
	recovered, err := newMountBrokerWithChecks(b.config, func(brokerEntry) bool { return true })
	if err != nil {
		t.Fatal("recover completed rename", err)
	}
	defer recovered.close()
	result := recovered.registry.Entries[0]
	if result.Path != "/After" || result.NextPath != "" {
		t.Fatal("rename journal not completed")
	}
	if len(result.PreviousPaths) != 1 || result.PreviousPaths[0] != "/Before" {
		t.Fatal("old Trash restore path lost")
	}
}
