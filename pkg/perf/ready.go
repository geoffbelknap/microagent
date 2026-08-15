package perf

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/geoffbelknap/microagent/pkg/rootfs"
	"github.com/geoffbelknap/microagent/pkg/vmkit"
	"github.com/geoffbelknap/microagent/pkg/workspace"
)

const readyProbeMarker = "__MICROAGENT_READY_PROBE_OK__"

// ReadyOptions configures the fresh-workspace interactive-readiness benchmark.
// It shares BootOptions' image, host, backend, baseline, and iteration fields;
// ExecCommand is the command sent through the ready interactive shell.
type ReadyOptions BootOptions

type ReadyReport struct {
	Benchmark  string             `json:"benchmark"`
	Backend    string             `json:"backend"`
	Arch       string             `json:"arch"`
	ImageRef   string             `json:"image_ref"`
	Profile    string             `json:"profile"`
	Probe      string             `json:"probe"`
	Iterations []ReadyIteration   `json:"iterations"`
	Summary    ReadySummary       `json:"summary"`
	Host       *vmkit.HostSupport `json:"host,omitempty"`
}

type ReadyIteration struct {
	Name       string      `json:"name"`
	OK         bool        `json:"ok"`
	DurationMs int64       `json:"duration_ms"`
	Rootfs     string      `json:"rootfs,omitempty"`
	Phases     ReadyPhases `json:"phases"`
	Error      string      `json:"error,omitempty"`
}

// ReadyPhases reports both disjoint stages and two useful rollups. Rootfs
// preparation is a measured subset of WorkspacePrepareMs. BareGuestReadyMs
// spans SupervisorStartMs plus ShellWaitMs. DurationMs on the iteration is the
// end-to-end time through the interactive probe and excludes teardown.
type ReadyPhases struct {
	RootfsPrepareMs    int64 `json:"rootfs_prepare_ms"`
	WorkspacePrepareMs int64 `json:"workspace_prepare_ms"`
	SupervisorStartMs  int64 `json:"supervisor_start_ms"`
	ShellWaitMs        int64 `json:"shell_wait_ms"`
	BareGuestReadyMs   int64 `json:"bare_guest_ready_ms"`
	AgentProbeMs       int64 `json:"agent_probe_ms"`
}

type Distribution struct {
	MinMs int64 `json:"min_ms"`
	AvgMs int64 `json:"avg_ms"`
	P95Ms int64 `json:"p95_ms"`
	MaxMs int64 `json:"max_ms"`
}

type ReadySummary struct {
	Count    int `json:"count"`
	Failures int `json:"failures"`
	// Baselines and Builds preserve the same measurement-fidelity signal as
	// perf boot. A prepared-image target should report only baselines.
	Baselines int `json:"baselines"`
	Builds    int `json:"builds"`

	InteractiveReady Distribution `json:"interactive_ready_ms"`
	RootfsPrepare    Distribution `json:"rootfs_prepare_ms"`
	WorkspacePrepare Distribution `json:"workspace_prepare_ms"`
	SupervisorStart  Distribution `json:"supervisor_start_ms"`
	ShellWait        Distribution `json:"shell_wait_ms"`
	BareGuestReady   Distribution `json:"bare_guest_ready_ms"`
	AgentProbe       Distribution `json:"agent_probe_ms"`
}

var (
	createReadyWorkspace = workspace.Create
	startReadyWorkspace  = workspace.Start
	dialReadyConsole     = workspace.DialConsole
	sendReadyCommand     = workspace.SendConsoleCommand
	deleteReadyWorkspace = workspace.Delete
)

// Ready measures a fresh workspace from private rootfs derivation through an
// actual command round-trip on the interactive shell. Image download/build is
// visible as RootfsSourceBuild and must not be mixed into a prepared-image run.
// Teardown happens after DurationMs is captured and is never counted as startup.
func Ready(ctx context.Context, opts ReadyOptions) (ReadyReport, error) {
	bootOpts := BootOptions(opts)
	if bootOpts.Iterations <= 0 {
		return ReadyReport{}, fmt.Errorf("perf ready iterations must be positive")
	}
	if bootOpts.Timeout <= 0 {
		return ReadyReport{}, fmt.Errorf("perf ready timeout must be positive")
	}
	if strings.TrimSpace(bootOpts.ImageRef) == "" {
		return ReadyReport{}, fmt.Errorf("perf ready requires --image")
	}
	if strings.TrimSpace(bootOpts.ExecCommand) == "" {
		return ReadyReport{}, fmt.Errorf("perf ready requires --exec")
	}
	report := ReadyReport{
		Benchmark: "ready",
		Backend:   bootOpts.Backend,
		Arch:      bootOpts.Architecture,
		ImageRef:  strings.TrimSpace(bootOpts.ImageRef),
		Profile:   strings.TrimSpace(bootOpts.Profile),
		Probe:     strings.TrimSpace(bootOpts.ExecCommand),
		Host:      bootOpts.Host,
	}
	for i := 0; i < bootOpts.Iterations; i++ {
		name := fmt.Sprintf("perf-ready-%d-%d", time.Now().UnixNano(), i+1)
		iteration, workspaceOpts := runReadyIteration(ctx, bootOpts, name)
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		_, cleanupErr := deleteReadyWorkspace(cleanupCtx, workspaceOpts, workspace.DeleteOptions{Force: true})
		cancel()
		if cleanupErr != nil && iteration.Error == "" {
			iteration.OK = false
			iteration.Error = "delete measured workspace: " + cleanupErr.Error()
		}
		report.Iterations = append(report.Iterations, iteration)
	}
	report.Summary = SummarizeReadyIterations(report.Iterations)
	return report, nil
}

