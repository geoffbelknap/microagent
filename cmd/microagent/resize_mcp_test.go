package main

import (
	"context"
	"testing"

	"github.com/geoffbelknap/microagent/pkg/volume"
	"github.com/geoffbelknap/microagent/pkg/workspace"
)

func TestMCPWorkspaceResizeUsesTypedHandler(t *testing.T) {
	old := mcpWorkspaceResize
	t.Cleanup(func() { mcpWorkspaceResize = old })

	mcpWorkspaceResize = func(opts workspace.ResizeOptions) (workspace.ResizeResult, error) {
		if opts.StateDir != "/tmp/state" || opts.Name != "demo" || opts.Backend != hostBackend() || opts.SizeMiB != 16384 {
			t.Fatalf("resize opts = %#v", opts)
		}
		if opts.Resize2fsPath == "" {
			t.Fatal("resize2fs path is empty")
		}
		return workspace.ResizeResult{Workspace: opts.Name, FromSizeMiB: 8192, ToSizeMiB: opts.SizeMiB}, nil
	}

	result, handled, err := runDirectMCPTool(context.Background(), "workspace.resize", map[string]any{
		"name": "demo", "size_mib": float64(16384), "state_dir": "/tmp/state",
	})
	if err != nil || !handled {
		t.Fatalf("workspace.resize: handled=%v err=%v", handled, err)
	}
	object, ok := result.(map[string]any)
	if !ok {
		t.Fatalf("result type = %T, want map", result)
	}
	if object["workspace"] != "demo" || object["to_size_mib"] != float64(16384) || object["from_size_mib"] != float64(8192) {
		t.Fatalf("workspace.resize result = %#v", result)
	}
}

func TestMCPVolumeResizeUsesTypedHandler(t *testing.T) {
	old := mcpVolumeResize
	t.Cleanup(func() { mcpVolumeResize = old })

	mcpVolumeResize = func(stateDir, name string, sizeMiB int64, e2fsckPath, resize2fsPath string, isRunning func(string) bool) (volume.Record, error) {
		if stateDir != "/tmp/state" || name != "data" || sizeMiB != 4096 || isRunning == nil {
			t.Fatalf("resize args: stateDir=%q name=%q sizeMiB=%d isRunningNil=%v", stateDir, name, sizeMiB, isRunning == nil)
		}
		if e2fsckPath == "" || resize2fsPath == "" {
			t.Fatal("e2fsck/resize2fs path is empty")
		}
		return volume.Record{Name: name, SizeMiB: sizeMiB}, nil
	}

	result, handled, err := runDirectMCPTool(context.Background(), "volume.resize", map[string]any{
		"name": "data", "size_mib": float64(4096), "state_dir": "/tmp/state",
	})
	if err != nil || !handled {
		t.Fatalf("volume.resize: handled=%v err=%v", handled, err)
	}
	object, ok := result.(map[string]any)
	if !ok {
		t.Fatalf("result type = %T, want map", result)
	}
	if object["name"] != "data" || object["size_mib"] != float64(4096) {
		t.Fatalf("volume.resize result = %#v", result)
	}
}

func TestPreviewDestructiveMCPToolWorkspaceResize(t *testing.T) {
	stateDir := t.TempDir()
	if err := workspace.WriteManifest(workspace.Options{Name: "demo", StateDir: stateDir, MemoryMiB: 512, CPUCount: 1, SizeMiB: 8192}); err != nil {
		t.Fatalf("WriteManifest: %v", err)
	}

	// Shrink: previews instead of resizing.
	preview := previewDestructiveMCPTool("workspace.resize", map[string]any{
		"name": "demo", "size_mib": float64(4096), "state_dir": stateDir, "preview": true,
	})
	if preview == nil {
		t.Fatal("expected a preview envelope for a shrink")
	}
	result, ok := preview["result"].(map[string]any)
	if !ok {
		t.Fatalf("preview envelope = %#v, want a result map", preview)
	}
	if result["preview"] != true || result["from_size_mib"] != int64(8192) || result["to_size_mib"] != int64(4096) {
		t.Fatalf("preview result = %#v", result)
	}

	// Grow: not destructive, so preview is a no-op (nil) and the real call proceeds.
	if got := previewDestructiveMCPTool("workspace.resize", map[string]any{
		"name": "demo", "size_mib": float64(16384), "state_dir": stateDir, "preview": true,
	}); got != nil {
		t.Fatalf("expected no preview for a grow, got %#v", got)
	}

	// No preview requested at all: always nil regardless of direction.
	if got := previewDestructiveMCPTool("workspace.resize", map[string]any{
		"name": "demo", "size_mib": float64(4096), "state_dir": stateDir,
	}); got != nil {
		t.Fatalf("expected no preview when preview is unset, got %#v", got)
	}
}

func TestPreviewDestructiveMCPToolVolumeResize(t *testing.T) {
	stateDir := t.TempDir()
	if err := volume.WriteIndex(stateDir, volume.Index{Volumes: []volume.Record{{Name: "data", SizeMiB: 1024}}}); err != nil {
		t.Fatalf("WriteIndex: %v", err)
	}

	preview := previewDestructiveMCPTool("volume.resize", map[string]any{
		"name": "data", "size_mib": float64(256), "state_dir": stateDir, "preview": true,
	})
	if preview == nil {
		t.Fatal("expected a preview envelope for a shrink")
	}
	result, ok := preview["result"].(map[string]any)
	if !ok {
		t.Fatalf("preview envelope = %#v, want a result map", preview)
	}
	if result["preview"] != true || result["from_size_mib"] != int64(1024) || result["to_size_mib"] != int64(256) {
		t.Fatalf("preview result = %#v", result)
	}

	if got := previewDestructiveMCPTool("volume.resize", map[string]any{
		"name": "data", "size_mib": float64(2048), "state_dir": stateDir, "preview": true,
	}); got != nil {
		t.Fatalf("expected no preview for a grow, got %#v", got)
	}
}
