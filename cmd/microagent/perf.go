package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/geoffbelknap/microagent/pkg/operation"
	"github.com/geoffbelknap/microagent/pkg/perf"
)

const (
	// networkModePerfFlagHelp mirrors networkModeFlagHelp for measured boots; an
	// empty value falls back to the backend default.
	networkModePerfFlagHelp = "Network mode for measured boots: user (rootless, unprivileged user namespace) or isolated (no network); empty uses the backend default"
)

type perfBootOptions = perf.BootOptions
type perfReport = perf.BootReport
type perfIteration = perf.Iteration
type perfSummary = perf.Summary
type perfReadyOptions = perf.ReadyOptions
type perfReadyReport = perf.ReadyReport
type perfFootprintReport = perf.FootprintReport
type perfSteadyReport = perf.SteadyReport
type perfRSSSample = perf.RSSSample
type perfRSSSummary = perf.RSSSummary

// perfBoot is the boot benchmark, indirected so tests can check what the CLI
// hands it.
var perfBoot = perf.Boot
var perfReady = perf.Ready
var perfSteady = perf.SteadyWithOptions

func runPerf(ctx context.Context, args []string, stdout *os.File) error {
	if len(args) == 0 || args[0] == "help" || args[0] == "--help" || args[0] == "-h" {
		printPerfHelp(stdout)
		return nil
	}
	switch args[0] {
	case "boot":
		return runPerfBoot(ctx, args[1:], stdout)
	case "ready":
		return runPerfReady(ctx, args[1:], stdout)
	case "footprint":
		return runPerfFootprint(args[1:], stdout)
	case "steady":
		return runPerfSteady(ctx, args[1:], stdout)
	default:
		return fmt.Errorf("unknown perf command: %s", args[0])
	}
}

func runPerfReady(ctx context.Context, args []string, stdout *os.File) error {
	opts := perfReadyOptions{BootOptions: defaultPerfBootOptions()}
	opts.Iterations = 5
	opts.Warmups = 1
	startMode := "cold"
	probeMode := "interactive"
	summaryOnly := false
	fs := newCommandFlagSet("perf ready")
	fs.StringVar(&opts.StateDir, "state-dir", opts.StateDir, "State directory")
	fs.StringVar(&opts.ImageRef, "image", opts.ImageRef, "Prepared OCI image reference")
	fs.StringVar(&opts.Profile, "profile", opts.Profile, "Resource profile")
	fs.StringVar(&opts.ExecCommand, "exec", opts.ExecCommand, "Interactive shell command used to prove readiness")
	fs.IntVar(&opts.Iterations, "iterations", opts.Iterations, "Number of readiness measurements")
	fs.IntVar(&opts.Warmups, "warmups", opts.Warmups, "Number of excluded full-path warm-up runs")
	fs.BoolVar(&summaryOnly, "summary", false, "Omit per-iteration and host details from JSON output")
	timeoutSeconds := int(opts.Timeout.Seconds())
	fs.IntVar(&timeoutSeconds, "timeout", timeoutSeconds, "Per-iteration timeout in seconds")
	fs.StringVar(&opts.Mke2fsPath, "mke2fs", opts.Mke2fsPath, "mke2fs binary path")
	fs.StringVar(&opts.DebugfsPath, "debugfs", opts.DebugfsPath, "debugfs binary path")
	fs.StringVar(&opts.SupervisorPath, "supervisor", opts.SupervisorPath, "Supervisor path")
	fs.StringVar(&opts.NetworkMode, "network", opts.NetworkMode, networkModePerfFlagHelp)
	fs.StringVar(&startMode, "start", startMode, "Measured lifecycle: cold, snapshot-fork, snapshot-restore, or paused-resume")
	fs.StringVar(&probeMode, "probe", probeMode, "Readiness interface: exec or interactive")
	if err := parseCommandFlags(fs, stdout, reorderFlagArgs(args)); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("unexpected perf ready argument: %s", fs.Arg(0))
	}
	if timeoutSeconds <= 0 {
		return operation.New(operation.ErrorValidation, "perf ready timeout must be positive")
	}
	if opts.Warmups < 0 {
		return operation.New(operation.ErrorValidation, "perf ready warmups must not be negative")
	}
	opts.Timeout = time.Duration(timeoutSeconds) * time.Second
	var err error
	opts.StartMode, err = perf.ParseReadyStartMode(startMode)
	if err != nil {
		return operation.New(operation.ErrorValidation, "%s", err)
	}
	opts.ProbeMode, err = perf.ParseReadyProbeMode(probeMode)
	if err != nil {
		return operation.New(operation.ErrorValidation, "%s", err)
	}
	opts.RootfsBaseline, opts.RootfsBaselineSave = rootfsBaselineHooks(opts.StateDir, strings.TrimSpace(opts.ImageRef), opts.Architecture, defaultGuestInitPath(opts.Architecture))
	hostResp, _ := doctorResponse(ctx, doctorOptions{Backend: hostBackend(), Arch: defaultGuestArch(), SupervisorPath: opts.SupervisorPath})
	opts.Host = hostResp.Host
	var progress *readyProgressPrinter
	if !outputJSON(stdout) {
		writeReadyPreamble(stdout, opts.Backend, opts.Architecture, opts.Profile)
		if enabled, interactive := progressPresentation(stdout); enabled {
			progress = newReadyProgressPrinter(os.Stderr, interactive)
			opts.Progress = progress.print
		}
	}
	report, err := perfReady(ctx, opts)
	if progress != nil {
		progress.close()
	}
	if err != nil {
		return err
	}
	if progress != nil {
		fmt.Fprintln(stdout)
	}
	if err := writeReadyReport(stdout, report, summaryOnly); err != nil {
		return err
	}
	if report.Warmup != nil && (report.Warmup.Summary.Failures > 0 || report.Warmup.Summary.TeardownFailures > 0) {
		return fmt.Errorf("perf ready: warm-up failed; %d measurements completed", report.Summary.Count)
	}
	if report.Summary.Failures > 0 || report.Summary.TeardownFailures > 0 {
		return fmt.Errorf("perf ready: %d of %d measurements failed; %d teardowns failed", report.Summary.Failures, report.Summary.Count, report.Summary.TeardownFailures)
	}
	return nil
}

