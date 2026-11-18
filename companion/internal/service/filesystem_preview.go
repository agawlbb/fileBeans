package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	_ "image/gif"
	"image/jpeg"
	_ "image/png"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
	"unicode"

	"golang.org/x/image/font"
	"golang.org/x/image/font/basicfont"
	"golang.org/x/image/math/fixed"
)

// Two renderers across all requests, including duplicate cache requests. Slots
// are acquired before opening roots, so queued requests do not pin mount leases.
var previewSlots = make(chan struct{}, 2)
var previewLocks = func() [64]chan struct{} {
	var locks [64]chan struct{}
	for i := range locks {
		locks[i] = make(chan struct{}, 1)
	}
	return locks
}()

func previewSupported(typ string) bool {
	return strings.HasPrefix(typ, "image/") || strings.HasPrefix(typ, "video/") || strings.HasPrefix(typ, "text/") || typ == "application/pdf"
}
func (s *Service) filesystemThumbnail(w http.ResponseWriter, r *http.Request) {
	digestKey := sha256.Sum256([]byte(r.URL.Query().Get("source") + "\x00" + r.URL.Query().Get("path")))
	cacheLock := previewLocks[int(digestKey[0])%len(previewLocks)]
	select {
	case cacheLock <- struct{}{}:
		defer func() { <-cacheLock }()
	case <-r.Context().Done():
		return
	}
	select {
	case previewSlots <- struct{}{}:
		defer func() { <-previewSlots }()
	case <-r.Context().Done():
		return
	}
	root, p, e := s.filesystemRoot(r)
	if e != nil {
		filesystemFail(w, e)
		return
	}
	defer root.Close()
	file, e := filesystemOpenFile(root, p)
	if e != nil {
		filesystemFail(w, e)
		return
	}
	defer file.Close()
	st, e := file.Stat()
	if e != nil {
		filesystemFail(w, e)
		return
	}
	if !st.Mode().IsRegular() {
		filesystemFail(w, ErrUnsafe)
		return
	}
	entry := filesystemInfo(p, st)
	if !previewSupported(entry.Type) {
		filesystemFail(w, ErrUnsupported)
		return
	}
	cacheDir := filepath.Join(filepath.Dir(p), thumbnailDirectory)
	if e = safe(root, cacheDir); e != nil {
		filesystemFail(w, e)
		return
	}
	if e = root.MkdirAll(cacheDir, 0770); e != nil {
		filesystemFail(w, e)
		return
	}
	digest := sha256.Sum256([]byte(filepath.Base(p) + "\x00" + entry.Revision + "\x00jpeg-320-v1"))
	cache := filepath.Join(cacheDir, hex.EncodeToString(digest[:])+".jpg")
	if e = safe(root, cache); e != nil {
		filesystemFail(w, e)
		return
	}
	if cached, err := filesystemOpenFile(root, cache); err == nil {
		defer cached.Close()
		info, err := cached.Stat()
		if err == nil && info.Mode().IsRegular() && info.Size() <= 2<<20 {
			w.Header().Set("Content-Type", "image/jpeg")
			_, _ = io.Copy(w, io.LimitReader(cached, 2<<20))
			return
		}
	}
	cleanupPreviewCache(root, cacheDir)
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	data, e := s.renderPreview(ctx, file, entry.Type, root, cacheDir)
	if e != nil {
		filesystemFail(w, ErrUnsupported)
		return
	}
	latest, e := file.Stat()
	if e != nil || filesystemRevision(latest) != entry.Revision {
		filesystemFail(w, ErrConflict)
		return
	}
	temp := filepath.Join(cacheDir, "render-"+ID())
	out, e := root.OpenFile(temp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0660)
	if e != nil {
		filesystemFail(w, e)
		return
	}
	defer root.Remove(temp)
	_, e = out.Write(data)
	ce := out.Close()
	if e == nil {
		e = ce
	}
	if e == nil {
		e = renameExclusive(root, temp, cache)
	}
	if e != nil && !os.IsExist(e) {
		filesystemFail(w, e)
		return
	}
	w.Header().Set("Content-Type", "image/jpeg")
	_, _ = w.Write(data)
}

