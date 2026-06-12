//go:build windows

package workspace

import (
	"context"
	"time"

	windowshyperv "github.com/geoffbelknap/microagent/pkg/supervisors/windows_hyperv"
)

// sampleWindowsHyperVStats reads the workspace's HCS statistics properties.
func sampleWindowsHyperVStats(stateDir, name string) (Stats, error) {
	sample, err := windowshyperv.SampleComputeSystemStats(context.Background(), windowshyperv.Options{
		Name:     name,
		StateDir: stateDir,
	})
	if err != nil {
		return Stats{}, err
	}
	return Stats{
		CPUPercent:   sample.CPUPercent,
		MemoryBytes:  sample.MemoryBytes,
		IOReadBytes:  sample.IOReadBytes,
		IOWriteBytes: sample.IOWriteBytes,
		SampledAt:    time.Now().UTC().Format(time.RFC3339),
	}, nil
}
