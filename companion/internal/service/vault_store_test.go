package service

import (
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

func TestPrivateStoreNeverInheritsShareAccessOrDefaultACL(t *testing.T) {
	b := testBroker(t)
	root := b.config.Roots[0].Path
	acl := make([]byte, 4)
	binary.LittleEndian.PutUint32(acl, 2)
	for _, entry := range [][3]uint32{{1, 7, ^uint32(0)}, {2, 7, 4242}, {4, 5, ^uint32(0)}, {16, 7, ^uint32(0)}, {32, 5, ^uint32(0)}} {
		raw := make([]byte, 8)
		binary.LittleEndian.PutUint16(raw, uint16(entry[0]))
		binary.LittleEndian.PutUint16(raw[2:], uint16(entry[1]))
		binary.LittleEndian.PutUint32(raw[4:], entry[2])
		acl = append(acl, raw...)
	}
	if e := unix.Setxattr(root, "system.posix_acl_default", acl, 0); e != nil {
		t.Fatal(e)
	}
	marker := filepath.Join(root, "PrivateMarker")
	// The real broker has root's rename privilege; this fixture is unprivileged.
	if e := os.Mkdir(marker, 0700); e != nil {
		t.Fatal(e)
	}
	var identity unix.Stat_t
	if e := unix.Stat(marker, &identity); e != nil {
		t.Fatal(e)
	}
	entry := brokerEntry{ID: ID(), Source: "disk", Device: uint64(identity.Dev), Inode: identity.Ino}
	if e := b.renameMarker(entry, "PrivateMarker", filepath.Join(brokerStore, entry.ID+".public")); e != nil {
		t.Fatal("park with inherited share ACL", e)
	}
	path := filepath.Join(root, brokerStore)
	st, e := os.Stat(path)
	if e != nil || st.Mode().Perm() != 0700 {
		t.Fatal("private permissions", e, st)
	}
	for _, name := range []string{"system.posix_acl_access", "system.posix_acl_default"} {
		if _, e := unix.Getxattr(path, name, nil); !errors.Is(e, unix.ENODATA) {
			t.Fatal("share ACL reached private store", name, e)
		}
	}
	if e = os.Chmod(path, 0770); e != nil {
		t.Fatal(e)
	}
	if opened, e := b.openPrivateStore("disk"); e == nil {
		opened.Close()
		t.Fatal("unsafe existing store silently repaired")
	}
}
func TestNativeFilesystemConsumerUsesReaderOwnership(t *testing.T) {
	b := testBroker(t)
	b.config.FilesystemConsumer = true
	v := b.vaultConfig(brokerEntry{ID: "probe", Source: "disk"})
	if want := fmt.Sprintf("%d:%d", b.config.ReaderUID, b.config.GID); v.forceOwner != want {
		t.Fatal("native consumer ownership", v.forceOwner, want)
	}
}
