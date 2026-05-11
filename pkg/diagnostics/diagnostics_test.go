package diagnostics

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/geoffbelknap/microagent/pkg/vmkit"
)

func TestCheckFirecrackerReportsHostSupport(t *testing.T) {
	resp, err := CheckFirecracker(
		Options{Backend: vmkit.BackendFirecracker, Arch: "amd64"},
		FirecrackerProbe{
			ResolveBinary:     func() (string, error) { return "/usr/local/bin/firecracker", nil },
			ResolveSupervisor: func(Options) (string, error) { return "/usr/local/bin/microagent-firecracker-supervisor", nil },
			ResolveGuestInit:  func(Options) (string, error) { return "/usr/local/libexec/microagent-guestinit-amd64", nil },
			Stat: func(path string) (os.FileInfo, error) {
				switch path {
				case "/dev/kvm", "/dev/vhost-vsock", "/dev/net/tun":
					return fakeFileInfo{name: filepath.Base(path)}, nil
				default:
					return nil, os.ErrNotExist
				}
			},
			BinaryVersion: func(string) string { return "Firecracker v1.15.1" },
			LookPath: func(name string) (string, error) {
				if name == "pasta" {
					return "/usr/bin/pasta", nil
				}
				return "", os.ErrNotExist
			},
			ReadFile: func(path string) ([]byte, error) {
				return []byte("1\n"), nil
			},
		},
	)
	if err != nil {
		t.Fatalf("CheckFirecracker: %v", err)
	}
	if !resp.OK || resp.Host == nil || !resp.Host.KVMAvailable || !resp.Host.VsockAvailable || !resp.Host.UserNetworkingAvailable {
		t.Fatalf("response = %#v", resp)
	}
	if !resp.Host.SupervisorAvailable || resp.Host.SupervisorPath != "/usr/local/bin/microagent-firecracker-supervisor" {
		t.Fatalf("supervisor support = %#v", resp.Host)
	}
	if !resp.Host.GuestInitAvailable || resp.Host.GuestInitPath != "/usr/local/libexec/microagent-guestinit-amd64" {
		t.Fatalf("guest init support = %#v", resp.Host)
	}
}

func TestCheckFirecrackerReportsMissingSupport(t *testing.T) {
	resp, err := CheckFirecracker(
		Options{Backend: vmkit.BackendFirecracker, Arch: "amd64"},
		FirecrackerProbe{
			ResolveBinary:     func() (string, error) { return "", fmt.Errorf("firecracker binary not found") },
			ResolveSupervisor: func(Options) (string, error) { return "", fmt.Errorf("microagent Firecracker supervisor not found") },
			ResolveGuestInit:  func(Options) (string, error) { return "", fmt.Errorf("microagent guest init not found") },
			Stat:              func(string) (os.FileInfo, error) { return nil, os.ErrNotExist },
			LookPath:          func(string) (string, error) { return "", os.ErrNotExist },
			ReadFile:          func(string) ([]byte, error) { return []byte("0\n"), nil },
		},
	)
	if err == nil {
		t.Fatal("CheckFirecracker returned nil error")
	}
	if resp.OK || resp.Host == nil || resp.Host.KVMAvailable {
		t.Fatalf("response = %#v", resp)
	}
	if resp.Error == "" {
		t.Fatal("missing error")
	}
	if !strings.Contains(resp.Error, "microagent Firecracker supervisor not found") || !strings.Contains(resp.Error, "microagent guest init not found") {
		t.Fatalf("error = %q", resp.Error)
	}
}

func TestCheckFirecrackerReportsMissingPasta(t *testing.T) {
	resp, err := CheckFirecracker(
		Options{Backend: vmkit.BackendFirecracker, Arch: "amd64"},
		FirecrackerProbe{
			ResolveBinary:     func() (string, error) { return "/usr/local/bin/firecracker", nil },
			ResolveSupervisor: func(Options) (string, error) { return "/usr/local/bin/microagent-firecracker-supervisor", nil },
			ResolveGuestInit:  func(Options) (string, error) { return "/usr/local/libexec/microagent-guestinit-amd64", nil },
			Stat: func(path string) (os.FileInfo, error) {
				return fakeFileInfo{name: filepath.Base(path)}, nil
			},
			LookPath: func(string) (string, error) { return "", os.ErrNotExist },
			ReadFile: func(string) ([]byte, error) { return []byte("1\n"), nil },
		},
	)
	if err == nil {
		t.Fatal("CheckFirecracker returned nil error")
	}
	if resp.Host == nil || resp.Host.UserNetworkingAvailable {
		t.Fatalf("response = %#v", resp)
	}
	if !strings.Contains(resp.Error, "apt install passt") {
		t.Fatalf("error = %q", resp.Error)
	}
}

type fakeFileInfo struct {
	name string
}

func (f fakeFileInfo) Name() string       { return f.name }
func (f fakeFileInfo) Size() int64        { return 0 }
func (f fakeFileInfo) Mode() os.FileMode  { return 0 }
func (f fakeFileInfo) ModTime() time.Time { return time.Time{} }
func (f fakeFileInfo) IsDir() bool        { return false }
func (f fakeFileInfo) Sys() any           { return nil }
