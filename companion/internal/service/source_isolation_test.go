package service

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

func TestBrokerStartsWithUnavailableSource(t *testing.T) {
	for _, failure := range []string{"device", "inode", "missing", "wrong-share"} {
		t.Run(failure, func(t *testing.T) {
			b := testBroker(t)
			healthy := b.config.Roots[0]
			remote := Root{Name: "remote", Path: filepath.Join(filepath.Dir(healthy.Path), "remote")}
			if err := os.Mkdir(remote.Path, 0700); err != nil {
				t.Fatal(err)
			}
			b.config.Roots = append(b.config.Roots, remote)
			r := brokerRegistry{Version: privateRegistryVersion}
			for i, root := range b.config.Roots {
				id := strings.Repeat(string(rune('a'+i)), 32)
				marker := filepath.Join(root.Path, brokerStore, id+".public")
				if err := os.MkdirAll(marker, 0700); err != nil {
					t.Fatal(err)
				}
				var st unix.Stat_t
				if err := unix.Stat(marker, &st); err != nil {
					t.Fatal(err)
				}
				r.Entries = append(r.Entries, brokerEntry{ID: id, Source: root.Name, RequestHash: strings.Repeat(string(rune('c'+i)), 64),
					Parked: true, Initialized: true, Device: uint64(st.Dev), Inode: st.Ino,
					Location: &sealedLocation{Nonce: bytes.Repeat([]byte{1}, 12), Data: bytes.Repeat([]byte{2}, 32)}})
			}
			switch failure {
			case "device":
				r.Entries[1].Device++
			case "inode":
				r.Entries[1].Inode++
			case "missing":
				if err := os.RemoveAll(remote.Path); err != nil {
					t.Fatal(err)
				}
			case "wrong-share":
				b.config.Roots[1].Mount = remote.Path
				b.config.Roots[1].MountSource = "//unexpected/share"
			}
			if err := brokerAtomicJSON(filepath.Join(b.config.State, "registry.json"), r, 0600, b.config.GID); err != nil {
				t.Fatal(err)
			}
			b.close()
			reopened, err := newMountBrokerWithChecks(b.config, b.lockedCheck)
			if err != nil {
				t.Fatal("one source prevented startup", err)
			}
			defer reopened.close()
			if _, err := reopened.root(healthy.Name); err != nil {
				t.Fatal("healthy source blocked", err)
			}
			if _, err := reopened.root(remote.Name); err == nil {
				t.Fatal("failed source accepted")
			}
			if _, err := reopened.reserve(remote.Name, "/", "Unsafe", "source-isolation-request"); err == nil {
				t.Fatal("failed source accepted writes")
			}
			statuses := reopened.statuses()
			if len(statuses) != 2 || statuses[0].State != "locked" || statuses[1].State != "error" {
				t.Fatalf("statuses: %+v", statuses)
			}
			data, err := os.ReadFile(filepath.Join(b.config.Runtime, "status.json"))
			if err != nil {
				t.Fatal(err)
			}
			var snapshot brokerSnapshot
			if err := json.Unmarshal(data, &snapshot); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(snapshot.UnavailableSources, []string{remote.Name}) {
				t.Fatalf("missing source denial: %+v", snapshot)
			}
			stored, err := os.ReadFile(filepath.Join(b.config.State, "registry.json"))
			if err != nil {
				t.Fatal(err)
			}
			var after brokerRegistry
			if err := json.Unmarshal(stored, &after); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(after, r) {
				t.Fatal("recovery changed stored identities or encrypted locations")
			}
			if failure == "wrong-share" {
				reopened.config.Roots[1].Mount = ""
				reopened.config.Roots[1].MountSource = ""
				reopened.retryUnavailableSources()
				if _, err := reopened.root(remote.Name); err != nil {
					t.Fatal("returning storage required restart", err)
				}
			}

		})
	}
}

