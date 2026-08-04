package rootfs

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// POSIX inode type bits, as understood by debugfs's "sif ... mode" command.
const (
	posixTypeRegular = 0o100000
	posixTypeDir     = 0o040000
	posixTypeSymlink = 0o120000
)

// applyStageOwnership corrects uid/gid and mode bits (including setuid,
// setgid, and sticky) on an already-built, unmounted ext4 image so they match
// what the source OCI image declared, regardless of which host user ran the
// build.
//
// mke2fs -d populates a new filesystem by copying the host stat() result of
// every file in the source directory tree; it never calls chown, so an
// unprivileged build can only ever encode the host user's own uid/gid, not
// the image's. debugfs -w edits raw inode fields directly on the unmounted
// image file, bypassing the host VFS/chown permission model entirely, so it
// can encode any uid/gid, setuid, setgid, or sticky bit with no host
// privilege at all.
func applyStageOwnership(ctx context.Context, debugfsPath, stageDir, imagePath string) error {
	entries, err := readStageEntries(stageDir)
	if err != nil {
		return fmt.Errorf("read stage ownership metadata: %w", err)
	}

	var script strings.Builder
	walkErr := filepath.WalkDir(stageDir, func(hostPath string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if hostPath == stageDir {
			// mke2fs already sets the root inode to 0:0; only children carry
			// the host build user's ownership.
			return nil
		}
		rel, err := filepath.Rel(stageDir, hostPath)
		if err != nil {
			return err
		}
		name := filepath.ToSlash(rel)
		if name == stageMetadataName {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		var typeBits int64
		switch {
		case entry.Type()&os.ModeSymlink != 0:
			typeBits = posixTypeSymlink
		case entry.IsDir():
			typeBits = posixTypeDir
		case info.Mode().IsRegular():
			typeBits = posixTypeRegular
		default:
			// Sockets, FIFOs, and device nodes are not produced by the stage
			// writer today; skip anything unexpected rather than guess.
			return nil
		}
		uid, gid, permBits := 0, 0, int64(info.Mode().Perm())
		if record, ok := entries[name]; ok {
			uid, gid, permBits = record.Uid, record.Gid, record.Mode
		}
		guestPath := "/" + name
		arg, err := quoteDebugfsPath(guestPath)
		if err != nil {
			return fmt.Errorf("cannot preserve ownership for %s: %w", guestPath, err)
		}
		fmt.Fprintf(&script, "sif %s mode 0%o\n", arg, typeBits|permBits)
		fmt.Fprintf(&script, "sif %s uid %d\n", arg, uid)
		fmt.Fprintf(&script, "sif %s gid %d\n", arg, gid)
		return nil
	})
	if walkErr != nil {
		return walkErr
	}
	if script.Len() == 0 {
		return nil
	}

	cmd := exec.CommandContext(ctx, debugfsPath, "-w", "-f", "-", imagePath)
	cmd.Stdin = strings.NewReader(script.String())
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("debugfs: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// quoteDebugfsPath quotes a guest path for use as a debugfs command argument.
// debugfs's command tokenizer has no escape mechanism inside double quotes,
// so a path containing an embedded quote or control character cannot be
// expressed at all; that is a fail-closed error rather than an attempt to
// approximate it, per pkg/commit's quoteDebugFSArg precedent for the same
// tool.
func quoteDebugfsPath(guestPath string) (string, error) {
	for _, r := range guestPath {
		if r == '"' || r < 0x20 || r == 0x7f {
			return "", fmt.Errorf("path %q cannot be expressed as a debugfs argument", guestPath)
		}
	}
	return `"` + guestPath + `"`, nil
}
