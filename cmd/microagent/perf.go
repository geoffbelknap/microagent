package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"sync"
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
		progress = newReadyProgressPrinter(os.Stderr, fileIsTerminal(os.Stderr))
		opts.Progress = progress.print
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
	fs := newCommandFlagSet("perf boot")
	fs.StringVar(&opts.StateDir, "state-dir", opts.StateDir, "State directory")
	fs.StringVar(&opts.ImageRef, "image", opts.ImageRef, "OCI image reference")
	fs.StringVar(&opts.Profile, "profile", opts.Profile, "Resource profile")
	fs.StringVar(&opts.ExecCommand, "exec", opts.ExecCommand, "Guest command used to mark boot completion")
	fs.IntVar(&opts.Iterations, "iterations", opts.Iterations, "Number of boot measurements")
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
	report, err := perfBoot(ctx, opts)
	if err != nil {
		return err
	}
	if err := writePerfReport(stdout, report); err != nil {
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

func writePerfReport(stdout *os.File, report perfReport) error {
	if outputJSON(stdout) {
		return writeJSON(stdout, report)
	}
	fmt.Fprintf(stdout, "Benchmark: %s\n", report.Benchmark)
	fmt.Fprintf(stdout, "Backend: %s\n", report.Backend)
	fmt.Fprintf(stdout, "Arch: %s\n", report.Arch)
	fmt.Fprintf(stdout, "Image: %s\n", report.ImageRef)
	fmt.Fprintf(stdout, "Profile: %s\n", report.Profile)
	fmt.Fprintf(stdout, "Timer: %s -> %s\n", report.Boundary.Start, report.Boundary.Stop)
	fmt.Fprintf(stdout, "Excluded: %s\n", strings.Join(report.Boundary.Excluded, ", "))
	fmt.Fprintf(stdout, "Cache condition: %s\n", report.CacheCondition)
	fmt.Fprintf(stdout, "Iterations: %d\n", report.Summary.Count)
	if report.Summary.Failures > 0 {
		fmt.Fprintf(stdout, "Failed: %d\n", report.Summary.Failures)
	}
	fmt.Fprintf(stdout, "Rootfs: baseline=%d build=%d\n", report.Summary.Baselines, report.Summary.Builds)
	fmt.Fprintf(stdout, "Boot ms: min=%d avg=%d max=%d\n", report.Summary.MinMs, report.Summary.AvgMs, report.Summary.MaxMs)
	for _, iteration := range report.Iterations {
		status := "ok"
		if !iteration.OK {
			status = "failed"
		}
		rootfsSource := iteration.Rootfs
		if rootfsSource == "" {
			rootfsSource = "-"
		}
		fmt.Fprintf(stdout, "%-28s %-8s %-8s %d", iteration.Name, status, rootfsSource, iteration.DurationMs)
		if iteration.Error != "" {
			fmt.Fprintf(stdout, " %s", iteration.Error)
		}
		fmt.Fprintln(stdout)
	}
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
	fmt.Fprintf(stdout, "Ready benchmark — %s / %s / %s\n\n", displayPerfValue(backend), displayPerfValue(arch), displayPerfValue(profile))
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

func formatPerfDuration(ms int64) string {
	if ms < 1000 {
		return fmt.Sprintf("%dms", ms)
	}
	return fmt.Sprintf("%.2fs", float64(ms)/1000)
}

func formatPerfRange(distribution perf.Distribution) string {
	return fmt.Sprintf("%s–%s", formatPerfDuration(distribution.MinMs), formatPerfDuration(distribution.MaxMs))
}

type readyProgressPrinter struct {
	out         io.Writer
	interactive bool
	events      chan perf.ReadyProgressEvent
	done        chan struct{}
	closeOnce   sync.Once
}

func newReadyProgressPrinter(out io.Writer, interactive bool) *readyProgressPrinter {
	p := &readyProgressPrinter{
		out:         out,
		interactive: interactive,
		events:      make(chan perf.ReadyProgressEvent, 32),
		done:        make(chan struct{}),
	}
	go p.run()
	return p
}

func (p *readyProgressPrinter) print(event perf.ReadyProgressEvent) {
	p.events <- event
}

func (p *readyProgressPrinter) close() {
	p.closeOnce.Do(func() {
		close(p.events)
		<-p.done
	})
}

func (p *readyProgressPrinter) run() {
	defer close(p.done)
	if !p.interactive {
		for event := range p.events {
			if event.Phase == perf.ReadyProgressComplete {
				fmt.Fprintln(p.out, readyProgressText(event, 0))
			} else {
				fmt.Fprintf(p.out, "• %s · %s\n", readyProgressLabel(event), readyProgressMessage(event, 0))
			}
		}
		return
	}

	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	frames := []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}
	frame := 0
	var current *perf.ReadyProgressEvent
	var runStarted time.Time
	var currentRun perf.ReadyProgressRun
	var currentIndex int
	for {
		select {
		case event, ok := <-p.events:
			if !ok {
				if current != nil {
					fmt.Fprint(p.out, "\r\033[2K")
				}
				return
			}
			if event.Run != currentRun || event.Index != currentIndex {
				runStarted = time.Now()
				currentRun = event.Run
				currentIndex = event.Index
			}
			if event.Phase == perf.ReadyProgressComplete {
				fmt.Fprintf(p.out, "\r\033[2K%s\n", readyProgressText(event, time.Since(runStarted)))
				current = nil
				continue
			}
			current = &event
			fmt.Fprintf(p.out, "\r\033[2K%s [%6s] %s · %s", frames[frame], formatPerfDuration(readyProgressElapsed(event, runStarted)), readyProgressLabel(event), readyProgressMessage(event, 0))
		case <-ticker.C:
			if current == nil {
				continue
			}
			frame = (frame + 1) % len(frames)
			fmt.Fprintf(p.out, "\r\033[2K%s [%6s] %s · %s", frames[frame], formatPerfDuration(readyProgressElapsed(*current, runStarted)), readyProgressLabel(*current), readyProgressMessage(*current, 0))
		}
	}
}

func readyProgressElapsed(event perf.ReadyProgressEvent, runStarted time.Time) int64 {
	if event.Phase == perf.ReadyProgressTeardown && event.ElapsedMs > 0 {
		return event.ElapsedMs
	}
	return time.Since(runStarted).Milliseconds()
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

func readyProgressText(event perf.ReadyProgressEvent, elapsed time.Duration) string {
	label := readyProgressLabel(event)
	if event.Phase != perf.ReadyProgressComplete {
		return readyProgressMessage(event, elapsed)
	}
	mark := "✓"
	if !event.OK {
		mark = "✗"
	}
	text := fmt.Sprintf("%s [%6s] %s", mark, formatPerfDuration(event.ElapsedMs), label)
	if event.Error != "" {
		text += " · " + event.Error
	}
	return text
}

func readyProgressMessage(event perf.ReadyProgressEvent, elapsed time.Duration) string {
	message := strings.TrimSpace(event.Message)
	if event.Rootfs != nil {
		if event.Rootfs.Indeterminate {
			message = strings.TrimSpace(event.Rootfs.Message)
		} else {
			message = formatProgressEvent(*event.Rootfs)
		}
	}
	if message == "" {
		message = strings.ReplaceAll(string(event.Phase), "_", " ")
	}
	if elapsed > 0 {
		message += " · " + formatPerfDuration(elapsed.Milliseconds())
	}
	return message
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
	fmt.Fprintf(stdout, "Benchmark: %s\n", report.Benchmark)
	fmt.Fprintf(stdout, "Workspace: %s\n", report.Workspace)
	fmt.Fprintf(stdout, "Backend: %s\n", report.Backend)
	fmt.Fprintf(stdout, "State: %s\n", report.State)
	fmt.Fprintf(stdout, "PID: %d\n", report.PID)
	fmt.Fprintf(stdout, "RSS KiB: %d\n", report.RSSKiB)
	return nil
}

func runPerfSteady(ctx context.Context, args []string, stdout *os.File) error {
	opts := stateCommandOptions{StateDir: defaultStateDir()}
	durationSeconds := 10
	intervalSeconds := 1
	fs := newCommandFlagSet("perf steady")
	fs.StringVar(&opts.StateDir, "state-dir", opts.StateDir, "State directory")
	fs.IntVar(&durationSeconds, "duration", durationSeconds, "Sampling duration in seconds")
	fs.IntVar(&intervalSeconds, "interval", intervalSeconds, "Sampling interval in seconds")
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
	report, err := perf.Steady(ctx, opts.StateDir, name, time.Duration(durationSeconds)*time.Second, time.Duration(intervalSeconds)*time.Second)
	if err != nil {
		return err
	}
	return writePerfSteadyReport(stdout, report)
}

func summarizeRSSSamples(samples []perfRSSSample) perfRSSSummary {
	return perf.SummarizeRSSSamples(samples)
}

func writePerfSteadyReport(stdout *os.File, report perfSteadyReport) error {
	if outputJSON(stdout) {
		return writeJSON(stdout, report)
	}
	fmt.Fprintf(stdout, "Benchmark: %s\n", report.Benchmark)
	fmt.Fprintf(stdout, "Workspace: %s\n", report.Workspace)
	fmt.Fprintf(stdout, "Backend: %s\n", report.Backend)
	fmt.Fprintf(stdout, "State: %s\n", report.State)
	fmt.Fprintf(stdout, "PID: %d\n", report.PID)
	fmt.Fprintf(stdout, "Samples: %d\n", report.Summary.Count)
	fmt.Fprintf(stdout, "RSS KiB: min=%d avg=%d max=%d\n", report.Summary.MinKiB, report.Summary.AvgKiB, report.Summary.MaxKiB)
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

Footprint options:
  -state-dir <dir>      State directory

Steady options:
  -duration <seconds>   Sampling duration
  -interval <seconds>   Sampling interval
  -state-dir <dir>      State directory
`)
}
