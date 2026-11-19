package service

import (
	"context"
	"log"
	"path/filepath"
	"sort"
	"strings"
)

func normalizationPicture(j NormalizationJob) bool {
	if strings.HasPrefix(j.SourceMIME, "image/") {
		return true
	}
	if strings.HasPrefix(j.SourceMIME, "video/") {
		return false
	}
	name := j.Request.Path
	if j.Request.Source == "drive" {
		name = j.Request.RelativePath
	}
	switch strings.ToLower(filepath.Ext(name)) {
	case ".jpg", ".jpeg", ".jpe", ".jfif", ".png", ".gif", ".apng", ".webp", ".bmp", ".tif", ".tiff", ".avif", ".jxl", ".heic", ".heif":
		return true
	}
	return false
}

// Finish pictures before admitting any further video. A waiting or failed
// picture retains the barrier until completed or explicitly cancelled.
func orderNormalizationJobs(jobs []NormalizationJob) bool {
	picturesPending := false
	for _, j := range jobs {
		if normalizationPicture(j) && j.State != "completed" && j.State != "cancelled" {
			picturesPending = true
		}
	}
	sort.SliceStable(jobs, func(i, j int) bool {
		pi, pj := normalizationPicture(jobs[i]), normalizationPicture(jobs[j])
		if pi != pj {
			return pi
		}
		if jobs[i].Created == jobs[j].Created {
			return jobs[i].ID < jobs[j].ID
		}
		return jobs[i].Created < jobs[j].Created
	})
	return picturesPending
}

// Older queue records have no MIME field. Resolve ambiguous Drive filenames
// from metadata only, so extensionless pictures cannot fall behind videos.
func (s *Service) classifyNormalizationJobs(ctx context.Context, jobs []NormalizationJob) bool {
	pending := false
	for i := range jobs {
		j := &jobs[i]
		if !normRunnable(*j) || j.SourceMIME != "" || j.Request.Source != "drive" || normalizationPicture(*j) {
			continue
		}
		switch strings.ToLower(filepath.Ext(j.Request.RelativePath)) {
		case ".mp4", ".m4v", ".mov", ".mkv", ".webm", ".wmv", ".avi", ".mpg", ".mpeg", ".ts", ".mts", ".m2ts", ".3gp", ".flv", ".ogv", ".ogg":
			continue
		}
		meta, err := s.driveMeta(ctx, j.Request.Path, j.Connection)
		if err != nil {
			pending = true
			continue
		}
		j.SourceMIME = meta.Mime
		if err = s.normSave(j); err != nil {
			pending = true
		}
	}
	if pending {
		log.Print("Normalization video admission waiting for ambiguous source metadata")
	}
	return pending
}
