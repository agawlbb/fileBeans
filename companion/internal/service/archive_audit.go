package service

import (
	"encoding/json"
	"io"
)

// ArchiveAudit emits only authorized operation metadata. It neither runs jobs
// nor changes their routing, retention or archive policy.
func (s *Service) ArchiveAudit(w io.Writer) error {
	tasks, err := s.archiveTasks()
	if err != nil {
		return err
	}
	records, err := s.records("normalize")
	if err != nil {
		return err
	}
	type proof struct {
		ID, State, SourceHash, OutputHash, OutputPath string
		Published                                     bool
	}
	proofs := []proof{}
	for _, raw := range records {
		var j NormalizationJob
		if err := json.Unmarshal(raw, &j); err != nil {
			return err
		}
		if j.SourceHash != "" && j.OutputHash != "" {
			proofs = append(proofs, proof{j.ID, j.State, j.SourceHash, j.OutputHash, j.OutputPath, j.Published})
		}
	}
	return json.NewEncoder(w).Encode(struct {
		Tasks         []archiveTask
		Normalization []proof
	}{tasks, proofs})
}
