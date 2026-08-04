//go:build !linux && !darwin

package workspace

import "fmt"

// hostTotalMemoryMiB has no implementation on this platform; MaxWorkspaces
// falls back to fallbackMaxWorkspaces when it errors.
func hostTotalMemoryMiB() (int64, error) {
	return 0, fmt.Errorf("host memory detection is not implemented on this platform")
}
