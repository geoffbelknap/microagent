//go:build linux

package diagnostics

import (
	"encoding/binary"
	"errors"

	"golang.org/x/sys/unix"
)

// capNetAdmin is the capability number for CAP_NET_ADMIN (caps 0-31 live in the
// first 32-bit word of the VFS capability data).
const capNetAdmin = 12

// BinaryHasNetAdmin reports whether the on-disk binary at path carries
// CAP_NET_ADMIN in its permitted file-capability set (as written by
// `setcap cap_net_admin+...`). A missing binary or missing capability xattr is
// reported as false with no error; only unexpected I/O errors are returned.
func BinaryHasNetAdmin(path string) (bool, error) {
	// vfs_cap_data is at most 24 bytes (revision 3 with rootid); 64 is ample.
	buf := make([]byte, 64)
	n, err := unix.Getxattr(path, "security.capability", buf)
	if err != nil {
		if errors.Is(err, unix.ENODATA) || errors.Is(err, unix.ENOENT) || errors.Is(err, unix.ENOTSUP) {
			return false, nil
		}
		return false, err
	}
	if n < 8 {
		return false, nil
	}
	// Layout (little-endian): magic_etc(u32), then data[]: {permitted(u32), inheritable(u32)} per 32-cap word.
	// CAP_NET_ADMIN is in the first word, so data[0].permitted is bytes [4,8).
	permitted := binary.LittleEndian.Uint32(buf[4:8])
	return permitted&(1<<uint(capNetAdmin)) != 0, nil
}
