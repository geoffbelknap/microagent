//go:build !windows

package windows_hyperv

import (
	"fmt"
	"time"
)

func waitForProcessExit(pid int, timeout time.Duration) error {
	return fmt.Errorf("windows-hyperv process exit wait is only supported on windows")
}
