// Package ext4fs provides shared, host-side ext4 maintenance helpers.
package ext4fs

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"syscall"
)

// ReconcileJournal runs e2fsck before a stopped ext filesystem is read or
// modified without mounting it. Besides replaying the journal, the full check
// normalizes directory metadata before another host tool consumes it.
func ReconcileJournal(e2fsckPath, imagePath string) error {
	ext, err := hasSuperblock(imagePath)
	if err != nil {
		return err
	}
	if !ext {
		return nil
	}
	cmd := exec.Command(e2fsckPath, "-fy", imagePath)
	output, err := cmd.CombinedOutput()
	text := strings.TrimSpace(string(output))
	if err == nil {
		return nil
	}
	exitErr, ok := err.(*exec.ExitError)
	if !ok {
		return fmt.Errorf("e2fsck %s: %w: %s", imagePath, err, text)
	}
	status, ok := exitErr.Sys().(syscall.WaitStatus)
	if !ok {
		return fmt.Errorf("e2fsck %s: %w: %s", imagePath, err, text)
	}
	// e2fsck uses bit flags. Bits 1 and 2 mean the filesystem was corrected;
	// higher bits indicate uncorrected errors or operational failures.
	if status.ExitStatus()&^3 == 0 {
		return nil
	}
	return fmt.Errorf("e2fsck %s: %w: %s", imagePath, err, text)
}

func hasSuperblock(imagePath string) (bool, error) {
	file, err := os.Open(imagePath)
	if err != nil {
		return false, err
	}
	defer func() { _ = file.Close() }()
	magic := []byte{0, 0}
	n, err := file.ReadAt(magic, 1080)
	if err != nil {
		if err == io.EOF || err == io.ErrUnexpectedEOF {
			return false, nil
		}
		return false, err
	}
	if n != len(magic) {
		return false, nil
	}
	return magic[0] == 0x53 && magic[1] == 0xef, nil
}
