package main

import (
	"os"
	"strings"
	"testing"
)

func TestRunCommitRegistryShadowOverride(t *testing.T) {
	stateDir := t.TempDir()
	ref := "example.com/acme/demo:v1"
	args := []string{"demo", ref, "--state-dir", stateDir}

	err := runCommit(t.Context(), args, os.Stdout)
	if err == nil || !strings.Contains(err.Error(), "explicitly allow registry shadowing") {
		t.Fatalf("commit without override error = %v", err)
	}

	for _, tc := range []struct {
		name string
		args []string
	}{
		{"before positionals", []string{"--allow-registry-shadow", "demo", ref, "--state-dir", stateDir}},
		{"after positionals", append(args, "--allow-registry-shadow")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := runCommit(t.Context(), tc.args, os.Stdout)
			if err == nil {
				t.Fatal("commit with override unexpectedly found a workspace")
			}
			if strings.Contains(err.Error(), "usage:") || strings.Contains(err.Error(), "explicitly allow registry shadowing") {
				t.Fatalf("commit override did not reach library options: %v", err)
			}
			if !strings.Contains(err.Error(), `workspace "demo" not found`) {
				t.Fatalf("commit override error = %v, want downstream missing workspace", err)
			}
		})
	}
}
