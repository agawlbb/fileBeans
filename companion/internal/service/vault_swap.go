package service

import (
	"io"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

// A cgroup v2 zero-swap ceiling is sufficient without disabling HA's host swap.
// Read the current process's kernel cgroup and require an actual cgroupfs file;
// an ordinary file, a symlink, an unknown path or "max" never grants an exception.
func processSwapDisabled() bool {
	data, e := os.ReadFile("/proc/self/cgroup")
	if e != nil {
		return false
	}
	path, e := unifiedCgroupPath(string(data))
	if e != nil {
		return false
	}
	for _, mount := range []string{"/sys/fs/cgroup", "/run/filebeans-limits"} {
		if zeroSwapCgroupFile(filepath.Join(mount, path, "memory.swap.max")) {
			return true
		}
	}
	return false
}
func zeroSwapCgroupFile(path string) bool {
	fd, e := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if e != nil {
		return false
	}
	f := os.NewFile(uintptr(fd), "memory.swap.max")
	defer f.Close()
	var filesystem unix.Statfs_t
	if unix.Fstatfs(fd, &filesystem) != nil || filesystem.Type != unix.CGROUP2_SUPER_MAGIC {
		return false
	}
	value, e := io.ReadAll(io.LimitReader(f, 32))
	return e == nil && strings.TrimSpace(string(value)) == "0"
}
func unifiedCgroupPath(data string) (string, error) {
	lines := strings.Split(strings.TrimSpace(data), "\n")
	if len(lines) != 1 || !strings.HasPrefix(lines[0], "0::/") {
		return "", ErrUnsafe
	}
	path := strings.TrimPrefix(lines[0], "0::")
	if filepath.Clean(path) != path || strings.Contains(path, "..") || strings.ContainsAny(path, "\x00\r\n") {
		return "", ErrUnsafe
	}
	return strings.TrimPrefix(path, "/"), nil
}