func runPerfBoot(ctx context.Context, args []string, stdout *os.File) error {
	opts := defaultPerfBootOptions()
	summaryOnly := false
	fs := newCommandFlagSet("perf boot")
	fs.StringVar(&opts.StateDir, "state-dir", opts.StateDir, "State directory")
	fs.StringVar(&opts.ImageRef, "image", opts.ImageRef, "OCI image reference")
	fs.StringVar(&opts.Profile, "profile", opts.Profile, "Resource profile")
	fs.StringVar(&opts.ExecCommand, "exec", opts.ExecCommand, "Guest command used to mark boot completion")
	fs.IntVar(&opts.Iterations, "iterations", opts.Iterations, "Number of boot measurements")
	fs.BoolVar(&summaryOnly, "summary", false, "Omit per-iteration and host details from JSON output")
	timeoutSeconds := int(opts.Timeout.Seconds())
	fs.IntVar(&timeoutSeconds, "timeout", timeoutSeconds, "Per-iteration timeout in seconds")
	fs.StringVar(&opts.Mke2fsPath, "mke2fs", opts.Mke2fsPath, "mke2fs binary path")
	fs.StringVar(&opts.DebugfsPath, "debugfs", opts.DebugfsPath, "debugfs binary path")
	fs.StringVar(&opts.SupervisorPath, "supervisor", opts.SupervisorPath, "Supervisor path")
	fs.StringVar(&opts.NetworkMode, "network", opts.NetworkMode, networkModePerfFlagHelp)
	if err := parseCommandFlags(fs, stdout, reorderFlagArgs(args)); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("unexpected perf boot argument: %s", fs.Arg(0))
	}
	if timeoutSeconds <= 0 {
		return operation.New(operation.ErrorValidation, "perf boot timeout must be positive")
	}
	opts.Timeout = time.Duration(timeoutSeconds) * time.Second
	// Measure the pipeline a real `run` takes: without the image-store hooks
	// every iteration rebuilds the rootfs that a repeat run clones, which
	// reports a first-boot time under the name "boot time".
	opts.RootfsBaseline, opts.RootfsBaselineSave = rootfsBaselineHooks(opts.StateDir, strings.TrimSpace(opts.ImageRef), opts.Architecture, defaultGuestInitPath(opts.Architecture))
	hostResp, _ := doctorResponse(ctx, doctorOptions{Backend: hostBackend(), Arch: defaultGuestArch(), SupervisorPath: opts.SupervisorPath})
	opts.Host = hostResp.Host
	var progress *bootProgressPrinter
	if !outputJSON(stdout) {
		writePerfPreamble(stdout, "Boot", opts.Backend, opts.Architecture, opts.Profile)
		if enabled, interactive := progressPresentation(stdout); enabled {
			progress = newBootProgressPrinter(os.Stderr, interactive)
			opts.Progress = progress.print
		}
	}
	report, err := perfBoot(ctx, opts)
	if progress != nil {
		progress.close()
	}
	if err != nil {
		return err
	}
	if progress != nil {
		fmt.Fprintln(stdout)
	}
	if err := writePerfReport(stdout, report, summaryOnly); err != nil {
		return err
	}
	if report.Summary.Failures > 0 {
		return fmt.Errorf("perf boot: %d of %d iterations failed", report.Summary.Failures, report.Summary.Count)
	}
	return nil
}

