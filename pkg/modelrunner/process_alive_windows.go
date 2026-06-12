//go:build windows

package modelrunner

import (
	"os"

	"golang.org/x/sys/windows"
)

// processAlive reports whether the runner process still runs. Signal(0)
// is not implemented on Windows (os.Process.Signal always errors there,
// which made every live runner self-heal out of the registry), so the
// probe asks the kernel for the process exit code instead.
func processAlive(pid int) bool {
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

// stopProcess terminates the runner. Windows has no SIGTERM; the engine
// holds no state worth a graceful pass, so terminate is the stop.
func stopProcess(pid int) error {
	if pid <= 0 {
		return nil
	}
	p, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	return p.Kill()
}
