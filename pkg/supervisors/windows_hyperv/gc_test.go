package windows_hyperv

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/geoffbelknap/microagent/pkg/vmkit"
)

func TestLeaseExpired(t *testing.T) {
	dir := t.TempDir()
	name := "agent-lease"
	state := func(lease int, startedAgo time.Duration) runtimeState {
		return runtimeState{
			Config:    vmkit.Config{LeaseSeconds: lease},
			StartedAt: time.Now().Add(-startedAgo).Format(time.RFC3339),
		}
	}
	if leaseExpired(state(0, time.Hour), dir, name) {
		t.Fatal("no lease declared: must be permanent")
	}
	if !leaseExpired(state(10, time.Hour), dir, name) {
		t.Fatal("idle well past the lease: must expire")
	}
	if leaseExpired(state(3600, time.Minute), dir, name) {
		t.Fatal("started a minute ago with an hour lease: must not expire")
	}

	// Recent activity renews the lease even with an old StartedAt.
	actPath := filepath.Join(dir, name, "activity")
	if err := os.MkdirAll(filepath.Dir(actPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(actPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if err := os.Chtimes(actPath, now, now); err != nil {
		t.Fatal(err)
	}
	if leaseExpired(state(3600, time.Hour), dir, name) {
		t.Fatal("recent activity must renew the lease")
	}
}

func TestDeadmanPollInterval(t *testing.T) {
	cases := []struct {
		lease int
		want  time.Duration
	}{
		{1, time.Second},        // lease/4 < 1s -> clamped up
		{4, time.Second},        // 4/4
		{40, 10 * time.Second},  // 40/4
		{400, 60 * time.Second}, // clamped down
	}
	for _, c := range cases {
		if got := deadmanPollInterval(c.lease); got != c.want {
			t.Fatalf("deadmanPollInterval(%d) = %s, want %s", c.lease, got, c.want)
		}
	}
}

func TestGCReapsStaleAndSparesHealthy(t *testing.T) {
	const computeID = "fake"
	const runtimeID = "11111111-1111-1111-1111-111111111111"
	req := func(stateDir string) vmkit.Request {
		return vmkit.Request{
			Command:  "gc",
			Identity: &vmkit.Identity{RuntimeID: "agent-gc", Backend: vmkit.BackendWindowsHyperV},
			Config:   &vmkit.Config{StateDir: stateDir},
		}
	}

	// Stale: recorded running, but HCS no longer knows the compute system.
	stale := req(t.TempDir())
	if _, err := writeRuntimeTransitionWithComputeIDs(stale, vmkit.StateRunning, "running", "", computeID, runtimeID); err != nil {
		t.Fatal(err)
	}
	resp, err := (Supervisor{adapter: &fakeAdapter{gone: true}}).gc(context.Background(), stale)
	if err != nil {
		t.Fatalf("gc stale: %v", err)
	}
	if resp.Event == nil || resp.Event.State != vmkit.StateStopped {
		t.Fatalf("stale not reaped: %+v", resp.Event)
	}

	// Healthy: running, compute system alive, no lease -> left untouched.
	healthy := req(t.TempDir())
	if _, err := writeRuntimeTransitionWithComputeIDs(healthy, vmkit.StateRunning, "running", "", computeID, runtimeID); err != nil {
		t.Fatal(err)
	}
	resp, err = (Supervisor{adapter: &fakeAdapter{}}).gc(context.Background(), healthy)
	if err != nil {
		t.Fatalf("gc healthy: %v", err)
	}
	if resp.Event == nil || resp.Event.State != vmkit.StateRunning {
		t.Fatalf("healthy VM reaped: %+v", resp.Event)
	}
}
