package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"os"
	"testing"
)

func fixture(headers ...tar.Header) []byte {
	var b bytes.Buffer
	g := gzip.NewWriter(&b)
	w := tar.NewWriter(g)
	for _, h := range headers {
		if e := w.WriteHeader(&h); e != nil {
			panic(e)
		}
		if h.Size > 0 {
			w.Write(make([]byte, h.Size))
		}
	}
	w.Close()
	g.Close()
	return b.Bytes()
}
func TestUnsafeArchives(t *testing.T) {
	for name, hs := range map[string][]tar.Header{
		"traversal":   {{Name: "../outside", Typeflag: tar.TypeReg}},
		"absolute":    {{Name: "/outside", Typeflag: tar.TypeReg}},
		"symlink":     {{Name: "link", Typeflag: tar.TypeSymlink, Linkname: "outside"}},
		"hardlink":    {{Name: "link", Typeflag: tar.TypeLink, Linkname: "outside"}},
		"extensions":  {{Name: "sub/A.JPG", Typeflag: tar.TypeReg}, {Name: "sub/a.png", Typeflag: tar.TypeReg}},
		"unicode":     {{Name: "caf\u00e9.jpg", Typeflag: tar.TypeReg}, {Name: "cafe\u0301.png", Typeflag: tar.TypeReg}},
		"duplicate":   {{Name: "a.jpg", Typeflag: tar.TypeReg}, {Name: "a.jpg", Typeflag: tar.TypeReg}},
		"file-parent": {{Name: "a", Typeflag: tar.TypeReg}, {Name: "a/b.jpg", Typeflag: tar.TypeReg}},
	} {
		t.Run(name, func(t *testing.T) {
			_, e := scan(context.Background(), bytes.NewReader(fixture(hs...)), nil)
			if e == nil {
				t.Fatal("unsafe archive accepted")
			}
		})
	}
}
func TestValidSubfolders(t *testing.T) {
	p, e := scan(context.Background(), bytes.NewReader(fixture(tar.Header{Name: "./nested/a.jpg", Size: 3}, tar.Header{Name: "other/a.png", Size: 4})), nil)
	if e != nil || p.Files != 2 || p.Bytes != 7 {
		t.Fatalf("%+v %v", p, e)
	}
}
func TestTruncatedGzip(t *testing.T) {
	b := fixture(tar.Header{Name: "a.jpg", Size: 4})
	_, e := scan(context.Background(), bytes.NewReader(b[:len(b)-3]), nil)
	if e == nil {
		t.Fatal("truncated gzip accepted")
	}
}
func TestRecoveryChecksum(t *testing.T) {
	if _, e := master("FBK1:" + string(bytes.Repeat([]byte("a"), 64)) + ":00000000"); e == nil {
		t.Fatal("bad checksum accepted")
	}
}
func TestEncryptedPayloadEquivalentRoundTrip(t *testing.T) {
	source := t.TempDir()
	destination := t.TempDir()
	old := os.Args
	defer func() { os.Args = old }()
	os.Args = []string{"worker", "selftest", source, destination}
	if e := testArchive(); e != nil {
		t.Fatal(e)
	}
}
func TestExistingDestinationRejected(t *testing.T) {
	root, e := os.OpenRoot(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	defer root.Close()
	root.WriteFile("existing.jpg", []byte("preserve"), 0644)
	if e = emptyDestination(root, "."); e == nil {
		t.Fatal("nonempty destination accepted")
	}
	b, e := root.ReadFile("existing.jpg")
	if e != nil || string(b) != "preserve" {
		t.Fatal("existing file changed")
	}
}
