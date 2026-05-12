//go:build windows

package main

import (
	"os/exec"
	"strconv"
	"strings"
)

func platformProcessStillActive(pid int) bool {
	output, err := exec.Command("tasklist", "/FI", "PID eq "+strconv.Itoa(pid), "/FO", "CSV", "/NH").CombinedOutput()
	if err != nil {
		return false
	}
	return strings.Contains(string(output), strconv.Itoa(pid))
}
