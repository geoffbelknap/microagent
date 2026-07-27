package diagnostics

import (
	"os"
	"path/filepath"
	"testing"
)

// stageSupervisorTree builds a packaged layout — bin/<supervisor> beside
// libexec/firecracker — and returns the supervisor path. This is the shape the
// Homebrew install and the release tarball both use.
func stageSupervisorTree(t *testing.T) (supervisorPath, firecrackerPath string) {
	t.Helper()
	root := t.TempDir()
	binDir := filepath.Join(root, "bin")
	libexecDir := filepath.Join(root, "libexec")
	for _, dir := range []string{binDir, libexecDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	supervisorPath = filepath.Join(binDir, "microagent-firecracker-supervisor")
	firecrackerPath = filepath.Join(libexecDir, "firecracker")
	for _, path := range []string{supervisorPath, firecrackerPath} {
		if err := os.WriteFile(path, []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return supervisorPath, firecrackerPath
}

// isolateFirecrackerLookup removes the two resolution paths that would find a
// VMM before the packaged lookup runs, so a test observes the anchor itself.
func isolateFirecrackerLookup(t *testing.T) {
	t.Helper()
	t.Setenv("MICROAGENT_FIRECRACKER", "")
	t.Setenv("PATH", t.TempDir())
}

// TestResolveAnchorsOnSupervisorNotThisProcess is the regression test for a
// doctor that contradicted reality. The packaged VMM lives at
// ../libexec/firecracker relative to the binary that launches it — the
// supervisor — but the probe anchored on os.Executable(), this process. With a
// CLI and supervisor installed in separate trees, `run` booted through the
// supervisor's tree while `doctor` reported the VMM missing and marked
// pause/resume and all three snapshot capabilities unavailable.
func TestResolveAnchorsOnSupervisorNotThisProcess(t *testing.T) {
	isolateFirecrackerLookup(t)
	supervisorPath, firecrackerPath := stageSupervisorTree(t)

	got, err := ResolveFirecrackerPathFor(supervisorPath)
	if err != nil {
		t.Fatalf("ResolveFirecrackerPathFor(%q) = %v, want the supervisor-adjacent VMM", supervisorPath, err)
	}
	if got != firecrackerPath {
		t.Errorf("resolved %q, want %q", got, firecrackerPath)
	}
}

// TestResolveWithoutAnchorDoesNotFindSupervisorTree is the control. Without the
// supervisor as an anchor the same layout must not resolve, which is what
// proves the anchor did the work rather than some other fallback.
func TestResolveWithoutAnchorDoesNotFindSupervisorTree(t *testing.T) {
	isolateFirecrackerLookup(t)
	stageSupervisorTree(t)

	if path, err := ResolveFirecrackerPathFor(""); err == nil {
		t.Errorf("resolved %q with no anchor; the supervisor tree must not be reachable without it", path)
	}
}

// TestResolveHonoursEnvironmentOverride keeps the precedence explicit:
// MICROAGENT_FIRECRACKER wins over any anchored lookup, so an operator can
// always point the probe at a specific binary.
func TestResolveHonoursEnvironmentOverride(t *testing.T) {
	isolateFirecrackerLookup(t)
	supervisorPath, _ := stageSupervisorTree(t)

	override := filepath.Join(t.TempDir(), "firecracker")
	if err := os.WriteFile(override, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MICROAGENT_FIRECRACKER", override)

	got, err := ResolveFirecrackerPathFor(supervisorPath)
	if err != nil {
		t.Fatalf("ResolveFirecrackerPathFor = %v, want the override", err)
	}
	if got != override {
		t.Errorf("resolved %q, want the MICROAGENT_FIRECRACKER override %q", got, override)
	}
}
