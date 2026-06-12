package workspace

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/geoffbelknap/microagent/pkg/vmkit"
)

func TestNormalizeGuestLayerTar(t *testing.T) {
	var raw bytes.Buffer
	tw := tar.NewWriter(&raw)
	if err := tw.WriteHeader(&tar.Header{Name: "./bin/", Typeflag: tar.TypeDir, Mode: 0o755, ModTime: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if err := tw.WriteHeader(&tar.Header{Name: "./bin/busybox", Typeflag: tar.TypeReg, Mode: 0o755, Size: 5, ModTime: time.Now(), Uid: 1000, Gid: 1000}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write([]byte("hello")); err != nil {
		t.Fatal(err)
	}
	if err := tw.WriteHeader(&tar.Header{Name: "./bin/sh", Typeflag: tar.TypeLink, Linkname: "./bin/busybox", Mode: 0o755, ModTime: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if err := tw.WriteHeader(&tar.Header{Name: "./dev/null", Typeflag: tar.TypeChar, Mode: 0o666, ModTime: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}

	normalized, err := normalizeGuestLayerTar(raw.Bytes())
	if err != nil {
		t.Fatalf("normalizeGuestLayerTar: %v", err)
	}
	reader := tar.NewReader(bytes.NewReader(normalized))
	var entries []*tar.Header
	for {
		header, err := reader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		copied := *header
		entries = append(entries, &copied)
	}
	if len(entries) != 3 {
		t.Fatalf("entries = %d, want 3 (device dropped)", len(entries))
	}
	if entries[0].Name != "bin/" || entries[1].Name != "bin/busybox" || entries[2].Name != "bin/sh" {
		t.Fatalf("names = %q %q %q, want cleaned relative paths in guest order", entries[0].Name, entries[1].Name, entries[2].Name)
	}
	if entries[2].Linkname != "bin/busybox" {
		t.Fatalf("hard link target = %q, want cleaned", entries[2].Linkname)
	}
	for _, entry := range entries {
		if !entry.ModTime.Equal(time.Unix(0, 0).UTC()) || entry.Uid != 0 || entry.Gid != 0 {
			t.Fatalf("entry %q not normalized: %+v", entry.Name, entry)
		}
	}

	var unsafe bytes.Buffer
	tw = tar.NewWriter(&unsafe)
	if err := tw.WriteHeader(&tar.Header{Name: "../escape", Typeflag: tar.TypeReg, Mode: 0o644, Size: 0}); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := normalizeGuestLayerTar(unsafe.Bytes()); err == nil {
		t.Fatal("traversal path must be rejected")
	}
}

func TestWithMaintenanceBootShapesOptionsAndAlwaysHalts(t *testing.T) {
	dir := t.TempDir()
	workspaceDir := filepath.Join(dir, "workspaces", "demo")
	if err := os.MkdirAll(workspaceDir, 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := `{"name":"demo","restart":"never","resources":{"memory_mib":256,"cpu_count":1},` +
		`"network":{"mode":"user"},"secrets":[{"name":"API","ref":"env:X"}],"model":"hf.co/o/r@main/m.gguf"}`
	if err := os.WriteFile(filepath.Join(workspaceDir, "workspace.json"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}

	oldStart, oldControl := startMaintenanceHook, controlMaintenanceHook
	t.Cleanup(func() { startMaintenanceHook, controlMaintenanceHook = oldStart, oldControl })
	var started Options
	startMaintenanceHook = func(ctx context.Context, opts Options) error {
		started = opts
		return nil
	}
	halts := 0
	controlMaintenanceHook = func(ctx context.Context, opts Options, command string) error {
		if command != "halt" {
			t.Fatalf("control command = %q, want halt", command)
		}
		halts++
		return nil
	}

	fnErr := errors.New("operation failed")
	err := withMaintenanceBoot(context.Background(), dir, "demo", func(Options) error { return fnErr })
	if !errors.Is(err, fnErr) {
		t.Fatalf("err = %v, want the operation error", err)
	}
	if halts != 1 {
		t.Fatalf("halts = %d, want the workspace halted even when the operation fails", halts)
	}
	if !started.MaintenanceBoot {
		t.Fatal("maintenance boot flag not set")
	}
	if started.Network.Mode != "isolated" {
		t.Fatalf("network mode = %q, want isolated (no HNS requirement)", started.Network.Mode)
	}
	if len(started.Secrets) != 0 || started.Model != "" || started.ResultPort != 0 {
		t.Fatalf("maintenance options carry workload config: %+v", started)
	}
	if vmkit.BackendCapabilities(started.Backend).GuestMediatedCopy != guestMediatedCopyEnabled() {
		t.Fatalf("backend = %q does not match host capability", started.Backend)
	}
}

func TestGuestPathForEndpointMapsDiskMountpoints(t *testing.T) {
	dir := t.TempDir()
	workspaceDir := filepath.Join(dir, "workspaces", "demo")
	if err := os.MkdirAll(workspaceDir, 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := `{"name":"demo","restart":"never","resources":{"memory_mib":256,"cpu_count":1},` +
		`"disks":[{"name":"data","path":"C:/x/data.vhd","mountpoint":"/data/","mode":"rw"}]}`
	if err := os.WriteFile(filepath.Join(workspaceDir, "workspace.json"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := guestPathForEndpoint(dir, remoteCopyEndpoint{Workspace: "demo", Disk: "rootfs", Path: "/etc/conf"})
	if err != nil || got != "/etc/conf" {
		t.Fatalf("rootfs path = %q, %v", got, err)
	}
	got, err = guestPathForEndpoint(dir, remoteCopyEndpoint{Workspace: "demo", Disk: "data", Path: "/out.json"})
	if err != nil || got != "/data/out.json" {
		t.Fatalf("disk path = %q, %v", got, err)
	}
	if _, err := guestPathForEndpoint(dir, remoteCopyEndpoint{Workspace: "demo", Disk: "missing", Path: "/x"}); err == nil ||
		!strings.Contains(err.Error(), "no disk") {
		t.Fatalf("missing disk err = %v", err)
	}
}
