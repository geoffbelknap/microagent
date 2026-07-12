package workspace

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReadSupervisorLogsReturnsPresentFilesSkipsMissing(t *testing.T) {
	dir := t.TempDir()
	wsDir := filepath.Join(dir, "ws")
	if err := os.MkdirAll(wsDir, 0o700); err != nil {
		t.Fatal(err)
	}
	// Two of the companion logs exist; the rest do not.
	if err := os.WriteFile(filepath.Join(wsDir, "vsock-listener.log"), []byte("broker listener failed"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wsDir, "user-network.stderr.log"), []byte("pasta up"), 0o600); err != nil {
		t.Fatal(err)
	}
	// A non-companion file must not be returned.
	if err := os.WriteFile(filepath.Join(wsDir, "serial.log"), []byte("guest boot"), 0o600); err != nil {
		t.Fatal(err)
	}

	logs, err := ReadSupervisorLogs(dir, "ws")
	if err != nil {
		t.Fatalf("ReadSupervisorLogs: %v", err)
	}
	if len(logs) != 2 {
		t.Fatalf("expected 2 companion logs, got %d: %v", len(logs), logs)
	}
	if logs["vsock-listener.log"] != "broker listener failed" {
		t.Errorf("vsock-listener.log = %q", logs["vsock-listener.log"])
	}
	if logs["user-network.stderr.log"] != "pasta up" {
		t.Errorf("user-network.stderr.log = %q", logs["user-network.stderr.log"])
	}
	if _, ok := logs["serial.log"]; ok {
		t.Errorf("serial.log must not be a supervisor companion log")
	}
}

func TestReadSupervisorLogsNoFilesReturnsEmpty(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "ws"), 0o700); err != nil {
		t.Fatal(err)
	}
	logs, err := ReadSupervisorLogs(dir, "ws")
	if err != nil {
		t.Fatalf("ReadSupervisorLogs on empty workspace: %v", err)
	}
	if len(logs) != 0 {
		t.Fatalf("expected no logs, got %v", logs)
	}
}

func TestReadSupervisorLogsRejectsInvalidName(t *testing.T) {
	if _, err := ReadSupervisorLogs(t.TempDir(), "../escape"); err == nil {
		t.Fatal("expected an error for an invalid workspace name")
	}
}
