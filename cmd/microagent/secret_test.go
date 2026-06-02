package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// runSecretCapture runs the CLI with the given args and env overrides,
// returning stdout contents.
func runSecretCapture(t *testing.T, env map[string]string, args ...string) (string, error) {
	t.Helper()
	for k, v := range env {
		t.Setenv(k, v)
	}
	dir := t.TempDir()
	stdoutPath := filepath.Join(dir, "stdout.txt")
	stdout, err := os.Create(stdoutPath)
	if err != nil {
		t.Fatal(err)
	}
	runErr := run(context.Background(), args, stdout)
	if closeErr := stdout.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	data, err := os.ReadFile(stdoutPath)
	if err != nil {
		t.Fatal(err)
	}
	return string(data), runErr
}

func TestSecretCheckTextReportsOKAndWarning(t *testing.T) {
	out, err := runSecretCapture(t, map[string]string{"MY_TOK": "abcdef"}, "--text", "secret", "check", "API=env:MY_TOK")
	if err != nil {
		t.Fatalf("run error: %v", err)
	}
	if !strings.Contains(out, "API") || !strings.Contains(out, "ok") {
		t.Fatalf("output missing ok line: %q", out)
	}
	if !strings.Contains(out, "bytes=6") {
		t.Fatalf("output missing byte length: %q", out)
	}
	if !strings.Contains(out, "warning") {
		t.Fatalf("output missing plaintext warning: %q", out)
	}
	if strings.Contains(out, "abcdef") {
		t.Fatalf("output leaked secret value: %q", out)
	}
}

func TestSecretCheckJSONFlag(t *testing.T) {
	out, err := runSecretCapture(t, map[string]string{"MY_TOK": "abcdef"}, "--json", "secret", "check", "API=env:MY_TOK")
	if err != nil {
		t.Fatalf("run error: %v", err)
	}
	var results []map[string]any
	if err := json.Unmarshal([]byte(out), &results); err != nil {
		t.Fatalf("output is not JSON array: %v\n%s", err, out)
	}
	if len(results) != 1 || results[0]["name"] != "API" || results[0]["ok"] != true {
		t.Fatalf("unexpected JSON: %s", out)
	}
	if strings.Contains(out, "abcdef") {
		t.Fatalf("JSON leaked secret value: %q", out)
	}
}

func TestSecretCheckUnknownSchemeExitsNonZero(t *testing.T) {
	_, err := runSecretCapture(t, nil, "secret", "check", "API=bogus:x")
	if err == nil {
		t.Fatal("expected non-zero exit for failing check")
	}
}

func TestSecretCheckNoArgsErrors(t *testing.T) {
	_, err := runSecretCapture(t, nil, "secret", "check")
	if err == nil {
		t.Fatal("expected usage error with no entries")
	}
}

func TestSecretUnknownSubcommandErrors(t *testing.T) {
	_, err := runSecretCapture(t, nil, "secret", "bogus")
	if err == nil {
		t.Fatal("expected error for unknown secret subcommand")
	}
}

func TestSecretCheckHelpGoesToStdout(t *testing.T) {
	out, err := runSecretCapture(t, nil, "secret", "check", "--help")
	if err != nil {
		t.Fatalf("run error: %v", err)
	}
	if !strings.Contains(out, "microagent secret") || !strings.Contains(out, "check NAME=") {
		t.Fatalf("help not printed to stdout: %q", out)
	}
}
