//go:build mupdf

package preview

import (
	"bytes"
	"context"
	"fmt"
	"image/jpeg"
	"os"
	"path/filepath"
	"testing"

	"github.com/gtsteffaniak/filebrowser/backend/pkg/indexing/iteminfo"
	"github.com/gtsteffaniak/filebrowser/backend/pkg/settings"
)

func TestVaultNativeDocumentPreview(t *testing.T) {
	settings.Config = settings.SetDefaults(false)
	cache := t.TempDir()
	s := NewPreviewGenerator(1, cache)
	// Generate a complete, synthetic PDF rather than depending on external data.
	var pdf bytes.Buffer
	pdf.WriteString("%PDF-1.4\n")
	stream := "BT /F1 18 Tf 20 100 Td (Native document fixture) Tj ET\n"
	objects := []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 256 256] /Resources << /Font << /F1 4 0 R >> >> /Contents 5 0 R >>",
		"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>",
		fmt.Sprintf("<< /Length %d >>\nstream\n%sendstream", len(stream), stream),
	}
	offsets := []int{0}
	for i, obj := range objects {
		offsets = append(offsets, pdf.Len())
		fmt.Fprintf(&pdf, "%d 0 obj\n%s\nendobj\n", i+1, obj)
	}
	xref := pdf.Len()
	fmt.Fprintf(&pdf, "xref\n0 %d\n0000000000 65535 f \n", len(offsets))
	for _, offset := range offsets[1:] {
		fmt.Fprintf(&pdf, "%010d 00000 n \n", offset)
	}
	fmt.Fprintf(&pdf, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(offsets), xref)
	for _, fixture := range []struct {
		name, kind string
		data       []byte
	}{
		{"sample.pdf", "application/pdf", pdf.Bytes()},
		{"sample.md", "text/markdown", []byte("Native text preview fixture\n")},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), fixture.name)
			if err := os.WriteFile(path, fixture.data, 0600); err != nil {
				t.Fatal(err)
			}
			file := iteminfo.ExtendedFileInfo{RealPath: path}
			file.Name = fixture.name
			file.Type = fixture.kind
			temp := filepath.Join(cache, "thumbnails", "docs", "snippet.txt")
			data, err := s.GenerateImageFromDoc(context.Background(), file, temp, 0, "small")
			if err != nil {
				t.Fatal(err)
			}
			img, err := jpeg.Decode(bytes.NewReader(data))
			if err != nil || img.Bounds().Empty() {
				t.Fatal("native document preview did not produce an image", err)
			}
			if _, err := os.Stat(temp); !os.IsNotExist(err) {
				t.Fatal("text scratch file was retained", err)
			}
		})
	}
}
