package workspace

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/geoffbelknap/microagent/pkg/operation"
	"github.com/geoffbelknap/microagent/pkg/vmkit"
)

func TestGCReportsBoundedProgressAndPartialFailures(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"broken", "stale"} {
		opts := Options{StateDir: dir, Name: name}
		req := vmkit.Request{
			Identity: &vmkit.Identity{RequestID: "req-" + name, RuntimeID: name, Role: vmkit.RoleWorkload, Backend: vmkit.BackendLinuxKVM},
			Config:   &vmkit.Config{StateDir: dir},
		}
		if err := WriteProcessState(opts, req, vmkit.StateRunning, 1234, ""); err != nil {
			t.Fatal(err)
		}
	}
	original := gcControl
	t.Cleanup(func() { gcControl = original })
	gcControl = func(_ context.Context, opts Options, command string) (vmkit.Response, error) {
		if command != "gc" {
			t.Fatalf("command = %q", command)
		}
		if opts.Progress != nil {
			t.Fatal("aggregate GC leaked its progress callback into per-workspace control")
		}
		if opts.Name == "broken" {
			return vmkit.Response{}, errors.New("supervisor unavailable")
		}
		return vmkit.Response{OK: true, Event: &vmkit.Event{State: vmkit.StateStopped}}, nil
	}
	var events []operation.ProgressEvent
	result, err := GC(context.Background(), Options{StateDir: dir, Progress: func(event operation.ProgressEvent) {
		events = append(events, event)
	}})
	if err != nil {
		t.Fatal(err)
	}
	if result.Checked != 2 || len(result.Reaped) != 1 || result.Reaped[0].Name != "stale" || len(result.Failed) != 1 || result.Failed[0].Name != "broken" {
		t.Fatalf("result = %#v", result)
	}
	var phases []string
	for _, event := range events {
		phases = append(phases, event.Phase)
		if event.Phase == "gc_reconcile" && event.Total != 2 {
			t.Fatalf("reconcile total = %d", event.Total)
		}
	}
	if want := []string{"gc_scan", "gc_reconcile", "gc_reconcile", "gc_complete"}; !reflect.DeepEqual(phases, want) {
		t.Fatalf("phases = %v, want %v", phases, want)
	}
}

func TestGCStopsBeforeDispatchWhenCanceled(t *testing.T) {
	dir := t.TempDir()
	opts := Options{StateDir: dir, Name: "running"}
	req := vmkit.Request{
		Identity: &vmkit.Identity{RequestID: "req", RuntimeID: opts.Name, Role: vmkit.RoleWorkload, Backend: vmkit.BackendLinuxKVM},
		Config:   &vmkit.Config{StateDir: dir},
	}
	if err := WriteProcessState(opts, req, vmkit.StateRunning, 1234, ""); err != nil {
		t.Fatal(err)
	}
	original := gcControl
	t.Cleanup(func() { gcControl = original })
	gcControl = func(context.Context, Options, string) (vmkit.Response, error) {
		t.Fatal("canceled GC dispatched workspace control")
		return vmkit.Response{}, nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := GC(ctx, Options{StateDir: dir})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}
