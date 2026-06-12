//go:build !windows

package perf

import (
	"fmt"
	"os/exec"
	"strconv"
)

func processRSSKiB(pid int) (int64, error) {
	output, err := exec.Command("ps", "-o", "rss=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return 0, fmt.Errorf("inspect pid %d rss: %w", pid, err)
	}
	return ParseRSSKiB(output)
}
