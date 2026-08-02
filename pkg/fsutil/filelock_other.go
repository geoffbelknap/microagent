//go:build !unix

package fsutil

// lockFile is a no-op on platforms without advisory file locking (Windows, the
// non-Unix targets). Callers still get a valid release func; the
// single-writer guarantees that rely on it are best-effort there.
func lockFile(lockPath string) (func() error, error) {
	return func() error { return nil }, nil
}

func tryLockFile(lockPath string) (func() error, bool, error) {
	return func() error { return nil }, true, nil
}
