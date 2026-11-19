package service

import (
	"context"
	"errors"
	"io/fs"
	"testing"
	"testing/fstest"
)

func TestNormalizationInternalPaths(t *testing.T) {
	for _, path := range []string{"pics/.filebeans-thumbnails/abc.jpg", "pics/.FILEBEANS-THUMBNAILS/nested/clip.mp4", ".filebeans-stage/input.jpg", "pics/.filebeans-vaults/id/photo.jpg"} {
		if !normalizationInternalPath(path) {
			t.Fatal(path)
		}
		s := &Service{}
		for _, req := range []NormalizationRequest{{Source: "VPS", Path: path}, {Source: "drive", Path: "opaque-id", RelativePath: path}} {
			if _, err := s.enqueueNormalization(req, "", "", nil); !errors.Is(err, ErrUnsafe) {
				t.Fatal(req, err)
			}
			j := NormalizationJob{Request: req, State: "queued", SourceMIME: "image/jpeg"}
			if normRunnable(j) || orderNormalizationJobs([]NormalizationJob{j}) {
				t.Fatal("cache admitted or blocking video", j)
			}
			if err := s.executeNormalization(context.Background(), &j, nil); !errors.Is(err, ErrUnsafe) {
				t.Fatal(err)
			}
		}
	}
	for _, path := range []string{"pics/photo.jpg", "pics/thumbnails/photo.jpg", "pics/cache/video.mp4", "pics/.filebeans-thumbnails-old/photo.jpg"} {
		if normalizationInternalPath(path) {
			t.Fatal("excluded original", path)
		}
	}
}

func TestNormalizationCompletedCacheRetained(t *testing.T) {
	s := &Service{}
	j := NormalizationJob{State: "completed", Published: true, Request: NormalizationRequest{Path: "pics/.filebeans-thumbnails/old.jpg"}, OutputPath: "pics/retained.jpg"}
	before := j
	if err := s.runNormalizationJob(context.Background(), &j); err != nil {
		t.Fatal(err)
	}
	if err := s.excludeNormalization(&j); err != nil || j != before {
		t.Fatal("completed output changed", j, err)
	}
	for _, field := range []string{"library", "output"} {
		job := NormalizationJob{State: "queued"}
		if field == "library" {
			job.LibraryPath = "pics/.filebeans-thumbnails"
		} else {
			job.OutputPath = "pics/.filebeans-thumbnails/a.jpg"
		}
		if normRunnable(job) {
			t.Fatal("internal destination admitted")
		}
	}
}

func TestNormalizationWalkSkipsInternalSubtrees(t *testing.T) {
	tree := fstest.MapFS{
		"pics/photo.jpg":                       &fstest.MapFile{},
		"pics/thumbnails/original.jpg":         &fstest.MapFile{},
		"pics/.filebeans-thumbnails/cache.jpg": &fstest.MapFile{},
		"pics/.FILEBEANS-STAGE/original":       &fstest.MapFile{},
		"pics/.filebeans-vaults/id/photo.jpg":  &fstest.MapFile{},
	}
	seen := map[string]bool{}
	err := walkNormalizationOriginals(tree, "pics", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if normalizationInternalPath(path) {
			t.Fatal("visited internal path", path)
		}
		if !d.IsDir() {
			seen[path] = true
		}
		return nil
	})
	if err != nil || len(seen) != 2 || !seen["pics/photo.jpg"] || !seen["pics/thumbnails/original.jpg"] {
		t.Fatal(seen, err)
	}
}

func TestNormalizationExcludedJobPreservesMediaState(t *testing.T) {
	for _, state := range []string{"queued", "waiting", "running"} {
		j := NormalizationJob{ID: "cache-job", State: state, Phase: "publishing", Next: 123, Published: true, RawMoved: true, OutputHash: "output-hash", SourceHash: "source-hash", OutputPath: "pics/.filebeans-thumbnails/result.jpg", Request: NormalizationRequest{Path: "pics/.filebeans-thumbnails/source.jpg"}}
		before := j
		markNormalizationExcluded(&j)
		if j.State != "failed" || j.Phase != "excluded" || j.Next != 0 || j.Error == "" {
			t.Fatal(j)
		}
		j.State, j.Phase, j.Next, j.Error = before.State, before.Phase, before.Next, before.Error
		if j != before {
			t.Fatal("media state changed", j)
		}
	}
}
