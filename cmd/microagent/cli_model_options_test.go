package main

import (
	"context"
	"testing"
)

func TestEnsureModelPairingNoModelIsNoOp(t *testing.T) {
	opts := workspaceOptions{Name: "ws", StateDir: t.TempDir()}
	release, err := ensureModelPairing(context.Background(), &opts, "", "")
	if err != nil {
		t.Fatalf("ensureModelPairing: %v", err)
	}
	if release == nil {
		t.Fatal("no-op pairing must return a non-nil release func")
	}
	release()
	if opts.Model != "" || opts.ModelTarget != "" || opts.Env != nil {
		t.Fatalf("opts mutated without a model: model=%q target=%q env=%#v", opts.Model, opts.ModelTarget, opts.Env)
	}
}

func TestEnsureModelPairingRejectsInvalidRef(t *testing.T) {
	opts := workspaceOptions{Name: "ws", StateDir: t.TempDir()}
	if _, err := ensureModelPairing(context.Background(), &opts, "not-a-ref", ""); err == nil {
		t.Fatal("ensureModelPairing accepted an invalid model ref")
	}
}

func containsTestString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
