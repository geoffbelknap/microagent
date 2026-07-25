package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestSnapshotCreateAcceptsForensicFlag: the library's forensic capture must be
// reachable from the CLI. Without this the capability exists only to Go callers
// — a library/adapter split the design rules forbid. The flag must parse and
// reach dispatch rather than being rejected as an unknown flag.
func TestSnapshotCreateAcceptsForensicFlag(t *testing.T) {
	dir := t.TempDir()
	out, err := os.Create(filepath.Join(dir, "out.txt"))
	if err != nil {
		t.Fatal(err)
	}
	rerr := run(t.Context(), []string{"snapshot", "create", "agent-1", "--forensic", "--tag", "ev", "--state-dir", dir, "--supervisor", filepath.Join(dir, "no-supervisor")}, out)
	_ = out.Close()
	if rerr == nil {
		t.Fatal("expected dispatch error with a missing supervisor")
	}
	if strings.Contains(rerr.Error(), "flag provided but not defined") || strings.Contains(rerr.Error(), "usage:") {
		t.Fatalf("snapshot create did not accept --forensic: %v", rerr)
	}
}

// TestSnapshotHelpAdvertisesForensic: an operator who never reads the docs must
// still be able to find the capture verb from --help.
func TestSnapshotHelpAdvertisesForensic(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "help.txt")
	out, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := runSnapshot(t.Context(), []string{"--help"}, out); err != nil {
		t.Fatal(err)
	}
	_ = out.Close()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "--forensic") {
		t.Fatalf("snapshot help does not advertise --forensic:\n%s", body)
	}
}

// TestSnapshotForensicNotExposedOverMCP: MCP is the AGENT-facing adapter, so a
// capture that retains guest secrets must not be reachable through it — an
// agent able to capture itself or a sibling could read credential material it
// was never granted. Capturing evidence is an operator action. This asserts the
// deliberate CLI/MCP difference stays deliberate.
func TestSnapshotForensicNotExposedOverMCP(t *testing.T) {
	got, err := mcpCLIArgs("snapshot.create", map[string]any{"name": "demo", "forensic": true, "tag": "ev"})
	if err != nil {
		t.Fatalf("mcpCLIArgs: %v", err)
	}
	for _, arg := range got {
		if strings.Contains(arg, "forensic") {
			t.Fatalf("MCP snapshot.create forwarded a forensic flag: %#v", got)
		}
	}
}
