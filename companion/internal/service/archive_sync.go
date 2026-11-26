package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const archiveQuiet = 5 * time.Minute
const archiveTaskKind = "archiveTask"

// Queue routing belongs to the Library's protection domain. In particular an
// unavailable/locked archive must not prevent saving a retained VPS original.
// This is an operation journal, not a permanent per-file link index.
type archiveTarget struct {
	Identity string `json:"identity,omitempty"`
	Backend  string `json:"backend"`
	Base     string `json:"base"`
	VaultID  string `json:"vaultID,omitempty"`
	Relative string `json:"relative,omitempty"`
}
type archiveTask struct {
	Birth            string          `json:"birth,omitempty"`
	ParentMove       string          `json:"parentMove,omitempty"`
	ArchiveIdentity  string          `json:"archiveIdentity,omitempty"`
	ID               string          `json:"id"`
	Link             string          `json:"link"`
	Library          ArchiveEndpoint `json:"library"`
	Target           archiveTarget   `json:"target"`
	Kind             string          `json:"kind"`
	State            string          `json:"state"`
	Relative         string          `json:"relative"`
	Original         string          `json:"original"`
	To               string          `json:"to,omitempty"`
	Identity         string          `json:"identity,omitempty"`
	Version          string          `json:"version,omitempty"`
	Output           string          `json:"output,omitempty"`
	Hash             string          `json:"hash,omitempty"`
	OutputHash       string          `json:"outputHash,omitempty"`
	Size             int64           `json:"size,omitempty"`
	Directory        bool            `json:"directory,omitempty"`
	TrashID          string          `json:"trashID,omitempty"`
	ArchiveTrashID   string          `json:"archiveTrashID,omitempty"`
	Created          int64           `json:"created"`
	Next             int64           `json:"next"`
	Attempts         int             `json:"attempts,omitempty"`
	Error            string          `json:"error,omitempty"`
	WaitingForMemory bool            `json:"waitingForMemory,omitempty"`
}

func (s *Service) archiveWriter() *Service {
	return &Service{config: s.config, db: s.db, vaults: s.vaults, archiveTrusted: true}
}
func (s *Service) archiveTasks() ([]archiveTask, error) {
	if s.db == nil {
		return nil, nil
	}
	data, e := s.records(archiveTaskKind)
	if e != nil {
		return nil, e
	}
	tasks := []archiveTask{}
	for _, b := range data {
		var j archiveTask
		if e = json.Unmarshal(b, &j); e != nil {
			return nil, e
		}
		tasks = append(tasks, j)
	}
	sort.Slice(tasks, func(i, j int) bool { return tasks[i].Created < tasks[j].Created })
	return tasks, nil
}
func (s *Service) saveArchiveTask(j *archiveTask) error {
	state := j.State
	if j.Error != "" && state == "waiting" {
		state = "attention"
	}
	return s.save(j.ID, archiveTaskKind, state, j)
}
func archiveRetry(attempt int) time.Duration {
	switch attempt {
	case 0:
		return time.Minute
	case 1:
		return 5 * time.Minute
	case 2:
		return 15 * time.Minute
	default:
		return 30 * time.Minute
	}
}
func (s *Service) failArchiveTask(j *archiveTask, e error) {
	var current archiveTask
	if s.read(j.ID, archiveTaskKind, &current) != nil || current.Relative != j.Relative || current.State != j.State || (j.State == "waiting" && current.Version != j.Version) {
		return
	}
	j = &current
	if errors.Is(e, errArchiveMemory) {
		if deferArchiveForMemory(j, time.Now()) {
			_ = s.saveArchiveTask(j)
		}
		return
	}
	j.WaitingForMemory = false
	j.Error = publicError(e)
	j.Next = time.Now().Add(archiveRetry(j.Attempts)).Unix()
	j.Attempts++
	_ = s.saveArchiveTask(j)
}
func (s *Service) resolveTask(j *archiveTask) {
	if links, e := s.cachedArchivePolicies(); e == nil {
		for _, link := range links {
			if link.ID == j.Link {
				j.Library = link.Library
				j.Target.Base = link.Archive.Path
				j.Target.Identity = link.Archive.Identity
				break
			}
		}
	}
	for _, v := range s.vaults.configs() {
		if v.ID == j.Library.Vault && v.Source == j.Library.Source {
			j.Library.Path = filepath.Join(v.Path, j.Library.Relative)
		}
		if v.ID == j.Target.VaultID && v.Source == j.Target.Backend {
			j.Target.Base = filepath.Join(v.Path, j.Target.Relative)
		}
	}
	j.Library.Path, _ = clean(j.Library.Path)
	j.Target.Base, _ = clean(j.Target.Base)
}
func newArchiveTask(link ArchiveLink, kind, relative string) archiveTask {
	link.Library.Path, _ = clean(link.Library.Path)
	link.Archive.Path, _ = clean(link.Archive.Path)
	return archiveTask{ID: ID(), Link: link.ID, Library: link.Library,
		Target: archiveTarget{Backend: link.Archive.Source, Base: link.Archive.Path, VaultID: link.Archive.Vault, Relative: link.Archive.Relative, Identity: link.Archive.Identity},
		Kind:   kind, State: "waiting", Relative: relative, Original: relative, Birth: relative, Created: time.Now().UnixNano()}
}
func archiveRelative(base, path string) (string, bool) {
	base = archivePath(base)
	path = archivePath(path)
	if !within(path, base) || path == base {
		return "", false
	}
	r, e := filepath.Rel(base, path)
	return r, e == nil
}
func archiveMedia(name string) bool {
	typ := filesystemInfo(name, fileNameInfo{name}).Type
	return strings.HasPrefix(typ, "image/") || strings.HasPrefix(typ, "video/")
}

