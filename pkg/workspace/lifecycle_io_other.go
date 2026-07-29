//go:build !linux && !darwin

package workspace

import "os"

// cloneFile is unavailable on this platform; CopyFile falls back to a byte
// copy.
func cloneFile(source, target string, mode os.FileMode) bool {
	return false
}
