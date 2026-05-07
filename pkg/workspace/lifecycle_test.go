package workspace

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/geoffbelknap/microagent-kit/pkg/vmkit"
)

func TestManifestAndStatusLifecycleAreLibraryOwned(t *testing.T) {
	dir := t.TempDir()
	opts := Options{
		Name:          "agency-task",
		StateDir:      dir,
		Backend:       vmkit.BackendFirecracker,
		Profile:       "small",
		RestartPolicy: "never",
		MemoryMiB:     512,
		CPUCount:      2,
		SizeMiB:       1024,
		Network:       vmkit.NetworkConfig{Mode: "nat"},
		Disks: []Disk{{
			Name:       "work",
			Path:       "/tmp/work.ext4",
			Mountpoint: "/work",
			Mode:       "rw",
		}},
		Outputs: []Output{{Name: "result", Path: "/work/result.txt"}},
	}
	if err := WriteManifest(opts); err != nil {
		t.Fatalf("WriteManifest: %v", err)
	}
	manifest, err := ReadManifest(dir, "agency-task")
	if err != nil {
		t.Fatalf("ReadManifest: %v", err)
	}
	if manifest.Name != "agency-task" || manifest.Artifacts.Egress[0].Name != "result" {
		t.Fatalf("manifest = %#v", manifest)
	}

	req := Request(opts, "run", filepath.Join(dir, "workspaces", "agency-task", "rootfs.ext4"), "req-1")
	if err := WriteProcessState(opts, req, vmkit.StateRunning, 1234, ""); err != nil {
		t.Fatalf("WriteProcessState: %v", err)
	}
	resp, err := Status(opts)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if resp.Event == nil || resp.Event.State != vmkit.StateRunning {
		t.Fatalf("status response = %#v", resp)
	}
	if resp.Artifacts == nil || len(resp.Artifacts.Egress) != 1 {
		t.Fatalf("artifacts = %#v", resp.Artifacts)
	}
	if _, err := os.Stat(filepath.Join(dir, "agency-task", "runtime.json")); err != nil {
		t.Fatalf("runtime.json not written: %v", err)
	}
	artifacts, err := ArtifactsFor(dir, "agency-task")
	if err != nil {
		t.Fatalf("ArtifactsFor: %v", err)
	}
	if len(artifacts.Egress) != 1 || artifacts.Egress[0].Name != "result" {
		t.Fatalf("artifacts = %#v", artifacts)
	}
	entries, err := List(dir)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(entries) != 1 || entries[0].Name != "agency-task" || entries[0].State != string(vmkit.StateRunning) {
		t.Fatalf("entries = %#v", entries)
	}
}
