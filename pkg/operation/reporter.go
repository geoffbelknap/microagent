package operation

import (
	"sync"
	"time"
)

// Reporter serializes typed progress callbacks and bounds high-frequency
// updates at their source. Producers should always use Emit for phase changes
// and final byte counts; EmitThrottled is for intermediate transfer updates.
type Reporter struct {
	fn       ProgressFunc
	mu       sync.Mutex
	lastEmit time.Time
}

func NewReporter(fn ProgressFunc) *Reporter {
	return &Reporter{fn: fn}
}

func (r *Reporter) Emit(event ProgressEvent) {
	if r == nil || r.fn == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.lastEmit = time.Now()
	r.fn(event)
}

func (r *Reporter) EmitThrottled(interval time.Duration, event ProgressEvent) {
	if r == nil || r.fn == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if interval > 0 && time.Since(r.lastEmit) < interval {
		return
	}
	r.lastEmit = time.Now()
	r.fn(event)
}
