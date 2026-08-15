package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/geoffbelknap/microagent/pkg/imagecache"
	"github.com/geoffbelknap/microagent/pkg/perf"
	"github.com/geoffbelknap/microagent/pkg/rootfs"
	"github.com/geoffbelknap/microagent/pkg/vmkit"
)

func TestPerfBootRejectsInvalidIterations(t *testing.T) {
	dir := t.TempDir()
	stdoutPath := filepath.Join(dir, "perf.json")
	stdout, err := os.Create(stdoutPath)
	if err != nil {
		t.Fatal(err)
	}
	err = runPerf(t.Context(), []string{"boot", "--iterations", "0"}, stdout)
	if closeErr := stdout.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	if err == nil || !strings.Contains(err.Error(), "iterations must be positive") {
		t.Fatalf("runPerf err = %v", err)
	}
}

// TestPerfBootWiresRootfsBaseline guards the measurement against the pipeline
// it measures: `perf boot` must hand the image-store hooks to every iteration,
// or it times a rootfs build that a repeat `run` of the same image skips and
// reports the result as boot time.
func TestPerfBootWiresRootfsBaseline(t *testing.T) {
	dir := t.TempDir()
	previous := perfBoot
	t.Cleanup(func() { perfBoot = previous })
	var captured perf.BootOptions
	perfBoot = func(_ context.Context, opts perf.BootOptions) (perf.BootReport, error) {
		captured = opts
		return perf.BootReport{Benchmark: "boot"}, nil
	}
	stdout, err := os.Create(filepath.Join(dir, "perf.txt"))
	if err != nil {
		t.Fatal(err)
	}
	err = runPerf(t.Context(), []string{"boot", "--state-dir", dir, "--image", "docker.io/library/busybox:1.36"}, stdout)
	if closeErr := stdout.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	if err != nil {
		t.Fatalf("runPerf boot: %v", err)
	}
	if captured.RootfsBaseline == nil {
		t.Error("perf boot measured without a rootfs baseline resolver: every iteration would rebuild")
	}
	if captured.RootfsBaselineSave == nil {
		t.Error("perf boot measured without a rootfs baseline save hook: it could not seed a baseline for later runs")
	}
}

func TestPerfReadyWiresPreparedBaselineAndInteractiveProbe(t *testing.T) {
	dir := t.TempDir()
	previous := perfReady
	t.Cleanup(func() { perfReady = previous })
	var captured perf.ReadyOptions
	perfReady = func(_ context.Context, opts perf.ReadyOptions) (perf.ReadyReport, error) {
		captured = opts
		return perf.ReadyReport{Benchmark: "ready"}, nil
	}
	stdout, err := os.Create(filepath.Join(dir, "ready.txt"))
	if err != nil {
		t.Fatal(err)
	}
	err = runPerf(t.Context(), []string{"ready", "--state-dir", dir, "--image", "local/coding-agent:prepared", "--exec", "pi --version"}, stdout)
	if closeErr := stdout.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	if err != nil {
		t.Fatalf("runPerf ready: %v", err)
	}
	if captured.RootfsBaseline == nil || captured.RootfsBaselineSave == nil {
		t.Fatal("perf ready did not receive image-store baseline hooks")
	}
	if captured.ExecCommand != "pi --version" {
		t.Fatalf("interactive probe = %q", captured.ExecCommand)
	}
	if captured.StartMode != perf.ReadyStartColdBoot || captured.ProbeMode != perf.ReadyProbeInteractiveShell {
		t.Fatalf("default ready modes = start:%q probe:%q", captured.StartMode, captured.ProbeMode)
	}
	if captured.Warmups != 1 || captured.Iterations != 5 {
		t.Fatalf("default ready runs = warmups:%d measurements:%d", captured.Warmups, captured.Iterations)
	}
}

