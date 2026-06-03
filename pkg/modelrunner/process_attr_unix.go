//go:build !windows

package modelrunner

import (
	"fmt"
	"os"
	"strings"
	"syscall"
)

func detachedSysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setpgid: true}
}

// processIsZombie reports whether pid is a zombie process. On Linux, a process
// that has been sent SIGTERM but whose parent called Release() (detached) will
// linger as a zombie until init reaps it; kill(pid, 0) still succeeds on a
// zombie, so Signal(0) alone cannot distinguish alive from zombie.
func processIsZombie(pid int) bool {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/status", pid))
	if err != nil {
		// /proc not available (macOS) or process already gone — not a zombie.
		return false
	}
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "State:") {
			return strings.Contains(line, "Z")
		}
	}
	return false
}