func defaultPerfBootOptions() perfBootOptions {
	return perfBootOptions{
		StateDir:       defaultStateDir(),
		ImageRef:       defaultWorkspaceImage(defaultGuestArch()),
		Profile:        defaultWorkspaceProfile,
		ExecCommand:    "true",
		Iterations:     1,
		Timeout:        120 * time.Second,
		Mke2fsPath:     defaultMke2fsPath(),
		DebugfsPath:    defaultDebugFSPath(),
		SupervisorPath: defaultSupervisorPath(hostBackend()),
		Backend:        hostBackend(),
		Architecture:   defaultGuestArch(),
	}
}

func summarizePerfIterations(iterations []perfIteration) perfSummary {
	return perf.SummarizeIterations(iterations)
}

type compactBootReport struct {
	Benchmark      string                   `json:"benchmark"`
	OK             bool                     `json:"ok"`
	Backend        string                   `json:"backend"`
	Arch           string                   `json:"arch"`
	ImageRef       string                   `json:"image_ref"`
	Profile        string                   `json:"profile"`
	Probe          string                   `json:"probe"`
	Boundary       perf.MeasurementBoundary `json:"boundary"`
	CacheCondition string                   `json:"cache_condition"`
	Summary        perf.Summary             `json:"summary"`
}

func writePerfReport(stdout *os.File, report perfReport, summaryOnly bool) error {
	if outputJSON(stdout) {
		if summaryOnly {
			return writeJSON(stdout, compactBootReport{
				Benchmark: report.Benchmark, OK: report.Summary.Failures == 0,
				Backend: report.Backend, Arch: report.Arch, ImageRef: report.ImageRef,
				Profile: report.Profile, Probe: report.Probe, Boundary: report.Boundary,
				CacheCondition: report.CacheCondition, Summary: report.Summary,
			})
		}
		return writeJSON(stdout, report)
	}
	successful := successfulBootIterations(report.Iterations)
	if len(successful) > 0 {
		fmt.Fprintln(stdout, "Boot time")
		if len(successful) == 1 {
			fmt.Fprintf(stdout, "  Result     %9s\n", formatPerfDuration(successful[0].DurationMs))
		} else {
			distribution := bootDurationDistribution(successful)
			fmt.Fprintf(stdout, "  Median     %9s\n", formatPerfDuration(distribution.P50Ms))
			fmt.Fprintf(stdout, "  Range      %9s\n", formatPerfRange(distribution))
			if len(successful) >= 20 {
				fmt.Fprintf(stdout, "  P95        %9s\n", formatPerfDuration(distribution.P95Ms))
			}
		}
	}
	var failed []perf.Iteration
	for _, iteration := range report.Iterations {
		if !iteration.OK {
			failed = append(failed, iteration)
		}
	}
	if len(failed) > 0 {
		fmt.Fprintln(stdout, "\nFailures")
	}
	for _, iteration := range failed {
		fmt.Fprintf(stdout, "  %s: %s", iteration.Name, iteration.Error)
		fmt.Fprintln(stdout)
	}
	fmt.Fprintln(stdout, "\nBenchmark details")
	fmt.Fprintf(stdout, "  Probe         %s\n", report.Probe)
	fmt.Fprintf(stdout, "  Measurements  %d · %s\n", report.Summary.Count, bootOutcomeText(report.Summary))
	fmt.Fprintf(stdout, "  Rootfs        %d baseline · %d build\n", report.Summary.Baselines, report.Summary.Builds)
	fmt.Fprintf(stdout, "  Cache         %s\n", humanizePerfValue(report.CacheCondition))
	fmt.Fprintf(stdout, "  Timer         %s → %s\n", humanizePerfValue(report.Boundary.Start), humanizePerfValue(report.Boundary.Stop))
	fmt.Fprintf(stdout, "  Excluded      %s\n", humanizeReadyExclusions(report.Boundary.Excluded))
	fmt.Fprintf(stdout, "  Image         %s\n", report.ImageRef)
	return nil
}

type compactReadyWarmup struct {
	Excluded         bool `json:"excluded"`
	Count            int  `json:"count"`
	Failures         int  `json:"failures"`
	TeardownFailures int  `json:"teardown_failures"`
	Baselines        int  `json:"baselines"`
	Builds           int  `json:"builds"`
}

