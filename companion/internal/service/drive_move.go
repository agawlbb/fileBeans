package service

import (
	"context"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
)

// Drive moves use the existing copy engines and the move journal. Publication
// completes before any original can be removed; retries resume cleanup only.
func (s *Service) executeDriveMove(ctx context.Context, j *Job) error {
	if e := s.guardArchiveJob(j.Request); e != nil {
		return e
	}
	if err := s.requireDriveWrite(j.Connection); err != nil {
		return err
	}
	if j.Connection == "" {
		grant, err := s.google()
		if err != nil {
			return err
		}
		j.Connection = grant.Connection
	}
	if j.Move == nil {
		state := &MoveState{}
		seenDrive := map[string]bool{}
		for i, item := range j.Request.Items {
			selection := MoveSelection{Source: item.Source, Path: item.Path}
			if item.Source == "drive" {
				meta, err := s.driveMeta(ctx, item.Path, j.Connection)
				if err != nil {
					return err
				}
				if isDriveRoot(meta) || meta.Trashed || meta.Version == "" || !meta.Capabilities.Trash {
					return ErrDrivePermission
				}
				entries, name, err := s.driveTree(ctx, item.Path, j.Connection, false)
				if err != nil {
					return err
				}
				for _, entry := range entries {
					if seenDrive[entry.From] {
						return ErrUnsafe
					}
					seenDrive[entry.From] = true
				}
				selection.To = name
				selection.DriveEntries = entries
			} else {
				root, err := s.root(item.Source)
				if err != nil {
					return err
				}
				entries, err := snapshotMove(ctx, root, item.Path)
				root.Close()
				if err != nil {
					return err
				}
				selection.To = filepath.Base(item.Path)
				selection.Entries = entries
				prefix := ""
				if v := s.vaults.pathVault(item.Source, item.Path); v != nil {
					prefix = strings.TrimPrefix(v.Path, "/")
				}
				selection.Quarantine = filepath.Join(prefix, ".filebeans-stage", j.ID, fmt.Sprintf("move-%d", i))
			}
			state.Selections = append(state.Selections, selection)
		}
		j.Move = state
		if err := s.saveMove(j); err != nil {
			return err
		}
	}
	if !j.Move.Published {
		for i := range j.Move.Selections {
			if err := s.checkDriveMoveSource(ctx, j, &j.Move.Selections[i]); err != nil {
				return err
			}
		}
		var err error
		if j.Request.Destination.Source == "drive" {
			err = s.executeDrive(ctx, j)
		} else {
			err = s.executeRclone(ctx, j)
		}
		if err != nil {
			return err
		}
		for i := range j.Move.Selections {
			m := &j.Move.Selections[i]
			if j.Request.Destination.Source == "drive" {
				for _, f := range j.Manifest {
					if f.To == m.To {
						m.DestinationID = f.TargetID
						break
					}
				}
				if m.DestinationID == "" {
					return ErrConflict
				}
				m.DestinationDrive, _, err = s.driveTree(ctx, m.DestinationID, j.Connection, true)
			} else {
				root, e := s.root(j.Request.Destination.Source)
				if e != nil {
					return e
				}
				path := filepath.Join(j.Request.Destination.Path, m.To)
				m.DestinationEntries, err = snapshotMove(ctx, root, path)
				if err == nil {
					err = syncMoveTree(root, path, m.DestinationEntries)
				}
				root.Close()
			}
			if err != nil {
				return err
			}
		}
		j.Move.Published = true
		if err := s.saveMove(j); err != nil {
			return err
		}
	}
	if !j.Move.Committed {
		for i := range j.Move.Selections {
			if err := s.checkDriveMoveSource(ctx, j, &j.Move.Selections[i]); err != nil {
				return err
			}
			if err := s.checkDriveMoveDestination(ctx, j, &j.Move.Selections[i]); err != nil {
				return err
			}
		}
		j.Move.Committed = true
		if err := s.saveMove(j); err != nil {
			return err
		}
	}
	ctx = context.WithoutCancel(ctx)
	for i := range j.Move.Selections {
		m := &j.Move.Selections[i]
		if m.Removed {
			continue
		}
		if err := s.checkDriveMoveDestination(ctx, j, m); err != nil {
			return err
		}
		if m.Source == "drive" {
			// The mutation journal reconciles a lost trash response on restart.
			key := fmt.Sprintf("drive-move-%s-%d", j.ID, i)
			var previous driveMutationRecord
			if s.existing(key, "driveMutation", &previous) != nil {
				if err := s.checkDriveMoveSource(ctx, j, m); err != nil {
					return err
				}
			}
			_, err := s.driveMutationForConnection(ctx, DriveMutation{Action: "trash", FileID: m.Path, Version: m.DriveEntries[0].Version}, key, j.Connection)
			if err != nil {
				return err
			}
			m.Removed = true
			if err = s.saveMove(j); err != nil {
				return err
			}
		} else {
			root, err := s.root(m.Source)
			if err != nil {
				return err
			}
			err = removeMoveSource(ctx, root, m, func() error { return s.saveMove(j) })
			root.Close()
			if err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *Service) checkDriveMoveSource(ctx context.Context, j *Job, m *MoveSelection) error {
	if m.Source == "drive" {
		current, _, err := s.driveTree(ctx, m.Path, j.Connection, false)
		if err != nil {
			return err
		}
		if !reflect.DeepEqual(current, m.DriveEntries) {
			return ErrConflict
		}
		return nil
	}
	root, err := s.root(m.Source)
	if err != nil {
		return err
	}
	defer root.Close()
	current, err := snapshotMove(ctx, root, m.Path)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(current, m.Entries) {
		return ErrConflict
	}
	return nil
}

func (s *Service) checkDriveMoveDestination(ctx context.Context, j *Job, m *MoveSelection) error {
	if j.Request.Destination.Source == "drive" {
		current, _, err := s.driveTree(ctx, m.DestinationID, j.Connection, true)
		if err != nil {
			return err
		}
		meta, err := s.driveMeta(ctx, m.DestinationID, j.Connection)
		if err != nil {
			return err
		}
		parent, err := s.driveParent(ctx, j.Request.Destination.Path, j.Connection)
		if err != nil {
			return err
		}
		if meta.Trashed || !contains(meta.Parents, parent.ID) || !sameDriveMoveDestination(current, m.DestinationDrive) {
			return ErrConflict
		}
		return nil
	}
	root, err := s.root(j.Request.Destination.Source)
	if err != nil {
		return err
	}
	defer root.Close()
	current, err := snapshotMove(ctx, root, filepath.Join(j.Request.Destination.Path, m.To))
	if err != nil {
		return err
	}
	if !sameMoveData(current, m.DestinationEntries, false) {
		return ErrConflict
	}
	return nil
}

// Drive versions include invisible server metadata changes after publication.
// Keep source checks strict, but allow destination version changes when bytes
// match. Old journals and files without checksums still require the same version.
func sameDriveMoveDestination(current, saved []File) bool {
	if len(current) != len(saved) {
		return false
	}
	for i, before := range saved {
		after := current[i]
		if after.Directory && before.Directory || before.Hash != "" && after.Hash == before.Hash {
			after.Version = before.Version
		}
		// Checksums can become available after publication. A legacy snapshot
		// without one can only be accepted if its version is unchanged.
		if before.Hash == "" {
			after.Hash = ""
		}
		if !reflect.DeepEqual(after, before) {
			return false
		}
	}
	return true
}
