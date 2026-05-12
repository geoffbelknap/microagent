//go:build !windows

package rootfs

func canFallbackToSymlinkMarker(error) bool {
	return false
}
