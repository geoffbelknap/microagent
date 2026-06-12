//go:build windows

package windows_hyperv

import (
	"fmt"
	"time"

	"golang.org/x/sys/windows"
)

// waitForProcessExit polls until the process is gone or the timeout elapses.
// Signal(0) is not implemented on Windows, so the probe asks the kernel for
// the process exit code.
func waitForProcessExit(pid int, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		if !windowsProcessAlive(pid) {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("process %d is still running after %s", pid, timeout)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func windowsProcessAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	handle, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return false
	}
	defer func() { _ = windows.CloseHandle(handle) }()
	var code uint32
	if err := windows.GetExitCodeProcess(handle, &code); err != nil {
		return false
	}
	// STILL_ACTIVE (259): the process has not exited.
	const stillActive = 259
	return code == stillActive
}
