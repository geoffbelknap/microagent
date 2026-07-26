package main

import (
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/geoffbelknap/microagent/pkg/vmkit"
)

func TestStatusReportsDeclaredArtifacts(t *testing.T) {
	outputFormat = "json"
	t.Cleanup(func() { outputFormat = "" })
	dir := t.TempDir()
	opts := workspaceOptions{
		StateDir:      dir,
		Name:          "research",
		Profile:       "small",
		RestartPolicy: "never",
		MemoryMiB:     512,
		CPUCount:      2,
		SizeMiB:       1024,
		Disks: []workspaceDisk{{
			Name:       "config",
			SourcePath: "/tmp/config.tar",
			Path:       filepath.Join(dir, "workspaces", "research", "config.ext4"),
			Mountpoint: "/config",
			Mode:       "ro",
			Bundle:     true,
		}},
		Outputs: []workspaceOutput{{Name: "report", Path: "/workspace/report.json"}},
	}
	if err := writeWorkspaceManifest(opts); err != nil {
		t.Fatal(err)
	}
	req := vmkit.Request{
		Identity: &vmkit.Identity{RequestID: "req-1", RuntimeID: "research", Role: vmkit.RoleWorkload, Backend: hostBackend()},
		Config: &vmkit.Config{
			KernelPath: "/tmp/kernel",
			RootfsPath: "/tmp/rootfs.ext4",
			StateDir:   dir,
		},
	}
	if err := writeWorkspaceProcessState(workspaceOptions{StateDir: dir, Name: "research"}, req, vmkit.StatePrepared, 0, ""); err != nil {
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
	if resp.Artifacts == nil || len(resp.Artifacts.Ingress) != 1 || len(resp.Artifacts.Egress) != 1 {
		t.Fatalf("artifacts = %#v", resp.Artifacts)
	}
	if resp.Artifacts.Ingress[0].Name != "config" || resp.Artifacts.Ingress[0].Kind != "bundle" || resp.Artifacts.Ingress[0].Mountpoint != "/config" {
		t.Fatalf("ingress = %#v", resp.Artifacts.Ingress[0])
	}
	if resp.Artifacts.Egress[0].Name != "report" || resp.Artifacts.Egress[0].Path != "/workspace/report.json" {
		t.Fatalf("egress = %#v", resp.Artifacts.Egress[0])
	}
}

func TestArtifactsCommandListsDeclaredArtifacts(t *testing.T) {
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
		Disks: []workspaceDisk{{
			Name:       "config",
			SourcePath: "/tmp/config.tar",
			Path:       filepath.Join(dir, "workspaces", "research", "config.ext4"),
			Mountpoint: "/config",
			Mode:       "ro",
			Bundle:     true,
		}},
		Outputs: []workspaceOutput{{Name: "report", Path: "/workspace/report.json"}},
	}); err != nil {
		t.Fatal(err)
	}
	stdoutPath := filepath.Join(dir, "artifacts.json")
	stdout, err := os.Create(stdoutPath)
	if err != nil {
		t.Fatal(err)
	}
	err = run(t.Context(), []string{"artifact", "research", "--state-dir", dir}, stdout)
	if closeErr := stdout.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	if err != nil {
		t.Fatalf("run artifacts: %v", err)
	}
	var result artifactsResult
	data, err := os.ReadFile(stdoutPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatal(err)
	}
	if result.Workspace != "research" || len(result.Artifacts.Ingress) != 1 || len(result.Artifacts.Egress) != 1 {
		t.Fatalf("artifacts = %#v", result)
	}
	if result.Artifacts.Egress[0].Name != "report" || result.Artifacts.Egress[0].Path != "/workspace/report.json" {
		t.Fatalf("egress = %#v", result.Artifacts.Egress[0])
	}
}

func TestArtifactGetCopiesDeclaredRootfsOutput(t *testing.T) {
	dir := t.TempDir()
	debugfs := fakeDebugFS(t, dir)
	workspaceDir := filepath.Join(dir, "workspaces", "research")
	if err := os.MkdirAll(workspaceDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspaceDir, "rootfs.ext4"), []byte("rootfs"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := writeWorkspaceManifest(workspaceOptions{
		StateDir:  dir,
		Name:      "research",
		Profile:   "small",
		MemoryMiB: 512,
		CPUCount:  2,
		SizeMiB:   1024,
		Outputs:   []workspaceOutput{{Name: "report", Path: "/workspace/report.json"}},
	}); err != nil {
		t.Fatal(err)
	}
	targetDir := filepath.Join(dir, "out")
	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		t.Fatal(err)
	}
	result, err := getWorkspaceArtifact(dir, debugfs, "research", "report", targetDir)
	if err != nil {
		t.Fatalf("getWorkspaceArtifact: %v", err)
	}
	if result.Artifact != "report" || result.Disk != "rootfs" || result.Direction != "from-workspace" {
		t.Fatalf("result = %#v", result)
	}
	if data, err := os.ReadFile(filepath.Join(targetDir, "report.json")); err != nil || string(data) != "fake-dump" {
		t.Fatalf("artifact data = %q err=%v", data, err)
	}
}

