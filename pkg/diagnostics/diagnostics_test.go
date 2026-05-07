package diagnostics

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/geoffbelknap/microagent-kit/pkg/vmkit"
)

func TestCheckFirecrackerReportsHostSupport(t *testing.T) {
	resp, err := CheckFirecracker(
		Options{Backend: vmkit.BackendFirecracker, Arch: "amd64"},
		FirecrackerProbe{
			ResolveBinary: func() (string, error) { return "/usr/local/bin/firecracker", nil },
			Stat: func(path string) (os.FileInfo, error) {
				switch path {
				case "/dev/kvm", "/dev/vhost-vsock":
					return fakeFileInfo{name: filepath.Base(path)}, nil
				default:
					return nil, os.ErrNotExist
				}
			},
			BinaryVersion: func(string) string { return "Firecracker v1.15.1" },
		},
	)
	if err != nil {
		t.Fatalf("CheckFirecracker: %v", err)
	}
	if !resp.OK || resp.Host == nil || !resp.Host.KVMAvailable || !resp.Host.VsockAvailable {
		t.Fatalf("response = %#v", resp)
	}
}

func TestCheckFirecrackerReportsMissingSupport(t *testing.T) {
	resp, err := CheckFirecracker(
		Options{Backend: vmkit.BackendFirecracker, Arch: "amd64"},
		FirecrackerProbe{
			ResolveBinary: func() (string, error) { return "", fmt.Errorf("firecracker binary not found") },
			Stat:          func(string) (os.FileInfo, error) { return nil, os.ErrNotExist },
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
