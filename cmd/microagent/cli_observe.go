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

func runList(ctx context.Context, args []string, stdout *os.File) error {
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
	reconcileLiveWorkspaces(ctx, opts.StateDir, entries)
	if entries, err = workspace.List(opts.StateDir); err != nil {
		return err
	}
	return writeWorkspaceList(stdout, entries)
}

func runPS(ctx context.Context, args []string, stdout *os.File) error {
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
	reconcileLiveWorkspaces(ctx, opts.StateDir, entries)
	if entries, err = workspace.List(opts.StateDir); err != nil {
		return err
	}
	return writeWorkspaceList(stdout, filterRunningWorkspaces(entries))
}

// isLiveRecordedState reports whether a recorded state claims the VM is still
// alive — the states whose truth must be re-checked against the running process
// before status/ps/list report them, since a recorded "running" can be stale.
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

// reconcileLiveWorkspaces reaps any listed workspace recorded as live whose
// firecracker process is gone, persisting its true terminal state, so ps/list
// report reality instead of a stale "running". Best-effort and idempotent:
// reconcile failures and healthy VMs leave the recorded state untouched.
func reconcileLiveWorkspaces(ctx context.Context, stateDir string, entries []workspaceListEntry) {
	supervisorPath := defaultSupervisorPath(hostBackend())
	for _, entry := range entries {
		if !isLiveRecordedState(vmkit.VMState(entry.State)) {
			continue
		}
		backend := entry.Backend
		if backend == "" {
			backend = hostBackend()
		}
		_, _ = workspace.Inspect(ctx, workspaceOptions{
			StateDir:       stateDir,
			Name:           entry.Name,
			Backend:        backend,
			SupervisorPath: supervisorPath,
		})
	}
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
	entries, err := workspace.List(opts.StateDir)
	if err != nil {
		return err
	}
	type gcReap struct {
		Name string `json:"name"`
		Was  string `json:"was"`
	}
	checked := 0
	reaped := []gcReap{}
	for _, entry := range entries {
		if vmkit.VMState(entry.State) != vmkit.StateRunning {
			continue
		}
		checked++
		wopts := workspaceOptions{StateDir: opts.StateDir, Name: entry.Name, Backend: backend, SupervisorPath: supervisorPath}
		resp, err := workspace.Control(ctx, wopts, "gc")
		if err != nil && resp.Error == "" {
			fmt.Fprintf(os.Stderr, "gc %s: %v\n", entry.Name, err)
			continue
		}
		if resp.Event != nil && resp.Event.State == vmkit.StateStopped {
			reaped = append(reaped, gcReap{Name: entry.Name, Was: entry.State})
		}
	}
	return writeJSON(stdout, struct {
		Checked int      `json:"checked"`
		Reaped  []gcReap `json:"reaped"`
	}{Checked: checked, Reaped: reaped})
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
	result, err := workspace.Clone(opts.StateDir, source, target)
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
