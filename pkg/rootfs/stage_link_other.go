//go:build !windows

package rootfs

import (
	"fmt"
	"os"
	"syscall"
)

// stageHardLinkID returns a stable identity for a regular stage file that
// has more than one name (a hard link), so the stage tar can emit later
// names as tar hard links instead of duplicating content. Returns ok=false
// for singly-linked files and whenever the identity cannot be read (the
// caller then falls back to copying the content, which is always correct).
func stageHardLinkID(_ string, info os.FileInfo) (string, bool) {
	if !info.Mode().IsRegular() {
		return "", false
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Nlink <= 1 {
		return "", false
	}
	return fmt.Sprintf("%d-%d", stat.Dev, stat.Ino), true
}
