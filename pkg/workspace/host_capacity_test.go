package workspace

import (
	"strings"
	"testing"

	"github.com/geoffbelknap/microagent/pkg/vmkit"
)

func TestDefaultMaxWorkspacesFormula(t *testing.T) {
	cases := []struct {
		name     string
		totalMiB int64
		wantMiB  int
	}{
		{"tiny host clamps to floor", 1024, minWorkspaceCeiling},
		{"below reservation clamps to floor", 512, minWorkspaceCeiling},
		{"exact division", hostReservedMemoryMiB + 10*DefaultWorkspaceMemoryMiB, 10},
		{"huge host clamps to ceiling", 10 * 1024 * 1024, maxWorkspaceCeiling},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := DefaultMaxWorkspaces(tc.totalMiB); got != tc.wantMiB {
				t.Fatalf("DefaultMaxWorkspaces(%d) = %d, want %d", tc.totalMiB, got, tc.wantMiB)
			}
		})
	}
}

func TestMaxWorkspacesEnvOverride(t *testing.T) {
	t.Setenv(MaxWorkspacesEnv, "7")
	n, source := MaxWorkspaces()
	if n != 7 || source != MaxWorkspacesSourceOperator {
		t.Fatalf("MaxWorkspaces() = (%d, %q), want (7, %q)", n, source, MaxWorkspacesSourceOperator)
	}
}

func TestMaxWorkspacesEnvOverrideIgnoresInvalidValues(t *testing.T) {
	for _, raw := range []string{"0", "-1", "not-a-number", ""} {
		t.Run(raw, func(t *testing.T) {
			t.Setenv(MaxWorkspacesEnv, raw)
			_, source := MaxWorkspaces()
			if source == MaxWorkspacesSourceOperator {
				t.Fatalf("MaxWorkspaces() honored invalid override %q", raw)
			}
		})
	}
}

func TestCountActiveWorkspacesCountsOnlyResourceHoldingStates(t *testing.T) {
	dir := t.TempDir()
	makeWorkspace := func(name string, state vmkit.VMState) {
		opts := Options{StateDir: dir, Name: name}
		req, err := Request(opts, "start", "/tmp/rootfs.ext4", NewRequestID())
		if err != nil {
			t.Fatalf("Request(%s): %v", name, err)
		}
		if err := WriteProcessState(opts, req, state, 1, ""); err != nil {
			t.Fatalf("WriteProcessState(%s): %v", name, err)
		}
	}
	makeWorkspace("running-one", vmkit.StateRunning)
	makeWorkspace("running-two", vmkit.StateRunning)
	makeWorkspace("starting-one", vmkit.StateStarting)
	makeWorkspace("paused-one", vmkit.StatePaused)
	makeWorkspace("halted-one", vmkit.StateHalted)
	makeWorkspace("stopped-one", vmkit.StateStopped)
	makeWorkspace("failed-one", vmkit.StateFailed)

	count, err := CountActiveWorkspaces(dir)
	if err != nil {
		t.Fatalf("CountActiveWorkspaces: %v", err)
	}
	if count != 4 {
		t.Fatalf("CountActiveWorkspaces = %d, want 4 (2 running + 1 starting + 1 paused; halted/stopped/failed excluded)", count)
	}
}

func TestEnsureWorkspaceCapacityFailsClosedAtLimit(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(MaxWorkspacesEnv, "2")
	makeRunning := func(name string) {
		opts := Options{StateDir: dir, Name: name}
		req, err := Request(opts, "start", "/tmp/rootfs.ext4", NewRequestID())
		if err != nil {
			t.Fatalf("Request(%s): %v", name, err)
		}
		if err := WriteProcessState(opts, req, vmkit.StateRunning, 1, ""); err != nil {
			t.Fatalf("WriteProcessState(%s): %v", name, err)
		}
	}
	makeRunning("a")
	if err := EnsureWorkspaceCapacity(Options{StateDir: dir, Name: "b"}); err != nil {
		t.Fatalf("EnsureWorkspaceCapacity below limit: %v", err)
	}
	makeRunning("b")
	err := EnsureWorkspaceCapacity(Options{StateDir: dir, Name: "c"})
	if err == nil {
		t.Fatal("EnsureWorkspaceCapacity at limit: want an error")
	}
	if !strings.Contains(err.Error(), "capacity") || !strings.Contains(err.Error(), MaxWorkspacesEnv) {
		t.Fatalf("EnsureWorkspaceCapacity error = %v, want it to name the cause and the override env var", err)
	}
}