// MIME classification without accessing any source bytes.
type fileNameInfo struct{ name string }

func (f fileNameInfo) Name() string     { return f.name }
func (fileNameInfo) Size() int64        { return 0 }
func (fileNameInfo) Mode() os.FileMode  { return 0600 }
func (fileNameInfo) ModTime() time.Time { return time.Time{} }
func (fileNameInfo) IsDir() bool        { return false }
func (fileNameInfo) Sys() any           { return nil }

// Called after completed publication only, never while receiving an upload.
func (s *Service) archiveAdded(source, path string) error {
	var err error
	path, err = clean(path)
	if err != nil {
		return err
	}
	if s.db == nil || s.archiveTrusted || !archiveMedia(path) {
		return nil
	}
	links, e := s.archivePolicyLinks()
	if e != nil {
		return e
	}
	for _, link := range links {
		if link.Library.Source != source {
			continue
		}
		rel, ok := archiveRelative(link.Library.Path, path)
		if !ok {
			continue
		}
		tasks, e := s.archiveTasks()
		if e != nil {
			return e
		}
		r, e := s.root(source)
		if e != nil {
			return e
		}
		st, e := r.Stat(path)
		r.Close()
		if e != nil {
			return e
		}
		if !st.Mode().IsRegular() {
			return ErrUnsupported
		}
		for _, old := range tasks {
			if old.Link == link.ID && old.Kind == "ingest" && old.Relative == rel && old.Identity == moveIdentity(st) {
				if old.State == "waiting" && old.Version != version(st) {
					old.Version = version(st)
					old.Next = time.Now().Add(archiveQuiet).Unix()
					old.Error = ""
					return s.saveArchiveTask(&old)
				}
				return nil
			}
		}
		j := newArchiveTask(link, "ingest", rel)
		j.Version = version(st)
		j.Identity = moveIdentity(st)
		j.Size = st.Size()
		j.Next = time.Now().Add(archiveQuiet).Unix()
		key := normKey("archive-add", link.ID, rel, j.Version, j.Identity)
		if e = s.insert(j.ID, archiveTaskKind, j.State, key, j); e != nil {
			var old archiveTask
			if s.existing(key, archiveTaskKind, &old) == nil {
				return nil
			}
			return e
		}
		return nil
	}
	return nil
}
func archiveStem(name string) string {
	stem := strings.TrimSuffix(name, filepath.Ext(name))
	if stem == "" {
		return name
	}
	return stem
}
func archiveTouches(relative, path string, directory bool) bool {
	if directory {
		return relative == path || within(relative, path)
	}
	return filepath.Dir(relative) == filepath.Dir(path) && archiveStem(filepath.Base(relative)) == archiveStem(filepath.Base(path))
}
func archiveMatch(root rootFS, base, relative string, directory bool) (string, error) {
	p, err := clean(filepath.Join(base, relative))
	if err != nil {
		return "", err
	}
	if directory {
		st, e := root.Stat(p)
		if e == nil && !st.IsDir() {
			return "", ErrConflict
		}
		return p, e
	}
	d, e := root.Open(filepath.Dir(p))
	if e != nil {
		return "", e
	}
	defer d.Close()
	entries, e := d.ReadDir(-1)
	if e != nil {
		return "", e
	}
	match := ""
	for _, entry := range entries {
		if entry.IsDir() || entry.Type()&os.ModeSymlink != 0 || filesystemHidden(entry.Name()) {
			continue
		}
		if archiveStem(entry.Name()) == archiveStem(filepath.Base(relative)) {
			if match != "" {
				return "", ErrConflict
			}
			match = filepath.Join(filepath.Dir(p), entry.Name())
		}
	}
	if match == "" {
		return "", os.ErrNotExist
	}
	return match, nil
}

