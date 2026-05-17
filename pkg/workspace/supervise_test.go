package workspace

import (
	"testing"

	"github.com/geoffbelknap/microagent/pkg/vmkit"
)

func TestSupervisedTerminalState(t *testing.T) {
	tests := []struct {
		state vmkit.VMState
		want  bool
	}{
		{state: vmkit.StateHalted, want: true},
		{state: vmkit.StateQuarantined, want: true},
		{state: vmkit.StateStopped, want: true},
		{state: vmkit.StateFailed, want: true},
		{state: vmkit.StateRunning, want: false},
		{state: vmkit.StatePrepared, want: false},
		{state: vmkit.StateUnknown, want: false},
	}
	for _, tt := range tests {
		if got := isSupervisedTerminalState(tt.state); got != tt.want {
			t.Fatalf("isSupervisedTerminalState(%q) = %v, want %v", tt.state, got, tt.want)
		}
	}
}
