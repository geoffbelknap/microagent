package perf

import (
	"context"
	"io"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/geoffbelknap/microagent/pkg/rootfs"
	"github.com/geoffbelknap/microagent/pkg/vmkit"
	"github.com/geoffbelknap/microagent/pkg/workspace"
	execprotocol "github.com/geoffbelknap/microagent/pkg/workspace/exec/protocol"
)

func TestReadyMeasuresInteractivePipelineAndCleansUp(t *testing.T) {
	previousCreate := createReadyWorkspace
	previousStart := startReadyWorkspace
	previousWait := waitReadyConsole
	previousSend := sendReadyCommand
	previousDelete := deleteReadyWorkspace
	t.Cleanup(func() {
		createReadyWorkspace = previousCreate
		startReadyWorkspace = previousStart
		waitReadyConsole = previousWait
		sendReadyCommand = previousSend
		deleteReadyWorkspace = previousDelete
	})

	created := 0
	started := 0
	deleted := 0
	var probes []string
	createReadyWorkspace = func(_ context.Context, opts workspace.Options) (workspace.Result, error) {
		created++
		if opts.ExecCommand != "" {
			t.Fatalf("interactive probe was baked into create: %q", opts.ExecCommand)
		}
		if opts.RootfsBaseline == nil {
			t.Fatal("create did not receive prepared-image baseline resolver")
		}
		rootfsPath := filepath.Join(opts.StateDir, opts.Name, "rootfs.ext4")
		_, prov, ok := opts.RootfsBaseline(rootfsPath)
		if !ok {
			t.Fatal("prepared baseline was unavailable")
		}
		if opts.Progress != nil {
			opts.Progress(rootfs.ProgressEvent{Phase: "copy-baseline", Total: 1, Indeterminate: true})
			opts.Progress(rootfs.ProgressEvent{Phase: "copy-baseline", Current: 1, Total: 1})
		}
		prov.BuilderPhase = "copy-baseline"
		return workspace.Result{Image: prov}, nil
	}
	startReadyWorkspace = func(_ context.Context, _ workspace.Options) (workspace.Result, error) {
		started++
		return workspace.Result{}, nil
	}
	waitReadyConsole = func(_ context.Context, opts workspace.ConsoleOptions) error {
		if !opts.RequireCommandReady {
			t.Fatal("shell readiness did not require a command round-trip")
		}
		return nil
	}
	sendReadyCommand = func(_ context.Context, _ workspace.ConsoleOptions, command string, output io.Writer) error {
		probes = append(probes, command)
		_, _ = io.WriteString(output, readyProbeMarker)
		return nil
	}
	deleteReadyWorkspace = func(_ context.Context, _ workspace.Options, opts workspace.DeleteOptions) (workspace.DeleteResult, error) {
		deleted++
		if !opts.Force {
			t.Fatal("benchmark cleanup was not forced")
		}
		return workspace.DeleteResult{Response: vmkit.Response{OK: true}, Deleted: true}, nil
	}

	dir := t.TempDir()
	opts := ReadyOptions{
		BootOptions: BootOptions{
			StateDir:    dir,
			ImageRef:    "local/coding-agent:prepared",
			ExecCommand: "pi --version",
			Iterations:  2,
			Timeout:     time.Second,
			RootfsBaseline: func(rootfsPath string) (string, rootfs.Provenance, bool) {
				return filepath.Join(dir, "baseline.ext4"), rootfs.Provenance{ImageRef: "local/coding-agent:prepared", OutputPath: rootfsPath}, true
			},
		},
	}
	report, err := Ready(context.Background(), opts)
	if err != nil {
		t.Fatalf("Ready: %v", err)
	}
	if created != 2 || started != 2 || deleted != 2 {
		t.Fatalf("pipeline calls create=%d start=%d delete=%d, want two each", created, started, deleted)
	}
	if len(probes) != 2 || !strings.Contains(probes[0], "pi --version") || !strings.Contains(probes[1], "pi --version") {
		t.Fatalf("interactive probes = %#v", probes)
	}
	if report.StartMode != ReadyStartColdBoot || report.ReadinessProbe != ReadyProbeInteractiveShell {
		t.Fatalf("contract modes = start:%q probe:%q", report.StartMode, report.ReadinessProbe)
	}
	if report.Boundary.Start != "before_workspace_create" || report.Boundary.Stop != "after_successful_interactive_shell_command" {
		t.Fatalf("boundary = %#v", report.Boundary)
	}
	if report.Summary.Failures != 0 || report.Summary.Baselines != 2 || report.Summary.Builds != 0 {
		t.Fatalf("summary = %#v", report.Summary)
	}
	for _, iteration := range report.Iterations {
		if !iteration.OK || iteration.Rootfs != RootfsSourceBaseline {
			t.Fatalf("iteration = %#v", iteration)
		}
	}
}

