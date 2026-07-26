package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/geoffbelknap/microagent/pkg/vmkit"
	"github.com/geoffbelknap/microagent/pkg/workspace"
)

func TestSuperviseWorkspaceSkipsNeverPolicy(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "workspaces", "research"), 0o755); err != nil {
		t.Fatal(err)
	}
	rootfsPath := workspace.WorkspaceRootfsPath(dir, "research", hostBackend())
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
	result, err := superviseWorkspace(t.Context(), superviseOptions{
		StateDir:       dir,
		Name:           "research",
		Backend:        hostBackend(),
		Architecture:   defaultGuestArch(),
		KernelPath:     kernelPath,
		KernelExplicit: true,
		SupervisorPath: "/tmp/supervisor",
	})
	if err != nil {
		t.Fatalf("superviseWorkspace: %v", err)
	}
	if result.Policy != "never" || !result.Stopped || result.Restarts != 0 {
		t.Fatalf("result = %#v", result)
	}
}

func TestCloneWorkspaceCopiesStoppedWorkspace(t *testing.T) {
	dir := t.TempDir()
	sourceDir := filepath.Join(dir, "workspaces", "template")
	if err := os.MkdirAll(filepath.Join(sourceDir, "disks"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sourceDir, "rootfs.ext4"), []byte("rootfs"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sourceDir, "disks", "workspace.ext4"), []byte("disk"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := writeWorkspaceManifest(workspaceOptions{
		StateDir:  dir,
		Name:      "template",
		Profile:   "medium",
		MemoryMiB: 2048,
		CPUCount:  2,
		SizeMiB:   8192,
		Disks: []workspaceDisk{{
			Name:       "workspace",
			Path:       filepath.Join(sourceDir, "disks", "workspace.ext4"),
			Mountpoint: "/workspace",
			Mode:       "rw",
		}},
	}); err != nil {
		t.Fatal(err)
	}
	req := vmkit.Request{
		Identity: &vmkit.Identity{RequestID: "req-1", RuntimeID: "template", Role: vmkit.RoleWorkload, Backend: vmkit.BackendAppleVF},
		Config:   &vmkit.Config{StateDir: dir},
	}
	if err := writeWorkspaceProcessState(workspaceOptions{StateDir: dir, Name: "template"}, req, vmkit.StateStopped, 0, ""); err != nil {
		t.Fatal(err)
	}
	result, err := cloneWorkspace(dir, "template", "copy")
	if err != nil {
		t.Fatalf("cloneWorkspace: %v", err)
	}
	if result.Workspace != "copy" || result.Profile != "medium" || result.Resources.MemoryMiB != 2048 {
		t.Fatalf("clone result = %#v", result)
	}
	if data, err := os.ReadFile(filepath.Join(dir, "workspaces", "copy", "rootfs.ext4")); err != nil || string(data) != "rootfs" {
		t.Fatalf("cloned rootfs = %q err=%v", data, err)
	}
	manifest, err := readWorkspaceManifest(dir, "copy")
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Name != "copy" {
		t.Fatalf("manifest name = %q", manifest.Name)
	}
	wantDiskPath := filepath.Join(dir, "workspaces", "copy", "disks", "workspace.ext4")
	if len(manifest.Disks) != 1 || manifest.Disks[0].Path != wantDiskPath {
		t.Fatalf("manifest disks = %#v, want path %q", manifest.Disks, wantDiskPath)
	}
	event, err := readWorkspaceEvent(workspaceOptions{StateDir: dir, Name: "copy"})
	if err != nil {
		t.Fatal(err)
	}
	if event.State != vmkit.StatePrepared || !strings.Contains(event.Detail, "template") {
		t.Fatalf("event = %#v", event)
	}
}

func TestCloneWorkspaceRejectsActiveSource(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "workspaces", "active"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "workspaces", "active", "rootfs.ext4"), []byte("rootfs"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := writeWorkspaceManifest(workspaceOptions{StateDir: dir, Name: "active", Profile: "small", MemoryMiB: 512, CPUCount: 2, SizeMiB: 1024}); err != nil {
		t.Fatal(err)
	}
	req := vmkit.Request{
		Identity: &vmkit.Identity{RequestID: "req-1", RuntimeID: "active", Role: vmkit.RoleWorkload, Backend: vmkit.BackendAppleVF},
		Config:   &vmkit.Config{StateDir: dir},
	}
	if err := writeWorkspaceProcessState(workspaceOptions{StateDir: dir, Name: "active"}, req, vmkit.StateRunning, 123, ""); err != nil {
		t.Fatal(err)
	}
	_, err := cloneWorkspace(dir, "active", "copy")
	if err == nil || !strings.Contains(err.Error(), "must be stopped") {
		t.Fatalf("err = %v, want stopped validation", err)
	}
	if _, statErr := os.Stat(filepath.Join(dir, "workspaces", "copy")); !os.IsNotExist(statErr) {
		t.Fatalf("target was created despite failed clone: %v", statErr)
	}
}

func TestCloneWorkspaceRejectsEventOnlyActiveSource(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "workspaces", "active"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "workspaces", "active", "rootfs.ext4"), []byte("rootfs"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := writeWorkspaceManifest(workspaceOptions{StateDir: dir, Name: "active", Profile: "small", MemoryMiB: 512, CPUCount: 2, SizeMiB: 1024}); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "active"), 0o755); err != nil {
		t.Fatal(err)
	}
	event := workspaceEventFile{
		Identity:   vmkit.Identity{RequestID: "req-1", RuntimeID: "active", Role: vmkit.RoleWorkload, Backend: vmkit.BackendAppleVF},
		State:      vmkit.StateRunning,
		ObservedAt: time.Date(2026, 5, 2, 7, 0, 0, 0, time.UTC).Format(time.RFC3339),
	}
	if err := writeJSONFile(filepath.Join(dir, "active", "event.json"), event); err != nil {
		t.Fatal(err)
	}
	_, err := cloneWorkspace(dir, "active", "copy")
	if err == nil || !strings.Contains(err.Error(), "must be stopped") {
		t.Fatalf("err = %v, want stopped validation", err)
	}
}

