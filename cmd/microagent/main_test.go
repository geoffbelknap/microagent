package main

import (
	"crypto/sha256"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/geoffbelknap/microagent-kit/pkg/rootfs"
	"github.com/geoffbelknap/microagent-kit/pkg/vmkit"
)

func TestRunVersionAliases(t *testing.T) {
	for _, args := range [][]string{{"version"}, {"--version"}, {"-v"}} {
		dir := t.TempDir()
		stdoutPath := filepath.Join(dir, "stdout.txt")
		stdout, err := os.Create(stdoutPath)
		if err != nil {
			t.Fatal(err)
		}
		err = run(t.Context(), args, stdout)
		if closeErr := stdout.Close(); closeErr != nil {
			t.Fatal(closeErr)
		}
		if err != nil {
			t.Fatalf("run(%v): %v", args, err)
		}
		data, err := os.ReadFile(stdoutPath)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(string(data), "microagent ") {
			t.Fatalf("version output = %q", data)
		}
	}
}

func TestFirecrackerDoctorDoesNotRequireAppleVFHelper(t *testing.T) {
	resp, err := firecrackerDoctorResponse(
		vmkit.BackendFirecracker,
		"amd64",
		func() (string, error) { return "/usr/local/bin/firecracker", nil },
		func(path string) (os.FileInfo, error) {
			switch path {
			case "/dev/kvm", "/dev/vhost-vsock":
				return fakeFileInfo{name: filepath.Base(path)}, nil
			default:
				return nil, os.ErrNotExist
			}
		},
		func(path string) string {
			if path != "/usr/local/bin/firecracker" {
				t.Fatalf("version path = %q", path)
			}
			return "Firecracker v1.15.1"
		},
	)
	if err != nil {
		t.Fatalf("firecrackerDoctorResponse: %v", err)
	}
	if !resp.OK {
		t.Fatalf("OK = false, error = %q", resp.Error)
	}
	if resp.Backend != vmkit.BackendFirecracker {
		t.Fatalf("Backend = %q, want %q", resp.Backend, vmkit.BackendFirecracker)
	}
	if resp.Host == nil {
		t.Fatal("Host is nil")
	}
	if resp.Host.BinaryPath != "/usr/local/bin/firecracker" {
		t.Fatalf("BinaryPath = %q", resp.Host.BinaryPath)
	}
	if resp.Host.BinaryVersion != "Firecracker v1.15.1" {
		t.Fatalf("BinaryVersion = %q", resp.Host.BinaryVersion)
	}
	if !resp.Host.VirtualizationSupported || !resp.Host.KVMAvailable || !resp.Host.VsockAvailable {
		t.Fatalf("Host support = %+v", resp.Host)
	}
}

func TestFirecrackerDoctorReportsMissingHostSupport(t *testing.T) {
	resp, err := firecrackerDoctorResponse(
		vmkit.BackendFirecracker,
		"amd64",
		func() (string, error) { return "", fmt.Errorf("firecracker binary not found") },
		func(string) (os.FileInfo, error) { return nil, os.ErrNotExist },
		func(string) string { return "" },
	)
	if err == nil {
		t.Fatal("firecrackerDoctorResponse returned nil error")
	}
	if resp.OK {
		t.Fatal("OK = true, want false")
	}
	if resp.Host == nil {
		t.Fatal("Host is nil")
	}
	if resp.Host.FrameworkAvailable || resp.Host.VirtualizationSupported || resp.Host.KVMAvailable {
		t.Fatalf("Host support = %+v", resp.Host)
	}
	if !strings.Contains(resp.Error, "firecracker binary not found") || !strings.Contains(resp.Error, "/dev/kvm") {
		t.Fatalf("Error = %q", resp.Error)
	}
}

