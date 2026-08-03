//go:build linux

package workspace

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/geoffbelknap/microagent/pkg/fsutil"
	"github.com/geoffbelknap/microagent/pkg/vmkit"
)

func runningMediatorState(pid int) RuntimeState {
	return RuntimeState{
		Event:             EventFile{Identity: vmkit.Identity{RuntimeID: "agent", Backend: vmkit.BackendLinuxKVM}, State: vmkit.StateRunning},
		EgressMediatorPID: pid,
	}
}

// recordedMediationLease creates the workspace's mediation lease without holding
// it: the state a workspace is left in once its mediator exits.
func recordedMediationLease(t *testing.T, dir string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, "agent"), 0o700); err != nil {
		t.Fatal(err)
	}
	path := EgressMediatorLeasePath(dir, "agent")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestObserveEgressCaptureRecordsDeadMediatorOnce(t *testing.T) {
	dir := t.TempDir()
	opts := Options{Name: "agent", StateDir: dir, Backend: vmkit.BackendLinuxKVM}
	recordedMediationLease(t, dir)
	state := runningMediatorState(1 << 30)
	report := vmkit.NegotiateEgressCapture(vmkit.BackendLinuxKVM, "user", "broker")

	observeEgressCapture(opts, state, &report)
	if report.Live == nil || *report.Live {
		t.Fatalf("egress capture live = %v, want observed false", report.Live)
	}
	if !strings.Contains(report.LivenessDetail, "not running") {
		t.Fatalf("liveness detail = %q", report.LivenessDetail)
	}
	observeEgressCapture(opts, state, &report)
	events, err := ReadEvents(dir, "agent")
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || !strings.HasPrefix(events[0].Detail, egressMediatorFailureDetail) {
		t.Fatalf("events = %#v, want one persistent enforcement failure", events)
	}
}

// A mediator PID is recorded by the supervisor that spawned it, and under user
// networking that supervisor sits in the nested PID namespace pasta created. The
// number it records therefore names an unrelated process here, if it names one
// at all — the test's own PID stands in for that — and it must not be allowed to
// contradict a held lease.
func TestObserveEgressCaptureTrustsTheLeaseOverAForeignPID(t *testing.T) {
	dir := t.TempDir()
	opts := Options{Name: "agent", StateDir: dir, Backend: vmkit.BackendLinuxKVM}
	release, err := fsutil.Lock(recordedMediationLease(t, dir))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = release() }()
	report := vmkit.NegotiateEgressCapture(vmkit.BackendLinuxKVM, "user", "broker")

	observeEgressCapture(opts, runningMediatorState(os.Getpid()), &report)
	if report.Live == nil || !*report.Live {
		t.Fatalf("egress capture live = %v (%s), want observed true", report.Live, report.LivenessDetail)
	}
	assertNoEnforcementFailure(t, dir)
}

// A runtime recorded before the mediator held a lease carries no evidence either
// way, and missing evidence must not be published as a dead mediator.
func TestObserveEgressCaptureDoesNotClaimLivenessWithoutALease(t *testing.T) {
	dir := t.TempDir()
	opts := Options{Name: "agent", StateDir: dir, Backend: vmkit.BackendLinuxKVM}
	report := vmkit.NegotiateEgressCapture(vmkit.BackendLinuxKVM, "user", "broker")

	observeEgressCapture(opts, runningMediatorState(os.Getpid()), &report)
	if report.Live != nil {
		t.Fatalf("egress capture live = %v, want omitted without a recorded lease", report.Live)
	}
	assertNoEnforcementFailure(t, dir)
}

func TestObserveEgressCaptureDoesNotClaimUnobservedLiveness(t *testing.T) {
	report := vmkit.NegotiateEgressCapture(vmkit.BackendLinuxKVM, "user", "broker")
	observeEgressCapture(Options{Name: "agent", StateDir: t.TempDir()}, RuntimeState{}, &report)
	if report.Live != nil {
		t.Fatalf("egress capture live = %v, want omitted without recorded mediator PID", report.Live)
	}
	if report.LivenessDetail == "" {
		t.Fatal("unobserved liveness detail is empty")
	}
}

func assertNoEnforcementFailure(t *testing.T, dir string) {
	t.Helper()
	events, err := ReadEvents(dir, "agent")
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	for _, event := range events {
		if strings.HasPrefix(event.Detail, egressMediatorFailureDetail) {
			t.Fatalf("events = %#v, want no enforcement failure recorded", events)
		}
	}
}
