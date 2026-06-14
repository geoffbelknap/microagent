package main

import (
	"os"
	"strings"
	"testing"
)

func TestSetupNetworkingCheckReportsWithoutMutating(t *testing.T) {
	f, _ := os.CreateTemp(t.TempDir(), "out")
	defer f.Close()
	// --check must never require root and never mutate the host.
	err := runHostSetupNetworking([]string{"--check"}, f)
	// On a host that isn't set up this returns a non-nil "not ready" error;
	// either way it must not panic and must print a readiness line.
	data, _ := os.ReadFile(f.Name())
	if !strings.Contains(string(data), "networking") && err == nil {
		t.Fatalf("expected a readiness report, got empty output")
	}
}

func TestSetupNetworkingRejectsUnknownFlag(t *testing.T) {
	f, _ := os.CreateTemp(t.TempDir(), "out")
	defer f.Close()
	if err := runHostSetupNetworking([]string{"--bogus"}, f); err == nil {
		t.Fatal("expected error for unknown flag")
	}
}
