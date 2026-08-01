package ext4fs

import (
	"encoding/binary"
	"fmt"
	"os"
	"syscall"
)

// Usage is a host-side snapshot of an ext4 image's occupancy. Three numbers,
// three meanings: TotalBytes is the filesystem's provisioned capacity,
// UsedBytes is what the filesystem's own accounting says is occupied, and
// HostAllocatedBytes is what the image really costs on the host disk (sparse
// images allocate far less than their logical size). Read without mounting,
// so a concurrently running guest makes this advisory, not exact.
type Usage struct {
	TotalBytes         int64
	FreeBytes          int64
	UsedBytes          int64
	HostAllocatedBytes int64
}

const (
	ext4SuperblockOffset = 1024
	ext4SuperblockSize   = 1024
	ext4Magic            = 0xEF53
	ext4Incompat64Bit    = 0x80
)

// ReadUsage parses the primary superblock of the ext4 image at path. It never
// mounts or modifies the image.
func ReadUsage(path string) (Usage, error) {
	f, err := os.Open(path)
	if err != nil {
		return Usage{}, err
	}
	defer func() { _ = f.Close() }()
	sb := make([]byte, ext4SuperblockSize)
	if _, err := f.ReadAt(sb, ext4SuperblockOffset); err != nil {
		return Usage{}, fmt.Errorf("read ext4 superblock of %s: %w", path, err)
	}
	if binary.LittleEndian.Uint16(sb[0x38:]) != ext4Magic {
		return Usage{}, fmt.Errorf("%s has no ext4 superblock magic", path)
	}
	blockSize := int64(1024) << binary.LittleEndian.Uint32(sb[0x18:])
	if blockSize <= 0 || blockSize > 64*1024 {
		return Usage{}, fmt.Errorf("%s reports implausible ext4 block size %d", path, blockSize)
	}
	blocks := uint64(binary.LittleEndian.Uint32(sb[0x4:]))
	freeBlocks := uint64(binary.LittleEndian.Uint32(sb[0xC:]))
	if binary.LittleEndian.Uint32(sb[0x60:])&ext4Incompat64Bit != 0 {
		blocks |= uint64(binary.LittleEndian.Uint32(sb[0x150:])) << 32
		freeBlocks |= uint64(binary.LittleEndian.Uint32(sb[0x158:])) << 32
	}
	if freeBlocks > blocks {
		return Usage{}, fmt.Errorf("%s reports more free than total ext4 blocks", path)
	}
	usage := Usage{
		TotalBytes: int64(blocks) * blockSize,
		FreeBytes:  int64(freeBlocks) * blockSize,
	}
	usage.UsedBytes = usage.TotalBytes - usage.FreeBytes
	if info, err := f.Stat(); err == nil {
		if sys, ok := info.Sys().(*syscall.Stat_t); ok {
			usage.HostAllocatedBytes = sys.Blocks * 512
		}
	}
	return usage, nil
}
