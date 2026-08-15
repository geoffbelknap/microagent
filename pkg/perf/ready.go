package perf

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/geoffbelknap/microagent/pkg/rootfs"
	"github.com/geoffbelknap/microagent/pkg/vmkit"
	"github.com/geoffbelknap/microagent/pkg/workspace"
	execprotocol "github.com/geoffbelknap/microagent/pkg/workspace/exec/protocol"
)

const (
	readyProbeMarker    = "__MICROAGENT_READY_PROBE_OK__"
	readySnapshotTag    = "perf-ready-baseline"
	readyCleanupTimeout = 45 * time.Second
)

// ReadyStartMode names the lifecycle transition whose time is included in a
// readiness measurement. Setup needed to create a reusable snapshot or paused
// workspace is reported separately and never included in an iteration.
type ReadyStartMode string

const (
	ReadyStartColdBoot        ReadyStartMode = "cold_boot"
	ReadyStartSnapshotFork    ReadyStartMode = "snapshot_fork"
	ReadyStartSnapshotRestore ReadyStartMode = "snapshot_restore"
	ReadyStartPausedResume    ReadyStartMode = "paused_resume"
)

// ReadyProbeMode names the guest interface that must accept and successfully
// complete a command before the workspace is reported fully ready.
type ReadyProbeMode string

const (
	ReadyProbeStructuredExec   ReadyProbeMode = "structured_exec"
	ReadyProbeInteractiveShell ReadyProbeMode = "interactive_shell"
)

// ReadyOptions configures the full-readiness benchmark. BootOptions supplies
// the image, host, backend, baseline, and iteration fields. Empty modes retain
// the original perf-ready behavior: a cold boot through an interactive command.
type ReadyOptions struct {
	BootOptions
	StartMode ReadyStartMode
	ProbeMode ReadyProbeMode
}

type ReadyReport struct {
	Benchmark      string             `json:"benchmark"`
	Backend        string             `json:"backend"`
	Arch           string             `json:"arch"`
	ImageRef       string             `json:"image_ref"`
	Profile        string             `json:"profile"`
	StartMode      ReadyStartMode     `json:"start_mode"`
	ReadinessProbe ReadyProbeMode     `json:"readiness_probe"`
	Probe          string             `json:"probe"`
	Boundary       ReadyBoundary      `json:"boundary"`
	CacheCondition string             `json:"cache_condition"`
	Setup          *ReadySetup        `json:"setup,omitempty"`
	Iterations     []ReadyIteration   `json:"iterations"`
	Summary        ReadySummary       `json:"summary"`
	Host           *vmkit.HostSupport `json:"host,omitempty"`
}

// ReadyBoundary retains the readiness-specific API name while sharing the
// same machine-readable timer contract with the boot benchmark.
type ReadyBoundary = MeasurementBoundary

// ReadySetup describes one-time preparation for a restore or resume benchmark.
// DurationMs is observable but excluded from all iteration distributions.
type ReadySetup struct {
	DurationMs      int64          `json:"duration_ms"`
	Rootfs          string         `json:"rootfs,omitempty"`
	RootfsPrepareMs int64          `json:"rootfs_prepare_ms,omitempty"`
	SnapshotTag     string         `json:"snapshot_tag,omitempty"`
	ReadinessProbe  ReadyProbeMode `json:"readiness_probe"`
	Excluded        bool           `json:"excluded"`
}

type ReadyIteration struct {
	Name          string      `json:"name"`
	OK            bool        `json:"ok"`
	DurationMs    int64       `json:"duration_ms"`
	Rootfs        string      `json:"rootfs,omitempty"`
	Phases        ReadyPhases `json:"phases"`
	Error         string      `json:"error,omitempty"`
	TeardownError string      `json:"teardown_error,omitempty"`
}

