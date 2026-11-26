package service

import (
	"context"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Backfill only local VPS sources. Samba archives are never scanned or rendered.
// The API worker lease elects one instance; rendering shares interactive limits.
func (s *Service) thumbnailBackfill(ctx context.Context) {
	if !s.config.Filesystem.Enabled {
		return
	}
	wait(ctx, 10*time.Second)
	for ctx.Err() == nil {
		counts := s.thumbnailBackfillPass(ctx)
		log.Printf("VPS thumbnail backfill: cached=%d generated=%d unavailable=%d", counts.cached, counts.generated, counts.unavailable)
		wait(ctx, 15*time.Minute)
	}
}

type thumbnailBackfillCounts struct{ cached, generated, unavailable int }

// Discard JPEG responses rather than buffering another copy of each thumbnail.
type thumbnailSink struct {
	header http.Header
	status int
}

func (w *thumbnailSink) Header() http.Header  { return w.header }
func (w *thumbnailSink) WriteHeader(code int) { w.status = code }
func (w *thumbnailSink) Write(p []byte) (int, error) {
	if w.status == 0 {
		w.status = 200
	}
	return len(p), nil
}

func thumbnailRequest(ctx context.Context, source, path string) *http.Request {
	r, _ := http.NewRequestWithContext(ctx, "GET", "/v1/fs/thumbnail?"+url.Values{"source": {source}, "path": {path}}.Encode(), nil)
	return r
}

func (s *Service) thumbnailBackfillPass(ctx context.Context) (counts thumbnailBackfillCounts) {
	remaining := 100000 // Bound traversal memory/work; subsequent passes retry safely.
	for _, source := range s.config.Roots {
		if !strings.EqualFold(source.Name, "VPS") {
			continue
		}
		visited := map[string]bool{}
		links, _ := s.archivePolicyLinks()
		var walk func(string, int)
		walk = func(path string, depth int) {
			path = archivePath(path)
			if ctx.Err() != nil || remaining <= 0 || depth > 64 {
				return
			}
			if visited[path] {
				return
			}
			visited[path] = true
			if suppressed, e := s.archiveThumbnailSuppressed(source.Name, path); e != nil || suppressed {
				return
			}
			r := thumbnailRequest(ctx, source.Name, path)
			root, p, e := s.filesystemRoot(r)
			if e != nil {
				return
			}
			f, e := root.Open(p)
			if e != nil {
				root.Close()
				return
			}
			// Release directory leases before rendering or descending into children.
			type child struct {
				path      string
				directory bool
			}
			children := []child{}
			for remaining > 0 && ctx.Err() == nil {
				entries, err := f.ReadDir(128)
				for _, entry := range entries {
					remaining--
					cp := filepath.Join(p, entry.Name())
					if remaining < 0 || filesystemHidden(cp) || strings.EqualFold(entry.Name(), "Trash") || entry.Type()&os.ModeSymlink != 0 {
						continue
					}
					if safe(root, cp) != nil {
						continue
					}
					info, err := root.Stat(cp)
					if err != nil {
						continue
					} // Includes locked vaults; never unlock.
					if info.IsDir() || info.Mode().IsRegular() && previewSupported(filesystemInfo(cp, info).Type) {
						children = append(children, child{cp, info.IsDir()})
					}
				}
				if err == io.EOF || err != nil {
					break
				}
			}
			f.Close()
			root.Close()
			for _, child := range children {
				if ctx.Err() != nil {
					return
				}
				if child.directory {
					walk(child.path, depth+1)
					continue
				}
				if suppressed, e := s.archiveThumbnailSuppressed(source.Name, child.path); e != nil || suppressed {
					continue
				}
				request := thumbnailRequest(ctx, source.Name, child.path)
				cached := &thumbnailSink{header: make(http.Header)}
				s.filesystemCachedThumbnail(cached, request)
				if cached.status == 200 {
					counts.cached++
					_ = s.refreshArchiveVideoThumbnail(ctx, source.Name, child.path, links)
					continue
				}
				// Missing caches only. Permission, lock and transient errors wait for
				// the next pass rather than opening media despite a failed check.
				if cached.status != 404 {
					counts.unavailable++
					continue
				}
				out := &thumbnailSink{header: make(http.Header)}
				s.filesystemThumbnail(out, request)
				if out.status == 200 {
					counts.generated++
					_ = s.refreshArchiveVideoThumbnail(ctx, source.Name, child.path, links)
				} else {
					counts.unavailable++
				}
				wait(ctx, 100*time.Millisecond)
			}
		}
		// Finish linked libraries first so their caches can be imported promptly.
		if links, e := s.archivePolicyLinks(); e == nil {
			for _, link := range links {
				if link.Library.Source == source.Name {
					walk(link.Library.Path, 0)
				}
			}
		}
		walk("/", 0)
	}
	return counts
}

// Refresh imported video previews from the local library's new cache version.
// This copies only thumbnail bytes; Samba originals are never decoded.
func (s *Service) refreshArchiveVideoThumbnail(ctx context.Context, source, path string, links []ArchiveLink) error {
	path, err := clean(path)
	if err != nil {
		return err
	}
	if !strings.HasPrefix(filesystemType(path), "video/") {
		return nil
	}
	for _, link := range links {
		if err := ctx.Err(); err != nil {
			return err
		}
		relative, ok := archiveRelative(link.Library.Path, path)
		if link.Library.Source != source || !ok {
			continue
		}
		return func() error {
			library, err := s.root(source)
			if err != nil {
				return err
			}
			defer library.Close()
			archive, err := s.archiveWriter().root(link.Archive.Source)
			if err != nil {
				return err
			}
			defer archive.Close()
			if err = archiveCheckRoot(archive, link.Archive.Path, link.Archive.Vault, link.Archive.Identity); err != nil {
				return err
			}
			original, err := archiveMatch(archive, link.Archive.Path, relative, false)
			if err != nil {
				return err
			}
			return s.copyArchiveThumbnail(&archiveTask{ID: ID()}, library, archive, path, original)
		}()
	}
	return nil
}
