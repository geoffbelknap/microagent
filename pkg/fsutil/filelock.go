package fsutil

// Lock acquires an exclusive advisory lock at lockPath (creating the file if
// needed) and returns a release func. Hold it across a read-modify-write of some
// shared on-disk state to serialize concurrent processes/goroutines against each
// other. The lock is released by the returned func, and by the OS if the process
// exits while holding it.
//
// On platforms without advisory file locking (Windows) this is a no-op that
// still returns a valid release func, so callers need no build-time branching.
func Lock(lockPath string) (release func() error, err error) {
	return lockFile(lockPath)
}

// TryLock attempts to acquire an exclusive advisory lock without waiting. The
// acquired result is false when another process still owns the lock. Callers
// use this for lifetime leases where blocking would hide a duplicate runtime
// instead of rejecting it.
func TryLock(lockPath string) (release func() error, acquired bool, err error) {
	return tryLockFile(lockPath)
}