// ReadyPhases reports stage timings plus the runtime-ready rollup. The fields
// are not all additive: RootfsPrepareMs is a subset of WorkspacePrepareMs, and
// cold-boot WorkspacePrepareMs is a subset of LifecycleMs. LifecycleMs is the
// selected lifecycle request itself: create+start for cold_boot, fork, restore,
// or resume. RuntimeReadyMs spans the timer start through a successful no-op on
// the selected guest interface. DurationMs then includes the caller's probe
// command and excludes teardown.
type ReadyPhases struct {
	RootfsPrepareMs    int64 `json:"rootfs_prepare_ms"`
	WorkspacePrepareMs int64 `json:"workspace_prepare_ms"`
	LifecycleMs        int64 `json:"lifecycle_ms"`
	InterfaceReadyMs   int64 `json:"interface_ready_ms"`
	RuntimeReadyMs     int64 `json:"runtime_ready_ms"`
	ProbeMs            int64 `json:"probe_ms"`
}

type Distribution struct {
	MinMs int64 `json:"min_ms"`
	AvgMs int64 `json:"avg_ms"`
	P50Ms int64 `json:"p50_ms"`
	P95Ms int64 `json:"p95_ms"`
	MaxMs int64 `json:"max_ms"`
}

type ReadySummary struct {
	Count            int `json:"count"`
	Failures         int `json:"failures"`
	TeardownFailures int `json:"teardown_failures"`
	// Baselines and Builds apply to cold_boot iterations. Snapshot and pause
	// setup records its rootfs source in ReadyReport.Setup instead.
	Baselines int `json:"baselines"`
	Builds    int `json:"builds"`

	FullReady        Distribution `json:"full_ready_ms"`
	RuntimeReady     Distribution `json:"runtime_ready_ms"`
	RootfsPrepare    Distribution `json:"rootfs_prepare_ms"`
	WorkspacePrepare Distribution `json:"workspace_prepare_ms"`
	Lifecycle        Distribution `json:"lifecycle_ms"`
	InterfaceReady   Distribution `json:"interface_ready_ms"`
	Probe            Distribution `json:"probe_ms"`
}

var (
	createReadyWorkspace             = workspace.Create
	startReadyWorkspace              = workspace.Start
	createReadyWorkspaceFromSnapshot = workspace.CreateFromSnapshot
	snapshotReadyWorkspace           = workspace.Snapshot
	controlReadyWorkspace            = workspace.Control
	pauseReadyWorkspace              = workspace.Pause
	resumeReadyWorkspace             = workspace.Resume
	runtimeLeaseReadyWorkspace       = workspace.RuntimeLeaseHeld
	execReadyWorkspace               = workspace.Exec
	waitReadyConsole                 = workspace.WaitConsoleCommandReady
	sendReadyCommand                 = workspace.SendConsoleCommand
	deleteReadyWorkspace             = workspace.Delete
)

type readySource struct {
	name string
	tag  string
	opts workspace.Options
}

