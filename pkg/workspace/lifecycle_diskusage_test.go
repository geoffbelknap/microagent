package workspace

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/geoffbelknap/microagent/pkg/vmkit"
)

// startableBackend returns the backend this platform's install can actually
// serve, so lifecycle-level tests don't trip the availability gate.
func startableBackend() string {
	if runtime.GOOS == "darwin" {
		return vmkit.BackendAppleVF
	}
	return vmkit.BackendLinuxKVM
}

// writeSyntheticExt4 writes just enough ext4 superblock for the host-side
// usage reader: 1000 4KiB blocks, 400 free.
func writeSyntheticExt4(t *testing.T, path string) {
	t.Helper()
	sb := make([]byte, 1024)
	binary.LittleEndian.PutUint16(sb[0x38:], 0xEF53)
	binary.LittleEndian.PutUint32(sb[0x18:], 2)
	binary.LittleEndian.PutUint32(sb[0x4:], 1000)
	binary.LittleEndian.PutUint32(sb[0xC:], 400)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteAt(sb, 1024); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestStatusReportsRootfsUsage(t *testing.T) {
	dir := t.TempDir()
	opts := Options{Name: "usage-ws", StateDir: dir, Backend: startableBackend()}
	writeSyntheticExt4(t, WorkspaceRootfsPath(dir, opts.Name, opts.Backend))
	event := EventFile{
		Identity:   vmkit.Identity{RuntimeID: opts.Name, Backend: opts.Backend},
		State:      vmkit.StateHalted,
		ObservedAt: time.Now().UTC().Format(time.RFC3339),
	}
	if err := writeJSONFile(filepath.Join(dir, opts.Name, "event.json"), event); err != nil {
		t.Fatalf("write event: %v", err)
	}

	resp, err := Status(opts)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	usage := resp.RootfsUsage
	if usage == nil {
		t.Fatal("Status response carries no rootfs usage")
	}
	// 1000 blocks of 4KiB = 3 MiB (floor), 600 used blocks = 2 MiB (floor).
	if usage.SizeMiB != 3 || usage.FSUsedMiB != 2 || usage.UsedPercent != 60 {
		t.Fatalf("usage = %+v, want size=3MiB used=2MiB percent=60", usage)
	}
	if usage.HostAllocatedMiB < 0 {
		t.Fatalf("host allocated = %d, want >= 0", usage.HostAllocatedMiB)
	}
}

func TestStatusToleratesMissingRootfs(t *testing.T) {
	dir := t.TempDir()
	opts := Options{Name: "usage-none", StateDir: dir, Backend: startableBackend()}
	event := EventFile{
		Identity:   vmkit.Identity{RuntimeID: opts.Name, Backend: opts.Backend},
		State:      vmkit.StateStopped,
		ObservedAt: time.Now().UTC().Format(time.RFC3339),
	}
	if err := writeJSONFile(filepath.Join(dir, opts.Name, "event.json"), event); err != nil {
		t.Fatalf("write event: %v", err)
	}
	resp, err := Status(opts)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if resp.RootfsUsage != nil {
		t.Fatalf("usage = %+v, want nil for a missing rootfs", resp.RootfsUsage)
	}
}
