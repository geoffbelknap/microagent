package workspace

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/geoffbelknap/microagent/pkg/operation"
	"github.com/geoffbelknap/microagent/pkg/vmkit"
)

// requireResizeTools resolves mke2fs/e2fsck/resize2fs on the host or skips,
// mirroring internal/ext4fs's own test gating.
func requireResizeTools(t *testing.T) (mke2fs, e2fsckTool, resize2fs string) {
	t.Helper()
	for _, name := range []string{"mke2fs", "e2fsck", "resize2fs"} {
		if _, err := exec.LookPath(name); err != nil {
			t.Skipf("%s not available", name)
		}
	}
	mke2fs, _ = exec.LookPath("mke2fs")
	e2fsckTool, _ = exec.LookPath("e2fsck")
	resize2fs, _ = exec.LookPath("resize2fs")
	return mke2fs, e2fsckTool, resize2fs
}

// buildRealRootfs formats a real, small ext4 image at the workspace's rootfs
// path so Resize has real e2fsprogs tooling to exercise, not a synthetic
// superblock.
func buildRealRootfs(t *testing.T, mke2fsPath, path string, sizeMiB int64) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(sizeMiB * 1024 * 1024); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(mke2fsPath, "-q", "-t", "ext4", path)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("mke2fs: %v: %s", err, out)
	}
}

func writeResizeFixture(t *testing.T, dir, name string, state vmkit.VMState, sizeMiB int64, verification *vmkit.RuntimeVerification) {
	t.Helper()
	backend := startableBackend()
	opts := Options{Name: name, StateDir: dir, Backend: backend, MemoryMiB: 512, CPUCount: 1, SizeMiB: sizeMiB, Verification: verification}
	if err := WriteManifest(opts); err != nil {
		t.Fatalf("WriteManifest: %v", err)
	}
	event := EventFile{
		Identity:   vmkit.Identity{RuntimeID: name, Backend: backend},
		State:      state,
		ObservedAt: time.Now().UTC().Format(time.RFC3339),
	}
	if err := writeJSONFile(filepath.Join(dir, name, "event.json"), event); err != nil {
		t.Fatalf("write event: %v", err)
	}
}

