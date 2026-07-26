package main

import (
	"errors"
	"testing"
)

func TestGuestExitErrorMatchesExec(t *testing.T) {
	nonzero := &guestResult{ExitCode: 3}
	var exitErr cliExitError
	if err := guestExitError(nonzero); !errors.As(err, &exitErr) || exitErr.Code != 3 {
		t.Fatalf("guestExitError = %v, want cliExitError code 3", err)
	}

	if err := guestExitError(&guestResult{ExitCode: 0}); err != nil {
		t.Fatalf("guestExitError(exit 0) = %v, want nil", err)
	}
}