func TestResolveFirecrackerPathUsesEnvironment(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "firecracker")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MICROAGENT_FIRECRACKER", path)
	got, err := resolveFirecrackerPath()
	if err != nil {
		t.Fatalf("resolveFirecrackerPath: %v", err)
	}
	if got != path {
		t.Fatalf("path = %q, want %q", got, path)
	}
}

func TestDefaultFirecrackerPathResolvesHomebrewSymlink(t *testing.T) {
	dir := t.TempDir()
	cellarVersion := "test-version"
	cellarBin := filepath.Join(dir, "Cellar", "microagent-kit", cellarVersion, "bin")
	cellarLibexec := filepath.Join(dir, "Cellar", "microagent-kit", cellarVersion, "libexec")
	homebrewBin := filepath.Join(dir, "bin")
	if err := os.MkdirAll(cellarBin, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(cellarLibexec, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(homebrewBin, 0o755); err != nil {
		t.Fatal(err)
	}
	executable := filepath.Join(cellarBin, "microagent")
	if err := os.WriteFile(executable, []byte("binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	firecracker := filepath.Join(cellarLibexec, "firecracker")
	if err := os.WriteFile(firecracker, []byte("firecracker"), 0o755); err != nil {
		t.Fatal(err)
	}
	resolvedFirecracker, err := filepath.EvalSymlinks(firecracker)
	if err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(homebrewBin, "microagent")
	if err := os.Symlink(executable, link); err != nil {
		t.Fatal(err)
	}
	if got := defaultFirecrackerPathFromExecutable(link); got != resolvedFirecracker {
		t.Fatalf("defaultFirecrackerPathFromExecutable() = %q, want %q", got, resolvedFirecracker)
	}
}

func TestFirstOutputLine(t *testing.T) {
	output := "\nFirecracker v1.15.1\n\n2026-05-02T17:44:08 [anonymous-instance:main] Firecracker exiting successfully. exit_code=0\n"
	if got := firstOutputLine(output); got != "Firecracker v1.15.1" {
		t.Fatalf("firstOutputLine() = %q", got)
	}
}

type fakeFileInfo struct {
	name string
}

func (f fakeFileInfo) Name() string       { return f.name }
func (f fakeFileInfo) Size() int64        { return 0 }
func (f fakeFileInfo) Mode() os.FileMode  { return 0 }
func (f fakeFileInfo) ModTime() time.Time { return time.Time{} }
func (f fakeFileInfo) IsDir() bool        { return false }
func (f fakeFileInfo) Sys() any           { return nil }

func TestRequestForCommandMapsHumanCommands(t *testing.T) {
	tests := []struct {
		name        string
		args        []string
		wantCommand string
	}{
		{
			name:        "doctor",
			args:        []string{"doctor"},
			wantCommand: "host",
		},
		{
			name: "create",
			args: []string{
				"create",
				"--id", "agent-1",
				"--kernel", "/tmp/kernel",
				"--rootfs", "/tmp/rootfs.ext4",
				"--state-dir", "/tmp/state",
			},
			wantCommand: "prepare",
		},
		{
			name: "create dry run",
			args: []string{
				"create",
				"--dry-run",
				"--id", "agent-1",
				"--kernel", "/tmp/kernel",
				"--rootfs", "/tmp/rootfs.ext4",
				"--state-dir", "/tmp/state",
			},
			wantCommand: "check",
		},
		{
			name: "start",
			args: []string{
				"start",
				"--id", "agent-1",
				"--kernel", "/tmp/kernel",
				"--rootfs", "/tmp/rootfs.ext4",
				"--state-dir", "/tmp/state",
			},
			wantCommand: "start",
		},
		{
			name:        "status",
			args:        []string{"status", "agent-1", "--state-dir", "/tmp/state"},
			wantCommand: "inspect",
		},
		{
			name:        "stop",
			args:        []string{"stop", "agent-1", "--state-dir", "/tmp/state"},
			wantCommand: "stop",
		},
		{
			name:        "kill",
			args:        []string{"kill", "agent-1", "--state-dir", "/tmp/state"},
			wantCommand: "kill",
		},
		{
			name:        "delete",
			args:        []string{"delete", "agent-1", "--state-dir", "/tmp/state"},
			wantCommand: "delete",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req, err := requestForCommand(tt.args[0], newFlagSet(tt.args[0]), reorderFlagArgs(tt.args[1:]))
			if err != nil {
				t.Fatalf("requestForCommand: %v", err)
			}
			if req.Command != tt.wantCommand {
				t.Fatalf("Command = %q, want %q", req.Command, tt.wantCommand)
			}
			if tt.args[0] != "doctor" && req.Identity.RuntimeID != "agent-1" {
				t.Fatalf("RuntimeID = %q, want agent-1", req.Identity.RuntimeID)
			}
		})
	}
}

func TestRequestForCommandReadsJSONFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "request.json")
	req := vmkit.Request{
		Identity: &vmkit.Identity{RequestID: "req-1", RuntimeID: "agent-1", Role: vmkit.RoleWorkload, Backend: vmkit.BackendAppleVF},
		Config:   &vmkit.Config{KernelPath: "/tmp/kernel", RootfsPath: "/tmp/rootfs.ext4", StateDir: "/tmp/state"},
	}
	data, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := requestForCommand("create", newFlagSet("create"), []string{"-json", path})
	if err != nil {
		t.Fatalf("requestForCommand: %v", err)
	}
	if got.Command != "prepare" {
		t.Fatalf("Command = %q, want prepare", got.Command)
	}
	if got.Identity.RuntimeID != "agent-1" {
		t.Fatalf("RuntimeID = %q, want agent-1", got.Identity.RuntimeID)
	}
}

func TestRequestForCommandParsesVsock(t *testing.T) {
	req, err := requestForCommand("create", newFlagSet("create"), reorderFlagArgs([]string{
		"--id", "agent-1",
		"--kernel", "/tmp/kernel",
		"--rootfs", "/tmp/rootfs.ext4",
		"--state-dir", "/tmp/state",
		"--vsock", "1024=127.0.0.1:8200",
	}))
	if err != nil {
		t.Fatalf("requestForCommand: %v", err)
	}
	if len(req.Config.VsockListeners) != 1 {
		t.Fatalf("VsockListeners len = %d, want 1", len(req.Config.VsockListeners))
	}
	listener := req.Config.VsockListeners[0]
	if listener.Port != 1024 || listener.Target != "127.0.0.1:8200" {
		t.Fatalf("listener = %#v", listener)
	}
}

func TestRunUsesHelperOverride(t *testing.T) {
	dir := t.TempDir()
	helper := filepath.Join(dir, "helper")
	script := `#!/usr/bin/env bash
set -euo pipefail
python3 -c 'import json,sys; req=json.load(sys.stdin); print(json.dumps({"ok": True, "backend": "apple-vf", "event": {"identity": req["identity"], "state": "prepared", "observedAt": "2026-05-02T00:00:00Z"}}))'
`
	if err := os.WriteFile(helper, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	stdoutPath := filepath.Join(dir, "stdout.json")
	stdout, err := os.Create(stdoutPath)
	if err != nil {
		t.Fatal(err)
	}
	err = run(t.Context(), []string{
		"create",
		"--helper", helper,
		"--id", "agent-1",
		"--kernel", "/tmp/kernel",
		"--rootfs", "/tmp/rootfs.ext4",
		"--state-dir", "/tmp/state",
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
	if !strings.Contains(string(data), `"state": "prepared"`) {
		t.Fatalf("stdout missing prepared state: %s", data)
	}
}

func TestRunStatusUsesWorkspaceStateDefaults(t *testing.T) {
	dir := t.TempDir()
	helper := filepath.Join(dir, "helper")
	script := `#!/usr/bin/env bash
set -euo pipefail
python3 -c 'import json,sys; req=json.load(sys.stdin); assert req["command"] == "inspect"; assert req["identity"]["runtimeID"] == "research"; assert req["config"]["stateDir"]; print(json.dumps({"ok": True, "backend": "apple-vf", "event": {"identity": req["identity"], "state": "running", "observedAt": "2026-05-02T00:00:00Z"}}))'
`
	if err := os.WriteFile(helper, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	stdoutPath := filepath.Join(dir, "stdout.json")
	stdout, err := os.Create(stdoutPath)
	if err != nil {
		t.Fatal(err)
	}
	err = run(t.Context(), []string{
		"status",
		"--helper", helper,
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
	if !strings.Contains(string(data), `"state": "running"`) {
		t.Fatalf("status output = %s", data)
	}
}

func TestRunDeleteRemovesSavedWorkspaceState(t *testing.T) {
	dir := t.TempDir()
	helper := filepath.Join(dir, "helper")
	script := `#!/usr/bin/env bash
set -euo pipefail
python3 -c 'import json,sys; req=json.load(sys.stdin); assert req["command"] == "delete"; print(json.dumps({"ok": True, "backend": "apple-vf", "event": {"identity": req["identity"], "state": "stopped", "detail": "deleted", "observedAt": "2026-05-02T00:00:00Z"}}))'
`
	if err := os.WriteFile(helper, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "workspaces", "research"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "research"), 0o755); err != nil {
		t.Fatal(err)
	}
	stdoutPath := filepath.Join(dir, "stdout.json")
	stdout, err := os.Create(stdoutPath)
	if err != nil {
		t.Fatal(err)
	}
	err = run(t.Context(), []string{
		"delete",
		"--helper", helper,
		"--state-dir", dir,
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

func TestRunRootFSValidatesRequiredFlags(t *testing.T) {
	dir := t.TempDir()
	stdoutPath := filepath.Join(dir, "stdout.json")
	stdout, err := os.Create(stdoutPath)
	if err != nil {
		t.Fatal(err)
	}
	err = runRootFS(t.Context(), []string{"build"}, stdout)
	if closeErr := stdout.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	if err == nil || !strings.Contains(err.Error(), "image_ref is required") {
		t.Fatalf("err = %v, want image_ref validation", err)
	}
}

func TestRootFSExecMapsToShellCommand(t *testing.T) {
	var req rootfs.BuildRequest
	execCommand := "echo hello"
	if strings.TrimSpace(execCommand) != "" {
		req.Command = []string{"/bin/sh", "-lc", execCommand}
	}
	if got := strings.Join(req.Command, " "); got != "/bin/sh -lc echo hello" {
		t.Fatalf("Command = %q", got)
	}
}

func TestParseWorkspaceOptionsForRun(t *testing.T) {
	opts, err := parseWorkspaceOptions("run", []string{
		"--image", "docker.io/library/ubuntu:24.04",
		"--exec", "uname -a",
		"--setup", "apt-get update",
		"--setup", "apt-get install -y git",
		"--name", "research",
		"--kernel", "/tmp/kernel",
		"--state-dir", "/tmp/microagent-state",
		"--mke2fs", "/tmp/mke2fs",
		"--arch", "arm64",
		"--memory", "1024",
		"--cpus", "4",
		"--size-mib", "2048",
	})
	if err != nil {
		t.Fatalf("parseWorkspaceOptions: %v", err)
	}
	if opts.ImageRef != "docker.io/library/ubuntu:24.04" {
		t.Fatalf("ImageRef = %q", opts.ImageRef)
	}
	if opts.ExecCommand != "uname -a" {
		t.Fatalf("ExecCommand = %q", opts.ExecCommand)
	}
	if len(opts.SetupCommands) != 2 || opts.SetupCommands[0] != "apt-get update" || opts.SetupCommands[1] != "apt-get install -y git" {
		t.Fatalf("SetupCommands = %#v", opts.SetupCommands)
	}
	if opts.Name != "research" {
		t.Fatalf("Name = %q", opts.Name)
	}
	if opts.KernelPath != "/tmp/kernel" {
		t.Fatalf("KernelPath = %q", opts.KernelPath)
	}
	if opts.MemoryMiB != 1024 || opts.CPUCount != 4 || opts.SizeMiB != 2048 {
		t.Fatalf("resource opts = memory %d cpus %d size %d", opts.MemoryMiB, opts.CPUCount, opts.SizeMiB)
	}
}

func TestWorkspaceCommandRunsSetupBeforeExec(t *testing.T) {
	command := workspaceCommand(workspaceOptions{
		SetupCommands: []string{"apt-get update", "apt-get install -y git"},
		ExecCommand:   "uname -a",
	})
	want := "set -eu\napt-get update\napt-get install -y git\nuname -a"
	if command != want {
		t.Fatalf("workspaceCommand = %q, want %q", command, want)
	}
}

func TestWorkspaceCommandAllowsMultiCommandExec(t *testing.T) {
	command := workspaceCommand(workspaceOptions{
		SetupCommands: []string{"echo setup"},
		ExecCommand:   "echo one; echo two",
	})
	want := "set -eu\necho setup\necho one; echo two"
	if command != want {
		t.Fatalf("workspaceCommand = %q, want %q", command, want)
	}
}

func TestWorkspaceCommandResetsGuestConfigForCreatedWorkspace(t *testing.T) {
	command := workspaceCommand(workspaceOptions{
		SetupCommands:   []string{"echo setup"},
		ResultPort:      1024,
		PrepareForStart: true,
	})
	if !strings.Contains(command, "echo setup") {
		t.Fatalf("workspaceCommand missing setup: %q", command)
	}
	if !strings.Contains(command, `> /etc/microagent/run.json`) || !strings.Contains(command, `"command":[]`) {
		t.Fatalf("workspaceCommand missing guest config reset: %q", command)
	}
}

func TestDefaultGuestInitPathResolvesHomebrewSymlink(t *testing.T) {
	dir := t.TempDir()
	cellarBin := filepath.Join(dir, "Cellar", "microagent-kit", "0.1.14", "bin")
	cellarLibexec := filepath.Join(dir, "Cellar", "microagent-kit", "0.1.14", "libexec")
	homebrewBin := filepath.Join(dir, "bin")
	if err := os.MkdirAll(cellarBin, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(cellarLibexec, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(homebrewBin, 0o755); err != nil {
		t.Fatal(err)
	}
	executable := filepath.Join(cellarBin, "microagent")
	if err := os.WriteFile(executable, []byte("binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	guestInit := filepath.Join(cellarLibexec, "microagent-guestinit-arm64")
	if err := os.WriteFile(guestInit, []byte("guest-init"), 0o755); err != nil {
		t.Fatal(err)
	}
	resolvedGuestInit, err := filepath.EvalSymlinks(guestInit)
	if err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(homebrewBin, "microagent")
	if err := os.Symlink(executable, link); err != nil {
		t.Fatal(err)
	}
	if got := defaultGuestInitPathFromExecutable(link, "arm64"); got != resolvedGuestInit {
		t.Fatalf("defaultGuestInitPathFromExecutable() = %q, want %q", got, resolvedGuestInit)
	}
}

func TestWorkspaceHasGuestCommand(t *testing.T) {
	if !workspaceHasGuestCommand(workspaceOptions{SetupCommands: []string{"echo setup"}}) {
		t.Fatal("setup command should count as guest work")
	}
	if !workspaceHasGuestCommand(workspaceOptions{ExecCommand: "echo run"}) {
		t.Fatal("exec command should count as guest work")
	}
	if workspaceHasGuestCommand(workspaceOptions{SetupCommands: []string{"  "}}) {
		t.Fatal("blank setup command should not count as guest work")
	}
}

func TestRunPSListsWorkspaces(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "workspaces", "research"), 0o755); err != nil {
		t.Fatal(err)
	}
	eventDir := filepath.Join(dir, "research")
	if err := os.MkdirAll(eventDir, 0o755); err != nil {
		t.Fatal(err)
	}
	event := vmkit.Event{
		Identity:   vmkit.Identity{RequestID: "req-1", RuntimeID: "research", Role: vmkit.RoleWorkload, Backend: vmkit.BackendAppleVF},
		State:      vmkit.StateStopped,
		ObservedAt: time.Date(2026, 5, 2, 7, 0, 0, 0, time.UTC),
	}
	data, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(eventDir, "event.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}
	stdoutPath := filepath.Join(dir, "ps.json")
	stdout, err := os.Create(stdoutPath)
	if err != nil {
		t.Fatal(err)
	}
	err = runPS([]string{"--state-dir", dir}, stdout)
	if closeErr := stdout.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	if err != nil {
		t.Fatalf("runPS: %v", err)
	}
	got, err := os.ReadFile(stdoutPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), `"name": "research"`) || !strings.Contains(string(got), `"state": "stopped"`) {
		t.Fatalf("ps output = %s", got)
	}
}

func TestRunLogsPrintsSerialLog(t *testing.T) {
	dir := t.TempDir()
	logDir := filepath.Join(dir, "research")
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(logDir, "serial.log"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	stdoutPath := filepath.Join(dir, "logs.txt")
	stdout, err := os.Create(stdoutPath)
	if err != nil {
		t.Fatal(err)
	}
	err = runLogs([]string{"research", "--state-dir", dir}, stdout)
	if closeErr := stdout.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	if err != nil {
		t.Fatalf("runLogs: %v", err)
	}
	got, err := os.ReadFile(stdoutPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "hello\n" {
		t.Fatalf("logs = %q", got)
	}
}

func TestRunWorkspaceRequiresExec(t *testing.T) {
	dir := t.TempDir()
	stdoutPath := filepath.Join(dir, "stdout.json")
	stdout, err := os.Create(stdoutPath)
	if err != nil {
		t.Fatal(err)
	}
	err = runWorkspace(t.Context(), []string{"--image", "docker.io/library/ubuntu:24.04"}, stdout)
	if closeErr := stdout.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	if err == nil || !strings.Contains(err.Error(), "run requires --exec") {
		t.Fatalf("err = %v, want --exec validation", err)
	}
}

func TestHighLevelCreateDetection(t *testing.T) {
	if !hasFlagValue([]string{"--image", "ubuntu:24.04"}, "image") {
		t.Fatal("expected --image to be detected")
	}
	if !hasFlagValue([]string{"--image=ubuntu:24.04"}, "image") {
		t.Fatal("expected --image=value to be detected")
	}
	if hasFlagValue([]string{"--kernel", "/tmp/kernel"}, "image") {
		t.Fatal("did not expect image flag")
	}
}

func TestKernelInstallFromLocalAndVerify(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "Image")
	if err := os.WriteFile(src, []byte("kernel"), 0o644); err != nil {
		t.Fatal(err)
	}
	sum := fmt.Sprintf("%x", sha256.Sum256([]byte("kernel")))
	out := filepath.Join(dir, "kernels", "Image")
	stdoutPath := filepath.Join(dir, "install.json")
	stdout, err := os.Create(stdoutPath)
	if err != nil {
		t.Fatal(err)
	}
	err = runKernelInstall(t.Context(), []string{"--from", src, "--sha256", sum, "--out", out}, stdout)
	if closeErr := stdout.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	if err != nil {
		t.Fatalf("runKernelInstall: %v", err)
	}
	if data, err := os.ReadFile(out); err != nil || string(data) != "kernel" {
		t.Fatalf("installed kernel = %q, %v", data, err)
	}
	verifyOut, err := os.Create(filepath.Join(dir, "verify.json"))
	if err != nil {
		t.Fatal(err)
	}
	err = runKernelVerify([]string{"--path", out, "--sha256", sum}, verifyOut)
	if closeErr := verifyOut.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	if err != nil {
		t.Fatalf("runKernelVerify: %v", err)
	}
}

func TestDefaultKernelManifestHasAppleVFArm64(t *testing.T) {
	kernel, ok := defaultKernel(vmkit.BackendAppleVF, "arm64")
	if !ok {
		t.Fatal("missing apple-vf arm64 kernel")
	}
	if kernel.URL == "" || kernel.SHA256 == "" {
		t.Fatalf("kernel = %#v", kernel)
	}
}

func TestDefaultKernelManifestHasFirecrackerAMD64(t *testing.T) {
	kernel, ok := defaultKernel(vmkit.BackendFirecracker, "amd64")
	if !ok {
		t.Fatal("missing firecracker amd64 kernel")
	}
	if kernel.URL != "https://github.com/geoffbelknap/microagent-kernels/releases/download/kernels-6.1.155-r1/microagent-kernel-6.1.155-firecracker-amd64" {
		t.Fatalf("url = %q", kernel.URL)
	}
	if kernel.SHA256 != "6f4196f67add6c49df08e780fd705eb9a55a3b4b8826a51948be9a7493f38d08" {
		t.Fatalf("sha256 = %q", kernel.SHA256)
	}
}

func TestDefaultKernelSupportReportsDownloadable(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "Image")
	originalDefaults := defaultKernels
	defaultKernels = []kernelManifestEntry{
		{
			Backend:      vmkit.BackendAppleVF,
			Architecture: "arm64",
			URL:          "https://example.com/kernel",
			SHA256:       "abc123",
		},
	}
	t.Cleanup(func() {
		defaultKernels = originalDefaults
	})

	support := defaultKernelSupportForPath(vmkit.BackendAppleVF, "arm64", path)
	if support.Status != "downloadable" {
		t.Fatalf("status = %q, want downloadable", support.Status)
	}
	if support.SHA256 != "abc123" {
		t.Fatalf("sha256 = %q, want abc123", support.SHA256)
	}
}

func TestEnsureWorkspaceKernelSkipsExplicitKernel(t *testing.T) {
	opts := workspaceOptions{
		Backend:        vmkit.BackendAppleVF,
		Architecture:   "arm64",
		KernelPath:     filepath.Join(t.TempDir(), "missing"),
		KernelExplicit: true,
	}
	if err := ensureWorkspaceKernel(t.Context(), &opts); err != nil {
		t.Fatalf("ensureWorkspaceKernel: %v", err)
	}
}

func TestEnsureWorkspaceKernelInstallsDefaultKernel(t *testing.T) {
	kernelBytes := []byte("test kernel")
	sum := sha256.Sum256(kernelBytes)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(kernelBytes)
	}))
	t.Cleanup(server.Close)

	originalDefaults := defaultKernels
	defaultKernels = []kernelManifestEntry{
		{
			Backend:      vmkit.BackendAppleVF,
			Architecture: "arm64",
			URL:          server.URL,
			SHA256:       fmt.Sprintf("%x", sum),
		},
	}
	t.Cleanup(func() {
		defaultKernels = originalDefaults
	})

	opts := workspaceOptions{
		Backend:      vmkit.BackendAppleVF,
		Architecture: "arm64",
		KernelPath:   filepath.Join(t.TempDir(), "Image"),
	}
	if err := ensureWorkspaceKernel(t.Context(), &opts); err != nil {
		t.Fatalf("ensureWorkspaceKernel: %v", err)
	}
	got, err := os.ReadFile(opts.KernelPath)
	if err != nil {
		t.Fatalf("read kernel: %v", err)
	}
	if string(got) != string(kernelBytes) {
		t.Fatalf("kernel bytes = %q, want %q", got, kernelBytes)
	}
}

func newFlagSet(name string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	return fs
}
