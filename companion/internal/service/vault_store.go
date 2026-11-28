package service

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

// A share's default ACL must never enter a newly created private store. Existing
// writable or incorrectly owned stores still fail closed instead of being seized.
func (b *mountBroker) openPrivateStore(source string) (*os.File, error) {
	r, e := b.root(source)
	if e != nil {
		return nil, e
	}
	base, e := brokerOpenDir(r.Path, ".")
	if e != nil {
		return nil, e
	}
	defer base.Close()
	created := false
	if e = unix.Mkdirat(int(base.Fd()), brokerStore, 0700); e == nil {
		created = true
	} else if !errors.Is(e, os.ErrExist) {
		return nil, e
	}
	dir, e := brokerOpenAt(int(base.Fd()), brokerStore)
	if e != nil {
		return nil, e
	}
	ok := false
	defer func() {
		if !ok {
			dir.Close()
		}
	}()
	if r.Mount == "" {
		var st unix.Stat_t
		if unix.Fstat(int(dir.Fd()), &st) != nil || st.Uid != uint32(os.Geteuid()) || !created && st.Mode&0022 != 0 {
			return nil, ErrUnsafe
		}
		if created {
			opened, err := os.Open(brokerFD(dir))
			if err != nil {
				return nil, err
			}
			defer opened.Close()
			for _, name := range []string{"system.posix_acl_access", "system.posix_acl_default"} {
				if err = unix.Fremovexattr(int(opened.Fd()), name); err != nil && !errors.Is(err, unix.ENODATA) && !errors.Is(err, unix.EOPNOTSUPP) {
					return nil, err
				}
			}
			if err = opened.Chmod(0700); err != nil {
				return nil, err
			}
			if err = opened.Sync(); err != nil {
				return nil, err
			}
		}
	}
	ok = true
	return dir, nil
}
