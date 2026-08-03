//go:build linux

package firecracker

import (
	"os"
	"os/exec"
	"testing"

	"github.com/geoffbelknap/microagent/pkg/fsutil"
	"github.com/geoffbelknap/microagent/pkg/workspace"
)

// leaseHeld reports the workspace-side view of the mediation lease: exactly what
// an observer in another PID namespace can see of the mediator.
func leaseHeld(t *testing.T, opts Options) bool {
	t.Helper()
	release, acquired, err := fsutil.TryLock(workspace.EgressMediatorLeasePath(opts.StateDir, opts.Name))
	if err != nil {
		t.Fatal(err)
	}
	if !acquired {
		return true
	}
	if err := release(); err != nil {
		t.Fatal(err)
	}
	return false
}

func TestEgressMediatorLeaseTracksTheProcessThatInheritsIt(t *testing.T) {
	sleep, err := exec.LookPath("sleep")
	if err != nil {
		t.Skipf("sleep is unavailable: %v", err)
	}
	opts := Options{Name: "agent-1", StateDir: t.TempDir()}
	lease, err := acquireEgressMediatorLease(opts)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(sleep, "60")
	cmd.ExtraFiles = []*os.File{lease}
	if err := cmd.Start(); err != nil {
		_ = lease.Close()
		t.Fatal(err)
	}
	// The spawner drops its copy the way startEgressMediator does; the child's
	// inherited descriptor is what must keep the lock held from here on.
	if err := lease.Close(); err != nil {
		t.Fatal(err)
	}
	if !leaseHeld(t, opts) {
		t.Fatal("mediation lease is unheld while the process holding it runs")
	}
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	if _, err := cmd.Process.Wait(); err != nil {
		t.Fatal(err)
	}
	if leaseHeld(t, opts) {
		t.Fatal("mediation lease is still held after the process holding it exited")
	}
}

func TestAcquireEgressMediatorLeaseRefusesASecondHolder(t *testing.T) {
	opts := Options{Name: "agent-1", StateDir: t.TempDir()}
	lease, err := acquireEgressMediatorLease(opts)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lease.Close() }()
	if _, err := acquireEgressMediatorLease(opts); err == nil {
		t.Fatal("acquireEgressMediatorLease succeeded twice for one workspace")
	}
}
