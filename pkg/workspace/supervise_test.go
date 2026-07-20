package workspace

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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

// TestWriteSuperviseStartFailureRecordsFailedState covers a backend whose runtime
// state the workspace layer owns (apple-vf host supervisor): a start failure IS
// recorded by the workspace layer.
func TestWriteSuperviseStartFailureRecordsFailedState(t *testing.T) {
	dir := t.TempDir()
	opts := Options{
		Name:          "start-fail",
		StateDir:      dir,
		Backend:       vmkit.BackendAppleVF,
		Profile:       "small",
		MemoryMiB:     512,
		CPUCount:      2,
		SizeMiB:       128,
		RestartPolicy: "on-failure",
		Network:       vmkit.NetworkConfig{Mode: "isolated"},
	}
	if vmkit.BackendCapabilities(opts.Backend).OwnsRuntimeState {
		t.Fatalf("test assumes %q does not own runtime state", opts.Backend)
	}
	if err := WriteManifest(opts); err != nil {
		t.Fatalf("WriteManifest: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "workspaces", "start-fail"), 0o755); err != nil {
		t.Fatalf("create workspace dir: %v", err)
	}

	writeSuperviseStartFailure(opts, errors.New("supervisor missing"))
	// Read the record directly: Status() gates on host backend availability, and
	// apple-vf is not available on a linux host, but the state file the workspace
	// layer wrote is host-independent.
	state, err := ReadRuntimeState(opts)
	if err != nil {
		t.Fatalf("ReadRuntimeState: %v", err)
	}
	if state.Event.State != vmkit.StateFailed || !strings.Contains(state.Error, "supervisor missing") {
		t.Fatalf("runtime state = %#v", state)
	}
}

// TestWriteSuperviseStartFailureSkipsSupervisorOwnedState is the B11 guard: for a
// backend that owns runtime state (linux-kvm), a supervise start failure must NOT
// overwrite the supervisor's authoritative record — e.g. an "already running"
// failure must leave the live VM's running state (and its pid) intact rather than
// zeroing it to Failed/pid=0 and orphaning its network resources.
func TestWriteSuperviseStartFailureSkipsSupervisorOwnedState(t *testing.T) {
	dir := t.TempDir()
	opts := Options{
		Name:          "start-fail",
		StateDir:      dir,
		Backend:       vmkit.BackendLinuxKVM,
		Profile:       "small",
		MemoryMiB:     512,
		CPUCount:      2,
		SizeMiB:       128,
		RestartPolicy: "on-failure",
		Network:       vmkit.NetworkConfig{Mode: "isolated"},
	}
	if !vmkit.BackendCapabilities(opts.Backend).OwnsRuntimeState {
		t.Fatalf("test assumes %q owns runtime state", opts.Backend)
	}
	if err := WriteManifest(opts); err != nil {
		t.Fatalf("WriteManifest: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "workspaces", "start-fail"), 0o755); err != nil {
		t.Fatalf("create workspace dir: %v", err)
	}
	// Stand in for the supervisor's authoritative running-state record.
	req, err := Request(opts, "run", WorkspaceRootfsPath(dir, "start-fail", opts.Backend), NewRequestID())
	if err != nil {
		t.Fatalf("Request: %v", err)
	}
	if err := WriteProcessState(opts, req, vmkit.StateRunning, 4242, ""); err != nil {
		t.Fatalf("WriteProcessState: %v", err)
	}

	writeSuperviseStartFailure(opts, errors.New("already running"))

	state, err := ReadRuntimeState(opts)
	if err != nil {
		t.Fatalf("ReadRuntimeState: %v", err)
	}
	if state.Event.State != vmkit.StateRunning || state.PID != 4242 {
		t.Fatalf("supervisor-owned state was clobbered: %#v", state)
	}
}

func writeSupervisedManifest(t *testing.T, dir, name, policy string) {
	t.Helper()
	opts := Options{
		StateDir:      dir,
		Name:          name,
		Backend:       HostBackend(),
		Profile:       "medium",
		MemoryMiB:     2048,
		CPUCount:      2,
		SizeMiB:       8192,
		RestartPolicy: policy,
		Network:       vmkit.NetworkConfig{Mode: "isolated"},
	}
	if err := WriteManifest(opts); err != nil {
		t.Fatalf("WriteManifest: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "workspaces", name), 0o755); err != nil {
		t.Fatal(err)
	}
	rootfsPath := WorkspaceRootfsPath(dir, name, HostBackend())
	if err := os.WriteFile(rootfsPath, []byte("rootfs"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestSupervisedOptionsUseManifestPolicy(t *testing.T) {
	dir := t.TempDir()
	writeSupervisedManifest(t, dir, "research", "on-failure")
	kernelPath := filepath.Join(dir, "Image")
	if err := os.WriteFile(kernelPath, []byte("kernel"), 0o644); err != nil {
		t.Fatal(err)
	}
	opts, err := supervisedOptions(SuperviseOptions{
		StateDir:       dir,
		Name:           "research",
		KernelPath:     kernelPath,
		KernelExplicit: true,
		SupervisorPath: "/tmp/supervisor",
	})
	if err != nil {
		t.Fatalf("supervisedOptions: %v", err)
	}
	if opts.RestartPolicy != "on-failure" || opts.MemoryMiB != 2048 || opts.CPUCount != 2 {
		t.Fatalf("opts = %#v", opts)
	}
}

func TestSuperviseRunsBeforeStartOnEveryBoot(t *testing.T) {
	dir := t.TempDir()
	writeSupervisedManifest(t, dir, "paired", "always")
	kernelPath := filepath.Join(dir, "Image")
	if err := os.WriteFile(kernelPath, []byte("kernel"), 0o644); err != nil {
		t.Fatal(err)
	}
	calls := 0
	result, err := Supervise(context.Background(), SuperviseOptions{
		StateDir:       dir,
		Name:           "paired",
		KernelPath:     kernelPath,
		KernelExplicit: true,
		SupervisorPath: "/tmp/supervisor",
		Interval:       time.Millisecond,
		MaxRestarts:    3,
		BeforeStart: func(ctx context.Context, opts *Options) error {
			calls++
			return errors.New("model runner unavailable")
		},
	})
	if err != nil {
		t.Fatalf("Supervise: %v (result=%#v)", err, result)
	}
	// Every boot attempt — initial and each policy restart — must run the
	// hook before Start so host-side pairings are re-established.
	if calls != 3 || result.Restarts != 3 || !result.Stopped {
		t.Fatalf("calls = %d, result = %#v; want 3 BeforeStart calls", calls, result)
	}
	if result.FinalState != string(vmkit.StateFailed) {
		t.Fatalf("final state = %q", result.FinalState)
	}
	state, err := ReadRuntimeState(Options{StateDir: dir, Name: "paired"})
	if err != nil {
		t.Fatalf("ReadRuntimeState: %v", err)
	}
	if !strings.Contains(state.Error, "model runner unavailable") {
		t.Fatalf("runtime state error = %q", state.Error)
	}
}
