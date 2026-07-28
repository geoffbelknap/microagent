package workspace

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestDispatchDryRunReturnsThePlan pins the fix for a silently discarded
// flag: RunDispatch computed the dry-run plan inside Run, threw it away, and
// returned {workspace, empty audit} after "cleaning up" a workspace that
// never existed — success output with no sign the flag did anything.
func TestDispatchDryRunReturnsThePlan(t *testing.T) {
	stateDir := t.TempDir()
	result, err := RunDispatch(context.Background(), Options{
		ImageRef:    "docker.io/library/alpine:3.20",
		ExecCommand: "echo hi",
		StateDir:    stateDir,
		DryRun:      true,
	})
	if err != nil {
		t.Fatalf("dry run: %v", err)
	}
	if result.Plan == nil {
		t.Fatal("dry run returned no plan")
	}
	if !strings.Contains(result.Plan.GuestCommand, "echo hi") {
		t.Errorf("plan does not carry the resolved command: %q", result.Plan.GuestCommand)
	}
	if result.Result != nil || result.Audit.DecisionCount != 0 {
		t.Errorf("a dry run fabricated run evidence: %+v", result)
	}

	// Side-effect-free: nothing was written under the state dir.
	entries, err := os.ReadDir(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		t.Errorf("dry run wrote state: %s", filepath.Join(stateDir, e.Name()))
	}
}

// TestDispatchDryRunStillValidates: the plan path keeps the offline image-ref
// check, so a dry run cannot bless a config the real dispatch would refuse.
func TestDispatchDryRunStillValidates(t *testing.T) {
	_, err := RunDispatch(context.Background(), Options{
		ImageRef:    "not-a-real-image///bad",
		ExecCommand: "echo hi",
		StateDir:    t.TempDir(),
		DryRun:      true,
	})
	if err == nil {
		t.Fatal("an invalid image ref passed the dry run")
	}
}
