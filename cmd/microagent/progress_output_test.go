package main

import (
	"bytes"
	"context"
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
