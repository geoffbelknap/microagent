//go:build windows

package workspace

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/geoffbelknap/microagent/pkg/vmkit"
)

func TestWindowsHyperVSmokeRunResult(t *testing.T) {
	if os.Getenv("MICROAGENT_WINDOWS_HYPERV_SMOKE") != "1" {
		t.Skip("set MICROAGENT_WINDOWS_HYPERV_SMOKE=1 to run the Windows Hyper-V smoke test")
	}
	kernelPath := strings.TrimSpace(os.Getenv("MICROAGENT_WINDOWS_HYPERV_KERNEL"))
	if kernelPath == "" {
		t.Fatal("MICROAGENT_WINDOWS_HYPERV_KERNEL is required")
	}
	imageRef := strings.TrimSpace(os.Getenv("MICROAGENT_WINDOWS_HYPERV_IMAGE"))
	if imageRef == "" {
		imageRef = "docker.io/library/busybox:1.36"
	}
	guestInitPath := strings.TrimSpace(os.Getenv("MICROAGENT_WINDOWS_HYPERV_GUESTINIT"))
	if guestInitPath == "" {
		guestInitPath = filepath.Join("..", "..", ".build", "dev", "microagent-guestinit-amd64")
	}
	if _, err := os.Stat(guestInitPath); err != nil {
		t.Fatalf("guest init %q: %v", guestInitPath, err)
	}
	stateDir := strings.TrimSpace(os.Getenv("MICROAGENT_WINDOWS_HYPERV_STATE_DIR"))
	if stateDir == "" {
		var err error
		stateDir, err = os.MkdirTemp("", "microagent-windows-hyperv-smoke-*")
		if err != nil {
			t.Fatal(err)
		}
	} else if err := os.MkdirAll(stateDir, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Logf("state dir: %s", stateDir)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	opts := Options{
		Name:          "windows-hyperv-smoke",
		Backend:       vmkit.BackendWindowsHyperV,
		Architecture:  "amd64",
		StateDir:      stateDir,
		KernelPath:    kernelPath,
		GuestInitPath: guestInitPath,
		ImageRef:      imageRef,
		ExecCommand:   "echo WINDOWS_HYPERV_SMOKE",
		Timeout:       2 * time.Minute,
		Keep:          true,
		MemoryMiB:     512,
		CPUCount:      2,
	}

	result, err := Run(ctx, opts)
	if err != nil {
		if data, readErr := os.ReadFile(SerialLogPath(opts.StateDir, opts.Name)); readErr == nil {
			t.Logf("serial.log:\n%s", string(data))
		}
		t.Fatalf("Run: %v\nresponse=%#v", err, result.Response)
	}
	if result.Response.Event == nil || result.Response.Event.State != vmkit.StateStopped {
		t.Fatalf("final response = %#v", result.Response)
	}
	if result.Response.Result == nil || !strings.Contains(result.Response.Result.Stdout, "WINDOWS_HYPERV_SMOKE") {
		t.Fatalf("runtime result = %#v", result.Response.Result)
	}
	if _, err := os.Stat(ResultPath(opts.StateDir, opts.Name)); err != nil {
		t.Fatalf("result.json: %v", err)
	}
	if _, err := os.Stat(SerialLogPath(opts.StateDir, opts.Name)); err != nil {
		t.Fatalf("serial.log: %v", err)
	}
}
