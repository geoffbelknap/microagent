//go:build linux

package firecracker

import (
	"testing"
	"time"
)

func TestNextStartupPollDelay(t *testing.T) {
	tests := []struct {
		name    string
		current time.Duration
		max     time.Duration
		want    time.Duration
	}{
		{name: "initial", max: 50 * time.Millisecond, want: time.Millisecond},
		{name: "double", current: 4 * time.Millisecond, max: 50 * time.Millisecond, want: 8 * time.Millisecond},
		{name: "cap", current: 32 * time.Millisecond, max: 50 * time.Millisecond, want: 50 * time.Millisecond},
		{name: "at cap", current: 50 * time.Millisecond, max: 50 * time.Millisecond, want: 50 * time.Millisecond},
		{name: "small cap", max: 500 * time.Microsecond, want: 500 * time.Microsecond},
		{name: "disabled", max: 0, want: 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := nextStartupPollDelay(tt.current, tt.max); got != tt.want {
				t.Fatalf("nextStartupPollDelay(%s, %s) = %s, want %s", tt.current, tt.max, got, tt.want)
			}
		})
	}
}