func TestReadyPreservesSuccessfulMeasurementWhenTeardownFails(t *testing.T) {
	previousCreate := createReadyWorkspace
	previousStart := startReadyWorkspace
	previousExec := execReadyWorkspace
	previousDelete := deleteReadyWorkspace
	t.Cleanup(func() {
		createReadyWorkspace = previousCreate
		startReadyWorkspace = previousStart
		execReadyWorkspace = previousExec
		deleteReadyWorkspace = previousDelete
	})

	createReadyWorkspace = func(context.Context, workspace.Options) (workspace.Result, error) {
		return workspace.Result{Image: rootfs.Provenance{ImageRef: "local/base:prepared", BuilderPhase: "copy-baseline"}}, nil
	}
	startReadyWorkspace = func(context.Context, workspace.Options) (workspace.Result, error) {
		return workspace.Result{}, nil
	}
	execReadyWorkspace = func(context.Context, workspace.Options, execprotocol.ExecRequest) (execprotocol.ExecResult, error) {
		exitCode := 0
		result := execprotocol.NewExecResult(execprotocol.ExecStatusExited)
		result.ExitCode = &exitCode
		return result, nil
	}
	deleteReadyWorkspace = func(ctx context.Context, _ workspace.Options, _ workspace.DeleteOptions) (workspace.DeleteResult, error) {
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) < 44*time.Second {
			t.Fatalf("cleanup deadline = %v, want at least 44 seconds", deadline)
		}
		return workspace.DeleteResult{}, context.DeadlineExceeded
	}

	report, err := Ready(context.Background(), ReadyOptions{
		BootOptions: BootOptions{
			StateDir:    t.TempDir(),
			ImageRef:    "local/base:prepared",
			ExecCommand: "true",
			Iterations:  1,
			Timeout:     time.Second,
		},
		ProbeMode: ReadyProbeStructuredExec,
	})
	if err != nil {
		t.Fatalf("Ready: %v", err)
	}
	iteration := report.Iterations[0]
	if !iteration.OK || iteration.Error != "" || !strings.Contains(iteration.TeardownError, "deadline exceeded") {
		t.Fatalf("iteration = %#v", iteration)
	}
	if report.Summary.Failures != 0 || report.Summary.TeardownFailures != 1 || report.Summary.Baselines != 1 {
		t.Fatalf("summary = %#v", report.Summary)
	}
}

func TestSummarizeReadyIterationsReportsNearestRankP95(t *testing.T) {
	iterations := make([]ReadyIteration, 20)
	for i := range iterations {
		value := int64(i + 1)
		iterations[i] = ReadyIteration{
			OK:         true,
			Rootfs:     RootfsSourceBaseline,
			DurationMs: value,
			Phases: ReadyPhases{
				RootfsPrepareMs:    value + 10,
				WorkspacePrepareMs: value + 20,
				LifecycleMs:        value + 30,
				InterfaceReadyMs:   value + 40,
				RuntimeReadyMs:     value + 50,
				ProbeMs:            value + 60,
			},
		}
	}
	summary := SummarizeReadyIterations(iterations)
	if summary.FullReady.P50Ms != 10 || summary.FullReady.P95Ms != 19 || summary.FullReady.MaxMs != 20 {
		t.Fatalf("full-ready distribution = %#v, want p50=10 p95=19 max=20", summary.FullReady)
	}
	if summary.RootfsPrepare.P50Ms != 20 || summary.RootfsPrepare.P95Ms != 29 || summary.Lifecycle.P95Ms != 49 || summary.InterfaceReady.P95Ms != 59 || summary.RuntimeReady.P95Ms != 69 || summary.Probe.P95Ms != 79 {
		t.Fatalf("phase distributions = rootfs:%#v lifecycle:%#v interface:%#v runtime:%#v probe:%#v", summary.RootfsPrepare, summary.Lifecycle, summary.InterfaceReady, summary.RuntimeReady, summary.Probe)
	}
}

