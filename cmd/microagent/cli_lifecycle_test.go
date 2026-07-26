package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/geoffbelknap/microagent/pkg/rootfs"
	"github.com/geoffbelknap/microagent/pkg/vmkit"
	"github.com/geoffbelknap/microagent/pkg/workspace"
)

func TestRunStatusUsesWorkspaceStateDefaults(t *testing.T) {
	dir := t.TempDir()
	req := vmkit.Request{
		Command: "inspect",
		Identity: &vmkit.Identity{
			RequestID: "req-1",
			RuntimeID: "research",
			Role:      vmkit.RoleWorkload,
			Backend:   vmkit.BackendAppleVF,
		},
		Config: &vmkit.Config{
			KernelPath: filepath.Join(dir, "Image"),
			RootfsPath: filepath.Join(dir, "rootfs.ext4"),
			StateDir:   dir,
			MemoryMiB:  512,
			CPUCount:   2,
		},
	}
	if err := writeWorkspaceProcessState(workspaceOptions{StateDir: dir, Name: "research", Backend: vmkit.BackendAppleVF}, req, vmkit.StateRunning, startWorkspaceReferencingProcess(t, dir, "research"), ""); err != nil {
		t.Fatalf("writeWorkspaceProcessState: %v", err)
	}
	if err := writeWorkspaceManifest(workspaceOptions{StateDir: dir, Name: "research", Profile: "small", RestartPolicy: "always", MemoryMiB: 512, CPUCount: 2, SizeMiB: 1024}); err != nil {
		t.Fatal(err)
	}
	stdoutPath := filepath.Join(dir, "stdout.json")
	stdout, err := os.Create(stdoutPath)
	if err != nil {
		t.Fatal(err)
	}
	err = run(t.Context(), []string{
		"status",
		"--state-dir", dir,
		"--name", "research",
	}, stdout)
	if closeErr := stdout.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	data, err := os.ReadFile(stdoutPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"state": "running"`) || !strings.Contains(string(data), `"restartPolicy": "always"`) {
		t.Fatalf("status output = %s", data)
	}
}

func TestWriteWorkspaceProcessStateAppendsEventHistory(t *testing.T) {
	dir := t.TempDir()
	req := vmkit.Request{
		Command: "inspect",
		Identity: &vmkit.Identity{
			RequestID: "req-1",
			RuntimeID: "research",
			Role:      vmkit.RoleWorkload,
			Backend:   vmkit.BackendAppleVF,
		},
		Config: &vmkit.Config{
			KernelPath: filepath.Join(dir, "Image"),
			RootfsPath: filepath.Join(dir, "rootfs.ext4"),
			StateDir:   dir,
			MemoryMiB:  512,
			CPUCount:   2,
		},
	}
	opts := workspaceOptions{StateDir: dir, Name: "research", Backend: vmkit.BackendAppleVF}
	if err := writeWorkspaceProcessState(opts, req, vmkit.StatePrepared, 0, ""); err != nil {
		t.Fatalf("write prepared state: %v", err)
	}
	if err := writeWorkspaceProcessState(opts, req, vmkit.StateHalted, 0, ""); err != nil {
		t.Fatalf("write halted state: %v", err)
	}
	if err := writeWorkspaceProcessState(opts, req, vmkit.StateQuarantined, 0, ""); err != nil {
		t.Fatalf("write quarantined state: %v", err)
	}
	var events []workspaceEventFile
	data, err := os.ReadFile(filepath.Join(dir, "research", "events.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &events); err != nil {
		t.Fatal(err)
	}
	if len(events) != 3 || events[0].State != vmkit.StatePrepared || events[1].State != vmkit.StateHalted || events[2].State != vmkit.StateQuarantined {
		t.Fatalf("events = %#v, want prepared, halted, then quarantined", events)
	}
}

func TestStatusReportsRecordedVerificationForPreparedWorkspace(t *testing.T) {
	outputFormat = "json"
	t.Cleanup(func() { outputFormat = "" })
	dir := t.TempDir()
	kernelPath := filepath.Join(dir, "Image")
	rootfsPath := filepath.Join(dir, "workspaces", "research", "rootfs.ext4")
	initPath := filepath.Join(dir, "microagent-init")
	if err := os.MkdirAll(filepath.Dir(rootfsPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(kernelPath, []byte("kernel-v1"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(rootfsPath, []byte("rootfs-v1"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(initPath, []byte("init-v1"), 0o644); err != nil {
		t.Fatal(err)
	}
	opts := workspaceOptions{
		StateDir:      dir,
		Name:          "research",
		Backend:       hostBackend(),
		KernelPath:    kernelPath,
		GuestInitPath: initPath,
		Profile:       "small",
		RestartPolicy: "never",
		MemoryMiB:     512,
		CPUCount:      2,
		SizeMiB:       1024,
	}
	result := workspaceResult{
		Workspace:  "research",
		RootfsPath: rootfsPath,
		Image: rootfs.Provenance{
			ImageRef:    "docker.io/library/busybox:1.36",
			ResolvedRef: "docker.io/library/busybox@sha256:abc",
			Digest:      "sha256:abc",
		},
	}
	verification, err := buildWorkspaceVerification(opts, result)
	if err != nil {
		t.Fatal(err)
	}
	opts.Verification = &verification
	if err := writeWorkspaceManifest(opts); err != nil {
		t.Fatal(err)
	}
	req := vmkit.Request{
		Identity: &vmkit.Identity{RequestID: "req-1", RuntimeID: "research", Role: vmkit.RoleWorkload, Backend: hostBackend()},
		Config: &vmkit.Config{
			KernelPath: kernelPath,
			RootfsPath: rootfsPath,
			StateDir:   dir,
			MemoryMiB:  512,
			CPUCount:   2,
		},
	}
	if err := writeWorkspaceProcessState(opts, req, vmkit.StatePrepared, 0, ""); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(rootfsPath, []byte("rootfs-v2"), 0o644); err != nil {
		t.Fatal(err)
	}
	stdoutPath := filepath.Join(dir, "stdout.json")
	stdout, err := os.Create(stdoutPath)
	if err != nil {
		t.Fatal(err)
	}
	err = run(t.Context(), []string{"status", "research", "--state-dir", dir, "--backend", hostBackend()}, stdout)
	if closeErr := stdout.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	if err != nil {
		t.Fatalf("run status: %v", err)
	}
	var resp vmkit.Response
	data, err := os.ReadFile(stdoutPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Verification == nil || !resp.Verification.OK {
		t.Fatalf("verification = %#v, want recorded verification without divergence", resp.Verification)
	}
	if len(resp.Verification.Divergence) != 0 {
		t.Fatalf("divergence = %#v, want none for prepared status fast path", resp.Verification.Divergence)
	}
	if resp.Verification.ImageDigest != "sha256:abc" || resp.Verification.Kernel.SHA256 == "" || resp.Verification.Rootfs.RecordedSHA256 == "" {
		t.Fatalf("verification details missing: %#v", resp.Verification)
	}
}

func TestStatusReportsReadinessSignals(t *testing.T) {
	outputFormat = "json"
	t.Cleanup(func() { outputFormat = "" })
	dir := t.TempDir()
	serialInput := serialInputPath(dir, "research")
	if err := os.MkdirAll(filepath.Dir(serialInput), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(serialInput, []byte{}, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(resultPath(workspaceOptions{StateDir: dir, Name: "research"}), []byte(`{"started_at":"2026-05-02T00:00:00Z","exited_at":"2026-05-02T00:00:01Z","exit_code":0}`), 0o644); err != nil {
		t.Fatal(err)
	}
	req := vmkit.Request{
		Identity: &vmkit.Identity{RequestID: "req-1", RuntimeID: "research", Role: vmkit.RoleWorkload, Backend: hostBackend()},
		Config: &vmkit.Config{
			KernelPath: filepath.Join(dir, "Image"),
			RootfsPath: filepath.Join(dir, "rootfs.ext4"),
			StateDir:   dir,
			MemoryMiB:  512,
			CPUCount:   2,
		},
	}
	if err := writeWorkspaceProcessState(workspaceOptions{StateDir: dir, Name: "research"}, req, vmkit.StateRunning, startWorkspaceReferencingProcess(t, dir, "research"), ""); err != nil {
		t.Fatal(err)
	}
	stdoutPath := filepath.Join(dir, "stdout.json")
	stdout, err := os.Create(stdoutPath)
	if err != nil {
		t.Fatal(err)
	}
	err = run(t.Context(), []string{"status", "research", "--state-dir", dir, "--backend", hostBackend()}, stdout)
	if closeErr := stdout.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	if err != nil {
		t.Fatalf("run status: %v", err)
	}
	var resp vmkit.Response
	data, err := os.ReadFile(stdoutPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Readiness == nil {
		t.Fatal("readiness missing")
	}
	if !resp.Readiness.GuestReady.Ready || !resp.Readiness.ShellReady.Ready || !resp.Readiness.ResultReady.Ready {
		t.Fatalf("readiness = %#v, want all ready", resp.Readiness)
	}
	if resp.Result == nil || resp.Result.ExitCode != 0 || resp.Result.CompletedAt != "2026-05-02T00:00:01Z" {
		t.Fatalf("result = %#v, want structured result", resp.Result)
	}
}

func TestInspectAliasDefaultsToJSONStatus(t *testing.T) {
	dir := t.TempDir()
	req := vmkit.Request{
		Identity: &vmkit.Identity{RequestID: "req-1", RuntimeID: "research", Role: vmkit.RoleWorkload, Backend: hostBackend()},
		Config: &vmkit.Config{
			KernelPath: filepath.Join(dir, "Image"),
			RootfsPath: filepath.Join(dir, "rootfs.ext4"),
			StateDir:   dir,
			MemoryMiB:  512,
			CPUCount:   2,
		},
	}
	if err := writeWorkspaceProcessState(workspaceOptions{StateDir: dir, Name: "research"}, req, vmkit.StateStopped, 0, ""); err != nil {
		t.Fatal(err)
	}
	stdoutPath := filepath.Join(dir, "status.json")
	stdout, err := os.Create(stdoutPath)
	if err != nil {
		t.Fatal(err)
	}
	err = run(t.Context(), []string{"status", "research", "--state-dir", dir, "--backend", hostBackend()}, stdout)
	if closeErr := stdout.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	if err != nil {
		t.Fatalf("run status: %v", err)
	}
	data, err := os.ReadFile(stdoutPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"runtimeID": "research"`) || !strings.Contains(string(data), `"state": "stopped"`) {
		t.Fatalf("status output = %s", data)
	}
}

