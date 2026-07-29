package workspace

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/geoffbelknap/microagent/pkg/vmkit"
)

func TestSampleStatsRejectsPausedWorkspace(t *testing.T) {
	dir := t.TempDir()
	opts := Options{Name: "agent-1", StateDir: dir, Backend: vmkit.BackendLinuxKVM}
	req, err := Request(opts, "run", filepath.Join(dir, "rootfs.ext4"), "req-1")
	if err != nil {
		t.Fatal(err)
	}
	if err := WriteProcessState(opts, req, vmkit.StatePaused, 1234, ""); err != nil {
		t.Fatal(err)
	}
	_, err = SampleStats(dir, "agent-1")
	if err == nil || !strings.Contains(err.Error(), "paused; resume it first") {
		t.Fatalf("err = %v, want paused; resume it first", err)
	}
}

func TestSampleProcStatsParsesProcFiles(t *testing.T) {
	dir := t.TempDir()
	pid := 4242
	procDir := filepath.Join(dir, "4242")
	if err := os.MkdirAll(procDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// comm deliberately contains spaces and parentheses to exercise the
	// last-')' parse. After comm: utime is field index 11, stime index 12.
	stat := "4242 (fire cracker) S 1 4242 4242 0 -1 0 0 0 0 0 1500 700 0 0 20 0 2 0 100\n"
	if err := os.WriteFile(filepath.Join(procDir, "stat"), []byte(stat), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(procDir, "status"), []byte("Name:\tfc\nVmRSS:\t262144 kB\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(procDir, "io"), []byte("read_bytes: 1048576\nwrite_bytes: 524288\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	saved := procRoot
	procRoot = dir
	t.Cleanup(func() { procRoot = saved })

	ticks, err := readProcCPUTicks(pid)
	if err != nil {
		t.Fatalf("readProcCPUTicks: %v", err)
	}
	if ticks != 2200 {
		t.Fatalf("ticks = %d, want 2200 (utime 1500 + stime 700)", ticks)
	}

	rss, err := readProcRSSBytes(pid)
	if err != nil {
		t.Fatalf("readProcRSSBytes: %v", err)
	}
	if rss != 262144*1024 {
		t.Fatalf("rss = %d, want %d", rss, 262144*1024)
	}

	read, write := readProcIO(pid)
	if read != 1048576 || write != 524288 {
		t.Fatalf("io = %d/%d, want 1048576/524288", read, write)
	}

	stats, err := sampleProcStats(pid)
	if err != nil {
		t.Fatalf("sampleProcStats: %v", err)
	}
	if stats.PID != pid || stats.MemoryBytes != 262144*1024 {
		t.Fatalf("stats = %#v", stats)
	}
	if stats.IOReadBytes == nil || stats.IOWriteBytes == nil || *stats.IOReadBytes != 1048576 || *stats.IOWriteBytes != 524288 {
		t.Fatalf("io stats = %#v", stats)
	}
	// CPU% is 0 because both samples read the same static fixture.
	if stats.CPUPercent != 0 {
		t.Fatalf("cpu = %f, want 0 for static fixture", stats.CPUPercent)
	}
}

func TestParsePSCPUTime(t *testing.T) {
	for input, want := range map[string]float64{
		"0:00.12":    0.12,
		"1:02.50":    62.5,
		"12:34:56":   45296,
		"2-03:04:05": 183845,
	} {
		got, err := parsePSCPUTime(input)
		if err != nil {
			t.Fatalf("parsePSCPUTime(%q): %v", input, err)
		}
		if got != want {
			t.Fatalf("parsePSCPUTime(%q) = %f, want %f", input, got, want)
		}
	}
	for _, bad := range []string{"", "12", "a:b", "1:2:3:4"} {
		if _, err := parsePSCPUTime(bad); err == nil {
			t.Fatalf("parsePSCPUTime(%q): expected error", bad)
		}
	}
}

// samplePSStats runs against the real `ps` on any Unix host; use our own PID.
// The I/O counters must be absent (nil), never zero-valued fakes.
func TestSamplePSStatsOwnPID(t *testing.T) {
	stats, err := samplePSStats(os.Getpid())
	if err != nil {
		t.Fatalf("samplePSStats: %v", err)
	}
	if stats.PID != os.Getpid() || stats.MemoryBytes == 0 {
		t.Fatalf("stats = %#v", stats)
	}
	if stats.CPUPercent < 0 {
		t.Fatalf("cpu = %f", stats.CPUPercent)
	}
	if stats.IOReadBytes != nil || stats.IOWriteBytes != nil {
		t.Fatalf("io counters should be absent from the ps sampler: %#v", stats)
	}
}

func TestReadProcIOMissingIsZero(t *testing.T) {
	dir := t.TempDir()
	saved := procRoot
	procRoot = dir
	t.Cleanup(func() { procRoot = saved })
	read, write := readProcIO(999999)
	if read != 0 || write != 0 {
		t.Fatalf("missing io = %d/%d, want 0/0", read, write)
	}
}
