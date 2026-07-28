package workspace

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/geoffbelknap/microagent/pkg/rootfs"
)

// TestBuildRootfsReusesBaselineForPlainWorkspace is the B19 restoration guard: a
// plain workspace clones a pulled/tagged baseline rootfs (no pull/rebuild) when
// the injected resolver offers one.
func TestBuildRootfsReusesBaselineForPlainWorkspace(t *testing.T) {
	dir := t.TempDir()
	baseline := filepath.Join(dir, "baseline.ext4")
	if err := os.WriteFile(baseline, []byte("BASELINE-ROOTFS"), 0o644); err != nil {
		t.Fatal(err)
	}
	name := "plain"
	rootfsPath := WorkspaceRootfsPath(dir, name, HostBackend())
	if err := os.MkdirAll(filepath.Dir(rootfsPath), 0o755); err != nil {
		t.Fatal(err)
	}

	called := false
	opts := Options{
		Name: name, StateDir: dir, Backend: HostBackend(),
		ImageRef: "microagent-baseline-test.invalid/alpine:3.20", Architecture: "amd64",
		PrepareForStart: true,
		RootfsBaseline: func(rp string) (string, rootfs.Provenance, bool) {
			called = true
			return baseline, rootfs.Provenance{ImageRef: "microagent-baseline-test.invalid/alpine:3.20", OutputPath: rp, BuilderPhase: "copy-baseline", SizeBytes: 1024 * 1024 * 1024}, true
		},
	}
	result, err := BuildRootfs(context.Background(), opts)
	if err != nil {
		t.Fatalf("BuildRootfs: %v", err)
	}
	if !called {
		t.Fatal("RootfsBaseline resolver was not consulted for a plain workspace")
	}
	got, err := os.ReadFile(rootfsPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "BASELINE-ROOTFS" {
		preview := got
		if len(preview) > 64 {
			preview = preview[:64]
		}
		t.Fatalf("cloned rootfs = %q… (%d bytes), want the baseline content (rootfs was rebuilt?)", preview, len(got))
	}
	if result.Image.BuilderPhase != "copy-baseline" {
		t.Fatalf("result Image = %+v, want the baseline provenance", result.Image)
	}
}

// TestCanReuseRootfsBaseline guards the predicate: a plain prepare-for-start
// workspace is reusable; anything that bakes workspace-specific content into the
// rootfs disqualifies reuse (which would otherwise hand it the wrong rootfs).
func TestCanReuseRootfsBaseline(t *testing.T) {
	if !CanReuseRootfsBaseline(Options{PrepareForStart: true}) {
		t.Fatal("a plain prepare-for-start workspace should be reusable")
	}
	// Hostname travels on the kernel command line, never into the rootfs, so
	// a defaulted (or explicit) hostname must NOT disqualify reuse — every
	// named workspace has one, and gating on it made the fast path
	// unreachable in practice.
	if !CanReuseRootfsBaseline(Options{PrepareForStart: true, Hostname: "h"}) {
		t.Fatal("hostname must not disqualify baseline reuse; it is not baked into the rootfs")
	}
	cases := map[string]Options{
		"not prepare-for-start": {PrepareForStart: false},
		"exec command":          {PrepareForStart: true, ExecCommand: "echo hi"},
		"service command":       {PrepareForStart: true, ServiceCommand: "run"},
		"env":                   {PrepareForStart: true, Env: map[string]string{"A": "B"}},
		"files":                 {PrepareForStart: true, Files: []File{{}}},
		"disks":                 {PrepareForStart: true, Disks: []Disk{{}}},
		"console shell":         {PrepareForStart: true, ConsoleShell: "/bin/bash"},
		// Baselines are built with the image command suppressed; cloning one
		// for --image-command would silently never run the entrypoint.
		"image command": {PrepareForStart: true, UseImageCommand: true},
		// Baselines are built at the default size; an explicit size must
		// build so the workspace gets the disk it asked for.
		"explicit size": {PrepareForStart: true, SizeExplicit: true, SizeMiB: 8192},
		"spec size":     {PrepareForStart: true, SpecSize: true, SizeMiB: 8192},
	}
	for name, o := range cases {
		if CanReuseRootfsBaseline(o) {
			t.Errorf("%s: must NOT be reusable", name)
		}
	}
}

// TestBaselineSatisfiesSize guards profile-implied sizes: the gate cannot
// see them (SizeExplicit stays false), so the clone path must compare the
// baseline's actual bytes against the workspace's effective size and fall
// through to a real build when the baseline is too small.
func TestBaselineSatisfiesSize(t *testing.T) {
	baseline := rootfs.Provenance{SizeBytes: 1024 * 1024 * 1024}
	if !BaselineSatisfiesSize(baseline, Options{SizeMiB: 1024}) {
		t.Error("a baseline exactly the requested size must satisfy")
	}
	if BaselineSatisfiesSize(baseline, Options{SizeMiB: 4096}) {
		t.Error("a 1GiB baseline must not satisfy a 4GiB request")
	}
	if BaselineSatisfiesSize(rootfs.Provenance{}, Options{SizeMiB: 1024}) {
		t.Error("a provenance without a recorded size must not satisfy")
	}
}

// TestBuildRootfsCloneRecordsActualSize: the manifest must record the disk
// the workspace actually has (the baseline's size), and a too-small
// baseline must fall through to a build rather than cloning.
func TestBuildRootfsCloneRecordsActualSize(t *testing.T) {
	dir := t.TempDir()
	baseline := filepath.Join(dir, "baseline.ext4")
	if err := os.WriteFile(baseline, []byte("BASELINE-ROOTFS"), 0o644); err != nil {
		t.Fatal(err)
	}
	name := "sized"
	rootfsPath := WorkspaceRootfsPath(dir, name, HostBackend())
	if err := os.MkdirAll(filepath.Dir(rootfsPath), 0o755); err != nil {
		t.Fatal(err)
	}
	opts := Options{
		Name: name, StateDir: dir, Backend: HostBackend(),
		ImageRef: "microagent-baseline-test.invalid/alpine:3.20", Architecture: "amd64",
		PrepareForStart: true,
		SizeMiB:         1024,
		RootfsBaseline: func(rp string) (string, rootfs.Provenance, bool) {
			return baseline, rootfs.Provenance{
				ImageRef:     "microagent-baseline-test.invalid/alpine:3.20",
				OutputPath:   rp,
				BuilderPhase: "copy-baseline",
				SizeBytes:    2048 * 1024 * 1024,
			}, true
		},
	}
	result, err := BuildRootfs(context.Background(), opts)
	if err != nil {
		t.Fatalf("BuildRootfs: %v", err)
	}
	if result.Image.BuilderPhase != "copy-baseline" {
		t.Fatalf("expected a clone, got %+v", result.Image)
	}
	if result.Resources.SizeMiB != 2048 {
		t.Errorf("manifest size = %d MiB, want the baseline's actual 2048", result.Resources.SizeMiB)
	}

	// A baseline smaller than a profile-implied request must not clone.
	tooSmall := opts
	tooSmall.Name = "toosmall"
	tooSmall.SizeMiB = 4096
	tooSmall.RootfsBaseline = func(rp string) (string, rootfs.Provenance, bool) {
		return baseline, rootfs.Provenance{SizeBytes: 1024 * 1024 * 1024, OutputPath: rp, BuilderPhase: "copy-baseline"}, true
	}
	if _, err := BuildRootfs(context.Background(), tooSmall); err == nil {
		t.Error("a too-small baseline cloned instead of falling through to a build (build must fail here: no registry)")
	}
}