func TestPerfReadyParsesExplicitLifecycleAndProbe(t *testing.T) {
	dir := t.TempDir()
	previous := perfReady
	t.Cleanup(func() { perfReady = previous })
	var captured perf.ReadyOptions
	perfReady = func(_ context.Context, opts perf.ReadyOptions) (perf.ReadyReport, error) {
		captured = opts
		return perf.ReadyReport{Benchmark: "ready"}, nil
	}
	stdout, err := os.Create(filepath.Join(dir, "ready-modes.txt"))
	if err != nil {
		t.Fatal(err)
	}
	err = runPerf(t.Context(), []string{
		"ready", "--state-dir", dir, "--image", "local/base:prepared",
		"--start", "snapshot-fork", "--probe", "exec",
	}, stdout)
	if closeErr := stdout.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	if err != nil {
		t.Fatalf("runPerf ready modes: %v", err)
	}
	if captured.StartMode != perf.ReadyStartSnapshotFork || captured.ProbeMode != perf.ReadyProbeStructuredExec {
		t.Fatalf("ready modes = start:%q probe:%q", captured.StartMode, captured.ProbeMode)
	}
}

func TestPerfReadyReportsTeardownFailureAfterWritingMeasurements(t *testing.T) {
	dir := t.TempDir()
	previous := perfReady
	t.Cleanup(func() { perfReady = previous })
	perfReady = func(_ context.Context, _ perf.ReadyOptions) (perf.ReadyReport, error) {
		return perf.ReadyReport{
			Benchmark: "ready",
			Iterations: []perf.ReadyIteration{{
				Name:          "perf-r-test-1",
				OK:            true,
				DurationMs:    12,
				TeardownError: "cleanup timed out",
			}},
			Summary: perf.ReadySummary{Count: 1, TeardownFailures: 1},
		}, nil
	}
	stdoutPath := filepath.Join(dir, "ready-teardown.json")
	stdout, err := os.Create(stdoutPath)
	if err != nil {
		t.Fatal(err)
	}
	err = runPerf(t.Context(), []string{"ready", "--state-dir", dir, "--image", "local/base:prepared"}, stdout)
	if closeErr := stdout.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	if err == nil || !strings.Contains(err.Error(), "0 of 1 measurements failed; 1 teardowns failed") {
		t.Fatalf("runPerf error = %v", err)
	}
	body, readErr := os.ReadFile(stdoutPath)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if !strings.Contains(string(body), `"teardown_failures": 1`) || !strings.Contains(string(body), `"teardown_error": "cleanup timed out"`) {
		t.Fatalf("output = %s", body)
	}
}

