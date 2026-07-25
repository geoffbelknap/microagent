package workspace

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/geoffbelknap/microagent/pkg/operation"
	"github.com/geoffbelknap/microagent/pkg/vmkit"
)

// procRoot is the procfs mount sampled for resource statistics. It is a package
// variable so tests can point it at a fixture directory.
var procRoot = "/proc"

// statsCPUSampleInterval is the delay between the two CPU-time samples used to
// derive a CPU percentage.
const statsCPUSampleInterval = 200 * time.Millisecond

// userHZ is the Linux clock-tick rate (USER_HZ) that /proc CPU times are
// reported in. It is 100 on effectively all Linux configurations.
const userHZ = 100

// Stats is a point-in-time resource sample for a running workspace, taken from
// the host view of the backing VM monitor process.
type Stats struct {
	PID          int     `json:"pid"`
	CPUPercent   float64 `json:"cpuPercent"`
	MemoryBytes  uint64  `json:"memoryBytes"`
	IOReadBytes  uint64  `json:"ioReadBytes"`
	IOWriteBytes uint64  `json:"ioWriteBytes"`
	SampledAt    string  `json:"sampledAt"`
}

// SampleStats returns a resource sample for a running workspace. CPUPercent is
// measured across a short interval and can exceed 100 for multi-vCPU workspaces.
func SampleStats(stateDir, name string) (Stats, error) {
	if err := ValidateName(name); err != nil {
		return Stats{}, err
	}
	state, pid, err := LatestStartState(stateDir, name)
	if err != nil {
		return Stats{}, err
	}
	if state == "" {
		return Stats{}, WorkspaceNotFoundError{Name: name}
	}
	if state == vmkit.StatePaused {
		return Stats{}, operation.New(operation.ErrorConflict, "workspace %s is paused; resume it first", name)
	}
	if state != vmkit.StateRunning {
		return Stats{}, operation.New(operation.ErrorConflict, "workspace %s is not running; stats are unavailable in state %s", name, state)
	}
	// windows-hyperv has no host guest PID (HCS owns the VM worker process);
	// its sample comes from HCS statistics properties instead.
	if runtimeState, stateErr := ReadRuntimeState(Options{StateDir: stateDir, Name: name}); stateErr == nil &&
		runtimeState.Event.Identity.Backend == vmkit.BackendWindowsHyperV {
		return sampleWindowsHyperVStats(stateDir, name)
	}
	if pid <= 0 {
		return Stats{}, fmt.Errorf("workspace %s has no recorded runtime PID", name)
	}
	return sampleHostStats(pid)
}

func sampleHostStats(pid int) (Stats, error) {
	if runtime.GOOS == "linux" {
		return sampleProcStats(pid)
	}
	return samplePSStats(pid)
}

func sampleProcStats(pid int) (Stats, error) {
	ticks0, err := readProcCPUTicks(pid)
	if err != nil {
		return Stats{}, err
	}
	wallStart := time.Now()
	time.Sleep(statsCPUSampleInterval)
	ticks1, err := readProcCPUTicks(pid)
	if err != nil {
		return Stats{}, err
	}
	elapsed := time.Since(wallStart).Seconds()
	cpu := 0.0
	if elapsed > 0 && ticks1 >= ticks0 {
		cpu = (float64(ticks1-ticks0) / userHZ) / elapsed * 100
	}
	rss, err := readProcRSSBytes(pid)
	if err != nil {
		return Stats{}, err
	}
	// I/O accounting can be unavailable (restricted /proc/<pid>/io); treat that
	// as zero rather than failing the whole sample.
	readBytes, writeBytes := readProcIO(pid)
	return Stats{
		PID:          pid,
		CPUPercent:   cpu,
		MemoryBytes:  rss,
		IOReadBytes:  readBytes,
		IOWriteBytes: writeBytes,
		SampledAt:    time.Now().UTC().Format(time.RFC3339),
	}, nil
}

// readProcCPUTicks returns utime+stime (in clock ticks) for a process. The comm
// field may contain spaces and parentheses, so fields are parsed after the last
// ')'.
func readProcCPUTicks(pid int) (uint64, error) {
	data, err := os.ReadFile(filepath.Join(procRoot, strconv.Itoa(pid), "stat"))
	if err != nil {
		return 0, err
	}
	content := string(data)
	close := strings.LastIndexByte(content, ')')
	if close < 0 {
		return 0, fmt.Errorf("malformed /proc/%d/stat", pid)
	}
	fields := strings.Fields(content[close+1:])
	// After comm, field indexes are 0=state ... 11=utime, 12=stime.
	if len(fields) < 13 {
		return 0, fmt.Errorf("unexpected /proc/%d/stat field count", pid)
	}
	utime, err := strconv.ParseUint(fields[11], 10, 64)
	if err != nil {
		return 0, err
	}
	stime, err := strconv.ParseUint(fields[12], 10, 64)
	if err != nil {
		return 0, err
	}
	return utime + stime, nil
}

func readProcRSSBytes(pid int) (uint64, error) {
	data, err := os.ReadFile(filepath.Join(procRoot, strconv.Itoa(pid), "status"))
	if err != nil {
		return 0, err
	}
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "VmRSS:") {
			fields := strings.Fields(line)
			if len(fields) >= 2 {
				kb, err := strconv.ParseUint(fields[1], 10, 64)
				if err != nil {
					return 0, err
				}
				return kb * 1024, nil
			}
		}
	}
	return 0, nil
}

func readProcIO(pid int) (read, write uint64) {
	data, err := os.ReadFile(filepath.Join(procRoot, strconv.Itoa(pid), "io"))
	if err != nil {
		return 0, 0
	}
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		switch fields[0] {
		case "read_bytes:":
			read, _ = strconv.ParseUint(fields[1], 10, 64)
		case "write_bytes:":
			write, _ = strconv.ParseUint(fields[1], 10, 64)
		}
	}
	return read, write
}

func samplePSStats(pid int) (Stats, error) {
	output, err := exec.Command("ps", "-o", "pcpu=", "-o", "rss=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return Stats{}, err
	}
	fields := strings.Fields(string(output))
	if len(fields) < 2 {
		return Stats{}, fmt.Errorf("unexpected ps stats output for pid %d", pid)
	}
	cpu, err := strconv.ParseFloat(fields[0], 64)
	if err != nil {
		return Stats{}, fmt.Errorf("parse ps cpu for pid %d: %w", pid, err)
	}
	rssKB, err := strconv.ParseUint(fields[1], 10, 64)
	if err != nil {
		return Stats{}, fmt.Errorf("parse ps rss for pid %d: %w", pid, err)
	}
	return Stats{
		PID:         pid,
		CPUPercent:  cpu,
		MemoryBytes: rssKB * 1024,
		SampledAt:   time.Now().UTC().Format(time.RFC3339),
	}, nil
}
