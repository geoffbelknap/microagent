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

func TestTryLockReportsBusyWithoutWaiting(t *testing.T) {
	lockPath := filepath.Join(t.TempDir(), "runtime.lock")
	release1, err := Lock(lockPath)
	if err != nil {
		t.Fatalf("Lock: %v", err)
	}
	defer func() { _ = release1() }()

	start := time.Now()
	if release2, acquired, err := TryLock(lockPath); err != nil {
		t.Fatalf("TryLock while busy: %v", err)
	} else if acquired {
		_ = release2()
		t.Fatal("TryLock acquired a lock held by another open file description")
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("TryLock blocked for %s", elapsed)
	}

	if err := release1(); err != nil {
		t.Fatalf("release first Lock: %v", err)
	}
	release1 = func() error { return nil }
	release2, acquired, err := TryLock(lockPath)
	if err != nil {
		t.Fatalf("TryLock after release: %v", err)
	}
	if !acquired {
		t.Fatal("TryLock did not acquire after release")
	}
	_ = release2()
}
