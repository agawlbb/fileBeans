// Package protection is shared by the companion and the pinned Quantum adapter.
// It contains no key handling or mount privileges.
package protection

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

const Protocol = 1

// The Quantum adapter may register only after installing every corresponding
// hook. Protocol compatibility alone must never enable protected-folder creation.
var ConsumerFeatures = []string{"requestLeases", "pathGuard", "encryptedPreviews", "indexIsolation", "metadataIsolation", "mutationBoundaries"}

var ErrUnavailable = errors.New("protected-folder service unavailable")
var ErrLocked = errors.New("protected folder is locked")
var ErrBoundary = errors.New("operation crosses a protected-folder boundary")
var ErrHidden = errors.New("private storage is inaccessible")
var ErrBusy = errors.New("protected folders are changing state")

type Vault struct {
	ID          string `json:"id"`
	Source      string `json:"source"`
	Path        string `json:"path"`
	Name        string `json:"name"`
	State       string `json:"state"`
	Initialized bool   `json:"initialized"`
}

type Snapshot struct {
	Version int     `json:"version"`
	Vaults  []Vault `json:"vaults"`
}

type Guard struct {
	runtime string
	roots   map[string]string
	owner   uint32
	// Injected only in package tests; live guards inspect the kernel mount table.
	mounted func(string, string) bool
}

type Lease struct {
	guard    *Guard
	file     *os.File
	Snapshot Snapshot
}

func within(path, root string) bool {
	return path == root || strings.HasPrefix(path, strings.TrimSuffix(root, "/")+"/")
}

func New(runtime string, roots map[string]string) (*Guard, error) {
	if !filepath.IsAbs(runtime) || filepath.Clean(runtime) != runtime || runtime == "/" || len(roots) == 0 {
		return nil, ErrUnavailable
	}
	g := &Guard{runtime: runtime, roots: make(map[string]string), mounted: mounted}
	for source, root := range roots {
		if source == "" || !filepath.IsAbs(root) || filepath.Clean(root) != root || root == "/" {
			return nil, ErrUnavailable
		}
		if within(root, runtime) || within(runtime, root) {
			return nil, ErrUnavailable
		}
		for _, other := range g.roots {
			if within(root, other) || within(other, root) {
				return nil, ErrUnavailable
			}
		}
		g.roots[source] = root
	}
	return g, nil
}

