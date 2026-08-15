package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"

	"github.com/geoffbelknap/microagent/pkg/vmkit"
	"github.com/geoffbelknap/microagent/pkg/workspace"
)

func runList(_ context.Context, args []string, stdout *os.File) error {
	opts := stateCommandOptions{StateDir: defaultStateDir()}
	fs := newCommandFlagSet("list")
	fs.StringVar(&opts.StateDir, "state-dir", opts.StateDir, "State directory")
	if err := parseCommandFlags(fs, stdout, reorderFlagArgs(args)); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("unexpected list argument: %s", fs.Arg(0))
	}
	entries, err := workspace.List(opts.StateDir)
	if err != nil {
		return err
	}
	return writeWorkspaceList(stdout, entries)
}

func runPS(_ context.Context, args []string, stdout *os.File) error {
	opts := stateCommandOptions{StateDir: defaultStateDir()}
	fs := newCommandFlagSet("ps")
	fs.StringVar(&opts.StateDir, "state-dir", opts.StateDir, "State directory")
	if err := parseCommandFlags(fs, stdout, reorderFlagArgs(args)); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("unexpected ps argument: %s", fs.Arg(0))
	}
	entries, err := workspace.List(opts.StateDir)
	if err != nil {
		return err
	}
	total := len(entries)
	return writeRunningWorkspaceList(stdout, filterRunningWorkspaces(entries), total)
}

// isLiveRecordedState reports whether the durable record claims the workspace
// is live. Read-only views intentionally filter that record without reconciling
// it; explicit gc owns stale-runtime detection and cleanup.
func isLiveRecordedState(state vmkit.VMState) bool {
	switch state {
	case vmkit.StateStarting, vmkit.StateRunning, vmkit.StatePaused, vmkit.StateQuarantined, vmkit.StateStopping:
		return true
	}
	return false
}

func filterRunningWorkspaces(entries []workspaceListEntry) []workspaceListEntry {
	filtered := entries[:0]
	for _, entry := range entries {
		if isLiveRecordedState(vmkit.VMState(entry.State)) {
			filtered = append(filtered, entry)
		}
	}
	return filtered
}

// runGC sweeps the host for VMs recorded as running whose firecracker process
// is gone (crashed, OOM-killed, host-rebooted, or an orphaned supervisor) and
// reaps them — reconciling runtime state and reclaiming lingering companion
// processes + transient network state. It does not touch healthy VMs. This is
// the backstop for the supervisor deadman; safe to run on demand.
func runGC(ctx context.Context, args []string, stdout *os.File) error {
	opts := stateCommandOptions{StateDir: defaultStateDir()}
	backend := hostBackend()
	supervisorPath := defaultSupervisorPath(backend)
	supervisorExplicit := hasFlagValue(args, "supervisor")
	fs := newCommandFlagSet("gc")
	fs.StringVar(&opts.StateDir, "state-dir", opts.StateDir, "State directory")
	fs.StringVar(&supervisorPath, "supervisor", supervisorPath, "supervisor path")
	fs.StringVar(&backend, "backend", backend, "Backend identity (internal; must match this install)")
	if err := parseCommandFlags(fs, stdout, reorderFlagArgs(args)); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if !supervisorExplicit {
		supervisorPath = defaultSupervisorPath(backend)
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("unexpected gc argument: %s", fs.Arg(0))
	}
	progress, finishProgress := commandProgressFor(stdout, "workspace-gc", "Reconcile workspaces")
	result, err := workspace.GC(ctx, workspace.Options{
		StateDir: opts.StateDir, Backend: backend, SupervisorPath: supervisorPath, Progress: progress,
	})
	finishProgress(err)
	if err != nil {
		return err
	}
	if err := writeJSON(stdout, result); err != nil {
		return err
	}
	if len(result.Failed) > 0 {
		return cliExitError{Code: 1, Silent: true}
	}
	return nil
}

func runClone(args []string, stdout *os.File) error {
	opts := stateCommandOptions{StateDir: defaultStateDir()}
	fs := newCommandFlagSet("clone")
	fs.StringVar(&opts.StateDir, "state-dir", opts.StateDir, "State directory")
	if err := parseCommandFlags(fs, stdout, reorderFlagArgs(args)); err != nil {
		return err
	}
	if fs.NArg() != 2 {
		return fmt.Errorf("usage: microagent clone <source> <target> [--state-dir <dir>]")
	}
	source := fs.Arg(0)
	target := fs.Arg(1)
	if err := validateWorkspaceName(source); err != nil {
		return err
	}
	if err := validateWorkspaceName(target); err != nil {
		return err
	}
	progress, finishProgress := commandProgressFor(stdout, "workspace-clone", "Clone workspace")
	result, err := workspace.CloneWithOptions(workspace.CloneOptions{
		StateDir: opts.StateDir, Source: source, Target: target, Progress: progress,
	})
	finishProgress(err)
	if err != nil {
		return err
	}
	return writeWorkspaceResult(stdout, result)
}

func runProfiles(args []string, stdout *os.File) error {
	fs := newCommandFlagSet("profiles")
	if err := parseCommandFlags(fs, stdout, reorderFlagArgs(args)); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("unexpected profiles argument: %s", fs.Arg(0))
	}
	if outputJSON(stdout) {
		return writeJSON(stdout, map[string]any{"profiles": resourceProfiles})
	}
	fmt.Fprintf(stdout, "%-10s %-10s %-6s %-10s %s\n", "NAME", "MEMORY", "CPUS", "DISK", "DESCRIPTION")
	for _, profile := range resourceProfiles {
		fmt.Fprintf(stdout, "%-10s %-10d %-6d %-10d %s\n",
			profile.Name,
			profile.Resources.MemoryMiB,
			profile.Resources.CPUCount,
			profile.Resources.SizeMiB,
			profile.Description,
		)
	}
	return nil
}
