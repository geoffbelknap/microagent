//go:build !windows

package main

import (
	"context"
	"os"
	"testing"
	"time"
)

// TestHostFDMediatorHonorsAllowlistLock pins the apple-vf host-fd datapath to
// the locked-allowlist contract: --lock-allowlist must reach the mediator's
// AllowlistLocked switch, which drops the allow-broad grant (and the
// permissive DNS grant with it). The flag was silently dropped — a workspace
// created with --egress-lock-allowlist still egressed anywhere on apple-vf.
func TestHostFDMediatorHonorsAllowlistLock(t *testing.T) {
	for _, locked := range []bool{true, false} {
		h, err := hostFDMediator(t.TempDir(), "ws", "broker", locked, []string{"example.com"}, nil, "")
		if err != nil {
			t.Fatalf("hostFDMediator(locked=%v): %v", locked, err)
		}
		if h.AllowlistLocked != locked {
			t.Fatalf("Handler.AllowlistLocked = %v, want %v", h.AllowlistLocked, locked)
		}
	}
}

func TestExitWhenParentExitsCallsExitOnParentChange(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	exited := make(chan struct{})
	go exitWhenParentExits(ctx, os.Getppid()+1000000, func() { close(exited) })
	select {
	case <-exited:
	case <-time.After(time.Second):
		t.Fatal("exitWhenParentExits did not call exit after parent mismatch")
	}
}
