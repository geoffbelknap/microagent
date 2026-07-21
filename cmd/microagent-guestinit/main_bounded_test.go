//go:build linux

package main

import (
	"os/exec"
	"testing"
)

// TestCaptureBoundedCommandBoundsAndFlags is the B18 guard: the primary run-mode
// output capture is size-bounded (so a chatty workload can't OOM PID 1) and
// flags truncation, rather than buffering unbounded output in memory.
func TestCaptureBoundedCommandBoundsAndFlags(t *testing.T) {
	// Emit 10 bytes into a 4-byte cap.
	cmd := exec.Command("sh", "-c", "printf HELLOWORLD")
	stdout, _, stdoutTrunc, _, err := captureBoundedCommand(cmd, 4)
	if err != nil {
		t.Fatalf("captureBoundedCommand: %v", err)
	}
	if len(stdout) > 4 {
		t.Fatalf("captured %d bytes, want <= 4 (bound not applied)", len(stdout))
	}
	if !stdoutTrunc {
		t.Fatal("output over the limit was not flagged truncated")
	}
}

// TestCaptureBoundedCommandExitAndNoTruncation checks the normal path: small
// output is captured whole, not flagged truncated, and the exit is observed.
func TestCaptureBoundedCommandExitAndNoTruncation(t *testing.T) {
	cmd := exec.Command("sh", "-c", "printf hi")
	stdout, _, stdoutTrunc, _, err := captureBoundedCommand(cmd, 0)
	if err != nil {
		t.Fatalf("captureBoundedCommand: %v", err)
	}
	if string(stdout) != "hi" {
		t.Fatalf("stdout = %q, want hi", stdout)
	}
	if stdoutTrunc {
		t.Fatal("small output wrongly flagged truncated")
	}
}
