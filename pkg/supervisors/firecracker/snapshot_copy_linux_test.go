//go:build linux

package firecracker

import (
	"bytes"
	"errors"
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

// TestCopyRangeIsByteExactAndFallsBack: the kernel-side extent copy must
// reproduce bytes exactly (it runs while the guest is PAUSED, so it is
// stop-the-world and worth keeping in the kernel), and must report
// errCopyRangeUnsupported — rather than a hard failure — when the kernel or
// filesystem cannot service it, so the userspace path can take over.
func TestCopyRangeIsByteExactAndFallsBack(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	body := bytes.Repeat([]byte("microplane-forensic-evidence"), 4096) // ~112 KiB
	if err := os.WriteFile(src, body, 0o600); err != nil {
		t.Fatal(err)
	}
	in, err := os.Open(src)
	if err != nil {
		t.Fatal(err)
	}
	defer in.Close()
	dst := filepath.Join(dir, "dst")
	out, err := os.Create(dst)
	if err != nil {
		t.Fatal(err)
	}
	if err := copyRange(in, out, 0, int64(len(body))); err != nil {
		if errors.Is(err, errCopyRangeUnsupported) {
			t.Skip("copy_file_range unsupported here; the userspace fallback covers it")
		}
		t.Fatalf("copyRange: %v", err)
	}
	if err := out.Close(); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, body) {
		t.Fatalf("copied %d bytes, want a byte-exact %d-byte copy", len(got), len(body))
	}

	// A closed destination cannot be serviced; the helper must classify that as
	// unsupported (nothing copied) so the caller falls back instead of failing.
	bad, err := os.Create(filepath.Join(dir, "bad"))
	if err != nil {
		t.Fatal(err)
	}
	_ = bad.Close()
	if err := copyRange(in, bad, 0, int64(len(body))); !errors.Is(err, errCopyRangeUnsupported) {
		t.Fatalf("copyRange on an unusable target = %v, want errCopyRangeUnsupported", err)
	}
}
