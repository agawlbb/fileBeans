package service

import "path/filepath"

type archiveActivity struct{ phase string }

type archiveWorkStatus struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Source      string `json:"source"`
	Destination string `json:"destination"`
	Phase       string `json:"phase"`
	Error       string `json:"error,omitempty"`
	Next        int64  `json:"next,omitempty"`
}

type archiveSyncStatus struct {
	PendingOriginals int                 `json:"pendingOriginals"`
	PendingChanges   int                 `json:"pendingChanges"`
	Problems         int                 `json:"problems"`
	PendingWork      int                 `json:"pendingWork"`
	Items            []archiveWorkStatus `json:"items"`
}

// Only IDs and phases live in memory. Names come from authorized/decrypted
// operation records when status is requested, so locked names stay hidden.
func (s *Service) startArchiveActivity(id, phase string) func() {
	activity := &archiveActivity{phase}
	s.archiveStatusMu.Lock()
	if s.archiveActive == nil {
		s.archiveActive = map[string]*archiveActivity{}
	}
	s.archiveActive[id] = activity
	s.archiveStatusMu.Unlock()
	return func() {
		s.archiveStatusMu.Lock()
		defer s.archiveStatusMu.Unlock()
		if s.archiveActive[id] == activity {
			delete(s.archiveActive, id)
		}
	}
}

func (s *Service) archiveWorkStatuses(tasks []archiveTask) []archiveWorkStatus {
	s.archiveStatusMu.Lock()
	defer s.archiveStatusMu.Unlock()
	items := []archiveWorkStatus{}
	for _, task := range tasks {
		if task.State == "paired" {
			continue
		}
		phase := "queuedChange"
		if task.Kind == "ingest" {
			phase = "queuedTranscode"
		}
		switch task.State {
		case "backup", "trash-backup":
			phase = "queuedCopy"
		case "publishing", "cleanup":
			phase = "finishing"
		}
		if task.Kind == "drift" {
			phase = "attention"
		}
		message, next := task.Error, task.Next
		if message != "" && phase != "attention" {
			phase = "retrying"
		}
		if activity := s.archiveActive[task.ID]; activity != nil {
			phase = activity.phase
			message, next = "", 0
		}
		name := filepath.Base(task.Relative)
		if name == "." || name == "/" {
			name = filepath.Base(task.Library.Path)
		}
		items = append(items, archiveWorkStatus{ID: task.ID, Name: name,
			Source: task.Library.Source, Destination: task.Target.Backend, Phase: phase, Error: message, Next: next})
	}
	return items
}
