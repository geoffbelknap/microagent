package perf

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/geoffbelknap/microagent/pkg/rootfs"
	"github.com/geoffbelknap/microagent/pkg/vmkit"
	"github.com/geoffbelknap/microagent/pkg/workspace"
)

// Rootfs sources reported per iteration: a measured boot either cloned a
// recorded rootfs baseline (what a repeat `run` of the same image does) or
// took a full rootfs build — pull, mke2fs, populate. They are different
// numbers, so the report names which one it measured.
const (
	RootfsSourceBaseline = "baseline"
	RootfsSourceBuild    = "build"
)

// runWorkspace is the boot every iteration measures, indirected so tests can
// check the measured pipeline's wiring without a microVM.
var runWorkspace = workspace.Run

type BootOptions struct {
	StateDir       string
	ImageRef       string
	Profile        string
	ExecCommand    string
	Backend        string
	Architecture   string
	Mke2fsPath     string
	DebugfsPath    string
	SupervisorPath string
	// NetworkMode selects the workspace network mode for each measured boot
	// (user, isolated). Empty means the backend default.
	NetworkMode string
	Iterations  int
	Timeout     time.Duration
	Host        *vmkit.HostSupport

	// RootfsBaseline and RootfsBaselineSave are handed to every measured
	// boot (see the matching workspace.Options fields), so `perf boot`
	// exercises the rootfs path a user's repeat `run` takes: clone a
	// recorded baseline instead of rebuilding, and seed the baseline from
	// the first full build. Left unset, every iteration takes the
	// full-build branch and the reported number is a first-boot time, not
	// a boot time. Injected by the caller, which owns the image cache.
	RootfsBaseline     func(rootfsPath string) (baseline string, prov rootfs.Provenance, ok bool)
	RootfsBaselineSave func(rootfsPath string, prov rootfs.Provenance)
}

type BootReport struct {
	Benchmark      string              `json:"benchmark"`
	Backend        string              `json:"backend"`
	Arch           string              `json:"arch"`
	ImageRef       string              `json:"image_ref"`
	Profile        string              `json:"profile"`
	Boundary       MeasurementBoundary `json:"boundary"`
	CacheCondition string              `json:"cache_condition"`
	Iterations     []Iteration         `json:"iterations"`
	Summary        Summary             `json:"summary"`
	Host           *vmkit.HostSupport  `json:"host,omitempty"`
}

// MeasurementBoundary makes a performance timer's contract machine-readable.
// Start and Stop name the timer edges; Excluded names work deliberately kept
// outside each iteration.
type MeasurementBoundary struct {
	Start    string   `json:"start"`
	Stop     string   `json:"stop"`
	Excluded []string `json:"excluded"`
}

type Iteration struct {
	Name       string `json:"name"`
	OK         bool   `json:"ok"`
	DurationMs int64  `json:"duration_ms"`
	// Rootfs names the rootfs branch this iteration measured, one of
	// RootfsSourceBaseline or RootfsSourceBuild. Empty when the iteration
	// never reached the rootfs stage.
	Rootfs string `json:"rootfs,omitempty"`
	Error  string `json:"error,omitempty"`
}

type Summary struct {
	Count    int `json:"count"`
	Failures int `json:"failures"`
	// Baselines and Builds count the iterations that cloned a baseline
	// versus took a full rootfs build. A mix means min/avg/max blend
	// warm-boot and first-boot numbers, so read them before comparing runs.
	Baselines int   `json:"baselines"`
	Builds    int   `json:"builds"`
	MinMs     int64 `json:"min_ms"`
	AvgMs     int64 `json:"avg_ms"`
	MaxMs     int64 `json:"max_ms"`
}

type FootprintReport struct {
	Benchmark string `json:"benchmark"`
	Workspace string `json:"workspace"`
	Backend   string `json:"backend"`
	PID       int    `json:"pid"`
	RSSKiB    int64  `json:"rss_kib"`
	State     string `json:"state"`
}

type SteadyReport struct {
	Benchmark       string      `json:"benchmark"`
	Workspace       string      `json:"workspace"`
	Backend         string      `json:"backend"`
	PID             int         `json:"pid"`
	State           string      `json:"state"`
	DurationSeconds int         `json:"duration_seconds"`
	IntervalSeconds int         `json:"interval_seconds"`
	Samples         []RSSSample `json:"samples"`
	Summary         RSSSummary  `json:"summary"`
}

type RSSSample struct {
	At     string `json:"at"`
	RSSKiB int64  `json:"rss_kib"`
}

type RSSSummary struct {
	Count  int   `json:"count"`
	MinKiB int64 `json:"min_kib"`
	AvgKiB int64 `json:"avg_kib"`
	MaxKiB int64 `json:"max_kib"`
}

