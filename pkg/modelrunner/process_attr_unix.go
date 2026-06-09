//go:build !windows

package modelrunner

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"
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
	if err == nil {
		for _, line := range strings.Split(string(data), "\n") {
			if strings.HasPrefix(line, "State:") {
				return strings.Contains(line, "Z")
			}
		}
		return false
	}
	// macOS and other Unix hosts do not expose /proc by default. Fall back to
	// ps(1), where zombie processes include "Z" in the stat column.
	out, err := exec.Command("ps", "-o", "stat=", "-p", strconv.Itoa(pid)).Output()
	return err == nil && strings.Contains(string(out), "Z")
}