func TestArtifactGetMapsOutputUnderAttachedDiskMount(t *testing.T) {
	dir := t.TempDir()
	debugfs := fakeDebugFS(t, dir)
	workspaceDir := filepath.Join(dir, "workspaces", "research")
	diskPath := filepath.Join(workspaceDir, "disks", "workspace.ext4")
	if err := os.MkdirAll(filepath.Dir(diskPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspaceDir, "rootfs.ext4"), []byte("rootfs"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(diskPath, []byte("disk"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := writeWorkspaceManifest(workspaceOptions{
		StateDir:  dir,
		Name:      "research",
		Profile:   "small",
		MemoryMiB: 512,
		CPUCount:  2,
		SizeMiB:   1024,
		Disks:     []workspaceDisk{{Name: "workspace", Path: diskPath, Mountpoint: "/workspace", Mode: "rw"}},
		Outputs:   []workspaceOutput{{Name: "report", Path: "/workspace/report.json"}},
	}); err != nil {
		t.Fatal(err)
	}
	targetDir := filepath.Join(dir, "out")
	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		t.Fatal(err)
	}
	result, err := getWorkspaceArtifact(dir, debugfs, "research", "report", targetDir)
	if err != nil {
		t.Fatalf("getWorkspaceArtifact: %v", err)
	}
	if result.Disk != "workspace" || result.Source != "research:workspace:/report.json" {
		t.Fatalf("result = %#v", result)
	}
	logData, err := os.ReadFile(filepath.Join(dir, "debugfs.log"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(logData), "-R|dump|/report.json|") {
		t.Fatalf("debugfs log = %s", logData)
	}
}

func TestRunArtifactGetCommand(t *testing.T) {
	outputFormat = "json"
	t.Cleanup(func() { outputFormat = "" })
	dir := t.TempDir()
	debugfs := fakeDebugFS(t, dir)
	workspaceDir := filepath.Join(dir, "workspaces", "research")
	if err := os.MkdirAll(workspaceDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspaceDir, "rootfs.ext4"), []byte("rootfs"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := writeWorkspaceManifest(workspaceOptions{
		StateDir:  dir,
		Name:      "research",
		Profile:   "small",
		MemoryMiB: 512,
		CPUCount:  2,
		SizeMiB:   1024,
		Outputs:   []workspaceOutput{{Name: "report", Path: "/workspace/report.json"}},
	}); err != nil {
		t.Fatal(err)
	}
	targetDir := filepath.Join(dir, "out")
	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		t.Fatal(err)
	}
	stdoutPath := filepath.Join(dir, "stdout.json")
	stdout, err := os.Create(stdoutPath)
	if err != nil {
		t.Fatal(err)
	}
	err = run(t.Context(), []string{"artifact", "get", "research", "report", targetDir, "--state-dir", dir, "--debugfs", debugfs}, stdout)
	if closeErr := stdout.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	if err != nil {
		t.Fatalf("run artifact get: %v", err)
	}
	var result copyResult
	data, err := os.ReadFile(stdoutPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatal(err)
	}
	if result.Artifact != "report" || result.Workspace != "research" || result.Direction != "from-workspace" {
		t.Fatalf("result = %#v", result)
	}
}

func TestArtifactGetRejectsUndeclaredOutput(t *testing.T) {
	dir := t.TempDir()
	if err := writeWorkspaceManifest(workspaceOptions{
		StateDir:  dir,
		Name:      "research",
		Profile:   "small",
		MemoryMiB: 512,
		CPUCount:  2,
		SizeMiB:   1024,
	}); err != nil {
		t.Fatal(err)
	}
	_, err := getWorkspaceArtifact(dir, "debugfs", "research", "missing", filepath.Join(dir, "out"))
	if err == nil || !strings.Contains(err.Error(), "not declared") {
		t.Fatalf("err = %v, want undeclared artifact error", err)
	}
}

func TestStatusReportsMediationReadiness(t *testing.T) {
	outputFormat = "json"
	t.Cleanup(func() { outputFormat = "" })
	dir := t.TempDir()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	mediation := vmkit.MediationConfig{
		Enabled:    true,
		Required:   true,
		Port:       2048,
		Target:     listener.Addr().String(),
		FailClosed: true,
	}
	opts := workspaceOptions{
		StateDir:      dir,
		Name:          "research",
		Profile:       "small",
		RestartPolicy: "never",
		MemoryMiB:     512,
		CPUCount:      2,
		SizeMiB:       1024,
		Mediation:     &mediation,
	}
	if err := writeWorkspaceManifest(opts); err != nil {
		t.Fatal(err)
	}
	req := vmkit.Request{
		Identity: &vmkit.Identity{RequestID: "req-1", RuntimeID: "research", Role: vmkit.RoleWorkload, Backend: hostBackend()},
		Config: &vmkit.Config{
			KernelPath: "/tmp/kernel",
			RootfsPath: "/tmp/rootfs.ext4",
			StateDir:   dir,
			Mediation:  &mediation,
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
	if resp.Mediation == nil || !resp.Mediation.Required || !resp.Mediation.FailClosed {
		t.Fatalf("mediation = %#v", resp.Mediation)
	}
	if resp.Readiness == nil || !resp.Readiness.MediationReady.Ready {
		t.Fatalf("readiness = %#v", resp.Readiness)
	}
	if !strings.Contains(resp.Readiness.MediationReady.Detail, "port=2048") {
		t.Fatalf("mediation detail = %q", resp.Readiness.MediationReady.Detail)
	}
	if !strings.Contains(resp.Readiness.MediationReady.Detail, "reachable") {
		t.Fatalf("mediation detail = %q, want live reachability detail", resp.Readiness.MediationReady.Detail)
	}
}
