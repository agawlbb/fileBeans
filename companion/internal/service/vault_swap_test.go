package service

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSwapProofRejectsAmbiguousAndEscapingCgroups(t *testing.T) {
	for _, input := range []string{"1:memory:/work", "0::/../other", "0::/work\n1:cpu:/other", "0::/work/", "0::relative", ""} {
		if _, e := unifiedCgroupPath(input); e == nil {
			t.Fatal("unsafe cgroup accepted", input)
		}
	}
	for input, want := range map[string]string{"0::/": "", "0::/work": "work", "0::/nested/work\n": "nested/work"} {
		got, e := unifiedCgroupPath(input)
		if e != nil || got != want {
			t.Fatal(input, got, e)
		}
	}
}

func TestSwapProofRejectsOrdinaryFilesAndSymlinks(t *testing.T) {
	path := filepath.Join(t.TempDir(), "memory.swap.max")
	if e := os.WriteFile(path, []byte("0\n"), 0600); e != nil {
		t.Fatal(e)
	}
	if zeroSwapCgroupFile(path) {
		t.Fatal("ordinary file forged a kernel swap ceiling")
	}
	link := path + "-link"
	if e := os.Symlink(path, link); e != nil {
		t.Fatal(e)
	}
	if zeroSwapCgroupFile(link) {
		t.Fatal("symlink forged a kernel swap ceiling")
	}
}
