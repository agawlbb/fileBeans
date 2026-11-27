package service

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"time"
)

// The coordinator owns the pairing journal; the archive owner owns its Trash.
// Retries use the same remote key and never delete the retained library original
// merely because the archive server cannot be reached.
func (s *Service) runArchivePeerTrash(ctx context.Context, j *archiveTask, remote *archivePeerRoot) error {
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
			if e := remote.callContext(ctx, archivePeerRequest{Operation: "trash-status", Key: j.ArchiveTrashID}, &archived); e != nil {
				return e
			}
			if archived.State != "restored" {
				if e := remote.callContext(ctx, archivePeerRequest{Operation: "restore", Key: j.ArchiveTrashID}, &archived); e != nil {
					return e
				}
			}
		}
		return s.removeArchiveTask(j)
	}
	if j.State == "paired" {
		var archived Trash
		if e := remote.callContext(ctx, archivePeerRequest{Operation: "trash-status", Key: j.ArchiveTrashID}, &archived); e != nil {
			return e
		}
		if t.State == "purged" && archived.Expires <= time.Now().Unix() {
			if archived.State != "purged" {
				if e := remote.callContext(ctx, archivePeerRequest{Operation: "purge", Key: archived.ID}, &archived); e != nil {
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
	key := "archive-trash-" + j.ID
	var archived Trash
	e = remote.callContext(ctx, archivePeerRequest{Operation: "trash-key", Key: key}, &archived)
	from := archived.Original
	if errors.Is(e, os.ErrNotExist) {
		from, e = archiveMatch(remote, j.Target.Base, j.Relative, j.Directory)
		if errors.Is(e, os.ErrNotExist) {
			from = filepath.Join(j.Target.Base, j.Relative)
			if j.Directory {
				e = mkdir(remote, from)
			} else {
				access, err := s.accessTrash(t)
				if err != nil {
					return err
				}
				defer access.close()
				e = s.copyArchiveOriginal(ctx, access.bin, trashPath(t), remote, from, "", filepath.Join(j.Target.Base, archiveStage, j.ID, "copy"))
			}
		}
		if e != nil {
			return e
		}
		if j.Directory {
			if e = s.archiveTrashTree(ctx, j, t, remote); e != nil {
				return e
			}
		}
		if e = remote.RemoveAll(filepath.Join(j.Target.Base, archiveStage, j.ID)); e != nil {
			return e
		}
	} else if e != nil {
		return e
	}
	if e = remote.callContext(ctx, archivePeerRequest{Operation: "trash", Path: from, Key: key}, &archived); e != nil {
		return e
	}
	j.ArchiveTrashID = archived.ID
	j.State = "paired"
	j.Next = time.Now().Add(time.Minute).Unix()
	j.Error = ""
	return s.saveArchiveTask(j)
}