func Boot(ctx context.Context, opts BootOptions) (BootReport, error) {
	if opts.Iterations <= 0 {
		return BootReport{}, fmt.Errorf("perf boot iterations must be positive")
	}
	if opts.Timeout <= 0 {
		return BootReport{}, fmt.Errorf("perf boot timeout must be positive")
	}
	if strings.TrimSpace(opts.ImageRef) == "" {
		return BootReport{}, fmt.Errorf("perf boot requires --image")
	}
	if strings.TrimSpace(opts.ExecCommand) == "" {
		return BootReport{}, fmt.Errorf("perf boot requires --exec")
	}
	report := BootReport{
		Benchmark: "boot",
		Backend:   opts.Backend,
		Arch:      opts.Architecture,
		ImageRef:  strings.TrimSpace(opts.ImageRef),
		Profile:   strings.TrimSpace(opts.Profile),
		Boundary: MeasurementBoundary{
			Start:    "before_workspace_run",
			Stop:     "after_guest_command_result",
			Excluded: []string{"iteration_teardown"},
		},
		CacheCondition: "host_page_cache_uncontrolled",
		Host:           opts.Host,
	}
	for i := 0; i < opts.Iterations; i++ {
		name := fmt.Sprintf("perf-boot-%d-%d", time.Now().UnixNano(), i+1)
		start := time.Now()
		rootfsSource, err := runBootWorkspace(ctx, opts, name)
		duration := time.Since(start)
		workspace.Cleanup(opts.StateDir, name)
		result := Iteration{Name: name, OK: err == nil, DurationMs: duration.Milliseconds(), Rootfs: rootfsSource}
		if err != nil {
			result.Error = err.Error()
		}
		report.Iterations = append(report.Iterations, result)
	}
	report.Summary = SummarizeIterations(report.Iterations)
	return report, nil
}

func Footprint(stateDir, name string) (FootprintReport, error) {
	if err := workspace.ValidateName(name); err != nil {
		return FootprintReport{}, err
	}
	state, err := workspace.ReadRuntimeState(workspace.Options{StateDir: stateDir, Name: name})
	if err != nil {
		return FootprintReport{}, err
	}
	if state.PID <= 0 {
		return FootprintReport{}, fmt.Errorf("workspace %s does not have a running process pid", name)
	}
	rssKiB, err := ProcessRSSKiB(state.PID)
	if err != nil {
		return FootprintReport{}, err
	}
	return FootprintReport{
		Benchmark: "footprint",
		Workspace: name,
		Backend:   state.Event.Identity.Backend,
		PID:       state.PID,
		RSSKiB:    rssKiB,
		State:     string(state.Event.State),
	}, nil
}

func Steady(ctx context.Context, stateDir, name string, duration, interval time.Duration) (SteadyReport, error) {
	if duration <= 0 {
		return SteadyReport{}, fmt.Errorf("perf steady duration must be positive")
	}
	if interval <= 0 {
		return SteadyReport{}, fmt.Errorf("perf steady interval must be positive")
	}
	if interval > duration {
		return SteadyReport{}, fmt.Errorf("perf steady interval must be less than or equal to duration")
	}
	if err := workspace.ValidateName(name); err != nil {
		return SteadyReport{}, err
	}
	state, err := workspace.ReadRuntimeState(workspace.Options{StateDir: stateDir, Name: name})
	if err != nil {
		return SteadyReport{}, err
	}
	var samples []RSSSample
	if state.PID <= 0 {
		return SteadyReport{}, fmt.Errorf("workspace %s does not have a running process pid", name)
	}
	samples, err = SampleProcessRSS(ctx, state.PID, duration, interval)
	if err != nil {
		return SteadyReport{}, err
	}
	return SteadyReport{
		Benchmark:       "steady",
		Workspace:       name,
		Backend:         state.Event.Identity.Backend,
		PID:             state.PID,
		State:           string(state.Event.State),
		DurationSeconds: int(duration.Seconds()),
		IntervalSeconds: int(interval.Seconds()),
		Samples:         samples,
		Summary:         SummarizeRSSSamples(samples),
	}, nil
}

func ProcessRSSKiB(pid int) (int64, error) {
	if pid <= 0 {
		return 0, fmt.Errorf("pid must be positive")
	}
	return processRSSKiB(pid)
}

func ParseRSSKiB(output []byte) (int64, error) {
	text := strings.TrimSpace(string(output))
	if text == "" {
		return 0, fmt.Errorf("process rss is unavailable")
	}
	fields := strings.Fields(text)
	if len(fields) == 0 {
		return 0, fmt.Errorf("process rss is unavailable")
	}
	rssKiB, err := strconv.ParseInt(fields[0], 10, 64)
	if err != nil || rssKiB < 0 {
		return 0, fmt.Errorf("process rss is invalid: %q", fields[0])
	}
	return rssKiB, nil
}

func SampleProcessRSS(ctx context.Context, pid int, duration, interval time.Duration) ([]RSSSample, error) {
	return sampleRSS(ctx, func() (int64, error) { return ProcessRSSKiB(pid) }, duration, interval)
}