func TestReadyLifecycleModesMeasureOnlySelectedTransition(t *testing.T) {
	tests := []struct {
		name          string
		mode          ReadyStartMode
		wantStarts    int
		wantForks     int
		wantSnapshots int
		wantHalts     int
		wantPauses    int
		wantResumes   int
		wantDeletes   int
	}{
		{name: "snapshot fork", mode: ReadyStartSnapshotFork, wantStarts: 1, wantForks: 2, wantSnapshots: 1, wantHalts: 1, wantDeletes: 3},
		{name: "snapshot restore", mode: ReadyStartSnapshotRestore, wantStarts: 3, wantSnapshots: 1, wantHalts: 3, wantDeletes: 1},
		{name: "paused resume", mode: ReadyStartPausedResume, wantStarts: 1, wantPauses: 3, wantResumes: 2, wantDeletes: 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			previousCreate := createReadyWorkspace
			previousStart := startReadyWorkspace
			previousFork := createReadyWorkspaceFromSnapshot
			previousSnapshot := snapshotReadyWorkspace
			previousControl := controlReadyWorkspace
			previousPause := pauseReadyWorkspace
			previousResume := resumeReadyWorkspace
			previousExec := execReadyWorkspace
			previousDelete := deleteReadyWorkspace
			t.Cleanup(func() {
				createReadyWorkspace = previousCreate
				startReadyWorkspace = previousStart
				createReadyWorkspaceFromSnapshot = previousFork
				snapshotReadyWorkspace = previousSnapshot
				controlReadyWorkspace = previousControl
				pauseReadyWorkspace = previousPause
				resumeReadyWorkspace = previousResume
				execReadyWorkspace = previousExec
				deleteReadyWorkspace = previousDelete
			})

			var creates, starts, forks, snapshots, halts, pauses, resumes, deletes, execs int
			createReadyWorkspace = func(_ context.Context, opts workspace.Options) (workspace.Result, error) {
				creates++
				if opts.ExecCommand != "" {
					t.Fatalf("source probe was baked into create: %q", opts.ExecCommand)
				}
				return workspace.Result{Image: rootfs.Provenance{ImageRef: "local/base:prepared", BuilderPhase: "copy-baseline"}}, nil
			}
			startReadyWorkspace = func(_ context.Context, opts workspace.Options) (workspace.Result, error) {
				starts++
				if tt.mode == ReadyStartSnapshotRestore && starts > 1 && opts.FromSnapshot != readySnapshotTag {
					t.Fatalf("restore start tag = %q", opts.FromSnapshot)
				}
				return workspace.Result{}, nil
			}
			createReadyWorkspaceFromSnapshot = func(_ context.Context, opts workspace.Options, source, tag string) (workspace.Result, error) {
				forks++
				if source == "" || tag != readySnapshotTag || opts.Name == source {
					t.Fatalf("fork target=%q source=%q tag=%q", opts.Name, source, tag)
				}
				return workspace.Result{}, nil
			}
			snapshotReadyWorkspace = func(_ context.Context, _ workspace.Options, tag string) (vmkit.SnapshotManifest, error) {
				snapshots++
				if tag != readySnapshotTag {
					t.Fatalf("snapshot tag = %q", tag)
				}
				return vmkit.SnapshotManifest{Tag: tag}, nil
			}
			controlReadyWorkspace = func(_ context.Context, _ workspace.Options, command string) (vmkit.Response, error) {
				if command != "halt" {
					t.Fatalf("control command = %q", command)
				}
				halts++
				return vmkit.Response{OK: true}, nil
			}
			pauseReadyWorkspace = func(_ context.Context, _ workspace.Options) (vmkit.Response, error) {
				pauses++
				return vmkit.Response{OK: true}, nil
			}
			resumeReadyWorkspace = func(_ context.Context, _ workspace.Options) (vmkit.Response, error) {
				resumes++
				return vmkit.Response{OK: true}, nil
			}
			execReadyWorkspace = func(_ context.Context, _ workspace.Options, req execprotocol.ExecRequest) (execprotocol.ExecResult, error) {
				execs++
				if len(req.Argv) == 0 {
					t.Fatal("empty exec readiness request")
				}
				exitCode := 0
				result := execprotocol.NewExecResult(execprotocol.ExecStatusExited)
				result.ExitCode = &exitCode
				return result, nil
			}
			deleteReadyWorkspace = func(_ context.Context, _ workspace.Options, opts workspace.DeleteOptions) (workspace.DeleteResult, error) {
				deletes++
				if !opts.Force {
					t.Fatal("perf cleanup was not forced")
				}
				return workspace.DeleteResult{Response: vmkit.Response{OK: true}, Deleted: true}, nil
			}

			report, err := Ready(context.Background(), ReadyOptions{
				BootOptions: BootOptions{
					StateDir:    t.TempDir(),
					ImageRef:    "local/base:prepared",
					ExecCommand: "printf ready",
					Iterations:  2,
					Timeout:     time.Second,
				},
				StartMode: tt.mode,
				ProbeMode: ReadyProbeStructuredExec,
			})
			if err != nil {
				t.Fatalf("Ready(%s): %v", tt.mode, err)
			}
			if report.Setup == nil || !report.Setup.Excluded || report.Setup.Rootfs != RootfsSourceBaseline {
				t.Fatalf("setup = %#v", report.Setup)
			}
			if report.Boundary.Stop != "after_successful_structured_exec_command" || report.CacheCondition != "host_page_cache_uncontrolled" {
				t.Fatalf("measurement contract = boundary:%#v cache:%q", report.Boundary, report.CacheCondition)
			}
			if report.Summary.Count != 2 || report.Summary.Failures != 0 || report.Summary.Baselines != 0 || report.Summary.Builds != 0 {
				t.Fatalf("summary = %#v", report.Summary)
			}
			if creates != 1 || starts != tt.wantStarts || forks != tt.wantForks || snapshots != tt.wantSnapshots || halts != tt.wantHalts || pauses != tt.wantPauses || resumes != tt.wantResumes || deletes != tt.wantDeletes {
				t.Fatalf("calls create=%d start=%d fork=%d snapshot=%d halt=%d pause=%d resume=%d delete=%d", creates, starts, forks, snapshots, halts, pauses, resumes, deletes)
			}
			// One source readiness check plus interface and command probes for each iteration.
			if execs != 5 {
				t.Fatalf("structured exec calls = %d, want 5", execs)
			}
		})
	}
}

