package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/geoffbelknap/microagent/pkg/vmkit"
	"github.com/geoffbelknap/microagent/pkg/workspace"
)

func runLogs(ctx context.Context, args []string, stdout *os.File) error {
	opts := stateCommandOptions{StateDir: defaultStateDir()}
	follow := false
	fs := newCommandFlagSet("logs")
	fs.StringVar(&opts.StateDir, "state-dir", opts.StateDir, "State directory")
	fs.BoolVar(&follow, "follow", false, "Stream the serial buffer and new output until the workspace stops or interrupted")
	fs.BoolVar(&follow, "f", false, "Alias for --follow")
	if err := parseCommandFlags(fs, stdout, reorderFlagArgs(args)); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("usage: microagent logs <name> [--follow] [--state-dir <dir>]")
	}
	name := fs.Arg(0)
	if err := validateWorkspaceName(name); err != nil {
		return err
	}
	if follow {
		if outputStructured() {
			return fmt.Errorf("logs --follow is not supported with --json/--output json; omit --follow to capture the buffer once")
		}
		return followLogs(ctx, opts.StateDir, name, stdout)
	}
	data, err := workspace.ReadLogs(opts.StateDir, name)
	if err != nil {
		return err
	}
	if outputStructured() {
		return writeJSON(stdout, map[string]any{
			"workspace": name,
			"logs":      string(data),
		})
	}
	_, err = stdout.Write(data)
	return err
}

// followLogs prints the captured serial buffer, then streams new output as it
// is appended, until the workspace leaves the running state or the caller
// interrupts (Ctrl-C). It is the streaming counterpart to ReadLogs.
func followLogs(ctx context.Context, stateDir, name string, stdout *os.File) error {
	// Surface the same "no such workspace" error as the non-follow path before
	// entering the stream loop.
	if _, err := workspace.ReadLogs(stateDir, name); err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()

	serialPath := workspace.SerialLogPath(stateDir, name)
	var offset int64
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		n, err := writeSerialTail(serialPath, offset, stdout)
		if err != nil {
			return err
		}
		offset += n

		// Once the workspace is no longer running, drain any final bytes and stop
		// so the command does not hang on a stopped workspace.
		state, _, stateErr := workspace.LatestStartState(stateDir, name)
		if stateErr != nil || state != vmkit.StateRunning {
			_, _ = writeSerialTail(serialPath, offset, stdout)
			return nil
		}
		select {
		case <-ctx.Done():
			_, _ = writeSerialTail(serialPath, offset, stdout)
			return nil
		case <-ticker.C:
		}
	}
}

// writeSerialTail copies bytes from path starting at offset to stdout and
// returns the number of bytes written. A not-yet-created serial log is treated
// as empty rather than an error so callers can poll a workspace that is still
// booting.
func writeSerialTail(path string, offset int64, stdout *os.File) (int64, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}
	defer func() { _ = f.Close() }()
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return 0, err
	}
	return io.Copy(stdout, f)
}

func runEvents(ctx context.Context, args []string, stdout *os.File) error {
	opts := stateCommandOptions{StateDir: defaultStateDir()}
	follow := false
	fs := newCommandFlagSet("events")
	fs.StringVar(&opts.StateDir, "state-dir", opts.StateDir, "State directory")
	fs.BoolVar(&follow, "follow", false, "Stream new lifecycle events until the workspace stops or interrupted")
	fs.BoolVar(&follow, "f", false, "Alias for --follow")
	if err := parseCommandFlags(fs, stdout, reorderFlagArgs(args)); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("usage: microagent events <name> [--follow] [--state-dir <dir>]")
	}
	name := fs.Arg(0)
	if err := validateWorkspaceName(name); err != nil {
		return err
	}
	events, err := workspace.ReadEvents(opts.StateDir, name)
	if err != nil {
		return err
	}
	if follow {
		if outputStructured() {
			return fmt.Errorf("events --follow is not supported with --json/--output json; omit --follow for a one-shot snapshot")
		}
		return followEvents(ctx, opts.StateDir, name, events, stdout)
	}
	if outputStructured() {
		return writeJSON(stdout, map[string]any{"workspace": name, "events": events})
	}
	for _, event := range events {
		writeEventLine(stdout, event)
	}
	return nil
}

