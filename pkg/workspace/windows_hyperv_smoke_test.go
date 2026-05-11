//go:build windows

package workspace

import (
	"context"
	"os"
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

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	opts := Options{
		Name:         "windows-hyperv-smoke",
		Backend:      vmkit.BackendWindowsHyperV,
		Architecture: "amd64",
		StateDir:     t.TempDir(),
		KernelPath:   kernelPath,
		ImageRef:     imageRef,
		ExecCommand:  "echo WINDOWS_HYPERV_SMOKE",
		Timeout:      2 * time.Minute,
		Keep:         true,
		MemoryMiB:    512,
		CPUCount:     2,
	}

	result, err := Run(ctx, opts)
	if err != nil {
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
