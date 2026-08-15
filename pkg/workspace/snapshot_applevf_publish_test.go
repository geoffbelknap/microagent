//go:build darwin

package workspace

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/geoffbelknap/microagent/pkg/operation"
	"github.com/geoffbelknap/microagent/pkg/vmkit"
)

// The apple-vf snapshot host path must match the Firecracker capture flow:
// stage the capture, publish atomically, and never destroy an existing
// snapshot at the tag — re-snapshotting a tag succeeds (overwrite on success)
// and a failed re-capture leaves the prior snapshot intact.

// writeAppleVFRuntimeState persists a minimal running runtime.json so
// snapshotAppleVF's state checks pass without a live VM.
func writeAppleVFRuntimeState(t *testing.T, opts Options) {
	t.Helper()
	state := RuntimeState{
		Event: EventFile{
			Identity: vmkit.Identity{RequestID: "req-1", RuntimeID: opts.Name, Role: vmkit.RoleWorkload, Backend: vmkit.BackendAppleVF},
			State:    vmkit.StateRunning,
		},
		Config: vmkit.Config{StateDir: opts.StateDir},
	}
	data, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(opts.StateDir, opts.Name)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "runtime.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
}

// fakeSnapshotSupervisor returns a supervisor executable that captures fake
// snapshot artifacts into the request's snapshotStagingDir, writing a
// generation marker ("capture-<n>") as the rootfs so tests can tell which
// capture ended up published.
func fakeSnapshotSupervisor(t *testing.T, dir string) string {
	t.Helper()
	capture := `import json, os, sys
counter = sys.argv[1]
req = json.load(sys.stdin)
staging = req.get("snapshotStagingDir", "")
if not staging or not os.path.isdir(staging):
    print(json.dumps({"ok": False, "error": "missing snapshotStagingDir"}))
    sys.exit(0)
n = 1
if os.path.exists(counter):
    n = int(open(counter).read()) + 1
open(counter, "w").write(str(n))
open(os.path.join(staging, "rootfs.ext4"), "w").write("capture-%d" % n)
open(os.path.join(staging, "machine-state.vz"), "w").write("machine-state")
print(json.dumps({"ok": True}))
`
	capturePath := filepath.Join(dir, "fake-capture.py")
	if err := os.WriteFile(capturePath, []byte(capture), 0o600); err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\nexec python3 " + capturePath + " " + filepath.Join(dir, "generation") + "\n"
	path := filepath.Join(dir, "fake-supervisor")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func failingSnapshotSupervisor(t *testing.T, dir string) string {
	t.Helper()
	path := filepath.Join(dir, "failing-supervisor")
	if err := os.WriteFile(path, []byte("#!/bin/sh\necho capture failed >&2\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func appleVFSnapshotRootfs(t *testing.T, opts Options, tag string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(vmkit.SnapshotDir(opts.StateDir, opts.Name, tag), vmkit.SnapshotRootfsName))
	if err != nil {
		t.Fatalf("read snapshot rootfs: %v", err)
	}
	return string(b)
}

func TestSnapshotAppleVFOverwriteReplacesTagAtomically(t *testing.T) {
	dir := t.TempDir()
	opts := Options{
		Name:           "ws",
		Backend:        vmkit.BackendAppleVF,
		StateDir:       dir,
		SupervisorPath: fakeSnapshotSupervisor(t, dir),
	}
	writeAppleVFRuntimeState(t, opts)

	if _, err := Snapshot(context.Background(), opts, "base"); err != nil {
		t.Fatalf("first snapshot: %v", err)
	}
	if got := appleVFSnapshotRootfs(t, opts, "base"); got != "capture-1" {
		t.Fatalf("first snapshot rootfs = %q, want capture-1", got)
	}

	// Re-snapshotting the same tag succeeds and the new capture wins,
	// matching the Firecracker overwrite-on-success semantics.
	if _, err := Snapshot(context.Background(), opts, "base"); err != nil {
		t.Fatalf("re-snapshot of existing tag: %v", err)
	}
	if got := appleVFSnapshotRootfs(t, opts, "base"); got != "capture-2" {
		t.Fatalf("re-snapshot rootfs = %q, want capture-2 (second capture wins)", got)
	}
	if _, err := vmkit.ReadSnapshotManifest(vmkit.SnapshotDir(dir, "ws", "base")); err != nil {
		t.Fatalf("manifest after overwrite: %v", err)
	}
	assertNoStagingResidue(t, opts)
}

func TestSnapshotAppleVFFailedRecaptureKeepsPriorSnapshot(t *testing.T) {
	dir := t.TempDir()
	opts := Options{
		Name:           "ws",
		Backend:        vmkit.BackendAppleVF,
		StateDir:       dir,
		SupervisorPath: fakeSnapshotSupervisor(t, dir),
	}
	writeAppleVFRuntimeState(t, opts)

	if _, err := Snapshot(context.Background(), opts, "base"); err != nil {
		t.Fatalf("first snapshot: %v", err)
	}

	failing := opts
	failing.SupervisorPath = failingSnapshotSupervisor(t, dir)
	var phases []string
	failing.Progress = func(event operation.ProgressEvent) { phases = append(phases, event.Phase) }
	_, err := Snapshot(context.Background(), failing, "base")
	if err == nil {
		t.Fatal("re-snapshot with a failing capture succeeded; want error")
	}
	if !strings.Contains(err.Error(), "capture failed") {
		t.Fatalf("err = %q, want the supervisor's capture failure", err)
	}
	if !strings.Contains(err.Error(), "source workspace was resumed") {
		t.Fatalf("err = %q, want confirmed source recovery state", err)
	}
	wantPhases := []string{"snapshot_validate", "snapshot_pause", "snapshot_secret_purge", "snapshot_capture", "snapshot_source_state"}
	if got := strings.Join(phases, ","); got != strings.Join(wantPhases, ",") {
		t.Fatalf("failure phases = %s, want %s", got, strings.Join(wantPhases, ","))
	}

	// The prior good snapshot at the tag survives the failed re-capture.
	if got := appleVFSnapshotRootfs(t, opts, "base"); got != "capture-1" {
		t.Fatalf("rootfs after failed re-capture = %q, want capture-1 (prior snapshot must survive)", got)
	}
	if _, err := vmkit.ReadSnapshotManifest(vmkit.SnapshotDir(dir, "ws", "base")); err != nil {
		t.Fatalf("manifest after failed re-capture: %v", err)
	}
	assertNoStagingResidue(t, opts)
}

// assertNoStagingResidue checks the staging parent holds no leftover capture
// or superseded-backup directories once a snapshot attempt has concluded.
func assertNoStagingResidue(t *testing.T, opts Options) {
	t.Helper()
	entries, err := os.ReadDir(vmkit.SnapshotStagingParent(opts.StateDir, opts.Name))
	if err != nil {
		if os.IsNotExist(err) {
			return
		}
		t.Fatal(err)
	}
	for _, entry := range entries {
		t.Errorf("staging residue left behind: %s", entry.Name())
	}
}
