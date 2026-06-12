//go:build windows

package rootfs

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/binary"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Microsoft/hcsshim/ext4/tar2ext4"
)

func TestBuildVHDImageWritesFixedVHDFooter(t *testing.T) {
	dir := t.TempDir()
	stageDir := filepath.Join(dir, "stage")
	if err := os.MkdirAll(filepath.Join(stageDir, "etc"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stageDir, "etc", "hostname"), []byte("microagent\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	outputPath := filepath.Join(dir, "rootfs.vhd")

	if err := buildVHDImage(context.Background(), stageDir, filepath.Join(dir, "tmp.vhd"), outputPath, 64*1024*1024, false); err != nil {
		t.Fatalf("buildVHDImage: %v", err)
	}
	data, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) < 512 {
		t.Fatalf("vhd size = %d, want at least fixed VHD footer", len(data))
	}
	if !bytes.Contains(data[len(data)-512:], []byte("conectix")) {
		t.Fatalf("vhd footer missing conectix cookie")
	}
}

func TestBuildVHDImageReservesFreeSpaceTowardSize(t *testing.T) {
	dir := t.TempDir()
	stageDir := filepath.Join(dir, "stage")
	if err := os.MkdirAll(filepath.Join(stageDir, "etc"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stageDir, "etc", "hostname"), []byte("microagent\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	outputPath := filepath.Join(dir, "rootfs.vhd")
	const sizeBytes = 64 * 1024 * 1024

	if err := buildVHDImage(context.Background(), stageDir, filepath.Join(dir, "tmp.vhd"), outputPath, sizeBytes, true); err != nil {
		t.Fatalf("buildVHDImage: %v", err)
	}
	out, err := os.Open(outputPath)
	if err != nil {
		t.Fatal(err)
	}
	defer out.Close()
	sb, err := tar2ext4.ReadExt4SuperBlockReadSeeker(out)
	if err != nil {
		t.Fatalf("read superblock: %v", err)
	}
	// The reserved-space file pads the filesystem toward the requested
	// size; without it a content-sized image leaves the guest no room to
	// write once the readonly flag is cleared.
	gotBytes := int64(sb.BlocksCountLow) * 4096
	if gotBytes < sizeBytes-2*vhdMetadataAllowanceBytes {
		t.Fatalf("filesystem spans %d bytes, want close to the requested %d", gotBytes, sizeBytes)
	}
}

func TestBuildVHDImageFailsClosedWhenContentExceedsSize(t *testing.T) {
	dir := t.TempDir()
	stageDir := filepath.Join(dir, "stage")
	if err := os.MkdirAll(stageDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stageDir, "big.bin"), bytes.Repeat([]byte{0xAB}, 6*1024*1024), 0o644); err != nil {
		t.Fatal(err)
	}
	err := buildVHDImage(context.Background(), stageDir, filepath.Join(dir, "tmp.vhd"), filepath.Join(dir, "rootfs.vhd"), 8*1024*1024, true)
	if err == nil || !strings.Contains(err.Error(), "cannot hold the image content") {
		t.Fatalf("err = %v, want content-exceeds-size failure", err)
	}
}

func TestWriteStageTarEmitsHardLinksOnce(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	content := bytes.Repeat([]byte{0x42}, 8192)
	if err := os.WriteFile(filepath.Join(dir, "bin", "busybox"), content, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, applet := range []string{"sh", "ls"} {
		if err := os.Link(filepath.Join(dir, "bin", "busybox"), filepath.Join(dir, "bin", applet)); err != nil {
			t.Fatalf("hard link %s: %v", applet, err)
		}
	}

	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	contentBytes, err := writeStageTar(dir, tw)
	if err != nil {
		t.Fatalf("writeStageTar: %v", err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	// One copy of the content, not three.
	if want := int64(len(content)); contentBytes != want {
		t.Fatalf("contentBytes = %d, want %d (hard links deduplicated)", contentBytes, want)
	}

	regular, links := 0, 0
	tr := tar.NewReader(bytes.NewReader(buf.Bytes()))
	for {
		header, err := tr.Next()
		if err != nil {
			break
		}
		if !strings.HasPrefix(header.Name, "bin/") || header.Typeflag == tar.TypeDir {
			continue
		}
		switch header.Typeflag {
		case tar.TypeReg:
			regular++
		case tar.TypeLink:
			links++
			if header.Linkname == "" || header.Linkname == header.Name {
				t.Fatalf("hard link entry %q has bad target %q", header.Name, header.Linkname)
			}
		default:
			t.Fatalf("unexpected entry type %d for %q", header.Typeflag, header.Name)
		}
	}
	if regular != 1 || links != 2 {
		t.Fatalf("entries = %d regular + %d links, want 1 + 2", regular, links)
	}
}

func TestBuildVHDImageClearsExt4ReadonlyFeature(t *testing.T) {
	dir := t.TempDir()
	stageDir := filepath.Join(dir, "stage")
	if err := os.MkdirAll(filepath.Join(stageDir, "etc"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stageDir, "etc", "hostname"), []byte("microagent\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	outputPath := filepath.Join(dir, "rootfs.vhd")

	if err := buildVHDImage(context.Background(), stageDir, filepath.Join(dir, "tmp.vhd"), outputPath, 64*1024*1024, true); err != nil {
		t.Fatalf("buildVHDImage: %v", err)
	}
	out, err := os.Open(outputPath)
	if err != nil {
		t.Fatal(err)
	}
	defer out.Close()
	sb, err := tar2ext4.ReadExt4SuperBlockReadSeeker(out)
	if err != nil {
		t.Fatalf("read superblock: %v", err)
	}
	if sb.FeatureRoCompat&ext4RoCompatReadonly != 0 {
		t.Fatalf("RO_COMPAT_READONLY still set: ro_compat = %#x; the guest kernel forces a read-only root mount", sb.FeatureRoCompat)
	}
}

func TestClearExt4ReadonlyFeatureDropsOnlyTheReadonlyFlag(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.ext4")
	out, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer out.Close()
	if err := tar2ext4.Convert(bytes.NewReader(emptyTar(t)), out, tar2ext4.MaximumDiskSize(64*1024*1024)); err != nil {
		t.Fatalf("tar2ext4.Convert: %v", err)
	}

	if _, err := out.Seek(0, 0); err != nil {
		t.Fatal(err)
	}
	before, err := tar2ext4.ReadExt4SuperBlockReadSeeker(out)
	if err != nil {
		t.Fatalf("read superblock before: %v", err)
	}
	if before.FeatureRoCompat&ext4RoCompatReadonly == 0 {
		t.Fatalf("tar2ext4 no longer sets RO_COMPAT_READONLY (%#x); the clear step may be obsolete", before.FeatureRoCompat)
	}

	if err := clearExt4ReadonlyFeature(out); err != nil {
		t.Fatalf("clearExt4ReadonlyFeature: %v", err)
	}

	if _, err := out.Seek(0, 0); err != nil {
		t.Fatal(err)
	}
	after, err := tar2ext4.ReadExt4SuperBlockReadSeeker(out)
	if err != nil {
		t.Fatalf("read superblock after: %v", err)
	}
	if after.FeatureRoCompat&ext4RoCompatReadonly != 0 {
		t.Fatalf("RO_COMPAT_READONLY still set: %#x", after.FeatureRoCompat)
	}
	if want := before.FeatureRoCompat &^ ext4RoCompatReadonly; after.FeatureRoCompat != want {
		t.Fatalf("other RO_COMPAT flags changed: before %#x after %#x", before.FeatureRoCompat, after.FeatureRoCompat)
	}
	if after.FeatureCompat != before.FeatureCompat || after.FeatureIncompat != before.FeatureIncompat {
		t.Fatalf("unrelated feature fields changed: compat %#x->%#x incompat %#x->%#x",
			before.FeatureCompat, after.FeatureCompat, before.FeatureIncompat, after.FeatureIncompat)
	}

	// Idempotent on an already-cleared image.
	if err := clearExt4ReadonlyFeature(out); err != nil {
		t.Fatalf("clearExt4ReadonlyFeature second pass: %v", err)
	}
}

func TestClearExt4ReadonlyFeatureRejectsNonExt4(t *testing.T) {
	path := filepath.Join(t.TempDir(), "not.ext4")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	junk := make([]byte, 4096)
	binary.LittleEndian.PutUint16(junk[ext4MagicOffset:], 0xBEEF)
	if _, err := f.Write(junk); err != nil {
		t.Fatal(err)
	}
	err = clearExt4ReadonlyFeature(f)
	if err == nil || !strings.Contains(err.Error(), "superblock magic") {
		t.Fatalf("err = %v, want superblock magic mismatch", err)
	}
}

// emptyTar returns a tar archive with a single file, the minimal input
// tar2ext4 accepts for a filesystem build.
func emptyTar(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	if err := tw.WriteHeader(&tar.Header{Name: "hello.txt", Mode: 0o644, Size: 5}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write([]byte("hello")); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}
