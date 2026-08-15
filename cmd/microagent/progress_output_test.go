package main

import (
	"bytes"
	"context"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/geoffbelknap/microagent/pkg/operation"
)

func TestOperationProgressPrinterUsesStablePlainPhaseTransitions(t *testing.T) {
	var output bytes.Buffer
	printer := newOperationProgressPrinter(&output, false, progressPrinterOptions{AlwaysPrintCompletion: true})
	printer.print(operation.ProgressEvent{Operation: "build", Phase: "download", Label: "Build rootfs", Message: "downloading", Bytes: 1, TotalBytes: 10})
	printer.print(operation.ProgressEvent{Operation: "build", Phase: "download", Label: "Build rootfs", Message: "downloading", Bytes: 9, TotalBytes: 10})
	printer.print(operation.ProgressEvent{Operation: "build", Phase: "verify", Label: "Build rootfs", Message: "verifying"})
	printer.print(operation.ProgressEvent{Operation: "build", Phase: "complete", Label: "Build rootfs", ElapsedMs: 1200, Status: operation.ProgressSucceeded})
	printer.close()

	lines := strings.Split(strings.TrimSpace(output.String()), "\n")
	if len(lines) != 3 {
		t.Fatalf("plain progress emitted %d lines, want 3:\n%s", len(lines), output.String())
	}
	if !strings.Contains(lines[0], "• Build rootfs · downloading") || !strings.Contains(lines[1], "• Build rootfs · verifying") {
		t.Fatalf("plain phase lines = %q", lines)
	}
	if lines[2] != "✓ [ 1.20s] Build rootfs" {
		t.Fatalf("completion = %q", lines[2])
	}
}

func TestOperationProgressPrinterSuppressesFastOperation(t *testing.T) {
	var output bytes.Buffer
	printer := newOperationProgressPrinter(&output, true, progressPrinterOptions{Delay: time.Hour})
	printer.print(operation.ProgressEvent{Operation: "fast", Phase: "work", Label: "Fast operation", Indeterminate: true})
	printer.print(operation.ProgressEvent{Operation: "fast", Phase: "complete", Label: "Fast operation", ElapsedMs: 10, Status: operation.ProgressSucceeded})
	printer.close()
	if output.Len() != 0 {
		t.Fatalf("fast operation output = %q", output.String())
	}
}

func TestOperationProgressPrinterCleansInteractiveLineOnFailure(t *testing.T) {
	var output bytes.Buffer
	printer := newOperationProgressPrinter(&output, true, progressPrinterOptions{AlwaysPrintCompletion: true})
	printer.print(operation.ProgressEvent{Operation: "start", Phase: "boot", Label: "Start workspace", Message: "booting", Indeterminate: true})
	printer.print(operation.ProgressEvent{Operation: "start", Phase: "complete", Label: "Start workspace", ElapsedMs: 900, Status: operation.ProgressFailed})
	printer.close()
	got := output.String()
	if !strings.Contains(got, "\r\033[2K") || !strings.Contains(got, "✗ [ 900ms] Start workspace\n") {
		t.Fatalf("interactive failure output = %q", got)
	}
}

func TestCommandProgressDistinguishesCancellation(t *testing.T) {
	var output bytes.Buffer
	progress := newCommandProgress(&output, false, "wait", "Wait for workspace")
	progress.printer.opts.AlwaysPrintCompletion = true
	progress.print(operation.ProgressEvent{Phase: "waiting", Message: "running"})
	progress.close(context.Canceled)
	if got := output.String(); !strings.Contains(got, "! [") || !strings.Contains(got, "Wait for workspace") {
		t.Fatalf("canceled progress = %q", got)
	}
}

func TestCommandProgressForIsSilentInJSONMode(t *testing.T) {
	previous := outputFormat
	outputFormat = "json"
	t.Cleanup(func() { outputFormat = previous })

	progress, finish := commandProgressFor(os.Stdout, "start", "Start workspace")
	if progress != nil {
		t.Fatal("JSON mode installed a human progress callback")
	}
	finish(nil)
}

func TestCommandProgressUntilPhaseStopsBeforeLongLivedOutput(t *testing.T) {
	previousFormat := outputFormat
	previousStderr := os.Stderr
	outputFormat = "text"
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stderr = writer
	t.Cleanup(func() {
		outputFormat = previousFormat
		os.Stderr = previousStderr
		_ = reader.Close()
		_ = writer.Close()
	})

	progress, finish := commandProgressUntilPhaseFor(os.Stdout, "service", "Serve workspace", "ready", true)
	progress(operation.ProgressEvent{Phase: "starting", Message: "starting"})
	progress(operation.ProgressEvent{Phase: "ready", Message: "ready"})
	progress(operation.ProgressEvent{Phase: "later", Message: "must not render"})
	finish(context.Canceled)
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	got := string(data)
	if strings.Count(got, "Serve workspace") != 1 || !strings.Contains(got, "✓ [") || strings.Contains(got, "must not render") || strings.Contains(got, "!") {
		t.Fatalf("readiness output = %q", got)
	}
}
