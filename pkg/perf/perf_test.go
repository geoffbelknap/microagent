package perf

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/geoffbelknap/microagent/pkg/rootfs"
	"github.com/geoffbelknap/microagent/pkg/vmkit"
	"github.com/geoffbelknap/microagent/pkg/workspace"
)

func TestBootRejectsInvalidIterations(t *testing.T) {
	_, err := Boot(context.Background(), BootOptions{Iterations: 0})
	if err == nil || !strings.Contains(err.Error(), "iterations must be positive") {
		t.Fatalf("Boot err = %v", err)
	}
}

func TestBootReportsUnknownProfileAsIterationFailure(t *testing.T) {
	report, err := Boot(context.Background(), BootOptions{
		StateDir:    t.TempDir(),
		ImageRef:    "docker.io/library/busybox:1.36",
		Profile:     "definitely-not-a-profile",
		ExecCommand: "true",
		Iterations:  1,
		Timeout:     1,
	})
	if err != nil {
		t.Fatalf("Boot: %v", err)
	}
	if report.Summary.Count != 1 {
		t.Fatalf("summary count = %d, want 1", report.Summary.Count)
	}
	if len(report.Iterations) != 1 || report.Iterations[0].OK {
		t.Fatalf("iterations = %#v, want one failed iteration", report.Iterations)
	}
	if !strings.Contains(report.Iterations[0].Error, "unknown resource profile") {
		t.Fatalf("iteration error = %q, want unknown profile", report.Iterations[0].Error)
	}
}

// stubBuildRootfs stands in for a measured boot, taking the same two branches
// BuildRootfs does: consult the injected resolver, and call the save hook when
// the full build runs. It records the Options each iteration was given so the
// tests can check what perf handed the workspace layer.
func stubBuildRootfs(t *testing.T, baselineAvailable bool) *[]workspace.Options {
	t.Helper()
	previous := runWorkspace
	t.Cleanup(func() { runWorkspace = previous })
	seen := &[]workspace.Options{}
	runWorkspace = func(_ context.Context, opts workspace.Options) (workspace.Result, error) {
		*seen = append(*seen, opts)
		rootfsPath := filepath.Join(opts.StateDir, opts.Name, "rootfs.ext4")
		if opts.RootfsBaseline != nil && baselineAvailable {
			if _, _, ok := opts.RootfsBaseline(rootfsPath); ok {
				return workspace.Result{}, nil
			}
		}
		if opts.RootfsBaselineSave != nil {
			opts.RootfsBaselineSave(rootfsPath, rootfs.Provenance{ImageRef: opts.ImageRef})
		}
		return workspace.Result{}, nil
	}
	return seen
}

func bootOptionsForTest(t *testing.T) BootOptions {
	t.Helper()
	return BootOptions{
		StateDir:    t.TempDir(),
		ImageRef:    "docker.io/library/busybox:1.36",
		ExecCommand: "true",
		Iterations:  1,
		Timeout:     time.Second,
	}
}

// TestBootReusesRootfsBaseline is the measurement-fidelity guard: `perf boot`
// hands the baseline resolver to every measured boot, so it times the rootfs
// path a repeat `run` takes instead of a full rebuild, and says so in the
// report.
func TestBootReusesRootfsBaseline(t *testing.T) {
	seen := stubBuildRootfs(t, true)
	resolved := 0
	opts := bootOptionsForTest(t)
	opts.Iterations = 2
	opts.RootfsBaseline = func(rootfsPath string) (string, rootfs.Provenance, bool) {
		resolved++
		return filepath.Join(opts.StateDir, "baseline.ext4"), rootfs.Provenance{ImageRef: opts.ImageRef}, true
	}
	report, err := Boot(context.Background(), opts)
	if err != nil {
		t.Fatalf("Boot: %v", err)
	}
	if len(*seen) != 2 {
		t.Fatalf("measured boots = %d, want 2", len(*seen))
	}
	for i, boot := range *seen {
		if boot.RootfsBaseline == nil {
			t.Fatalf("iteration %d was measured without the baseline resolver", i+1)
		}
	}
	if resolved != 2 {
		t.Fatalf("resolver consulted %d times, want once per iteration", resolved)
	}
	for i, iteration := range report.Iterations {
		if iteration.Rootfs != RootfsSourceBaseline {
			t.Fatalf("iteration %d rootfs = %q, want %q", i+1, iteration.Rootfs, RootfsSourceBaseline)
		}
	}
	if report.Summary.Baselines != 2 || report.Summary.Builds != 0 {
		t.Fatalf("summary = %#v, want two baseline clones", report.Summary)
	}
}

