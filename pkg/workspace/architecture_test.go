package workspace

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/geoffbelknap/microagent/pkg/operation"
)

func TestValidateArch(t *testing.T) {
	for _, arch := range []string{"arm64", "aarch64", "amd64", "x86_64", " X64 "} {
		t.Run(arch, func(t *testing.T) {
			if err := ValidateArch(arch); err != nil {
				t.Fatalf("ValidateArch(%q): %v", arch, err)
			}
		})
	}

	for _, arch := range []string{"", "riscv64", "../amd64", strings.Repeat("a", 128)} {
		t.Run("reject_"+arch, func(t *testing.T) {
			err := ValidateArch(arch)
			if !operation.IsKind(err, operation.ErrorValidation) {
				t.Fatalf("ValidateArch(%q) error = %#v, want typed validation error", arch, err)
			}
		})
	}
}

func TestArchitectureDerivedPathsRejectUnsafeValues(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	executable := filepath.Join(home, "bin", "microagent")
	unsafeArch := "../../../escaped"

	tests := map[string]string{
		"kernel":                KernelPath("linux-kvm", unsafeArch),
		"writable kernel":       WritableKernelPath("linux-kvm", unsafeArch),
		"packaged kernel":       PackagedKernelPathFromExecutable(executable, "linux-kvm", unsafeArch),
		"guest init":            GuestInitPath(unsafeArch),
		"guest init executable": GuestInitPathFromExecutable(executable, unsafeArch),
		"unsafe backend":        LegacyKernelPath("../linux-kvm"),
	}
	for name, got := range tests {
		if got != "" {
			t.Errorf("%s path = %q, want empty", name, got)
		}
	}

	want := filepath.Join(home, ".microagent", "kernels", "linux-kvm", "amd64", "Image")
	if got := WritableKernelPath("linux-kvm", "x86_64"); got != want {
		t.Fatalf("WritableKernelPath alias = %q, want %q", got, want)
	}
}
