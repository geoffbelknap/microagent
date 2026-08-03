package main

import (
	"flag"
	"os"
	"strings"
	"testing"

	"github.com/geoffbelknap/microagent/pkg/operation"
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

// TestParseCommandFlagsJSONAliasHintDoesNotFireOnLongerFlagName is F5: the
// hint trigger must match Go's "flag provided but not defined: -json" error
// exactly (via a suffix check), not merely contain "not defined: -json" as a
// substring - otherwise an unrelated flag like "-jsonfile" that happens to
// start with "json" would incorrectly get the --request-json remediation
// note.
func TestParseCommandFlagsJSONAliasHintDoesNotFireOnLongerFlagName(t *testing.T) {
	fs := newCommandFlagSet("create")
	fs.String("state-dir", "", "State directory")
	err := parseCommandFlags(fs, os.Stdout, []string{"-jsonfile", "x"})
	if err == nil {
		t.Fatal("want error")
	}
	msg := err.Error()
	if strings.Contains(msg, "use --request-json") {
		t.Errorf("error must not fire the --json alias hint for -jsonfile, got %q", msg)
	}
}

// TestFlagParseFailuresAreNeverRetryable pins the class, not the wording. A
// command line the flag parser rejected cannot be accepted by re-running it
// unchanged, so it must never reach a caller as retryable.
//
// Untyped, these messages fell through to mapStructuredError's substring tail,
// which reads the whole string — the flag's own NAME included. `--timeout
// 5min` came back kind=transient, retryable=true, retry_after_ms=1000 and exit
// 75 (EX_TEMPFAIL) with a remediation about waiting for a host resource, so a
// scripted retry loop was told to keep re-running a typo. Each case below
// still matches a non-permanent rule in that tail (asserted here, so the case
// cannot silently stop being a regression test); the typed check is what keeps
// the classification permanent, and asserting kind rather than message text is
// what stops a reworded message from restoring the bug.
func TestFlagParseFailuresAreNeverRetryable(t *testing.T) {
	tests := []struct {
		name  string
		setup func(fs *flag.FlagSet)
		args  []string
	}{
		{
			name:  "malformed duration for a flag named timeout",
			setup: func(fs *flag.FlagSet) { durationFlag(fs, "timeout", 0, "Command timeout") },
			args:  []string{"--timeout", "5min"},
		},
		{
			name:  "unknown flag whose name reads as transient",
			setup: func(fs *flag.FlagSet) { fs.String("state-dir", "", "State directory") },
			args:  []string{"--unreachable"},
		},
		{
			name:  "rejected value that reads as transient",
			setup: func(fs *flag.FlagSet) { fs.Int("retries", 0, "Retries") },
			args:  []string{"--retries", "temporary"},
		},
		{
			name:  "rejected value that reads as a connection failure",
			setup: func(fs *flag.FlagSet) { fs.Int("retries", 0, "Retries") },
			args:  []string{"--retries", "connection refused"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fs := newCommandFlagSet("exec")
			tt.setup(fs)
			err := parseCommandFlags(fs, os.Stdout, tt.args)
			if err == nil {
				t.Fatal("want a parse error")
			}
			if !operation.IsKind(err, operation.ErrorValidation) {
				t.Fatalf("parse failure is not a typed validation error: %#v", err)
			}
			if rule, ok := matchSubstringClassifierRule(strings.ToLower(err.Error())); !ok || rule.Kind == errorKindPermanent {
				t.Fatalf("case no longer exercises the substring tail (matched=%v kind=%q); pick a message that still trips it", ok, rule.Kind)
			}

			mapped := mapStructuredError(err, "")
			if mapped.Kind != errorKindPermanent {
				t.Errorf("Kind = %q, want %q", mapped.Kind, errorKindPermanent)
			}
			if mapped.Retryable {
				t.Errorf("Retryable = true; a retry loop would re-run an unfixable command line")
			}
			if mapped.RetryAfterMS != 0 {
				t.Errorf("RetryAfterMS = %d, want 0", mapped.RetryAfterMS)
			}
			if _, code := captureError(t, "", err); code == exitTransient {
				t.Errorf("exit = %d (EX_TEMPFAIL), want a permanent code", code)
			}
		})
	}
}

// TestDurationFlagRejectionIsActionable covers the other half of the report:
// the flag package's own duration value discards time.ParseDuration's error
// and reports a bare "parse error", naming neither the accepted unit suffixes
// nor a value that would work.
func TestDurationFlagRejectionIsActionable(t *testing.T) {
	fs := newCommandFlagSet("exec")
	durationFlag(fs, "timeout", 0, "Command timeout")
	err := parseCommandFlags(fs, os.Stdout, []string{"--timeout", "5min"})
	if err == nil {
		t.Fatal("want a parse error")
	}
	msg := err.Error()
	for _, want := range []string{`"5min"`, "-timeout", "30s"} {
		if !strings.Contains(msg, want) {
			t.Errorf("message must carry %q (the offending value, the flag, and a usable example), got %q", want, msg)
		}
	}
	if strings.Contains(msg, "parse error") {
		t.Errorf("message still carries the flag package's opaque text: %q", msg)
	}
}

// TestBadFlagValuesExitPermanentEndToEnd walks the whole path a retry loop
// sees — parse or validate, classify, render, exit code — rather than only the
// classifier.
//
// The first case is the flag parser rejecting the value; the rest are the
// commands' own post-parse range checks, which were written as untyped errors
// and reached the substring tail carrying "timeout" or "too large" in their
// own wording. Every one of these exited 75 (EX_TEMPFAIL) with retryable=true
// for a command line no retry could ever fix.
func TestBadFlagValuesExitPermanentEndToEnd(t *testing.T) {
	tests := map[string][]string{
		"unparseable exec --timeout":  {"exec", "dev-agent", "--timeout", "5min", "--", "true"},
		"negative wait --timeout":     {"wait", "ws", "--timeout", "-5s"},
		"non-positive perf --timeout": {"perf", "boot", "--timeout", "0"},
		"negative --ready-timeout":    {"connect", "ws", "--ready-timeout", "-1"},
		"oversized --result-port": {
			"create", "ws", "--dry-run",
			"--image", "docker.io/library/alpine:3.20",
			"--result-port", "4294967296",
		},
	}

	for name, args := range tests {
		t.Run(name, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			t.Setenv("MICROAGENT_OUTPUT", "")
			stdout, stderr, code := runMainCapture(t, args...)
			combined := string(stdout) + string(stderr)

			if code == exitTransient {
				t.Fatalf("exit = %d (EX_TEMPFAIL); a retry loop is told to re-run an unfixable command line:\n%s", code, combined)
			}
			if code != 1 {
				t.Errorf("exit = %d, want 1:\n%s", code, combined)
			}
			if strings.Contains(combined, "becomes available") {
				t.Errorf("remediation still tells the caller to wait for a host resource:\n%s", combined)
			}
		})
	}
}
