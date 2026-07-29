//go:build linux

package workspace

import (
	"os"

	"golang.org/x/sys/unix"
)

// cloneFile attempts a reflink (FICLONE): on filesystems with shared
// extents (btrfs, XFS with reflink) the copy is metadata-only and
// effectively free, which is what makes cloning a 1GiB baseline rootfs per
// workspace cheap. Returns false when the filesystem or kernel cannot
// reflink; the caller falls back to a byte copy.
func cloneFile(source, target string, mode os.FileMode) bool {
	in, err := os.Open(source)
	if err != nil {
		return false
	}
	defer func() { _ = in.Close() }()
	out, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
	if err != nil {
		return false
	}
	if err := unix.IoctlFileClone(int(out.Fd()), int(in.Fd())); err != nil {
		_ = out.Close()
		_ = os.Remove(target)
		return false
	}
	if err := out.Close(); err != nil {
		_ = os.Remove(target)
		return false
	}
	return true
}
