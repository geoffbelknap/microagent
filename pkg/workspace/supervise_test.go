package workspace

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
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

func TestWriteSuperviseStartFailureRecordsFailedState(t *testing.T) {
	dir := t.TempDir()
	opts := Options{
		Name:          "start-fail",
		StateDir:      dir,
		Backend:       HostBackend(),
		Profile:       "small",
		MemoryMiB:     512,
		CPUCount:      2,
		SizeMiB:       128,
		RestartPolicy: "on-failure",
		Network:       vmkit.NetworkConfig{Mode: "isolated"},
	}
	if err := WriteManifest(opts); err != nil {
		t.Fatalf("WriteManifest: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "workspaces", "start-fail"), 0o755); err != nil {
		t.Fatalf("create workspace dir: %v", err)
	}

	writeSuperviseStartFailure(opts, errors.New("supervisor missing"))
	resp, err := Status(opts)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if resp.Event == nil || resp.Event.State != vmkit.StateFailed {
		t.Fatalf("status event = %#v", resp.Event)
	}
	state, err := ReadRuntimeState(opts)
	if err != nil {
		t.Fatalf("ReadRuntimeState: %v", err)
	}
	if state.Event.State != vmkit.StateFailed || !strings.Contains(state.Error, "supervisor missing") {
		t.Fatalf("runtime state = %#v", state)
	}
}
