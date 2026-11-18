package preview

import (
	"context"
	"filebeans/companion/protection"
	"os"
	"path/filepath"

	"github.com/gtsteffaniak/filebrowser/backend/internal/adapters/fs/diskcache"
	"github.com/gtsteffaniak/filebrowser/backend/pkg/indexing/iteminfo"
	"github.com/gtsteffaniak/filebrowser/backend/pkg/vaultbridge"
)

func previewServiceForFile(file iteminfo.ExtendedFileInfo) (*Service, *protection.Lease, error) {
	lease, e := vaultbridge.Read(file.RealPath, false)
	if e != nil {
		return nil, nil, e
	}
	if lease == nil {
		return service, nil, nil
	}
	private, e := lease.CacheDirectory(file.RealPath)
	if e != nil {
		lease.Close()
		return nil, nil, e
	}
	if private == "" {
		return service, lease, nil
	}
	if file.OnlyOfficeId != "" {
		lease.Close()
		return nil, nil, ErrUnsupportedMedia
	}
	cacheDir := filepath.Join(private, "cache", "previews")
	tmpDir := filepath.Join(private, "tmp", "previews")
	for _, dir := range []string{cacheDir, tmpDir, filepath.Join(cacheDir, "thumbnails", "docs"), filepath.Join(cacheDir, "thumbnails", "videos")} {
		if e = os.MkdirAll(dir, 0700); e != nil {
			lease.Close()
			return nil, nil, e
		}
	}
	cache, e := diskcache.NewFileCache(cacheDir)
	if e != nil {
		lease.Close()
		return nil, nil, e
	}
	return &Service{fileCache: cache, cacheDir: cacheDir, debug: false, docGenMutex: service.docGenMutex,
		ffmpegService: service.ffmpegService.WithCacheDir(tmpDir), imageSem: service.imageSem, imageLargeSem: service.imageLargeSem}, lease, nil
}

func GeneratePreviewWithMD5(ctx context.Context, file iteminfo.ExtendedFileInfo, size, officeURL string, seek int, hash string) ([]byte, error) {
	selected, lease, e := previewServiceForFile(file)
	if e != nil {
		return nil, e
	}
	defer lease.Close()
	return selected.generateWithMD5(ctx, file, size, officeURL, seek, hash)
}