func writeEventLine(stdout *os.File, event workspace.EventFile) {
	line := fmt.Sprintf("%s  %s", event.ObservedAt, event.State)
	if event.Detail != "" {
		line += "  " + event.Detail
	}
	fmt.Fprintln(stdout, line)
}

// eventFollowComplete reports whether the latest event is a terminal lifecycle
// state, so events --follow returns instead of polling forever. Quarantine
// stops the runtime after any best-effort forensic capture, so it is terminal.
func eventFollowComplete(events []workspace.EventFile) bool {
	if len(events) == 0 {
		return false
	}
	switch events[len(events)-1].State {
	case vmkit.StateHalted, vmkit.StateStopped, vmkit.StateFailed, vmkit.StateQuarantined:
		return true
	default:
		return false
	}
}

// runEgress surfaces a workspace's egress decisions — the mediator's
// connection-level records (egress-access.jsonl) and the broker's per-request
// decision records (broker-access.jsonl), merged time-ordered into one view.
// It mirrors runEvents: a one-shot snapshot by default, or a live tail with
// --follow. An absent file is not an error (mediation/broker may be off, or no
// decision has been recorded yet) — it reports as an empty list.
func runEgress(ctx context.Context, args []string, stdout *os.File) error {
	opts := stateCommandOptions{StateDir: defaultStateDir()}
	follow := false
	fs := newCommandFlagSet("egress")
	fs.StringVar(&opts.StateDir, "state-dir", opts.StateDir, "State directory")
	fs.BoolVar(&follow, "follow", false, "Stream new egress decisions until the workspace stops or interrupted")
	fs.BoolVar(&follow, "f", false, "Alias for --follow")
	if err := parseCommandFlags(fs, stdout, reorderFlagArgs(args)); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("usage: microagent egress <name> [--follow] [--state-dir <dir>]")
	}
	name := fs.Arg(0)
	if err := validateWorkspaceName(name); err != nil {
		return err
	}
	mediator, err := workspace.ReadEgressAudit(opts.StateDir, name)
	if err != nil {
		return err
	}
	brokered, err := workspace.ReadBrokerAccess(opts.StateDir, name)
	if err != nil {
		return err
	}
	if follow {
		if outputStructured() {
			return fmt.Errorf("egress --follow is not supported with --json/--output json; omit --follow for a one-shot snapshot")
		}
		return followEgress(ctx, opts.StateDir, name, mediator, brokered, stdout)
	}
	merged := workspace.MergeEgressEvents(mediator, brokered)
	if outputStructured() {
		return writeJSON(stdout, map[string]any{"workspace": name, "egress": merged})
	}
	for _, event := range merged {
		writeEgressLine(stdout, event)
	}
	return nil
}

// writeEgressLine renders one egress decision as a compact human line:
// "<ts>  <event>  <host>  <reason-or-dst>". The trailing column prefers the
// reason (why a decision was made) and falls back to the destination so a row
// with neither still aligns.
func writeEgressLine(stdout *os.File, event workspace.EgressEvent) {
	host := event.Host
	if host == "" {
		host = "-"
	}
	detail := event.Reason
	if detail == "" {
		detail = event.Dst
	}
	line := fmt.Sprintf("%s  %s  %s", event.TS, event.Event, host)
	if detail != "" {
		line += "  " + detail
	}
	fmt.Fprintln(stdout, line)
}

