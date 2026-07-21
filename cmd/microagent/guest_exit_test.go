package main

import (
	"errors"
	"testing"
)

// TestGuestExitErrorMatchesExecAcrossModes is the B21 guard: run/dispatch map a
// guest exit code the same way `exec` does in BOTH modes — AX carries the code
// in the result envelope and exits 0; human mode propagates it as the process
// exit status. Previously run/dispatch propagated the code even in AX mode,
// diverging from exec and from run.md.
func TestGuestExitErrorMatchesExecAcrossModes(t *testing.T) {
	old := globalOutputMode
	t.Cleanup(func() { globalOutputMode = old })

	nonzero := &guestResult{ExitCode: 3}

	globalOutputMode = outputModeAX
	if err := guestExitError(nonzero); err != nil {
		t.Fatalf("AX guestExitError = %v, want nil (exit 0; code carried in result.exit_code)", err)
	}

	globalOutputMode = outputModeUX
	var exitErr cliExitError
	if err := guestExitError(nonzero); !errors.As(err, &exitErr) || exitErr.Code != 3 {
		t.Fatalf("UX guestExitError = %v, want cliExitError code 3", err)
	}

	// A zero exit is nil in both modes.
	globalOutputMode = outputModeUX
	if err := guestExitError(&guestResult{ExitCode: 0}); err != nil {
		t.Fatalf("UX guestExitError(exit 0) = %v, want nil", err)
	}
}
