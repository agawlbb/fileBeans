package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"filebeans/companion/protection"
	"fmt"
	"golang.org/x/sys/unix"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// This orchestrator exists only in the test executable. The fixture endpoints
// simulate the mobile companion without giving Quantum the filesystem key.
func TestBrokerQuantumIntegration(t *testing.T) {
	runBrokerQuantumIntegration(t, false)
}

func TestBrokerQuantumSMBIntegration(t *testing.T) {
	runBrokerQuantumIntegration(t, true)
}

func runBrokerQuantumIntegration(t *testing.T, smb bool) {
	if os.Getenv("FILEBEANS_TEST_FUSE") != "1" || os.Getenv("FILEBEANS_TEST_QUANTUM") == "" {
		t.Skip("isolated Quantum/FUSE runtime required")
	}
	b := testBroker(t)
	b.config.Roots[0].Name = "uploads"
	physical := ""
	if smb {
		physical = mountSyntheticSMB(t, b)
	}
	b.config.Gocryptfs = os.Getenv("FILEBEANS_TEST_GOCRYPTFS")
	b.config.Fusermount = "/test-tools/fusermount3"
	b.manager.memoryCheck = func() error { return nil } // synthetic test keys only
	var normalizationRoot rootFS
	if !smb {
		guard, e := protection.New(b.config.Runtime, map[string]string{"uploads": b.config.Roots[0].Path})
		if e != nil {
			t.Fatal(e)
		}
		worker := &Service{config: Config{Roots: b.config.Roots, Normalization: NormalizationConfig{Enabled: true, Source: "uploads", Path: "ordinary"}},
			vaults: &vaultManager{guard: guard, remote: newBrokerClient(b.config.Runtime, b.config.Roots)}}
		normalizationRoot, _, e = worker.normalizationRoot()
		if e != nil {
			t.Fatal(e)
		}
		// Model a long conversion: keep this root open throughout creation,
		// preview operations and lock/unlock in the separate Quantum process.
		defer normalizationRoot.Close()
	}
	base := filepath.Dir(b.config.Runtime)
	for _, p := range []string{filepath.Dir(base), base} {
		if e := os.Chmod(p, 0750); e != nil {
			t.Fatal(e)
		}
	}
	if e := os.Chmod(b.config.Roots[0].Path, 0770); e != nil {
		t.Fatal(e)
	}
	// Shared only inside this disposable test namespace. The reader helper
	// creates a separate slave namespace before dropping to the Quantum UID.
	if e := unix.Mount(base, base, "", unix.MS_BIND|unix.MS_REC, ""); e != nil {
		t.Fatal(e)
	}
	if e := unix.Mount("", base, "", unix.MS_SHARED|unix.MS_REC, ""); e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() {
		b.close()
		for _, entry := range b.registry.Entries {
			if b.isLockedMount(entry) {
				_ = b.unmountFiles(b.publicPath(entry))
			}
		}
		if smb {
			if e := unix.Unmount(b.config.Roots[0].Path, 0); e != nil {
				t.Error("CIFS slave fixture cleanup", e)
			}
		}
		if e := unix.Unmount(base, 0); e != nil {
			t.Error("shared fixture cleanup", e)
		}
	})
	listener, e := net.Listen("unix", filepath.Join(b.config.Runtime, "broker.sock"))
	if e != nil {
		t.Fatal(e)
	}
	if e = os.Chmod(filepath.Join(b.config.Runtime, "broker.sock"), 0660); e != nil {
		t.Fatal(e)
	}
	key := bytes.Repeat([]byte{0x42}, 32)
	var id string
	handler := b.handler()
	server := &http.Server{ConnContext: brokerPeerContext, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/fixture/") {
			handler.ServeHTTP(w, r)
			return
		}
		peer, _ := r.Context().Value(brokerPeer{}).(uint32)
		if peer != b.config.ReaderUID || r.Method != "POST" {
			w.WriteHeader(403)
			return
		}
		if r.URL.Path == "/fixture/shared-writer" {
			cmd := exec.Command(os.Args[0], "-test.run=^TestVaultSharedWriterHelper$", "-test.v")
			cmd.Env = append(os.Environ(), "FILEBEANS_VAULT_RUNTIME="+b.config.Runtime, "FILEBEANS_VAULT_TEST_WRITER_ROOT="+b.config.Roots[0].Path)
			cmd.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: b.config.ReaderUID + 1, Gid: uint32(b.config.GID)}}
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Log(string(out))
				http.Error(w, "shared writer failed", 500)
				return
			}
			w.WriteHeader(200)
			return
		}
		route, body := "", []byte(nil)
		switch r.URL.Path {
		case "/fixture/create":
			route = "/v1/vaults"
			body, _ = json.Marshal(map[string]string{"source": "uploads", "parent": "/", "name": "Private", "key": encodeVaultKey(key)})
		case "/fixture/lock":
			route = "/v1/vaults/" + id + "/lock"
		case "/fixture/unlock":
			route = "/v1/vaults/" + id + "/unlock"
			body, _ = json.Marshal(map[string]string{"key": encodeVaultKey(key)})
		default:
			w.WriteHeader(404)
			return
		}
		forward := httptest.NewRequest("POST", route, bytes.NewReader(body))
		forward.Header.Set("Idempotency-Key", "synthetic-quantum-integration")
		forward = forward.WithContext(context.WithValue(r.Context(), brokerPeer{}, b.config.ControlUID))
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, forward)
		if route == "/v1/vaults" && response.Code == 201 {
			var v VaultStatus
			_ = json.Unmarshal(response.Body.Bytes(), &v)
			id = v.ID
			if normalizationRoot != nil {
				if e := accessibleDirectory(normalizationRoot, "."); e != nil {
					t.Errorf("normalizer rejected an ordinary ancestor: %v", e)
				}
				if e := normalizationRoot.WriteFile("normalizer-ordinary.txt", []byte("ordinary job"), 0600); e != nil {
					t.Errorf("normalizer could not continue ordinary work: %v", e)
				}
				if _, e := normalizationRoot.Stat("Private"); !errors.Is(e, ErrUnsafe) {
					t.Errorf("normalizer accepted a newly protected folder: %v", e)
				}
				if e := normalizationRoot.WriteFile("Private/forbidden.txt", []byte("must never publish"), 0600); !errors.Is(e, ErrUnsafe) {
					t.Errorf("normalizer wrote into a new vault: %v", e)
				}
			}
		}
		w.WriteHeader(response.Code)
		_, _ = w.Write(response.Body.Bytes())
	})}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { _ = server.Close() })
	sources, _ := json.Marshal(map[string]string{"uploads": b.config.Roots[0].Path})
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestVaultNamespaceExecHelper$", "-test.v", "-test.timeout=55s")
	cmd.Env = append(os.Environ(), "FILEBEANS_VAULT_RUNTIME="+b.config.Runtime, "FILEBEANS_VAULT_SOURCES="+string(sources), "FILEBEANS_VAULT_TEST_ROOT="+b.config.Roots[0].Path,
		"FILEBEANS_VAULT_NAMESPACE_BASE="+base, "FILEBEANS_VAULT_TEST_UID="+strconv.Itoa(int(b.config.ReaderUID)), "FILEBEANS_VAULT_TEST_GID="+strconv.Itoa(b.config.GID))
	cmd.SysProcAttr = &syscall.SysProcAttr{Cloneflags: unix.CLONE_NEWNS}
	output, e := cmd.CombinedOutput()
	t.Log(string(output))
	if e != nil {
		t.Fatal("unprivileged Quantum integration failed", e)
	}
	// Recover the app-created data without registry, database, broker or mobile
	// code. Only the copied ciphertext and the middle field of the saved code
	// are supplied to an independent, standard gocryptfs process.
	recoverBrokerCiphertext(t, filepath.Join(b.config.Roots[0].Path, brokerStore, id), encodeVaultKey(key))
	if physical != "" {
		if e = filepath.Walk(physical, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			if strings.Contains(info.Name(), "secret.") {
				t.Fatal("SMB stores plaintext filename", path)
			}
			if !info.IsDir() {
				data, err := os.ReadFile(path)
				if err != nil {
					return err
				}
				if bytes.Contains(data, []byte("private text")) {
					t.Fatal("SMB stores plaintext content", path)
				}
			}
			return nil
		}); e != nil {
			t.Fatal(e)
		}
	}
}

