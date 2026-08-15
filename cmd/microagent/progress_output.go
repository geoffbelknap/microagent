package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/geoffbelknap/microagent/pkg/operation"
	"github.com/geoffbelknap/microagent/pkg/rootfs"
)

const defaultProgressDelay = 300 * time.Millisecond

var progressSpinnerFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

type progressPrinterOptions struct {
	Delay                 time.Duration
	AlwaysPrintCompletion bool
}

// operationProgressPrinter is the single human presentation adapter for
// library-owned progress. It deliberately knows nothing about workspace,
// rootfs, or perf semantics.
type operationProgressPrinter struct {
	out         io.Writer
	interactive bool
	opts        progressPrinterOptions
	events      chan operation.ProgressEvent
	done        chan struct{}
	closeOnce   sync.Once
}

func newOperationProgressPrinter(out io.Writer, interactive bool, opts progressPrinterOptions) *operationProgressPrinter {
	p := &operationProgressPrinter{
		out:         out,
		interactive: interactive,
		opts:        opts,
		events:      make(chan operation.ProgressEvent, 32),
		done:        make(chan struct{}),
	}
	go p.run()
	return p
}

func (p *operationProgressPrinter) print(event operation.ProgressEvent) {
	p.events <- event
}

func (p *operationProgressPrinter) close() {
	p.closeOnce.Do(func() {
		close(p.events)
		<-p.done
	})
}

func (p *operationProgressPrinter) run() {
	defer close(p.done)
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	var current *operation.ProgressEvent
	var started time.Time
	var currentOperation string
	var visible bool
	var lastPlainPhase string
	frame := 0

	clearInteractive := func() {
		if p.interactive && visible {
			fmt.Fprint(p.out, "\r\033[2K")
		}
	}

	elapsed := func(event operation.ProgressEvent) time.Duration {
		if event.ElapsedMs > 0 {
			return time.Duration(event.ElapsedMs) * time.Millisecond
		}
		if started.IsZero() {
			return 0
		}
		return time.Since(started)
	}

	renderCurrent := func(event operation.ProgressEvent) {
		if elapsed(event) < p.opts.Delay {
			return
		}
		if p.interactive {
			fmt.Fprintf(p.out, "\r\033[2K%s [%6s] %s", progressSpinnerFrames[frame], formatProgressDuration(elapsed(event)), progressLabel(event))
			if detail := formatProgressDetail(event); detail != "" {
				fmt.Fprintf(p.out, " · %s", detail)
			}
			visible = true
			return
		}
		if visible && event.Phase == lastPlainPhase {
			return
		}
		fmt.Fprintf(p.out, "• %s", progressLabel(event))
		if detail := formatProgressDetail(event); detail != "" {
			fmt.Fprintf(p.out, " · %s", detail)
		}
		fmt.Fprintln(p.out)
		visible = true
		lastPlainPhase = event.Phase
	}

	for {
		select {
		case event, ok := <-p.events:
			if !ok {
				clearInteractive()
				return
			}
			operationID := event.Operation
			if operationID == "" {
				operationID = event.Label
			}
			if operationID != currentOperation {
				clearInteractive()
				currentOperation = operationID
				started = time.Now()
				visible = false
				lastPlainPhase = ""
			}
			if event.Terminal() {
				duration := elapsed(event)
				if visible || p.opts.AlwaysPrintCompletion || duration >= p.opts.Delay {
					clearInteractive()
					fmt.Fprintf(p.out, "%s [%6s] %s", progressMark(event.Status), formatProgressDuration(duration), progressLabel(event))
					if detail := formatTerminalProgressDetail(event); detail != "" {
						fmt.Fprintf(p.out, " · %s", detail)
					}
					fmt.Fprintln(p.out)
				}
				current = nil
				visible = false
				lastPlainPhase = ""
				continue
			}
			current = &event
			renderCurrent(event)
		case <-ticker.C:
			if current == nil {
				continue
			}
			frame = (frame + 1) % len(progressSpinnerFrames)
			renderCurrent(*current)
		}
	}
}

func progressMark(status operation.ProgressStatus) string {
	switch status {
	case operation.ProgressSucceeded:
		return "✓"
	case operation.ProgressCanceled:
		return "!"
	default:
		return "✗"
	}
}

func progressLabel(event operation.ProgressEvent) string {
	if label := strings.TrimSpace(event.Label); label != "" {
		return label
	}
	if operationID := strings.TrimSpace(event.Operation); operationID != "" {
		return strings.ReplaceAll(operationID, "_", " ")
	}
	return "Working"
}

