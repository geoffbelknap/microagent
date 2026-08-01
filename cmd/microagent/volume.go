package main

import (
	"context"
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
	switch canonicalSubverb(args[0]) {
	case "create":
		return runVolumeCreate(ctx, args[1:], stdout)
	case "list":
		return runVolumeList(args[1:], stdout)
	case "delete":
		return runVolumeRemove(args[1:], stdout)
	case "status":
		return runVolumeInspect(args[1:], stdout)
	case "resize":
		return runVolumeResize(args[1:], stdout)
	}
	return fmt.Errorf("unknown volume command %q; see microagent volume --help", args[0])
}

func runVolumeCreate(ctx context.Context, args []string, stdout *os.File) error {
	stateDir := defaultStateDir()
	var sizeMiB int64
	fs := newCommandFlagSet("volume create")
	fs.Int64Var(&sizeMiB, "size-mib", 0, "Volume size in MiB (default 1024)")
	fs.StringVar(&stateDir, "state-dir", stateDir, "State directory")
	if err := parseCommandFlags(fs, stdout, reorderFlagArgs(args)); err != nil {
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
	fs := newCommandFlagSet("volume list")
	fs.StringVar(&stateDir, "state-dir", stateDir, "State directory")
	if err := parseCommandFlags(fs, stdout, reorderFlagArgs(args)); err != nil {
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
	return writeVolumeList(stdout, records)
}

func writeVolumeList(stdout *os.File, records []volume.Record) error {
	cols := []tableColumn{
		{Header: "NAME", Legacy: 20, Min: 10, Max: 28, Flex: true},
		{Header: "SIZE-MIB", Legacy: 10, Min: 8, Max: 10},
		{Header: "ATTACHED", Legacy: 0, Min: 8},
	}
	rows := make([][]tableCell, len(records))
	for i, r := range records {
		attached := r.AttachedTo
		if attached == "" {
			attached = "-"
		}
		rows[i] = []tableCell{
			cell(r.Name),
			cell(fmt.Sprintf("%d", r.SizeMiB)),
			cell(attached),
		}
	}
	renderTable(stdout, cols, rows)
	return nil
}

func runVolumeRemove(args []string, stdout *os.File) error {
	stateDir := defaultStateDir()
	force := false
	fs := newCommandFlagSet("volume delete")
	fs.BoolVar(&force, "force", false, "Remove even if the volume is attached")
	fs.BoolVar(&force, "f", false, "Remove even if the volume is attached")
	fs.StringVar(&stateDir, "state-dir", stateDir, "State directory")
	if err := parseCommandFlags(fs, stdout, reorderFlagArgs(args)); err != nil {
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
	fs := newCommandFlagSet("volume inspect")
	fs.StringVar(&stateDir, "state-dir", stateDir, "State directory")
	if err := parseCommandFlags(fs, stdout, reorderFlagArgs(args)); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("usage: microagent volume inspect <name> [--state-dir <dir>]")
	}
	record, err := volume.Get(stateDir, fs.Arg(0))
	if err != nil {
		return err
	}
	usage := workspace.VolumeDiskUsage(stateDir, hostBackend(), record.Name)
	if outputJSON(stdout) {
		if usage != nil {
			return writeJSON(stdout, struct {
				volume.Record
				Usage *vmkit.DiskUsage `json:"usage"`
			}{Record: record, Usage: usage})
		}
		return writeJSON(stdout, record)
	}
	attached := record.AttachedTo
	if attached == "" {
		attached = "-"
	}
	fmt.Fprintf(stdout, "Name:     %s\n", record.Name)
	fmt.Fprintf(stdout, "Size:     %d MiB\n", record.SizeMiB)
	if usage != nil {
		fmt.Fprintf(stdout, "Used:     %d MiB (%d%%), host allocation %d MiB\n", usage.FSUsedMiB, usage.UsedPercent, usage.HostAllocatedMiB)
	}
	fmt.Fprintf(stdout, "Created:  %s\n", record.CreatedAt)
	fmt.Fprintf(stdout, "Attached: %s\n", attached)
	fmt.Fprintf(stdout, "Path:     %s\n", volume.DiskPath(stateDir, hostBackend(), record.Name))
	return nil
}

func runVolumeResize(args []string, stdout *os.File) error {
	stateDir := defaultStateDir()
	e2fsckPath := defaultE2fsckPath()
	resize2fsPath := defaultResize2fsPath()
	fs := newCommandFlagSet("volume resize")
	sizeMiB := fs.Int64("size-mib", 0, "Target volume size in MiB")
	fs.StringVar(&stateDir, "state-dir", stateDir, "State directory")
	if err := parseCommandFlags(fs, stdout, reorderFlagArgs(args)); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("usage: microagent volume resize <name> --size-mib <n> [--state-dir <dir>]")
	}
	record, err := volume.Resize(stateDir, fs.Arg(0), *sizeMiB, e2fsckPath, resize2fsPath, workspaceRunningPredicate(stateDir))
	if err != nil {
		return err
	}
	if outputJSON(stdout) {
		return writeJSON(stdout, record)
	}
	fmt.Fprintf(stdout, "Resized volume %q to %d MiB\n", record.Name, record.SizeMiB)
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
	printGroupHelpHeader(stdout, "volume")
	printUsageBlock(stdout, "volume", "volume")
	fmt.Fprint(stdout, `
Manage user-defined named volumes: VM-independent ext4 disks attached by name.

Attach a volume to a workspace by name with --volume <name>:/mount, e.g.
  microagent run IMAGE --volume data:/work

A volume is single-attach: at most one running workspace holds it at a time.

Options:
  --size-mib <n>        Volume size in MiB for create/resize (default 1024)
  --force               Remove a volume even if it is attached
  --state-dir <dir>     State directory
`)
}
