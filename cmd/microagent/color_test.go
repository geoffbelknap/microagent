package main

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/geoffbelknap/microagent/pkg/vmkit"
)

// ttyStandinForTest opens /dev/ptmx, a character-special device, so
// fileIsTerminal (and thus colorEnabled) sees it as a terminal without
// needing a full pty pair. Skips if the platform/sandbox has no /dev/ptmx.
func ttyStandinForTest(t *testing.T) *os.File {
	t.Helper()
	f, err := os.OpenFile("/dev/ptmx", os.O_RDWR, 0)
	if err != nil {
		t.Skipf("no /dev/ptmx available to stand in for a TTY: %v", err)
	}
	t.Cleanup(func() { f.Close() })
	return f
}

func withNoColorEnv(t *testing.T, value string, set bool) {
	t.Helper()
	prev, had := os.LookupEnv("NO_COLOR")
	if set {
		os.Setenv("NO_COLOR", value)
	} else {
		os.Unsetenv("NO_COLOR")
	}
	t.Cleanup(func() {
		if had {
			os.Setenv("NO_COLOR", prev)
		} else {
			os.Unsetenv("NO_COLOR")
		}
	})
}

func TestColorEnabledNonTTYAlwaysFalse(t *testing.T) {
	pipe := pipeStdoutForTest(t)
	cases := []struct {
		name    string
		noColor bool
		env     bool
	}{
		{"defaults", false, false},
		{"NO_COLOR set", false, true},
		{"--no-color set", true, false},
		{"both set", true, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			withNoColorEnv(t, "1", tc.env)
			noColorFlag = tc.noColor
			defer func() { noColorFlag = false }()
			if colorEnabled(pipe) {
				t.Fatalf("colorEnabled must be false on a non-TTY, got true")
			}
		})
	}
}