func formatProgressDetail(event operation.ProgressEvent) string {
	message := strings.TrimSpace(event.Message)
	if message == "" {
		message = strings.ReplaceAll(strings.TrimSpace(event.Phase), "_", " ")
	}
	if event.TotalBytes > 0 {
		done := clampProgress(event.Bytes, event.TotalBytes)
		detail := fmt.Sprintf("%s %s/%s", progressBar(done, event.TotalBytes, 20), formatBytes(done), formatBytes(event.TotalBytes))
		if event.Total > 0 {
			detail += fmt.Sprintf(" (item %d/%d)", clampProgress(event.Current, event.Total), event.Total)
		}
		return joinProgressDetail(message, detail)
	}
	if event.Total > 0 {
		done := clampProgress(event.Current, event.Total)
		return joinProgressDetail(message, fmt.Sprintf("%s %d/%d", progressBar(done, event.Total, 20), done, event.Total))
	}
	return message
}

func formatTerminalProgressDetail(event operation.ProgressEvent) string {
	detail := strings.TrimSpace(event.Message)
	if event.Error != "" {
		detail = joinProgressDetail(detail, strings.TrimSpace(event.Error))
	}
	return detail
}

func joinProgressDetail(left, right string) string {
	if left == "" {
		return right
	}
	if right == "" {
		return left
	}
	return left + " · " + right
}

func clampProgress(value, total int64) int64 {
	if value < 0 {
		return 0
	}
	if value > total {
		return total
	}
	return value
}

func formatProgressDuration(duration time.Duration) string {
	ms := duration.Milliseconds()
	if ms < 1000 {
		return fmt.Sprintf("%dms", ms)
	}
	return fmt.Sprintf("%.2fs", duration.Seconds())
}

type commandProgress struct {
	printer   *operationProgressPrinter
	operation string
	label     string
	started   time.Time
	finish    sync.Once
}

func newCommandProgress(out io.Writer, interactive bool, operationID, label string) *commandProgress {
	return &commandProgress{
		printer:   newOperationProgressPrinter(out, interactive, progressPrinterOptions{Delay: defaultProgressDelay}),
		operation: operationID,
		label:     label,
		started:   time.Now(),
	}
}

func (p *commandProgress) print(event operation.ProgressEvent) {
	event.Operation = p.operation
	event.Label = p.label
	p.printer.print(event)
}

func (p *commandProgress) close(err error) {
	p.finish.Do(func() {
		status := operation.ProgressSucceeded
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			status = operation.ProgressCanceled
		} else if err != nil {
			status = operation.ProgressFailed
		}
		p.printer.print(operation.ProgressEvent{
			Operation: p.operation,
			Phase:     "complete",
			Label:     p.label,
			Status:    status,
			ElapsedMs: time.Since(p.started).Milliseconds(),
		})
		p.printer.close()
	})
}

func rootfsProgress(stdout *os.File, operationID string) (rootfs.ProgressFunc, func(error)) {
	if outputJSON(stdout) {
		return nil, func(error) {}
	}
	labels := map[string]string{
		"create":   "Create workspace",
		"run":      "Run workspace",
		"dispatch": "Dispatch task",
		"rootfs":   "Build rootfs",
	}
	label := labels[operationID]
	if label == "" {
		label = strings.ReplaceAll(operationID, "_", " ")
	}
	progress := newCommandProgress(os.Stderr, fileIsTerminal(os.Stderr), operationID, label)
	return progress.print, progress.close
}

// formatProgressEvent remains the compact detail formatter used by tests and
// snapshot-sized output. Interactive presentation belongs to the shared
// operationProgressPrinter above.
func formatProgressEvent(event rootfs.ProgressEvent) string {
	detail := formatProgressDetail(event)
	if event.Indeterminate && event.Current > 0 {
		detail += " (" + formatElapsed(event.Current) + ")"
	}
	return detail
}

func progressBar(done, total int64, width int) string {
	if width <= 0 {
		width = 20
	}
	filled := 0
	if total > 0 {
		filled = int(done * int64(width) / total)
	}
	if filled < 0 {
		filled = 0
	}
	if filled > width {
		filled = width
	}
	return "[" + strings.Repeat("=", filled) + strings.Repeat("-", width-filled) + "]"
}

func formatElapsed(seconds int64) string {
	if seconds < 60 {
		return fmt.Sprintf("%ds", seconds)
	}
	minutes := seconds / 60
	seconds %= 60
	if minutes < 60 {
		return fmt.Sprintf("%dm%02ds", minutes, seconds)
	}
	hours := minutes / 60
	minutes %= 60
	return fmt.Sprintf("%dh%02dm%02ds", hours, minutes, seconds)
}