func TestReadyModeParsingRejectsAmbiguousValues(t *testing.T) {
	if mode, err := ParseReadyStartMode("snapshot-fork"); err != nil || mode != ReadyStartSnapshotFork {
		t.Fatalf("ParseReadyStartMode = %q, %v", mode, err)
	}
	if mode, err := ParseReadyProbeMode("exec"); err != nil || mode != ReadyProbeStructuredExec {
		t.Fatalf("ParseReadyProbeMode = %q, %v", mode, err)
	}
	if _, err := ParseReadyStartMode("warm"); err == nil {
		t.Fatal("ParseReadyStartMode accepted ambiguous warm mode")
	}
	if _, err := ParseReadyProbeMode("console-ish"); err == nil {
		t.Fatal("ParseReadyProbeMode accepted ambiguous console mode")
	}
}

func TestReadyWorkspaceNamesLeaveFirecrackerSocketBudget(t *testing.T) {
	for _, name := range []string{readyWorkspaceName("r", 20), readyWorkspaceName("rs", 0)} {
		if len(name) > 32 {
			t.Fatalf("benchmark workspace name %q is %d bytes, want at most 32", name, len(name))
		}
		if err := workspace.ValidateName(name); err != nil {
			t.Fatalf("benchmark workspace name %q: %v", name, err)
		}
	}
}