func TestRunCloneCommand(t *testing.T) {
	outputFormat = ""
	t.Cleanup(func() { outputFormat = "" })
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "workspaces", "template"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "workspaces", "template", "rootfs.ext4"), []byte("rootfs"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := writeWorkspaceManifest(workspaceOptions{StateDir: dir, Name: "template", Profile: "small", MemoryMiB: 512, CPUCount: 2, SizeMiB: 1024}); err != nil {
		t.Fatal(err)
	}
	stdoutPath := filepath.Join(dir, "stdout.json")
	stdout, err := os.Create(stdoutPath)
	if err != nil {
		t.Fatal(err)
	}
	err = run(t.Context(), []string{"--json", "clone", "template", "copy", "--state-dir", dir}, stdout)
	if closeErr := stdout.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	if err != nil {
		t.Fatalf("run clone: %v", err)
	}
	data, err := os.ReadFile(stdoutPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"workspace": "copy"`) || !strings.Contains(string(data), `"state": "prepared"`) {
		t.Fatalf("clone output = %s", data)
	}
}

func TestCopyWorkspaceFileToRootfs(t *testing.T) {
	dir := t.TempDir()
	debugfs := fakeDebugFS(t, dir)
	if err := os.MkdirAll(filepath.Join(dir, "workspaces", "research"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "workspaces", "research", "rootfs.ext4"), []byte("rootfs"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := writeWorkspaceManifest(workspaceOptions{StateDir: dir, Name: "research", Profile: "small", MemoryMiB: 512, CPUCount: 2, SizeMiB: 1024}); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(dir, "hello.txt")
	if err := os.WriteFile(source, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	result, err := copyWorkspaceFile(dir, debugfs, source, "research:/workspace/hello.txt")
	if err != nil {
		t.Fatalf("copyWorkspaceFile: %v", err)
	}
	if result.Direction != "to-workspace" || result.Disk != "rootfs" || result.Bytes != 5 {
		t.Fatalf("result = %#v", result)
	}
	logData, err := os.ReadFile(filepath.Join(dir, "debugfs.log"))
	if err != nil {
		t.Fatal(err)
	}
	logText := string(logData)
	if !strings.Contains(logText, "-w|-R|write|"+source+"|/workspace/hello.txt") {
		t.Fatalf("debugfs log = %s", logText)
	}
}

func TestCopyWorkspaceFileFromAttachedDisk(t *testing.T) {
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
	}); err != nil {
		t.Fatal(err)
	}
	targetDir := filepath.Join(dir, "out")
	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		t.Fatal(err)
	}
	result, err := copyWorkspaceFile(dir, debugfs, "research:workspace:/notes.txt", targetDir)
	if err != nil {
		t.Fatalf("copyWorkspaceFile: %v", err)
	}
	if result.Direction != "from-workspace" || result.Disk != "workspace" {
		t.Fatalf("result = %#v", result)
	}
	targetPath := filepath.Join(targetDir, "notes.txt")
	data, err := os.ReadFile(targetPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "fake-dump" {
		t.Fatalf("dumped data = %q", data)
	}
}

func TestCopyWorkspaceFileRejectsActiveWorkspace(t *testing.T) {
	dir := t.TempDir()
	debugfs := fakeDebugFS(t, dir)
	if err := os.MkdirAll(filepath.Join(dir, "workspaces", "active"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "workspaces", "active", "rootfs.ext4"), []byte("rootfs"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := writeWorkspaceManifest(workspaceOptions{StateDir: dir, Name: "active", Profile: "small", MemoryMiB: 512, CPUCount: 2, SizeMiB: 1024}); err != nil {
		t.Fatal(err)
	}
	req := vmkit.Request{
		Identity: &vmkit.Identity{RequestID: "req-1", RuntimeID: "active", Role: vmkit.RoleWorkload, Backend: vmkit.BackendAppleVF},
		Config:   &vmkit.Config{StateDir: dir},
	}
	if err := writeWorkspaceProcessState(workspaceOptions{StateDir: dir, Name: "active"}, req, vmkit.StateRunning, 123, ""); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(dir, "hello.txt")
	if err := os.WriteFile(source, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := copyWorkspaceFile(dir, debugfs, source, "active:/hello.txt")
	if err == nil || !strings.Contains(err.Error(), "must be stopped") {
		t.Fatalf("err = %v, want stopped validation", err)
	}
}

func TestCopyWorkspaceFileRejectsTwoRemoteEndpoints(t *testing.T) {
	_, err := copyWorkspaceFile(t.TempDir(), "debugfs", "a:/x", "b:/y")
	if err == nil || !strings.Contains(err.Error(), "exactly one") {
		t.Fatalf("err = %v, want endpoint validation", err)
	}
}

func TestRunCPCommand(t *testing.T) {
	outputFormat = ""
	t.Cleanup(func() { outputFormat = "" })
	dir := t.TempDir()
	debugfs := fakeDebugFS(t, dir)
	if err := os.MkdirAll(filepath.Join(dir, "workspaces", "research"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "workspaces", "research", "rootfs.ext4"), []byte("rootfs"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := writeWorkspaceManifest(workspaceOptions{StateDir: dir, Name: "research", Profile: "small", MemoryMiB: 512, CPUCount: 2, SizeMiB: 1024}); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(dir, "hello.txt")
	if err := os.WriteFile(source, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	stdoutPath := filepath.Join(dir, "stdout.json")
	stdout, err := os.Create(stdoutPath)
	if err != nil {
		t.Fatal(err)
	}
	err = run(t.Context(), []string{"--json", "cp", "--debugfs", debugfs, "--state-dir", dir, source, "research:/hello.txt"}, stdout)
	if closeErr := stdout.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	if err != nil {
		t.Fatalf("run cp: %v", err)
	}
	data, err := os.ReadFile(stdoutPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"direction": "to-workspace"`) || !strings.Contains(string(data), `"workspace": "research"`) {
		t.Fatalf("cp output = %s", data)
	}
}

