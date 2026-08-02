//go:build unix

package fsutil

import (
	"os"

	"golang.org/x/sys/unix"
)

// lockFile takes an exclusive flock on lockPath. The lock is tied to the open
// file description, so closing the file (the returned release func) drops it —
// as does process exit. flock is advisory: it only excludes other flock holders,
// which is exactly the cooperating microagent processes that use it.
func lockFile(lockPath string) (func() error, error) {
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX); err != nil {
		_ = f.Close()
		return nil, err
	}
	return f.Close, nil
}

func tryLockFile(lockPath string) (func() error, bool, error) {
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, false, err
	}
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		_ = f.Close()
		if err == unix.EWOULDBLOCK || err == unix.EAGAIN {
			return nil, false, nil
		}
		return nil, false, err
	}
	return f.Close, true, nil
}
