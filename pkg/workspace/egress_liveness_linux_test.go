//go:build linux

package workspace

import (
	"strings"
	"testing"

	"github.com/geoffbelknap/microagent/pkg/vmkit"
)

func TestObserveEgressCaptureRecordsDeadMediatorOnce(t *testing.T) {
	dir := t.TempDir()
	opts := Options{Name: "agent", StateDir: dir, Backend: vmkit.BackendLinuxKVM}
	state := RuntimeState{
		Event:             EventFile{Identity: vmkit.Identity{RuntimeID: "agent", Backend: vmkit.BackendLinuxKVM}, State: vmkit.StateRunning},
		EgressMediatorPID: 1 << 30,
	}
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
