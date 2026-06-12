package workspace

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/geoffbelknap/microagent/pkg/volume"
)

// TestPrepareDisksResolvesManagedVolume guards against the regression where a
// managed-volume disk reached ValidateDisk with an empty Path because resolution
// never ran (the cmd-layer resolver was dead code). PrepareDisks must resolve a
// ManagedVolume disk to its backing ext4 path and record the attachment.
func TestPrepareDisksResolvesManagedVolume(t *testing.T) {
	stateDir := t.TempDir()

	// Seed the volume registry directly (no mke2fs needed): a record plus a
	// placeholder backing file at the path PrepareDisks will resolve to.
	if err := volume.WriteIndex(stateDir, volume.Index{Volumes: []volume.Record{{Name: "data", SizeMiB: 32}}}); err != nil {
		t.Fatalf("seed registry: %v", err)
	}
	if err := os.WriteFile(volume.DiskPath(stateDir, "", "data"), []byte{}, 0o644); err != nil {
		t.Fatalf("seed backing file: %v", err)
	}

	opts := Options{
		StateDir: stateDir,
		Name:     "consumer",
		Disks: []Disk{{
			Name:          "data",
			Mountpoint:    "/work",
			Mode:          "rw",
			ManagedVolume: true,
		}},
	}
	disks, err := PrepareDisks(context.Background(), opts)
	if err != nil {
		t.Fatalf("PrepareDisks: %v", err)
	}
	if len(disks) != 1 {
		t.Fatalf("expected 1 disk, got %d", len(disks))
	}
	want := filepath.Join(stateDir, "volumes", "data.ext4")
	if disks[0].Path != want {
		t.Errorf("resolved path = %q, want %q", disks[0].Path, want)
	}
	if disks[0].ManagedVolume {
		t.Error("ManagedVolume flag should be cleared after resolution")
	}

	rec, err := volume.Get(stateDir, "data")
	if err != nil {
		t.Fatalf("get volume: %v", err)
	}
	if rec.AttachedTo != "consumer" {
		t.Errorf("expected volume attached to consumer, got %q", rec.AttachedTo)
	}
}

func TestPrepareDisksUnknownManagedVolumeFails(t *testing.T) {
	opts := Options{
		StateDir: t.TempDir(),
		Name:     "consumer",
		Disks:    []Disk{{Name: "missing", Mountpoint: "/work", Mode: "rw", ManagedVolume: true}},
	}
	if _, err := PrepareDisks(context.Background(), opts); err == nil {
		t.Error("expected PrepareDisks to fail for an unknown managed volume")
	}
}
