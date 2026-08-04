//go:build linux

package workspace

import "syscall"

// hostTotalMemoryMiB reports the host's total physical memory. Total, not
// available, so the workspace-count ceiling it feeds is a stable property of
// the machine rather than something that shrinks under the host's own cache
// pressure and rejects a create that would otherwise succeed once that cache
// is reclaimed.
func hostTotalMemoryMiB() (int64, error) {
	var info syscall.Sysinfo_t
	if err := syscall.Sysinfo(&info); err != nil {
		return 0, err
	}
	return int64(info.Totalram * uint64(info.Unit) / (1024 * 1024)), nil
}
