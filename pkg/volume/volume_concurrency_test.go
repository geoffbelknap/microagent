package volume

import (
	"fmt"
	"sync"
	"testing"
)

// TestAttachSingleWriterUnderConcurrency is the B17 guard: many workspaces racing
// to attach the same single-attach volume must yield exactly one holder, not a
// last-writer-wins free-for-all where several believe they hold it (which would
// let two running VMs mount the same ext4 read-write).
func TestAttachSingleWriterUnderConcurrency(t *testing.T) {
	dir := t.TempDir()
	// Seed one unattached volume directly to avoid formatting a real disk.
	if err := WriteIndex(dir, Index{Volumes: []Record{{Name: "vol", SizeMiB: 128}}}); err != nil {
		t.Fatalf("WriteIndex: %v", err)
	}
	isRunning := func(string) bool { return true } // a recorded holder always counts as active

	const n = 8
	var wg sync.WaitGroup
	var mu sync.Mutex
	winners := 0
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func(i int) {
			defer wg.Done()
			if _, err := Attach(dir, "vol", fmt.Sprintf("ws-%d", i), isRunning); err == nil {
				mu.Lock()
				winners++
				mu.Unlock()
			}
		}(i)
	}
	wg.Wait()

	if winners != 1 {
		t.Fatalf("concurrent Attach winners = %d, want exactly 1 (single-attach)", winners)
	}
	rec, err := Get(dir, "vol")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if rec.AttachedTo == "" {
		t.Fatal("volume shows no holder after concurrent Attach")
	}
}
