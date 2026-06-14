//go:build !linux

package main

import (
	"os"
	"strings"
	"testing"
)

func TestSetupNetworkingUnsupportedOffLinux(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("asserts the non-root path")
	}
	f, _ := os.CreateTemp(t.TempDir(), "out")
	defer f.Close()
	err := runHostSetupNetworking(nil, f)
	if err == nil || !strings.Contains(err.Error(), "only supported on Linux") {
		t.Fatalf("err = %v, want unsupported-off-linux", err)
	}
}
