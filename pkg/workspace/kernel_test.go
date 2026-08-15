package workspace

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/geoffbelknap/microagent/pkg/operation"
)

func stubKernelInstaller(t *testing.T) *[]string {
	t.Helper()
	var calls []string
	prev := defaultKernelInstaller
	defaultKernelInstaller = func(_ context.Context, _, _, outputPath string) error {
		calls = append(calls, outputPath)
		if err := os.MkdirAll(filepath.Dir(outputPath), 0o755); err != nil {
			return err
		}
		return os.WriteFile(outputPath, []byte("kernel"), 0o644)
	}
	t.Cleanup(func() { defaultKernelInstaller = prev })
	return &calls
}

func TestEnsureKernelForwardsProgressToBuiltInInstaller(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	previous := defaultProgressKernelInstaller
	defaultProgressKernelInstaller = func(_ context.Context, _, _, outputPath string, progress operation.ProgressFunc) error {
		progress(operation.ProgressEvent{Phase: "kernel_transfer"})
		if err := os.MkdirAll(filepath.Dir(outputPath), 0o755); err != nil {
			return err
		}
		return os.WriteFile(outputPath, []byte("kernel"), 0o644)
	}
	t.Cleanup(func() { defaultProgressKernelInstaller = previous })
	var phases []string
	opts := Options{Backend: "linux-kvm", Architecture: "arm64", Progress: func(event operation.ProgressEvent) {
		phases = append(phases, event.Phase)
	}}
	if err := EnsureKernel(context.Background(), &opts); err != nil {
		t.Fatal(err)
	}
	if len(phases) != 1 || phases[0] != "kernel_transfer" {
		t.Fatalf("progress phases = %#v", phases)
	}
}

func TestKernelInstallerRegistrationUsesLastRegistration(t *testing.T) {
	previous := defaultKernelInstaller
	previousProgress := defaultProgressKernelInstaller
	t.Cleanup(func() {
		defaultKernelInstaller = previous
		defaultProgressKernelInstaller = previousProgress
	})

	legacy := KernelInstaller(func(context.Context, string, string, string) error { return nil })
	progress := ProgressKernelInstaller(func(context.Context, string, string, string, operation.ProgressFunc) error { return nil })
	RegisterProgressKernelInstaller(progress)
	if defaultProgressKernelInstaller == nil || defaultKernelInstaller != nil {
		t.Fatal("progress registration did not replace the legacy installer")
	}
	RegisterKernelInstaller(legacy)
	if defaultKernelInstaller == nil || defaultProgressKernelInstaller != nil {
		t.Fatal("legacy registration did not replace the progress installer")
	}
}

func TestEnsureKernelInstallsMissingDefaultKernel(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	calls := stubKernelInstaller(t)

	opts := Options{Backend: "linux-kvm", Architecture: "arm64"}
	if err := EnsureKernel(context.Background(), &opts); err != nil {
		t.Fatal(err)
	}
	want := WritableKernelPath("linux-kvm", "arm64")
	if len(*calls) != 1 || (*calls)[0] != want {
		t.Fatalf("installer calls = %v, want one call for %q", *calls, want)
	}
	if opts.KernelPath != want {
		t.Fatalf("KernelPath = %q, want %q", opts.KernelPath, want)
	}

	// The kernel now exists; a second ensure must not reinstall it.
	if err := EnsureKernel(context.Background(), &opts); err != nil {
		t.Fatal(err)
	}
	if len(*calls) != 1 {
		t.Fatalf("installer ran again for an existing kernel: %v", *calls)
	}
}

func TestEnsureKernelLeavesExplicitKernelAlone(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	calls := stubKernelInstaller(t)

	opts := Options{
		Backend:        "linux-kvm",
		Architecture:   "arm64",
		KernelPath:     filepath.Join(home, "custom", "Image"),
		KernelExplicit: true,
	}
	if err := EnsureKernel(context.Background(), &opts); err != nil {
		t.Fatal(err)
	}
	if len(*calls) != 0 {
		t.Fatalf("installer ran for an explicit kernel path: %v", *calls)
	}
}

func TestEnsureKernelLeavesCustomPathAlone(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	calls := stubKernelInstaller(t)

	opts := Options{
		Backend:      "linux-kvm",
		Architecture: "arm64",
		KernelPath:   filepath.Join(home, "elsewhere", "Image"),
	}
	if err := EnsureKernel(context.Background(), &opts); err != nil {
		t.Fatal(err)
	}
	if len(*calls) != 0 {
		t.Fatalf("installer ran for a non-managed kernel path: %v", *calls)
	}
}