// Persist intent before a VPS rename. A worker verifies the resulting inode
// before applying the archive side, so crashes cannot turn a failed move into a
// different archive move. Directory intents cover their complete relative tree.
func (s *Service) archiveMoveIntent(source, from, to string) (string, error) {
	if s.db == nil || s.archiveTrusted {
		return "", nil
	}
	links, e := s.archivePolicyLinks()
	if e != nil {
		return "", e
	}
	for _, link := range links {
		if link.Library.Source != source {
			continue
		}
		a, inside := archiveRelative(link.Library.Path, from)
		b, targetInside := archiveRelative(link.Library.Path, to)
		if inside && !targetInside {
			return "", ErrArchiveLinked
		}
		if !inside || !targetInside {
			continue
		}
		r, e := s.root(source)
		if e != nil {
			return "", e
		}
		st, e := r.Stat(from)
		r.Close()
		if e != nil {
			return "", e
		}
		j := newArchiveTask(link, "move", a)
		j.To = b
		j.Identity = moveIdentity(st)
		j.Directory = st.IsDir()
		j.State = "intent"
		return j.ID, s.insert(j.ID, archiveTaskKind, j.State, "", j)
	}
	return "", nil
}
func (s *Service) finishArchiveMove(id string) error {
	s.archiveEpoch.Add(1)
	if id == "" {
		return nil
	}
	var action archiveTask
	if e := s.read(id, archiveTaskKind, &action); e != nil {
		return e
	}
	tasks, e := s.archiveTasks()
	if e != nil {
		return e
	}
	for _, j := range tasks {
		if j.Link != action.Link || (j.Kind != "ingest" && j.Kind != "mkdir") {
			continue
		}
		if j.Relative == action.Relative || action.Directory && within(j.Relative, action.Relative) {
			rel, _ := filepath.Rel(action.Relative, j.Relative)
			j.Relative = filepath.Join(action.To, rel)
			if j.Kind == "mkdir" {
				j.ParentMove = action.ID
				j.Next = 0
				if e = s.saveArchiveTask(&j); e != nil {
					return e
				}
				continue
			}
			if j.Output != "" {
				r, _ := filepath.Rel(action.Relative, j.Output)
				j.Output = filepath.Join(action.To, r)
			}
			// Preserve the original extension when the visible VPS file is encoded.
			oldext := filepath.Ext(j.Original)
			j.Original = strings.TrimSuffix(j.Relative, filepath.Ext(j.Relative)) + oldext
			j.Next = time.Now().Add(archiveQuiet).Unix()
			if e = s.saveArchiveTask(&j); e != nil {
				return e
			}
		}
	}
	action.State = "move"
	action.Next = 0
	return s.saveArchiveTask(&action)
}
func (s *Service) discardArchiveIntent(id string) {
	if id != "" {
		_, _ = s.db.Exec("DELETE FROM fb_operations WHERE id=? AND kind=? AND state='intent'", id, archiveTaskKind)
	}
}

func (s *Service) archiveTrashIntent(t Trash) error {
	s.archiveEpoch.Add(1)
	if s.db == nil || s.archiveTrusted {
		return nil
	}
	links, e := s.archivePolicyLinks()
	if e != nil {
		return e
	}
	for _, link := range links {
		if link.Library.Source != t.Source {
			continue
		}
		rel, ok := archiveRelative(link.Library.Path, t.Original)
		if !ok {
			continue
		}
		r, e := s.root(t.Source)
		if e != nil {
			return e
		}
		st, e := r.Stat(t.Original)
		r.Close()
		if e != nil {
			return e
		}
		j := newArchiveTask(link, "trash", rel)
		j.State = "trash-intent"
		j.TrashID = t.ID
		j.Directory = st.IsDir()
		j.Identity = moveIdentity(st)
		if e = s.insert(j.ID, archiveTaskKind, j.State, "", j); e != nil {
			return e
		}
		tasks, e := s.archiveTasks()
		if e != nil {
			return e
		}
		for _, pending := range tasks {
			if pending.Link == link.ID && (pending.Kind == "ingest" || pending.Kind == "mkdir") && (pending.Relative == rel || j.Directory && within(pending.Relative, rel)) {
				live, e := rTaskIdentity(s, pending)
				if e == nil && live == pending.Identity {
					pending.TrashID = t.ID
					if e = s.saveArchiveTask(&pending); e != nil {
						return e
					}
				}
			}
		}
		return nil
	}
	return nil
}
func (s *Service) archiveTrashState(id, state string) error {
	s.archiveEpoch.Add(1)
	if s.archiveTrusted {
		return nil
	}
	tasks, e := s.archiveTasks()
	if e != nil {
		return e
	}
	for _, j := range tasks {
		if j.TrashID != id {
			continue
		}
		if j.Kind == "trash" {
			j.State = state
			if state == "restore" {
				j.Created = time.Now().UnixNano()
			}
			j.Next = 0
			if e = s.saveArchiveTask(&j); e != nil {
				return e
			}
			if state == "restore" {
				for _, pending := range tasks {
					if pending.Link == j.Link && pending.Kind == "ingest" && pending.TrashID == id {
						pending.TrashID = ""
						pending.Next = time.Now().Add(archiveQuiet).Unix()
						if e = s.saveArchiveTask(&pending); e != nil {
							return e
						}
					}
				}
			}
		}
	}
	return nil
}
func (s *Service) guardArchivePurge(t Trash) error {
	if s.archiveTrusted {
		return nil
	}
	tasks, e := s.archiveTasks()
	if e != nil {
		return e
	}
	for _, j := range tasks {
		if j.TrashID == t.ID && j.Kind == "trash" && j.ArchiveTrashID == "" {
			return fmt.Errorf("%w: original is awaiting archive backup", ErrConflict)
		}
	}
	return nil
}
func (s *Service) archiveSyncRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/archive-sync/status", func(w http.ResponseWriter, r *http.Request) {
		var status archiveSyncStatus
		e := s.db.QueryRowContext(r.Context(), "SELECT COALESCE(SUM(state IN ('publishing','backup','trash-backup')),0), COALESCE(SUM(state NOT IN ('paired','waiting')),0), COALESCE(SUM(state='attention'),0), COALESCE(SUM(state<>'paired'),0) FROM fb_operations WHERE kind=?", archiveTaskKind).Scan(&status.PendingOriginals, &status.PendingChanges, &status.Problems, &status.PendingWork)
		if e != nil {
			fail(w, e)
			return
		}
		tasks, e := s.archiveTasks()
		if e != nil {
			fail(w, e)
			return
		}
		status.Items = s.archiveWorkStatuses(tasks)
		reply(w, 200, status)
	})
}

