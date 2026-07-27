package main

import (
	"os/exec"
	"testing"
)

// TestClassifyRunError pins the one distinction StartError exists for: an
// error WITH an exit status means the workload ran and failed (the code's
// problem); an error WITHOUT one means no process ever existed (the
// environment's problem). res.Error alone carried both as text — "exit
// status 5" vs "fork/exec /bin/sh: no such file or directory" — and the only
// way to tell them apart downstream was matching that text, the exact
// anti-pattern the typed-error rules exist to prevent.
func TestClassifyRunError(t *testing.T) {
	t.Run("ran and failed: exit status, no start error", func(t *testing.T) {
		err := exec.Command("sh", "-c", "exit 5").Run()
		code, startErr := classifyRunError(err)
		if code != 5 || startErr != "" {
			t.Errorf("got code=%d startErr=%q; a real exit is the code's own", code, startErr)
		}
	})

	t.Run("never ran: start error carries the diagnosis", func(t *testing.T) {
		err := exec.Command("/nonexistent-binary-xyz").Run()
		code, startErr := classifyRunError(err)
		if startErr == "" {
			t.Fatal("no start error for a process that never existed")
		}
		if code != 1 {
			t.Errorf("code = %d, want the generic 1 (there is no real status)", code)
		}
	})

	t.Run("killed by signal: ran, so not a start failure", func(t *testing.T) {
		cmd := exec.Command("sh", "-c", "kill -KILL $$")
		err := cmd.Run()
		code, startErr := classifyRunError(err)
		if startErr != "" {
			t.Errorf("a signaled process ran; startErr=%q misroutes blame to the environment", startErr)
		}
		if code != -1 {
			t.Errorf("code = %d, want -1 for a signaled exit", code)
		}
	})

	t.Run("clean", func(t *testing.T) {
		code, startErr := classifyRunError(nil)
		if code != 0 || startErr != "" {
			t.Errorf("got %d, %q for nil", code, startErr)
		}
	})
}
