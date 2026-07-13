package workspace

import (
	"context"
	"os"
	"path/filepath"
	"testing"
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
