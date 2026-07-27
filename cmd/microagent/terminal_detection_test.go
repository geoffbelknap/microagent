package main

import (
	"os"
	"testing"
)

// TestDevNullIsNotATerminal pins the fix for a check that treated every
// redirection to /dev/null as an interactive session.
//
// fileIsTerminal tested os.ModeCharDevice, and /dev/null is a character
// device. The confirmation guard in confirmDestructive has always had the right
// branch — "pass --yes to confirm" when stdin is not a terminal — and this made
// it unreachable in exactly the unattended case it was written for: `delete <
// /dev/null` prompted a stdin nobody was on, read EOF, and reported "delete
// cancelled" with no hint that --yes was the way through.
//
// The same predicate also put /dev/null into raw mode for connect, refused to
// serve MCP over it, and wrote color codes and table borders to it.
func TestDevNullIsNotATerminal(t *testing.T) {
	f, err := os.Open(os.DevNull)
	if err != nil {
		t.Skipf("open %s: %v", os.DevNull, err)
	}
	defer func() { _ = f.Close() }()

	if fileIsTerminal(f) {
		t.Errorf("fileIsTerminal(%s) = true; a character device is not a terminal", os.DevNull)
	}
}

// TestRegularFileIsNotATerminal is the case the old check already got right,
// kept so a future rewrite cannot regress it.
func TestRegularFileIsNotATerminal(t *testing.T) {
	f, err := os.CreateTemp(t.TempDir(), "out")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()

	if fileIsTerminal(f) {
		t.Error("fileIsTerminal(regular file) = true")
	}
}

// TestPtyIsATerminal is the control. Without it every assertion above is
// satisfied by a function that returns false unconditionally, which would
// disable the interactive prompt entirely — a worse failure than the one being
// fixed, because it turns the safety check into a permanent refusal.
func TestPtyIsATerminal(t *testing.T) {
	ptmx, err := os.OpenFile("/dev/ptmx", os.O_RDWR, 0)
	if err != nil {
		t.Skipf("no pty available: %v", err)
	}
	defer func() { _ = ptmx.Close() }()

	if !fileIsTerminal(ptmx) {
		t.Error("fileIsTerminal(pty) = false; the interactive path would never run")
	}
}

// TestOutputDefaultsToJSONWhenNotATerminal keeps the output policy tied to the
// same predicate. Redirecting to /dev/null used to select the human table.
func TestOutputDefaultsToJSONWhenNotATerminal(t *testing.T) {
	t.Setenv("MICROAGENT_OUTPUT", "")

	f, err := os.Open(os.DevNull)
	if err != nil {
		t.Skipf("open %s: %v", os.DevNull, err)
	}
	defer func() { _ = f.Close() }()

	if !outputJSON(f) {
		t.Error("outputJSON(/dev/null) = false; a non-terminal stream gets structured output")
	}
}
