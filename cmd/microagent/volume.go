package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"github.com/geoffbelknap/microagent/pkg/vmkit"
	"github.com/geoffbelknap/microagent/pkg/volume"
	"github.com/geoffbelknap/microagent/pkg/workspace"
)

func runVolume(ctx context.Context, args []string, stdout *os.File) error {
	if wantsHelp(args) || len(args) == 0 {
		printVolumeHelp(stdout)
		return nil
	}
	switch args[0] {
	case "create":
		return runVolumeCreate(ctx, args[1:], stdout)
	case "list":
		return runVolumeList(args[1:], stdout)
	case "delete":
		return runVolumeRemove(args[1:], stdout)
	case "status", "inspect":
		return runVolumeInspect(args[1:], stdout)
	}
	return fmt.Errorf("unknown volume command %q; see microagent volume --help", args[0])
}

func runVolumeCreate(ctx context.Context, args []string, stdout *os.File) error {
	stateDir := defaultStateDir()
	var sizeMiB int64
	fs := flag.NewFlagSet("volume create", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.Int64Var(&sizeMiB, "size-mib", 0, "Volume size in MiB (default 1024)")
	fs.StringVar(&stateDir, "state-dir", stateDir, "State directory")
	if err := fs.Parse(reorderFlagArgs(args)); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("usage: microagent volume create <name> [--size-mib <n>] [--state-dir <dir>]")
	}
	record, err := volume.Create(ctx, stateDir, hostBackend(), fs.Arg(0), sizeMiB, defaultMke2fsPath())
	if err != nil {
		return err
	}
	if outputJSON(stdout) {
		return writeJSON(stdout, record)
	}
	fmt.Fprintf(stdout, "Created volume %q (%d MiB)\n", record.Name, record.SizeMiB)
	return nil
}

func runVolumeList(args []string, stdout *os.File) error {
	stateDir := defaultStateDir()
	fs := flag.NewFlagSet("volume list", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.StringVar(&stateDir, "state-dir", stateDir, "State directory")
	if err := fs.Parse(reorderFlagArgs(args)); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("usage: microagent volume list [--state-dir <dir>]")
	}
	records, err := volume.List(stateDir)
	if err != nil {
		return err
	}
	if outputJSON(stdout) {
		return writeJSON(stdout, map[string]any{"volumes": records})
	}
	fmt.Fprintf(stdout, "%-20s %-10s %s\n", "NAME", "SIZE-MIB", "ATTACHED")
	for _, r := range records {
		attached := r.AttachedTo
		if attached == "" {
			attached = "-"
		}
		fmt.Fprintf(stdout, "%-20s %-10d %s\n", r.Name, r.SizeMiB, attached)
	}
	return nil
}

func runVolumeRemove(args []string, stdout *os.File) error {
	stateDir := defaultStateDir()
	force := false
	fs := flag.NewFlagSet("volume delete", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.BoolVar(&force, "force", false, "Remove even if the volume is attached")
	fs.BoolVar(&force, "f", false, "Remove even if the volume is attached")
	fs.StringVar(&stateDir, "state-dir", stateDir, "State directory")
	if err := fs.Parse(reorderFlagArgs(args)); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("usage: microagent volume delete <name> [--force] [--state-dir <dir>]")
	}
	name := fs.Arg(0)
	if err := volume.Remove(stateDir, name, force, workspaceRunningPredicate(stateDir)); err != nil {
		return err
	}
	if outputJSON(stdout) {
		return writeJSON(stdout, map[string]any{"removed": name})
	}
	fmt.Fprintf(stdout, "Removed volume %q\n", name)
	return nil
}

func runVolumeInspect(args []string, stdout *os.File) error {
	stateDir := defaultStateDir()
	fs := flag.NewFlagSet("volume inspect", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.StringVar(&stateDir, "state-dir", stateDir, "State directory")
	if err := fs.Parse(reorderFlagArgs(args)); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("usage: microagent volume inspect <name> [--state-dir <dir>]")
	}
	record, err := volume.Get(stateDir, fs.Arg(0))
	if err != nil {
		return err
	}
	if outputJSON(stdout) {
		return writeJSON(stdout, record)
	}
	attached := record.AttachedTo
	if attached == "" {
		attached = "-"
	}
	fmt.Fprintf(stdout, "Name:     %s\n", record.Name)
	fmt.Fprintf(stdout, "Size:     %d MiB\n", record.SizeMiB)
	fmt.Fprintf(stdout, "Created:  %s\n", record.CreatedAt)
	fmt.Fprintf(stdout, "Attached: %s\n", attached)
	fmt.Fprintf(stdout, "Path:     %s\n", volume.DiskPath(stateDir, hostBackend(), record.Name))
	return nil
}

// workspaceRunningPredicate reports whether a workspace is in a state that
// still holds its volumes (it could be using the disk). A workspace with no
// event, or one that is stopped/halted/failed, is reclaimable.
func workspaceRunningPredicate(stateDir string) func(string) bool {
	return func(name string) bool {
		event, err := workspace.ReadEvent(workspace.Options{StateDir: stateDir, Name: name})
		if err != nil {
			return false
		}
		switch event.State {
		case vmkit.StateStarting, vmkit.StateRunning, vmkit.StatePaused, vmkit.StateQuarantined:
			return true
		default:
			return false
		}
	}
}

func printVolumeHelp(stdout *os.File) {
	fmt.Fprint(stdout, `microagent volume

Manage user-defined named volumes: VM-independent ext4 disks attached by name.

Usage:
  microagent volume create <name> [options]  Create a named volume
  microagent volume list                      List named volumes
  microagent volume status <name>             Show one volume (alias: inspect)
  microagent volume delete <name> [options]   Remove a named volume

Attach a volume to a workspace by name with --volume <name>:/mount, e.g.
  microagent run IMAGE --volume data:/work

A volume is single-attach: at most one running workspace holds it at a time.

Options:
  --size-mib <n>        Volume size in MiB for create (default 1024)
  --force               Remove a volume even if it is attached
  --state-dir <dir>     State directory
`)
}
