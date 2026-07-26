package main

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/geoffbelknap/microagent/pkg/vmkit"
	"github.com/geoffbelknap/microagent/pkg/workspace"
)

func TestStartUsesPersistedWorkspaceResources(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("start state writing with a fake executable supervisor is Apple VF-specific")
	}
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "workspaces", "research"), 0o755); err != nil {
		t.Fatal(err)
	}
	rootfsPath := workspace.WorkspaceRootfsPath(dir, "research", vmkit.BackendAppleVF)
	if err := os.WriteFile(rootfsPath, []byte("rootfs"), 0o644); err != nil {
		t.Fatal(err)
	}
	kernelPath := filepath.Join(dir, "Image")
	if err := os.WriteFile(kernelPath, []byte("kernel"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := writeWorkspaceManifest(workspaceOptions{
		StateDir:      dir,
		Name:          "research",
		Profile:       "medium",
		RestartPolicy: "always",
		MemoryMiB:     2048,
		CPUCount:      2,
		SizeMiB:       8192,
	}); err != nil {
		t.Fatal(err)
	}
	supervisor := filepath.Join(dir, "supervisor")
	script := `#!/usr/bin/env bash
set -euo pipefail
python3 -c 'import json,sys; req=json.load(sys.stdin); print(json.dumps({"ok": True, "backend": "apple-vf", "event": {"identity": req["identity"], "state": "running", "observedAt": "2026-05-02T00:00:00Z"}}))'
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
		"start",
		"research",
		"--state-dir", dir,
		"--backend", vmkit.BackendAppleVF,
		"--supervisor", supervisor,
		"--kernel", filepath.Join(dir, "Image"),
	}, stdout)
	if closeErr := stdout.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	if err != nil {
		t.Fatalf("run start: %v", err)
	}
	state, err := readWorkspaceRuntimeState(workspaceOptions{StateDir: dir, Name: "research"})
	if err != nil {
		t.Fatal(err)
	}
	if state.Config.MemoryMiB != 2048 || state.Config.CPUCount != 2 {
		t.Fatalf("runtime config = memory %d cpus %d", state.Config.MemoryMiB, state.Config.CPUCount)
	}
	manifest, err := readWorkspaceManifest(dir, "research")
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Restart != "always" {
		t.Fatalf("restart = %q", manifest.Restart)
	}
}

func TestStartRejectsQuarantinedWorkspace(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "workspaces", "research"), 0o755); err != nil {
		t.Fatal(err)
	}
	rootfsPath := workspace.WorkspaceRootfsPath(dir, "research", vmkit.BackendAppleVF)
	if err := os.WriteFile(rootfsPath, []byte("rootfs"), 0o644); err != nil {
		t.Fatal(err)
	}
	kernelPath := filepath.Join(dir, "Image")
	if err := os.WriteFile(kernelPath, []byte("kernel"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := writeWorkspaceManifest(workspaceOptions{
		StateDir:      dir,
		Name:          "research",
		Profile:       "small",
		RestartPolicy: "never",
		MemoryMiB:     512,
		CPUCount:      2,
		SizeMiB:       1024,
	}); err != nil {
		t.Fatal(err)
	}
	req := vmkit.Request{
		Command: "run",
		Identity: &vmkit.Identity{
			RequestID: "req-1",
			RuntimeID: "research",
			Role:      vmkit.RoleWorkload,
			Backend:   hostBackend(),
		},
		Config: &vmkit.Config{
			KernelPath: kernelPath,
			RootfsPath: filepath.Join(dir, "workspaces", "research", "rootfs.ext4"),
			StateDir:   dir,
			MemoryMiB:  512,
			CPUCount:   2,
		},
	}
	if err := writeWorkspaceProcessState(workspaceOptions{StateDir: dir, Name: "research"}, req, vmkit.StateQuarantined, 4242, ""); err != nil {
		t.Fatal(err)
	}
	stdout, err := os.Create(filepath.Join(dir, "stdout.json"))
	if err != nil {
		t.Fatal(err)
	}
	err = run(t.Context(), []string{
		"start",
		"research",
		"--state-dir", dir,
		"--backend", hostBackend(),
		"--kernel", kernelPath,
	}, stdout)
	if closeErr := stdout.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	if err == nil || !strings.Contains(err.Error(), "is quarantined with preserved pid 4242") {
		t.Fatalf("err = %v, want quarantined start rejection", err)
	}
}

func TestStartRejectsRunningWorkspace(t *testing.T) {
	dir := t.TempDir()
	if err := writeWorkspaceManifest(workspaceOptions{
		StateDir:      dir,
		Name:          "research",
		Profile:       "small",
		RestartPolicy: "never",
		MemoryMiB:     512,
		CPUCount:      2,
		SizeMiB:       1024,
	}); err != nil {
		t.Fatal(err)
	}
	req := vmkit.Request{
		Command: "run",
		Identity: &vmkit.Identity{
			RequestID: "req-1",
			RuntimeID: "research",
			Role:      vmkit.RoleWorkload,
			Backend:   vmkit.BackendAppleVF,
		},
		Config: &vmkit.Config{
			KernelPath: "/tmp/kernel",
			RootfsPath: "/tmp/rootfs.ext4",
			StateDir:   dir,
			MemoryMiB:  512,
			CPUCount:   2,
		},
	}
	if err := writeWorkspaceProcessState(workspaceOptions{StateDir: dir, Name: "research"}, req, vmkit.StateRunning, 123, ""); err != nil {
		t.Fatal(err)
	}
	if err := ensureWorkspaceCanStart(dir, "research"); err == nil || !strings.Contains(err.Error(), "already running") {
		t.Fatalf("err = %v, want running start rejection", err)
	}
}

func TestRunNetworkReportsManifestAndRuntimeNetwork(t *testing.T) {
	outputFormat = "text"
	t.Cleanup(func() { outputFormat = "" })
	dir := t.TempDir()
	if err := writeWorkspaceManifest(workspaceOptions{
		StateDir:      dir,
		Name:          "research",
		Profile:       "small",
		RestartPolicy: "never",
		MemoryMiB:     512,
		CPUCount:      2,
		SizeMiB:       1024,
		Network: vmkit.NetworkConfig{
			Mode: "user",
			PortForwards: []vmkit.PortForward{
				{Protocol: "tcp", Host: "127.0.0.1", HostPort: 8080, GuestPort: 80},
			},
			DNS: []string{"1.1.1.1"},
		},
	}); err != nil {
		t.Fatal(err)
	}
	req := vmkit.Request{
		Identity: &vmkit.Identity{RequestID: "req-1", RuntimeID: "research", Role: vmkit.RoleWorkload, Backend: vmkit.BackendAppleVF},
		Config: &vmkit.Config{
			KernelPath: "/tmp/kernel",
			RootfsPath: "/tmp/rootfs.ext4",
			StateDir:   dir,
			Network:    &vmkit.NetworkConfig{Mode: "user", IP: "192.168.64.2", Routes: []string{"0.0.0.0/0"}},
		},
	}
	if err := writeWorkspaceProcessState(workspaceOptions{StateDir: dir, Name: "research"}, req, vmkit.StateRunning, 123, ""); err != nil {
		t.Fatal(err)
	}
	stdoutPath := filepath.Join(dir, "network.json")
	stdout, err := os.Create(stdoutPath)
	if err != nil {
		t.Fatal(err)
	}
	err = runNetwork([]string{"research", "--state-dir", dir}, stdout)
	if closeErr := stdout.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	if err != nil {
		t.Fatalf("runNetwork: %v", err)
	}
	data, err := os.ReadFile(stdoutPath)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	if !strings.Contains(text, "Forward: tcp 127.0.0.1:8080 -> guest:80") || !strings.Contains(text, "IP: 192.168.64.2") {
		t.Fatalf("network output = %s", data)
	}
}

func TestApplyUpdatesStoppedWorkspaceNetwork(t *testing.T) {
	dir := t.TempDir()
	if err := writeWorkspaceManifest(workspaceOptions{
		StateDir:      dir,
		Name:          "homebridge",
		Profile:       "small",
		RestartPolicy: "always",
		MemoryMiB:     512,
		CPUCount:      2,
		SizeMiB:       1024,
		Network: vmkit.NetworkConfig{
			Mode:         "user",
			PortForwards: []vmkit.PortForward{{Protocol: "tcp", HostPort: 8581, GuestPort: 8581}},
		},
	}); err != nil {
		t.Fatal(err)
	}
	specPath := filepath.Join(dir, "homebridge.yaml")
	spec := []byte(`name: homebridge
network:
  mode: user
  forwards:
    - host: 0.0.0.0
      hostPort: 8581
      guestPort: 8581
      protocol: tcp
`)
	if err := os.WriteFile(specPath, spec, 0o644); err != nil {
		t.Fatal(err)
	}
	stdout, err := os.Create(filepath.Join(dir, "apply.out"))
	if err != nil {
		t.Fatal(err)
	}
	err = run(t.Context(), []string{"apply", "--file", specPath, "--state-dir", dir}, stdout)
	if closeErr := stdout.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	manifest, err := readWorkspaceManifest(dir, "homebridge")
	if err != nil {
		t.Fatal(err)
	}
	if got := manifest.Network.PortForwards[0].Host; got != "0.0.0.0" {
		t.Fatalf("forward host = %q, want 0.0.0.0", got)
	}
}

func TestApplyRejectsLiveNonHostNetworkChange(t *testing.T) {
	dir := t.TempDir()
	originalNetwork := vmkit.NetworkConfig{
		Mode:         "user",
		PortForwards: []vmkit.PortForward{{Protocol: "tcp", HostPort: 8581, GuestPort: 8581}},
	}
	if err := writeWorkspaceManifest(workspaceOptions{
		StateDir:      dir,
		Name:          "homebridge",
		Profile:       "small",
		RestartPolicy: "always",
		MemoryMiB:     512,
		CPUCount:      2,
		SizeMiB:       1024,
		Network:       originalNetwork,
	}); err != nil {
		t.Fatal(err)
	}
	req := vmkit.Request{
		Command: "run",
		Identity: &vmkit.Identity{
			RequestID: "req-1",
			RuntimeID: "homebridge",
			Role:      vmkit.RoleWorkload,
			Backend:   vmkit.BackendLinuxKVM,
		},
		Config: &vmkit.Config{
			KernelPath: "/tmp/kernel",
			RootfsPath: "/tmp/rootfs.ext4",
			StateDir:   dir,
			Network:    &originalNetwork,
		},
	}
	if err := writeWorkspaceProcessState(workspaceOptions{StateDir: dir, Name: "homebridge"}, req, vmkit.StateRunning, 123, ""); err != nil {
		t.Fatal(err)
	}
	specPath := filepath.Join(dir, "homebridge.yaml")
	spec := []byte(`name: homebridge
network:
  mode: user
  forwards:
    - host: 0.0.0.0
      hostPort: 8581
      guestPort: 8582
      protocol: tcp
`)
	if err := os.WriteFile(specPath, spec, 0o644); err != nil {
		t.Fatal(err)
	}
	stdout, err := os.Create(filepath.Join(dir, "apply.out"))
	if err != nil {
		t.Fatal(err)
	}
	err = run(t.Context(), []string{"apply", "--file", specPath, "--state-dir", dir, "--backend", vmkit.BackendLinuxKVM}, stdout)
	if closeErr := stdout.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	if err == nil || !strings.Contains(err.Error(), "host bind changes") {
		t.Fatalf("err = %v, want host-bind-only rejection", err)
	}
	manifest, err := readWorkspaceManifest(dir, "homebridge")
	if err != nil {
		t.Fatal(err)
	}
	if got := manifest.Network.PortForwards[0].GuestPort; got != 8581 {
		t.Fatalf("guest port = %d, want unchanged 8581", got)
	}
}

func TestStatusReportsRuntimeNetworkAssignment(t *testing.T) {
	outputFormat = "json"
	t.Cleanup(func() { outputFormat = "" })
	dir := t.TempDir()
	if err := writeWorkspaceManifest(workspaceOptions{
		StateDir:      dir,
		Name:          "research",
		Profile:       "small",
		RestartPolicy: "never",
		MemoryMiB:     512,
		CPUCount:      2,
		SizeMiB:       1024,
		Network:       vmkit.NetworkConfig{Mode: "user"},
	}); err != nil {
		t.Fatal(err)
	}
	req := vmkit.Request{
		Identity: &vmkit.Identity{RequestID: "req-1", RuntimeID: "research", Role: vmkit.RoleWorkload, Backend: hostBackend()},
		Config: &vmkit.Config{
			KernelPath: "/tmp/kernel",
			RootfsPath: "/tmp/rootfs.ext4",
			StateDir:   dir,
			Network: &vmkit.NetworkConfig{
				Mode:    "user",
				IP:      "10.43.12.2/29",
				Subnet:  "10.43.12.0/29",
				Gateway: "10.43.12.1",
				DNS:     []string{"1.1.1.1", "8.8.8.8"},
				Routes:  []string{"0.0.0.0/0 via 10.43.12.1"},
			},
		},
	}
	if err := writeWorkspaceProcessState(workspaceOptions{StateDir: dir, Name: "research"}, req, vmkit.StateRunning, 123, ""); err != nil {
		t.Fatal(err)
	}
	stdoutPath := filepath.Join(dir, "status.json")
	stdout, err := os.Create(stdoutPath)
	if err != nil {
		t.Fatal(err)
	}
	err = runWorkspaceStateCommand(context.Background(), "status", []string{"research", "--state-dir", dir}, stdout)
	if closeErr := stdout.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	data, err := os.ReadFile(stdoutPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"runtime"`) ||
		!strings.Contains(string(data), `"ip": "10.43.12.2/29"`) ||
		!strings.Contains(string(data), `"subnet": "10.43.12.0/29"`) {
		t.Fatalf("status output = %s", data)
	}
}
