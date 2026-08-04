//go:build darwin

package workspace

import "golang.org/x/sys/unix"

// hostTotalMemoryMiB reports the host's total physical memory via the
// hw.memsize sysctl (bytes). Total, not available: see the linux
// implementation's comment for why.
func hostTotalMemoryMiB() (int64, error) {
	bytes, err := unix.SysctlUint64("hw.memsize")
	if err != nil {
		return 0, err
	}
	return int64(bytes / (1024 * 1024)), nil
}
