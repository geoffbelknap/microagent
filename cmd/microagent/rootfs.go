package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/geoffbelknap/microagent/pkg/rootfs"
	"github.com/geoffbelknap/microagent/pkg/workspace"
)

func runRootFS(ctx context.Context, args []string, stdout *os.File) error {
	if len(args) == 0 || args[0] == "help" || args[0] == "--help" || args[0] == "-h" {
		printRootFSHelp(stdout)
		return nil
	}
	if args[0] != "build" {
		return fmt.Errorf("unknown rootfs command: %s", args[0])
	}
	var req rootfs.BuildRequest
	fs := newCommandFlagSet("rootfs build")
	fs.StringVar(&req.ImageRef, "image", "", "OCI image reference")
	fs.StringVar(&req.Platform.OS, "os", "linux", "target operating system")
	fs.StringVar(&req.Platform.Architecture, "arch", defaultGuestArch(), "target architecture; defaults to the host architecture")
	fs.StringVar(&req.OutputPath, "out", "", "output rootfs path")
	fs.StringVar(&req.InitPath, "init", rootfs.DefaultInitPath, "guest init path to inject")
	fs.StringVar(&req.StateDir, "state-dir", "", "builder state directory")
	fs.StringVar(&req.Mke2fsPath, "mke2fs", "mke2fs", "mke2fs binary path")
	fs.Int64Var(&req.SizeMiB, "size-mib", rootfs.DefaultSizeMiB, "rootfs image size in MiB; without the flag the disk grows to fit the image")
	fs.BoolVar(&req.KeepStage, "keep-stage", false, "keep temporary unpacked stage directory")
	fs.StringVar(&req.StageSnapshot, "stage-snapshot", "", "copy unpacked stage directory to this path before ext4 creation")
	fs.BoolVar(&req.AllowMutable, "allow-mutable", false, "allow mutable image references")
	var execCommand string
	fs.StringVar(&execCommand, "exec", "", "shell command to run as guest init")
	if err := parseCommandFlags(fs, stdout, args[1:]); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("unexpected rootfs argument: %s", fs.Arg(0))
	}
	req.AutoSize = !hasFlagValue(args[1:], "size-mib")
	req.Platform.Architecture = workspace.NormalizeArch(req.Platform.Architecture)
	if strings.TrimSpace(execCommand) != "" {
		req.Command = []string{"/bin/sh", "-lc", execCommand}
	}
	req.Progress = rootfsProgress(stdout, "rootfs")
	provenance, err := rootfs.NewBuilder().Build(ctx, req)
	// Emit the provenance envelope whenever the build produced one, except under
	if provenance.ImageRef != "" {
		if encodeErr := writeJSON(stdout, provenance); encodeErr != nil {
			return encodeErr
		}
	}
	return err
}

func printRootFSHelp(stdout *os.File) {
	fmt.Fprintln(stdout, "microagent rootfs")
	printUsageBlock(stdout, "rootfs", "rootfs")
	fmt.Fprint(stdout, `
Commands:
  build                Build a rootfs from an OCI image

Build options:
  -image <ref>         OCI image
  -out <path>          Output rootfs path
  -os <os>             Target OS
  -arch <arch>         Target architecture; defaults to the host architecture
  -size-mib <MiB>      Disk size
  -mke2fs <path>       mke2fs binary path
  -exec <command>      Shell command to run as guest init
  -allow-mutable       Allow tag references
`)
}
