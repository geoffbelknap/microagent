//go:build linux

package main

import (
	"syscall"
	"testing"
)

func TestParseOCIStopSignal(t *testing.T) {
	for _, test := range []struct {
		value string
		want  syscall.Signal
	}{
		{"", syscall.SIGTERM},
		{"SIGTERM", syscall.SIGTERM},
		{"term", syscall.SIGTERM},
		{"10", syscall.Signal(10)},
		{"SIGUSR2", syscall.SIGUSR2},
		{"SIGRTMIN+3", syscall.Signal(37)},
		{"SIGRTMAX-2", syscall.Signal(62)},
	} {
		got, err := parseOCIStopSignal(test.value)
		if err != nil || got != test.want {
			t.Errorf("parseOCIStopSignal(%q) = %v, %v; want %v", test.value, got, err, test.want)
		}
	}
	for _, value := range []string{"0", "65", "SIGBOGUS", "SIGRTMIN+31"} {
		if _, err := parseOCIStopSignal(value); err == nil {
			t.Errorf("parseOCIStopSignal(%q) succeeded", value)
		}
	}
}