func fakeDebugFS(t *testing.T, dir string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		path := filepath.Join(dir, "debugfs.cmd")
		script := `@echo off
setlocal EnableDelayedExpansion
set "log=` + filepath.Join(dir, "debugfs.log") + `"
set "line=%*"
set "line=!line:"=!"
set "line=!line: =|!"
>>"%log%" echo !line!
:args
if "%~1"=="" goto done_args
if "%~1"=="-R" (
  set "cmd=%~2"
  set "target="
  for %%P in (!cmd!) do set "target=%%~P"
  if "!cmd:~0,5!"=="dump " (
    >"!target!" <nul set /p "=fake-dump"
  )
)
shift
goto args
:done_args
`
		if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
		return path
	}
	path := filepath.Join(dir, "debugfs")
	script := `#!/usr/bin/env bash
set -euo pipefail
log="` + filepath.Join(dir, "debugfs.log") + `"
# Strip the double quotes that the host adds around -R request arguments,
# mirroring how real debugfs tokenizes quoted request words.
printf '%s\n' "$*" | tr -d '"' | tr ' ' '|' >> "$log"
args=("$@")
for ((i=0; i<${#args[@]}; i++)); do
  if [[ "${args[$i]}" == "-R" ]]; then
    cmd="${args[$((i+1))]}"
    if [[ "$cmd" == dump\ * ]]; then
      target="${cmd##* }"
      target="${target%\"}"
      target="${target#\"}"
      printf fake-dump > "$target"
    fi
  fi
done
`
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}
