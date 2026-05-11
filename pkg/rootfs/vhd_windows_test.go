//go:build windows

package rootfs

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
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

	if err := buildVHDImage(context.Background(), stageDir, filepath.Join(dir, "tmp.vhd"), outputPath, 64*1024*1024); err != nil {
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
