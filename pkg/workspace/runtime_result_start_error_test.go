package workspace

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/geoffbelknap/microagent/pkg/vmkit"
)

// TestRuntimeResultCarriesStartError pins the mapping the `result` command
// depends on. GuestResult gained StartError, but ReadRuntimeResult built the
// RuntimeResult field-by-field and dropped it — so the never-ran diagnosis
// showed in `run` output and vanished from `result`, the command whose
// documentation promises it. A field-by-field mapping fails silently on every
// field it forgets; this test at least makes this one loud.
func TestRuntimeResultCarriesStartError(t *testing.T) {
	opts := DefaultOptions()
	opts.Name = "start-error-probe"
	opts.StateDir = t.TempDir()

	guest := GuestResult{
		StartedAt:  "2026-07-27T00:00:00Z",
		ExitedAt:   "2026-07-27T00:00:01Z",
		ExitCode:   127,
		Error:      "fork/exec /bin/sh: no such file or directory",
		StartError: "fork/exec /bin/sh: no such file or directory",
	}
	raw, err := json.Marshal(guest)
	if err != nil {
		t.Fatal(err)
	}
	path := ResultPath(opts.StateDir, opts.Name)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}

	res, err := ReadRuntimeResult(opts, vmkit.Identity{RuntimeID: opts.Name})
	if err != nil {
		t.Fatalf("ReadRuntimeResult: %v", err)
	}
	if res.StartError != guest.StartError {
		t.Errorf("StartError = %q, want the guest's diagnosis", res.StartError)
	}
	if res.ExitCode != 127 {
		t.Errorf("ExitCode = %d, want 127", res.ExitCode)
	}
}