func TestWriteReadyReportUsesSummaryFirstText(t *testing.T) {
	previousOutput := outputFormat
	outputFormat = "text"
	t.Cleanup(func() { outputFormat = previousOutput })
	path := filepath.Join(t.TempDir(), "ready.txt")
	stdout, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	report := perf.ReadyReport{
		Benchmark:      "ready",
		Backend:        vmkit.BackendAppleVF,
		Arch:           "arm64",
		Profile:        "small",
		ImageRef:       "local/base:prepared",
		StartMode:      perf.ReadyStartColdBoot,
		ReadinessProbe: perf.ReadyProbeInteractiveShell,
		Probe:          "true",
		Boundary:       perf.ReadyBoundary{Start: "before_workspace_create", Stop: "after_successful_interactive_shell_command", Excluded: []string{"iteration_teardown", "warmup_runs"}},
		CacheCondition: "host_page_cache_uncontrolled",
		Warmup: &perf.ReadyWarmup{Excluded: true, Summary: perf.ReadySummary{
			Count: 1, Builds: 1,
		}},
		Summary: perf.ReadySummary{
			Count: 5, Baselines: 5,
			FullReady:        perf.Distribution{MinMs: 475, P50Ms: 582, MaxMs: 598},
			WorkspacePrepare: perf.Distribution{P50Ms: 34},
			Lifecycle:        perf.Distribution{P50Ms: 271},
			InterfaceReady:   perf.Distribution{P50Ms: 309},
			Probe:            perf.Distribution{P50Ms: 1},
		},
		Iterations: []perf.ReadyIteration{
			{OK: true, DurationMs: 475, Phases: perf.ReadyPhases{WorkspacePrepareMs: 31, LifecycleMs: 260, InterfaceReadyMs: 213, ProbeMs: 1}},
			{OK: true, DurationMs: 582, Phases: perf.ReadyPhases{WorkspacePrepareMs: 34, LifecycleMs: 271, InterfaceReadyMs: 309, ProbeMs: 1}},
			{OK: true, DurationMs: 598, Phases: perf.ReadyPhases{WorkspacePrepareMs: 36, LifecycleMs: 280, InterfaceReadyMs: 316, ProbeMs: 2}},
		},
	}
	writeReadyPreamble(stdout, report.Backend, report.Arch, report.Profile)
	if err := writeReadyReport(stdout, report, false); err != nil {
		t.Fatal(err)
	}
	if err := stdout.Close(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, want := range []string{"Ready benchmark — apple-vf / arm64 / small\n\nReady time", "Median", "582ms", "475ms–598ms", "Median run breakdown", "Lifecycle transition", "237ms", "Benchmark details", "Warm-up       1 · 1 passed", "Measurements  5 · 5 passed"} {
		if !strings.Contains(text, want) {
			t.Fatalf("text report missing %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "Full ready ms:") || strings.Contains(text, "p95=598") {
		t.Fatalf("text report retained diagnostic dump:\n%s", text)
	}
}

func TestWriteReadyReportCompactJSONOmitsIterationsAndHost(t *testing.T) {
	previousOutput := outputFormat
	outputFormat = "json"
	t.Cleanup(func() { outputFormat = previousOutput })
	path := filepath.Join(t.TempDir(), "ready.json")
	stdout, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	report := perf.ReadyReport{
		Benchmark:  "ready",
		Warmup:     &perf.ReadyWarmup{Excluded: true, Summary: perf.ReadySummary{Count: 1}},
		Iterations: []perf.ReadyIteration{{Name: "hidden", OK: true}},
		Summary:    perf.ReadySummary{Count: 1},
		Host:       &vmkit.HostSupport{Backend: vmkit.BackendAppleVF},
	}
	if err := writeReadyReport(stdout, report, true); err != nil {
		t.Fatal(err)
	}
	if err := stdout.Close(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	if strings.Contains(text, `"iterations"`) || strings.Contains(text, `"host"`) || strings.Contains(text, "hidden") {
		t.Fatalf("compact JSON retained details: %s", text)
	}
	for _, want := range []string{`"ok": true`, `"warmup"`, `"summary"`} {
		if !strings.Contains(text, want) {
			t.Fatalf("compact JSON missing %s: %s", want, text)
		}
	}
}

func TestReadyProgressPrinterUsesStableNonTTYLines(t *testing.T) {
	var output bytes.Buffer
	printer := newReadyProgressPrinter(&output, false)
	printer.print(perf.ReadyProgressEvent{Run: perf.ReadyProgressWarmup, Index: 1, Total: 1, Phase: perf.ReadyProgressLifecycle, Message: "starting workspace", Excluded: true})
	printer.print(perf.ReadyProgressEvent{Run: perf.ReadyProgressWarmup, Index: 1, Total: 1, Phase: perf.ReadyProgressComplete, ElapsedMs: 4000, Excluded: true, OK: true})
	printer.close()
	text := output.String()
	if !strings.Contains(text, "• Warm-up 1/1 · starting workspace") || !strings.Contains(text, "✓ [ 4.00s] Warm-up 1/1") {
		t.Fatalf("progress output = %q", text)
	}
	if strings.Contains(text, "\033[") || strings.Contains(text, "\r") {
		t.Fatalf("non-TTY progress contains terminal controls: %q", text)
	}
}

func TestWriteBootReportUsesResultFirstText(t *testing.T) {
	previousOutput := outputFormat
	outputFormat = "text"
	t.Cleanup(func() { outputFormat = previousOutput })
	path := filepath.Join(t.TempDir(), "boot.txt")
	stdout, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	iterations := []perf.Iteration{
		{Name: "one", OK: true, DurationMs: 475, Rootfs: perf.RootfsSourceBaseline},
		{Name: "two", OK: true, DurationMs: 581, Rootfs: perf.RootfsSourceBaseline},
		{Name: "three", OK: true, DurationMs: 594, Rootfs: perf.RootfsSourceBuild},
	}
	report := perf.BootReport{
		Benchmark: "boot", Backend: vmkit.BackendAppleVF, Arch: "arm64", Profile: "small",
		ImageRef: "local/base:prepared", Probe: "true", Iterations: iterations,
		Summary: perf.SummarizeIterations(iterations), CacheCondition: "host_page_cache_uncontrolled",
		Boundary: perf.MeasurementBoundary{Start: "before_workspace_run", Stop: "after_guest_command_result", Excluded: []string{"iteration_teardown"}},
	}
	writePerfPreamble(stdout, "Boot", report.Backend, report.Arch, report.Profile)
	if err := writePerfReport(stdout, report, false); err != nil {
		t.Fatal(err)
	}
	if err := stdout.Close(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, want := range []string{"Boot benchmark — apple-vf / arm64 / small\n\nBoot time", "Median", "581ms", "475ms–594ms", "Benchmark details", "Measurements  3 · 3 passed", "Rootfs        2 baseline · 1 build"} {
		if !strings.Contains(text, want) {
			t.Fatalf("boot report missing %q:\n%s", want, text)
		}
	}
}

func TestWriteBootReportCompactJSONOmitsIterationsAndHost(t *testing.T) {
	previousOutput := outputFormat
	outputFormat = "json"
	t.Cleanup(func() { outputFormat = previousOutput })
	path := filepath.Join(t.TempDir(), "boot.json")
	stdout, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	report := perf.BootReport{Benchmark: "boot", Iterations: []perf.Iteration{{Name: "hidden", OK: true}}, Summary: perf.Summary{Count: 1, P50Ms: 42}, Host: &vmkit.HostSupport{Backend: vmkit.BackendAppleVF}}
	if err := writePerfReport(stdout, report, true); err != nil {
		t.Fatal(err)
	}
	if err := stdout.Close(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	if strings.Contains(text, `"iterations"`) || strings.Contains(text, `"host"`) || !strings.Contains(text, `"p50_ms": 42`) {
		t.Fatalf("compact boot JSON = %s", text)
	}
}

func TestWriteFootprintReportLeadsWithMemory(t *testing.T) {
	previousOutput := outputFormat
	outputFormat = "text"
	t.Cleanup(func() { outputFormat = previousOutput })
	path := filepath.Join(t.TempDir(), "footprint.txt")
	stdout, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := writePerfFootprintReport(stdout, perf.FootprintReport{Benchmark: "footprint", Workspace: "research", Backend: vmkit.BackendAppleVF, State: "running", PID: 42, RSSKiB: 131072}); err != nil {
		t.Fatal(err)
	}
	if err := stdout.Close(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	if !strings.Contains(text, "Footprint benchmark — research / apple-vf\n\nResident memory") || !strings.Contains(text, "128.0MiB") || !strings.Contains(text, "Process    PID 42") {
		t.Fatalf("footprint report = %s", text)
	}
}

func TestWriteSteadyReportUsesResultFirstText(t *testing.T) {
	previousOutput := outputFormat
	outputFormat = "text"
	t.Cleanup(func() { outputFormat = previousOutput })
	path := filepath.Join(t.TempDir(), "steady.txt")
	stdout, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	report := perf.SteadyReport{Benchmark: "steady", Workspace: "research", Backend: vmkit.BackendAppleVF, State: "running", PID: 42, DurationSeconds: 10, IntervalSeconds: 1, Summary: perf.RSSSummary{Count: 11, MinKiB: 128000, AvgKiB: 131072, MaxKiB: 134144}}
	if err := writePerfSteadyReport(stdout, report, false); err != nil {
		t.Fatal(err)
	}
	if err := stdout.Close(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	if !strings.HasPrefix(text, "Steady memory\n") || !strings.Contains(text, "Average") || !strings.Contains(text, "128.0MiB") || !strings.Contains(text, "11 samples · every 1s for 10s") {
		t.Fatalf("steady report = %s", text)
	}
}

func TestWriteSteadyReportCompactJSONOmitsSamples(t *testing.T) {
	previousOutput := outputFormat
	outputFormat = "json"
	t.Cleanup(func() { outputFormat = previousOutput })
	path := filepath.Join(t.TempDir(), "steady.json")
	stdout, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	report := perf.SteadyReport{Benchmark: "steady", Workspace: "research", Samples: []perf.RSSSample{{RSSKiB: 10}}, Summary: perf.RSSSummary{Count: 1, AvgKiB: 10}}
	if err := writePerfSteadyReport(stdout, report, true); err != nil {
		t.Fatal(err)
	}
	if err := stdout.Close(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	if strings.Contains(text, `"samples"`) || !strings.Contains(text, `"ok": true`) || !strings.Contains(text, `"avg_kib": 10`) {
		t.Fatalf("compact steady JSON = %s", text)
	}
}

func TestSteadyProgressPrinterUsesStableNonTTYLines(t *testing.T) {
	var output bytes.Buffer
	printer := newSteadyProgressPrinter(&output, false)
	printer.print(perf.SteadyProgressEvent{ElapsedMs: 1000, SampleCount: 2, Sample: perf.RSSSample{RSSKiB: 131072}})
	printer.print(perf.SteadyProgressEvent{ElapsedMs: 2000, SampleCount: 3, Complete: true, OK: true})
	printer.close()
	text := output.String()
	if !strings.Contains(text, "• Sampling memory · 2 samples · 128.0MiB") || !strings.Contains(text, "✓ [ 2.00s] Sampling memory · 3 samples") {
		t.Fatalf("steady progress = %q", text)
	}
}

func TestSummarizePerfIterations(t *testing.T) {
	summary := summarizePerfIterations([]perfIteration{
		{Name: "one", OK: true, DurationMs: 30},
		{Name: "two", OK: true, DurationMs: 10},
		{Name: "three", OK: true, DurationMs: 20},
	})
	if summary.Count != 3 || summary.MinMs != 10 || summary.AvgMs != 20 || summary.MaxMs != 30 {
		t.Fatalf("summary = %#v", summary)
	}
}

func TestParseRSSKiB(t *testing.T) {
	rss, err := parseRSSKiB([]byte("  12345\n"))
	if err != nil {
		t.Fatalf("parseRSSKiB: %v", err)
	}
	if rss != 12345 {
		t.Fatalf("rss = %d", rss)
	}
	if _, err := parseRSSKiB([]byte("")); err == nil {
		t.Fatal("parseRSSKiB accepted empty list output")
	}
}

func TestRunPerfFootprintRequiresRunningPID(t *testing.T) {
	dir := t.TempDir()
	testFirecrackerRuntimeState(t, dir, "research", vmkit.StateStopped, 0)
	stdoutPath := filepath.Join(dir, "footprint.txt")
	stdout, err := os.Create(stdoutPath)
	if err != nil {
		t.Fatal(err)
	}
	err = runPerfFootprint([]string{"research", "--state-dir", dir}, stdout)
	if closeErr := stdout.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	if err == nil || !strings.Contains(err.Error(), "does not have a running process pid") {
		t.Fatalf("runPerfFootprint err = %v", err)
	}
}

func TestSummarizeRSSSamples(t *testing.T) {
	summary := summarizeRSSSamples([]perfRSSSample{
		{RSSKiB: 40},
		{RSSKiB: 20},
		{RSSKiB: 30},
	})
	if summary.Count != 3 || summary.MinKiB != 20 || summary.AvgKiB != 30 || summary.MaxKiB != 40 {
		t.Fatalf("summary = %#v", summary)
	}
}

func TestRunPerfSteadyRejectsInvalidSampling(t *testing.T) {
	dir := t.TempDir()
	stdoutPath := filepath.Join(dir, "steady.txt")
	stdout, err := os.Create(stdoutPath)
	if err != nil {
		t.Fatal(err)
	}
	err = runPerf(t.Context(), []string{"steady", "research", "--duration", "1", "--interval", "2", "--state-dir", dir}, stdout)
	if closeErr := stdout.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	if err == nil || !strings.Contains(err.Error(), "interval must be less than or equal to duration") {
		t.Fatalf("runPerf err = %v", err)
	}
}

func TestImagesListAndPruneUseLocalIndex(t *testing.T) {
	outputFormat = "json"
	t.Cleanup(func() { outputFormat = "" })
	dir := t.TempDir()
	rootfsPath := filepath.Join(dir, "workspaces", "research", "rootfs.ext4")
	if err := os.MkdirAll(filepath.Dir(rootfsPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(rootfsPath, []byte("rootfs"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := imagecache.RecordProvenance(dir, rootfs.Provenance{
		ImageRef:    "docker.io/library/busybox:1.36",
		ResolvedRef: "docker.io/library/busybox@sha256:abc",
		Digest:      "sha256:abc",
		Platform:    rootfs.Platform{OS: "linux", Architecture: "arm64"},
		OutputPath:  rootfsPath,
		SizeBytes:   6,
	}); err != nil {
		t.Fatal(err)
	}
	stdoutPath := filepath.Join(dir, "images.json")
	stdout, err := os.Create(stdoutPath)
	if err != nil {
		t.Fatal(err)
	}
	err = runImage([]string{"list", "--state-dir", dir}, stdout)
	if closeErr := stdout.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	if err != nil {
		t.Fatalf("runImage list: %v", err)
	}
	data, err := os.ReadFile(stdoutPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"digest": "sha256:abc"`) {
		t.Fatalf("images output = %s", data)
	}
	if err := os.Remove(rootfsPath); err != nil {
		t.Fatal(err)
	}
	pruned, err := imagecache.Prune(dir, false)
	if err != nil {
		t.Fatalf("imagecache.Prune: %v", err)
	}
	if len(pruned.Removed) != 1 || len(pruned.Kept) != 0 {
		t.Fatalf("pruned = %#v", pruned)
	}
}

func TestImagesPruneDeleteRemovesReusableBaselines(t *testing.T) {
	dir := t.TempDir()
	rootfsPath := filepath.Join(dir, "images", "rootfs", "busybox.ext4")
	if err := os.MkdirAll(filepath.Dir(rootfsPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(rootfsPath, []byte("rootfs"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, ref := range []string{"docker.io/library/busybox:1.36", "local/busybox:baseline"} {
		if err := imagecache.Upsert(dir, imagecache.Record{
			ImageRef:    ref,
			ResolvedRef: "docker.io/library/busybox@sha256:abc",
			Digest:      "sha256:abc",
			Platform:    rootfs.Platform{OS: "linux", Architecture: "arm64"},
			OutputPath:  rootfsPath,
			SizeBytes:   6,
			LastUsedAt:  time.Now().UTC().Format(time.RFC3339),
		}); err != nil {
			t.Fatal(err)
		}
	}
	pruned, err := imagecache.Prune(dir, true)
	if err != nil {
		t.Fatalf("imagecache.Prune: %v", err)
	}
	if len(pruned.Deleted) != 2 || len(pruned.Kept) != 0 || len(pruned.Removed) != 0 {
		t.Fatalf("pruned = %#v", pruned)
	}
	if _, err := os.Stat(rootfsPath); !os.IsNotExist(err) {
		t.Fatalf("rootfs still exists or stat failed unexpectedly: %v", err)
	}
}

func TestRunImagesPruneDeleteRequiresConfirmationWithoutTTY(t *testing.T) {
	dir := t.TempDir()
	oldTerminal := stdinIsTerminal
	t.Cleanup(func() { stdinIsTerminal = oldTerminal })
	stdinIsTerminal = func() bool { return false }
	stdout, err := os.Create(filepath.Join(dir, "stdout.txt"))
	if err != nil {
		t.Fatal(err)
	}
	err = runImage([]string{"prune", "--purge", "--state-dir", dir}, stdout)
	if closeErr := stdout.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	if err == nil || !strings.Contains(err.Error(), "pass --yes") {
		t.Fatalf("err = %v, want --yes confirmation error", err)
	}
}

func TestRunImagePruneDeletesReusableBaselinesWithYes(t *testing.T) {
	oldOutput := outputFormat
	t.Cleanup(func() { outputFormat = oldOutput })
	outputFormat = "text"
	dir := t.TempDir()
	rootfsPath := filepath.Join(dir, "images", "rootfs", "busybox.ext4")
	if err := os.MkdirAll(filepath.Dir(rootfsPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(rootfsPath, []byte("rootfs"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := imagecache.Upsert(dir, imagecache.Record{
		ImageRef:    "docker.io/library/busybox:1.36",
		ResolvedRef: "docker.io/library/busybox@sha256:abc",
		Digest:      "sha256:abc",
		Platform:    rootfs.Platform{OS: "linux", Architecture: "arm64"},
		OutputPath:  rootfsPath,
		SizeBytes:   6,
		LastUsedAt:  time.Now().UTC().Format(time.RFC3339),
	}); err != nil {
		t.Fatal(err)
	}
	stdoutPath := filepath.Join(dir, "prune.txt")
	stdout, err := os.Create(stdoutPath)
	if err != nil {
		t.Fatal(err)
	}
	err = runImage([]string{"prune", "--purge", "--yes", "--state-dir", dir}, stdout)
	if closeErr := stdout.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	if err != nil {
		t.Fatalf("runImage: %v", err)
	}
	data, err := os.ReadFile(stdoutPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "Deleted: 1") {
		t.Fatalf("prune output = %s", data)
	}
	if _, err := os.Stat(rootfsPath); !os.IsNotExist(err) {
		t.Fatalf("rootfs still exists or stat failed unexpectedly: %v", err)
	}
}

func TestRunImageDeleteFlagRejected(t *testing.T) {
	dir := t.TempDir()
	stdout, err := os.Create(filepath.Join(dir, "stdout.txt"))
	if err != nil {
		t.Fatal(err)
	}
	err = runImage([]string{"delete", "test", "--delete", "--state-dir", dir}, stdout)
	if closeErr := stdout.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	// The stray --delete token is bucketed as a positional by the reorder
	// machinery, so the rejection surfaces as image delete's usage error
	// (which names the current --purge flag), not a flag-package error.
	if err == nil || !strings.Contains(err.Error(), "usage: microagent image delete") || !strings.Contains(err.Error(), "--purge") {
		t.Fatalf("err = %v, want image delete usage error naming --purge", err)
	}
}

func TestImagesPruneDeleteKeepsWorkspaceRootfs(t *testing.T) {
	dir := t.TempDir()
	rootfsPath := filepath.Join(dir, "workspaces", "research", "rootfs.ext4")
	if err := os.MkdirAll(filepath.Dir(rootfsPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(rootfsPath, []byte("rootfs"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := imagecache.Upsert(dir, imagecache.Record{
		ImageRef:    "docker.io/library/busybox:1.36",
		ResolvedRef: "docker.io/library/busybox@sha256:abc",
		Digest:      "sha256:abc",
		Platform:    rootfs.Platform{OS: "linux", Architecture: "arm64"},
		OutputPath:  rootfsPath,
		SizeBytes:   6,
		LastUsedAt:  time.Now().UTC().Format(time.RFC3339),
	}); err != nil {
		t.Fatal(err)
	}
	pruned, err := imagecache.Prune(dir, true)
	if err != nil {
		t.Fatalf("imagecache.Prune: %v", err)
	}
	if len(pruned.Kept) != 1 || len(pruned.Deleted) != 0 || len(pruned.Removed) != 0 {
		t.Fatalf("pruned = %#v", pruned)
	}
	if _, err := os.Stat(rootfsPath); err != nil {
		t.Fatalf("workspace rootfs was removed: %v", err)
	}
}

func TestImagesTagCreatesAlias(t *testing.T) {
	dir := t.TempDir()
	rootfsPath := filepath.Join(dir, "images", "rootfs", "busybox.ext4")
	if err := os.MkdirAll(filepath.Dir(rootfsPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(rootfsPath, []byte("rootfs"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := imagecache.Upsert(dir, imagecache.Record{
		ImageRef:    "docker.io/library/busybox:1.36",
		ResolvedRef: "docker.io/library/busybox@sha256:abc",
		Digest:      "sha256:abc",
		Platform:    rootfs.Platform{OS: "linux", Architecture: "arm64"},
		OutputPath:  rootfsPath,
		SizeBytes:   6,
		LastUsedAt:  "2026-05-06T00:00:00Z",
	}); err != nil {
		t.Fatal(err)
	}
	tagged, err := imagecache.Tag(dir, "sha256:abc", "local/busybox:baseline")
	if err != nil {
		t.Fatalf("imagecache.Tag: %v", err)
	}
	if tagged.ImageRef != "local/busybox:baseline" || tagged.OutputPath != rootfsPath {
		t.Fatalf("tagged = %#v", tagged)
	}
	images, err := imagecache.List(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(images) != 2 {
		t.Fatalf("images len = %d, want 2: %#v", len(images), images)
	}
}

func TestImagesRemoveAliasKeepsSharedBaseline(t *testing.T) {
	dir := t.TempDir()
	rootfsPath := filepath.Join(dir, "images", "rootfs", "busybox.ext4")
	if err := os.MkdirAll(filepath.Dir(rootfsPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(rootfsPath, []byte("rootfs"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, ref := range []string{"docker.io/library/busybox:1.36", "local/busybox:baseline"} {
		if err := imagecache.Upsert(dir, imagecache.Record{
			ImageRef:    ref,
			ResolvedRef: "docker.io/library/busybox@sha256:abc",
			Digest:      "sha256:abc",
			Platform:    rootfs.Platform{OS: "linux", Architecture: "arm64"},
			OutputPath:  rootfsPath,
			SizeBytes:   6,
			LastUsedAt:  time.Now().UTC().Format(time.RFC3339),
		}); err != nil {
			t.Fatal(err)
		}
	}
	removed, err := imagecache.Remove(dir, "local/busybox:baseline", true)
	if err != nil {
		t.Fatalf("imagecache.Remove: %v", err)
	}
	if len(removed.Removed) != 1 || len(removed.Deleted) != 0 || len(removed.Kept) != 1 {
		t.Fatalf("removed = %#v", removed)
	}
	if _, err := os.Stat(rootfsPath); err != nil {
		t.Fatalf("baseline was removed: %v", err)
	}
}

func TestImagesRemoveDigestDeletesUnsharedBaseline(t *testing.T) {
	dir := t.TempDir()
	rootfsPath := filepath.Join(dir, "images", "rootfs", "busybox.ext4")
	if err := os.MkdirAll(filepath.Dir(rootfsPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(rootfsPath, []byte("rootfs"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, ref := range []string{"docker.io/library/busybox:1.36", "local/busybox:baseline"} {
		if err := imagecache.Upsert(dir, imagecache.Record{
			ImageRef:    ref,
			ResolvedRef: "docker.io/library/busybox@sha256:abc",
			Digest:      "sha256:abc",
			Platform:    rootfs.Platform{OS: "linux", Architecture: "arm64"},
			OutputPath:  rootfsPath,
			SizeBytes:   6,
			LastUsedAt:  time.Now().UTC().Format(time.RFC3339),
		}); err != nil {
			t.Fatal(err)
		}
	}
	removed, err := imagecache.Remove(dir, "sha256:abc", true)
	if err != nil {
		t.Fatalf("imagecache.Remove: %v", err)
	}
	if len(removed.Deleted) != 2 || len(removed.Removed) != 0 || len(removed.Kept) != 0 {
		t.Fatalf("removed = %#v", removed)
	}
	if _, err := os.Stat(rootfsPath); !os.IsNotExist(err) {
		t.Fatalf("baseline still exists or stat failed unexpectedly: %v", err)
	}
}
