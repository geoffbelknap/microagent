package workspace

import (
	"context"
	"testing"

	"github.com/geoffbelknap/microagent/pkg/operation"
)

func TestLifecycleEntryPointsEmitProgressBeforeValidationFailure(t *testing.T) {
	for _, tc := range []struct {
		name      string
		wantPhase string
		run       func(Options) error
	}{
		{
			name: "start", wantPhase: "start_validate",
			run: func(opts Options) error { _, err := Start(context.Background(), opts); return err },
		},
		{
			name: "control", wantPhase: "pause_validate",
			run: func(opts Options) error { _, err := Control(context.Background(), opts, "pause"); return err },
		},
		{
			name: "delete", wantPhase: "delete_validate",
			run: func(opts Options) error { _, err := Delete(context.Background(), opts, DeleteOptions{}); return err },
		},
		{
			name: "quarantine", wantPhase: "quarantine_validate",
			run: func(opts Options) error {
				_, err := Quarantine(context.Background(), opts, QuarantineOptions{})
				return err
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var phases []string
			opts := Options{Progress: func(event operation.ProgressEvent) { phases = append(phases, event.Phase) }}
			if err := tc.run(opts); err == nil {
				t.Fatal("operation unexpectedly succeeded")
			}
			if len(phases) == 0 || phases[0] != tc.wantPhase {
				t.Fatalf("progress phases = %#v, want first phase %q", phases, tc.wantPhase)
			}
		})
	}
}

func assertProgressPhaseOrder(t *testing.T, got, want []string) {
	t.Helper()
	position := 0
	for _, phase := range got {
		if position < len(want) && phase == want[position] {
			position++
		}
	}
	if position != len(want) {
		t.Fatalf("progress phases = %#v, want ordered subsequence %#v", got, want)
	}
}
