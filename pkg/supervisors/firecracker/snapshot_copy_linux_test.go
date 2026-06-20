//go:build linux

package firecracker

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

func TestCopyFilePreservesSparseHoles(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "rootfs.ext4")
	dst := filepath.Join(dir, "snapshot-rootfs.ext4")
	const size = 64 << 20
	head := bytes.Repeat([]byte{0x41}, 4096)
	tail := bytes.Repeat([]byte{0x42}, 4096)

	f, err := os.Create(src)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteAt(head, 0); err != nil {
		_ = f.Close()
		t.Fatal(err)
	}
	if _, err := f.WriteAt(tail, size-int64(len(tail))); err != nil {
		_ = f.Close()
		t.Fatal(err)
	}
	if err := f.Truncate(size); err != nil {
		_ = f.Close()
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	if err := copyFile(src, dst); err != nil {
		t.Fatalf("copyFile: %v", err)
	}

	srcInfo, err := os.Stat(src)
	if err != nil {
		t.Fatal(err)
	}
	dstInfo, err := os.Stat(dst)
	if err != nil {
		t.Fatal(err)
	}
	if dstInfo.Size() != srcInfo.Size() {
		t.Fatalf("dst size = %d, want %d", dstInfo.Size(), srcInfo.Size())
	}
	gotHead := make([]byte, len(head))
	gotTail := make([]byte, len(tail))
	copied, err := os.Open(dst)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := copied.ReadAt(gotHead, 0); err != nil {
		_ = copied.Close()
		t.Fatal(err)
	}
	if _, err := copied.ReadAt(gotTail, size-int64(len(tail))); err != nil {
		_ = copied.Close()
		t.Fatal(err)
	}
	if err := copied.Close(); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(gotHead, head) || !bytes.Equal(gotTail, tail) {
		t.Fatal("copied file did not preserve written extents")
	}

	var st unix.Stat_t
	if err := unix.Stat(dst, &st); err != nil {
		t.Fatal(err)
	}
	allocated := st.Blocks * 512
	if allocated >= size/4 {
		t.Fatalf("copied sparse file allocated %d bytes, want well below logical size %d", allocated, size)
	}
}
