package main

import (
	"context"
	"fmt"
	"os"
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
	startMode := "cold"
	probeMode := "interactive"
	fs := newCommandFlagSet("perf ready")
	fs.StringVar(&opts.StateDir, "state-dir", opts.StateDir, "State directory")
	fs.StringVar(&opts.ImageRef, "image", opts.ImageRef, "Prepared OCI image reference")
	fs.StringVar(&opts.Profile, "profile", opts.Profile, "Resource profile")
	fs.StringVar(&opts.ExecCommand, "exec", opts.ExecCommand, "Interactive shell command used to prove readiness")
	fs.IntVar(&opts.Iterations, "iterations", opts.Iterations, "Number of readiness measurements")
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
	report, err := perfReady(ctx, opts)
	if err != nil {
		return err
	}
	if err := writeReadyReport(stdout, report); err != nil {
		return err
	}
	if report.Summary.Failures > 0 {
		return fmt.Errorf("perf ready: %d of %d iterations failed", report.Summary.Failures, report.Summary.Count)
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

func writeReadyReport(stdout *os.File, report perfReadyReport) error {
	if outputJSON(stdout) {
		return writeJSON(stdout, report)
	}
	fmt.Fprintf(stdout, "Benchmark: %s\n", report.Benchmark)
	fmt.Fprintf(stdout, "Backend: %s\n", report.Backend)
	fmt.Fprintf(stdout, "Arch: %s\n", report.Arch)
	fmt.Fprintf(stdout, "Image: %s\n", report.ImageRef)
	fmt.Fprintf(stdout, "Profile: %s\n", report.Profile)
	fmt.Fprintf(stdout, "Start mode: %s\n", report.StartMode)
	fmt.Fprintf(stdout, "Readiness probe: %s\n", report.ReadinessProbe)
	fmt.Fprintf(stdout, "Probe: %s\n", report.Probe)
	fmt.Fprintf(stdout, "Timer: %s -> %s\n", report.Boundary.Start, report.Boundary.Stop)
	fmt.Fprintf(stdout, "Excluded: %s\n", strings.Join(report.Boundary.Excluded, ", "))
	fmt.Fprintf(stdout, "Cache condition: %s\n", report.CacheCondition)
	if report.Setup != nil {
		fmt.Fprintf(stdout, "Setup (excluded): duration=%dms rootfs=%s rootfs_prepare=%dms snapshot=%s readiness_probe=%s\n",
			report.Setup.DurationMs, report.Setup.Rootfs, report.Setup.RootfsPrepareMs, report.Setup.SnapshotTag, report.Setup.ReadinessProbe)
	}
	fmt.Fprintf(stdout, "Iterations: %d\n", report.Summary.Count)
	if report.Summary.Failures > 0 {
		fmt.Fprintf(stdout, "Failed: %d\n", report.Summary.Failures)
	}
	fmt.Fprintf(stdout, "Rootfs: baseline=%d build=%d\n", report.Summary.Baselines, report.Summary.Builds)
	writeDistribution := func(label string, distribution perf.Distribution) {
		fmt.Fprintf(stdout, "%s ms: min=%d avg=%d p50=%d p95=%d max=%d\n", label, distribution.MinMs, distribution.AvgMs, distribution.P50Ms, distribution.P95Ms, distribution.MaxMs)
	}
	writeDistribution("Full ready", report.Summary.FullReady)
	writeDistribution("Runtime ready", report.Summary.RuntimeReady)
	writeDistribution("Rootfs prepare", report.Summary.RootfsPrepare)
	writeDistribution("Workspace prepare", report.Summary.WorkspacePrepare)
	writeDistribution("Lifecycle", report.Summary.Lifecycle)
	writeDistribution("Interface ready", report.Summary.InterfaceReady)
	writeDistribution("Probe", report.Summary.Probe)
	for _, iteration := range report.Iterations {
		status := "ok"
		if !iteration.OK {
			status = "failed"
		}
		rootfsSource := iteration.Rootfs
		if rootfsSource == "" {
			rootfsSource = "-"
		}
		fmt.Fprintf(stdout, "%-29s %-8s %-8s total=%d rootfs=%d prepare=%d lifecycle=%d interface=%d runtime=%d probe=%d",
			iteration.Name, status, rootfsSource, iteration.DurationMs,
			iteration.Phases.RootfsPrepareMs, iteration.Phases.WorkspacePrepareMs,
			iteration.Phases.LifecycleMs, iteration.Phases.InterfaceReadyMs,
			iteration.Phases.RuntimeReadyMs, iteration.Phases.ProbeMs)
		if iteration.Error != "" {
			fmt.Fprintf(stdout, " %s", iteration.Error)
		}
		fmt.Fprintln(stdout)
	}
	return nil
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