// Ready measures a workspace from the selected lifecycle request through a
// successful guest command. Snapshot creation, source boot, pause preparation,
// and teardown are explicit excluded setup. The host page cache is deliberately
// reported as uncontrolled until the caller asks for and the implementation can
// provide a safe cross-platform cache-control contract.
func Ready(ctx context.Context, opts ReadyOptions) (ReadyReport, error) {
	if opts.Iterations <= 0 {
		return ReadyReport{}, fmt.Errorf("perf ready iterations must be positive")
	}
	if opts.Timeout <= 0 {
		return ReadyReport{}, fmt.Errorf("perf ready timeout must be positive")
	}
	if strings.TrimSpace(opts.ImageRef) == "" {
		return ReadyReport{}, fmt.Errorf("perf ready requires --image")
	}
	if strings.TrimSpace(opts.ExecCommand) == "" {
		return ReadyReport{}, fmt.Errorf("perf ready requires --exec")
	}
	startMode, err := ParseReadyStartMode(string(opts.StartMode))
	if err != nil {
		return ReadyReport{}, err
	}
	probeMode, err := ParseReadyProbeMode(string(opts.ProbeMode))
	if err != nil {
		return ReadyReport{}, err
	}
	opts.StartMode = startMode
	opts.ProbeMode = probeMode
	report := ReadyReport{
		Benchmark:      "ready",
		Backend:        opts.Backend,
		Arch:           opts.Architecture,
		ImageRef:       strings.TrimSpace(opts.ImageRef),
		Profile:        strings.TrimSpace(opts.Profile),
		StartMode:      startMode,
		ReadinessProbe: probeMode,
		Probe:          strings.TrimSpace(opts.ExecCommand),
		Boundary:       readyBoundary(startMode, probeMode),
		CacheCondition: "host_page_cache_uncontrolled",
		Host:           opts.Host,
	}

	var source readySource
	cleanupSource := false
	defer func() {
		if cleanupSource {
			cleanupCtx, cancel := context.WithTimeout(context.Background(), readyCleanupTimeout)
			_, _ = deleteReadyWorkspace(cleanupCtx, source.opts, workspace.DeleteOptions{Force: true})
			cancel()
		}
	}()
	if startMode != ReadyStartColdBoot {
		prepared, setup, err := prepareReadySource(ctx, opts)
		if err != nil {
			return report, fmt.Errorf("perf ready setup: %w", err)
		}
		source = prepared
		report.Setup = &setup
		cleanupSource = true
	}

	for i := 0; i < opts.Iterations; i++ {
		name := readyWorkspaceName("r", i+1)
		iteration, workspaceOpts := runReadyIteration(ctx, opts, name, source)
		cleanupCtx, cancel := context.WithTimeout(context.Background(), readyCleanupTimeout)
		cleanupErr := teardownReadyIteration(cleanupCtx, startMode, workspaceOpts)
		cancel()
		if cleanupErr != nil {
			iteration.TeardownError = cleanupErr.Error()
		}
		report.Iterations = append(report.Iterations, iteration)
	}
	report.Summary = SummarizeReadyIterations(report.Iterations)

	if source.name != "" {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), readyCleanupTimeout)
		_, cleanupErr := deleteReadyWorkspace(cleanupCtx, source.opts, workspace.DeleteOptions{Force: true})
		cancel()
		if cleanupErr != nil {
			return report, fmt.Errorf("delete perf ready source workspace: %w", cleanupErr)
		}
		cleanupSource = false
	}
	return report, nil
}

// ParseReadyStartMode accepts CLI-style hyphens or structured-output
// underscores. Empty retains the original cold-boot benchmark behavior.
func ParseReadyStartMode(value string) (ReadyStartMode, error) {
	normalized := strings.ReplaceAll(strings.ToLower(strings.TrimSpace(value)), "-", "_")
	if normalized == "" || normalized == "cold" {
		return ReadyStartColdBoot, nil
	}
	mode := ReadyStartMode(normalized)
	switch mode {
	case ReadyStartColdBoot, ReadyStartSnapshotFork, ReadyStartSnapshotRestore, ReadyStartPausedResume:
		return mode, nil
	default:
		return "", fmt.Errorf("perf ready start must be one of cold, snapshot-fork, snapshot-restore, or paused-resume")
	}
}

// ParseReadyProbeMode accepts the short CLI values and canonical JSON values.
// Empty retains the original interactive benchmark behavior.
func ParseReadyProbeMode(value string) (ReadyProbeMode, error) {
	normalized := strings.ReplaceAll(strings.ToLower(strings.TrimSpace(value)), "-", "_")
	switch normalized {
	case "", "interactive", "shell", string(ReadyProbeInteractiveShell):
		return ReadyProbeInteractiveShell, nil
	case "exec", string(ReadyProbeStructuredExec):
		return ReadyProbeStructuredExec, nil
	default:
		return "", fmt.Errorf("perf ready probe must be one of exec or interactive")
	}
}