type compactReadyReport struct {
	Benchmark      string              `json:"benchmark"`
	OK             bool                `json:"ok"`
	Backend        string              `json:"backend"`
	Arch           string              `json:"arch"`
	ImageRef       string              `json:"image_ref"`
	Profile        string              `json:"profile"`
	StartMode      perf.ReadyStartMode `json:"start_mode"`
	ReadinessProbe perf.ReadyProbeMode `json:"readiness_probe"`
	Probe          string              `json:"probe"`
	Boundary       perf.ReadyBoundary  `json:"boundary"`
	CacheCondition string              `json:"cache_condition"`
	Warmup         *compactReadyWarmup `json:"warmup,omitempty"`
	Summary        perf.ReadySummary   `json:"summary"`
}

func writeReadyReport(stdout *os.File, report perfReadyReport, summaryOnly bool) error {
	if outputJSON(stdout) {
		if summaryOnly {
			compact := compactReadyReport{
				Benchmark:      report.Benchmark,
				OK:             report.Summary.Failures == 0 && report.Summary.TeardownFailures == 0,
				Backend:        report.Backend,
				Arch:           report.Arch,
				ImageRef:       report.ImageRef,
				Profile:        report.Profile,
				StartMode:      report.StartMode,
				ReadinessProbe: report.ReadinessProbe,
				Probe:          report.Probe,
				Boundary:       report.Boundary,
				CacheCondition: report.CacheCondition,
				Summary:        report.Summary,
			}
			if report.Warmup != nil {
				warmup := report.Warmup.Summary
				compact.Warmup = &compactReadyWarmup{
					Excluded:         report.Warmup.Excluded,
					Count:            warmup.Count,
					Failures:         warmup.Failures,
					TeardownFailures: warmup.TeardownFailures,
					Baselines:        warmup.Baselines,
					Builds:           warmup.Builds,
				}
				compact.OK = compact.OK && warmup.Failures == 0 && warmup.TeardownFailures == 0
			}
			return writeJSON(stdout, compact)
		}
		return writeJSON(stdout, report)
	}
	if report.Summary.Count-report.Summary.Failures > 0 {
		fmt.Fprintln(stdout, "Ready time")
		fmt.Fprintf(stdout, "  Median     %9s\n", formatPerfDuration(report.Summary.FullReady.P50Ms))
		fmt.Fprintf(stdout, "  Range      %9s\n", formatPerfRange(report.Summary.FullReady))
		if report.Summary.Count-report.Summary.Failures >= 20 {
			fmt.Fprintf(stdout, "  P95        %9s\n", formatPerfDuration(report.Summary.FullReady.P95Ms))
		}
		if median, ok := medianReadyIteration(report.Iterations); ok {
			writeMedianReadyBreakdown(stdout, report, median)
		}
	}

	var failed []perf.ReadyIteration
	if report.Warmup != nil {
		for _, iteration := range report.Warmup.Iterations {
			if !iteration.OK || iteration.TeardownError != "" {
				failed = append(failed, iteration)
			}
		}
	}
	for _, iteration := range report.Iterations {
		if !iteration.OK || iteration.TeardownError != "" {
			failed = append(failed, iteration)
		}
	}
	if len(failed) > 0 {
		fmt.Fprintln(stdout, "\nFailures")
	}
	for _, iteration := range failed {
		fmt.Fprintf(stdout, "  %s", iteration.Name)
		if iteration.Error != "" {
			fmt.Fprintf(stdout, ": %s", iteration.Error)
		}
		if iteration.TeardownError != "" {
			fmt.Fprintf(stdout, ": cleanup: %s", iteration.TeardownError)
		}
		fmt.Fprintln(stdout)
	}
	fmt.Fprintln(stdout, "\nBenchmark details")
	fmt.Fprintf(stdout, "  Path          %s → %s\n", humanizePerfValue(string(report.StartMode)), humanizePerfValue(string(report.ReadinessProbe)))
	fmt.Fprintf(stdout, "  Probe         %s\n", report.Probe)
	if report.Setup != nil {
		fmt.Fprintf(stdout, "  Setup         %s · not measured\n", formatPerfDuration(report.Setup.DurationMs))
	}
	if report.Warmup != nil {
		fmt.Fprintf(stdout, "  Warm-up       %d · %s\n", report.Warmup.Summary.Count, readyOutcomeText(report.Warmup.Summary))
	}
	fmt.Fprintf(stdout, "  Measurements  %d · %s\n", report.Summary.Count, readyOutcomeText(report.Summary))
	fmt.Fprintf(stdout, "  Rootfs        %d baseline · %d build\n", report.Summary.Baselines, report.Summary.Builds)
	fmt.Fprintf(stdout, "  Cache         %s\n", humanizePerfValue(report.CacheCondition))
	fmt.Fprintf(stdout, "  Timer         %s → %s\n", humanizePerfValue(report.Boundary.Start), humanizePerfValue(report.Boundary.Stop))
	fmt.Fprintf(stdout, "  Excluded      %s\n", humanizeReadyExclusions(report.Boundary.Excluded))
	fmt.Fprintf(stdout, "  Image         %s\n", report.ImageRef)
	return nil
}

