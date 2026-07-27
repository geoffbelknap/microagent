package workspace

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/geoffbelknap/microagent/pkg/operation"
	"github.com/geoffbelknap/microagent/pkg/vmkit"
)

// countPaths reports how many entries exist under root, so a test can assert a
// call wrote nothing.
func countPaths(t *testing.T, root string) int {
	t.Helper()
	n := 0
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if path != root {
			n++
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
	return n
}

func dryRunOptions(t *testing.T) Options {
	t.Helper()
	return Options{
		Name:        "dry",
		StateDir:    t.TempDir(),
		Backend:     vmkit.BackendLinuxKVM,
		ImageRef:    "docker.io/library/alpine:3.20",
		ExecCommand: "echo hi",
	}
}

// TestRunDryRunWritesNothing is the regression test for a documented safety flag
// that performed the real operation. DryRun is an Options field three adapters
// can set, each adapter implemented it separately, and run never got a copy —
// so `run --dry-run` parsed the flag, discarded it, and booted a VM.
func TestRunDryRunWritesNothing(t *testing.T) {
	opts := dryRunOptions(t)
	opts.DryRun = true

	before := countPaths(t, opts.StateDir)
	result, err := Run(t.Context(), opts)
	if err != nil {
		t.Fatalf("Run dry run: %v", err)
	}

	if got := countPaths(t, opts.StateDir); got != before {
		t.Errorf("dry run wrote %d new paths under the state dir; it must write nothing", got-before)
	}
	if state := result.Response.Event.State; state != vmkit.StatePrepared {
		t.Errorf("state = %q, want %q", state, vmkit.StatePrepared)
	}
	if result.SerialLog != "" || result.FinalState != "" {
		t.Errorf("dry run reported runtime output (serial=%d bytes, final=%q); nothing ran",
			len(result.SerialLog), result.FinalState)
	}
}

// TestCreateDryRunWritesNothing pins the same property for create, which had a
// working implementation in one adapter. Both entry points now share one.
func TestCreateDryRunWritesNothing(t *testing.T) {
	opts := dryRunOptions(t)
	opts.DryRun = true

	before := countPaths(t, opts.StateDir)
	result, err := Create(t.Context(), opts)
	if err != nil {
		t.Fatalf("Create dry run: %v", err)
	}

	if got := countPaths(t, opts.StateDir); got != before {
		t.Errorf("dry run wrote %d new paths under the state dir; it must write nothing", got-before)
	}
	if state := result.Response.Event.State; state != vmkit.StatePrepared {
		t.Errorf("state = %q, want %q", state, vmkit.StatePrepared)
	}
}

// TestDryRunReportsTheResolvedCommand covers the gap that made a dry run hard
// to trust: it validated a configuration without showing what would execute,
// which matters most when more than one input can set the command.
func TestDryRunReportsTheResolvedCommand(t *testing.T) {
	opts := dryRunOptions(t)
	opts.DryRun = true

	result, err := Run(t.Context(), opts)
	if err != nil {
		t.Fatalf("Run dry run: %v", err)
	}
	if !strings.Contains(result.GuestCommand, "echo hi") {
		t.Errorf("GuestCommand = %q, want it to report the exec command", result.GuestCommand)
	}

	imageOpts := dryRunOptions(t)
	imageOpts.DryRun = true
	imageOpts.ExecCommand = ""
	imageOpts.UseImageCommand = true
	imageResult, err := Run(t.Context(), imageOpts)
	if err != nil {
		t.Fatalf("Run dry run with image command: %v", err)
	}
	if imageResult.GuestCommand == "" {
		t.Error("GuestCommand is empty for an image-command workspace; it should name the source")
	}
}

// TestGuestCommandInputConflicts pins which combinations are rejected and,
// just as importantly, which are not.
//
// The CLI already rejected two of these before reaching the library, but MCP
// and direct library callers do not go through that path — so the check has to
// live here to cover every caller.
func TestGuestCommandInputConflicts(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*Options)
		wantErr bool
	}{
		{
			name:    "image command with exec command",
			mutate:  func(o *Options) { o.UseImageCommand = true; o.ExecCommand = "echo hi" },
			wantErr: true,
		},
		{
			name:    "image command with service command",
			mutate:  func(o *Options) { o.UseImageCommand = true; o.ExecCommand = ""; o.ServiceCommand = "sleep 1" },
			wantErr: true,
		},
		{
			// A setup/exec boot followed by a managed service is a supported
			// pattern, not a conflict.
			name:    "service command with exec command",
			mutate:  func(o *Options) { o.ServiceCommand = "sleep 1"; o.ExecCommand = "echo setup" },
			wantErr: false,
		},
		{
			// Setup commands run before the exec command by design.
			name:    "setup commands with exec command",
			mutate:  func(o *Options) { o.SetupCommands = []string{"echo s"}; o.ExecCommand = "echo e" },
			wantErr: false,
		},
		{
			name:    "image command alone",
			mutate:  func(o *Options) { o.UseImageCommand = true; o.ExecCommand = "" },
			wantErr: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			opts := dryRunOptions(t)
			tt.mutate(&opts)

			err := validateGuestCommandInputs(opts)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected a conflict, got nil")
				}
				if !operation.IsKind(err, operation.ErrorConflict) {
					t.Errorf("error kind is not conflict: %v", err)
				}
				return
			}
			if err != nil {
				t.Errorf("unexpected conflict for a supported combination: %v", err)
			}
		})
	}
}

// TestRunRejectsConflictingInputsBeforeActing checks the conflict is caught on
// the real entry point, not only in isolation, and without writing anything.
func TestRunRejectsConflictingInputsBeforeActing(t *testing.T) {
	opts := dryRunOptions(t)
	opts.UseImageCommand = true // together with the exec command from the helper

	before := countPaths(t, opts.StateDir)
	if _, err := Run(t.Context(), opts); err == nil {
		t.Fatal("Run accepted conflicting guest-command inputs")
	} else if !operation.IsKind(err, operation.ErrorConflict) {
		t.Errorf("error kind is not conflict: %v", err)
	}
	if got := countPaths(t, opts.StateDir); got != before {
		t.Errorf("rejected call wrote %d new paths; it must fail before acting", got-before)
	}
}