func TestVaultSharedWriterHelper(t *testing.T) {
	path := os.Getenv("FILEBEANS_VAULT_TEST_WRITER_ROOT")
	if path == "" {
		t.Skip("synthetic cross-process fixture required")
	}
	unix.Umask(0007)
	g, e := protection.New(os.Getenv("FILEBEANS_VAULT_RUNTIME"), map[string]string{"uploads": path})
	if e != nil {
		t.Fatal(e)
	}
	if e = g.RequirePrimaryGroup(); e != nil {
		t.Fatal(e)
	}
	lease, e := g.Acquire()
	if e != nil {
		t.Fatal(e)
	}
	root, e := os.OpenRoot(path)
	if e != nil {
		lease.Close()
		t.Fatal(e)
	}
	r := &guardedRoot{Root: root, lease: lease}
	defer r.Close()
	if data, e := r.ReadFile("Private/secret.txt"); e != nil || string(data) != "private text" {
		t.Fatal("companion cannot read Quantum upload", e)
	}
	if e = accessibleDirectory(r, "Private/shared"); e != nil {
		t.Fatal("worker rejected an already shared Quantum directory", e)
	}
	st, e := r.Stat("Private/notshared")
	if e != nil {
		t.Fatal(e)
	}
	e = accessibleDirectory(r, "Private/notshared")
	// CIFS fixed masks can make even a requested 0700 directory shared.
	if st.Mode().Perm()&0770 != 0770 && e == nil {
		t.Fatal("worker accepted an inaccessible directory")
	}
	if st.Mode().Perm()&0770 == 0770 && e != nil {
		t.Fatal("worker rejected the CIFS sharing mask", e)
	}

	for _, prefix := range []string{"", "Private"} {
		for _, kind := range []string{"file", "folder"} {
			t.Run("delete-restore-purge-"+prefix+"-"+kind, func(t *testing.T) {
				original := filepath.Join(prefix, "shared-delete-"+kind)
				suffix := ""
				if kind == "folder" {
					suffix = "payload"
				}
				verify := func(base string) {
					t.Helper()
					if data, err := r.ReadFile(filepath.Join(base, suffix)); err != nil || string(data) != "delete payload" {
						t.Fatal("contents damaged", err)
					}
					if kind == "folder" {
						if st, err := r.Stat(filepath.Join(base, "empty")); err != nil || !st.IsDir() {
							t.Fatal("empty folder lost", err)
						}
					}
				}
				verify(original)
				entry := Trash{ID: ID(), Original: original, Name: filepath.Base(original), Storage: filepath.Join(prefix, "Trash", ID())}
				if err := retainTrashFile(r, entry); err != nil {
					t.Fatal("delete through shared Trash", err)
				}
				if _, err := r.Stat(original); !errors.Is(err, os.ErrNotExist) {
					t.Fatal("delete left original", err)
				}
				verify(trashPath(entry))
				if err := renameExclusive(r, trashPath(entry), original); err != nil {
					t.Fatal("restore", err)
				}
				verify(original)
				if err := r.RemoveAll(trashDirectory(entry)); err != nil {
					t.Fatal(err)
				}
				if err := retainTrashFile(r, entry); err != nil {
					t.Fatal("repeat delete", err)
				}
				if err := r.RemoveAll(trashDirectory(entry)); err != nil {
					t.Fatal("permanent deletion", err)
				}
				if _, err := r.Stat(trashPath(entry)); !errors.Is(err, os.ErrNotExist) {
					t.Fatal("purge retained file", err)
				}
			})
		}
	}

	t.Run("private-trash-outside-public-folder", func(t *testing.T) {
		var id string
		for _, v := range lease.Snapshot.Vaults {
			if v.Path == "/Private" {
				id = v.ID
			}
		}
		if id == "" {
			t.Fatal("fixture vault missing")
		}
		roots := []Root{{Name: "uploads", Path: path}}
		service := &Service{config: Config{Roots: roots}, vaults: &vaultManager{guard: g, remote: newBrokerClient(os.Getenv("FILEBEANS_VAULT_RUNTIME"), roots)}}
		original := "Private/private-delete"
		if e := r.WriteFile(original, []byte("private bin"), 0660); e != nil {
			t.Fatal(e)
		}
		entry := Trash{ID: ID(), Source: "uploads", Original: original, Name: "private-delete", Vault: id}
		entry.Storage = filepath.Join("trash", entry.ID)
		a, e := service.accessTrash(entry)
		if e != nil {
			t.Fatal(e)
		}
		defer a.close()
		if e = retainTrashAccess(a, entry); e != nil {
			t.Fatal(e)
		}
		if _, e = r.Stat(original); !errors.Is(e, os.ErrNotExist) {
			t.Fatal("original remains", e)
		}
		if data, e := a.bin.ReadFile(trashPath(entry)); e != nil || string(data) != "private bin" {
			t.Fatal(e)
		}
		if e = renameRoots(a.bin, trashPath(entry), a.files, a.original); e != nil {
			t.Fatal("private restore", e)
		}
		if e = a.bin.RemoveAll(trashDirectory(entry)); e != nil {
			t.Fatal(e)
		}
		if e = retainTrashAccess(a, entry); e != nil {
			t.Fatal(e)
		}
		if e = a.bin.RemoveAll(trashDirectory(entry)); e != nil {
			t.Fatal(e)
		}
		if e = removeEmptyTrashRoot(r); e != nil && !errors.Is(e, os.ErrNotExist) {
			t.Fatal(e)
		}
	})
	copyCommand := exec.Command("/test-tools/rclone", "--config", "/dev/null", "--local-no-set-modtime", "copyto",
		filepath.Join(path, "Private/secret.txt"), filepath.Join(path, "Private/shared/rclone-copy.txt"))
	if output, err := copyCommand.CombinedOutput(); err != nil {
		t.Fatal("separate-UID protected rclone transfer", err, string(output))
	}
	if data, err := r.ReadFile("Private/shared/rclone-copy.txt"); err != nil || string(data) != "private text" {
		t.Fatal("protected rclone transfer content", err)
	}

	// Exercise the same verified source-removal primitive on real FUSE and CIFS,
	// in both directions, while all fixture data and keys remain disposable.
	for i, pair := range [][2]string{{"move-in", "Private/shared/move-in"}, {"Private/shared/move-out", "move-out"}} {
		from, to := pair[0], pair[1]
		if e = r.WriteFile(from, []byte("move payload"), 0660); e != nil {
			t.Fatal(e)
		}
		entries, err := snapshotMove(context.Background(), r, from)
		if err != nil {
			t.Fatal(err)
		}
		command := exec.Command("/test-tools/rclone", "--config", "/dev/null", "--local-no-set-modtime", "copyto", filepath.Join(path, from), filepath.Join(path, to))
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatal(err, string(output))
		}
		if err = syncMoveTree(r, to, entries); err != nil {
			t.Fatal(err)
		}
		prefix := ".filebeans-stage"
		if strings.HasPrefix(from, "Private/") {
			prefix = "Private/.filebeans-stage"
		}
		m := MoveSelection{Path: from, To: filepath.Base(to), Quarantine: filepath.Join(prefix, fmt.Sprintf("move-fixture-%d", i)), Entries: entries}
		persisted := 0
		if err = finishMoveSource(context.Background(), r, r, filepath.Dir(to), &m, func() error { persisted++; return nil }); err != nil {
			t.Fatal(err)
		}
		if !m.Removed || persisted != 2 {
			t.Fatal("move did not commit cleanup")
		}
		if _, err = r.Stat(from); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("source retained", err)
		}
		if data, err := r.ReadFile(to); err != nil || string(data) != "move payload" {
			t.Fatal("destination damaged", err)
		}
	}
	if e = r.WriteFile("Private/shared/transfer.txt", []byte("shared writer"), 0660); e != nil {
		t.Fatal(e)
	}
	if e = renameExclusive(r, "Private/shared/transfer.txt", "Private/shared/renamed.txt"); e != nil {
		t.Fatal("protected rename", e)
	}
	if e = r.MkdirAll("Private/.filebeans-stage/trash", 0770); e != nil {
		t.Fatal(e)
	}
	if e = renameExclusive(r, "Private/shared/renamed.txt", "Private/.filebeans-stage/trash/renamed.txt"); e != nil {
		t.Fatal("protected staging", e)
	}
	if e = renameExclusive(r, "Private/.filebeans-stage/trash/renamed.txt", "Private/shared/restored.txt"); e != nil {
		t.Fatal("protected restoration", e)
	}
}