func (s *Service) renderPreview(ctx context.Context, in *os.File, typ string, root rootFS, cacheDir string) ([]byte, error) {
	if strings.HasPrefix(typ, "text/") {
		return textPreview(in)
	}
	// GIF/JPEG/PNG are decoded in-process after checking pixel limits. Other
	// formats and videos use FFmpeg with an inherited, seekable descriptor.
	if typ == "image/jpeg" || typ == "image/png" || typ == "image/gif" {
		cfg, _, e := image.DecodeConfig(in)
		if e != nil {
			return nil, e
		}
		if cfg.Width <= 0 || cfg.Height <= 0 || int64(cfg.Width)*int64(cfg.Height) > 40_000_000 {
			return nil, ErrUnsupported
		}
		if _, e = in.Seek(0, 0); e != nil {
			return nil, e
		}
		img, _, e := image.Decode(in)
		if e != nil {
			return nil, e
		}
		return scaledJPEG(img)
	}
	dir, e := root.Open(cacheDir)
	if e != nil {
		return nil, e
	}
	defer dir.Close()
	tool := s.config.Filesystem.FFmpeg
	args := []string{"-v", "error", "-nostdin", "-threads", "1", "-filter_threads", "1", "-filter_complex_threads", "1", "-protocol_whitelist", "file,pipe", "-i", "/proc/self/fd/3", "-map", "0:v:0", "-frames:v", "1", "-vf", "scale=320:320:force_original_aspect_ratio=decrease", "-threads", "1", "-f", "image2pipe", "-vcodec", "mjpeg", "pipe:1"}
	if typ == "application/pdf" {
		tool = s.config.Filesystem.PDFToPPM
		args = []string{"-f", "1", "-singlefile", "-scale-to", "320", "-jpeg", "/proc/self/fd/3"}
	}
	if tool == "" {
		return nil, ErrUnsupported
	}
	cmd := exec.CommandContext(ctx, tool, args...)
	cmd.ExtraFiles = []*os.File{in, dir}
	// All subprocess scratch/cache files remain beside the original, inside the
	// encrypted filesystem for protected media. Input names never enter argv.
	// chdir occurs before os/exec remaps ExtraFiles in the child. Resolve the
	// parent's pinned descriptor here; /proc/self/fd/4 is valid only after exec.
	cmd.Dir = fmt.Sprintf("/proc/%d/fd/%d", os.Getpid(), dir.Fd())
	cmd.Env = []string{"PATH=/usr/bin:/bin", "TMPDIR=/proc/self/fd/4", "HOME=/proc/self/fd/4", "XDG_CACHE_HOME=/proc/self/fd/4"}
	var output limitedPreview
	cmd.Stdout = &output
	if e = cmd.Run(); e != nil {
		return nil, e
	}
	// Decode and re-encode to enforce dimensions and a consistent JPEG response.
	cfg, _, e := image.DecodeConfig(bytes.NewReader(output.Bytes()))
	if e != nil || cfg.Width > 1024 || cfg.Height > 1024 {
		return nil, ErrUnsupported
	}
	img, _, e := image.Decode(bytes.NewReader(output.Bytes()))
	if e != nil {
		return nil, e
	}
	return scaledJPEG(img)
}

type limitedPreview struct{ bytes.Buffer }

func (b *limitedPreview) Write(p []byte) (int, error) {
	if b.Len()+len(p) > 2<<20 {
		return 0, fmt.Errorf("preview exceeds limit")
	}
	return b.Buffer.Write(p)
}
func scaledJPEG(src image.Image) ([]byte, error) {
	bounds := src.Bounds()
	width, height := bounds.Dx(), bounds.Dy()
	if width <= 0 || height <= 0 {
		return nil, ErrUnsupported
	}
	if width > 320 || height > 320 {
		if width >= height {
			height = max(1, height*320/width)
			width = 320
		} else {
			width = max(1, width*320/height)
			height = 320
		}
	}
	dst := image.NewRGBA(image.Rect(0, 0, width, height))
	draw.Draw(dst, dst.Bounds(), image.NewUniform(color.White), image.Point{}, draw.Src)
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			c := src.At(bounds.Min.X+x*bounds.Dx()/width, bounds.Min.Y+y*bounds.Dy()/height) // Composite transparent pixels on white.
			rr, gg, bb, aa := c.RGBA()
			dst.SetRGBA(x, y, color.RGBA{uint8((rr + 65535 - aa) >> 8), uint8((gg + 65535 - aa) >> 8), uint8((bb + 65535 - aa) >> 8), 255})
		}
	}
	var out bytes.Buffer
	e := jpeg.Encode(&out, dst, &jpeg.Options{Quality: 80})
	return out.Bytes(), e
}
func textPreview(in io.Reader) ([]byte, error) {
	data, e := io.ReadAll(io.LimitReader(in, 8192))
	if e != nil {
		return nil, e
	}
	img := image.NewRGBA(image.Rect(0, 0, 256, 320))
	draw.Draw(img, img.Bounds(), image.NewUniform(color.White), image.Point{}, draw.Src)
	d := font.Drawer{Dst: img, Src: image.NewUniform(color.RGBA{30, 30, 30, 255}), Face: basicfont.Face7x13}
	line := 0
	col := 0
	var text strings.Builder
	flush := func() { d.Dot = fixed.P(10, 22+line*15); d.DrawString(text.String()); text.Reset(); line++; col = 0 }
	for _, r := range string(data) {
		if line >= 19 {
			break
		}
		if r == '\n' {
			flush()
			continue
		}
		if r == '\t' {
			r = ' '
		}
		if !unicode.IsPrint(r) {
			continue
		}
		if r > 126 {
			r = '?'
		}
		text.WriteRune(r)
		col++
		if col >= 33 {
			flush()
		}
	}
	if line < 19 && text.Len() > 0 {
		flush()
	}
	return scaledJPEG(img)
}

// Cleanup is opportunistic on cache misses, never a recursive scan of the
// archive. Upload spools are excluded because an active slow transfer can be long.
func cleanupPreviewCache(root rootFS, dir string) {
	f, e := root.Open(dir)
	if e != nil {
		return
	}
	defer f.Close()
	entries, e := f.ReadDir(-1)
	if e != nil {
		return
	}
	for _, entry := range entries {
		name := entry.Name()
		if entry.Type()&os.ModeSymlink != 0 || entry.IsDir() {
			continue
		}
		age := 30 * 24 * time.Hour
		if strings.HasPrefix(name, "render-") {
			age = 48 * time.Hour
		} else if len(name) != 68 || !strings.HasSuffix(name, ".jpg") {
			continue
		}
		if strings.HasSuffix(name, ".jpg") {
			if _, e := hex.DecodeString(strings.TrimSuffix(name, ".jpg")); e != nil {
				continue
			}
		}
		info, e := entry.Info()
		if e == nil && time.Since(info.ModTime()) > age {
			_ = root.Remove(filepath.Join(dir, name))
		}
	}
}