func readyBoundary(startMode ReadyStartMode, probeMode ReadyProbeMode) ReadyBoundary {
	boundary := ReadyBoundary{Stop: "after_successful_guest_command", Excluded: []string{"iteration_teardown"}}
	switch startMode {
	case ReadyStartColdBoot:
		boundary.Start = "before_workspace_create"
	case ReadyStartSnapshotFork:
		boundary.Start = "before_snapshot_fork"
		boundary.Excluded = append(boundary.Excluded, "source_workspace_create_and_boot", "snapshot_capture")
	case ReadyStartSnapshotRestore:
		boundary.Start = "before_snapshot_restore"
		boundary.Excluded = append(boundary.Excluded, "source_workspace_create_and_boot", "snapshot_capture")
	case ReadyStartPausedResume:
		boundary.Start = "before_paused_workspace_resume"
		boundary.Excluded = append(boundary.Excluded, "source_workspace_create_and_boot", "initial_pause")
	}
	if probeMode == ReadyProbeStructuredExec {
		boundary.Stop = "after_successful_structured_exec_command"
	} else {
		boundary.Stop = "after_successful_interactive_shell_command"
	}
	return boundary
}

func prepareReadySource(ctx context.Context, opts ReadyOptions) (source readySource, setup ReadySetup, retErr error) {
	name := readyWorkspaceName("rs", 0)
	workspaceOpts, err := bootWorkspaceOptions(opts.BootOptions, name)
	if err != nil {
		return source, setup, err
	}
	workspaceOpts.ExecCommand = ""
	workspaceOpts.RootfsBaseline = opts.RootfsBaseline
	workspaceOpts.RootfsBaselineSave = opts.RootfsBaselineSave
	setup.Excluded = true
	setup.ReadinessProbe = ReadyProbeStructuredExec
	setupStarted := time.Now()
	var rootfsStarted time.Time
	workspaceOpts.Progress = func(event rootfs.ProgressEvent) {
		if event.Phase != "copy-baseline" {
			return
		}
		if event.Current == 0 && rootfsStarted.IsZero() {
			rootfsStarted = time.Now()
			return
		}
		if event.Current == event.Total && event.Total > 0 && !rootfsStarted.IsZero() {
			setup.RootfsPrepareMs = time.Since(rootfsStarted).Milliseconds()
		}
	}
	source = readySource{name: name, tag: readySnapshotTag, opts: workspaceOpts}
	keep := false
	defer func() {
		setup.DurationMs = time.Since(setupStarted).Milliseconds()
		if !keep {
			cleanupCtx, cancel := context.WithTimeout(context.Background(), readyCleanupTimeout)
			_, _ = deleteReadyWorkspace(cleanupCtx, workspaceOpts, workspace.DeleteOptions{Force: true})
			cancel()
		}
	}()

	setupCtx, cancel := context.WithTimeout(ctx, opts.Timeout)
	defer cancel()
	created, err := createReadyWorkspace(setupCtx, workspaceOpts)
	if created.Image.BuilderPhase == "copy-baseline" {
		setup.Rootfs = RootfsSourceBaseline
	} else if strings.TrimSpace(created.Image.ImageRef) != "" {
		setup.Rootfs = RootfsSourceBuild
	}
	if err != nil {
		return source, setup, err
	}
	if _, err := startReadyWorkspace(setupCtx, workspaceOpts); err != nil {
		return source, setup, err
	}
	// Reusable templates are captured without opening the interface being
	// measured. A structured no-op proves guest-init is usable while avoiding
	// an in-flight or just-closed interactive session in the machine snapshot.
	if err := waitReadyInterface(setupCtx, workspaceOpts, ReadyProbeStructuredExec, opts.Timeout); err != nil {
		return source, setup, fmt.Errorf("source interface readiness: %w", err)
	}

	switch opts.StartMode {
	case ReadyStartSnapshotFork, ReadyStartSnapshotRestore:
		if _, err := snapshotReadyWorkspace(setupCtx, workspaceOpts, readySnapshotTag); err != nil {
			return source, setup, err
		}
		setup.SnapshotTag = readySnapshotTag
		if _, err := controlReadyWorkspace(setupCtx, workspaceOpts, "halt"); err != nil {
			return source, setup, fmt.Errorf("halt snapshot source: %w", err)
		}
		if err := waitReadyRuntimeRelease(setupCtx, workspaceOpts); err != nil {
			return source, setup, fmt.Errorf("wait for snapshot source shutdown: %w", err)
		}
	case ReadyStartPausedResume:
		if _, err := pauseReadyWorkspace(setupCtx, workspaceOpts); err != nil {
			return source, setup, err
		}
	default:
		return source, setup, fmt.Errorf("unsupported perf ready setup mode %q", opts.StartMode)
	}
	keep = true
	return source, setup, nil
}

