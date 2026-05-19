package perf

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

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

func TestSummarizeIterations(t *testing.T) {
	summary := SummarizeIterations([]Iteration{
		{Name: "one", OK: true, DurationMs: 30},
		{Name: "two", OK: true, DurationMs: 10},
		{Name: "three", OK: true, DurationMs: 20},
	})
	if summary.Count != 3 || summary.MinMs != 10 || summary.AvgMs != 20 || summary.MaxMs != 30 {
		t.Fatalf("summary = %#v", summary)
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
	req := workspace.Request(opts, "run", filepath.Join(dir, "rootfs.ext4"), "req-1")
	if err := workspace.WriteProcessState(opts, req, vmkit.StateStopped, 0, ""); err != nil {
		t.Fatal(err)
	}
	_, err := Footprint(dir, "research")
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