// TestBootSeedsRootfsBaselineFromFullBuild covers the other half: when no
// baseline exists yet the measured boot seeds one, so the first later `run`
// does not pay for a build perf already did.
func TestBootSeedsRootfsBaselineFromFullBuild(t *testing.T) {
	stubBuildRootfs(t, false)
	saved := 0
	opts := bootOptionsForTest(t)
	opts.RootfsBaseline = func(string) (string, rootfs.Provenance, bool) {
		return "", rootfs.Provenance{}, false
	}
	opts.RootfsBaselineSave = func(string, rootfs.Provenance) { saved++ }
	report, err := Boot(context.Background(), opts)
	if err != nil {
		t.Fatalf("Boot: %v", err)
	}
	if saved != 1 {
		t.Fatalf("baseline saved %d times, want 1", saved)
	}
	if report.Iterations[0].Rootfs != RootfsSourceBuild {
		t.Fatalf("iteration rootfs = %q, want %q", report.Iterations[0].Rootfs, RootfsSourceBuild)
	}
	if report.Summary.Builds != 1 || report.Summary.Baselines != 0 {
		t.Fatalf("summary = %#v, want one full build", report.Summary)
	}
}

// TestBootWithoutBaselineHooksMeasuresFullBuild keeps a caller that injects
// nothing on the old behavior — and labels what it measured.
func TestBootWithoutBaselineHooksMeasuresFullBuild(t *testing.T) {
	seen := stubBuildRootfs(t, true)
	report, err := Boot(context.Background(), bootOptionsForTest(t))
	if err != nil {
		t.Fatalf("Boot: %v", err)
	}
	if (*seen)[0].RootfsBaseline != nil {
		t.Fatal("measured boot got a baseline resolver the caller never injected")
	}
	if report.Iterations[0].Rootfs != RootfsSourceBuild {
		t.Fatalf("iteration rootfs = %q, want %q", report.Iterations[0].Rootfs, RootfsSourceBuild)
	}
}

func TestSummarizeIterations(t *testing.T) {
	summary := SummarizeIterations([]Iteration{
		{Name: "one", OK: true, DurationMs: 30, Rootfs: RootfsSourceBuild},
		{Name: "two", OK: true, DurationMs: 10, Rootfs: RootfsSourceBaseline},
		{Name: "three", OK: true, DurationMs: 20, Rootfs: RootfsSourceBaseline},
	})
	if summary.Count != 3 || summary.MinMs != 10 || summary.AvgMs != 20 || summary.MaxMs != 30 {
		t.Fatalf("summary = %#v", summary)
	}
	if summary.Failures != 0 {
		t.Fatalf("Failures = %d, want 0", summary.Failures)
	}
	// A blend of branches has to be visible in the summary: min is a warm
	// boot and max a first boot, so the average describes neither.
	if summary.Baselines != 2 || summary.Builds != 1 {
		t.Fatalf("summary = %#v, want baselines=2 builds=1", summary)
	}

	failed := SummarizeIterations([]Iteration{
		{Name: "one", OK: true, DurationMs: 30},
		{Name: "two", OK: false, DurationMs: 10, Error: "boot timeout"},
	})
	if failed.Failures != 1 {
		t.Fatalf("Failures = %d, want 1", failed.Failures)
	}
}

func TestParseRSSKiB(t *testing.T) {
	rss, err := ParseRSSKiB([]byte("  12345\n"))
	if err != nil {
		t.Fatalf("ParseRSSKiB: %v", err)
	}
	if rss != 12345 {
		t.Fatalf("rss = %d", rss)
	}
	if _, err := ParseRSSKiB([]byte("")); err == nil {
		t.Fatal("ParseRSSKiB accepted empty ps output")
	}
}

func TestFootprintRequiresRunningPID(t *testing.T) {
	dir := t.TempDir()
	opts := workspace.Options{StateDir: dir, Name: "research"}
	req, err := workspace.Request(opts, "run", filepath.Join(dir, "rootfs.ext4"), "req-1")
	if err != nil {
		t.Fatal(err)
	}
	if err := workspace.WriteProcessState(opts, req, vmkit.StateStopped, 0, ""); err != nil {
		t.Fatal(err)
	}
	_, err = Footprint(dir, "research")
	if err == nil || !strings.Contains(err.Error(), "does not have a running process pid") {
		t.Fatalf("Footprint err = %v", err)
	}
}

func TestSummarizeRSSSamples(t *testing.T) {
	summary := SummarizeRSSSamples([]RSSSample{
		{RSSKiB: 40},
		{RSSKiB: 20},
		{RSSKiB: 30},
	})
	if summary.Count != 3 || summary.MinKiB != 20 || summary.AvgKiB != 30 || summary.MaxKiB != 40 {
		t.Fatalf("summary = %#v", summary)
	}
}

func TestSteadyRejectsInvalidSampling(t *testing.T) {
	_, err := Steady(context.Background(), t.TempDir(), "research", 0, 1)
	if err == nil || !strings.Contains(err.Error(), "duration must be positive") {
		t.Fatalf("Steady err = %v", err)
	}
}

func TestProcessRSSKiBRejectsInvalidPID(t *testing.T) {
	if _, err := ProcessRSSKiB(0); err == nil || !strings.Contains(err.Error(), "pid must be positive") {
		t.Fatalf("ProcessRSSKiB err = %v", err)
	}
}
