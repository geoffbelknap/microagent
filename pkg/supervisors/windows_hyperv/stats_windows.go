//go:build windows

package windows_hyperv

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/geoffbelknap/microagent/pkg/vmkit"
)

// statsCPUSampleInterval is the delay between the two processor-runtime
// samples used to derive a CPU percentage, matching the Linux sampler.
const statsCPUSampleInterval = 200 * time.Millisecond

// ComputeSystemStats is a point-in-time resource sample for a running
// windows-hyperv workspace, read from HCS statistics properties. There is no
// host guest PID on this backend: HCS owns the VM worker process.
type ComputeSystemStats struct {
	CPUPercent   float64
	MemoryBytes  uint64
	IOReadBytes  uint64
	IOWriteBytes uint64
}

// hcsStatistics mirrors the Statistics and Memory sections of the HCS v2
// properties document for a virtual machine. LinuxKernelDirect VMs report
// zeros for Statistics.Memory, so memory comes from the VM Memory property
// (page counts) instead.
type hcsStatistics struct {
	Statistics struct {
		Timestamp time.Time `json:"Timestamp"`
		Processor struct {
			TotalRuntime100ns  uint64 `json:"TotalRuntime100ns"`
			RuntimeUser100ns   uint64 `json:"RuntimeUser100ns"`
			RuntimeKernel100ns uint64 `json:"RuntimeKernel100ns"`
		} `json:"Processor"`
		Memory struct {
			MemoryUsageCommitBytes            uint64 `json:"MemoryUsageCommitBytes"`
			MemoryUsageCommitPeakBytes        uint64 `json:"MemoryUsageCommitPeakBytes"`
			MemoryUsagePrivateWorkingSetBytes uint64 `json:"MemoryUsagePrivateWorkingSetBytes"`
		} `json:"Memory"`
		Storage struct {
			ReadSizeBytes  uint64 `json:"ReadSizeBytes"`
			WriteSizeBytes uint64 `json:"WriteSizeBytes"`
		} `json:"Storage"`
	} `json:"Statistics"`
	Memory struct {
		VirtualMachineMemory struct {
			AssignedMemory uint64 `json:"AssignedMemory"`
		} `json:"VirtualMachineMemory"`
		VirtualNodes []struct {
			MemoryUsageInPages uint64 `json:"MemoryUsageInPages"`
		} `json:"VirtualNodes"`
	} `json:"Memory"`
}

const hcsMemoryPageBytes = 4096

func (s hcsStatistics) memoryBytes() uint64 {
	var pages uint64
	for _, node := range s.Memory.VirtualNodes {
		pages += node.MemoryUsageInPages
	}
	if pages == 0 {
		pages = s.Memory.VirtualMachineMemory.AssignedMemory
	}
	if pages != 0 {
		return pages * hcsMemoryPageBytes
	}
	if s.Statistics.Memory.MemoryUsagePrivateWorkingSetBytes != 0 {
		return s.Statistics.Memory.MemoryUsagePrivateWorkingSetBytes
	}
	return s.Statistics.Memory.MemoryUsageCommitBytes
}

// SampleComputeSystemStats samples HCS statistics for a running workspace.
// CPUPercent is measured across a short interval like the Linux /proc
// sampler and can exceed 100 for multi-vCPU workspaces.
func SampleComputeSystemStats(ctx context.Context, opts Options) (ComputeSystemStats, error) {
	req := vmkit.Request{
		Identity: &vmkit.Identity{RuntimeID: opts.Name, Backend: vmkit.BackendWindowsHyperV},
		Config:   &vmkit.Config{StateDir: opts.StateDir},
	}
	state, err := readRuntimeState(req)
	if err != nil {
		return ComputeSystemStats{}, err
	}
	if state.Event.State != vmkit.StateRunning {
		return ComputeSystemStats{}, fmt.Errorf("workspace %s is not running; stats are unavailable in state %s", opts.Name, state.Event.State)
	}
	if state.ComputeSystemID == "" {
		return ComputeSystemStats{}, fmt.Errorf("workspace %s has no recorded compute system", opts.Name)
	}
	client := newVMComputeClient()
	first, err := queryComputeSystemStatistics(ctx, client, state.ComputeSystemID)
	if err != nil {
		return ComputeSystemStats{}, err
	}
	wallStart := time.Now()
	timer := time.NewTimer(statsCPUSampleInterval)
	select {
	case <-ctx.Done():
		timer.Stop()
		return ComputeSystemStats{}, ctx.Err()
	case <-timer.C:
	}
	second, err := queryComputeSystemStatistics(ctx, client, state.ComputeSystemID)
	if err != nil {
		return ComputeSystemStats{}, err
	}
	elapsed := time.Since(wallStart)
	cpu := 0.0
	if elapsed > 0 && second.Statistics.Processor.TotalRuntime100ns >= first.Statistics.Processor.TotalRuntime100ns {
		delta100ns := float64(second.Statistics.Processor.TotalRuntime100ns - first.Statistics.Processor.TotalRuntime100ns)
		cpu = delta100ns / float64(elapsed.Nanoseconds()/100) * 100
	}
	return ComputeSystemStats{
		CPUPercent:   cpu,
		MemoryBytes:  second.memoryBytes(),
		IOReadBytes:  second.Statistics.Storage.ReadSizeBytes,
		IOWriteBytes: second.Statistics.Storage.WriteSizeBytes,
	}, nil
}

func queryComputeSystemStatistics(ctx context.Context, client hcsClient, id string) (hcsStatistics, error) {
	raw, err := client.GetComputeSystemStatistics(ctx, id)
	if err != nil {
		return hcsStatistics{}, err
	}
	var decoded hcsStatistics
	if err := json.Unmarshal([]byte(raw), &decoded); err != nil {
		snippet := raw
		if len(snippet) > 200 {
			snippet = snippet[:200]
		}
		return hcsStatistics{}, fmt.Errorf("parse HCS statistics: %w (properties: %s)", err, snippet)
	}
	return decoded, nil
}
