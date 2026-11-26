package service

import (
	"encoding/json"
	"strings"
	"sync"
	"testing"
)

func TestArchiveWorkStatusStages(t *testing.T) {
	s := &Service{}
	for _, tc := range []struct{ kind, state, message, phase string }{
		{"ingest", "waiting", "", "queuedTranscode"},
		{"ingest", "backup", "", "queuedCopy"},
		{"ingest", "trash-backup", "", "queuedCopy"},
		{"ingest", "publishing", "", "finishing"},
		{"ingest", "cleanup", "", "finishing"},
		{"ingest", "backup", "Share unavailable", "retrying"},
		{"move", "intent", "", "queuedChange"},
		{"drift", "attention", "Folders changed", "attention"},
	} {
		t.Run(tc.phase+"/"+tc.state, func(t *testing.T) {
			items := s.archiveWorkStatuses([]archiveTask{{ID: "work", Kind: tc.kind, State: tc.state,
				Relative: "album/video.mov", Error: tc.message, Next: 123,
				Library: ArchiveEndpoint{Source: "VPS"}, Target: archiveTarget{Backend: "Samba"}}})
			if len(items) != 1 || items[0].Phase != tc.phase || items[0].Name != "video.mov" || items[0].Source != "VPS" || items[0].Destination != "Samba" || items[0].Error != tc.message {
				t.Fatal(items)
			}
		})
	}
	if items := s.archiveWorkStatuses([]archiveTask{{ID: "done", Kind: "trash", State: "paired"}}); len(items) != 0 {
		t.Fatal("completed work shown as pending", items)
	}
}

func TestArchiveWorkStatusActivityLifecycleAndVisibility(t *testing.T) {
	s := &Service{}
	task := archiveTask{ID: "work", Kind: "ingest", State: "waiting", Relative: "album/video.mov",
		Error: "Previous retry failure", Next: 123, Hash: "private-checksum", Library: ArchiveEndpoint{Path: "/private-library"}}
	stopEncode := s.startArchiveActivity(task.ID, "transcoding")
	item := s.archiveWorkStatuses([]archiveTask{task})[0]
	if item.Phase != "transcoding" || item.Error != "" || item.Next != 0 {
		t.Fatal("active retry retained stale failure", item)
	}
	stopCopy := s.startArchiveActivity(task.ID, "copying")
	stopEncode()
	if item = s.archiveWorkStatuses([]archiveTask{task})[0]; item.Phase != "copying" {
		t.Fatal("old worker removed the new worker status", item)
	}
	encoded, err := json.Marshal(item)
	if err != nil || strings.Contains(string(encoded), "private-checksum") || strings.Contains(string(encoded), "private-library") || strings.Contains(string(encoded), "album/") {
		t.Fatal("internal metadata exposed", string(encoded), err)
	}
	// Active IDs alone must never disclose work excluded by record decryption.
	if items := s.archiveWorkStatuses(nil); len(items) != 0 {
		t.Fatal("hidden work exposed from live activity", items)
	}
	stopCopy()
	if item = s.archiveWorkStatuses([]archiveTask{task})[0]; item.Phase != "retrying" {
		t.Fatal("finished worker still reported active", item)
	}
}

func TestArchiveWorkStatusConcurrentWorkers(t *testing.T) {
	s := &Service{}
	var workers sync.WaitGroup
	for i := 0; i < 4; i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for j := 0; j < 100; j++ {
				stop := s.startArchiveActivity("shared", "copying")
				s.archiveWorkStatuses([]archiveTask{{ID: "shared", State: "backup"}})
				stop()
			}
		}()
	}
	workers.Wait()
}
