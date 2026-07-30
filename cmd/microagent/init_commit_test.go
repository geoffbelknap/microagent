package main

import (
	"os"
	"strings"
	"testing"
)

func TestRunCommitRegistryShadowOverride(t *testing.T) {
	stateDir := t.TempDir()
	args := []string{"demo", "example.com/acme/demo:v1", "--state-dir", stateDir}

	err := runCommit(t.Context(), args, os.Stdout)
	if err == nil || !strings.Contains(err.Error(), "explicitly allow registry shadowing") {
		t.Fatalf("commit without override error = %v", err)
	}

	err = runCommit(t.Context(), append(args, "--allow-registry-shadow"), os.Stdout)
	if err == nil {
		t.Fatal("commit with override unexpectedly found a workspace")
	}
	if strings.Contains(err.Error(), "explicitly allow registry shadowing") {
		t.Fatalf("commit override did not reach library options: %v", err)
	}
}