func (s *Service) archiveSyncLoop(ctx context.Context) {
	if !s.config.Filesystem.Enabled {
		return
	}
	// Encoding must continue even if Samba discovery or a backup transfer stalls.
	encodeDone := make(chan struct{})
	discoverDone := make(chan struct{})
	go func() { defer close(encodeDone); s.archiveEncodeLoop(ctx) }()
	go func() {
		defer close(discoverDone)
		for ctx.Err() == nil {
			s.discoverArchiveMedia(ctx)
			wait(ctx, time.Minute)
		}
	}()
	defer func() { <-encodeDone; <-discoverDone }()
	for ctx.Err() == nil {
		tasks, e := s.archiveTasks()
		if e == nil {
			for _, j := range tasks {
				if ctx.Err() != nil {
					return
				}
				if j.Kind == "ingest" && (j.State == "waiting" || j.State == "publishing" || j.State == "cleanup") {
					continue
				}
				if j.Kind == "drift" {
					continue
				}
				if j.Kind == "trash" && j.State == "paired" {
					var t Trash
					if s.read(j.TrashID, "trash", &t) == nil && t.State != "retained" {
						j.Next = 0
					}
				}
				if j.Next > time.Now().Unix() {
					continue
				}
				s.resolveTask(&j)
				if e = s.runArchiveTask(ctx, &j); e != nil {
					s.failArchiveTask(&j, e)
				} else {
					s.archiveEpoch.Add(1)
				}
			}
		}
		wait(ctx, 5*time.Second)
	}
}
func (s *Service) archiveEncodeLoop(ctx context.Context) {
	for ctx.Err() == nil {
		tasks, e := s.archiveTasks()
		if e == nil {
			recovering := false
			for _, task := range tasks {
				recovering = recovering || task.WaitingForMemory
			}
			s.archiveMemory.canStart(time.Now(), recovering)
			for _, j := range tasks {
				if ctx.Err() != nil {
					return
				}
				if j.Kind != "ingest" || j.Next > time.Now().Unix() || (j.State != "waiting" && j.State != "publishing" && j.State != "cleanup") {
					continue
				}
				s.resolveTask(&j)
				if e = s.runArchiveIngest(ctx, &j, true); e != nil {
					s.failArchiveTask(&j, e)
				}
			}
		}
		wait(ctx, 5*time.Second)
	}
}
func (s *Service) discoverArchiveMedia(ctx context.Context) {
	links, e := s.archivePolicyLinks()
	if e != nil {
		return
	}
	for _, link := range links {
		// Discovery needs the archive to distinguish an already encoded baseline
		link.Library.Path, _ = clean(link.Library.Path)
		link.Archive.Path, _ = clean(link.Archive.Path)
		tasks, e := s.archiveTasks()
		if e != nil {
			return
		}
		pending := false
		for _, j := range tasks {
			if j.Link == link.ID && (j.Kind == "move" || j.Kind == "trash" || j.Kind == "mkdir") && j.State != "paired" {
				pending = true
				break
			}
		}
		if pending {
			continue
		}
		// from new files. FileBeans upload hooks still queue while it is offline.
		a, e := s.archiveWriter().root(link.Archive.Source)
		if e != nil {
			continue
		}
		r, e := s.root(link.Library.Source)
		if e != nil {
			a.Close()
			continue
		}
		if archiveCheckRoot(a, link.Archive.Path, link.Archive.Vault, link.Archive.Identity) != nil || archiveCheckRoot(r, link.Library.Path, link.Library.Vault, link.Library.Identity) != nil {
			r.Close()
			a.Close()
			continue
		}
		epoch := s.archiveEpoch.Load()
		drift, e := archiveDrift(ctx, r, link.Library.Path, a, link.Archive.Path)
		if e != nil || epoch != s.archiveEpoch.Load() {
			r.Close()
			a.Close()
			continue
		}
		if drift {
			found := false
			for _, j := range tasks {
				if j.Link == link.ID && j.Kind == "drift" {
					found = true
					break
				}
			}
			if !found {
				j := newArchiveTask(link, "drift", "")
				j.State = "attention"
				j.Error = "Linked folders changed outside FileBeans"
				_ = s.insert(j.ID, archiveTaskKind, j.State, "", j)
			}
			r.Close()
			a.Close()
			continue
		}
		for _, j := range tasks {
			if j.Link == link.ID && j.Kind == "drift" {
				_ = s.removeArchiveTask(&j)
			}
		}
		budget := 100000
		var walk func(string, int)
		walk = func(rel string, depth int) {
			if ctx.Err() != nil || budget <= 0 || depth > 64 {
				return
			}
			p := filepath.Join(link.Library.Path, rel)
			epoch := s.archiveEpoch.Load()
			if safe(r, p) != nil {
				return
			}
			f, e := r.Open(p)
			if e != nil {
				return
			}
			entries, e := f.ReadDir(budget + 1)
			f.Close()
			if e != nil && e != io.EOF {
				return
			}
			if _, e = a.Stat(filepath.Join(link.Archive.Path, rel)); errors.Is(e, os.ErrNotExist) {
				archiveMu.Lock()
				if s.archiveEpoch.Load() == epoch {
					_ = s.archiveDirectoryAdded(link.Library.Source, p)
				}
				archiveMu.Unlock()
			}
			for _, entry := range entries {
				budget--
				if budget <= 0 {
					return
				}
				cp := filepath.Join(rel, entry.Name())
				if filesystemHidden(cp) || entry.Type()&os.ModeSymlink != 0 {
					continue
				}
				if entry.IsDir() {
					walk(cp, depth+1)
					continue
				}
				if !archiveMedia(entry.Name()) {
					continue
				}
				fileEpoch := s.archiveEpoch.Load()
				if _, e := archiveMatch(a, link.Archive.Path, cp, false); errors.Is(e, os.ErrNotExist) {
					archiveMu.Lock()
					if s.archiveEpoch.Load() == fileEpoch && fileEpoch == epoch {
						_ = s.archiveAdded(link.Library.Source, filepath.Join(link.Library.Path, cp))
					}
					archiveMu.Unlock()
				}
			}
		}
		walk(".", 0)
		r.Close()
		a.Close()
	}
}
func (s *Service) removeArchiveTask(j *archiveTask) error {
	s.archiveEpoch.Add(1)
	_, e := s.db.Exec("DELETE FROM fb_operations WHERE id=? AND kind=?", j.ID, archiveTaskKind)
	return e
}
func (s *Service) runArchiveTask(ctx context.Context, j *archiveTask) error {
	if e := validateArchiveTask(j); e != nil {
		return e
	}
	if j.Kind != "ingest" {
		stopActivity := s.startArchiveActivity(j.ID, "updating")
		defer stopActivity()
	}
	if j.Kind != "ingest" && j.State != "intent" {
		a, e := s.archiveWriter().root(j.Target.Backend)
		if e != nil {
			return e
		}
		e = archiveCheckRoot(a, j.Target.Base, j.Target.VaultID, j.Target.Identity)
		a.Close()
		if e != nil {
			return e
		}
		tasks, e := s.archiveTasks()
		if e != nil {
			return e
		}
		for _, prior := range tasks {
			if prior.Link == j.Link && (prior.Kind == "move" || prior.Kind == "trash" || prior.Kind == "mkdir") && prior.State != "paired" && prior.Created < j.Created && prior.ParentMove == "" {
				return ErrConflict
			}
		}
	}
	switch j.Kind {
	case "mkdir":
		if j.ParentMove != "" {
			var parent archiveTask
			if s.read(j.ParentMove, archiveTaskKind, &parent) == nil {
				return ErrConflict
			}
		}
		if j.TrashID != "" {
			var t Trash
			if s.read(j.TrashID, "trash", &t) == nil && t.State == "retained" {
				return s.removeArchiveTask(j)
			}
		}
		a, e := s.archiveWriter().root(j.Target.Backend)
		if e != nil {
			return e
		}
		defer a.Close()
		if e = mkdir(a, filepath.Join(j.Target.Base, j.Relative)); e != nil {
			return e
		}
		return s.removeArchiveTask(j)
	case "ingest":
		return s.runArchiveIngest(ctx, j, false)
	case "move":
		return s.runArchiveMove(ctx, j)
	case "trash":
		return s.runArchiveTrash(ctx, j)
	}
	return ErrUnsafe
}
func validateArchiveTask(j *archiveTask) error {
	if len(j.ID) != 32 {
		return ErrUnsafe
	}
	for _, c := range j.ID {
		if !strings.ContainsRune("0123456789abcdef", c) {
			return ErrUnsafe
		}
	}
	for _, rel := range []string{j.Relative, j.Original, j.Output, j.To, j.Birth} {
		if rel == "" {
			continue
		}
		p, e := clean(rel)
		if e != nil || p == "." || filepath.IsAbs(rel) || p != rel {
			return ErrUnsafe
		}
	}
	return nil
}
func archiveCheckRoot(root rootFS, path, vault, identity string) error {
	var err error
	path, err = clean(path)
	if err != nil {
		return err
	}
	if e := safe(root, path); e != nil {
		return e
	}
	info, e := root.Stat(path)
	if e != nil {
		return e
	}
	if !info.IsDir() {
		return ErrUnsafe
	}
	if vault == "" && identity != "" && moveIdentity(info) != identity {
		return ErrConflict
	}
	return nil
}