func writeReadyPreamble(stdout *os.File, backend, arch, profile string) {
	writePerfPreamble(stdout, "Ready", backend, arch, profile)
}

func writePerfPreamble(stdout *os.File, benchmark, backend, arch, profile string) {
	fmt.Fprintf(stdout, "%s benchmark — %s / %s / %s\n\n", benchmark, displayPerfValue(backend), displayPerfValue(arch), displayPerfValue(profile))
}

func medianReadyIteration(iterations []perf.ReadyIteration) (perf.ReadyIteration, bool) {
	successful := make([]perf.ReadyIteration, 0, len(iterations))
	for _, iteration := range iterations {
		if iteration.OK {
			successful = append(successful, iteration)
		}
	}
	if len(successful) == 0 {
		return perf.ReadyIteration{}, false
	}
	sort.SliceStable(successful, func(i, j int) bool { return successful[i].DurationMs < successful[j].DurationMs })
	index := (50*len(successful)+99)/100 - 1
	return successful[index], true
}

func writeMedianReadyBreakdown(stdout *os.File, report perfReadyReport, iteration perf.ReadyIteration) {
	workspacePrepare := iteration.Phases.WorkspacePrepareMs
	lifecycleTransition := iteration.Phases.LifecycleMs - workspacePrepare
	if lifecycleTransition < 0 {
		lifecycleTransition = 0
	}
	interfaceLabel := "Interactive shell"
	if report.ReadinessProbe == perf.ReadyProbeStructuredExec {
		interfaceLabel = "Structured exec"
	}
	fmt.Fprintln(stdout, "\nMedian run breakdown")
	if workspacePrepare > 0 {
		fmt.Fprintf(stdout, "  Workspace preparation %9s\n", formatPerfDuration(workspacePrepare))
	}
	fmt.Fprintf(stdout, "  Lifecycle transition  %9s\n", formatPerfDuration(lifecycleTransition))
	fmt.Fprintf(stdout, "  %-21s %9s\n", interfaceLabel, formatPerfDuration(iteration.Phases.InterfaceReadyMs))
	fmt.Fprintf(stdout, "  Probe                 %9s\n", formatPerfDuration(iteration.Phases.ProbeMs))
}

func displayPerfValue(value string) string {
	if strings.TrimSpace(value) == "" {
		return "unknown"
	}
	return value
}

func humanizePerfValue(value string) string {
	return strings.ReplaceAll(value, "_", " ")
}

func humanizeReadyExclusions(values []string) string {
	humanized := make([]string, len(values))
	for i, value := range values {
		switch value {
		case "iteration_teardown":
			humanized[i] = "workspace cleanup"
		case "warmup_runs":
			humanized[i] = "warm-up runs"
		default:
			humanized[i] = humanizePerfValue(value)
		}
	}
	return strings.Join(humanized, ", ")
}

func readyOutcomeText(summary perf.ReadySummary) string {
	passed := summary.Count - summary.Failures
	if summary.Failures == 0 && summary.TeardownFailures == 0 {
		return fmt.Sprintf("%d passed · cleanup clean", passed)
	}
	return fmt.Sprintf("%d passed · %d failed · %d cleanup failed", passed, summary.Failures, summary.TeardownFailures)
}

func bootOutcomeText(summary perf.Summary) string {
	passed := summary.Count - summary.Failures
	if summary.Failures == 0 {
		return fmt.Sprintf("%d passed", passed)
	}
	return fmt.Sprintf("%d passed · %d failed", passed, summary.Failures)
}

func successfulBootIterations(iterations []perf.Iteration) []perf.Iteration {
	successful := make([]perf.Iteration, 0, len(iterations))
	for _, iteration := range iterations {
		if iteration.OK {
			successful = append(successful, iteration)
		}
	}
	return successful
}

func bootDurationDistribution(iterations []perf.Iteration) perf.Distribution {
	values := make([]int64, 0, len(iterations))
	for _, iteration := range iterations {
		values = append(values, iteration.DurationMs)
	}
	sort.Slice(values, func(i, j int) bool { return values[i] < values[j] })
	if len(values) == 0 {
		return perf.Distribution{}
	}
	return perf.Distribution{
		MinMs: values[0],
		P50Ms: values[(50*len(values)+99)/100-1],
		P95Ms: values[(95*len(values)+99)/100-1],
		MaxMs: values[len(values)-1],
	}
}

