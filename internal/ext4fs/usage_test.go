package ext4fs

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
)

// writeSyntheticSuperblock writes a minimal ext4 primary superblock: enough
// for ReadUsage, nothing more. Hermetic on purpose — no mke2fs dependency.
func writeSyntheticSuperblock(t *testing.T, blocks, freeBlocks uint64, logBlockSize uint32, sixtyFourBit bool) string {
	t.Helper()
	sb := make([]byte, ext4SuperblockSize)
	binary.LittleEndian.PutUint16(sb[0x38:], ext4Magic)
	binary.LittleEndian.PutUint32(sb[0x18:], logBlockSize)
	binary.LittleEndian.PutUint32(sb[0x4:], uint32(blocks))
	binary.LittleEndian.PutUint32(sb[0xC:], uint32(freeBlocks))
	if sixtyFourBit {
		binary.LittleEndian.PutUint32(sb[0x60:], ext4Incompat64Bit)
		binary.LittleEndian.PutUint32(sb[0x150:], uint32(blocks>>32))
		binary.LittleEndian.PutUint32(sb[0x158:], uint32(freeBlocks>>32))
	}
	path := filepath.Join(t.TempDir(), "image.ext4")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteAt(sb, ext4SuperblockOffset); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestReadUsage(t *testing.T) {
	// 4 KiB blocks (log=2): 1000 blocks total, 400 free.
	path := writeSyntheticSuperblock(t, 1000, 400, 2, false)
	usage, err := ReadUsage(path)
	if err != nil {
		t.Fatalf("ReadUsage: %v", err)
	}
	if usage.TotalBytes != 1000*4096 || usage.FreeBytes != 400*4096 || usage.UsedBytes != 600*4096 {
		t.Fatalf("usage = %+v, want 1000/400/600 blocks of 4096", usage)
	}
	if usage.HostAllocatedBytes <= 0 {
		t.Fatalf("host allocated = %d, want > 0", usage.HostAllocatedBytes)
	}
}

func TestReadUsage64Bit(t *testing.T) {
	blocks := uint64(5) << 32
	free := uint64(1) << 32
	path := writeSyntheticSuperblock(t, blocks, free, 2, true)
	usage, err := ReadUsage(path)
	if err != nil {
		t.Fatalf("ReadUsage: %v", err)
	}
	if usage.TotalBytes != int64(blocks)*4096 || usage.FreeBytes != int64(free)*4096 {
		t.Fatalf("usage = %+v, want 64-bit block counts honored", usage)
	}
}

func TestReadUsageRejectsNonExt4(t *testing.T) {
	path := filepath.Join(t.TempDir(), "not-ext4")
	if err := os.WriteFile(path, make([]byte, 4096), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadUsage(path); err == nil {
		t.Fatal("ReadUsage accepted a non-ext4 file")
	}
}

func TestReadUsageRejectsCorruptCounts(t *testing.T) {
	path := writeSyntheticSuperblock(t, 100, 200, 2, false)
	if _, err := ReadUsage(path); err == nil {
		t.Fatal("ReadUsage accepted free > total")
	}
}
