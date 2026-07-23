package main

import (
	"os"
	"strings"
	"testing"
)

func TestParseCommandFlagsFriendlyError(t *testing.T) {
	fs := newCommandFlagSet("list")
	fs.String("state-dir", "", "State directory")
	err := parseCommandFlags(fs, os.Stdout, []string{"--nope"})
	if err == nil {
		t.Fatal("want error")
	}
	msg := err.Error()
	if !strings.Contains(msg, "microagent list --help") {
		t.Errorf("error must point at --help, got %q", msg)
	}
	if strings.Count(msg, "not defined") > 1 {
		t.Errorf("error text must not repeat: %q", msg)
	}
}