func formatPerfDuration(ms int64) string {
	return formatProgressDuration(time.Duration(ms) * time.Millisecond)
}

func formatPerfRange(distribution perf.Distribution) string {
	return fmt.Sprintf("%s–%s", formatPerfDuration(distribution.MinMs), formatPerfDuration(distribution.MaxMs))
}

func formatPerfMemory(kib int64) string {
	const unit = 1024
	if kib < unit {
		return fmt.Sprintf("%dKiB", kib)
	}
	mib := float64(kib) / unit
	if mib < unit {
		return fmt.Sprintf("%.1fMiB", mib)
	}
	return fmt.Sprintf("%.2fGiB", mib/unit)
}

func formatPerfMemoryRange(minKiB, maxKiB int64) string {
	return fmt.Sprintf("%s–%s", formatPerfMemory(minKiB), formatPerfMemory(maxKiB))
}

func formatPerfSampleCount(count int) string {
	if count == 1 {
		return "1 sample"
	}
	return fmt.Sprintf("%d samples", count)
}

type readyProgressPrinter struct {
	printer *operationProgressPrinter
	label   func(perf.ReadyProgressEvent) string
	detail  func(perf.ReadyProgressEvent) string
}

type bootProgressPrinter struct {
	printer *readyProgressPrinter
}

type steadyProgressPrinter struct {
	printer *readyProgressPrinter
}

func newBootProgressPrinter(out io.Writer, interactive bool) *bootProgressPrinter {
	return &bootProgressPrinter{printer: newReadyProgressPrinter(out, interactive)}
}

func (p *bootProgressPrinter) print(event perf.BootProgressEvent) {
	phase := perf.ReadyProgressWorkspacePrepare
	switch event.Phase {
	case perf.BootProgressTeardown:
		phase = perf.ReadyProgressTeardown
	case perf.BootProgressComplete:
		phase = perf.ReadyProgressComplete
	}
	p.printer.print(perf.ReadyProgressEvent{
		Run: perf.ReadyProgressMeasurement, Index: event.Index, Total: event.Total,
		Phase: phase, Message: event.Message, ElapsedMs: event.ElapsedMs,
		OK: event.OK, Error: event.Error, Rootfs: event.Rootfs,
	})
}

func (p *bootProgressPrinter) close() {
	p.printer.close()
}

func newSteadyProgressPrinter(out io.Writer, interactive bool) *steadyProgressPrinter {
	printer := newReadyProgressPrinter(out, interactive)
	printer.label = func(perf.ReadyProgressEvent) string { return "Sampling memory" }
	printer.detail = func(event perf.ReadyProgressEvent) string { return event.Message }
	return &steadyProgressPrinter{printer: printer}
}

func (p *steadyProgressPrinter) print(event perf.SteadyProgressEvent) {
	phase := perf.ReadyProgressInterface
	message := formatPerfSampleCount(event.SampleCount)
	if !event.Complete && event.SampleCount > 0 {
		message += " · " + formatPerfMemory(event.Sample.RSSKiB)
	}
	if event.Complete {
		phase = perf.ReadyProgressComplete
	}
	p.printer.print(perf.ReadyProgressEvent{
		Run: perf.ReadyProgressMeasurement, Index: 1, Total: 1,
		Phase: phase, Message: message, ElapsedMs: event.ElapsedMs,
		OK: event.OK, Error: event.Error,
	})
}

func (p *steadyProgressPrinter) close() {
	p.printer.close()
}

func newReadyProgressPrinter(out io.Writer, interactive bool) *readyProgressPrinter {
	return &readyProgressPrinter{printer: newOperationProgressPrinter(out, interactive, progressPrinterOptions{
		AlwaysPrintCompletion: true,
	})}
}

func (p *readyProgressPrinter) print(event perf.ReadyProgressEvent) {
	progress := operation.ProgressEvent{
		Operation: fmt.Sprintf("perf-ready-%s-%d", event.Run, event.Index),
		Phase:     string(event.Phase),
		Label:     p.progressLabel(event),
		Message:   strings.TrimSpace(event.Message),
		Error:     event.Error,
	}
	// Running phases use the renderer clock so the elapsed value continues to
	// advance between library updates. Teardown intentionally retains the
	// measured duration, and terminal events use the authoritative result.
	if event.Phase == perf.ReadyProgressTeardown || event.Phase == perf.ReadyProgressComplete {
		progress.ElapsedMs = event.ElapsedMs
	}
	if event.Rootfs != nil {
		progress.Message = strings.TrimSpace(event.Rootfs.Message)
		progress.Current = event.Rootfs.Current
		progress.Total = event.Rootfs.Total
		progress.Bytes = event.Rootfs.Bytes
		progress.TotalBytes = event.Rootfs.TotalBytes
		progress.Indeterminate = event.Rootfs.Indeterminate
	}
	if event.Phase == perf.ReadyProgressComplete {
		progress.Message = ""
		if p.detail != nil {
			progress.Message = strings.TrimSpace(p.detail(event))
		}
		progress.Status = operation.ProgressFailed
		if event.OK {
			progress.Status = operation.ProgressSucceeded
		}
	} else if p.detail != nil {
		progress.Message = strings.TrimSpace(p.detail(event))
	}
	p.printer.print(progress)
}