func (g *Guard) trustedFile(name string) (*os.File, error) {
	fd, e := unix.Open(filepath.Join(g.runtime, name), unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if e != nil {
		return nil, ErrUnavailable
	}
	f := os.NewFile(uintptr(fd), name)
	var st unix.Stat_t
	if unix.Fstat(fd, &st) != nil || st.Uid != g.owner || st.Mode&0022 != 0 || st.Mode&unix.S_IFMT != unix.S_IFREG {
		f.Close()
		return nil, ErrUnavailable
	}
	return f, nil
}

func (g *Guard) snapshot() (Snapshot, error) {
	f, e := g.trustedFile("status.json")
	if e != nil {
		return Snapshot{}, e
	}
	defer f.Close()
	var s Snapshot
	if e = json.NewDecoder(f).Decode(&s); e != nil || s.Version != Protocol {
		return Snapshot{}, ErrUnavailable
	}
	ids := map[string]bool{}
	for _, v := range s.Vaults {
		root, ok := g.roots[v.Source]
		id, e := hex.DecodeString(v.ID)
		if e != nil || len(id) != 16 || strings.ToLower(v.ID) != v.ID || !ok || ids[v.ID] || !strings.HasPrefix(v.Path, "/") ||
			filepath.Clean(v.Path) != v.Path || v.Path == "/" || !within(filepath.Join(root, v.Path), root) {
			return Snapshot{}, ErrUnavailable
		}
		if v.State != "locked" && v.State != "unlocked" && v.State != "error" {
			return Snapshot{}, ErrUnavailable
		}
		for _, other := range s.Vaults {
			if other.ID != v.ID && other.Source == v.Source && (within(v.Path, other.Path) || within(other.Path, v.Path)) {
				return Snapshot{}, ErrUnavailable
			}
		}
		ids[v.ID] = true
	}
	return s, nil
}

// Acquire must span the entire operation, including response streaming and child
// processes. Its read lock prevents the broker from unmounting or adding a vault.
func (g *Guard) Acquire() (*Lease, error) {
	f, e := g.trustedFile("gate")
	if e != nil {
		return nil, e
	}
	if e = unix.Flock(int(f.Fd()), unix.LOCK_SH|unix.LOCK_NB); e != nil {
		f.Close()
		return nil, ErrBusy
	}
	s, e := g.snapshot()
	if e != nil {
		f.Close()
		return nil, e
	}
	return &Lease{guard: g, file: f, Snapshot: s}, nil
}

func (l *Lease) Close() error {
	if l == nil || l.file == nil {
		return nil
	}
	e := l.file.Close()
	l.file = nil
	return e
}

func hidden(path string) bool {
	for _, part := range strings.Split(filepath.Clean(path), string(filepath.Separator)) {
		if part == ".filebeans-vaults" {
			return true
		}
	}
	return false
}

func (l *Lease) classify(path string) (*Vault, bool, error) {
	if l == nil || l.file == nil || !filepath.IsAbs(path) {
		return nil, false, ErrUnavailable
	}
	path = filepath.Clean(path)
	if hidden(path) || within(path, l.guard.runtime) {
		return nil, false, ErrHidden
	}
	sourceRoot := ""
	for _, root := range l.guard.roots {
		if within(path, root) {
			sourceRoot = root
			break
		}
	}
	if sourceRoot == "" {
		return nil, false, ErrBoundary
	}
	// Check the resolved location too. Uploads may target a nonexistent child;
	// resolve its nearest existing parent before accepting the lexical path.
	ancestor := path
	for {
		resolved, e := filepath.EvalSymlinks(ancestor)
		if e == nil {
			if hidden(resolved) || within(resolved, l.guard.runtime) {
				return nil, false, ErrHidden
			}
			if !within(resolved, sourceRoot) {
				return nil, false, ErrBoundary
			}
			// CIFS can resolve a differently cased pathname without reporting a
			// symlink. Recognize the FUSE device too, so an alias cannot select an
			// ordinary preview cache or bypass a protected mutation boundary.
			var sourceStat, ancestorStat unix.Stat_t
			if unix.Stat(sourceRoot, &sourceStat) != nil || unix.Stat(ancestor, &ancestorStat) != nil {
				return nil, false, ErrUnavailable
			}
			for _, v := range l.Snapshot.Vaults {
				public := filepath.Join(l.guard.roots[v.Source], v.Path)
				var vaultStat unix.Stat_t
				if unix.Stat(public, &vaultStat) == nil && vaultStat.Dev != sourceStat.Dev && ancestorStat.Dev == vaultStat.Dev && !within(path, public) {
					return nil, false, ErrBoundary
				}
			}
			if resolved != ancestor {
				for _, v := range l.Snapshot.Vaults {
					root := filepath.Join(l.guard.roots[v.Source], v.Path)
					if within(resolved, root) || within(ancestor, root) || within(root, resolved) || within(root, ancestor) {
						return nil, false, ErrBoundary
					}
				}
			}
			break
		}
		if !errors.Is(e, os.ErrNotExist) {
			return nil, false, ErrUnavailable
		}
		parent := filepath.Dir(ancestor)
		if parent == ancestor {
			return nil, false, ErrUnavailable
		}
		ancestor = parent
	}
	contains := false
	for i := range l.Snapshot.Vaults {
		v := &l.Snapshot.Vaults[i]
		root := filepath.Join(l.guard.roots[v.Source], v.Path)
		if within(path, root) {
			return v, false, nil
		}
		if within(root, path) {
			contains = true
		}
	}
	return nil, contains, nil
}

func (l *Lease) Check(path string, recursive bool) error {
	v, contains, e := l.classify(path)
	if e != nil {
		return e
	}
	if recursive && contains {
		return ErrBoundary
	}
	if v != nil {
		public := filepath.Join(l.guard.roots[v.Source], v.Path)
		if v.State != "unlocked" || !l.guard.mounted(public, "filebeans-vault-"+v.ID) {
			return ErrLocked
		}
	}
	return nil
}

// Mutation rejects moving a vault's mountpoint, operating recursively on an
// ancestor, or transferring through ordinary Quantum APIs across a boundary.
// Explicit exports use the companion's separately tracked transfer operation.
func (l *Lease) Mutation(from, to string) error {
	var previous string
	for i, path := range []string{from, to} {
		if path == "" {
			continue
		}
		if e := l.Check(path, true); e != nil {
			return e
		}
		v, _, e := l.classify(path)
		if e != nil {
			return e
		}
		id := ""
		if v != nil {
			if filepath.Clean(path) == filepath.Join(l.guard.roots[v.Source], v.Path) {
				return ErrBoundary
			}
			id = v.ID
		}
		if i == 1 && from != "" && previous != id {
			return ErrBoundary
		}
		previous = id
	}
	return nil
}

func (l *Lease) CacheDirectory(path string) (string, error) {
	if e := l.Check(path, false); e != nil {
		return "", e
	}
	v, _, e := l.classify(path)
	if e != nil {
		return "", e
	}
	if v == nil {
		return "", nil
	}
	return filepath.Join(l.guard.runtime, "mounts", v.ID, "private"), nil
}

// Indexable is consulted per filesystem entry, including background scans. A
// missing registry disables indexing; it never falls back to the ordinary SQL DB.
func (g *Guard) Indexable(path string) bool {
	l, e := g.Acquire()
	if e != nil {
		return false
	}
	defer l.Close()
	v, _, e := l.classify(path)
	return e == nil && v == nil
}

func mounted(path, source string) bool {
	data, e := os.ReadFile("/proc/self/mountinfo")
	if e != nil {
		return false
	}
	decode := strings.NewReplacer("\\040", " ", "\\011", "\t", "\\012", "\n", "\\134", "\\")
	for _, line := range strings.Split(string(data), "\n") {
		parts := strings.SplitN(line, " - ", 2)
		if len(parts) != 2 {
			continue
		}
		left, right := strings.Fields(parts[0]), strings.Fields(parts[1])
		if len(left) > 4 && len(right) > 1 && decode.Replace(left[4]) == path && decode.Replace(right[1]) == source {
			return true
		}
	}
	return false
}

func (g *Guard) String() string {
	return fmt.Sprintf("protected filesystem guard (%d sources)", len(g.roots))
}
