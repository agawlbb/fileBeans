package service

import (
	"context"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestMediaTimestampAndArchiveCopy(t *testing.T) {
	source := t.TempDir()
	destination := t.TempDir()
	want := time.Unix(1539447530, 123456789)
	atime := time.Unix(1500000000, 987654321)
	if err := os.WriteFile(filepath.Join(source, "original.mov"), []byte("original media"), 0600); err != nil {
		t.Fatal(err)
	}
	left, err := os.OpenRoot(source)
	if err != nil {
		t.Fatal(err)
	}
	defer left.Close()
	right, err := os.OpenRoot(destination)
	if err != nil {
		t.Fatal(err)
	}
	defer right.Close()
	if err = os.Chtimes(filepath.Join(source, "original.mov"), atime, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err = normSetModified(left, "original.mov", want.UTC().Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	info, _ := left.Stat("original.mov")
	if !info.ModTime().Equal(want) || info.Sys().(*syscall.Stat_t).Atim.Nano() != atime.UnixNano() {
		t.Fatal("timestamp precision or atime changed", info)
	}
	s := &Service{}
	for i := 0; i < 2; i++ {
		if err = s.copyArchiveOriginal(context.Background(), left, "original.mov", right, "copy.mov", "", ".stage/copy"); err != nil {
			t.Fatal(err)
		}
		info, _ = right.Stat("copy.mov")
		if !info.ModTime().Equal(want) {
			t.Fatal("archive copy lost original date", info.ModTime())
		}
	}
}
