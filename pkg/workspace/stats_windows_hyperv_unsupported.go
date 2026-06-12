//go:build !windows

package workspace

import "fmt"

func sampleWindowsHyperVStats(stateDir, name string) (Stats, error) {
	return Stats{}, fmt.Errorf("windows-hyperv stats are only supported on windows")
}
