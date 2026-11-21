package service

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"

	"golang.org/x/sys/unix"
)

func TestBrokerCapabilities(t *testing.T) {
	// Reported production masks: bounding included SETUID, but permitted and
	// effective did not. The broker must reject this before opening any vault.
	bad := [2]unix.CapUserData{{Permitted: 0x20004b, Effective: 0x20004b}}
	if err := checkBrokerCapabilities(bad); !errors.Is(err, ErrBrokerCapabilities) || !strings.Contains(err.Error(), "CAP_SETUID") {
		t.Fatal("accepted missing SETUID", err)
	}
	good := bad
	good[0].Permitted |= 1 << unix.CAP_SETUID
	good[0].Effective |= 1 << unix.CAP_SETUID
	if err := checkBrokerCapabilities(good); err != nil {
		t.Fatal(err)
	}
	for _, capability := range brokerCapabilities {
		for _, permitted := range []bool{false, true} {
			data := good
			if permitted {
				data[capability.bit/32].Permitted &^= 1 << (capability.bit % 32)
			} else {
				data[capability.bit/32].Effective &^= 1 << (capability.bit % 32)
			}
			if err := checkBrokerCapabilities(data); !errors.Is(err, ErrBrokerCapabilities) || !strings.Contains(err.Error(), capability.name) {
				t.Fatalf("accepted missing %s (permitted=%v): %v", capability.name, permitted, err)
			}
		}
	}
}

func TestBrokerLocalVaultUnprivilegedWrites(t *testing.T) {
	if os.Getenv("FILEBEANS_TEST_LOCAL_VAULT_WRITER") == "1" {
		if os.Geteuid() != 1000 {
			t.Fatal("writer must run as UID 1000")
		}
		if err := unix.Mkdirat(3, "companion-created", 0770); err != nil {
			t.Fatal("unprivileged protected mkdir", err)
		}
		fd, err := unix.Openat(3, "companion-created/text.txt", unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL, 0660)
		if err != nil {
			t.Fatal(err)
		}
		file := os.NewFile(uintptr(fd), "text.txt")
		_, err = file.WriteString("protected companion write")
		closeErr := file.Close()
		if err != nil || closeErr != nil {
			t.Fatal(err, closeErr)
		}
		return
	}
	if os.Getenv("FILEBEANS_TEST_FUSE") != "1" {
		t.Skip("isolated FUSE runtime required")
	}
	b := testBroker(t)
	b.config.Gocryptfs = os.Getenv("FILEBEANS_TEST_GOCRYPTFS")
	b.config.Fusermount = "/test-tools/fusermount3"
	b.manager.memoryCheck = func() error { return nil }
	entry, err := b.reserve("disk", "/", "Private", "local-unprivileged-writer")
	if err != nil {
		t.Fatal(err)
	}
	if err = b.open(entry, bytes.Repeat([]byte{0x42}, 32)); err != nil {
		t.Fatal(err)
	}
	if b.vaultConfig(entry).forceOwner != "" {
		t.Fatal("local regression must exercise gocryptfs ownership preservation")
	}
	dir, err := os.Open(b.publicPath(entry))
	if err != nil {
		t.Fatal(err)
	}
	defer dir.Close()
	cmd := exec.Command(os.Args[0], "-test.run=^TestBrokerLocalVaultUnprivilegedWrites$", "-test.v")
	cmd.Env = append(os.Environ(), "FILEBEANS_TEST_LOCAL_VAULT_WRITER=1")
	cmd.ExtraFiles = []*os.File{dir}
	cmd.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: 1000, Gid: uint32(b.config.GID)}}
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("companion identity cannot write: %v\n%s", err, out)
	}
	path := filepath.Join(b.publicPath(entry), "companion-created/text.txt")
	if content, err := os.ReadFile(path); err != nil || string(content) != "protected companion write" {
		t.Fatal("protected content verification", err)
	}
	var st unix.Stat_t
	if err = unix.Stat(path, &st); err != nil || st.Uid != 1000 || st.Gid != uint32(b.config.GID) {
		t.Fatal("requesting writer ownership was not preserved", st.Uid, st.Gid, err)
	}
}

// Runs in a disposable root test container. Credential changes affect only an
// isolated subprocess thread, never the test runner or host service.
func TestBrokerSetUIDCredentialSwitch(t *testing.T) {
	if os.Getenv("FILEBEANS_TEST_CREDENTIAL_SWITCH") == "" {
		if os.Geteuid() != 0 {
			t.Skip("isolated root test runtime required")
		}
		for _, mode := range []string{"present", "missing"} {
			cmd := exec.Command(os.Args[0], "-test.run=^TestBrokerSetUIDCredentialSwitch$", "-test.v")
			cmd.Env = append(os.Environ(), "FILEBEANS_TEST_CREDENTIAL_SWITCH="+mode)
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("%s: %v\n%s", mode, err, out)
			}
		}
		return
	}
	runtime.LockOSThread()
	// Leave this thread locked: it has deliberately changed credentials/caps.
	header := unix.CapUserHeader{Version: unix.LINUX_CAPABILITY_VERSION_3}
	var data [2]unix.CapUserData
	if err := unix.Capget(&header, &data[0]); err != nil {
		t.Fatal(err)
	}
	if data[0].Effective&(1<<unix.CAP_SETUID) == 0 {
		t.Fatal("test runtime must initially grant CAP_SETUID")
	}
	missing := os.Getenv("FILEBEANS_TEST_CREDENTIAL_SWITCH") == "missing"
	if missing {
		data[0].Effective &^= 1 << unix.CAP_SETUID
		data[0].Permitted &^= 1 << unix.CAP_SETUID
		if err := unix.Capset(&header, &data[0]); err != nil {
			t.Fatal(err)
		}
		if err := requireBrokerCapabilities(); !errors.Is(err, ErrBrokerCapabilities) || !strings.Contains(err.Error(), "CAP_SETUID") {
			t.Fatal("startup did not identify the missing process capability", err)
		}
	}
	// Match gocryptfs's thread-local Setreuid(-1, requestingUID).
	_, _, errno := unix.RawSyscall(unix.SYS_SETREUID, ^uintptr(0), 1000, 0)
	if missing {
		if errno != unix.EPERM {
			t.Fatalf("without SETUID: want EPERM, got %v", errno)
		}
	} else if errno != 0 {
		t.Fatalf("with SETUID: %v", errno)
	}
}
