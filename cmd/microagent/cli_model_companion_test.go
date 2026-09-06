package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestModelCompanionExecutable(t *testing.T) {
	t.Setenv("MICROAGENT_MODEL_SERVICE_BIN", "")
	want, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if got, err := modelCompanionExecutable(); err != nil || got != want {
		t.Fatalf("bundled = %q, %v", got, err)
	}
	dir := t.TempDir()
	companion := filepath.Join(dir, "model service")
	if err := os.WriteFile(companion, []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MICROAGENT_MODEL_SERVICE_BIN", companion)
	if got, err := modelCompanionExecutable(); err != nil || got != companion {
		t.Fatalf("explicit = %q, %v", got, err)
	}
	for _, path := range []string{"microagent-model-service", "./microagent-model-service", dir, filepath.Join(dir, "missing")} {
		t.Setenv("MICROAGENT_MODEL_SERVICE_BIN", path)
		if _, err := modelCompanionExecutable(); err == nil {
			t.Fatalf("accepted %q", path)
		}
	}
	if err := os.Chmod(companion, 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MICROAGENT_MODEL_SERVICE_BIN", companion)
	if _, err := modelCompanionExecutable(); err == nil {
		t.Fatal("accepted non-executable file")
	}
	if _, err := ensureModelPairing(t.Context(), &workspaceOptions{}, "invalid-model", ""); err == nil || !strings.Contains(err.Error(), "MICROAGENT_MODEL_SERVICE_BIN") {
		t.Fatalf("invalid companion must fail before model setup: %v", err)
	}

	// Ordinary workspaces must not need the optional executable.
	if _, err := ensureModelPairing(t.Context(), &workspaceOptions{}, "", ""); err != nil {
		t.Fatal(err)
	}
}