func sampleRSS(ctx context.Context, sample func() (int64, error), duration, interval time.Duration) ([]RSSSample, error) {
	if duration <= 0 {
		return nil, fmt.Errorf("duration must be positive")
	}
	if interval <= 0 {
		return nil, fmt.Errorf("interval must be positive")
	}
	deadline := time.Now().Add(duration)
	samples := []RSSSample{}
	for {
		rssKiB, err := sample()
		if err != nil {
			return nil, err
		}
		samples = append(samples, RSSSample{At: time.Now().UTC().Format(time.RFC3339Nano), RSSKiB: rssKiB})
		if !time.Now().Before(deadline) {
			return samples, nil
		}
		sleep := interval
		if remaining := time.Until(deadline); remaining < sleep {
			sleep = remaining
		}
		timer := time.NewTimer(sleep)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}

func SummarizeIterations(iterations []Iteration) Summary {
	summary := Summary{Count: len(iterations)}
	if len(iterations) == 0 {
		return summary
	}
	var total int64
	for i, iteration := range iterations {
		if !iteration.OK {
			summary.Failures++
		}
		switch iteration.Rootfs {
		case RootfsSourceBaseline:
			summary.Baselines++
		case RootfsSourceBuild:
			summary.Builds++
		}
		if i == 0 || iteration.DurationMs < summary.MinMs {
			summary.MinMs = iteration.DurationMs
		}
		if iteration.DurationMs > summary.MaxMs {
			summary.MaxMs = iteration.DurationMs
		}
		total += iteration.DurationMs
	}
	summary.AvgMs = total / int64(len(iterations))
	return summary
}

func SummarizeRSSSamples(samples []RSSSample) RSSSummary {
	summary := RSSSummary{Count: len(samples)}
	if len(samples) == 0 {
		return summary
	}
	var total int64
	for i, sample := range samples {
		if i == 0 || sample.RSSKiB < summary.MinKiB {
			summary.MinKiB = sample.RSSKiB
		}
		if sample.RSSKiB > summary.MaxKiB {
			summary.MaxKiB = sample.RSSKiB
		}
		total += sample.RSSKiB
	}
	summary.AvgKiB = total / int64(len(samples))
	return summary
}

// runBootWorkspace boots one disposable workspace and reports which rootfs
// branch it took (RootfsSourceBaseline, RootfsSourceBuild, or empty when the
// boot failed before the rootfs stage).
func runBootWorkspace(ctx context.Context, opts BootOptions, name string) (string, error) {
	workspaceOpts, err := bootWorkspaceOptions(opts, name)
	if err != nil {
		return "", err
	}
	workspaceOpts.ExecCommand = opts.ExecCommand
	// Run normally removes its one-shot workspace before returning. Retain it
	// just long enough for Boot to stop the readiness timer, then perform the
	// same cleanup outside the measured interval.
	workspaceOpts.Keep = true
	// The branch is observed from the hooks themselves: BuildRootfs consults
	// the resolver only when it is about to reuse a baseline, and calls the
	// save hook only after a full build. The save hook is installed even when
	// the caller supplied none, so the label is complete either way.
	rootfsSource := ""
	if opts.RootfsBaseline != nil {
		workspaceOpts.RootfsBaseline = func(rootfsPath string) (string, rootfs.Provenance, bool) {
			baseline, prov, ok := opts.RootfsBaseline(rootfsPath)
			if ok {
				rootfsSource = RootfsSourceBaseline
			}
			return baseline, prov, ok
		}
	}
	workspaceOpts.RootfsBaselineSave = func(rootfsPath string, prov rootfs.Provenance) {
		rootfsSource = RootfsSourceBuild
		if opts.RootfsBaselineSave != nil {
			opts.RootfsBaselineSave(rootfsPath, prov)
		}
	}
	_, err = runWorkspace(ctx, workspaceOpts)
	return rootfsSource, err
}

func bootWorkspaceOptions(opts BootOptions, name string) (workspace.Options, error) {
	workspaceOpts := workspace.Options{Name: name}
	workspaceOpts.StateDir = opts.StateDir
	workspaceOpts.ImageRef = strings.TrimSpace(opts.ImageRef)
	workspaceOpts.Profile = strings.TrimSpace(opts.Profile)
	if workspaceOpts.Profile != "" {
		if _, ok := workspace.LookupProfile(workspaceOpts.Profile); !ok {
			return workspaceOpts, fmt.Errorf("unknown resource profile %q; choose one of: %s", workspaceOpts.Profile, strings.Join(workspace.ProfileNames(), ", "))
		}
	}
	workspaceOpts.Timeout = opts.Timeout
	if strings.TrimSpace(opts.Backend) != "" {
		workspaceOpts.Backend = opts.Backend
	}
	if strings.TrimSpace(opts.Architecture) != "" {
		workspaceOpts.Architecture = opts.Architecture
	}
	if strings.TrimSpace(opts.Mke2fsPath) != "" {
		workspaceOpts.Mke2fsPath = opts.Mke2fsPath
	}
	if strings.TrimSpace(opts.DebugfsPath) != "" {
		workspaceOpts.DebugfsPath = opts.DebugfsPath
	}
	if strings.TrimSpace(opts.SupervisorPath) != "" {
		workspaceOpts.SupervisorPath = opts.SupervisorPath
	}
	if mode := strings.TrimSpace(opts.NetworkMode); mode != "" {
		workspaceOpts.Network = vmkit.NetworkConfig{Mode: mode}
	}
	return workspaceOpts, nil
}
