package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/geoffbelknap/microagent/pkg/commit"
	"github.com/geoffbelknap/microagent/pkg/imagecache"
)

func runImage(args []string, stdout *os.File) error {
	opts := stateCommandOptions{StateDir: defaultStateDir()}
	guestInitExplicit := hasFlagValue(args, "guest-init")
	fs := newCommandFlagSet("image")
	fs.StringVar(&opts.StateDir, "state-dir", opts.StateDir, "State directory")
	arch := fs.String("arch", defaultGuestArch(), "Image architecture")
	sizeMiB := fs.Int64("size-mib", 0, "Rootfs image size in MiB (default: fit the image)")
	mke2fsPath := fs.String("mke2fs", defaultMke2fsPath(), "mke2fs binary path")
	debugfsPath := fs.String("debugfs", defaultDebugFSPath(), "debugfs binary path")
	guestInitPath := fs.String("guest-init", defaultGuestInitPath(*arch), "Guest init path")
	purgeFiles := fs.Bool("purge", false, "Also remove the reusable rootfs baseline files")
	yes := fs.Bool("yes", false, "Confirm destructive image cache cleanup without prompting")
	fs.BoolVar(yes, "y", false, "Confirm destructive image cache cleanup without prompting")
	if wantsHelp(args) {
		printImageHelp(stdout, fs, args)
		return nil
	}
	if err := parseCommandFlags(fs, stdout, reorderFlagArgs(args)); err != nil {
		return err
	}
	if !guestInitExplicit {
		*guestInitPath = defaultGuestInitPath(*arch)
	}
	// No subcommand is a help request, like every other group. This used to
	// fall through to `list`, so a bare `microagent image` emitted a JSON
	// document — indistinguishable from a deliberate listing, and the only
	// group whose omission dispatched work instead of explaining the group.
	if fs.NArg() == 0 {
		printImageHelp(stdout, fs, nil)
		return nil
	}
	if canonicalSubverb(fs.Arg(0)) == "list" {
		if fs.NArg() > 1 {
			return fmt.Errorf("usage: microagent image list [--state-dir <dir>]")
		}
		images, err := imagecache.List(opts.StateDir)
		if err != nil {
			return err
		}
		return writeImageList(stdout, images)
	}
	switch canonicalSubverb(fs.Arg(0)) {
	case "pull":
		if fs.NArg() != 2 {
			return fmt.Errorf("usage: microagent image pull <image> [--state-dir <dir>]")
		}
		record, err := imagecache.Pull(context.Background(), imagecache.PullOptions{
			StateDir:      opts.StateDir,
			ImageRef:      fs.Arg(1),
			Architecture:  *arch,
			SizeMiB:       *sizeMiB,
			Mke2fsPath:    *mke2fsPath,
			DebugfsPath:   *debugfsPath,
			GuestInitPath: *guestInitPath,
		})
		if err != nil {
			return err
		}
		return writeImageRecord(stdout, record)
	case "push":
		if fs.NArg() != 2 {
			return fmt.Errorf("usage: microagent image push <image> [--state-dir <dir>]")
		}
		if err := commit.Push(context.Background(), opts.StateDir, fs.Arg(1)); err != nil {
			return err
		}
		if outputJSON(stdout) {
			return writeJSON(stdout, map[string]any{"pushed": fs.Arg(1)})
		}
		fmt.Fprintf(stdout, "Pushed %s\n", fs.Arg(1))
		return nil
	case "tag":
		if fs.NArg() != 3 {
			return fmt.Errorf("usage: microagent image tag <source> <target> [--state-dir <dir>]")
		}
		record, err := imagecache.Tag(opts.StateDir, fs.Arg(1), fs.Arg(2))
		if err != nil {
			return err
		}
		return writeImageRecord(stdout, record)
	case "delete":
		if fs.NArg() != 2 {
			return fmt.Errorf("usage: microagent image delete <image> [--purge] [--state-dir <dir>]")
		}
		if *purgeFiles {
			if err := confirmImageCacheDelete(*yes); err != nil {
				return err
			}
		}
		result, err := imagecache.Remove(opts.StateDir, fs.Arg(1), *purgeFiles)
		if err != nil {
			return err
		}
		return writeImagePruneResult(stdout, result)
	case "prune":
		if fs.NArg() != 1 {
			return fmt.Errorf("usage: microagent image prune [--purge] [--state-dir <dir>]")
		}
		if *purgeFiles {
			if err := confirmImageCacheDelete(*yes); err != nil {
				return err
			}
		}
		result, err := imagecache.Prune(opts.StateDir, *purgeFiles)
		if err != nil {
			return err
		}
		return writeImagePruneResult(stdout, result)
	default:
		return fmt.Errorf("unknown image command: %s", fs.Arg(0))
	}
}

func confirmImageCacheDelete(yes bool) error {
	if yes {
		return nil
	}
	ok, err := confirmAction("Delete reusable image cache rootfs files under the local image store? Workspace disks will not be deleted.")
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("prune cancelled")
	}
	return nil
}

// printImageHelp answers `image --help` and `image <subcommand> --help`.
//
// Every image subcommand used to take --help as an image reference. delete and
// push ran a lookup with it, pull put it through the OCI ref parser, and the
// two that did print a usage line exited 1 — so a script could not tell asking
// for help from failing. Every sibling group (volume, snapshot, model, secret,
// registry, kernel) already resolved help before touching its arguments.
func printImageHelp(stdout *os.File, fs *flag.FlagSet, args []string) {
	name := "image"
	for _, a := range args {
		if a == "help" || strings.HasPrefix(a, "-") {
			continue
		}
		name = "image " + canonicalSubverb(a)
		break
	}
	printGroupHelpHeader(stdout, "image")
	printUsageBlock(stdout, name, "image")
	fmt.Fprint(stdout, "\nOptions:\n")
	for _, opt := range collapsedFlags(fs) {
		fmt.Fprintf(stdout, "  %-20s %s\n", opt.label, opt.usage)
	}
}
