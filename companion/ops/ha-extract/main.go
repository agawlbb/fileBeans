// Temporary, Pi-local archive recovery worker. No HTTP ports or saved keys.
package main

import (
	"archive/tar"
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
	"golang.org/x/text/cases"
	"golang.org/x/text/unicode/norm"
)

const socket = "/share/.filebeans-extract-control/control.sock"
const tools = "/local_apps/filebeans_extract/"
const maxBytes int64 = 128 << 30
const maxEntries = 100000

type Request struct {
	Action      string `json:"action"`
	Source      string `json:"source_vault"`
	Destination string `json:"destination_vault"`
	Archive     string `json:"archive"`
	Folder      string `json:"destination_folder"`
	Key         string `json:"key,omitempty"`
}
type Result struct {
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
	Data  any    `json:"data,omitempty"`
}
type Plan struct {
	Files       int   `json:"files"`
	Directories int   `json:"directories"`
	Bytes       int64 `json:"expanded_bytes"`
	Compressed  int64 `json:"archive_bytes"`
}

var idPattern = regexp.MustCompile(`^[0-9a-f]{32}$`)

func main() {
	if len(os.Args) < 2 {
		fatal(errors.New("use serve, client JOB.json, or selftest"))
	}
	switch os.Args[1] {
	case "serve":
		fatal(serve())
	case "client":
		if len(os.Args) != 3 {
			fatal(errors.New("client requires a job JSON file"))
		}
		var q Request
		f, e := os.Open(os.Args[2])
		if e != nil {
			fatal(e)
		}
		e = json.NewDecoder(io.LimitReader(f, 65536)).Decode(&q)
		f.Close()
		if e != nil {
			fatal(e)
		}
		if q.Action != "status" && q.Action != "selftest" {
			key, e := bufio.NewReader(io.LimitReader(os.Stdin, 256)).ReadString('\n')
			if e != nil && e != io.EOF {
				fatal(e)
			}
			q.Key = strings.TrimSpace(key)
		}
		c, e := net.Dial("unix", socket)
		if e != nil {
			fatal(e)
		}
		defer c.Close()
		fatal(json.NewEncoder(c).Encode(q))
		var result Result
		fatal(json.NewDecoder(c).Decode(&result))
		fatal(json.NewEncoder(os.Stdout).Encode(result))
		if !result.OK {
			os.Exit(1)
		}
	case "selftest":
		fatal(testArchive())
	default:
		fatal(errors.New("unknown command"))
	}
}
func fatal(e error) {
	if e != nil {
		fmt.Fprintln(os.Stderr, e)
		os.Exit(1)
	}
}
func serve() error {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	if e := os.MkdirAll(filepath.Dir(socket), 0700); e != nil {
		return e
	}
	if e := os.Remove(socket); e != nil && !os.IsNotExist(e) {
		return e
	}
	l, e := net.Listen("unix", socket)
	if e != nil {
		return e
	}
	defer l.Close()
	defer os.Remove(socket)
	if e = os.Chmod(socket, 0600); e != nil {
		return e
	}
	go func() { <-ctx.Done(); l.Close() }()
	fmt.Println("FileBeans extraction helper ready; no key loaded")
	for {
		c, e := l.Accept()
		if e != nil {
			if ctx.Err() != nil {
				return nil
			}
			return e
		}
		c.SetReadDeadline(time.Now().Add(30 * time.Second))
		var q Request
		e = json.NewDecoder(io.LimitReader(c, 65536)).Decode(&q)
		r := Result{}
		started := time.Now().UTC()
		if e == nil {
			writeStatus(q.Action, "running", started, nil)
		}
		if e == nil {
			r.Data, e = handle(ctx, q)
		}
		q.Key = ""
		r.OK = e == nil
		if e != nil {
			r.Error = e.Error()
		}
		state := "finished"
		if e != nil {
			state = "failed"
		}
		writeStatus(q.Action, state, started, r.Data)
		json.NewEncoder(c).Encode(r)
		c.Close()
	}
}
func master(code string) (string, error) {
	p := strings.Split(strings.TrimSpace(code), ":")
	if len(p) != 3 || p[0] != "FBK1" || len(p[1]) != 64 || len(p[2]) != 8 {
		return "", errors.New("invalid FBK1 recovery code")
	}
	raw, e := hex.DecodeString(p[1])
	if e != nil || len(raw) != 32 {
		return "", errors.New("invalid recovery key")
	}
	hash := sha256.Sum256([]byte("FBK1:" + p[1]))
	if hex.EncodeToString(hash[:4]) != p[2] {
		return "", errors.New("recovery checksum mismatch")
	}
	return p[1], nil
}
func handle(ctx context.Context, q Request) (any, error) {
	expectedHashes = map[string]string{}
	defer func() { expectedHashes = map[string]string{} }()
	switch q.Action {
	case "status":
		return map[string]any{"ready": true, "key_loaded": false, "max_expanded_bytes": maxBytes, "max_entries": maxEntries}, nil
	case "selftest":
		cmd := exec.CommandContext(ctx, "/bin/sh", tools+"selftest.sh")
		out, e := cmd.CombinedOutput()
		if e != nil {
			return nil, fmt.Errorf("synthetic test failed: %s", out)
		}
		return string(out), nil
	case "inspect", "extract":
	default:
		return nil, errors.New("unknown action")
	}
	key, e := master(q.Key)
	q.Key = ""
	if e != nil {
		return nil, e
	}
	if !idPattern.MatchString(q.Source) || !idPattern.MatchString(q.Destination) {
		return nil, errors.New("invalid vault ID")
	}
	archive, e := clean(q.Archive)
	if e != nil || archive == "." {
		return nil, errors.New("invalid archive path")
	}
	folder, e := clean(q.Folder)
	if e != nil {
		return nil, errors.New("invalid destination path")
	}
	if q.Action == "extract" && q.Source == q.Destination {
		return nil, errors.New("source and destination must be different vaults")
	}
	src, closeSrc, e := mountVault(ctx, q.Source, key, true)
	if e != nil {
		return nil, e
	}
	defer closeSrc()
	dst, closeDst, e := mountVault(ctx, q.Destination, key, q.Action != "extract")
	if e != nil {
		return nil, e
	}
	defer closeDst()
	key = ""
	sourceRoot, e := os.OpenRoot(filepath.Join(src, "files"))
	if e != nil {
		return nil, e
	}
	defer sourceRoot.Close()
	destRoot, e := os.OpenRoot(filepath.Join(dst, "files"))
	if e != nil {
		return nil, e
	}
	defer destRoot.Close()
	// Reject symlink components, even though os.Root also confines path resolution.
	if e = noLinks(sourceRoot, archive); e != nil {
		return nil, e
	}
	if e = noLinks(destRoot, folder); e != nil {
		return nil, e
	}
	f, e := sourceRoot.Open(archive)
	if e != nil {
		return nil, e
	}
	defer f.Close()
	info, e := f.Stat()
	if e != nil {
		return nil, e
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("archive is not a regular file")
	}
	p, e := scan(ctx, f, nil)
	if e != nil {
		return nil, e
	}
	p.Compressed = info.Size()
	if q.Action == "inspect" {
		return p, nil
	}
	var space unix.Statfs_t
	if e = unix.Statfs(filepath.Join(dst, "files"), &space); e != nil {
		return nil, e
	}
	need := uint64(p.Bytes) + uint64(p.Bytes/100) + (1 << 30)
	if uint64(space.Bavail)*uint64(space.Bsize) < need {
		return nil, errors.New("insufficient destination space including encryption overhead and reserve")
	}
	if e = emptyDestination(destRoot, folder); e != nil {
		return nil, e
	}
	if _, e = f.Seek(0, 0); e != nil {
		return nil, e
	}
	stage := ".filebeans-extract-incomplete-" + randomID()
	if e = destRoot.Mkdir(stage, 0700); e != nil {
		return nil, e
	}
	defer destRoot.RemoveAll(stage)
	staging, e := destRoot.OpenRoot(stage)
	if e != nil {
		return nil, e
	}
	defer staging.Close()
	if _, e = scan(ctx, f, staging); e != nil {
		return nil, e
	}
	after, e := f.Stat()
	if e != nil {
		return nil, e
	}
	if after.Size() != info.Size() || !after.ModTime().Equal(info.ModTime()) {
		return nil, errors.New("source archive changed")
	}
	// Verify the complete encrypted destination via a plaintext readback before promotion.
	if e = verify(staging); e != nil {
		return nil, e
	}
	target := folder
	created := false
	if target != "." {
		if e = destRoot.Mkdir(target, 0755); e != nil {
			return nil, e
		}
		created = true
	}
	rollback := func(names []string) {
		for _, n := range names {
			destRoot.Rename(path.Join(target, n), path.Join(stage, n))
		}
		if created {
			destRoot.Remove(target)
		}
	}
	entries, e := os.ReadDir(filepath.Join(dst, "files", stage))
	if e != nil {
		return nil, e
	}
	moved := []string{}
	for _, entry := range entries {
		from := filepath.Join(dst, "files", stage, entry.Name())
		to := filepath.Join(dst, "files", target, entry.Name())
		if e = unix.Renameat2(unix.AT_FDCWD, from, unix.AT_FDCWD, to, unix.RENAME_NOREPLACE); e != nil {
			rollback(moved)
			return nil, fmt.Errorf("promotion stopped without overwriting: %w", e)
		}
		moved = append(moved, entry.Name())
	}
	return p, nil
}
func clean(name string) (string, error) {
	if name == "" {
		return ".", nil
	}
	if strings.ContainsAny(name, "\\\x00") || path.IsAbs(name) {
		return "", errors.New("unsafe archive path")
	}
	for _, p := range strings.Split(name, "/") {
		if p == ".." {
			return "", errors.New("parent traversal is forbidden")
		}
	}
	c := path.Clean(name)
	if len(c) > 4096 || len(strings.Split(c, "/")) > 64 {
		return "", errors.New("path too long")
	}
	return c, nil
}
func noLinks(r *os.Root, name string) error {
	parts := strings.Split(name, "/")
	for i := range parts {
		p := path.Join(parts[:i+1]...)
		s, e := r.Lstat(p)
		if os.IsNotExist(e) {
			return nil
		}
		if e != nil {
			return e
		}
		if s.Mode()&os.ModeSymlink != 0 {
			return errors.New("symlink path forbidden")
		}
	}
	return nil
}
func emptyDestination(r *os.Root, name string) error {
	f, e := r.Open(name)
	if os.IsNotExist(e) && name != "." {
		parent := path.Dir(name)
		s, e := r.Stat(parent)
		if e != nil || !s.IsDir() {
			return errors.New("destination parent must already exist")
		}
		return nil
	}
	if e != nil {
		return e
	}
	defer f.Close()
	// Existing named destinations are never replaced, even if empty.
	if name != "." {
		return errors.New("named destination already exists")
	}
	names, e := f.Readdirnames(1)
	if e != nil && e != io.EOF {
		return e
	}
	if len(names) > 0 {
		return errors.New("destination must be empty")
	}
	return nil
}
func stem(name string) string {
	ext := path.Ext(name)
	if ext == name {
		ext = ""
	}
	return cases.Fold().String(norm.NFC.String(strings.TrimSuffix(name, ext)))
}

