package workspace

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/geoffbelknap/microagent/pkg/vmkit"
)

// stageForkSource writes a minimal but readable snapshot for source workspace
// "src" under a fresh state dir, so CreateFromSnapshot gets past its manifest
// read the way a real fork would.
func stageForkSource(t *testing.T) string {
	t.Helper()
	stateDir := t.TempDir()
	dir := vmkit.SnapshotDir(stateDir, "src", "tag")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := vmkit.WriteSnapshotManifest(dir, vmkit.SnapshotManifest{
		Tag:      "tag",
		ImageRef: "docker.io/library/python:3.13-slim",
	}); err != nil {
		t.Fatal(err)
	}
	return stateDir
}

// forkOptions is the smallest option set a fork needs, pointed at the staged
// state dir.
func forkOptions(stateDir string) Options {
	opts := DefaultOptions()
	opts.Name = "fork-target"
	opts.StateDir = stateDir
	opts.Backend = HostBackend()
	return opts
}

// TestForkDryRunPerformsNoFork pins DryRun on the snapshot-fork path.
//
// CreateFromSnapshot ignored the field entirely. Create and Run honored it, so
// the one adapter that could express both at once — MCP workspace.create,
// whose documentation explicitly offers dry_run "including snapshot forks" —
// performed the real fork when asked not to: rootfs copied, manifest written,
// snapshot duplicated, all from a request whose flag meant "don't do it".
func TestForkDryRunPerformsNoFork(t *testing.T) {
	stateDir := stageForkSource(t)
	opts := forkOptions(stateDir)
	opts.DryRun = true

	res, err := CreateFromSnapshot(context.Background(), opts, "src", "tag")
	if err != nil {
		t.Fatalf("dry-run fork: %v", err)
	}
	if res.Workspace != "fork-target" {
		t.Errorf("prepared result names %q, want the fork target", res.Workspace)
	}

	// Nothing may exist for the fork target: no rootfs, no manifest, no
	// duplicated snapshot. The staged source is the only thing on disk.
	for _, path := range []string{
		filepath.Dir(WorkspaceRootfsPath(stateDir, "fork-target", opts.Backend)),
		vmkit.SnapshotDir(stateDir, "fork-target", "tag"),
	} {
		if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
			t.Errorf("dry run left %s behind (err=%v)", path, statErr)
		}
	}
	if _, mErr := ReadManifest(stateDir, "fork-target"); mErr == nil {
		t.Error("dry run wrote the fork's workspace manifest")
	}
}

// TestForkDryRunStillValidates keeps the dry run honest about failure: a
// missing snapshot must be reported, not validated into silence. A dry run
// that says less than "the real command would fail here" is the shipped
// defect's mirror image.
func TestForkDryRunStillValidates(t *testing.T) {
	opts := forkOptions(t.TempDir()) // no staged snapshot at all
	opts.DryRun = true

	_, err := CreateFromSnapshot(context.Background(), opts, "src", "tag")
	if err == nil {
		t.Fatal("dry-run fork of a nonexistent snapshot succeeded")
	}
	if got := err.Error(); !strings.Contains(got, "not found") {
		t.Errorf("error does not report the missing snapshot: %v", err)
	}
}

// TestForkWithoutDryRunStillForks is the control: the new early return must
// trigger only on the flag, or every real fork becomes a no-op that reports
// success — a worse failure than the one being fixed.
func TestForkWithoutDryRunStillForks(t *testing.T) {
	stateDir := stageForkSource(t)
	// The real path continues into EnsureKernel and the rootfs copy; the
	// staged fixture has no rootfs artifact, so the fork must FAIL — but only
	// after passing the point where the dry run stops. Reaching a
	// side-effect-path error is the proof the flag gated nothing here.
	_, err := CreateFromSnapshot(context.Background(), forkOptions(stateDir), "src", "tag")
	if err == nil {
		t.Fatal("fork of a rootfs-less fixture succeeded; the fixture should not be bootable")
	}
	if strings.Contains(err.Error(), "not found for workspace") {
		t.Errorf("real fork stopped at validation; it never advanced past the dry-run point: %v", err)
	}
}
