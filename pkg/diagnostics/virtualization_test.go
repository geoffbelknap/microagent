package diagnostics

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/geoffbelknap/microagent/pkg/vmkit"
)

func TestCpuHasVirtualizationFlags(t *testing.T) {
	cases := []struct {
		name string
		data string
		want bool
	}{
		{"intel vmx", "processor\t: 0\nflags\t\t: fpu vme vmx lm\n", true},
		{"amd svm", "flags\t\t: fpu svm\n", true},
		{"arm features, no x86 virt flag", "Features\t: fp asimd\n", false},
		{"no virt flags", "flags\t\t: fpu vme lm\n", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := cpuHasVirtualizationFlags(func(string) ([]byte, error) { return []byte(tc.data), nil })
			if got != tc.want {
				t.Errorf("cpuHasVirtualizationFlags(%q) = %v, want %v", tc.data, got, tc.want)
			}
		})
	}
	if cpuHasVirtualizationFlags(func(string) ([]byte, error) { return nil, os.ErrNotExist }) {
		t.Error("unreadable /proc/cpuinfo should report false")
	}
	if cpuHasVirtualizationFlags(nil) {
		t.Error("nil readFile should report false")
	}
}

// TestCheckFirecrackerVirtualizationWithoutKVM proves VirtualizationSupported is
// an independent fact: the CPU advertising vmx makes it true even when /dev/kvm
// is absent, so doctor can distinguish "CPU can't virtualize" from "KVM not set up".
func TestCheckFirecrackerVirtualizationWithoutKVM(t *testing.T) {
	resp, _ := CheckFirecracker(
		Options{Backend: vmkit.BackendLinuxKVM, Arch: "amd64"},
		FirecrackerProbe{
			ResolveBinary:     func() (string, error) { return "/usr/local/bin/firecracker", nil },
			ResolveSupervisor: func(Options) (string, error) { return "/usr/local/bin/microagent-firecracker-supervisor", nil },
			ResolveGuestInit:  func(Options) (string, error) { return "/usr/local/libexec/microagent-guestinit-amd64", nil },
			Stat: func(path string) (os.FileInfo, error) {
				if path == "/dev/kvm" {
					return nil, os.ErrNotExist
				}
				return fakeFileInfo{name: filepath.Base(path)}, nil
			},
			BinaryVersion: func(string) string { return "Firecracker v1.15.1" },
			LookPath:      func(string) (string, error) { return "/usr/bin/pasta", nil },
			ReadFile: func(path string) ([]byte, error) {
				if path == "/proc/cpuinfo" {
					return []byte("flags\t\t: fpu vmx lm\n"), nil
				}
				return []byte("1\n"), nil
			},
			ProbeUserNamespaces: func() error { return nil },
		},
	)
	if resp.Host == nil {
		t.Fatal("nil host")
	}
	if resp.Host.KVMAvailable {
		t.Errorf("KVMAvailable = true, want false (/dev/kvm absent)")
	}
	if !resp.Host.VirtualizationSupported {
		t.Errorf("VirtualizationSupported = false, want true (CPU advertises vmx)")
	}
}