func (s *Service) archiveDirectoryAdded(source, path string) error {
	var err error
	path, err = clean(path)
	if err != nil {
		return err
	}
	if s.db == nil || s.archiveTrusted {
		return nil
	}
	links, e := s.archivePolicyLinks()
	if e != nil {
		return e
	}
	for _, link := range links {
		if link.Library.Source != source {
			continue
		}
		rel, ok := archiveRelative(link.Library.Path, path)
		if !ok {
			continue
		}
		j := newArchiveTask(link, "mkdir", rel)
		j.State = "mkdir"
		r, e := s.root(source)
		if e != nil {
			return e
		}
		info, e := r.Stat(path)
		r.Close()
		if e != nil {
			return e
		}
		j.Identity = moveIdentity(info)
		key := normKey("archive-directory", link.ID, rel, j.Identity)
		if e = s.insert(j.ID, archiveTaskKind, j.State, key, j); e != nil {
			var old archiveTask
			if s.existing(key, archiveTaskKind, &old) == nil {
				return nil
			}
			return e
		}
		return nil
	}
	return nil
}
func (s *Service) archiveAddedTree(source, path string) error {
	var err error
	path, err = clean(path)
	if err != nil {
		return err
	}
	if source == "drive" || s.db == nil {
		return nil
	}
	links, e := s.archivePolicyLinks()
	if e != nil {
		return e
	}
	inside := false
	for _, link := range links {
		if link.Library.Source == source {
			_, inside = archiveRelative(link.Library.Path, path)
			if inside || archivePath(path) == archivePath(link.Library.Path) {
				inside = true
				break
			}
		}
	}
	if !inside {
		return nil
	}
	r, e := s.root(source)
	if e != nil {
		return e
	}
	defer r.Close()
	budget := 100000
	var walk func(string, int) error
	walk = func(p string, depth int) error {
		if budget <= 0 || depth > 64 {
			return ErrUnsafe
		}
		budget--
		if safe(r, p) != nil {
			return ErrUnsafe
		}
		st, e := r.Stat(p)
		if e != nil {
			return e
		}
		if !st.IsDir() {
			return s.archiveAdded(source, p)
		}
		if e = s.archiveDirectoryAdded(source, p); e != nil {
			return e
		}
		f, e := r.Open(p)
		if e != nil {
			return e
		}
		entries, e := f.ReadDir(budget + 1)
		f.Close()
		if e != nil && e != io.EOF {
			return e
		}
		for _, entry := range entries {
			cp := filepath.Join(p, entry.Name())
			if filesystemHidden(cp) {
				continue
			}
			if e = walk(cp, depth+1); e != nil {
				return e
			}
		}
		return nil
	}
	return walk(path, 0)
}
func (s *Service) archivePublished(j *Job) error {
	if s.db == nil || j.Request.Destination.Source == "drive" {
		return nil
	}
	names := map[string]bool{}
	if j.Move != nil {
		links, e := s.archivePolicyLinks()
		if e != nil {
			return e
		}
		for _, item := range j.Move.Selections {
			inside := item.ArchiveIntent != ""
			for _, link := range links {
				if item.Source == link.Library.Source {
					if _, ok := archiveRelative(link.Library.Path, item.Path); ok {
						inside = true
					}
				}
			}
			if !inside {
				names[item.To] = true
			}
		}
	} else {
		for _, name := range j.Published {
			names[strings.Split(filepath.ToSlash(name), "/")[0]] = true
		}
		if len(names) == 0 {
			for _, item := range j.Manifest {
				names[strings.Split(filepath.ToSlash(item.To), "/")[0]] = true
			}
		}
	}
	for name := range names {
		if !validName(name) {
			return ErrUnsafe
		}
		if e := s.archiveAddedTree(j.Request.Destination.Source, filepath.Join(j.Request.Destination.Path, name)); e != nil {
			return e
		}
	}
	return nil
}
func (s *Service) runArchiveMove(ctx context.Context, j *archiveTask) error {
	r, e := s.root(j.Library.Source)
	if e != nil {
		return e
	}
	defer r.Close()
	if j.State == "intent" {
		archiveMu.Lock()
		defer archiveMu.Unlock()
		st, e := r.Stat(filepath.Join(j.Library.Path, j.To))
		if e == nil && moveIdentity(st) == j.Identity {
			return s.finishArchiveMove(j.ID)
		}
		st, e = r.Stat(filepath.Join(j.Library.Path, j.Relative))
		if e == nil && moveIdentity(st) == j.Identity {
			return s.removeArchiveTask(j)
		}
		return ErrConflict
	}
	// Preserve operation order for multiple moves of the same file/folder.
	tasks, e := s.archiveTasks()
	if e != nil {
		return e
	}
	for _, other := range tasks {
		if other.Link == j.Link && other.Kind == "move" && other.Created < j.Created {
			return ErrConflict
		}
	}
	a, e := s.archiveWriter().root(j.Target.Backend)
	if e != nil {
		return e
	}
	defer a.Close()
	from, e := archiveMatch(a, j.Target.Base, j.Relative, j.Directory)
	if errors.Is(e, os.ErrNotExist) {
		if target, err := archiveMatch(a, j.Target.Base, j.To, j.Directory); err == nil && j.ArchiveIdentity != "" {
			info, err := a.Stat(target)
			if err != nil {
				return err
			}
			if moveIdentity(info) != j.ArchiveIdentity {
				return ErrConflict
			}
			return s.finishArchiveMoved(ctx, j, r, a, target)
		}
		// New files can move several times before they have an archive original.
		for _, pending := range tasks {
			if pending.Link == j.Link && (pending.Kind == "ingest" || pending.Kind == "mkdir") && (pending.Relative == j.To || j.Directory && within(pending.Relative, j.To) || pending.Birth == j.Relative || j.Directory && within(pending.Birth, j.Relative)) {
				return s.removeArchiveTask(j)
			}
		}
		return e
	}
	if e != nil {
		return e
	}
	info, e := a.Stat(from)
	if e != nil {
		return e
	}
	j.ArchiveIdentity = moveIdentity(info)
	if e = s.saveArchiveTask(j); e != nil {
		return e
	}
	to := filepath.Join(j.Target.Base, j.To)
	if !j.Directory {
		to = strings.TrimSuffix(to, filepath.Ext(to)) + filepath.Ext(from)
	}
	if from == to {
		return s.finishArchiveMoved(ctx, j, r, a, to)
	}
	if e = mkdir(a, filepath.Dir(to)); e != nil {
		return e
	}
	if e = renameExclusive(a, from, to); e != nil {
		return e
	}
	if e = syncDir(a, filepath.Dir(to)); e != nil {
		return e
	}
	return s.finishArchiveMoved(ctx, j, r, a, to)
}
func (s *Service) finishArchiveMoved(ctx context.Context, j *archiveTask, r, a rootFS, to string) error {
	if j.Directory {
		return s.removeArchiveTask(j)
	}
	info, e := a.Stat(to)
	if e != nil {
		return e
	}
	revision := filesystemRevision(info)
	oldName := archiveStem(filepath.Base(j.Relative)) + filepath.Ext(to)
	oldCache := filepath.Join(j.Target.Base, filepath.Dir(j.Relative), thumbnailDirectory, previewCacheName(oldName, revision))
	newDir := filepath.Join(filepath.Dir(to), thumbnailDirectory)
	target := filepath.Join(newDir, previewCacheName(to, revision))
	if _, e = a.Stat(target); e == nil {
		return s.removeArchiveTask(j)
	}
	if e = mkdir(a, newDir); e != nil {
		return e
	}
	if _, e = a.Stat(oldCache); e == nil {
		if e = renameExclusive(a, oldCache, target); e != nil {
			return e
		}
		if e = syncDir(a, newDir); e != nil {
			return e
		}
		return s.removeArchiveTask(j)
	}
	visible := j.To
	tasks, e := s.archiveTasks()
	if e != nil {
		return e
	}
	for _, later := range tasks {
		if later.Link != j.Link || later.Created <= j.Created {
			continue
		}
		if later.Kind == "trash" && (visible == later.Relative || later.Directory && within(visible, later.Relative)) {
			return s.removeArchiveTask(j)
		}
		if later.Kind == "move" && (visible == later.Relative || later.Directory && within(visible, later.Relative)) {
			rel, _ := filepath.Rel(later.Relative, visible)
			visible = filepath.Join(later.To, rel)
		}
	}
	if e = s.archiveThumbnail(ctx, j, r, a, filepath.Join(j.Library.Path, visible), to); e != nil {
		return e
	}
	return s.removeArchiveTask(j)
}
func (s *Service) runArchiveTrash(ctx context.Context, j *archiveTask) error {
	var t Trash
	if e := s.read(j.TrashID, "trash", &t); e != nil {
		return e
	}
	if t.State == "failed" {
		return s.removeArchiveTask(j)
	}
	if t.State == "restored" || j.State == "restore" {
		if j.ArchiveTrashID != "" {
			var archived Trash
			if e := s.read(j.ArchiveTrashID, "trash", &archived); e != nil {
				return e
			}
			if archived.State != "restored" {
				if e := s.archiveWriter().restore(j.ArchiveTrashID); e != nil {
					return e
				}
			}
		}
		return s.removeArchiveTask(j)
	}
	if j.State == "paired" {
		var archived Trash
		if e := s.read(j.ArchiveTrashID, "trash", &archived); e != nil {
			return e
		}
		if t.State == "purged" && archived.Expires <= time.Now().Unix() {
			if archived.State != "purged" {
				if e := s.archiveWriter().purge(&archived); e != nil {
					return e
				}
			}
			return s.removeArchiveTask(j)
		}
		j.Next = time.Now().Add(time.Minute).Unix()
		return s.saveArchiveTask(j)
	}
	if t.State != "retained" && t.State != "purging" && t.State != "purged" {
		return ErrConflict
	}
	tasks, e := s.archiveTasks()
	if e != nil {
		return e
	}
	for _, pending := range tasks {
		if pending.Link == j.Link && pending.Kind == "ingest" && pending.TrashID == j.TrashID {
			return ErrConflict
		}
	}
	writer := s.archiveWriter()
	key := "archive-trash-" + j.ID
	var recovered Trash
	if e := s.existing(key, "trash", &recovered); e == nil {
		if recovered.State != "retained" {
			access, e := writer.accessTrash(recovered)
			if e != nil {
				return e
			}
			defer access.close()
			if _, e = access.bin.Stat(trashPath(recovered)); e != nil {
				if !errors.Is(e, os.ErrNotExist) {
					return e
				}
				if e = retainTrashAccess(access, recovered); e != nil {
					return e
				}
			}
			recovered.State = "retained"
			if e = s.save(recovered.ID, "trash", recovered.State, recovered); e != nil {
				return e
			}
		}
		if e = writer.archiveTrashCache(recovered, true); e != nil {
			return e
		}
		j.ArchiveTrashID = recovered.ID
		j.State = "paired"
		j.Error = ""
		j.Next = time.Now().Add(time.Minute).Unix()
		return s.saveArchiveTask(j)
	}
	a, e := writer.root(j.Target.Backend)
	if e != nil {
		return e
	}
	defer a.Close()
	from, e := archiveMatch(a, j.Target.Base, j.Relative, j.Directory)
	if errors.Is(e, os.ErrNotExist) {
		if j.Directory {
			if e = mkdir(a, filepath.Join(j.Target.Base, j.Relative)); e != nil {
				return e
			}
			from = filepath.Join(j.Target.Base, j.Relative)
		} else {
			access, e := s.accessTrash(t)
			if e != nil {
				return e
			}
			defer access.close()
			from = filepath.Join(j.Target.Base, j.Relative)
			if e = s.copyArchiveOriginal(ctx, access.bin, trashPath(t), a, from, "", filepath.Join(j.Target.Base, archiveStage, j.ID, "copy")); e != nil {
				return e
			}
		}
	} else if e != nil {
		return e
	}
	if j.Directory {
		if e = s.archiveTrashTree(ctx, j, t, a); e != nil {
			return e
		}
	}
	if e = a.RemoveAll(filepath.Join(j.Target.Base, archiveStage, j.ID)); e != nil {
		return e
	}
	var archived Trash
	if e = s.existing(key, "trash", &archived); e != nil {
		archived, e = writer.trash(Item{Source: j.Target.Backend, Path: from}, key)
		if e != nil {
			return e
		}
	}
	j.ArchiveTrashID = archived.ID
	j.State = "paired"
	j.Next = time.Now().Add(time.Minute).Unix()
	j.Error = ""
	return s.saveArchiveTask(j)
}
func (s *Service) archiveTrashTree(ctx context.Context, j *archiveTask, t Trash, a rootFS) error {
	access, e := s.accessTrash(t)
	if e != nil {
		return e
	}
	defer access.close()
	budget := 100000
	var walk func(string, int) error
	walk = func(rel string, depth int) error {
		if e := ctx.Err(); e != nil {
			return e
		}
		if budget <= 0 || depth > 64 {
			return ErrUnsafe
		}
		budget--
		path := filepath.Join(trashPath(t), rel)
		if e := safe(access.bin, path); e != nil {
			return e
		}
		info, e := access.bin.Stat(path)
		if e != nil {
			return e
		}
		if !info.IsDir() {
			if !archiveMedia(path) {
				return nil
			}
			target, e := archiveMatch(a, j.Target.Base, filepath.Join(j.Relative, rel), false)
			if e == nil {
				return nil
			}
			if !errors.Is(e, os.ErrNotExist) {
				return e
			}
			target = filepath.Join(j.Target.Base, j.Relative, rel)
			return s.copyArchiveOriginal(ctx, access.bin, path, a, target, "", filepath.Join(j.Target.Base, archiveStage, j.ID, "copy"))
		}
		if e = mkdir(a, filepath.Join(j.Target.Base, j.Relative, rel)); e != nil {
			return e
		}
		f, e := access.bin.Open(path)
		if e != nil {
			return e
		}
		entries, e := f.ReadDir(budget + 1)
		f.Close()
		if e != nil && e != io.EOF {
			return e
		}
		for _, entry := range entries {
			cp := filepath.Join(rel, entry.Name())
			if filesystemHidden(cp) {
				continue
			}
			if e = walk(cp, depth+1); e != nil {
				return e
			}
		}
		return nil
	}
	return walk(".", 0)
}
func rTaskIdentity(s *Service, j archiveTask) (string, error) {
	s.resolveTask(&j)
	r, e := s.root(j.Library.Source)
	if e != nil {
		return "", e
	}
	defer r.Close()
	st, e := r.Stat(filepath.Join(j.Library.Path, j.Relative))
	if e != nil {
		return "", e
	}
	return moveIdentity(st), nil
}