func runReadyIteration(ctx context.Context, opts BootOptions, name string) (ReadyIteration, workspace.Options) {
	iteration := ReadyIteration{Name: name}
	workspaceOpts, err := bootWorkspaceOptions(opts, name)
	if err != nil {
		iteration.Error = err.Error()
		return iteration, workspaceOpts
	}
	// The readiness command travels through the shell after boot. It must not
	// become a create-time setup command or alter the immutable rootfs.
	workspaceOpts.ExecCommand = ""

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

	iterationStarted := time.Now()
	iterationCtx, cancel := context.WithTimeout(ctx, opts.Timeout)
	defer cancel()

	prepareStarted := time.Now()
	created, err := createReadyWorkspace(iterationCtx, workspaceOpts)
	iteration.Phases.WorkspacePrepareMs = time.Since(prepareStarted).Milliseconds()
	if created.Image.BuilderPhase == "copy-baseline" {
		iteration.Rootfs = RootfsSourceBaseline
	} else if strings.TrimSpace(created.Image.ImageRef) != "" {
		iteration.Rootfs = RootfsSourceBuild
	}
	if err != nil {
		iteration.DurationMs = time.Since(iterationStarted).Milliseconds()
		iteration.Error = err.Error()
		return iteration, workspaceOpts
	}

	startStarted := time.Now()
	if _, err := startReadyWorkspace(iterationCtx, workspaceOpts); err != nil {
		iteration.Phases.SupervisorStartMs = time.Since(startStarted).Milliseconds()
		iteration.DurationMs = time.Since(iterationStarted).Milliseconds()
		iteration.Error = err.Error()
		return iteration, workspaceOpts
	}
	iteration.Phases.SupervisorStartMs = time.Since(startStarted).Milliseconds()

	shellWaitStarted := time.Now()
	consoleOpts := workspace.ConsoleOptions{
		StateDir:            workspaceOpts.StateDir,
		Name:                workspaceOpts.Name,
		ReadyTimeout:        remainingReadyTimeout(iterationStarted, opts.Timeout),
		SendTimeout:         time.Second,
		RequireCommandReady: true,
	}
	conn, err := dialReadyConsole(iterationCtx, consoleOpts)
	iteration.Phases.ShellWaitMs = time.Since(shellWaitStarted).Milliseconds()
	iteration.Phases.BareGuestReadyMs = time.Since(startStarted).Milliseconds()
	if err != nil {
		iteration.DurationMs = time.Since(iterationStarted).Milliseconds()
		iteration.Error = err.Error()
		return iteration, workspaceOpts
	}
	_ = conn.Close()

	agentStarted := time.Now()
	consoleOpts.ReadyTimeout = 0
	consoleOpts.SendTimeout = remainingReadyTimeout(iterationStarted, opts.Timeout)
	consoleOpts.RequireCommandReady = false
	var probeOutput bytes.Buffer
	probeCommand := fmt.Sprintf("{ %s; } && printf '\\n%s\\n'", opts.ExecCommand, readyProbeMarker)
	err = sendReadyCommand(iterationCtx, consoleOpts, probeCommand, io.MultiWriter(io.Discard, &probeOutput))
	iteration.Phases.AgentProbeMs = time.Since(agentStarted).Milliseconds()
	iteration.DurationMs = time.Since(iterationStarted).Milliseconds()
	if err != nil {
		iteration.Error = err.Error()
		return iteration, workspaceOpts
	}
	if !strings.Contains(probeOutput.String(), readyProbeMarker) {
		iteration.Error = fmt.Sprintf("interactive probe %q did not complete successfully", opts.ExecCommand)
		return iteration, workspaceOpts
	}
	iteration.OK = true
	return iteration, workspaceOpts
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
	var total, rootfsPrepare, workspacePrepare, supervisorStart, shellWait, bareGuestReady, agentProbe []int64
	for _, iteration := range iterations {
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
		total = append(total, iteration.DurationMs)
		rootfsPrepare = append(rootfsPrepare, iteration.Phases.RootfsPrepareMs)
		workspacePrepare = append(workspacePrepare, iteration.Phases.WorkspacePrepareMs)
		supervisorStart = append(supervisorStart, iteration.Phases.SupervisorStartMs)
		shellWait = append(shellWait, iteration.Phases.ShellWaitMs)
		bareGuestReady = append(bareGuestReady, iteration.Phases.BareGuestReadyMs)
		agentProbe = append(agentProbe, iteration.Phases.AgentProbeMs)
	}
	summary.InteractiveReady = summarizeDistribution(total)
	summary.RootfsPrepare = summarizeDistribution(rootfsPrepare)
	summary.WorkspacePrepare = summarizeDistribution(workspacePrepare)
	summary.SupervisorStart = summarizeDistribution(supervisorStart)
	summary.ShellWait = summarizeDistribution(shellWait)
	summary.BareGuestReady = summarizeDistribution(bareGuestReady)
	summary.AgentProbe = summarizeDistribution(agentProbe)
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
	// Nearest-rank p95. Integer arithmetic computes ceil(0.95*n)-1.
	p95Index := (95*len(sorted)+99)/100 - 1
	return Distribution{
		MinMs: sorted[0],
		AvgMs: total / int64(len(sorted)),
		P95Ms: sorted[p95Index],
		MaxMs: sorted[len(sorted)-1],
	}
}