// scan bounds both expanded content and metadata, validates before writing, and
// only accepts regular files/directories. No ownership, links, or modes from tar.
func scan(ctx context.Context, f io.Reader, out *os.Root) (Plan, error) {
	var p Plan
	gz, e := gzip.NewReader(f)
	if e != nil {
		return p, e
	}
	defer gz.Close()
	bounded := &io.LimitedReader{R: gz, N: maxBytes + (256 << 20) + 1}
	tr := tar.NewReader(bounded)
	seen := map[string]byte{}
	stems := map[string]string{}
	for {
		if e = ctx.Err(); e != nil {
			return p, e
		}
		h, e := tr.Next()
		if e == io.EOF {
			break
		}
		if e != nil {
			return p, e
		}
		if len(seen) >= maxEntries {
			return p, errors.New("archive exceeds 100000 entries")
		}
		name, e := clean(h.Name)
		if e != nil {
			return p, e
		}
		if h.Typeflag != tar.TypeDir && h.Typeflag != tar.TypeReg && h.Typeflag != tar.TypeRegA {
			return p, errors.New("archive contains a link or unsupported entry")
		}
		if name == "." {
			if h.Typeflag == tar.TypeDir {
				continue
			}
			return p, errors.New("invalid root file")
		}
		if old, ok := seen[name]; ok {
			if old == tar.TypeDir && h.Typeflag == tar.TypeDir {
				continue
			}
			return p, errors.New("duplicate archive path")
		}
		for parent := path.Dir(name); parent != "."; parent = path.Dir(parent) {
			if typ, ok := seen[parent]; ok && typ != tar.TypeDir {
				return p, errors.New("file used as a directory")
			}
			seen[parent] = tar.TypeDir
		}
		if len(seen) >= maxEntries {
			return p, errors.New("archive exceeds 100000 paths")
		}
		if h.Typeflag == tar.TypeDir {
			seen[name] = tar.TypeDir
			p.Directories++
			if out != nil {
				e = out.MkdirAll(name, 0755)
				if e != nil {
					return p, e
				}
			}
			continue
		}
		key := path.Dir(name) + "/" + stem(path.Base(name))
		if other, ok := stems[key]; ok && other != name {
			return p, errors.New("files have identical names except for extension")
		}
		stems[key] = name
		seen[name] = tar.TypeReg
		if h.Size < 0 || h.Size > maxBytes-p.Bytes {
			return p, errors.New("archive exceeds 128 GiB expanded limit")
		}
		p.Bytes += h.Size
		p.Files++
		if out != nil {
			if e = out.MkdirAll(path.Dir(name), 0755); e != nil {
				return p, e
			}
			w, e := out.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0644)
			if e != nil {
				return p, e
			}
			hash := sha256.New()
			_, e = copyContext(ctx, io.MultiWriter(w, hash), tr)
			if e == nil {
				e = w.Sync()
			}
			ce := w.Close()
			if e == nil {
				e = ce
			}
			if e != nil {
				return p, e
			}
			// Expected hashes stay in RAM, never in user folders.
			expectedHashes[name] = hex.EncodeToString(hash.Sum(nil))
		}
	}
	if _, e = copyContext(ctx, io.Discard, bounded); e != nil {
		return p, e
	}
	if bounded.N <= 0 {
		return p, errors.New("decompression bound exceeded")
	}
	p.Directories = 0
	for _, typ := range seen {
		if typ == tar.TypeDir {
			p.Directories++
		}
	}
	return p, nil
}

