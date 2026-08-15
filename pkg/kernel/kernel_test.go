package kernel

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/geoffbelknap/microagent/pkg/operation"
	"github.com/geoffbelknap/microagent/pkg/vmkit"
	"github.com/geoffbelknap/microagent/pkg/workspace"
)

func TestInstallFromPathAndVerify(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "source-kernel")
	if err := os.WriteFile(source, []byte("kernel bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(dir, "kernels", "Image")

	var events []operation.ProgressEvent
	installed, err := Install(t.Context(), InstallOptions{
		FromPath:     source,
		OutputPath:   target,
		Architecture: "amd64",
		Progress: func(event operation.ProgressEvent) {
			events = append(events, event)
		},
	})
	if err != nil {
		t.Fatalf("Install: %v", err)
	}
	if installed.Path != target || installed.SHA256 == "" {
		t.Fatalf("installed = %#v", installed)
	}
	verified, err := Verify(VerifyOptions{Path: target, SHA256: installed.SHA256})
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if !verified.OK || verified.Path != target {
		t.Fatalf("verified = %#v", verified)
	}
	assertKernelProgressOrder(t, events, "kernel_validate", "kernel_transfer", "kernel_verify", "kernel_publish", "kernel_installed")
	var transfer operation.ProgressEvent
	for _, event := range events {
		if event.Phase == "kernel_transfer" {
			transfer = event
		}
	}
	if transfer.Bytes != int64(len("kernel bytes")) || transfer.TotalBytes != int64(len("kernel bytes")) {
		t.Fatalf("transfer progress = %#v", transfer)
	}
}

func TestInstallPublicationFailureDoesNotReportInstalled(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "source-kernel")
	if err := os.WriteFile(source, []byte("kernel bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	previous := publishKernel
	publishKernel = func(string, string) error { return errors.New("publish failed") }
	t.Cleanup(func() { publishKernel = previous })
	var phases []string
	_, err := Install(t.Context(), InstallOptions{
		FromPath: source, OutputPath: filepath.Join(dir, "Image"), Architecture: "amd64",
		Progress: func(event operation.ProgressEvent) { phases = append(phases, event.Phase) },
	})
	if err == nil || !strings.Contains(err.Error(), "publish failed") {
		t.Fatalf("Install error = %v", err)
	}
	for _, phase := range phases {
		if phase == "kernel_installed" {
			t.Fatalf("publication failure reported installed: %#v", phases)
		}
	}
}

func assertKernelProgressOrder(t *testing.T, events []operation.ProgressEvent, phases ...string) {
	t.Helper()
	position := 0
	for _, event := range events {
		if position < len(phases) && event.Phase == phases[position] {
			position++
		}
	}
	if position != len(phases) {
		t.Fatalf("progress events = %#v, want ordered phases %#v", events, phases)
	}
}

func TestSupportReportsUnavailableWhenMissing(t *testing.T) {
	// With kernel sources now driven by the signed manifest (no local pins),
	// Support stays cheap and offline: a missing kernel is simply unavailable.
	support := SupportForPath(vmkit.BackendLinuxKVM, "amd64", filepath.Join(t.TempDir(), "missing"))
	if support.Status != "unavailable" {
		t.Fatalf("support = %#v", support)
	}
}

func TestInstallRejectsUnsafeArchitectureBeforeWriting(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "home")
	if err := os.MkdirAll(home, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	source := filepath.Join(root, "source-kernel")
	if err := os.WriteFile(source, []byte("kernel bytes"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := Install(t.Context(), InstallOptions{
		FromPath:     source,
		Backend:      workspace.HostBackend(),
		Architecture: "../../../escaped",
	})
	if !operation.IsKind(err, operation.ErrorValidation) {
		t.Fatalf("Install error = %#v, want typed validation error", err)
	}
	escaped := filepath.Join(home, "escaped", "Image")
	if _, statErr := os.Stat(escaped); !os.IsNotExist(statErr) {
		t.Fatalf("unsafe architecture wrote %q: %v", escaped, statErr)
	}
	if got := workspace.WritableKernelPath(workspace.HostBackend(), "../../../escaped"); got != "" {
		t.Fatalf("WritableKernelPath = %q, want empty", got)
	}
}

func TestVerifyRejectsUnsupportedArchitecture(t *testing.T) {
	path := filepath.Join(t.TempDir(), "Image")
	if err := os.WriteFile(path, []byte("kernel bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := Verify(VerifyOptions{
		Path:         path,
		Backend:      workspace.HostBackend(),
		Architecture: "riscv64",
	})
	if !operation.IsKind(err, operation.ErrorValidation) {
		t.Fatalf("Verify error = %#v, want typed validation error", err)
	}
}
