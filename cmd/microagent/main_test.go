package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/geoffbelknap/microagent-kit/pkg/rootfs"
	firecrackersupervisor "github.com/geoffbelknap/microagent-kit/pkg/supervisors/firecracker"
	"github.com/geoffbelknap/microagent-kit/pkg/vmkit"
)

func TestMain(m *testing.M) {
	if os.Getenv("GO_WANT_FIRECRACKER_SUPERVISOR_HELPER") == "1" {
		var req vmkit.Request
		if err := json.NewDecoder(os.Stdin).Decode(&req); err != nil {
			_ = json.NewEncoder(os.Stdout).Encode(vmkit.Response{OK: false, Backend: vmkit.BackendFirecracker, Error: err.Error()})
			os.Exit(1)
		}
		resp, err := firecrackersupervisor.Supervisor{}.Do(context.Background(), req)
		_ = json.NewEncoder(os.Stdout).Encode(resp)
		if err != nil {
			os.Exit(1)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

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

func TestCreateHelpUsesWorkspaceHelp(t *testing.T) {
	dir := t.TempDir()
	stdoutPath := filepath.Join(dir, "stdout.txt")
	stdout, err := os.Create(stdoutPath)
	if err != nil {
		t.Fatal(err)
	}
	err = run(t.Context(), []string{"create", "--help"}, stdout)
	if closeErr := stdout.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	if err != nil {
		t.Fatalf("run create --help: %v", err)
	}
	data, err := os.ReadFile(stdoutPath)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	if !strings.Contains(text, "microagent create") || !strings.Contains(text, "-entrypoint <command>") {
		t.Fatalf("create help = %s", text)
	}
	if strings.Contains(text, "Rootfs image path") {
		t.Fatalf("create help exposed low-level supervisor flags: %s", text)
	}
}

func TestGlobalJSONOutputSwitch(t *testing.T) {
	outputFormat = ""
	t.Cleanup(func() { outputFormat = "" })
	args := parseGlobalFlags([]string{"--json", "doctor"})
	if outputFormat != "json" {
		t.Fatalf("outputFormat = %q, want json", outputFormat)
	}
	if len(args) != 1 || args[0] != "doctor" {
		t.Fatalf("args = %#v", args)
	}
}

func TestFirecrackerDoctorDoesNotRequireAppleVFSupervisor(t *testing.T) {
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

func TestDefaultPackagedKernelPathResolvesHomebrewSymlink(t *testing.T) {
	dir := t.TempDir()
	cellarVersion := "test-version"
	cellarBin := filepath.Join(dir, "Cellar", "microagent-kit", cellarVersion, "bin")
	cellarLibexec := filepath.Join(dir, "Cellar", "microagent-kit", cellarVersion, "libexec")
	homebrewBin := filepath.Join(dir, "bin")
	if err := os.MkdirAll(cellarBin, 0o755); err != nil {
		t.Fatal(err)
	}
	kernelDir := filepath.Join(cellarLibexec, "kernels", "apple-vf", "arm64")
	if err := os.MkdirAll(kernelDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(homebrewBin, 0o755); err != nil {
		t.Fatal(err)
	}
	executable := filepath.Join(cellarBin, "microagent")
	if err := os.WriteFile(executable, []byte("binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	kernel := filepath.Join(kernelDir, "Image")
	if err := os.WriteFile(kernel, []byte("kernel"), 0o644); err != nil {
		t.Fatal(err)
	}
	resolvedKernel, err := filepath.EvalSymlinks(kernel)
	if err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(homebrewBin, "microagent")
	if err := os.Symlink(executable, link); err != nil {
		t.Fatal(err)
	}
	if got := defaultPackagedKernelPathFromExecutable(link, "apple-vf", "arm64"); got != resolvedKernel {
		t.Fatalf("defaultPackagedKernelPathFromExecutable() = %q, want %q", got, resolvedKernel)
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

func TestRequestForCommandParsesDisk(t *testing.T) {
	req, err := requestForCommand("create", newFlagSet("create"), reorderFlagArgs([]string{
		"--id", "agent-1",
		"--kernel", "/tmp/kernel",
		"--rootfs", "/tmp/rootfs.ext4",
		"--state-dir", "/tmp/state",
		"--disk", "constraints=/tmp/constraints.ext4:/config:ro",
	}))
	if err != nil {
		t.Fatalf("requestForCommand: %v", err)
	}
	if len(req.Config.Disks) != 1 {
		t.Fatalf("Disks len = %d, want 1", len(req.Config.Disks))
	}
	disk := req.Config.Disks[0]
	if disk.Name != "constraints" || disk.Path != "/tmp/constraints.ext4" || disk.Mountpoint != "/config" || disk.Mode != "ro" {
		t.Fatalf("disk = %#v", disk)
	}
}

func TestWorkspaceRequestIncludesVsockMappings(t *testing.T) {
	req := workspaceRequest(workspaceOptions{
		Name:           "agent-1",
		Backend:        "apple-vf",
		KernelPath:     "/tmp/kernel",
		MemoryMiB:      512,
		CPUCount:       2,
		ResultPort:     1024,
		VsockListeners: []vmkit.VsockListener{{Port: 3128, Target: "127.0.0.1:19000"}},
	}, "run", "/tmp/rootfs.ext4")
	if len(req.Config.VsockListeners) != 2 {
		t.Fatalf("VsockListeners len = %d, want 2", len(req.Config.VsockListeners))
	}
	if req.Config.VsockListeners[1].Port != 3128 || req.Config.VsockListeners[1].Target != "127.0.0.1:19000" {
		t.Fatalf("enforcer listener = %#v", req.Config.VsockListeners[1])
	}
}

func TestWorkspaceRequestIncludesDisks(t *testing.T) {
	req := workspaceRequest(workspaceOptions{
		Name:       "agent-1",
		Backend:    "apple-vf",
		KernelPath: "/tmp/kernel",
		MemoryMiB:  512,
		CPUCount:   2,
		Disks: []workspaceDisk{{
			Name:       "workspace",
			Path:       "/tmp/workspace.ext4",
			Mountpoint: "/workspace",
			Mode:       "rw",
		}},
	}, "run", "/tmp/rootfs.ext4")
	if len(req.Config.Disks) != 1 {
		t.Fatalf("Disks len = %d, want 1", len(req.Config.Disks))
	}
	if req.Config.Disks[0].Mountpoint != "/workspace" || req.Config.Disks[0].Mode != "rw" {
		t.Fatalf("disk = %#v", req.Config.Disks[0])
	}
}

func TestRunUsesSupervisorOverride(t *testing.T) {
	dir := t.TempDir()
	supervisor := filepath.Join(dir, "supervisor")
	script := `#!/usr/bin/env bash
set -euo pipefail
python3 -c 'import json,sys; req=json.load(sys.stdin); print(json.dumps({"ok": True, "backend": "apple-vf", "event": {"identity": req["identity"], "state": "prepared", "observedAt": "2026-05-02T00:00:00Z"}}))'
`
	if err := os.WriteFile(supervisor, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	stdoutPath := filepath.Join(dir, "stdout.json")
	stdout, err := os.Create(stdoutPath)
	if err != nil {
		t.Fatal(err)
	}
	err = run(t.Context(), []string{
		"create",
		"--supervisor", supervisor,
		"--backend", vmkit.BackendAppleVF,
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
	if err := writeWorkspaceProcessState(workspaceOptions{StateDir: dir, Name: "research", Backend: vmkit.BackendAppleVF}, req, vmkit.StateRunning, 123, ""); err != nil {
		t.Fatalf("writeWorkspaceProcessState: %v", err)
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
	if !strings.Contains(string(data), `"state": "running"`) {
		t.Fatalf("status output = %s", data)
	}
}

func TestRunDeleteRemovesSavedWorkspaceState(t *testing.T) {
	dir := t.TempDir()
	supervisor := filepath.Join(dir, "supervisor")
	script := `#!/usr/bin/env bash
set -euo pipefail
python3 -c 'import json,sys; req=json.load(sys.stdin); assert req["command"] == "delete"; print(json.dumps({"ok": True, "backend": "apple-vf", "event": {"identity": req["identity"], "state": "stopped", "detail": "deleted", "observedAt": "2026-05-02T00:00:00Z"}}))'
`
	if err := os.WriteFile(supervisor, []byte(script), 0o755); err != nil {
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
		"--supervisor", supervisor,
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
		"--entrypoint", "/app/entrypoint.sh",
		"--env", "AGENCY_AGENT_NAME=research",
		"--env", "AGENCY_MODEL=standard",
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
	if opts.Entrypoint != "/app/entrypoint.sh" {
		t.Fatalf("Entrypoint = %q", opts.Entrypoint)
	}
	if opts.Env["AGENCY_AGENT_NAME"] != "research" || opts.Env["AGENCY_MODEL"] != "standard" {
		t.Fatalf("Env = %#v", opts.Env)
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

func TestParseWorkspaceOptionsForCreateDefaultsImageAndPositionalName(t *testing.T) {
	opts, err := parseWorkspaceOptions("create", []string{
		"research",
		"--arch", "amd64",
	})
	if err != nil {
		t.Fatalf("parseWorkspaceOptions: %v", err)
	}
	if opts.Name != "research" {
		t.Fatalf("Name = %q", opts.Name)
	}
	if opts.ImageRef != defaultWorkspaceImageAMD64 {
		t.Fatalf("ImageRef = %q, want %q", opts.ImageRef, defaultWorkspaceImageAMD64)
	}
	if opts.MemoryMiB != defaultWorkspaceMemoryMiB || opts.CPUCount != 2 || opts.SizeMiB != rootfs.DefaultSizeMiB {
		t.Fatalf("defaults = memory %d cpus %d size %d", opts.MemoryMiB, opts.CPUCount, opts.SizeMiB)
	}
}

func TestParseWorkspaceOptionsAcceptsDiskAndBundle(t *testing.T) {
	opts, err := parseWorkspaceOptions("create", []string{
		"research",
		"--disk", "workspace=/tmp/workspace.ext4:/workspace:rw",
		"--bundle", "constraints=/tmp/constraints.tar:/config:ro",
	})
	if err != nil {
		t.Fatalf("parseWorkspaceOptions: %v", err)
	}
	if len(opts.Disks) != 2 {
		t.Fatalf("Disks len = %d, want 2", len(opts.Disks))
	}
	if opts.Disks[0].Name != "workspace" || opts.Disks[0].Bundle {
		t.Fatalf("disk = %#v", opts.Disks[0])
	}
	if opts.Disks[1].Name != "constraints" || !opts.Disks[1].Bundle || opts.Disks[1].Mode != "ro" {
		t.Fatalf("bundle = %#v", opts.Disks[1])
	}
}

func TestCreateDispatchKeepsLowLevelSupervisorCreate(t *testing.T) {
	if !shouldUseHighLevelCreate([]string{"research"}) {
		t.Fatal("positional create should use high-level workspace create")
	}
	if !shouldUseHighLevelCreate([]string{"--name", "research"}) {
		t.Fatal("--name create should use high-level workspace create")
	}
	if shouldUseHighLevelCreate([]string{"--id", "agent", "--rootfs", "/tmp/rootfs.ext4", "--kernel", "/tmp/Image"}) {
		t.Fatal("low-level rootfs create should stay on supervisor create path")
	}
}

func TestWorkspaceSupervisorSelectsSymmetricBackends(t *testing.T) {
	firecracker, err := workspaceSupervisor(workspaceOptions{Backend: vmkit.BackendFirecracker})
	if err != nil {
		t.Fatalf("firecracker supervisor: %v", err)
	}
	executable, ok := firecracker.(vmkit.ExecutableSupervisor)
	if !ok {
		t.Fatalf("firecracker supervisor = %T, want vmkit.ExecutableSupervisor", firecracker)
	}
	if executable.Path != "microagent-firecracker-supervisor" {
		t.Fatalf("firecracker supervisor path = %q", executable.Path)
	}

	appleVF, err := workspaceSupervisor(workspaceOptions{Backend: vmkit.BackendAppleVF, SupervisorPath: "/tmp/applevf"})
	if err != nil {
		t.Fatalf("apple vf supervisor: %v", err)
	}
	executable, ok = appleVF.(vmkit.ExecutableSupervisor)
	if !ok {
		t.Fatalf("apple vf supervisor = %T, want vmkit.ExecutableSupervisor", appleVF)
	}
	if executable.Path != "/tmp/applevf" {
		t.Fatalf("apple vf supervisor path = %q", executable.Path)
	}
}

func firecrackerSupervisorHelper(t *testing.T) string {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "microagent-firecracker-supervisor")
	script := fmt.Sprintf("#!/usr/bin/env bash\nGO_WANT_FIRECRACKER_SUPERVISOR_HELPER=1 %q\n", executable)
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func processStillActive(pid int) bool {
	if pid <= 0 {
		return false
	}
	if err := syscall.Kill(pid, 0); err != nil {
		return err != syscall.ESRCH
	}
	if data, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "stat")); err == nil {
		fields := strings.Fields(string(data))
		if len(fields) >= 3 && fields[2] == "Z" {
			return false
		}
	}
	return true
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
		Entrypoint:      "/app/entrypoint.sh",
		SetupCommands:   []string{"echo setup"},
		Env:             map[string]string{"AGENCY_AGENT_NAME": "research"},
		Disks:           []workspaceDisk{{Name: "constraints", Path: "/tmp/constraints.ext4", Mountpoint: "/config", Mode: "ro"}},
		ResultPort:      1024,
		PrepareForStart: true,
	})
	if !strings.Contains(command, "echo setup") {
		t.Fatalf("workspaceCommand missing setup: %q", command)
	}
	if !strings.Contains(command, `> /etc/microagent/run.json`) ||
		!strings.Contains(command, `"command":["/bin/sh","-lc","/app/entrypoint.sh"]`) ||
		!strings.Contains(command, `"mountpoint":"/config"`) ||
		!strings.Contains(command, `"AGENCY_AGENT_NAME=research"`) {
		t.Fatalf("workspaceCommand missing guest config reset: %q", command)
	}
}

func TestWorkspaceBuildCommandUsesStartConfigWhenNoSetupIsNeeded(t *testing.T) {
	command, port := workspaceBuildCommandAndPort(workspaceOptions{
		Entrypoint:      "/app/entrypoint.sh",
		ResultPort:      1024,
		PrepareForStart: true,
	})
	if port != 0 {
		t.Fatalf("port = %d, want 0", port)
	}
	if strings.Join(command, " ") != "/bin/sh -lc /app/entrypoint.sh" {
		t.Fatalf("command = %#v", command)
	}
}

func TestWorkspaceBuildCommandKeepsSetupResultPort(t *testing.T) {
	command, port := workspaceBuildCommandAndPort(workspaceOptions{
		Entrypoint:      "/app/entrypoint.sh",
		SetupCommands:   []string{"echo setup"},
		ResultPort:      1024,
		PrepareForStart: true,
	})
	if port != 1024 {
		t.Fatalf("port = %d, want 1024", port)
	}
	joined := strings.Join(command, " ")
	if !strings.Contains(joined, "echo setup") || !strings.Contains(joined, "/etc/microagent/run.json") {
		t.Fatalf("command = %#v", command)
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

func TestRunPSCanPrintHumanOutput(t *testing.T) {
	t.Setenv("MICROAGENT_OUTPUT", "text")
	dir := t.TempDir()
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
	stdoutPath := filepath.Join(dir, "ps.txt")
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
	if !strings.Contains(string(got), "NAME") || !strings.Contains(string(got), "research") || strings.Contains(string(got), `"workspaces"`) {
		t.Fatalf("ps human output = %s", got)
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
	if !shouldUseHighLevelCreate([]string{"test"}) {
		t.Fatal("expected positional create name to use high-level create")
	}
	if shouldUseHighLevelCreate([]string{"--rootfs", "/tmp/rootfs.ext4", "--id", "agent-1"}) {
		t.Fatal("legacy rootfs create should not use high-level create")
	}
}

func TestParseWorkspaceOptionsAcceptsPositionalName(t *testing.T) {
	opts, err := parseWorkspaceOptions("create", []string{"test", "--image", "docker.io/library/ubuntu:24.04"})
	if err != nil {
		t.Fatalf("parseWorkspaceOptions: %v", err)
	}
	if opts.Name != "test" {
		t.Fatalf("Name = %q, want test", opts.Name)
	}
}

func TestFirecrackerStopTerminatesRecordedPID(t *testing.T) {
	if runtime.GOOS == "darwin" {
		t.Skip("darwin uses Apple VF")
	}
	dir := t.TempDir()
	req := testFirecrackerRuntimeState(t, dir, "agent-1", vmkit.StateRunning, 0)
	cmd := exec.Command("sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	})
	if err := writeWorkspaceProcessState(
		workspaceOptions{StateDir: dir, Name: "agent-1"},
		req,
		vmkit.StateRunning,
		cmd.Process.Pid,
		"",
	); err != nil {
		t.Fatal(err)
	}
	stdout, err := os.Create(filepath.Join(dir, "stdout.json"))
	if err != nil {
		t.Fatal(err)
	}
	err = run(t.Context(), []string{"stop", "agent-1", "--state-dir", dir, "--supervisor", firecrackerSupervisorHelper(t)}, stdout)
	if closeErr := stdout.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	if err != nil {
		t.Fatalf("run stop: %v", err)
	}
	state, err := readWorkspaceRuntimeState(workspaceOptions{StateDir: dir, Name: "agent-1"})
	if err != nil {
		t.Fatal(err)
	}
	if state.Event.State != vmkit.StateStopped || state.PID != 0 {
		t.Fatalf("state = %#v, want stopped with no pid", state)
	}
	if processStillActive(cmd.Process.Pid) {
		t.Fatalf("process %d still active", cmd.Process.Pid)
	}
}

func TestFirecrackerKillTerminatesRecordedPID(t *testing.T) {
	if runtime.GOOS == "darwin" {
		t.Skip("darwin uses Apple VF")
	}
	dir := t.TempDir()
	req := testFirecrackerRuntimeState(t, dir, "agent-1", vmkit.StateRunning, 0)
	cmd := exec.Command("sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	})
	if err := writeWorkspaceProcessState(
		workspaceOptions{StateDir: dir, Name: "agent-1"},
		req,
		vmkit.StateRunning,
		cmd.Process.Pid,
		"",
	); err != nil {
		t.Fatal(err)
	}
	stdout, err := os.Create(filepath.Join(dir, "stdout.json"))
	if err != nil {
		t.Fatal(err)
	}
	err = run(t.Context(), []string{"kill", "agent-1", "--state-dir", dir, "--supervisor", firecrackerSupervisorHelper(t)}, stdout)
	if closeErr := stdout.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	if err != nil {
		t.Fatalf("run kill: %v", err)
	}
	state, err := readWorkspaceRuntimeState(workspaceOptions{StateDir: dir, Name: "agent-1"})
	if err != nil {
		t.Fatal(err)
	}
	if state.Event.State != vmkit.StateStopped || state.PID != 0 {
		t.Fatalf("state = %#v, want stopped with no pid", state)
	}
}

func TestFirecrackerDeleteRefusesRunningPID(t *testing.T) {
	if runtime.GOOS == "darwin" {
		t.Skip("darwin uses Apple VF")
	}
	dir := t.TempDir()
	req := testFirecrackerRuntimeState(t, dir, "agent-1", vmkit.StateRunning, 0)
	cmd := exec.Command("sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	})
	if err := writeWorkspaceProcessState(
		workspaceOptions{StateDir: dir, Name: "agent-1"},
		req,
		vmkit.StateRunning,
		cmd.Process.Pid,
		"",
	); err != nil {
		t.Fatal(err)
	}
	stdout, err := os.Create(filepath.Join(dir, "stdout.json"))
	if err != nil {
		t.Fatal(err)
	}
	err = run(t.Context(), []string{"delete", "agent-1", "--state-dir", dir, "--supervisor", firecrackerSupervisorHelper(t)}, stdout)
	if closeErr := stdout.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	if err == nil || !strings.Contains(err.Error(), "is running; stop or kill it before delete") {
		t.Fatalf("err = %v, want running delete refusal", err)
	}
	if _, statErr := os.Stat(filepath.Join(dir, "agent-1", "runtime.json")); statErr != nil {
		t.Fatalf("runtime state should remain after refused delete: %v", statErr)
	}
}

func TestFirecrackerLegacyCreatePreparesStateLocally(t *testing.T) {
	if runtime.GOOS == "darwin" {
		t.Skip("darwin uses Apple VF")
	}
	dir := t.TempDir()
	kernel := filepath.Join(dir, "Image")
	rootfs := filepath.Join(dir, "rootfs.ext4")
	if err := os.WriteFile(kernel, []byte("kernel"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(rootfs, []byte("rootfs"), 0o644); err != nil {
		t.Fatal(err)
	}
	stdout, err := os.Create(filepath.Join(dir, "stdout.json"))
	if err != nil {
		t.Fatal(err)
	}
	err = run(t.Context(), []string{
		"create",
		"--rootfs", rootfs,
		"--kernel", kernel,
		"--id", "agent-1",
		"--state-dir", dir,
		"--supervisor", firecrackerSupervisorHelper(t),
	}, stdout)
	if closeErr := stdout.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "agent-1", "runtime.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"state": "prepared"`) || !strings.Contains(string(data), `"backend": "firecracker"`) {
		t.Fatalf("runtime state = %s", data)
	}
	if _, err := os.Stat(filepath.Join(dir, "agent-1", "firecracker.json")); err != nil {
		t.Fatalf("firecracker config missing: %v", err)
	}
}

func TestFirecrackerStatusReadsPreparedState(t *testing.T) {
	if runtime.GOOS == "darwin" {
		t.Skip("darwin uses Apple VF")
	}
	dir := t.TempDir()
	req := vmkit.Request{
		Command: "prepare",
		Identity: &vmkit.Identity{
			RequestID: "req-1",
			RuntimeID: "research",
			Role:      vmkit.RoleWorkload,
			Backend:   vmkit.BackendFirecracker,
		},
		Config: &vmkit.Config{
			KernelPath: filepath.Join(dir, "Image"),
			RootfsPath: filepath.Join(dir, "rootfs.ext4"),
			StateDir:   dir,
			MemoryMiB:  512,
			CPUCount:   2,
		},
	}
	if _, err := (firecrackersupervisor.Supervisor{Options: firecrackersupervisor.Options{Name: "research", StateDir: dir}}).Do(t.Context(), req); err != nil {
		t.Fatalf("firecracker prepare: %v", err)
	}
	stdout, err := os.Create(filepath.Join(dir, "status.json"))
	if err != nil {
		t.Fatal(err)
	}
	err = run(t.Context(), []string{"status", "research", "--state-dir", dir}, stdout)
	if closeErr := stdout.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	if err != nil {
		t.Fatalf("run status: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "status.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"state": "prepared"`) || !strings.Contains(string(data), `"backend": "firecracker"`) {
		t.Fatalf("status output = %s", data)
	}
}

func testFirecrackerRuntimeState(t *testing.T, dir, name string, state vmkit.VMState, pid int) vmkit.Request {
	t.Helper()
	req := vmkit.Request{
		Command: "run",
		Identity: &vmkit.Identity{
			RequestID: "req-1",
			RuntimeID: name,
			Role:      vmkit.RoleWorkload,
			Backend:   vmkit.BackendFirecracker,
		},
		Config: &vmkit.Config{
			KernelPath: filepath.Join(dir, "Image"),
			RootfsPath: filepath.Join(dir, "rootfs.ext4"),
			StateDir:   dir,
			MemoryMiB:  512,
			CPUCount:   2,
		},
	}
	if err := writeWorkspaceProcessState(workspaceOptions{StateDir: dir, Name: name}, req, state, pid, ""); err != nil {
		t.Fatal(err)
	}
	return req
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
	if kernel.URL != "https://github.com/geoffbelknap/microagent-kernels/releases/download/kernels-6.1.155-r2/microagent-kernel-6.1.155-firecracker-amd64" {
		t.Fatalf("url = %q", kernel.URL)
	}
	if kernel.SHA256 != "4bbe8b2fd19f78fea4bf02d52a67482227a896c90a63f272b6a084fa46a416c0" {
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

func TestFirecrackerGuestHaltedDetectsKernelShutdown(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("firecracker supervisor is linux-only")
	}
	dir := t.TempDir()
	serialPath := filepath.Join(dir, "serial.log")
	if firecrackersupervisor.GuestHalted(serialPath) {
		t.Fatal("missing serial log reported halted")
	}
	if err := os.WriteFile(serialPath, []byte("[ 1.0 ] reboot: System halted\n"), 0o644); err != nil {
		t.Fatalf("write serial log: %v", err)
	}
	if !firecrackersupervisor.GuestHalted(serialPath) {
		t.Fatal("system halt was not detected")
	}
	if err := os.WriteFile(serialPath, []byte("[ 1.0 ] reboot: Power down\n"), 0o644); err != nil {
		t.Fatalf("write serial log: %v", err)
	}
	if !firecrackersupervisor.GuestHalted(serialPath) {
		t.Fatal("power down was not detected")
	}
}

func newFlagSet(name string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	return fs
}
