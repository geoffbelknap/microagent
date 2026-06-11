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
