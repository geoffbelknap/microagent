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
		ImageRef: "docker.io/library/alpine:3.20", Architecture: "amd64",
		PrepareForStart: true,
		RootfsBaseline: func(rp string) (string, rootfs.Provenance, bool) {
			called = true
			return baseline, rootfs.Provenance{ImageRef: "docker.io/library/alpine:3.20", OutputPath: rp, BuilderPhase: "copy-baseline"}, true
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
		t.Fatalf("cloned rootfs = %q, want the baseline content (rootfs was rebuilt?)", got)
	}
	if result.Image.BuilderPhase != "copy-baseline" {
		t.Fatalf("result Image = %+v, want the baseline provenance", result.Image)
	}
}

// TestCanReuseRootfsBaseline guards the predicate: a plain prepare-for-start
// workspace is reusable; anything that bakes workspace-specific content into the
// rootfs disqualifies reuse (which would otherwise hand it the wrong rootfs).
func TestCanReuseRootfsBaseline(t *testing.T) {
	if !canReuseRootfsBaseline(Options{PrepareForStart: true}) {
		t.Fatal("a plain prepare-for-start workspace should be reusable")
	}
	cases := map[string]Options{
		"not prepare-for-start": {PrepareForStart: false},
		"exec command":          {PrepareForStart: true, ExecCommand: "echo hi"},
		"service command":       {PrepareForStart: true, ServiceCommand: "run"},
		"env":                   {PrepareForStart: true, Env: map[string]string{"A": "B"}},
		"files":                 {PrepareForStart: true, Files: []File{{}}},
		"disks":                 {PrepareForStart: true, Disks: []Disk{{}}},
		"hostname":              {PrepareForStart: true, Hostname: "h"},
		"console shell":         {PrepareForStart: true, ConsoleShell: "/bin/bash"},
	}
	for name, o := range cases {
		if canReuseRootfsBaseline(o) {
			t.Errorf("%s: must NOT be reusable", name)
		}
	}
}
