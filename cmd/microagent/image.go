package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"github.com/geoffbelknap/microagent/pkg/commit"
	"github.com/geoffbelknap/microagent/pkg/imagecache"
	"github.com/geoffbelknap/microagent/pkg/rootfs"
)

func runImage(args []string, stdout *os.File) error {
	opts := stateCommandOptions{StateDir: defaultStateDir()}
	guestInitExplicit := hasFlagValue(args, "guest-init")
	fs := flag.NewFlagSet("image", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.StringVar(&opts.StateDir, "state-dir", opts.StateDir, "State directory")
	arch := fs.String("arch", defaultGuestArch(), "Image architecture")
	sizeMiB := fs.Int64("size-mib", rootfs.DefaultSizeMiB, "Rootfs image size in MiB")
	mke2fsPath := fs.String("mke2fs", defaultMke2fsPath(), "mke2fs binary path")
	guestInitPath := fs.String("guest-init", defaultGuestInitPath(*arch), "Guest init path")
	deleteFiles := fs.Bool("delete", false, "Delete reusable local image rootfs files during prune")
	yes := fs.Bool("yes", false, "Confirm destructive image cache cleanup without prompting")
	fs.BoolVar(yes, "y", false, "Confirm destructive image cache cleanup without prompting")
	if err := fs.Parse(reorderFlagArgs(args)); err != nil {
		return err
	}
	if !guestInitExplicit {
		*guestInitPath = defaultGuestInitPath(*arch)
	}
	if fs.NArg() == 0 || fs.Arg(0) == "list" {
		if fs.NArg() > 1 {
			return fmt.Errorf("usage: microagent image list [--state-dir <dir>]")
		}
		images, err := imagecache.List(opts.StateDir)
		if err != nil {
			return err
		}
		return writeImageList(stdout, images)
	}
	switch fs.Arg(0) {
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
			return fmt.Errorf("usage: microagent image delete <image> [--delete] [--state-dir <dir>]")
		}
		if *deleteFiles {
			if err := confirmImageCacheDelete(*yes); err != nil {
				return err
			}
		}
		result, err := imagecache.Remove(opts.StateDir, fs.Arg(1), *deleteFiles)
		if err != nil {
			return err
		}
		return writeImagePruneResult(stdout, result)
	case "prune":
		if fs.NArg() != 1 {
			return fmt.Errorf("usage: microagent image prune [--state-dir <dir>]")
		}
		if *deleteFiles {
			if err := confirmImageCacheDelete(*yes); err != nil {
				return err
			}
		}
		result, err := imagecache.Prune(opts.StateDir, *deleteFiles)
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
