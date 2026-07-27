package diagnostics

import (
	"os"
	"path/filepath"
	"testing"
)

// TestDoctorNeverResolvesWhatTheSupervisorCannot pins the second half of the
// anchor fix, the half the first repair missed.
//
// Doctor originally anchored its firecracker lookup on the CLI binary while
// boots anchored on the supervisor — split layouts reported broken (false
// red). The repair anchored on the supervisor but kept this executable's own
// tree as a last resort, which mirrored the bug: firecracker present only in
// the CLI's tree made doctor report a boot path the supervisor cannot see —
// a false green, worse than the false red it replaced, because it points the
// operator away from the actual problem.
//
// The staging below reproduces that exact layout. This test binary stands in
// for the CLI: a firecracker is planted in ITS ../libexec tree, the
// supervisor sits in a bare tree with none, PATH is empty, no override. A
// resolver that consults the CLI tree finds the planted binary and reports
// healthy; the supervisor at boot would find nothing. Doctor must say what
// the supervisor will experience.
func TestDoctorNeverResolvesWhatTheSupervisorCannot(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Skipf("no executable path: %v", err)
	}
	cliLibexec := filepath.Clean(filepath.Join(filepath.Dir(exe), "..", "libexec"))
	if err := os.MkdirAll(cliLibexec, 0o755); err != nil {
		t.Skipf("cannot stage the CLI tree: %v", err)
	}
	planted := filepath.Join(cliLibexec, "firecracker")
	if err := os.WriteFile(planted, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Skipf("cannot plant: %v", err)
	}
	t.Cleanup(func() { _ = os.Remove(planted) })

	supervisor := filepath.Join(t.TempDir(), "bin", "microagent-firecracker-supervisor")
	if err := os.MkdirAll(filepath.Dir(supervisor), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(supervisor, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MICROAGENT_FIRECRACKER", "")
	t.Setenv("PATH", t.TempDir())

	got, err := ResolveFirecrackerPathFor(supervisor)
	if err == nil {
		t.Errorf("doctor resolved %q, which the supervisor's own resolution cannot see — the false-green divergence is back", got)
	}
}

// TestDoctorResolvesTheSupervisorTree is the control: the layout the anchor
// fix exists for still resolves — firecracker beside the supervisor, nothing
// on PATH.
func TestDoctorResolvesTheSupervisorTree(t *testing.T) {
	tree := t.TempDir()
	packaged := filepath.Join(tree, "libexec", "firecracker")
	supervisor := filepath.Join(tree, "bin", "microagent-firecracker-supervisor")
	for _, p := range []string{packaged, supervisor} {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("MICROAGENT_FIRECRACKER", "")
	t.Setenv("PATH", t.TempDir())

	got, err := ResolveFirecrackerPathFor(supervisor)
	if err != nil || got != packaged {
		t.Errorf("got %q, %v; want %q from the supervisor's tree", got, err, packaged)
	}
}
