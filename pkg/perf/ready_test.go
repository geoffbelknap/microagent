package perf

import (
	"context"
	"io"
	"net"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/geoffbelknap/microagent/pkg/rootfs"
	"github.com/geoffbelknap/microagent/pkg/vmkit"
	"github.com/geoffbelknap/microagent/pkg/workspace"
)

func TestReadyMeasuresInteractivePipelineAndCleansUp(t *testing.T) {
	previousCreate := createReadyWorkspace
	previousStart := startReadyWorkspace
	previousDial := dialReadyConsole
	previousSend := sendReadyCommand
	previousDelete := deleteReadyWorkspace
	t.Cleanup(func() {
		createReadyWorkspace = previousCreate
		startReadyWorkspace = previousStart
		dialReadyConsole = previousDial
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
	dialReadyConsole = func(_ context.Context, opts workspace.ConsoleOptions) (net.Conn, error) {
		if !opts.RequireCommandReady {
			t.Fatal("shell readiness did not require a command round-trip")
		}
		client, server := net.Pipe()
		_ = server.Close()
		return client, nil
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
		StateDir:    dir,
		ImageRef:    "local/coding-agent:prepared",
		ExecCommand: "pi --version",
		Iterations:  2,
		Timeout:     time.Second,
		RootfsBaseline: func(rootfsPath string) (string, rootfs.Provenance, bool) {
			return filepath.Join(dir, "baseline.ext4"), rootfs.Provenance{ImageRef: "local/coding-agent:prepared", OutputPath: rootfsPath}, true
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
	if report.Summary.Failures != 0 || report.Summary.Baselines != 2 || report.Summary.Builds != 0 {
		t.Fatalf("summary = %#v", report.Summary)
	}
	for _, iteration := range report.Iterations {
		if !iteration.OK || iteration.Rootfs != RootfsSourceBaseline {
			t.Fatalf("iteration = %#v", iteration)
		}
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
				SupervisorStartMs:  value + 30,
				ShellWaitMs:        value + 40,
				BareGuestReadyMs:   value + 50,
				AgentProbeMs:       value + 60,
			},
		}
	}
	summary := SummarizeReadyIterations(iterations)
	if summary.InteractiveReady.P95Ms != 19 || summary.InteractiveReady.MaxMs != 20 {
		t.Fatalf("interactive distribution = %#v, want p95=19 max=20", summary.InteractiveReady)
	}
	if summary.RootfsPrepare.P95Ms != 29 || summary.SupervisorStart.P95Ms != 49 || summary.ShellWait.P95Ms != 59 || summary.BareGuestReady.P95Ms != 69 || summary.AgentProbe.P95Ms != 79 {
		t.Fatalf("phase distributions = rootfs:%#v start:%#v shell:%#v guest:%#v probe:%#v", summary.RootfsPrepare, summary.SupervisorStart, summary.ShellWait, summary.BareGuestReady, summary.AgentProbe)
	}
}
