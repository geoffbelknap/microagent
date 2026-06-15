package modelrunner

import (
	"context"
	"reflect"
	"runtime"
	"testing"
	"time"
)

// longRunningArgv is a portable stand-in for a model server process: it must
// outlive the test and exist on a bare host ("sleep" is not on a Windows
// PATH outside Git Bash; ping with a count waits about a second per echo).
func longRunningArgv() []string {
	if runtime.GOOS == "windows" {
		return []string{"ping", "-n", "60", "127.0.0.1"}
	}
	return []string{"sleep", "30"}
}

type fakeEngine struct{}

func (fakeEngine) Name() string                     { return "fake" }
func (fakeEngine) Argv(_, _ string, _ int) []string { return longRunningArgv() }
func (fakeEngine) HealthPath() string               { return "/health" }

func withHealthyProbe(t *testing.T) {
	t.Helper()
	prev := probeHealth
	probeHealth = func(_ context.Context, _ string, _ time.Duration) error { return nil }
	t.Cleanup(func() { probeHealth = prev })
}

func TestEnsureSpawnsReusesAndReaps(t *testing.T) {
	withHealthyProbe(t)
	dir := t.TempDir()
	base := EnsureOptions{StateDir: dir, ModelRef: "hf.co/o/r@main/m.gguf", ModelPath: "/tmp/m.gguf", Engine: fakeEngine{}, ReadyTimeout: 3 * time.Second}

	o1 := base
	o1.Holder = "vm1"
	r1, err := Ensure(context.Background(), o1)
	if err != nil {
		t.Fatalf("Ensure vm1: %v", err)
	}
	if r1.PID <= 0 || r1.ReadyAt == "" || !processAlive(r1.PID) {
		t.Fatalf("runner not healthy/tracked: %+v", r1)
	}

	// Second holder reuses the same process.
	o2 := base
	o2.Holder = "vm2"
	r2, err := Ensure(context.Background(), o2)
	if err != nil {
		t.Fatalf("Ensure vm2: %v", err)
	}
	if r2.PID != r1.PID {
		t.Fatalf("expected reuse of PID %d, got %d", r1.PID, r2.PID)
	}

	o3 := base
	o3.Holder = "vm3"
	o3.RunnerConfig = RunnerConfig{Args: []string{"--gpu"}}
	r3, err := Ensure(context.Background(), o3)
	if err != nil {
		t.Fatalf("Ensure vm3: %v", err)
	}
	defer stopProcess(r3.PID)
	if r3.PID == r1.PID {
		t.Fatal("runner with different config reused the existing process")
	}
	if r3.RunnerConfigDigest == "" || !reflect.DeepEqual(r3.RunnerArgs, []string{"--gpu"}) {
		t.Fatalf("runner config not recorded: %+v", r3)
	}

	// Releasing one holder keeps it alive.
	if err := Release(dir, base.ModelRef, "vm1"); err != nil {
		t.Fatalf("Release vm1: %v", err)
	}
	if !processAlive(r1.PID) {
		t.Fatal("runner died while vm2 still holds it")
	}
	// Releasing the last holder reaps it.
	if err := Release(dir, base.ModelRef, "vm2"); err != nil {
		t.Fatalf("Release vm2: %v", err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for processAlive(r1.PID) && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if processAlive(r1.PID) {
		t.Fatal("runner not reaped after last release")
		_ = stopProcess(r1.PID)
	}
	_ = Release(dir, base.ModelRef, "vm3")
	list, _ := List(dir)
	if len(list) != 0 {
		t.Fatalf("expected empty registry, got %+v", list)
	}
}

func TestEnsurePinnedSurvivesNoHolders(t *testing.T) {
	withHealthyProbe(t)
	dir := t.TempDir()
	o := EnsureOptions{StateDir: dir, ModelRef: "hf.co/o/r@main/m.gguf", ModelPath: "/tmp/m.gguf", Engine: fakeEngine{}, Pinned: true, ReadyTimeout: 3 * time.Second}
	r, err := Ensure(context.Background(), o)
	if err != nil {
		t.Fatalf("Ensure pinned: %v", err)
	}
	defer stopProcess(r.PID)
	// No holders, but pinned: Release of a non-holder must not reap it.
	if err := Release(dir, o.ModelRef, "ghost"); err != nil {
		t.Fatalf("Release: %v", err)
	}
	if !processAlive(r.PID) {
		t.Fatal("pinned runner was reaped")
	}
	list, _ := List(dir)
	if len(list) != 1 {
		t.Fatalf("expected 1 pinned runner, got %+v", list)
	}
	// Stop force-removes it.
	n, err := Stop(dir, o.ModelRef)
	if err != nil || n != 1 {
		t.Fatalf("Stop: n=%d err=%v", n, err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for processAlive(r.PID) && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if processAlive(r.PID) {
		t.Fatal("runner not stopped")
	}
}

func TestEnsureReadinessTimeoutReaps(t *testing.T) {
	prev := probeHealth
	probeHealth = func(_ context.Context, _ string, _ time.Duration) error { return context.DeadlineExceeded }
	t.Cleanup(func() { probeHealth = prev })
	dir := t.TempDir()
	o := EnsureOptions{StateDir: dir, ModelRef: "hf.co/o/r@main/m.gguf", ModelPath: "/tmp/m.gguf", Engine: fakeEngine{}, Holder: "vm1", ReadyTimeout: 400 * time.Millisecond}
	_, err := Ensure(context.Background(), o)
	if err == nil {
		t.Fatal("expected readiness timeout error")
	}
	list, _ := List(dir)
	if len(list) != 0 {
		t.Fatalf("expected runner reaped on readiness failure, got %+v", list)
	}
}
