package egress

import (
	"context"
	"fmt"
	"testing"
	"time"
)

// drainDropDenies returns the packet counts of every unmediatable-protocol
// deny the logger recorded, in order.
func drainDropDenies(log *BufferLogger) []uint64 {
	var out []uint64
	for _, ev := range log.Snapshot() {
		if ev["event"] != "egress_deny" || ev["signal"] != SignalUnmediatableProtocol {
			continue
		}
		if packets, ok := ev["packets"].(uint64); ok {
			out = append(out, packets)
		}
	}
	return out
}

// runSampler drives sampleDropCounters through exactly len(readings) polls by
// feeding one reading per call and cancelling once they are exhausted.
func runSampler(t *testing.T, readings [][]DropCount) *BufferLogger {
	t.Helper()
	log := &BufferLogger{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	calls := 0
	go func() {
		defer close(done)
		sampleDropCounters(ctx, log, func() ([]DropCount, error) {
			if calls >= len(readings) {
				cancel()
				return nil, nil
			}
			r := readings[calls]
			calls++
			return r, nil
		}, time.Millisecond)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("sampleDropCounters did not return")
	}
	return log
}

func counts(n uint64) []DropCount {
	return []DropCount{{Class: "non-tcp-udp-ipv4", Packets: n, Bytes: n * 84}}
}

// TestSampleDropCountersSeedsFirstPoll is the audit-integrity case: the
// counters live in datapath rules that outlive the mediator, so a mediator
// that restarted mid-workspace must not re-report drops a previous one
// already recorded. The first poll establishes the baseline silently.
func TestSampleDropCountersSeedsFirstPoll(t *testing.T) {
	log := runSampler(t, [][]DropCount{counts(7)})
	if got := drainDropDenies(log); len(got) != 0 {
		t.Fatalf("first poll reported %v, want nothing (it only seeds the baseline)", got)
	}
}

// TestSampleDropCountersReportsIncrease covers the real case this exists for:
// a guest ping is dropped at the firewall, and the increase surfaces as an
// audit event carrying the delta, not the cumulative total.
func TestSampleDropCountersReportsIncrease(t *testing.T) {
	log := runSampler(t, [][]DropCount{counts(0), counts(3), counts(5)})
	got := drainDropDenies(log)
	want := []uint64{3, 2}
	if len(got) != len(want) {
		t.Fatalf("reported %v, want %v (per-poll deltas, not cumulative totals)", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("reported %v, want %v", got, want)
		}
	}
}

// TestSampleDropCountersQuietClassStaysSilent: an unchanged counter must not
// produce a heartbeat event every interval.
func TestSampleDropCountersQuietClassStaysSilent(t *testing.T) {
	log := runSampler(t, [][]DropCount{counts(4), counts(4), counts(4)})
	if got := drainDropDenies(log); len(got) != 0 {
		t.Fatalf("quiet class reported %v, want nothing", got)
	}
}

// TestSampleDropCountersHandlesCounterReset covers the rules being replaced
// mid-run: the counter restarts below its previous value. Reporting
// new-minus-old would underflow uint64 into a nonsense burst, so the reset is
// re-baselined instead, and only growth after it is reported.
func TestSampleDropCountersHandlesCounterReset(t *testing.T) {
	log := runSampler(t, [][]DropCount{counts(0), counts(9), counts(2), counts(6)})
	got := drainDropDenies(log)
	want := []uint64{9, 4}
	if len(got) != len(want) {
		t.Fatalf("reported %v, want %v (reset re-baselines, never underflows)", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("reported %v, want %v", got, want)
		}
	}
}

// TestSampleDropCountersClassAppearingMidRunIsBaselined: rules reinstalled
// mid-run introduce a class the sampler has not seen. Its cumulative total is
// history, not a delta, so it is baselined rather than reported as one burst.
func TestSampleDropCountersClassAppearingMidRun(t *testing.T) {
	log := runSampler(t, [][]DropCount{
		{},
		{{Class: "non-tcp-udp-ipv4", Packets: 12}},
		{{Class: "non-tcp-udp-ipv4", Packets: 15}},
	})
	got := drainDropDenies(log)
	if len(got) != 1 || got[0] != 3 {
		t.Fatalf("reported %v, want [3] (the class is baselined on first sight, then reports growth)", got)
	}
}

// TestSampleDropCountersSurvivesSampleError: losing drop visibility must never
// take the mediator down, and a transient error must not corrupt the baseline.
func TestSampleDropCountersSurvivesSampleError(t *testing.T) {
	log := &BufferLogger{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	calls := 0
	go func() {
		defer close(done)
		sampleDropCounters(ctx, log, func() ([]DropCount, error) {
			calls++
			switch calls {
			case 1:
				return counts(0), nil
			case 2:
				return nil, fmt.Errorf("read failed")
			case 3:
				return counts(4), nil
			default:
				cancel()
				return nil, nil
			}
		}, time.Millisecond)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("sampleDropCounters did not return after a sample error")
	}
	if got := drainDropDenies(log); len(got) != 1 || got[0] != 4 {
		t.Fatalf("reported %v, want [4] (the loop recovers and the baseline survives)", got)
	}
	sawErr := false
	for _, ev := range log.Snapshot() {
		if ev["event"] == "egress_drop_counter_error" {
			sawErr = true
		}
	}
	if !sawErr {
		t.Fatal("sample error was not recorded")
	}
}

// TestSampleDropCountersNilHookReturns: with no counter source the sampler
// must return immediately rather than spin.
func TestSampleDropCountersNilHookReturns(t *testing.T) {
	done := make(chan struct{})
	go func() {
		defer close(done)
		sampleDropCounters(context.Background(), &BufferLogger{}, nil, time.Millisecond)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("sampleDropCounters with a nil hook did not return")
	}
}