func TestBrokerCIFSBootIdentityReconciliation(t *testing.T) {
	if os.Getenv("FILEBEANS_TEST_FUSE") != "1" {
		t.Skip("isolated CIFS runtime required")
	}
	for _, mismatch := range []bool{false, true} {
		t.Run(map[bool]string{false: "remounted", true: "replaced-marker"}[mismatch], func(t *testing.T) {
			b := testBroker(t)
			mountSyntheticSMB(t, b)
			root := b.config.Roots[0]
			r := brokerRegistry{Version: privateRegistryVersion}
			for i := 0; i < 2; i++ {
				id := strings.Repeat(string(rune('a'+i)), 32)
				marker := filepath.Join(root.Path, brokerStore, id+".public")
				if err := os.MkdirAll(marker, 0700); err != nil {
					t.Fatal(err)
				}
				cipher := filepath.Join(root.Path, brokerStore, id)
				if err := os.MkdirAll(cipher, 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(cipher, "gocryptfs.conf"), []byte("synthetic"), 0600); err != nil {
					t.Fatal(err)
				}
				var st unix.Stat_t
				if err := unix.Stat(marker, &st); err != nil {
					t.Fatal(err)
				}
				r.Entries = append(r.Entries, brokerEntry{ID: id, Source: root.Name, RequestHash: strings.Repeat(string(rune('c'+i)), 64),
					Parked: true, Initialized: true, Device: uint64(st.Dev) + 100, Inode: st.Ino,
					Location: &sealedLocation{Nonce: bytes.Repeat([]byte{1}, 12), Data: bytes.Repeat([]byte{2}, 32)}})
			}
			if mismatch {
				r.Entries[1].Inode++
			}
			if err := brokerAtomicJSON(filepath.Join(b.config.State, "registry.json"), r, 0600, b.config.GID); err != nil {
				t.Fatal(err)
			}
			b.close()
			reopened, err := newMountBrokerWithChecks(b.config, b.lockedCheck)
			if err != nil {
				t.Fatal("CIFS reboot prevented startup", err)
			}
			defer reopened.close()
			_, rootErr := reopened.root(root.Name)
			if mismatch {
				if rootErr == nil {
					t.Fatal("replaced marker accepted")
				}
				if !reflect.DeepEqual(reopened.registry, r) {
					t.Fatal("partial identity refresh on marker mismatch")
				}
			} else {
				if rootErr != nil {
					t.Fatal("valid remount not recovered", rootErr, reopened.unavailableSources)
				}
				var st unix.Stat_t
				if err := unix.Stat(root.Path, &st); err != nil {
					t.Fatal(err)
				}
				for i, e := range reopened.registry.Entries {
					if e.Device != uint64(st.Dev) || e.Inode != r.Entries[i].Inode || !reflect.DeepEqual(e.Location, r.Entries[i].Location) {
						t.Fatal("incorrect reconciliation")
					}
				}
			}
		})
	}
}

func TestSambaChildMountReachesExistingConsumer(t *testing.T) {
	if os.Getenv("FILEBEANS_TEST_FUSE") != "1" {
		t.Skip("isolated CIFS runtime required")
	}
	b := testBroker(t)
	parent := b.config.Roots[0].Path
	consumer := filepath.Join(filepath.Dir(parent), "consumer")
	if err := os.Mkdir(consumer, 0700); err != nil {
		t.Fatal(err)
	}
	if err := unix.Mount(parent, parent, "", unix.MS_BIND, ""); err != nil {
		t.Fatal(err)
	}
	if err := unix.Mount("", parent, "", unix.MS_SHARED|unix.MS_REC, ""); err != nil {
		t.Fatal(err)
	}
	if err := unix.Mount(parent, consumer, "", unix.MS_BIND|unix.MS_REC, ""); err != nil {
		t.Fatal(err)
	}
	if err := unix.Mount("", consumer, "", unix.MS_SLAVE|unix.MS_REC, ""); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := unix.Unmount(consumer, 0); err != nil {
			t.Error(err)
		}
		if err := unix.Unmount(parent, 0); err != nil {
			t.Error(err)
		}
	})
	share := filepath.Join(parent, "share")
	if err := os.Mkdir(share, 0700); err != nil {
		t.Fatal(err)
	}
	b.config.Roots[0].Path = share
	mountSyntheticSMB(t, b)
	probe := filepath.Join(share, "mounted-later")
	if err := os.WriteFile(probe, []byte("propagated"), 0600); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(consumer, "share", "mounted-later"))
	if err != nil || string(data) != "propagated" {
		t.Fatal("returning share did not reach existing consumer", err)
	}
}
