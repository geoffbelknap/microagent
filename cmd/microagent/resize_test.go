package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/geoffbelknap/microagent/pkg/vmkit"
	"github.com/geoffbelknap/microagent/pkg/volume"
	"github.com/geoffbelknap/microagent/pkg/workspace"
)

func TestRunResizeUsage(t *testing.T) {
	if err := runResize(t.Context(), nil, os.Stdout); err == nil || !strings.Contains(err.Error(), "usage: microagent resize") {
		t.Fatalf("resize with no args error = %v", err)
	}
	if err := runResize(t.Context(), []string{"demo", "extra", "--size-mib", "8"}, os.Stdout); err == nil || !strings.Contains(err.Error(), "usage: microagent resize") {
		t.Fatalf("resize with extra arg error = %v", err)
	}
}

func TestRunResizeRealFlow(t *testing.T) {
	mke2fsPath, err := exec.LookPath("mke2fs")
	if err != nil {
		t.Skip("mke2fs not available")
	}
	if _, err := exec.LookPath("resize2fs"); err != nil {
		t.Skip("resize2fs not available")
	}
	stateDir := t.TempDir()
	const name = "resize-cli"
	backend := hostBackend()

	if err := workspace.WriteManifest(workspace.Options{Name: name, StateDir: stateDir, Backend: backend, MemoryMiB: 512, CPUCount: 1, SizeMiB: 8}); err != nil {
		t.Fatalf("WriteManifest: %v", err)
	}
	event := workspace.EventFile{
		Identity:   vmkit.Identity{RuntimeID: name, Backend: backend},
		State:      vmkit.StateHalted,
		ObservedAt: time.Now().UTC().Format(time.RFC3339),
	}
	eventPath := filepath.Join(stateDir, name, "event.json")
	if err := os.MkdirAll(filepath.Dir(eventPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := writeJSONFile(eventPath, event); err != nil {
		t.Fatalf("write event: %v", err)
	}

	rootfsPath := workspace.WorkspaceRootfsPath(stateDir, name, backend)
	if err := os.MkdirAll(filepath.Dir(rootfsPath), 0o755); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(rootfsPath, os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(8 * 1024 * 1024); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command(mke2fsPath, "-q", "-t", "ext4", rootfsPath).CombinedOutput(); err != nil {
		t.Fatalf("mke2fs: %v: %s", err, out)
	}

	if err := runResize(t.Context(), []string{name, "--size-mib", "16", "--state-dir", stateDir, "--backend", backend}, os.Stdout); err != nil {
		t.Fatalf("runResize: %v", err)
	}

	manifest, err := workspace.ReadManifest(stateDir, name)
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Resources.SizeMiB != 16 {
		t.Fatalf("manifest.Resources.SizeMiB = %d, want 16", manifest.Resources.SizeMiB)
	}
}

func TestRunVolumeResizeUsage(t *testing.T) {
	if err := runVolumeResize(nil, os.Stdout); err == nil || !strings.Contains(err.Error(), "usage: microagent volume resize") {
		t.Fatalf("volume resize with no args error = %v", err)
	}
	if err := runVolumeResize([]string{"data", "extra"}, os.Stdout); err == nil || !strings.Contains(err.Error(), "usage: microagent volume resize") {
		t.Fatalf("volume resize with extra arg error = %v", err)
	}
}

func TestRunVolumeResizeRealFlow(t *testing.T) {
	if _, err := exec.LookPath("e2fsck"); err != nil {
		t.Skip("e2fsck not available")
	}
	if _, err := exec.LookPath("resize2fs"); err != nil {
		t.Skip("resize2fs not available")
	}
	stateDir := t.TempDir()
	if err := volume.WriteIndex(stateDir, volume.Index{Volumes: []volume.Record{{Name: "data", SizeMiB: 1024}}}); err != nil {
		t.Fatalf("WriteIndex: %v", err)
	}

	if err := runVolumeResize([]string{"data", "--size-mib", "0", "--state-dir", stateDir}, os.Stdout); err == nil {
		t.Fatal("expected an error for a zero-size resize")
	}

	if err := runVolumeResize([]string{"data", "--size-mib", "1024", "--state-dir", stateDir}, os.Stdout); err != nil {
		t.Fatalf("no-op resize to the same size: %v", err)
	}
	record, err := volume.Get(stateDir, "data")
	if err != nil {
		t.Fatal(err)
	}
	if record.SizeMiB != 1024 {
		t.Fatalf("record.SizeMiB = %d, want 1024", record.SizeMiB)
	}
}