func runReadyIteration(ctx context.Context, opts ReadyOptions, name string, source readySource) (ReadyIteration, workspace.Options) {
	iteration := ReadyIteration{Name: name}
	workspaceOpts, err := readyIterationOptions(opts, name, source)
	if err != nil {
		iteration.Error = err.Error()
		return iteration, workspaceOpts
	}
	iterationStarted := time.Now()
	iterationCtx, cancel := context.WithTimeout(ctx, opts.Timeout)
	defer cancel()

	lifecycleStarted := time.Now()
	switch opts.StartMode {
	case ReadyStartColdBoot:
		var rootfsStarted time.Time
		workspaceOpts.Progress = func(event rootfs.ProgressEvent) {
			if event.Phase != "copy-baseline" {
				return
			}
			if event.Current == 0 && rootfsStarted.IsZero() {
				rootfsStarted = time.Now()
				return
			}
			if event.Current == event.Total && event.Total > 0 && !rootfsStarted.IsZero() {
				iteration.Phases.RootfsPrepareMs = time.Since(rootfsStarted).Milliseconds()
			}
		}
		workspaceOpts.RootfsBaseline = opts.RootfsBaseline
		workspaceOpts.RootfsBaselineSave = opts.RootfsBaselineSave
		prepareStarted := time.Now()
		created, createErr := createReadyWorkspace(iterationCtx, workspaceOpts)
		iteration.Phases.WorkspacePrepareMs = time.Since(prepareStarted).Milliseconds()
		if created.Image.BuilderPhase == "copy-baseline" {
			iteration.Rootfs = RootfsSourceBaseline
		} else if strings.TrimSpace(created.Image.ImageRef) != "" {
			iteration.Rootfs = RootfsSourceBuild
		}
		if createErr != nil {
			err = createErr
			break
		}
		_, err = startReadyWorkspace(iterationCtx, workspaceOpts)
	case ReadyStartSnapshotFork:
		_, err = createReadyWorkspaceFromSnapshot(iterationCtx, workspaceOpts, source.name, source.tag)
	case ReadyStartSnapshotRestore:
		workspaceOpts.FromSnapshot = source.tag
		_, err = startReadyWorkspace(iterationCtx, workspaceOpts)
	case ReadyStartPausedResume:
		_, err = resumeReadyWorkspace(iterationCtx, workspaceOpts)
	default:
		err = fmt.Errorf("unsupported perf ready start mode %q", opts.StartMode)
	}
	iteration.Phases.LifecycleMs = time.Since(lifecycleStarted).Milliseconds()
	if err != nil {
		iteration.DurationMs = time.Since(iterationStarted).Milliseconds()
		iteration.Error = err.Error()
		return iteration, workspaceOpts
	}

	interfaceStarted := time.Now()
	err = waitReadyInterface(iterationCtx, workspaceOpts, opts.ProbeMode, remainingReadyTimeout(iterationStarted, opts.Timeout))
	iteration.Phases.InterfaceReadyMs = time.Since(interfaceStarted).Milliseconds()
	iteration.Phases.RuntimeReadyMs = time.Since(iterationStarted).Milliseconds()
	if err != nil {
		iteration.DurationMs = time.Since(iterationStarted).Milliseconds()
		iteration.Error = err.Error()
		return iteration, workspaceOpts
	}

	probeStarted := time.Now()
	err = runReadyProbe(iterationCtx, workspaceOpts, opts.ProbeMode, opts.ExecCommand, remainingReadyTimeout(iterationStarted, opts.Timeout))
	iteration.Phases.ProbeMs = time.Since(probeStarted).Milliseconds()
	iteration.DurationMs = time.Since(iterationStarted).Milliseconds()
	if err != nil {
		iteration.Error = err.Error()
		return iteration, workspaceOpts
	}
	iteration.OK = true
	return iteration, workspaceOpts
}

