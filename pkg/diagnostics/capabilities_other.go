//go:build !linux

package diagnostics

// BinaryHasNetAdmin is a no-op on non-Linux platforms: file capabilities are a
// Linux concept and privileged Firecracker networking is Linux-only.
func BinaryHasNetAdmin(path string) (bool, error) {
	return false, nil
}