func recoverBrokerCiphertext(t *testing.T, cipher, code string) {
	t.Helper()
	backup := t.TempDir()
	if e := filepath.Walk(cipher, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(cipher, path)
		if err != nil {
			return err
		}
		target := filepath.Join(backup, rel)
		if info.IsDir() {
			return os.MkdirAll(target, 0700)
		}
		in, err := os.Open(path)
		if err != nil {
			return err
		}
		defer in.Close()
		out, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			return err
		}
		_, err = io.Copy(out, in)
		closeErr := out.Close()
		if err != nil {
			return err
		}
		return closeErr
	}); e != nil {
		t.Fatal("ciphertext backup", e)
	}
	mount := t.TempDir()
	cmd := exec.Command(os.Getenv("FILEBEANS_TEST_GOCRYPTFS"), "-fg", "-nosyslog", "-masterkey=stdin", backup, mount)
	cmd.Stdin = strings.NewReader(strings.Split(code, ":")[1] + "\n")
	if e := cmd.Start(); e != nil {
		t.Fatal(e)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	defer func() {
		_ = exec.Command("/test-tools/fusermount3", "-u", mount).Run()
		_ = cmd.Process.Kill()
		<-done
	}()
	for deadline := time.Now().Add(5 * time.Second); !mounted(mount, backup); {
		if time.Now().After(deadline) {
			t.Fatal("independent recovery mount unavailable")
		}
		time.Sleep(25 * time.Millisecond)
	}
	got, e := os.ReadFile(filepath.Join(mount, "files", "secret.txt"))
	if e != nil || string(got) != "private text" {
		t.Fatal("broker-created data could not be recovered independently", e)
	}
	for _, name := range []string{"secret.jpg", "private-video.mp4"} {
		if info, e := os.Stat(filepath.Join(mount, "files", name)); e != nil || info.Size() == 0 {
			t.Fatal("missing recovered media", name, e)
		}
	}
}