func readyIterationOptions(opts ReadyOptions, name string, source readySource) (workspace.Options, error) {
	if opts.StartMode == ReadyStartSnapshotRestore || opts.StartMode == ReadyStartPausedResume {
		return source.opts, nil
	}
	workspaceOpts, err := bootWorkspaceOptions(opts.BootOptions, name)
	if err != nil {
		return workspaceOpts, err
	}
	// The readiness command travels through the selected guest interface after
	// startup. It must never become a create-time setup command.
	workspaceOpts.ExecCommand = ""
	return workspaceOpts, nil
}

func waitReadyInterface(ctx context.Context, opts workspace.Options, mode ReadyProbeMode, timeout time.Duration) error {
	switch mode {
	case ReadyProbeStructuredExec:
		result, err := execReadyWorkspace(ctx, opts, execprotocol.NewExecRequest([]string{"true"}))
		return successfulExec(result, err, "structured exec readiness")
	case ReadyProbeInteractiveShell:
		return waitReadyConsole(ctx, workspace.ConsoleOptions{
			StateDir:            opts.StateDir,
			Name:                opts.Name,
			ReadyTimeout:        timeout,
			SendTimeout:         time.Second,
			RequireCommandReady: true,
		})
	default:
		return fmt.Errorf("unsupported perf ready probe mode %q", mode)
	}
}

func runReadyProbe(ctx context.Context, opts workspace.Options, mode ReadyProbeMode, command string, timeout time.Duration) error {
	switch mode {
	case ReadyProbeStructuredExec:
		req := execprotocol.NewExecRequest([]string{"/bin/sh", "-lc", command})
		result, err := execReadyWorkspace(ctx, opts, req)
		return successfulExec(result, err, "structured exec probe")
	case ReadyProbeInteractiveShell:
		consoleOpts := workspace.ConsoleOptions{
			StateDir:            opts.StateDir,
			Name:                opts.Name,
			ReadyTimeout:        0,
			SendTimeout:         timeout,
			RequireCommandReady: false,
		}
		var probeOutput bytes.Buffer
		probeCommand := fmt.Sprintf("{ %s; } && printf '\\n%s\\n'", command, readyProbeMarker)
		if err := sendReadyCommand(ctx, consoleOpts, probeCommand, io.MultiWriter(io.Discard, &probeOutput)); err != nil {
			return err
		}
		if !strings.Contains(probeOutput.String(), readyProbeMarker) {
			return fmt.Errorf("interactive probe %q did not complete successfully", command)
		}
		return nil
	default:
		return fmt.Errorf("unsupported perf ready probe mode %q", mode)
	}
}

func successfulExec(result execprotocol.ExecResult, err error, label string) error {
	if err != nil {
		return err
	}
	if result.Error != nil {
		return fmt.Errorf("%s failed: %s", label, result.Error.Message)
	}
	if result.Status != execprotocol.ExecStatusExited || result.ExitCode == nil || *result.ExitCode != 0 {
		return fmt.Errorf("%s did not exit successfully: status=%s exit_code=%v", label, result.Status, result.ExitCode)
	}
	return nil
}

func teardownReadyIteration(ctx context.Context, mode ReadyStartMode, opts workspace.Options) error {
	switch mode {
	case ReadyStartColdBoot, ReadyStartSnapshotFork:
		_, err := deleteReadyWorkspace(ctx, opts, workspace.DeleteOptions{Force: true})
		return err
	case ReadyStartSnapshotRestore:
		if _, err := controlReadyWorkspace(ctx, opts, "halt"); err != nil {
			return err
		}
		return waitReadyRuntimeRelease(ctx, opts)
	case ReadyStartPausedResume:
		_, err := pauseReadyWorkspace(ctx, opts)
		return err
	default:
		return fmt.Errorf("unsupported perf ready teardown mode %q", mode)
	}
}

