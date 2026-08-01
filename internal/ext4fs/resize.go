package ext4fs

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// ShrinkMargin is added to an ext4 image's reported used bytes when checking
// whether a shrink target leaves it enough room. resize2fs computes the true
// minimum size itself and is the real authority on feasibility; this margin
// only covers the gap between our superblock read and resize2fs's own read
// (a concurrently written filesystem can drift in that window), so it is
// deliberately small rather than a capacity reservation.
const ShrinkMargin = 8 * 1024 * 1024

// FitsShrink reports whether targetBytes is large enough to hold path's
// current content plus ShrinkMargin, read from the superblock without
// mounting. It fails closed: a target the filesystem's own accounting says
// will not fit is refused with a clear reason before resize2fs ever runs.
func FitsShrink(path string, targetBytes int64) error {
	usage, err := ReadUsage(path)
	if err != nil {
		return err
	}
	needed := usage.UsedBytes + ShrinkMargin
	if targetBytes < needed {
		return fmt.Errorf("%s uses %d bytes; a %d byte target does not leave the required %d byte margin (needs at least %d bytes)", path, usage.UsedBytes, targetBytes, ShrinkMargin, needed)
	}
	return nil
}

// Resize changes the ext4 image at path to targetBytes, growing or shrinking
// as needed. It is a no-op when the backing file is already targetBytes.
func Resize(e2fsckPath, resize2fsPath, path string, targetBytes int64) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	switch {
	case targetBytes == info.Size():
		return nil
	case targetBytes > info.Size():
		return Grow(e2fsckPath, resize2fsPath, path, targetBytes)
	default:
		return Shrink(e2fsckPath, resize2fsPath, path, targetBytes)
	}
}

// Grow extends the ext4 image at path to targetBytes: e2fsck -f runs first
// (resize2fs refuses without it, the same precondition as a shrink — a
// filesystem past its mount-count/time check interval fails a grow too, not
// just a shrink), then the backing file is truncated up (preserving existing
// content), then resize2fs fills the new space.
func Grow(e2fsckPath, resize2fsPath, path string, targetBytes int64) error {
	if err := ReconcileJournal(e2fsckPath, path); err != nil {
		return fmt.Errorf("reconcile %s before grow: %w", path, err)
	}
	if err := truncateTo(path, targetBytes); err != nil {
		return fmt.Errorf("grow %s: %w", path, err)
	}
	return runResize2fs(resize2fsPath, path, targetBytes)
}

// Shrink reduces the ext4 image at path to targetBytes. FitsShrink runs
// first (a clearer error than resize2fs's own), then e2fsck -f, then
// resize2fs shrinks the filesystem before the backing file is truncated
// down. That order matters: truncating before the filesystem is shrunk
// would cut into live metadata.
func Shrink(e2fsckPath, resize2fsPath, path string, targetBytes int64) error {
	if err := FitsShrink(path, targetBytes); err != nil {
		return err
	}
	if err := ReconcileJournal(e2fsckPath, path); err != nil {
		return fmt.Errorf("reconcile %s before shrink: %w", path, err)
	}
	if err := runResize2fs(resize2fsPath, path, targetBytes); err != nil {
		return err
	}
	if err := truncateTo(path, targetBytes); err != nil {
		return fmt.Errorf("shrink %s: %w", path, err)
	}
	return nil
}

func truncateTo(path string, targetBytes int64) error {
	f, err := os.OpenFile(path, os.O_WRONLY, 0)
	if err != nil {
		return err
	}
	truncErr := f.Truncate(targetBytes)
	closeErr := f.Close()
	if truncErr != nil {
		return truncErr
	}
	return closeErr
}

func runResize2fs(resize2fsPath, path string, targetBytes int64) error {
	if strings.TrimSpace(resize2fsPath) == "" {
		resize2fsPath = "resize2fs"
	}
	size := fmt.Sprintf("%dK", targetBytes/1024)
	cmd := exec.Command(resize2fsPath, path, size)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("resize2fs %s %s: %w: %s", path, size, err, strings.TrimSpace(string(output)))
	}
	return nil
}