// A cache is generated/read only on the VPS side, then rekeyed for the original.
func (s *Service) archiveThumbnail(ctx context.Context, j *archiveTask, library, archive rootFS, visible, original string) error {
	w := &thumbnailSink{header: make(http.Header)}
	s.filesystemThumbnail(w, thumbnailRequest(ctx, j.Library.Source, visible))
	if w.status != 200 {
		return ErrUnsupported
	}
	return s.copyArchiveThumbnail(j, library, archive, visible, original)
}

// Copy an already rendered library cache without opening either media file.
func (s *Service) copyArchiveThumbnail(j *archiveTask, library, archive rootFS, visible, original string) error {
	originalInfo, e := archive.Stat(original)
	if e != nil {
		return e
	}
	dir := filepath.Join(filepath.Dir(original), thumbnailDirectory)
	target := filepath.Join(dir, previewCacheName(original, filesystemRevision(originalInfo)))
	if _, e = archive.Stat(target); e == nil {
		return nil
	} else if !os.IsNotExist(e) {
		return e
	}
	st, e := library.Stat(visible)
	if e != nil {
		return e
	}
	f, e := filesystemOpenFile(library, filepath.Join(filepath.Dir(visible), thumbnailDirectory, previewCacheName(visible, filesystemRevision(st))))
	if e != nil {
		return e
	}
	defer f.Close()
	data, e := io.ReadAll(io.LimitReader(f, (2<<20)+1))
	if e != nil {
		return e
	}
	if len(data) > 2<<20 {
		return ErrUnsupported
	}
	if e = mkdir(archive, dir); e != nil {
		return e
	}
	temp := filepath.Join(dir, "import-"+j.ID)
	_ = archive.Remove(temp)
	out, e := archive.OpenFile(temp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0660)
	if e != nil {
		return e
	}
	defer archive.Remove(temp)
	_, e = out.Write(data)
	if e == nil {
		e = out.Sync()
	}
	ce := out.Close()
	if e != nil {
		return e
	}
	if ce != nil {
		return ce
	}
	if e = renameExclusive(archive, temp, target); e != nil {
		return e
	}
	return syncDir(archive, dir)
}