func TestColorEnabledTTYGates(t *testing.T) {
	tty := ttyStandinForTest(t)
	cases := []struct {
		name     string
		noColor  bool
		env      bool
		envValue string
		want     bool
	}{
		{"tty, no gate set", false, false, "", true},
		{"tty, NO_COLOR set", false, true, "1", false},
		{"tty, NO_COLOR set empty", false, true, "", false},
		{"tty, --no-color set", true, false, "", false},
		{"tty, both set", true, true, "1", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			withNoColorEnv(t, tc.envValue, tc.env)
			noColorFlag = tc.noColor
			defer func() { noColorFlag = false }()
			if got := colorEnabled(tty); got != tc.want {
				t.Fatalf("colorEnabled = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestColorizeStateMapping(t *testing.T) {
	tty := ttyStandinForTest(t)
	noColorFlag = false
	withNoColorEnv(t, "", false)

	cases := []struct {
		word  string
		color string
	}{
		{"failed", ansiRed},
		{"running", ansiGreen},
		{"ready", ansiGreen},
		{"ok", ansiGreen},
		{"PASS", ansiGreen},
		{"WARN", ansiYellow},
		{"quarantined", ansiYellow},
		{"paused", ansiYellow},
		// Neutral lifecycle states: never colored, even on a TTY.
		{"prepared", ""},
		{"starting", ""},
		{"halted", ""},
		{"stopped", ""},
		{"unknown-word", ""},
	}
	for _, tc := range cases {
		t.Run(tc.word, func(t *testing.T) {
			got := colorizeState(tty, tc.word)
			if tc.color == "" {
				if got != tc.word {
					t.Fatalf("colorizeState(%q) = %q, want unchanged", tc.word, got)
				}
				return
			}
			want := tc.color + tc.word + ansiReset
			if got != want {
				t.Fatalf("colorizeState(%q) = %q, want %q", tc.word, got, want)
			}
		})
	}

	// Same table, non-TTY: every word must come back unchanged, mapped or not.
	pipe := pipeStdoutForTest(t)
	for _, tc := range cases {
		if got := colorizeState(pipe, tc.word); got != tc.word {
			t.Fatalf("colorizeState(%q) on non-TTY = %q, want unchanged", tc.word, got)
		}
	}
}

func TestPadCellAlignment(t *testing.T) {
	pipe := pipeStdoutForTest(t)
	if got := padCell(pipe, "running", 12); got != "running     " {
		t.Fatalf("padCell (no color) = %q, want %q", got, "running     ")
	}
	if got := padCell(pipe, "quarantined", 4); got != "quarantined" {
		t.Fatalf("padCell with word longer than width = %q, want unpadded %q", got, "quarantined")
	}

	tty := ttyStandinForTest(t)
	noColorFlag = false
	withNoColorEnv(t, "", false)
	got := padCell(tty, "running", 12)
	wantVisible := "running     " // 12 wide
	stripped := strings.ReplaceAll(strings.ReplaceAll(got, ansiGreen, ""), ansiReset, "")
	if stripped != wantVisible {
		t.Fatalf("padCell (color) stripped = %q, want %q", stripped, wantVisible)
	}
	if !strings.HasPrefix(got, ansiGreen) {
		t.Fatalf("padCell (color) = %q, want ansiGreen prefix", got)
	}
	// The trailing padding spaces must sit outside the color wrap so the
	// reset always lands right after the word, not after the padding.
	if !strings.Contains(got, ansiReset+"     ") {
		t.Fatalf("padCell (color) = %q, want reset immediately before padding spaces", got)
	}
}

// TestWorkspaceListTextNonTTYHasNoEscapeBytes is the accessibility/compat
// invariant this task exists to protect: piped (non-TTY) text output must
// never contain ANSI escape bytes, even when the entries carry state words
// that would be colored on a real terminal.
func TestWorkspaceListTextNonTTYHasNoEscapeBytes(t *testing.T) {
	prevFormat := outputFormat
	prevNoColor := noColorFlag
	t.Cleanup(func() {
		outputFormat = prevFormat
		noColorFlag = prevNoColor
	})
	outputFormat = "text" // force human rendering despite the pipe
	noColorFlag = false
	withNoColorEnv(t, "", false)

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	entries := []workspaceListEntry{
		{Name: "a", State: "failed", Backend: "linux-kvm", Profile: "tiny", Network: "isolated", Restart: "never"},
		{Name: "b", State: "running", Backend: "linux-kvm", Profile: "tiny", Network: "user", Restart: "always"},
		{Name: "c", State: "quarantined", Backend: "apple-vf", Profile: "tiny", Network: "isolated", Restart: "never"},
		{Name: "d", State: "prepared", Backend: "apple-vf", Profile: "tiny", Network: "isolated", Restart: "never"},
	}
	done := make(chan struct{})
	var out []byte
	go func() {
		out, _ = io.ReadAll(r)
		close(done)
	}()
	if err := writeWorkspaceList(w, entries); err != nil {
		t.Fatalf("writeWorkspaceList: %v", err)
	}
	w.Close()
	<-done
	r.Close()

	if bytes.ContainsRune(out, 0x1b) {
		t.Fatalf("non-TTY text output contained an ANSI escape byte:\n%s", out)
	}
	if !bytes.Contains(out, []byte("failed")) || !bytes.Contains(out, []byte("running")) {
		t.Fatalf("expected state words to still be present in plain form:\n%s", out)
	}
}

// TestListDispatchNonTTYHasNoEscapeBytes tests the real dispatch path:
// running `list --output text` through the CLI ensures that capture to
// non-TTY files (via runMainCapture) produces plain text with no ANSI escapes,
// even when workspace entries carry state words that would be colored on a terminal.
func TestListDispatchNonTTYHasNoEscapeBytes(t *testing.T) {
	withNoColorEnv(t, "", false) // NO_COLOR not set
	noColorFlag = false
	defer func() { noColorFlag = false }()

	// Seed a temp state dir with test workspaces.
	dir := t.TempDir()

	// Create entries with various state words: some colorizable, some not.
	entries := []struct {
		name  string
		state vmkit.VMState
	}{
		{"a", vmkit.StateFailed},
		{"b", vmkit.StateRunning},
		{"c", vmkit.StateQuarantined},
		{"d", vmkit.StatePrepared},
	}

	for _, e := range entries {
		if err := writeWorkspaceManifest(workspaceOptions{
			StateDir:      dir,
			Name:          e.name,
			Profile:       "tiny",
			RestartPolicy: "never",
			MemoryMiB:     256,
			CPUCount:      1,
			SizeMiB:       512,
		}); err != nil {
			t.Fatal(err)
		}
		eventDir := filepath.Join(dir, e.name)
		if err := os.MkdirAll(eventDir, 0o755); err != nil {
			t.Fatal(err)
		}
		event := vmkit.Event{
			Identity:   vmkit.Identity{RequestID: "req-1", RuntimeID: e.name, Role: vmkit.RoleWorkload, Backend: vmkit.BackendLinuxKVM},
			State:      e.state,
			ObservedAt: time.Date(2026, 5, 2, 7, 0, 0, 0, time.UTC),
		}
		data, err := json.Marshal(event)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(eventDir, "event.json"), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}

	// Run list command via runMainCapture (captures to files, non-TTY).
	stdout, stderr, code := runMainCapture(t, "list", "--output", "text", "--state-dir", dir)

	if code != 0 {
		t.Fatalf("list command exited with code %d; stderr: %s", code, stderr)
	}

	// Assert neither stream contains ANSI escape bytes.
	if bytes.ContainsRune(stdout, 0x1b) {
		t.Fatalf("stdout contained ANSI escape byte:\n%s", stdout)
	}
	if bytes.ContainsRune(stderr, 0x1b) {
		t.Fatalf("stderr contained ANSI escape byte:\n%s", stderr)
	}

	// Verify state words are still present in plain form.
	if !bytes.Contains(stdout, []byte("failed")) || !bytes.Contains(stdout, []byte("running")) {
		t.Fatalf("expected state words to be present:\n%s", stdout)
	}
}
