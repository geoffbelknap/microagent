package hostworker

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestEnsureProcessSpawnsIndexesAndReusesMediator(t *testing.T) {
	dir := t.TempDir()
	var spawned [][]string
	var logPaths []string
	prevSpawn, prevStop, prevLive, prevProbe := spawnProcess, stopProcess, processLive, probeMediatorHealth
	spawnProcess = func(argv []string, env []string, logPath string) (int, error) {
		spawned = append(spawned, append([]string{}, argv...))
		logPaths = append(logPaths, logPath)
		return 12345, nil
	}
	stopProcess = func(pid int) error { return nil }
	processLive = func(pid int) bool { return pid == 12345 }
	probeMediatorHealth = func(ctx context.Context, url string, timeout time.Duration) error { return nil }
	t.Cleanup(func() {
		spawnProcess = prevSpawn
		stopProcess = prevStop
		processLive = prevLive
		probeMediatorHealth = prevProbe
	})

	opts := ProcessOptions{
		StateDir:      dir,
		WorkspaceID:   "ws",
		Capability:    DefaultCapability,
		WorkerID:      "worker-a",
		TargetBaseURL: "http://127.0.0.1:9000/v1",
		Mode:          ModeLocalAllow,
		ExecPath:      "/bin/microagent",
	}
	rec, err := EnsureProcess(context.Background(), opts)
	if err != nil {
		t.Fatalf("EnsureProcess: %v", err)
	}
	if rec.PID != 12345 || rec.Host != defaultListenHost || rec.Port <= 0 || rec.AuditLogPath == "" {
		t.Fatalf("unexpected record: %+v", rec)
	}
	if len(spawned) != 1 {
		t.Fatalf("spawn count = %d", len(spawned))
	}
	wantArgs := []string{
		"/bin/microagent",
		"--host-worker-mediator",
		"--target-base-url", "http://127.0.0.1:9000/v1",
		"--bind-host", "127.0.0.1",
		"--mode", "local-allow",
		"--workspace-id", "ws",
		"--capability", DefaultCapability,
		"--worker-id", "worker-a",
	}
	for _, want := range wantArgs {
		if !contains(spawned[0], want) {
			t.Fatalf("spawn argv missing %q: %#v", want, spawned[0])
		}
	}
	if len(logPaths) != 1 || !strings.Contains(logPaths[0], "host-workers") {
		t.Fatalf("log paths = %#v", logPaths)
	}
	idx, err := ReadProcessIndex(dir)
	if err != nil {
		t.Fatalf("ReadProcessIndex: %v", err)
	}
	if !reflect.DeepEqual(idx.Mediators, []ProcessRecord{rec}) {
		t.Fatalf("index = %+v, want record %+v", idx.Mediators, rec)
	}

	reused, err := EnsureProcess(context.Background(), opts)
	if err != nil {
		t.Fatalf("EnsureProcess reuse: %v", err)
	}
	if len(spawned) != 1 {
		t.Fatalf("reuse spawned again: %#v", spawned)
	}
	if !reflect.DeepEqual(reused, rec) {
		t.Fatalf("reused = %+v, want %+v", reused, rec)
	}
}

func TestEnsureProcessReplacesStaleMediator(t *testing.T) {
	dir := t.TempDir()
	var stopped []int
	prevSpawn, prevStop, prevLive, prevProbe := spawnProcess, stopProcess, processLive, probeMediatorHealth
	spawnProcess = func(argv []string, env []string, logPath string) (int, error) { return 222, nil }
	stopProcess = func(pid int) error {
		stopped = append(stopped, pid)
		return nil
	}
	processLive = func(pid int) bool { return pid != 111 }
	probeMediatorHealth = func(ctx context.Context, url string, timeout time.Duration) error { return nil }
	t.Cleanup(func() {
		spawnProcess = prevSpawn
		stopProcess = prevStop
		processLive = prevLive
		probeMediatorHealth = prevProbe
	})
	if err := WriteProcessIndex(dir, ProcessIndex{Mediators: []ProcessRecord{{
		Key:           processKey("ws", DefaultCapability),
		WorkspaceID:   "ws",
		Capability:    DefaultCapability,
		WorkerID:      "old",
		TargetBaseURL: "http://127.0.0.1:9000/v1",
		Mode:          ModeLocalAllow,
		PID:           111,
	}}}); err != nil {
		t.Fatal(err)
	}

	rec, err := EnsureProcess(context.Background(), ProcessOptions{
		StateDir:      dir,
		WorkspaceID:   "ws",
		WorkerID:      "new",
		TargetBaseURL: "http://127.0.0.1:9000/v1",
		Mode:          ModeLocalAllow,
		ExecPath:      "/bin/microagent",
	})
	if err != nil {
		t.Fatalf("EnsureProcess: %v", err)
	}
	if rec.PID != 222 || rec.WorkerID != "new" {
		t.Fatalf("record = %+v", rec)
	}
	if !reflect.DeepEqual(stopped, []int{111}) {
		t.Fatalf("stopped = %#v", stopped)
	}
}

func TestReleaseProcessStopsAndRemovesMediator(t *testing.T) {
	dir := t.TempDir()
	prevStop := stopProcess
	var stopped []int
	stopProcess = func(pid int) error {
		stopped = append(stopped, pid)
		return nil
	}
	t.Cleanup(func() { stopProcess = prevStop })
	if err := WriteProcessIndex(dir, ProcessIndex{Mediators: []ProcessRecord{{
		Key:         processKey("ws", DefaultCapability),
		WorkspaceID: "ws",
		Capability:  DefaultCapability,
		PID:         123,
	}}}); err != nil {
		t.Fatal(err)
	}

	if err := ReleaseProcess(dir, "ws", ""); err != nil {
		t.Fatalf("ReleaseProcess: %v", err)
	}
	if !reflect.DeepEqual(stopped, []int{123}) {
		t.Fatalf("stopped = %#v", stopped)
	}
	idx, err := ReadProcessIndex(dir)
	if err != nil {
		t.Fatalf("ReadProcessIndex: %v", err)
	}
	if len(idx.Mediators) != 0 {
		t.Fatalf("mediator not removed: %+v", idx.Mediators)
	}
}

func TestEnsureProcessValidatesPolicyMode(t *testing.T) {
	_, err := EnsureProcess(context.Background(), ProcessOptions{
		StateDir:      t.TempDir(),
		WorkspaceID:   "ws",
		TargetBaseURL: "http://127.0.0.1:9000/v1",
		Mode:          ModePolicy,
		ExecPath:      "/bin/microagent",
	})
	if err == nil || !strings.Contains(err.Error(), "policy URL is required") {
		t.Fatalf("error = %v", err)
	}
}

func TestWaitMediatorHealthyHonorsProbe(t *testing.T) {
	prevProbe := probeMediatorHealth
	calls := 0
	probeMediatorHealth = func(ctx context.Context, url string, timeout time.Duration) error {
		calls++
		if calls == 2 {
			return nil
		}
		return errors.New("not ready")
	}
	t.Cleanup(func() { probeMediatorHealth = prevProbe })
	rec := ProcessRecord{Host: "127.0.0.1", Port: 1}
	if err := waitMediatorHealthy(context.Background(), rec, time.Second); err != nil {
		t.Fatalf("waitMediatorHealthy: %v", err)
	}
	if calls != 2 {
		t.Fatalf("probe calls = %d", calls)
	}
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
