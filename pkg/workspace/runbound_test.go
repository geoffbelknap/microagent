package workspace

import (
	"testing"
	"time"
)

// The run bound derives from the dispatch timeout for one-shot shapes only;
// zero and negative timeouts stay unbounded, and sub-second timeouts round up
// so a positive timeout never becomes an unbounded run.
func TestRunBoundSeconds(t *testing.T) {
	for timeout, want := range map[time.Duration]int{
		0:                      0,
		-time.Second:           0,
		500 * time.Millisecond: 1,
		time.Second:            1,
		2 * time.Minute:        120,
	} {
		if got := runBoundSeconds(timeout); got != want {
			t.Fatalf("runBoundSeconds(%v) = %d, want %d", timeout, got, want)
		}
	}
}
