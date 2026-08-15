package ext4fs

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestFitsShrink(t *testing.T) {
	// 4 KiB blocks: 1000 blocks total, 400 free -> 600 blocks (2,457,600 bytes) used.
	path := writeSyntheticSuperblock(t, 1000, 400, 2, false)
	usedBytes := int64(600 * 4096)

	if err := FitsShrink(path, usedBytes+ShrinkMargin); err != nil {
		t.Fatalf("FitsShrink(used+margin) = %v, want nil", err)
	}
	if err := FitsShrink(path, usedBytes+ShrinkMargin-1); err == nil {
		t.Fatal("FitsShrink accepted a target one byte short of the required margin")
	}
}

func TestResizeNoopWhenAlreadyTargetSize(t *testing.T) {
	path := filepath.Join(t.TempDir(), "image.ext4")
	if err := os.WriteFile(path, make([]byte, 4096), 0o644); err != nil {
		t.Fatal(err)
	}
	// Bogus tool paths prove Resize never execs anything when the backing
	// file is already the requested size.
	var phases []string
	if err := ResizeWithProgress("e2fsck-should-not-run", "resize2fs-should-not-run", path, 4096, func(phase string) {
		phases = append(phases, phase)
	}); err != nil {
		t.Fatalf("Resize no-op = %v, want nil", err)
	}
	if len(phases) != 1 || phases[0] != "verify" {
		t.Fatalf("no-op phases = %#v, want [verify]", phases)
	}
}

// requireE2fsprogs skips the test when any named tool is unavailable on the
// host, mirroring pkg/rootfs's rootfsHostFormat gating.
func requireE2fsprogs(t *testing.T, names ...string) map[string]string {
	t.Helper()
	paths := map[string]string{}
	for _, name := range names {
		path, err := exec.LookPath(name)
		if err != nil {
			t.Skipf("%s not available", name)
		}
		paths[name] = path
	}
	return paths
}

func buildRealExt4Image(t *testing.T, mke2fsPath string, sizeMiB int64) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "image.ext4")
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
	return path
}

func TestGrowRealFilesystem(t *testing.T) {
	tools := requireE2fsprogs(t, "mke2fs", "e2fsck", "resize2fs")
	path := buildRealExt4Image(t, tools["mke2fs"], 8)

	var phases []string
	if err := ResizeWithProgress(tools["e2fsck"], tools["resize2fs"], path, 16*1024*1024, func(phase string) {
		phases = append(phases, phase)
	}); err != nil {
		t.Fatalf("Grow: %v", err)
	}
	if got, want := strings.Join(phases, ","), "check,disk,filesystem,verify"; got != want {
		t.Fatalf("grow phases = %s, want %s", got, want)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() != 16*1024*1024 {
		t.Fatalf("backing file size = %d, want %d", info.Size(), 16*1024*1024)
	}
	usage, err := ReadUsage(path)
	if err != nil {
		t.Fatalf("ReadUsage after grow: %v", err)
	}
	if usage.TotalBytes < 15*1024*1024 {
		t.Fatalf("filesystem did not grow into the new space: total=%d", usage.TotalBytes)
	}
}

func TestShrinkRealFilesystem(t *testing.T) {
	tools := requireE2fsprogs(t, "mke2fs", "e2fsck", "resize2fs")
	path := buildRealExt4Image(t, tools["mke2fs"], 32)

	var phases []string
	if err := ResizeWithProgress(tools["e2fsck"], tools["resize2fs"], path, 16*1024*1024, func(phase string) {
		phases = append(phases, phase)
	}); err != nil {
		t.Fatalf("Shrink: %v", err)
	}
	if got, want := strings.Join(phases, ","), "check,filesystem,disk,verify"; got != want {
		t.Fatalf("shrink phases = %s, want %s", got, want)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() != 16*1024*1024 {
		t.Fatalf("backing file size = %d, want %d", info.Size(), 16*1024*1024)
	}
	usage, err := ReadUsage(path)
	if err != nil {
		t.Fatalf("ReadUsage after shrink: %v", err)
	}
	if usage.TotalBytes > 17*1024*1024 {
		t.Fatalf("filesystem did not shrink: total=%d", usage.TotalBytes)
	}
}

func TestShrinkRefusesWhenTargetTooSmall(t *testing.T) {
	tools := requireE2fsprogs(t, "mke2fs", "e2fsck", "resize2fs")
	path := buildRealExt4Image(t, tools["mke2fs"], 8)

	if err := Shrink(tools["e2fsck"], tools["resize2fs"], path, 1024); err == nil {
		t.Fatal("Shrink accepted a target far smaller than the filesystem's own used blocks")
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() != 8*1024*1024 {
		t.Fatalf("backing file size changed after a refused shrink: %d", info.Size())
	}
}
