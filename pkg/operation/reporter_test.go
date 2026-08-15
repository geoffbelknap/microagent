package operation

import (
	"testing"
	"time"
)

func TestReporterAlwaysEmitsPhaseAndFinalUpdates(t *testing.T) {
	var got []ProgressEvent
	reporter := NewReporter(func(event ProgressEvent) { got = append(got, event) })
	reporter.Emit(ProgressEvent{Phase: "resolve"})
	reporter.EmitThrottled(time.Hour, ProgressEvent{Phase: "download", Bytes: 1})
	reporter.Emit(ProgressEvent{Phase: "download", Bytes: 2})

	if len(got) != 2 || got[0].Phase != "resolve" || got[1].Bytes != 2 {
		t.Fatalf("events = %#v", got)
	}
}
