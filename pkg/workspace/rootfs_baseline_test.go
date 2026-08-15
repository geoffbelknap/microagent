package workspace

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/geoffbelknap/microagent/pkg/rootfs"
	"github.com/geoffbelknap/microagent/pkg/vmkit"
)

// TestBuildRootfsReusesBaselineForPlainWorkspace is the B19 restoration guard: a
// plain workspace clones a pulled/tagged baseline rootfs (no pull/rebuild) when
// the injected resolver offers one.
func TestBuildRootfsReusesBaselineForPlainWorkspace(t *testing.T) {
	dir := t.TempDir()
	baseline := filepath.Join(dir, "baseline.ext4")
	if err := os.WriteFile(baseline, []byte("BASELINE-ROOTFS"), 0o444); err != nil {
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
	info, err := os.Stat(rootfsPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0o200 == 0 {
		t.Fatalf("derived rootfs mode = %04o, want a private writable disk", info.Mode().Perm())
	}
	if err := os.WriteFile(rootfsPath, []byte("WORKSPACE-WRITE"), 0o644); err != nil {
		t.Fatalf("write derived rootfs: %v", err)
	}
	base, err := os.ReadFile(baseline)
	if err != nil {
		t.Fatal(err)
	}
	if string(base) != "BASELINE-ROOTFS" {
		t.Fatalf("baseline changed through private derivation: %q", base)
	}
}

// TestCanReuseRootfsBaseline guards the predicate: with every
// per-workspace fact traveling on the per-boot config disk, nothing but an
// explicit size request changes the rootfs bytes — commands, env, files,
// disks, ports, shells, and hostnames all reuse the baseline.
func TestCanReuseRootfsBaseline(t *testing.T) {
	reusable := map[string]Options{
		"plain create":    {PrepareForStart: true},
		"one-shot run":    {ExecCommand: "echo hi"},
		"service":         {PrepareForStart: true, ServiceCommand: "run"},
		"image command":   {PrepareForStart: true, UseImageCommand: true},
		"env":             {PrepareForStart: true, Env: map[string]string{"A": "B"}},
		"declared files":  {PrepareForStart: true, Files: []File{{}}},
		"disks":           {PrepareForStart: true, Disks: []Disk{{}}},
		"console shell":   {PrepareForStart: true, ConsoleShell: "/bin/bash"},
		"published ports": {PrepareForStart: true, Network: vmkit.NetworkConfig{PortForwards: []vmkit.PortForward{{HostPort: 8080, GuestPort: 80}}}},
	}
	for name, o := range reusable {
		if !CanReuseRootfsBaseline(o) {
			t.Errorf("%s: must be reusable — nothing here changes rootfs bytes", name)
		}
	}
	for name, o := range map[string]Options{
		"explicit size": {PrepareForStart: true, SizeExplicit: true, SizeMiB: 8192},
		"spec size":     {PrepareForStart: true, SpecSize: true, SizeMiB: 8192},
		// Baselines are stripped; a setuid-preserving workspace must build
		// fresh rather than clone a rootfs missing the bits it asked for.
		"allow guest setuid": {PrepareForStart: true, AllowGuestSetuid: true},
	} {
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
