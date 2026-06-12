//go:build windows

package rootfs

import (
	"archive/tar"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/Microsoft/hcsshim/ext4/tar2ext4"
)

// ext4 superblock locations and values used to drop the read-only feature
// flag, per the ext4 disk layout (superblock at byte 1024; s_magic at +0x38,
// s_feature_ro_compat at +0x64).
const (
	ext4SuperBlockOffset = 1024
	ext4MagicOffset      = ext4SuperBlockOffset + 0x38
	ext4RoCompatOffset   = ext4SuperBlockOffset + 0x64
	ext4SuperBlockMagic  = 0xEF53
	ext4RoCompatReadonly = 0x1000
)

// reservedSpaceName is a zero-filled file appended to rootfs image tars so
// the tar2ext4-built filesystem carries guest-writable capacity (tar2ext4
// sizes filesystems to their content with no free blocks). The guest init
// deletes it on first boot, releasing the blocks. The name must match the
// removal in cmd/microagent-guestinit.
const reservedSpaceName = ".microagent-reserved-space"

// vhdMetadataAllowanceBytes is headroom left for ext4 metadata (inode
// tables, bitmaps, group descriptors) when sizing the reserved-space file;
// actual metadata for workspace-scale images is well under this.
const vhdMetadataAllowanceBytes = 8 * 1024 * 1024

// writeImageTar streams the stage tree as a tar archive. When
// targetSizeBytes is positive, it appends a zero-filled reserved-space
// entry that pads the resulting filesystem toward targetSizeBytes so the
// guest has writable capacity once the entry is deleted at first boot.
// Fails closed when the content plus metadata cannot fit the target size.
func writeImageTar(stageDir string, tw *tar.Writer, targetSizeBytes int64) error {
	contentBytes, err := writeStageTar(stageDir, tw)
	if err != nil {
		return err
	}
	if targetSizeBytes <= 0 {
		return nil
	}
	reserve := targetSizeBytes - contentBytes - vhdMetadataAllowanceBytes
	if reserve <= 0 {
		return fmt.Errorf("rootfs size %d MiB cannot hold the image content (%d MiB plus filesystem metadata); increase the rootfs size",
			targetSizeBytes/(1024*1024), contentBytes/(1024*1024))
	}
	if err := tw.WriteHeader(&tar.Header{Name: reservedSpaceName, Mode: 0o600, Size: reserve}); err != nil {
		return err
	}
	_, err = io.CopyN(tw, zeroReader{}, reserve)
	return err
}

type zeroReader struct{}

func (zeroReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = 0
	}
	return len(p), nil
}

// clearExt4ReadonlyFeature drops the RO_COMPAT_READONLY flag that tar2ext4
// stamps into every filesystem it writes. tar2ext4 backs shared read-only
// container layer VHDs, so the flag is right for its callers — but it makes
// the kernel force a read-only mount even though microagent boots the guest
// with root=/dev/sda rw and attaches the VHD writable. A workspace owns its
// rootfs exclusively, and the writer emits complete block/inode bitmaps and
// free counts, so this flag is the only thing keeping the guest from
// writing. Fails closed when the superblock magic does not match.
func clearExt4ReadonlyFeature(f io.ReadWriteSeeker) error {
	var magic [2]byte
	if _, err := f.Seek(ext4MagicOffset, io.SeekStart); err != nil {
		return err
	}
	if _, err := io.ReadFull(f, magic[:]); err != nil {
		return err
	}
	if got := binary.LittleEndian.Uint16(magic[:]); got != ext4SuperBlockMagic {
		return fmt.Errorf("ext4 superblock magic = %#x, want %#x", got, ext4SuperBlockMagic)
	}
	var flags [4]byte
	if _, err := f.Seek(ext4RoCompatOffset, io.SeekStart); err != nil {
		return err
	}
	if _, err := io.ReadFull(f, flags[:]); err != nil {
		return err
	}
	roCompat := binary.LittleEndian.Uint32(flags[:])
	if roCompat&ext4RoCompatReadonly == 0 {
		return nil
	}
	binary.LittleEndian.PutUint32(flags[:], roCompat&^uint32(ext4RoCompatReadonly))
	if _, err := f.Seek(ext4RoCompatOffset, io.SeekStart); err != nil {
		return err
	}
	_, err := f.Write(flags[:])
	return err
}

// buildVHDImage converts the stage tree into a fixed VHD holding an ext4
// filesystem. When reserveFreeSpace is true (workspace rootfs images), the
// tar stream is padded toward sizeBytes with the reserved-space file the
// guest init deletes on first boot, so the guest gets writable capacity;
// content-sized images (read-only bundles) pass false.
func buildVHDImage(ctx context.Context, stageDir, tmpImage, outputPath string, sizeBytes int64, reserveFreeSpace bool) error {
	if err := os.MkdirAll(filepath.Dir(outputPath), 0o755); err != nil {
		return fmt.Errorf("create output dir: %w", err)
	}
	out, err := os.Create(tmpImage)
	if err != nil {
		return fmt.Errorf("create vhd image: %w", err)
	}
	defer func() { _ = out.Close() }()

	targetSizeBytes := int64(0)
	if reserveFreeSpace {
		targetSizeBytes = sizeBytes
	}
	reader, writer := io.Pipe()
	errCh := make(chan error, 1)
	go func() {
		tw := tar.NewWriter(writer)
		writeErr := writeImageTar(stageDir, tw, targetSizeBytes)
		closeErr := tw.Close()
		if writeErr == nil {
			writeErr = closeErr
		}
		if writeErr != nil {
			_ = writer.CloseWithError(writeErr)
			errCh <- writeErr
			return
		}
		errCh <- writer.Close()
	}()

	convertErr := tar2ext4.Convert(reader, out, tar2ext4.ConvertBackslash, tar2ext4.AppendVhdFooter, tar2ext4.MaximumDiskSize(sizeBytes))
	readCloseErr := reader.Close()
	writeErr := <-errCh
	if ctxErr := ctx.Err(); ctxErr != nil {
		return ctxErr
	}
	if convertErr != nil {
		return fmt.Errorf("build vhd image: %w", convertErr)
	}
	if readCloseErr != nil {
		return fmt.Errorf("close stage tar pipe: %w", readCloseErr)
	}
	if writeErr != nil {
		return fmt.Errorf("write stage tar: %w", writeErr)
	}
	if err := clearExt4ReadonlyFeature(out); err != nil {
		return fmt.Errorf("clear ext4 read-only feature: %w", err)
	}
	if err := out.Close(); err != nil {
		return fmt.Errorf("close vhd image: %w", err)
	}
	if err := os.Rename(tmpImage, outputPath); err != nil {
		return fmt.Errorf("commit vhd image: %w", err)
	}
	return nil
}
