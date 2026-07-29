//go:build darwin

package workspace

import (
	"os"

	"golang.org/x/sys/unix"
)

// cloneFile attempts an APFS clonefile: a metadata-only copy-on-write
// clone, which is what makes cloning a 1GiB baseline rootfs per workspace
// cheap. Returns false when the filesystem cannot clone (or the target
// exists); the caller falls back to a byte copy.
func cloneFile(source, target string, mode os.FileMode) bool {
	if err := unix.Clonefile(source, target, 0); err != nil {
		return false
	}
	if err := os.Chmod(target, mode); err != nil {
		_ = os.Remove(target)
		return false
	}
	return true
}
