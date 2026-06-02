package secretxfer

import (
	"fmt"
	"os"
	"path/filepath"
)

// zeroFile overwrites a regular file's bytes with zeros of its original length
// and fsyncs, scrubbing the backing tmpfs pages so a memory snapshot captures
// zeros rather than the secret. The file is kept (same size, all zero). Secret
// files are written 0400, so the mode is widened to 0600 for the rewrite.
func zeroFile(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Size() == 0 {
		return nil
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return fmt.Errorf("chmod for scrub: %w", err)
	}
	f, err := os.OpenFile(path, os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := f.Write(make([]byte, info.Size())); err != nil {
		return fmt.Errorf("overwrite with zeros: %w", err)
	}
	return f.Sync()
}

// PurgeSecrets scrubs every regular file under root (overwrite-with-zeros) and
// then removes it, leaving root empty. A missing root is a no-op (no secrets
// were materialized).
func PurgeSecrets(root string) error {
	entries, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		path := filepath.Join(root, entry.Name())
		if err := zeroFile(path); err != nil {
			return fmt.Errorf("scrub %q: %w", entry.Name(), err)
		}
		if err := os.Remove(path); err != nil {
			return fmt.Errorf("remove %q: %w", entry.Name(), err)
		}
	}
	return nil
}
