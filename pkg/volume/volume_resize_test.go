package volume

import (
	"context"
	"os"
	"os/exec"
	"testing"
)

// requireVolumeResizeTools resolves mke2fs/e2fsck/resize2fs on the host or
// skips, mirroring internal/ext4fs's own test gating.
func requireVolumeResizeTools(t *testing.T) (mke2fs, e2fsckTool, resize2fs string) {
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

func TestVolumeResizeGrowsAndShrinks(t *testing.T) {
	mke2fsPath, e2fsckPath, resize2fsPath := requireVolumeResizeTools(t)
	dir := t.TempDir()

	if _, err := Create(context.Background(), dir, "", "data", 8, mke2fsPath); err != nil {
		t.Fatalf("create: %v", err)
	}

	rec, err := Resize(dir, "data", 32, e2fsckPath, resize2fsPath, nil)
	if err != nil {
		t.Fatalf("resize up: %v", err)
	}
	if rec.SizeMiB != 32 {
		t.Fatalf("record.SizeMiB = %d, want 32", rec.SizeMiB)
	}
	info, err := os.Stat(DiskPath(dir, "", "data"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() != 32*1024*1024 {
		t.Fatalf("backing file size = %d, want %d", info.Size(), 32*1024*1024)
	}
	if got, err := Get(dir, "data"); err != nil || got.SizeMiB != 32 {
		t.Fatalf("Get after grow = %+v, %v, want SizeMiB=32", got, err)
	}

	rec, err = Resize(dir, "data", 16, e2fsckPath, resize2fsPath, nil)
	if err != nil {
		t.Fatalf("resize down: %v", err)
	}
	if rec.SizeMiB != 16 {
		t.Fatalf("record.SizeMiB = %d, want 16", rec.SizeMiB)
	}
	info, err = os.Stat(DiskPath(dir, "", "data"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() != 16*1024*1024 {
		t.Fatalf("backing file size = %d, want %d", info.Size(), 16*1024*1024)
	}
}

func TestVolumeResizeNoopWhenSameSize(t *testing.T) {
	fakeFormat(t)
	dir := t.TempDir()
	if _, err := Create(context.Background(), dir, "", "data", 256, ""); err != nil {
		t.Fatalf("create: %v", err)
	}
	// Bogus tool paths prove Resize never execs anything when the requested
	// size matches the recorded size.
	rec, err := Resize(dir, "data", 256, "should-not-run", "should-not-run", nil)
	if err != nil {
		t.Fatalf("resize no-op: %v", err)
	}
	if rec.SizeMiB != 256 {
		t.Fatalf("record.SizeMiB = %d, want 256", rec.SizeMiB)
	}
}

func TestVolumeResizeRefusesWhileAttachedAndRunning(t *testing.T) {
	fakeFormat(t)
	dir := t.TempDir()
	if _, err := Create(context.Background(), dir, "", "data", 256, ""); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := Attach(dir, "data", "ws1", nil); err != nil {
		t.Fatalf("attach: %v", err)
	}

	if _, err := Resize(dir, "data", 512, "should-not-run", "should-not-run", func(string) bool { return true }); err == nil {
		t.Fatal("Resize succeeded on a volume attached to a running workspace")
	}
}

func TestVolumeResizeAllowedWhenHolderNotRunning(t *testing.T) {
	fakeFormat(t)
	dir := t.TempDir()
	if _, err := Create(context.Background(), dir, "", "data", 256, ""); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := Attach(dir, "data", "ws1", nil); err != nil {
		t.Fatalf("attach: %v", err)
	}
	// Same size as recorded: no-op path, so no real e2fsprogs tools needed
	// to prove the running-holder gate is what's being tested here.
	if _, err := Resize(dir, "data", 256, "should-not-run", "should-not-run", func(string) bool { return false }); err != nil {
		t.Fatalf("Resize refused with a stale (non-running) holder: %v", err)
	}
}

func TestVolumeResizeRejectsInvalidSize(t *testing.T) {
	fakeFormat(t)
	dir := t.TempDir()
	if _, err := Create(context.Background(), dir, "", "data", 256, ""); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := Resize(dir, "data", 0, "", "", nil); err == nil {
		t.Fatal("Resize accepted a zero size")
	}
	if _, err := Resize(dir, "data", maxSizeMiB+1, "", "", nil); err == nil {
		t.Fatal("Resize accepted an oversize target")
	}
}

func TestVolumeResizeUnknownVolume(t *testing.T) {
	dir := t.TempDir()
	if _, err := Resize(dir, "missing", 256, "", "", nil); err == nil {
		t.Fatal("Resize succeeded on an unknown volume")
	}
}