// followEgress prints the recorded egress decisions (mediator + broker,
// merged), then streams newly appended decisions from both files, returning
// when the workspace reaches a terminal lifecycle state or the caller
// interrupts. Both logs are append-only, so new records are detected by a
// growing per-file record count; each poll's new records are merged before
// printing so the interleaved view stays time-ordered. The terminal-state
// check reads the lifecycle event history, since the audit logs themselves
// carry no lifecycle signal.
func followEgress(ctx context.Context, stateDir, name string, seenMediator, seenBroker []workspace.EgressEvent, stdout *os.File) error {
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()
	for _, event := range workspace.MergeEgressEvents(seenMediator, seenBroker) {
		writeEgressLine(stdout, event)
	}
	mediatorCount, brokerCount := len(seenMediator), len(seenBroker)
	if lifecycle, err := workspace.ReadEvents(stateDir, name); err == nil && eventFollowComplete(lifecycle) {
		return nil
	}
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		mediator, err := workspace.ReadEgressAudit(stateDir, name)
		if err != nil {
			return err
		}
		brokered, err := workspace.ReadBrokerAccess(stateDir, name)
		if err != nil {
			return err
		}
		var freshMediator, freshBroker []workspace.EgressEvent
		if len(mediator) > mediatorCount {
			freshMediator = mediator[mediatorCount:]
			mediatorCount = len(mediator)
		}
		if len(brokered) > brokerCount {
			freshBroker = brokered[brokerCount:]
			brokerCount = len(brokered)
		}
		for _, event := range workspace.MergeEgressEvents(freshMediator, freshBroker) {
			writeEgressLine(stdout, event)
		}
		if lifecycle, err := workspace.ReadEvents(stateDir, name); err == nil && eventFollowComplete(lifecycle) {
			return nil
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

func runStats(ctx context.Context, args []string, stdout *os.File) error {
	opts := stateCommandOptions{StateDir: defaultStateDir()}
	follow := false
	fs := newCommandFlagSet("stats")
	fs.StringVar(&opts.StateDir, "state-dir", opts.StateDir, "State directory")
	fs.BoolVar(&follow, "follow", false, "Stream resource samples until the workspace stops or interrupted")
	fs.BoolVar(&follow, "f", false, "Alias for --follow")
	if err := parseCommandFlags(fs, stdout, reorderFlagArgs(args)); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("usage: microagent stats <name> [--follow] [--state-dir <dir>]")
	}
	name := fs.Arg(0)
	if err := validateWorkspaceName(name); err != nil {
		return err
	}
	if follow {
		if outputStructured() {
			return fmt.Errorf("stats --follow is not supported with --json/--output json; omit --follow for a single sample")
		}
		return followStats(ctx, opts.StateDir, name, stdout)
	}
	stats, err := workspace.SampleStats(opts.StateDir, name)
	if err != nil {
		return err
	}
	if outputStructured() {
		return writeJSON(stdout, stats)
	}
	fmt.Fprintln(stdout, formatStatsLine(stats))
	return nil
}

func formatStatsLine(stats workspace.Stats) string {
	const mib = 1024 * 1024
	return fmt.Sprintf("pid=%d  cpu=%.1f%%  mem=%.1f MiB  io_read=%.1f MiB  io_write=%.1f MiB",
		stats.PID,
		stats.CPUPercent,
		float64(stats.MemoryBytes)/mib,
		float64(stats.IOReadBytes)/mib,
		float64(stats.IOWriteBytes)/mib,
	)
}

func followStats(ctx context.Context, stateDir, name string, stdout *os.File) error {
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()
	for {
		stats, err := workspace.SampleStats(stateDir, name)
		if err != nil {
			// Stop quietly once the workspace is no longer running; surface any
			// other error.
			if state, _, stateErr := workspace.LatestStartState(stateDir, name); stateErr == nil && state != vmkit.StateRunning {
				return nil
			}
			return err
		}
		fmt.Fprintln(stdout, formatStatsLine(stats))
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(800 * time.Millisecond):
		}
	}
}

// followEvents prints the recorded events, then streams newly appended events
// as the workspace changes state, returning when the workspace reaches a
// terminal state or the caller interrupts. events.json is rewritten wholesale
// on each change, so new entries are detected by a growing event count.
func followEvents(ctx context.Context, stateDir, name string, seen []workspace.EventFile, stdout *os.File) error {
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()
	for _, event := range seen {
		writeEventLine(stdout, event)
	}
	count := len(seen)
	if eventFollowComplete(seen) {
		return nil
	}
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		events, err := workspace.ReadEvents(stateDir, name)
		if err != nil {
			return err
		}
		if len(events) > count {
			for _, event := range events[count:] {
				writeEventLine(stdout, event)
			}
			count = len(events)
		}
		if eventFollowComplete(events) {
			return nil
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}
