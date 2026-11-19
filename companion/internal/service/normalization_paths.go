package service

import (
	"io/fs"
	"strings"
)

// Reserved application subtrees contain derived data, never original media.
func normalizationInternalPath(path string) bool {
	for _, part := range strings.Split(strings.ReplaceAll(path, "\\", "/"), "/") {
		switch strings.ToLower(part) {
		case ".filebeans-thumbnails", ".filebeans-stage", ".filebeans-vaults":
			return true
		}
	}
	return false
}

func normalizationInternalJob(j NormalizationJob) bool {
	return normalizationInternalPath(j.Request.Path) || normalizationInternalPath(j.Request.RelativePath) || normalizationInternalPath(j.LibraryPath) || normalizationInternalPath(j.OutputPath)
}

// Do not clean up an excluded workspace: it may hold a retained original or output.
func (s *Service) excludeNormalization(j *NormalizationJob) error {
	if j.State != "queued" && j.State != "running" && j.State != "waiting" {
		return nil
	}
	markNormalizationExcluded(j)
	return s.normSave(j)
}

// Skip reserved directories before WalkDir attempts to enumerate their contents.
func walkNormalizationOriginals(root fs.FS, path string, visit fs.WalkDirFunc) error {
	return fs.WalkDir(root, path, func(path string, d fs.DirEntry, err error) error {
		if err == nil && normalizationInternalPath(path) {
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		return visit(path, d, err)
	})
}

func markNormalizationExcluded(j *NormalizationJob) {
	j.State, j.Phase, j.Error, j.Next = "failed", "excluded", "Application cache files are excluded from normalization", 0
}
