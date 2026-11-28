package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

type ArchiveRepairPlan struct {
	Link  string   `json:"link"`
	Tasks []string `json:"tasks"`
}
type ArchiveRepairReport struct {
	Completed, PreservedVersions, ClearedDiscovery, Remaining int
	Errors                                                    map[string]int
}

// ReconcileArchiveRetentions is an explicitly invoked maintenance operation.
// Only the supplied journal IDs belonging to this link may change. Differing
// bytes are retained on the archive owner, never substituted for its original.
func (s *Service) ReconcileArchiveRetentions(ctx context.Context, plan ArchiveRepairPlan) (ArchiveRepairReport, error) {
	report := ArchiveRepairReport{Errors: map[string]int{}}
	if !validBrokerID(plan.Link) || len(plan.Tasks) > 4096 {
		return report, ErrUnsafe
	}
	wanted := map[string]bool{}
	for _, id := range plan.Tasks {
		if !validBrokerID(id) {
			return report, ErrUnsafe
		}
		wanted[id] = true
	}
	if s.config.SQLite == "" {
		conn, err := s.db.Conn(ctx)
		if err != nil {
			return report, err
		}
		defer conn.Close()
		var got int
		if err = conn.QueryRowContext(ctx, "SELECT GET_LOCK('filebeans-companion-worker',0)").Scan(&got); err != nil {
			return report, err
		}
		if got != 1 {
			return report, ErrVaultBusy
		}
		defer conn.ExecContext(context.Background(), "SELECT RELEASE_LOCK('filebeans-companion-worker')")
	}
	tasks, err := s.archiveTasks()
	if err != nil {
		return report, err
	}
	for _, j := range tasks {
		if !wanted[j.ID] {
			continue
		}
		if j.Link != plan.Link {
			return report, ErrUnsafe
		}
		if err = ctx.Err(); err != nil {
			return report, err
		}
		s.resolveTask(&j)
		recovered, discovered := false, false
		if j.Kind == "ingest" && j.State == "waiting" && j.ParentMove == "" && j.TrashID == "" {
			err = s.clearStaleArchiveDiscovery(&j)
			discovered = err == nil
		} else {
			err = s.runArchiveTask(ctx, &j)
			if errors.Is(err, ErrConflict) && j.Kind == "ingest" && j.State == "backup" {
				err = s.preserveArchiveRecovery(ctx, &j)
				recovered = err == nil
			}
		}
		if err != nil {
			report.Remaining++
			report.Errors[publicError(err)]++
			continue
		}
		report.Completed++
		if recovered {
			report.PreservedVersions++
		}
		if discovered {
			report.ClearedDiscovery++
		}
	}
	return report, nil
}
func (s *Service) clearStaleArchiveDiscovery(j *archiveTask) error {
	r, err := s.root(j.Library.Source)
	if err != nil {
		return err
	}
	defer r.Close()
	if err = archiveCheckRoot(r, j.Library.Path, j.Library.Vault, j.Library.Identity); err != nil {
		return err
	}
	st, err := r.Stat(filepath.Join(j.Library.Path, j.Relative))
	if err != nil {
		return err
	}
	if !st.Mode().IsRegular() || version(st) != j.Version {
		return ErrConflict
	}
	base := filepath.Join(j.Library.Path, archiveStage, j.ID)
	if _, err = r.Stat(base); !errors.Is(err, os.ErrNotExist) {
		return ErrConflict
	}
	a, err := s.archiveWriter().root(j.Target.Backend)
	if err != nil {
		return err
	}
	defer a.Close()
	if err = archiveCheckRoot(a, j.Target.Base, j.Target.VaultID, j.Target.Identity); err != nil {
		return err
	}
	if _, err = archiveMatch(a, j.Target.Base, j.Relative, false); err != nil {
		return err
	}
	// Both user-visible versions remain untouched. This drops only the supplied
	// unstarted discovery intent whose unchanged file already has an archive peer.
	return s.removeArchiveTask(j)
}
func (s *Service) preserveArchiveRecovery(ctx context.Context, j *archiveTask) error {
	if err := validateArchiveTask(j); err != nil {
		return err
	}
	r, err := s.root(j.Library.Source)
	if err != nil {
		return err
	}
	defer r.Close()
	if err = archiveCheckRoot(r, j.Library.Path, j.Library.Vault, j.Library.Identity); err != nil {
		return err
	}
	a, err := s.archiveWriter().root(j.Target.Backend)
	if err != nil {
		return err
	}
	defer a.Close()
	if err = archiveCheckRoot(a, j.Target.Base, j.Target.VaultID, j.Target.Identity); err != nil {
		return err
	}
	canonical := filepath.Join(j.Target.Base, j.Original)
	st, err := a.Stat(canonical)
	if errors.Is(err, os.ErrNotExist) {
		// An earlier encode can change the extension. The unique archive
		// counterpart stays canonical; the retained generation is kept apart.
		canonical, err = archiveMatch(a, j.Target.Base, j.Original, false)
		if err != nil {
			return err
		}
		st, err = a.Stat(canonical)
	}
	if err != nil {
		return err
	}
	if !st.Mode().IsRegular() {
		return ErrConflict
	}
	raw := filepath.Join(j.Library.Path, archiveStage, j.ID, "original")
	hash, err := archiveHashContext(ctx, r, raw)
	if err != nil {
		return err
	}
	if hash != j.Hash {
		return ErrConflict
	}
	dir := filepath.Join(j.Target.Base, ".filebeans-stage", "archive-recovery", j.ID)
	target := filepath.Join(dir, filepath.Base(j.Original))
	if err = s.copyArchiveOriginal(ctx, r, raw, a, target, hash, filepath.Join(j.Target.Base, archiveStage, j.ID, "recovery-copy")); err != nil {
		return err
	}
	details := struct {
		Version                                                                               int
		Job, Canonical, CanonicalIdentity, CanonicalRevision, RetainedSHA256, Library, Reason string
	}{1, j.ID, canonical, moveIdentity(st), version(st), hash, filepath.Join(j.Library.Path, j.Relative), "Retained VPS bytes differ from existing archive; both versions preserved"}
	data, err := json.Marshal(details)
	if err != nil {
		return err
	}
	manifest := filepath.Join(dir, "details.json")
	localManifest := filepath.Join(j.Library.Path, archiveStage, j.ID, "recovery-details.json")
	if err = r.WriteFile(localManifest, data, 0660); err != nil {
		return err
	}
	if err = syncDir(r, localManifest); err != nil {
		return err
	}
	if err = s.copyArchiveOriginal(ctx, r, localManifest, a, manifest, "", filepath.Join(j.Target.Base, archiveStage, j.ID, "recovery-details-copy")); err != nil {
		return err
	}
	if err = syncDir(a, manifest); err != nil {
		return err
	}
	if err = syncDir(a, dir); err != nil {
		return err
	}
	for _, parent := range []string{filepath.Dir(dir), filepath.Dir(filepath.Dir(dir))} {
		if err = syncDir(a, parent); err != nil {
			return err
		}
	}
	readback, err := a.ReadFile(manifest)
	if err != nil || string(readback) != string(data) {
		return fmt.Errorf("recovery metadata readback: %w", ErrConflict)
	}
	// The canonical original must still be the one examined before retention is released.
	latest, err := a.Stat(canonical)
	if err != nil || version(latest) != version(st) || moveIdentity(latest) != moveIdentity(st) {
		return ErrConflict
	}
	if err = a.RemoveAll(filepath.Join(j.Target.Base, archiveStage, j.ID)); err != nil {
		return err
	}
	j.State = "cleanup"
	j.Error = ""
	j.Next = 0
	if err = s.saveArchiveTask(j); err != nil {
		return err
	}
	if err = r.Remove(raw); err != nil {
		return err
	}
	if err = r.RemoveAll(filepath.Join(j.Library.Path, archiveStage, j.ID)); err != nil {
		return err
	}
	return s.removeArchiveTask(j)
}
