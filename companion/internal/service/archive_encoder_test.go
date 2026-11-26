package service

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestArchiveEncoderMemoryBudget(t *testing.T) {
	ffmpeg := os.Getenv("FILEBEANS_TEST_ARCHIVE_FFMPEG")
	ffprobe := os.Getenv("FILEBEANS_TEST_ARCHIVE_FFPROBE")
	if ffmpeg == "" || ffprobe == "" {
		t.Skip("set explicit FFmpeg/ffprobe paths for bounded encoder integration")
	}
	for _, tc := range []struct{ name, size, rate, duration string }{
		{"portrait-720p", "720x1280", "30", "10"},
		{"portrait-maximum", "1080x1920", "10", "2"},
		{"wide-maximum", "2560x1080", "10", "2"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			base := t.TempDir()
			inputPath := filepath.Join(base, "source.mp4")
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
			defer cancel()
			fixture := exec.CommandContext(ctx, ffmpeg, "-hide_banner", "-nostdin", "-v", "error", "-filter_threads", "1",
				"-f", "lavfi", "-i", "testsrc2=size="+tc.size+":rate="+tc.rate+":duration="+tc.duration,
				"-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000:duration="+tc.duration,
				"-c:v", "libx264", "-preset", "ultrafast", "-threads", "1", "-pix_fmt", "yuv420p",
				"-c:a", "aac", "-ac", "2", "-shortest", inputPath)
			if output, err := fixture.CombinedOutput(); err != nil {
				t.Fatalf("synthetic fixture: %v\n%s", err, output)
			}
			root, err := os.OpenRoot(base)
			if err != nil {
				t.Fatal(err)
			}
			defer root.Close()
			originalHash, err := archiveHash(root, "source.mp4")
			if err != nil {
				t.Fatal(err)
			}
			input, err := root.Open("source.mp4")
			if err != nil {
				t.Fatal(err)
			}
			defer input.Close()
			s := &Service{config: Config{Normalization: NormalizationConfig{FFmpeg: ffmpeg, FFprobe: ffprobe}}}
			info, err := s.probeMedia(ctx, input, false)
			if err != nil {
				t.Fatal(err)
			}
			plan, err := makeMediaPlan(info, "source.mp4")
			if err != nil || plan.Copy || plan.Audio == nil {
				t.Fatal("fixture must exercise video and audio encoding", err)
			}
			if err = root.MkdirAll("stage", 0700); err != nil {
				t.Fatal(err)
			}
			directory, err := root.Open("stage")
			if err != nil {
				t.Fatal(err)
			}
			defer directory.Close()
			if err = s.encodeArchiveMedia(ctx, input, directory, plan, 64<<20); err != nil {
				t.Fatalf("efficient encoding under 1.5 GiB data / 2 GiB address caps: %v", err)
			}
			if err = s.validateArchiveMedia(ctx, root, "stage/output.mp4", plan); err != nil {
				t.Fatal("full output validation", err)
			}
			encoded, err := root.Open("stage/output.mp4")
			if err != nil {
				t.Fatal(err)
			}
			encodedInfo, err := s.probeMedia(ctx, encoded, false)
			encoded.Close()
			if err != nil || len(encodedInfo.Streams) == 0 || encodedInfo.Streams[0].Pixel != "yuv420p10le" {
				t.Fatal("10-bit output profile changed", encodedInfo, err)
			}
			if tc.name == "portrait-720p" {
				archive, err := os.OpenRoot(t.TempDir())
				if err != nil {
					t.Fatal(err)
				}
				defer archive.Close()
				if err = s.copyArchiveOriginal(ctx, root, "source.mp4", archive, "original.mp4", originalHash, "stage/copy"); err != nil {
					t.Fatal("original archive copy", err)
				}
				copied, err := archiveHash(archive, "original.mp4")
				if err != nil || copied != originalHash {
					t.Fatal("original copy failed checksum verification", err)
				}
			}
			after, err := archiveHash(root, "source.mp4")
			if err != nil || after != originalHash {
				t.Fatal("source original changed", err)
			}
		})
	}
}

func TestArchiveEncoderFailureReportsCause(t *testing.T) {
	for _, tc := range []struct {
		diagnostic, message string
		cause               error
	}{
		{"x265 [error]: malloc of size 2764800 failed", "Video encoder ran out of memory", errors.New("encoder exit")},
		{"memory allocation failure, aborting encode", "Video encoder ran out of memory", errors.New("encoder exit")},
		{"Cannot allocate memory", "Video encoder ran out of memory", errors.New("encoder exit")},
		{"", "Video encoding interrupted; waiting to retry", context.Canceled},
		{"", "Video encoding timed out", context.DeadlineExceeded},
		{"/private/source/video.mp4: diagnostic detail", "Video encoding failed", errors.New("encoder exit")},
	} {
		failure := &archiveEncodingError{cause: tc.cause, diagnostic: tc.diagnostic}
		if got := publicError(fmt.Errorf("worker: %w", failure)); got != tc.message || strings.Contains(got, "/private") {
			t.Fatal("incorrect public error", got)
		}
		if !errors.Is(failure, tc.cause) {
			t.Fatal("original error lost")
		}
	}
}

func TestArchiveEncoderSignalReportsCrash(t *testing.T) {
	command := archiveCommand(context.Background(), "/bin/sh", "-c", "kill -SEGV $$")
	err := command.Run()
	if err == nil {
		t.Fatal("fixture did not terminate by signal")
	}
	if got := publicError(&archiveEncodingError{cause: err}); got != "Video encoder crashed" {
		t.Fatal(got)
	}
}