var expectedHashes = map[string]string{}

func copyContext(ctx context.Context, w io.Writer, r io.Reader) (int64, error) {
	b := make([]byte, 128<<10)
	var total int64
	for {
		if e := ctx.Err(); e != nil {
			return total, e
		}
		n, e := r.Read(b)
		if n > 0 {
			m, we := w.Write(b[:n])
			total += int64(m)
			if we != nil {
				return total, we
			}
			if m != n {
				return total, io.ErrShortWrite
			}
		}
		if e == io.EOF {
			return total, nil
		}
		if e != nil {
			return total, e
		}
	}
}
func verify(r *os.Root) error {
	defer func() { expectedHashes = map[string]string{} }()
	for name, want := range expectedHashes {
		f, e := r.Open(name)
		if e != nil {
			return e
		}
		h := sha256.New()
		_, e = io.Copy(h, f)
		f.Close()
		if e != nil {
			return e
		}
		if hex.EncodeToString(h.Sum(nil)) != want {
			return errors.New("destination readback mismatch")
		}
	}
	return nil
}
func randomID() string {
	var b [12]byte
	if _, e := rand.Read(b[:]); e != nil {
		panic(e)
	}
	return hex.EncodeToString(b[:])
}
func mountVault(ctx context.Context, id, key string, ro bool) (string, func(), error) {
	dir := "/share/fileBeans/.filebeans-vaults/" + id
	st, e := os.Lstat(dir)
	if e != nil || !st.IsDir() || st.Mode()&os.ModeSymlink != 0 {
		return "", nil, errors.New("vault directory unavailable")
	}
	m := "/run/filebeans-mount-" + randomID()
	if e = os.Mkdir(m, 0700); e != nil {
		return "", nil, e
	}
	args := []string{"-fg", "-q", "-nosyslog", "-masterkey=stdin"}
	if ro {
		args = append(args, "-ro")
	}
	args = append(args, dir, m)
	cmd := exec.Command(tools+"gocryptfs", args...)
	cmd.Stdin = strings.NewReader(key + "\n")
	// gocryptfs output is suppressed so supplied key material cannot enter logs.
	if e = cmd.Start(); e != nil {
		os.Remove(m)
		return "", nil, e
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	cleanup := func() {
		unix.Unmount(m, 0)
		cmd.Process.Signal(syscall.SIGTERM)
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			cmd.Process.Kill()
			<-done
		}
		os.Remove(m)
	}
	for i := 0; i < 100; i++ {
		var fs unix.Statfs_t
		if unix.Statfs(m, &fs) == nil && fs.Type == 0x65735546 {
			return m, cleanup, nil
		}
		select {
		case <-ctx.Done():
			cleanup()
			return "", nil, ctx.Err()
		case <-done:
			os.Remove(m)
			return "", nil, errors.New("gocryptfs mount failed")
		case <-time.After(100 * time.Millisecond):
		}
	}
	cleanup()
	return "", nil, errors.New("vault mount timeout")
}
func testArchive() error {
	if len(os.Args) != 4 {
		return errors.New("selftest requires source and destination mounts")
	}
	source := os.Args[2]
	destination := os.Args[3]
	name := "subfolder/" + strings.Repeat("x", 220) + ".jpg"
	content := bytes.Repeat([]byte("synthetic media\n"), 100)
	file, e := os.Create(filepath.Join(source, "fixture.tar.gz"))
	if e != nil {
		return e
	}
	gz := gzip.NewWriter(file)
	tw := tar.NewWriter(gz)
	if e = tw.WriteHeader(&tar.Header{Name: name, Mode: 0644, Size: int64(len(content))}); e != nil {
		return e
	}
	if _, e = tw.Write(content); e != nil {
		return e
	}
	if e = tw.Close(); e != nil {
		return e
	}
	if e = gz.Close(); e != nil {
		return e
	}
	if e = file.Close(); e != nil {
		return e
	}
	f, e := os.Open(filepath.Join(source, "fixture.tar.gz"))
	if e != nil {
		return e
	}
	defer f.Close()
	p, e := scan(context.Background(), f, nil)
	if e != nil {
		return e
	}
	if p.Files != 1 || p.Bytes != int64(len(content)) {
		return errors.New("unexpected plan")
	}
	f.Seek(0, 0)
	r, e := os.OpenRoot(destination)
	if e != nil {
		return e
	}
	defer r.Close()
	if _, e = scan(context.Background(), f, r); e != nil {
		return e
	}
	if e = verify(r); e != nil {
		return e
	}
	read, e := r.ReadFile(name)
	if e != nil {
		return e
	}
	if !bytes.Equal(read, content) {
		return errors.New("synthetic content mismatch")
	}
	// Exercise the exact no-overwrite promotion operation on this filesystem.
	old := filepath.Join(destination, "subfolder")
	renamed := filepath.Join(destination, "promoted")
	if e = unix.Renameat2(unix.AT_FDCWD, old, unix.AT_FDCWD, renamed, unix.RENAME_NOREPLACE); e != nil {
		return e
	}
	if e = os.Mkdir(old, 0755); e != nil {
		return e
	}
	if e = unix.Renameat2(unix.AT_FDCWD, renamed, unix.AT_FDCWD, old, unix.RENAME_NOREPLACE); e != unix.EEXIST {
		return errors.New("no-overwrite protection failed")
	}
	fmt.Println("Encrypted archive extraction, subfolders, long names, SHA-256 readback and no-overwrite checks passed")
	return nil
}

// Bounded operational status contains counts only, never a key or child filename.
func writeStatus(action, state string, started time.Time, data any) {
	status := map[string]any{"action": action, "state": state, "started": started, "updated": time.Now().UTC()}
	if p, ok := data.(Plan); ok {
		status["summary"] = p
	}
	b, e := json.Marshal(status)
	if e != nil || len(b) > 4096 {
		return
	}
	name := filepath.Join(filepath.Dir(socket), "status.json")
	if os.WriteFile(name+".new", append(b, '\n'), 0600) == nil {
		os.Rename(name+".new", name)
	}
}
