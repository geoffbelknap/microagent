//go:build windows

package rootfs

import (
	"errors"
	"os"
	"syscall"
)

func canFallbackToSymlinkMarker(err error) bool {
	if errors.Is(err, os.ErrPermission) {
		return true
	}
	return errors.Is(err, syscall.ERROR_PRIVILEGE_NOT_HELD)
}