func TestRunResultReportsStructuredResult(t *testing.T) {
	outputFormat = "json"
	t.Cleanup(func() { outputFormat = "" })
	dir := t.TempDir()
	req := vmkit.Request{
		Identity: &vmkit.Identity{RequestID: "req-1", RuntimeID: "research", Role: vmkit.RoleWorkload, Backend: hostBackend()},
		Config: &vmkit.Config{
			KernelPath: filepath.Join(dir, "Image"),
			RootfsPath: filepath.Join(dir, "rootfs.ext4"),
			StateDir:   dir,
			MemoryMiB:  512,
			CPUCount:   2,
		},
	}
	if err := writeWorkspaceProcessState(workspaceOptions{StateDir: dir, Name: "research"}, req, vmkit.StateStopped, 0, ""); err != nil {
		t.Fatal(err)
	}
	resultJSON := `{"started_at":"2026-05-02T00:00:00Z","exited_at":"2026-05-02T00:00:01Z","exit_code":7,"stdout":"done\n","stderr":"warn\n"}`
	if err := os.WriteFile(resultPath(workspaceOptions{StateDir: dir, Name: "research"}), []byte(resultJSON), 0o644); err != nil {
		t.Fatal(err)
	}
	stdoutPath := filepath.Join(dir, "result.json")
	stdout, err := os.Create(stdoutPath)
	if err != nil {
		t.Fatal(err)
	}
	err = run(t.Context(), []string{"result", "research", "--state-dir", dir, "--backend", hostBackend()}, stdout)
	if closeErr := stdout.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	if err != nil {
		t.Fatalf("run result: %v", err)
	}
	var resp vmkit.Response
	data, err := os.ReadFile(stdoutPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Result == nil {
		t.Fatal("result missing")
	}
	if resp.Result.Identity.RuntimeID != "research" || resp.Result.ExitCode != 7 || resp.Result.Stdout != "done\n" || resp.Result.Stderr != "warn\n" {
		t.Fatalf("result = %#v", resp.Result)
	}
	if resp.Result.ResultPath == "" || resp.Result.Backend != hostBackend() {
		t.Fatalf("result metadata = %#v", resp.Result)
	}
}

func TestRunDeleteRemovesSavedWorkspaceState(t *testing.T) {
	dir := t.TempDir()
	supervisor := filepath.Join(dir, "supervisor")
	backend := hostBackend()
	script := `#!/usr/bin/env bash
set -euo pipefail
python3 -c 'import json,sys; req=json.load(sys.stdin); assert req["command"] == "delete"; print(json.dumps({"ok": True, "backend": "` + backend + `", "event": {"identity": req["identity"], "state": "stopped", "detail": "deleted", "observedAt": "2026-05-02T00:00:00Z"}}))'
`
	if err := os.WriteFile(supervisor, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "workspaces", "research"), 0o755); err != nil {
		t.Fatal(err)
	}
	// Give the workspace a real runtime/event record (not just a bare
	// directory) so the delete existence probe finds it instead of
	// short-circuiting with WorkspaceNotFoundError.
	testFirecrackerRuntimeState(t, dir, "research", vmkit.StateStopped, 0)
	stdoutPath := filepath.Join(dir, "stdout.json")
	stdout, err := os.Create(stdoutPath)
	if err != nil {
		t.Fatal(err)
	}
	err = run(t.Context(), []string{
		"delete",
		"--backend", backend,
		"--supervisor", supervisor,
		"--state-dir", dir,
		"--yes",
		"research",
	}, stdout)
	if closeErr := stdout.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "workspaces", "research")); !os.IsNotExist(err) {
		t.Fatalf("workspace root still exists after delete: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "research")); !os.IsNotExist(err) {
		t.Fatalf("runtime state still exists after delete: %v", err)
	}
}