func (p *readyProgressPrinter) close() {
	p.printer.close()
}

func readyProgressLabel(event perf.ReadyProgressEvent) string {
	switch event.Run {
	case perf.ReadyProgressSetup:
		return "Setup"
	case perf.ReadyProgressWarmup:
		return fmt.Sprintf("Warm-up %d/%d", event.Index, event.Total)
	default:
		return fmt.Sprintf("Measurement %d/%d", event.Index, event.Total)
	}
}

func (p *readyProgressPrinter) progressLabel(event perf.ReadyProgressEvent) string {
	if p.label != nil {
		return p.label(event)
	}
	return readyProgressLabel(event)
}

func runPerfFootprint(args []string, stdout *os.File) error {
	opts := stateCommandOptions{StateDir: defaultStateDir()}
	fs := newCommandFlagSet("perf footprint")
	fs.StringVar(&opts.StateDir, "state-dir", opts.StateDir, "State directory")
	if err := parseCommandFlags(fs, stdout, reorderFlagArgs(args)); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("usage: microagent perf footprint <name> [--state-dir <dir>]")
	}
	name := fs.Arg(0)
	if err := validateWorkspaceName(name); err != nil {
		return err
	}
	report, err := perf.Footprint(opts.StateDir, name)
	if err != nil {
		return err
	}
	return writePerfFootprintReport(stdout, report)
}

func parseRSSKiB(output []byte) (int64, error) {
	return perf.ParseRSSKiB(output)
}

func writePerfFootprintReport(stdout *os.File, report perfFootprintReport) error {
	if outputJSON(stdout) {
		return writeJSON(stdout, report)
	}
	fmt.Fprintf(stdout, "Footprint benchmark — %s / %s\n\n", report.Workspace, displayPerfValue(report.Backend))
	fmt.Fprintln(stdout, "Resident memory")
	fmt.Fprintf(stdout, "  RSS        %9s\n", formatPerfMemory(report.RSSKiB))
	fmt.Fprintln(stdout, "\nBenchmark details")
	fmt.Fprintf(stdout, "  Workspace  %s\n", report.Workspace)
	fmt.Fprintf(stdout, "  Backend    %s\n", displayPerfValue(report.Backend))
	fmt.Fprintf(stdout, "  State      %s\n", humanizePerfValue(report.State))
	fmt.Fprintf(stdout, "  Process    PID %d\n", report.PID)
	return nil
}

func runPerfSteady(ctx context.Context, args []string, stdout *os.File) error {
	opts := stateCommandOptions{StateDir: defaultStateDir()}
	durationSeconds := 10
	intervalSeconds := 1
	summaryOnly := false
	fs := newCommandFlagSet("perf steady")
	fs.StringVar(&opts.StateDir, "state-dir", opts.StateDir, "State directory")
	fs.IntVar(&durationSeconds, "duration", durationSeconds, "Sampling duration in seconds")
	fs.IntVar(&intervalSeconds, "interval", intervalSeconds, "Sampling interval in seconds")
	fs.BoolVar(&summaryOnly, "summary", false, "Omit individual samples from JSON output")
	if err := parseCommandFlags(fs, stdout, reorderFlagArgs(args)); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("usage: microagent perf steady <name> [--state-dir <dir>]")
	}
	name := fs.Arg(0)
	if err := validateWorkspaceName(name); err != nil {
		return err
	}
	if durationSeconds <= 0 {
		return operation.New(operation.ErrorValidation, "perf steady duration must be positive")
	}
	if intervalSeconds <= 0 {
		return operation.New(operation.ErrorValidation, "perf steady interval must be positive")
	}
	if intervalSeconds > durationSeconds {
		return operation.New(operation.ErrorValidation, "perf steady interval must be less than or equal to duration")
	}
	steadyOpts := perf.SteadyOptions{
		StateDir: opts.StateDir, Name: name,
		Duration: time.Duration(durationSeconds) * time.Second,
		Interval: time.Duration(intervalSeconds) * time.Second,
	}
	var progress *steadyProgressPrinter
	if !outputJSON(stdout) {
		fmt.Fprintf(stdout, "Steady memory benchmark — %s\n\n", name)
		if enabled, interactive := progressPresentation(stdout); enabled {
			progress = newSteadyProgressPrinter(os.Stderr, interactive)
			steadyOpts.Progress = progress.print
		}
	}
	report, err := perfSteady(ctx, steadyOpts)
	if progress != nil {
		progress.close()
	}
	if err != nil {
		return err
	}
	if progress != nil {
		fmt.Fprintln(stdout)
	}
	return writePerfSteadyReport(stdout, report, summaryOnly)
}

