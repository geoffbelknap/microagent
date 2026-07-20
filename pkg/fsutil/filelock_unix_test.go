//go:build unix

package fsutil

import (
	"path/filepath"
	"testing"
	"time"
)

// TestLockIsExclusive proves a second Lock on the same path blocks until the
// first is released — the mutual exclusion the single-writer callers rely on.
func TestLockIsExclusive(t *testing.T) {
	lockPath := filepath.Join(t.TempDir(), "index.lock")

	release1, err := Lock(lockPath)
	if err != nil {
		t.Fatalf("first Lock: %v", err)
	}

	acquired := make(chan struct{})
	go func() {
		release2, err := Lock(lockPath)
		if err != nil {
			return
		}
		close(acquired)
		_ = release2()
	}()

	// The second Lock must not acquire while the first is held.
	select {
	case <-acquired:
		t.Fatal("second Lock acquired while the first was still held")
	case <-time.After(150 * time.Millisecond):
	}

	if err := release1(); err != nil {
		t.Fatalf("release first Lock: %v", err)
	}

	// Now it must acquire.
	select {
	case <-acquired:
	case <-time.After(3 * time.Second):
		t.Fatal("second Lock did not acquire after the first was released")
	}
}