// TestRunDeleteYesOnFullyMissingWorkspace pins I2: a workspace with no root
// directory and no runtime/event records is genuinely nonexistent, so
// `delete --yes` on it must still report WorkspaceNotFoundError rather than
// proceeding.
func TestRunDeleteYesOnFullyMissingWorkspace(t *testing.T) {
	dir := t.TempDir()
	_, err := runDeleteWorkspace(t.Context(), workspaceOptions{StateDir: dir, Name: "no-such-ws", Backend: hostBackend()}, true, false)
	var nf workspace.WorkspaceNotFoundError
	if !errors.As(err, &nf) {
		t.Fatalf("err = %v, want WorkspaceNotFoundError", err)
	}
}

// TestRunDeletePartiallyCreatedWorkspaceProceeds pins I2: a workspace whose
// root directory exists (e.g. a disk was written) but has no runtime/event
// record yet - a crash between rootfs build and the first supervisor event -
// is partially created, not nonexistent. `delete --yes` on it must proceed
// and remove the directory instead of short-circuiting on the same
// WorkspaceNotFoundError a fully-missing workspace reports. This restores the
// bare-directory delete semantics TestRunDeleteRemovesSavedWorkspaceState
// exercised before the delete existence probe was added.
func TestRunDeletePartiallyCreatedWorkspaceProceeds(t *testing.T) {
	dir := t.TempDir()
	supervisor := filepath.Join(dir, "supervisor")
	backend := hostBackend()
	script := `#!/usr/bin/env bash
set -euo pipefail
python3 -c 'import json,sys; req=json.load(sys.stdin); assert req["command"] == "delete"; print(json.dumps({"ok": True, "backend": "` + backend + `", "event": {"identity": req["identity"], "state": "stopped", "detail": "deleted", "observedAt": "2026-05-02T00:00:00Z"}}))'
`
	if err := os.WriteFile(supervisor, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	// Bare root directory only - no runtime state, no event file.
	if err := os.MkdirAll(filepath.Join(dir, "workspaces", "research"), 0o755); err != nil {
		t.Fatal(err)
	}
	resp, err := runDeleteWorkspace(t.Context(), workspaceOptions{
		StateDir:       dir,
		Name:           "research",
		Backend:        backend,
		SupervisorPath: supervisor,
	}, true, false)
	if err != nil {
		t.Fatalf("runDeleteWorkspace: %v", err)
	}
	if !resp.OK {
		t.Fatalf("resp not ok: %#v", resp)
	}
	if _, statErr := os.Stat(filepath.Join(dir, "workspaces", "research")); !os.IsNotExist(statErr) {
		t.Fatalf("workspace root still exists after delete: %v", statErr)
	}
}

func TestDeleteRequiresConfirmationWithoutTTY(t *testing.T) {
	dir := t.TempDir()
	testFirecrackerRuntimeState(t, dir, "research", vmkit.StateStopped, 0)
	oldTerminal := stdinIsTerminal
	t.Cleanup(func() { stdinIsTerminal = oldTerminal })
	stdinIsTerminal = func() bool { return false }
	_, err := runDeleteWorkspace(t.Context(), workspaceOptions{StateDir: dir, Name: "research", Backend: hostBackend()}, false, false)
	if err == nil || !strings.Contains(err.Error(), "pass --yes") {
		t.Fatalf("err = %v, want --yes confirmation error", err)
	}
}

func TestDeleteCancelsWhenConfirmationDeclines(t *testing.T) {
	dir := t.TempDir()
	testFirecrackerRuntimeState(t, dir, "research", vmkit.StateStopped, 0)
	oldTerminal := stdinIsTerminal
	oldConfirm := readConfirmation
	t.Cleanup(func() {
		stdinIsTerminal = oldTerminal
		readConfirmation = oldConfirm
	})
	stdinIsTerminal = func() bool { return true }
	readConfirmation = func(string) (bool, error) { return false, nil }
	_, err := runDeleteWorkspace(t.Context(), workspaceOptions{StateDir: dir, Name: "research", Backend: hostBackend()}, false, false)
	if err == nil || !strings.Contains(err.Error(), "delete cancelled") {
		t.Fatalf("err = %v, want cancellation", err)
	}
}

func TestDeleteMissingWorkspaceDoesNotPrompt(t *testing.T) {
	oldTerminal := stdinIsTerminal
	t.Cleanup(func() { stdinIsTerminal = oldTerminal })
	// A prompt would need a TTY (or --yes/--force) to resolve; forcing "no TTY"
	// here means any path that reaches the prompt fails on "pass --yes", not on
	// WorkspaceNotFoundError, so this also proves the not-found check runs first.
	stdinIsTerminal = func() bool { return false }
	opts := workspaceOptions{Name: "no-such-ws", StateDir: t.TempDir()}
	_, err := runDeleteWorkspace(context.Background(), opts, false, false)
	var nf workspace.WorkspaceNotFoundError
	if !errors.As(err, &nf) {
		t.Fatalf("want WorkspaceNotFoundError before any prompt, got %v", err)
	}
}
