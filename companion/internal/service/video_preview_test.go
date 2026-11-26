package service

import (
	"bytes"
	"context"
	"image"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestVideoPreviewSkipsBlackOpening(t *testing.T) {
	for _, tc := range []struct {
		name, source string
		black        bool
	}{
		{"black-opening", "color=red:s=160x90:r=10:d=4,drawbox=color=black:t=fill:enable='lt(t,2)'", false},
		{"long-black-opening", "color=red:s=160x90:r=10:d=8,drawbox=color=black:t=fill:enable='lt(t,6)'", false},
		{"short", "color=red:s=160x90:r=10:d=0.1", false},
		{"all-black", "color=black:s=160x90:r=10:d=0.5", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, base := filesystemFixture(t)
			if _, err := os.Stat(s.config.Filesystem.FFmpeg); err != nil {
				t.Skip("FFmpeg unavailable")
			}
			path := filepath.Join(base, "clip.mkv")
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, s.config.Filesystem.FFmpeg, "-v", "error", "-nostdin", "-filter_threads", "1", "-f", "lavfi", "-i", tc.source, "-c:v", "ffv1", "-threads", "1", path)
			if output, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("fixture: %v\n%s", err, output)
			}
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			info, _ := os.Stat(path)
			cacheDir := filepath.Join(base, thumbnailDirectory)
			if err = os.Mkdir(cacheDir, 0700); err != nil {
				t.Fatal(err)
			}
			// A first-frame cache from the old renderer must not mask the fix.
			old := filepath.Join(cacheDir, previewCacheNameVersion("clip.mkv", filesystemRevision(info), "jpeg-320-v1"))
			blackThumbnail, err := scaledJPEG(image.NewRGBA(image.Rect(0, 0, 160, 90)))
			if err != nil {
				t.Fatal(err)
			}
			if err = os.WriteFile(old, blackThumbnail, 0600); err != nil {
				t.Fatal(err)
			}
			out := fsRequest(t, s, "GET", "thumbnail", "/clip.mkv", nil, nil)
			if out.Code != 200 {
				t.Fatal(out.Code, out.Body.String())
			}
			img, _, err := image.Decode(bytes.NewReader(out.Body.Bytes()))
			if err != nil {
				t.Fatal(err)
			}
			r, g, b, _ := img.At(img.Bounds().Dx()/2, img.Bounds().Dy()/2).RGBA()
			if tc.black {
				if r > 5000 || g > 5000 || b > 5000 {
					t.Fatal("all-black fallback changed the image")
				}
			} else if r < 40000 || g > 10000 || b > 10000 {
				t.Fatalf("expected visible red scene, got %d %d %d", r, g, b)
			}
			after, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(before, after) {
				t.Fatal("thumbnail generation changed source video", err)
			}
		})
	}
}

func TestVideoPreviewCacheVersionPreservesOtherPreviews(t *testing.T) {
	for _, path := range []string{"photo.jpg", "readme.txt", "document.pdf"} {
		if previewCacheName(path, "revision") != previewCacheNameVersion(path, "revision", "jpeg-320-v1") {
			t.Fatal("unrelated preview invalidated", path)
		}
	}
	for _, path := range []string{"clip.mp4", "clip.MOV", "clip.mkv", "clip.webm"} {
		if previewCacheName(path, "revision") == previewCacheNameVersion(path, "revision", "jpeg-320-v1") {
			t.Fatal("old video cache retained", path)
		}
	}
}

func TestVideoPreviewBackfillRefreshesImportedCacheWithoutOpeningMedia(t *testing.T) {
	s, library := filesystemFixture(t)
	s.config.Roots[0].Name = "VPS"
	archive := t.TempDir()
	s.config.Roots = append(s.config.Roots, Root{Name: "samba", Path: archive})
	for _, path := range []string{filepath.Join(library, "clip.mp4"), filepath.Join(archive, "clip.mov")} {
		if err := os.WriteFile(path, []byte("unreadable media"), 0000); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(path, 0600) })
	}
	info, _ := os.Stat(filepath.Join(library, "clip.mp4"))
	if err := os.Mkdir(filepath.Join(library, thumbnailDirectory), 0700); err != nil {
		t.Fatal(err)
	}
	data := []byte("updated library preview")
	cache := filepath.Join(library, thumbnailDirectory, previewCacheName("clip.mp4", filesystemRevision(info)))
	if err := os.WriteFile(cache, data, 0600); err != nil {
		t.Fatal(err)
	}
	links := []ArchiveLink{{Library: ArchiveEndpoint{Source: "VPS", Path: "/"}, Archive: ArchiveEndpoint{Source: "samba", Path: "/"}}}
	if err := s.refreshArchiveVideoThumbnail(context.Background(), "VPS", "/clip.mp4", links); err != nil {
		t.Fatal(err)
	}
	info, _ = os.Stat(filepath.Join(archive, "clip.mov"))
	imported := filepath.Join(archive, thumbnailDirectory, previewCacheName("clip.mov", filesystemRevision(info)))
	got, err := os.ReadFile(imported)
	if err != nil || !bytes.Equal(got, data) {
		t.Fatal("new video thumbnail was not imported", err)
	}
	// Once imported, a later pass must not rewrite it or need the library cache.
	if err = os.Remove(cache); err != nil {
		t.Fatal(err)
	}
	if err = s.refreshArchiveVideoThumbnail(context.Background(), "VPS", "/clip.mp4", links); err != nil {
		t.Fatal("already imported cache was not reused", err)
	}
}