// waitReadyRuntimeRelease keeps prior-iteration shutdown outside the next
// iteration's timer. Halt records the terminal lifecycle result before the
// detached supervisor necessarily exits; start correctly refuses while that
// process still owns the namespace-independent runtime lease.
func waitReadyRuntimeRelease(ctx context.Context, opts workspace.Options) error {
	for {
		held, err := runtimeLeaseReadyWorkspace(opts.StateDir, opts.Name)
		if err != nil {
			return err
		}
		if !held {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("workspace %s runtime lease remained held: %w", opts.Name, ctx.Err())
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func remainingReadyTimeout(start time.Time, timeout time.Duration) time.Duration {
	remaining := timeout - time.Since(start)
	if remaining < time.Millisecond {
		return time.Millisecond
	}
	return remaining
}

func SummarizeReadyIterations(iterations []ReadyIteration) ReadySummary {
	summary := ReadySummary{Count: len(iterations)}
	var fullReady, runtimeReady, rootfsPrepare, workspacePrepare, lifecycle, interfaceReady, probe []int64
	for _, iteration := range iterations {
		if iteration.TeardownError != "" {
			summary.TeardownFailures++
		}
		if !iteration.OK {
			summary.Failures++
			continue
		}
		switch iteration.Rootfs {
		case RootfsSourceBaseline:
			summary.Baselines++
		case RootfsSourceBuild:
			summary.Builds++
		}
		fullReady = append(fullReady, iteration.DurationMs)
		runtimeReady = append(runtimeReady, iteration.Phases.RuntimeReadyMs)
		rootfsPrepare = append(rootfsPrepare, iteration.Phases.RootfsPrepareMs)
		workspacePrepare = append(workspacePrepare, iteration.Phases.WorkspacePrepareMs)
		lifecycle = append(lifecycle, iteration.Phases.LifecycleMs)
		interfaceReady = append(interfaceReady, iteration.Phases.InterfaceReadyMs)
		probe = append(probe, iteration.Phases.ProbeMs)
	}
	summary.FullReady = summarizeDistribution(fullReady)
	summary.RuntimeReady = summarizeDistribution(runtimeReady)
	summary.RootfsPrepare = summarizeDistribution(rootfsPrepare)
	summary.WorkspacePrepare = summarizeDistribution(workspacePrepare)
	summary.Lifecycle = summarizeDistribution(lifecycle)
	summary.InterfaceReady = summarizeDistribution(interfaceReady)
	summary.Probe = summarizeDistribution(probe)
	return summary
}

func summarizeDistribution(values []int64) Distribution {
	if len(values) == 0 {
		return Distribution{}
	}
	sorted := append([]int64(nil), values...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	var total int64
	for _, value := range sorted {
		total += value
	}
	// Nearest-rank percentiles. Integer arithmetic computes ceil(p*n)-1.
	p50Index := (50*len(sorted)+99)/100 - 1
	p95Index := (95*len(sorted)+99)/100 - 1
	return Distribution{
		MinMs: sorted[0],
		AvgMs: total / int64(len(sorted)),
		P50Ms: sorted[p50Index],
		P95Ms: sorted[p95Index],
		MaxMs: sorted[len(sorted)-1],
	}
}

// readyWorkspaceName stays deliberately short. Firecracker API sockets live
// below state-dir/workspace and sockaddr_un paths are bounded (108 bytes on
// Linux); benchmark-owned names must not consume that budget with prose.
func readyWorkspaceName(kind string, iteration int) string {
	token := strconv.FormatInt(time.Now().UnixNano(), 36)
	if iteration > 0 {
		return fmt.Sprintf("perf-%s-%s-%d", kind, token, iteration)
	}
	return fmt.Sprintf("perf-%s-%s", kind, token)
}
