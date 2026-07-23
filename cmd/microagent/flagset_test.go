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

// TestParseCommandFlagsJSONAliasHint pins the extra guidance line added when
// a command's flagset rejects "-json": the removed request-JSON alias shape
// (e.g. "create --json request.json" reaching the flagset as a bare "-json"
// after global-flag extraction leaves it alone) must fail with both the
// generic --help pointer and a pointer at --request-json / MIGRATION.md.
func TestParseCommandFlagsJSONAliasHint(t *testing.T) {
	fs := newCommandFlagSet("create")
	fs.String("state-dir", "", "State directory")
	err := parseCommandFlags(fs, os.Stdout, []string{"-json", "x"})
	if err == nil {
		t.Fatal("want error")
	}
	msg := err.Error()
	if !strings.Contains(msg, "microagent create --help") {
		t.Errorf("error must point at --help, got %q", msg)
	}
	if !strings.Contains(msg, "use --request-json") {
		t.Errorf("error must point at --request-json, got %q", msg)
	}
}