func TestVaultNamespaceExecHelper(t *testing.T) {
	base := os.Getenv("FILEBEANS_VAULT_NAMESPACE_BASE")
	if base == "" {
		t.Skip("test orchestrator required")
	}
	if e := unix.Mount("", base, "", unix.MS_SLAVE|unix.MS_REC, ""); e != nil {
		t.Fatal(e)
	}
	uid, e := strconv.Atoi(os.Getenv("FILEBEANS_VAULT_TEST_UID"))
	if e != nil {
		t.Fatal(e)
	}
	gid, e := strconv.Atoi(os.Getenv("FILEBEANS_VAULT_TEST_GID"))
	if e != nil {
		t.Fatal(e)
	}
	cmd := exec.Command(os.Getenv("FILEBEANS_TEST_QUANTUM"), "-test.run=^TestVaultFilesystemIntegration$", "-test.v", "-test.timeout=50s")
	cmd.Env = os.Environ()
	cmd.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: uint32(uid), Gid: uint32(gid)}}
	output, e := cmd.CombinedOutput()
	t.Log(string(output))
	if e != nil {
		t.Fatal("slave-namespace reader failed", e)
	}
}

func mountSyntheticSMB(t *testing.T, b *mountBroker) string {
	t.Helper()
	physical := t.TempDir()
	port := 1445
	ctx, cancel := context.WithCancel(context.Background())
	server := exec.CommandContext(ctx, "/usr/bin/python3", "/test-tools/smb-fixture.py", "--root", physical, "--port", strconv.Itoa(port))
	var output bytes.Buffer
	server.Stdout = &output
	server.Stderr = &output
	server.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGKILL}
	if e := server.Start(); e != nil {
		cancel()
		t.Fatal(e)
	}
	t.Cleanup(func() { cancel(); _ = server.Wait() })
	ready := false
	for deadline := time.Now().Add(8 * time.Second); time.Now().Before(deadline); {
		conn, e := net.DialTimeout("tcp", "127.0.0.1:1445", 100*time.Millisecond)
		if e == nil {
			conn.Close()
			ready = true
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !ready {
		cancel()
		_ = server.Wait()
		t.Fatal("synthetic SMB server unavailable", output.String())
	}
	root := b.config.Roots[0].Path
	options := "vers=2.0,username=fixture,password=fixture-only,ip=127.0.0.1,port=1445,uid=1000,gid=1000,dir_mode=0770,file_mode=0660,nounix,noperm,noserverino,nosharesock,soft,echo_interval=1"
	if e := unix.Mount("//127.0.0.1/vaults", root, "cifs", unix.MS_NOSUID|unix.MS_NODEV, options); e != nil {
		cancel()
		_ = server.Wait()
		t.Fatal("synthetic CIFS mount failed", e, output.String())
	}
	b.config.Roots[0].Mount = root
	b.config.Roots[0].MountSource = "//127.0.0.1/vaults"
	t.Cleanup(func() {
		b.close()
		for _, entry := range b.registry.Entries {
			if b.isLockedMount(entry) {
				_ = b.unmountFiles(b.publicPath(entry))
			}
		}
		if e := unix.Unmount(root, 0); e != nil {
			t.Error("synthetic CIFS cleanup failed", e)
		}
	})
	return physical
}
