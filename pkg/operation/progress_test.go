package operation

import "testing"

func TestProgressEventTerminalStates(t *testing.T) {
	for _, tc := range []struct {
		status   ProgressStatus
		terminal bool
	}{
		{status: "", terminal: false},
		{status: ProgressRunning, terminal: false},
		{status: ProgressSucceeded, terminal: true},
		{status: ProgressFailed, terminal: true},
		{status: ProgressCanceled, terminal: true},
	} {
		if got := (ProgressEvent{Status: tc.status}).Terminal(); got != tc.terminal {
			t.Fatalf("status %q: Terminal() = %v, want %v", tc.status, got, tc.terminal)
		}
	}
}