func TestResizeGrowsRootfsAndRefreshesManifest(t *testing.T) {
	mke2fsPath, _, resize2fsPath := requireResizeTools(t)
	dir := t.TempDir()
	const name = "resize-grow"
	backend := startableBackend()

	writeResizeFixture(t, dir, name, vmkit.StateHalted, 8, &vmkit.RuntimeVerification{
		OK:     true,
		Rootfs: &vmkit.VerifiedArtifact{SHA256: "stale-hash-from-before-resize"},
	})
	rootfsPath := WorkspaceRootfsPath(dir, name, backend)
	buildRealRootfs(t, mke2fsPath, rootfsPath, 8)

	var phases []string
	result, err := Resize(ResizeOptions{StateDir: dir, Name: name, Backend: backend, SizeMiB: 16, Resize2fsPath: resize2fsPath, Progress: func(event operation.ProgressEvent) {
		phases = append(phases, event.Phase)
	}})
	if err != nil {
		t.Fatalf("Resize: %v", err)
	}
	if result.FromSizeMiB != 8 || result.ToSizeMiB != 16 {
		t.Fatalf("result = %+v, want from=8 to=16", result)
	}
	assertProgressPhaseOrder(t, phases, []string{"resize_validate", "resize_check", "resize_disk", "resize_filesystem", "resize_verify", "resize_published"})
	if result.Usage == nil || result.Usage.SizeMiB < 15 {
		t.Fatalf("result.Usage = %+v, want a ~16 MiB filesystem", result.Usage)
	}

	info, err := os.Stat(rootfsPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() != 16*1024*1024 {
		t.Fatalf("rootfs size = %d, want %d", info.Size(), 16*1024*1024)
	}

	manifest, err := ReadManifest(dir, name)
	if err != nil {
		t.Fatalf("ReadManifest: %v", err)
	}
	if manifest.Resources.SizeMiB != 16 {
		t.Fatalf("manifest.Resources.SizeMiB = %d, want 16", manifest.Resources.SizeMiB)
	}
	if manifest.SizeDerived {
		t.Fatal("manifest.SizeDerived should be false after an explicit resize")
	}
	if manifest.Verification == nil || manifest.Verification.Rootfs == nil {
		t.Fatal("manifest.Verification.Rootfs should still be recorded after resize")
	}
	wantSHA, err := FileSHA256(rootfsPath)
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Verification.Rootfs.SHA256 != wantSHA {
		t.Fatalf("manifest.Verification.Rootfs.SHA256 = %q, want refreshed to %q", manifest.Verification.Rootfs.SHA256, wantSHA)
	}
}

func TestResizeShrinksRootfs(t *testing.T) {
	mke2fsPath, _, resize2fsPath := requireResizeTools(t)
	dir := t.TempDir()
	const name = "resize-shrink"
	backend := startableBackend()

	writeResizeFixture(t, dir, name, vmkit.StateStopped, 32, nil)
	rootfsPath := WorkspaceRootfsPath(dir, name, backend)
	buildRealRootfs(t, mke2fsPath, rootfsPath, 32)

	result, err := Resize(ResizeOptions{StateDir: dir, Name: name, Backend: backend, SizeMiB: 16, Resize2fsPath: resize2fsPath})
	if err != nil {
		t.Fatalf("Resize: %v", err)
	}
	if result.FromSizeMiB != 32 || result.ToSizeMiB != 16 {
		t.Fatalf("result = %+v, want from=32 to=16", result)
	}
	info, err := os.Stat(rootfsPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() != 16*1024*1024 {
		t.Fatalf("rootfs size = %d, want %d", info.Size(), 16*1024*1024)
	}
}

func TestResizeRefusesWhileRunning(t *testing.T) {
	dir := t.TempDir()
	const name = "resize-running"
	writeResizeFixture(t, dir, name, vmkit.StateRunning, 8, nil)

	// Bogus tool path proves Resize refuses before ever touching the disk.
	_, err := Resize(ResizeOptions{StateDir: dir, Name: name, Backend: startableBackend(), SizeMiB: 16, Resize2fsPath: "should-not-run"})
	if err == nil {
		t.Fatal("Resize succeeded on a running workspace")
	}
}

func TestResizeRefusesWhilePaused(t *testing.T) {
	dir := t.TempDir()
	const name = "resize-paused"
	writeResizeFixture(t, dir, name, vmkit.StatePaused, 8, nil)

	if _, err := Resize(ResizeOptions{StateDir: dir, Name: name, Backend: startableBackend(), SizeMiB: 16, Resize2fsPath: "should-not-run"}); err == nil {
		t.Fatal("Resize succeeded on a paused workspace")
	}
}

func TestResizeRefusesWithSnapshots(t *testing.T) {
	dir := t.TempDir()
	const name = "resize-snapshotted"
	writeResizeFixture(t, dir, name, vmkit.StateHalted, 8, nil)

	snapshotDir := filepath.Join(vmkit.SnapshotsDir(dir, name), "idle")
	if err := os.MkdirAll(snapshotDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := vmkit.WriteSnapshotManifest(snapshotDir, vmkit.SnapshotManifest{Tag: "idle", CreatedAt: time.Now().UTC().Format(time.RFC3339)}); err != nil {
		t.Fatalf("WriteSnapshotManifest: %v", err)
	}

	if _, err := Resize(ResizeOptions{StateDir: dir, Name: name, Backend: startableBackend(), SizeMiB: 16, Resize2fsPath: "should-not-run"}); err == nil {
		t.Fatal("Resize succeeded on a workspace with a snapshot present")
	}
}

func TestResizeRejectsNonPositiveSize(t *testing.T) {
	dir := t.TempDir()
	if _, err := Resize(ResizeOptions{StateDir: dir, Name: "resize-zero", SizeMiB: 0}); err == nil {
		t.Fatal("Resize accepted a zero target size")
	}
	if _, err := Resize(ResizeOptions{StateDir: dir, Name: "resize-negative", SizeMiB: -1}); err == nil {
		t.Fatal("Resize accepted a negative target size")
	}
}
