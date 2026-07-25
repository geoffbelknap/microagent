package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/geoffbelknap/microagent/pkg/vmkit"
	"github.com/geoffbelknap/microagent/pkg/workspace"
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

// TestSnapshotForensicOverMCP: `serve mcp` is an OPERATOR surface — an MCP
// client launches it as a stdio subprocess on the operator's host, with the
// same authority as the CLI user. Guest agents inside microVMs do not reach it.
// A client that can already create workspaces and configure credential
// brokering is not escalated by taking a capture, and an investigating operator
// works through exactly this surface, so withholding the flag only made the
// adapters inconsistent.
func TestSnapshotForensicOverMCP(t *testing.T) {
	oldCreate := mcpSnapshotCreate
	oldForensic := mcpSnapshotForensic
	t.Cleanup(func() {
		mcpSnapshotCreate = oldCreate
		mcpSnapshotForensic = oldForensic
	})
	var ordinary, forensic int
	mcpSnapshotCreate = func(context.Context, workspace.Options, string) (vmkit.SnapshotManifest, error) {
		ordinary++
		return vmkit.SnapshotManifest{Tag: "ordinary"}, nil
	}
	mcpSnapshotForensic = func(context.Context, workspace.Options, string) (vmkit.SnapshotManifest, error) {
		forensic++
		return vmkit.SnapshotManifest{Tag: "forensic"}, nil
	}
	for _, args := range []map[string]any{
		{"name": "demo", "tag": "ev"},
		{"name": "demo", "tag": "ev", "forensic": false},
		{"name": "demo", "tag": "ev", "forensic": true},
	} {
		if _, handled, err := runDirectMCPTool(t.Context(), "snapshot.create", args); err != nil || !handled {
			t.Fatalf("runDirectMCPTool: handled=%v err=%v", handled, err)
		}
	}
	if ordinary != 2 || forensic != 1 {
		t.Fatalf("ordinary=%d forensic=%d, want 2/1", ordinary, forensic)
	}
}
