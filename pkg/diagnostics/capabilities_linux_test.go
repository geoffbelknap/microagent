//go:build linux

package diagnostics

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestBinaryHasNetAdmin(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("setting file capabilities requires root")
	}
	if _, err := exec.LookPath("setcap"); err != nil {
		t.Skip("setcap not available")
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "fake-supervisor")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	// No caps yet.
	ok, err := BinaryHasNetAdmin(bin)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ok {
		t.Fatal("fresh binary should not report CAP_NET_ADMIN")
	}

	// Grant the cap and re-check.
	if out, err := exec.Command("setcap", "cap_net_admin+eip", bin).CombinedOutput(); err != nil {
		t.Fatalf("setcap failed: %v: %s", err, out)
	}
	ok, err = BinaryHasNetAdmin(bin)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ok {
		t.Fatal("binary with cap_net_admin+eip should report CAP_NET_ADMIN")
	}
}

func TestBinaryHasNetAdminMissingFileIsNotError(t *testing.T) {
	ok, err := BinaryHasNetAdmin(filepath.Join(t.TempDir(), "does-not-exist"))
	if err != nil {
		t.Fatalf("missing binary should not error, got %v", err)
	}
	if ok {
		t.Fatal("missing binary should report no capability")
	}
}
