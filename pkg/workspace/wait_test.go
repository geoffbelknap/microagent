package workspace

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/geoffbelknap/microagent/pkg/vmkit"
)

func TestIsWaitTerminalState(t *testing.T) {
	tests := []struct {
		state vmkit.VMState
		want  bool
	}{
		{state: vmkit.StatePrepared, want: true},
		{state: vmkit.StateStopped, want: true},
		{state: vmkit.StateHalted, want: true},
		{state: vmkit.StateFailed, want: true},
		{state: vmkit.StateQuarantined, want: true},
		{state: vmkit.StateStarting, want: false},
		{state: vmkit.StateRunning, want: false},
		{state: vmkit.StatePaused, want: false},
		{state: vmkit.StateStopping, want: false},
		{state: vmkit.StateUnknown, want: false},
	}
	for _, tt := range tests {
		if got := IsWaitTerminalState(tt.state); got != tt.want {
			t.Fatalf("IsWaitTerminalState(%q) = %v, want %v", tt.state, got, tt.want)
		}
	}
}

func TestWaitStateOK(t *testing.T) {
	tests := []struct {
		state vmkit.VMState
		want  bool
	}{
		{state: vmkit.StatePrepared, want: true},
		{state: vmkit.StateStopped, want: true},
		{state: vmkit.StateHalted, want: true},
		{state: vmkit.StateFailed, want: false},
		{state: vmkit.StateQuarantined, want: false},
		{state: vmkit.StateRunning, want: false},
	}
	for _, tt := range tests {
		if got := WaitStateOK(tt.state); got != tt.want {
			t.Fatalf("WaitStateOK(%q) = %v, want %v", tt.state, got, tt.want)
		}
	}
}

func writeWaitTestState(t *testing.T, dir, name string, state vmkit.VMState) Options {
	t.Helper()
	opts := Options{
		Name:      name,
		StateDir:  dir,
		Backend:   HostBackend(),
		Profile:   "small",
		MemoryMiB: 512,
		CPUCount:  2,
		SizeMiB:   128,
		Network:   vmkit.NetworkConfig{Mode: "isolated"},
	}
	if err := WriteManifest(opts); err != nil {
		t.Fatalf("WriteManifest: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "workspaces", name), 0o755); err != nil {
		t.Fatalf("create workspace dir: %v", err)
	}
	req, err := Request(opts, "run", WorkspaceRootfsPath(dir, name, opts.Backend), NewRequestID())
	if err != nil {
		t.Fatalf("Request: %v", err)
	}
	if err := WriteProcessState(opts, req, state, 0, ""); err != nil {
		t.Fatalf("WriteProcessState: %v", err)
	}
	return opts
}

func TestWaitReturnsImmediatelyOnTerminalState(t *testing.T) {
	tests := []struct {
		state vmkit.VMState
		ok    bool
	}{
		{state: vmkit.StateStopped, ok: true},
		{state: vmkit.StateHalted, ok: true},
		{state: vmkit.StatePrepared, ok: true},
		{state: vmkit.StateFailed, ok: false},
		{state: vmkit.StateQuarantined, ok: false},
	}
	for _, tt := range tests {
		t.Run(string(tt.state), func(t *testing.T) {
			opts := writeWaitTestState(t, t.TempDir(), "wait-"+string(tt.state), tt.state)
			restore := waitInspect
			defer func() { waitInspect = restore }()
			waitInspect = func(ctx context.Context, opts Options) (vmkit.Response, error) {
				t.Fatal("Wait inspected a workspace whose recorded state is already terminal")
				return vmkit.Response{}, nil
			}
			result, err := Wait(context.Background(), opts, WaitOptions{})
			if err != nil {
				t.Fatalf("Wait: %v", err)
			}
			if result.State != string(tt.state) || result.OK != tt.ok || result.Workspace != opts.Name {
				t.Fatalf("Wait result = %#v", result)
			}
		})
	}
}

func TestWaitPollsLiveStateUntilTerminal(t *testing.T) {
	opts := writeWaitTestState(t, t.TempDir(), "wait-live", vmkit.StateRunning)
	restore := waitInspect
	defer func() { waitInspect = restore }()
	calls := 0
	waitInspect = func(ctx context.Context, opts Options) (vmkit.Response, error) {
		calls++
		state := vmkit.StateRunning
		if calls >= 3 {
			state = vmkit.StateStopped
		}
		return vmkit.Response{OK: true, Event: &vmkit.Event{State: state}}, nil
	}
	result, err := Wait(context.Background(), opts, WaitOptions{Interval: time.Millisecond})
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if result.State != string(vmkit.StateStopped) || !result.OK {
		t.Fatalf("Wait result = %#v", result)
	}
	if calls < 3 {
		t.Fatalf("waitInspect calls = %d, want at least 3", calls)
	}
}

func TestWaitTreatsTerminalEventOnInspectErrorAsFinished(t *testing.T) {
	opts := writeWaitTestState(t, t.TempDir(), "wait-inspect-err", vmkit.StateRunning)
	restore := waitInspect
	defer func() { waitInspect = restore }()
	waitInspect = func(ctx context.Context, opts Options) (vmkit.Response, error) {
		return vmkit.Response{Event: &vmkit.Event{State: vmkit.StateFailed}}, errors.New("supervisor exited")
	}
	result, err := Wait(context.Background(), opts, WaitOptions{Interval: time.Millisecond})
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if result.State != string(vmkit.StateFailed) || result.OK {
		t.Fatalf("Wait result = %#v", result)
	}
}

func TestWaitTimeout(t *testing.T) {
	opts := writeWaitTestState(t, t.TempDir(), "wait-timeout", vmkit.StateRunning)
	restore := waitInspect
	defer func() { waitInspect = restore }()
	waitInspect = func(ctx context.Context, opts Options) (vmkit.Response, error) {
		return vmkit.Response{OK: true, Event: &vmkit.Event{State: vmkit.StateRunning}}, nil
	}
	result, err := Wait(context.Background(), opts, WaitOptions{Timeout: 25 * time.Millisecond, Interval: 5 * time.Millisecond})
	if !errors.Is(err, WaitTimeoutError{}) {
		t.Fatalf("Wait error = %v, want WaitTimeoutError", err)
	}
	if result.State != string(vmkit.StateRunning) || result.OK {
		t.Fatalf("Wait result = %#v", result)
	}
	var timeoutErr WaitTimeoutError
	if !errors.As(err, &timeoutErr) || timeoutErr.LastState != vmkit.StateRunning {
		t.Fatalf("WaitTimeoutError = %#v", err)
	}
}

func TestWaitMissingWorkspaceFailsFast(t *testing.T) {
	opts := Options{Name: "wait-missing", StateDir: t.TempDir()}
	_, err := Wait(context.Background(), opts, WaitOptions{Timeout: time.Second})
	if !errors.Is(err, WorkspaceNotFoundError{}) {
		t.Fatalf("Wait error = %v, want WorkspaceNotFoundError", err)
	}
}
