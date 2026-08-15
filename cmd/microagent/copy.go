package main

import (
	"context"
	"fmt"
	"os"

	"github.com/geoffbelknap/microagent/pkg/workspace"
)

func runCP(ctx context.Context, args []string, stdout *os.File) error {
	opts := stateCommandOptions{StateDir: defaultStateDir()}
	debugfsPath := defaultDebugFSPath()
	fs := newCommandFlagSet("cp")
	fs.StringVar(&opts.StateDir, "state-dir", opts.StateDir, "State directory")
	fs.StringVar(&debugfsPath, "debugfs", debugfsPath, "debugfs binary path")
	if err := parseCommandFlags(fs, stdout, reorderFlagArgs(args)); err != nil {
		return err
	}
	if fs.NArg() != 2 {
		return fmt.Errorf("usage: microagent cp <source> <target> [--state-dir <dir>]")
	}
	progress, finishProgress := commandProgressFor(stdout, "workspace-copy", "Copy file")
	result, err := workspace.CopyWithOptions(ctx, workspace.CopyOptions{
		StateDir: opts.StateDir, DebugFSPath: debugfsPath, Source: fs.Arg(0), Target: fs.Arg(1), Progress: progress,
	})
	finishProgress(err)
	if err != nil {
		return err
	}
	return writeCopyResult(stdout, result)
}

func runArtifact(ctx context.Context, args []string, stdout *os.File) error {
	if len(args) > 0 && args[0] == "get" {
		return runArtifactGet(ctx, args[1:], stdout)
	}
	opts := stateCommandOptions{StateDir: defaultStateDir()}
	fs := newCommandFlagSet("artifact")
	fs.StringVar(&opts.StateDir, "state-dir", opts.StateDir, "State directory")
	if err := parseCommandFlags(fs, stdout, reorderFlagArgs(args)); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("usage: microagent artifact <name> [--state-dir <dir>]")
	}
	name := fs.Arg(0)
	if err := validateWorkspaceName(name); err != nil {
		return err
	}
	artifacts, err := workspace.ArtifactsFor(opts.StateDir, name)
	if err != nil {
		return err
	}
	result := artifactsResult{Workspace: name, Artifacts: artifacts}
	return writeArtifactsResult(stdout, result)
}

func runArtifactGet(ctx context.Context, args []string, stdout *os.File) error {
	opts := stateCommandOptions{StateDir: defaultStateDir()}
	debugfsPath := defaultDebugFSPath()
	fs := newCommandFlagSet("artifact get")
	fs.StringVar(&opts.StateDir, "state-dir", opts.StateDir, "State directory")
	fs.StringVar(&debugfsPath, "debugfs", debugfsPath, "debugfs binary path")
	if err := parseCommandFlags(fs, stdout, reorderFlagArgs(args)); err != nil {
		return err
	}
	if fs.NArg() != 3 {
		return fmt.Errorf("usage: microagent artifact get <name> <artifact> <target> [--state-dir <dir>]")
	}
	name := fs.Arg(0)
	if err := validateWorkspaceName(name); err != nil {
		return err
	}
	progress, finishProgress := commandProgressFor(stdout, "artifact-get", "Get artifact")
	result, err := workspace.GetArtifactWithOptions(ctx, workspace.ArtifactGetOptions{
		StateDir: opts.StateDir, DebugFSPath: debugfsPath, Workspace: name,
		Artifact: fs.Arg(1), Target: fs.Arg(2), Progress: progress,
	})
	finishProgress(err)
	if err != nil {
		return err
	}
	return writeCopyResult(stdout, result)
}
