package main

import (
	"context"
	"fmt"
	"os"

	"github.com/geoffbelknap/microagent/pkg/workspace"
)

func runResize(ctx context.Context, args []string, stdout *os.File) error {
	if wantsHelp(args) {
		printResizeHelp(stdout)
		return nil
	}
	stateDir := defaultStateDir()
	backend := hostBackend()
	resize2fsPath := defaultResize2fsPath()
	fs := newCommandFlagSet("resize")
	fs.StringVar(&stateDir, "state-dir", stateDir, "State directory")
	fs.StringVar(&backend, "backend", backend, "Backend identity override")
	fs.StringVar(&resize2fsPath, "resize2fs", resize2fsPath, "resize2fs binary path")
	sizeMiB := fs.Int64("size-mib", 0, "Target rootfs size in MiB")
	if err := parseCommandFlags(fs, stdout, reorderFlagArgs(args)); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("usage: microagent resize <workspace> --size-mib <n> [--resize2fs <path>] [--backend <name>] [--state-dir <dir>]")
	}
	if err := validateWorkspaceName(fs.Arg(0)); err != nil {
		return err
	}
	result, err := workspace.Resize(workspace.ResizeOptions{
		StateDir:      stateDir,
		Name:          fs.Arg(0),
		Backend:       backend,
		SizeMiB:       *sizeMiB,
		Resize2fsPath: resize2fsPath,
	})
	if err != nil {
		return err
	}
	if outputJSON(stdout) {
		return writeJSON(stdout, result)
	}
	fmt.Fprintf(stdout, "Resized %s: %d MiB -> %d MiB\n", result.Workspace, result.FromSizeMiB, result.ToSizeMiB)
	if result.Usage != nil {
		fmt.Fprintf(stdout, "  disk: used=%dMiB (%d%%) host=%dMiB\n", result.Usage.FSUsedMiB, result.Usage.UsedPercent, result.Usage.HostAllocatedMiB)
	}
	return nil
}

func printResizeHelp(stdout *os.File) {
	fmt.Fprint(stdout, `microagent resize

Grow or shrink a stopped workspace's rootfs disk in place. The workspace must
be halted or stopped and must have no snapshots.

Usage:
  microagent resize <workspace> --size-mib <n> [options]

Options:
  --size-mib <n>        Target rootfs size in MiB
  --resize2fs <path>    resize2fs binary path
  --backend <name>      Backend identity override
  --state-dir <dir>     State directory
`)
}