func summarizeRSSSamples(samples []perfRSSSample) perfRSSSummary {
	return perf.SummarizeRSSSamples(samples)
}

type compactSteadyReport struct {
	Benchmark       string          `json:"benchmark"`
	OK              bool            `json:"ok"`
	Workspace       string          `json:"workspace"`
	Backend         string          `json:"backend"`
	PID             int             `json:"pid"`
	State           string          `json:"state"`
	DurationSeconds int             `json:"duration_seconds"`
	IntervalSeconds int             `json:"interval_seconds"`
	Summary         perf.RSSSummary `json:"summary"`
}

func writePerfSteadyReport(stdout *os.File, report perfSteadyReport, summaryOnly bool) error {
	if outputJSON(stdout) {
		if summaryOnly {
			return writeJSON(stdout, compactSteadyReport{
				Benchmark: report.Benchmark, OK: true, Workspace: report.Workspace,
				Backend: report.Backend, PID: report.PID, State: report.State,
				DurationSeconds: report.DurationSeconds, IntervalSeconds: report.IntervalSeconds,
				Summary: report.Summary,
			})
		}
		return writeJSON(stdout, report)
	}
	fmt.Fprintln(stdout, "Steady memory")
	fmt.Fprintf(stdout, "  Average    %9s\n", formatPerfMemory(report.Summary.AvgKiB))
	fmt.Fprintf(stdout, "  Range      %9s\n", formatPerfMemoryRange(report.Summary.MinKiB, report.Summary.MaxKiB))
	fmt.Fprintln(stdout, "\nBenchmark details")
	fmt.Fprintf(stdout, "  Workspace  %s\n", report.Workspace)
	fmt.Fprintf(stdout, "  Backend    %s\n", displayPerfValue(report.Backend))
	fmt.Fprintf(stdout, "  State      %s\n", humanizePerfValue(report.State))
	fmt.Fprintf(stdout, "  Process    PID %d\n", report.PID)
	fmt.Fprintf(stdout, "  Sampling   %s · every %ds for %ds\n", formatPerfSampleCount(report.Summary.Count), report.IntervalSeconds, report.DurationSeconds)
	return nil
}

func printPerfHelp(stdout *os.File) {
	printGroupHelpHeader(stdout, "perf")
	printUsageBlock(stdout, "perf", "perf")
	fmt.Fprint(stdout, `
Measure workspace performance.

Commands:
  boot                 Measure disposable workspace boot time
  ready                Measure full readiness across lifecycle and guest interfaces
  footprint            Report host process RSS for a running workspace
  steady               Sample host process RSS over time

Boot options:
  -image <ref>          OCI image; defaults to Python 3.13 slim
  -exec <command>       Guest command used to mark boot completion; defaults to true
  -iterations <n>       Number of boot measurements
  -summary              Omit per-iteration and host details from JSON output
  -profile <name>       Resource profile: tiny, small, medium, or large
  -state-dir <dir>      State directory
  -timeout <seconds>    Per-iteration timeout
  -mke2fs <path>        mke2fs binary path
  -supervisor <path>    Override the supervisor path
  -network <mode>       Network mode for measured boots:
                         user (rootless, unprivileged user namespace) or
                         isolated (no network); empty uses the backend default

Ready options:
  Same as boot. -start selects cold, snapshot-fork, snapshot-restore, or
  paused-resume. -probe selects structured exec or the interactive shell.
  Source preparation and teardown are reported but excluded from iterations.
  -start <mode>        Lifecycle transition to measure (default cold)
  -probe <interface>   Guest interface: exec or interactive (default interactive)
  -warmups <n>         Excluded full-path warm-up runs (default 1)
  -summary             Omit per-iteration and host details from JSON output

Footprint options:
  -state-dir <dir>      State directory

Steady options:
  -duration <seconds>   Sampling duration
  -interval <seconds>   Sampling interval
  -state-dir <dir>      State directory
  -summary              Omit individual samples from JSON output
`)
}
