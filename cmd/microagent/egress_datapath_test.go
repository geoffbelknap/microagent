//go:build !windows

package main

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/geoffbelknap/microagent/internal/egress"
	"github.com/geoffbelknap/microagent/pkg/vmkit"
)

// TestHostFDMediatorHonorsAllowlistLock pins the apple-vf host-fd datapath to
// the locked-allowlist contract: --lock-allowlist must reach the mediator's
// AllowlistLocked switch, which drops the allow-broad grant (and the
// permissive DNS grant with it). The flag was silently dropped — a workspace
// created with --egress-lock-allowlist still egressed anywhere on apple-vf.
func TestHostFDMediatorHonorsAllowlistLock(t *testing.T) {
	for _, locked := range []bool{true, false} {
		h, err := hostFDMediator(hostFDEgressConfig{stateDir: t.TempDir(), name: "ws", mode: "broker", lockAllowlist: locked, allow: []string{"example.com"}})
		if err != nil {
			t.Fatalf("hostFDMediator(locked=%v): %v", locked, err)
		}
		if h.AllowlistLocked != locked {
			t.Fatalf("Handler.AllowlistLocked = %v, want %v", h.AllowlistLocked, locked)
		}
	}
}

// TestEgressDatapathFlagsCoverParityRegistry is the Apple VF half of the
// mediator/datapath parity guard: every egress control the Firecracker mediator
// forwards (vmkit.EgressDatapathFields) must have a matching --egress-datapath
// flag, so a control honored on Firecracker cannot silently drop on Apple VF
// (the B1/B22/B23 fail-open class).
func TestEgressDatapathFlagsCoverParityRegistry(t *testing.T) {
	fs, _ := newEgressDatapathFlagSet()
	for _, f := range vmkit.EgressDatapathFields() {
		if fs.Lookup(f.DatapathFlag) == nil {
			t.Errorf("egress-datapath has no --%s flag (config %q); the Apple VF datapath would silently drop this egress control", f.DatapathFlag, f.ConfigField)
		}
	}
}

// TestHostFDMediatorAppliesCapsResolversAndRotation proves the parsed caps,
// resolver allowlist, and audit rotation actually reach the egress.Handler —
// not just the flag surface. These were dropped before B23.
func TestHostFDMediatorAppliesCapsResolversAndRotation(t *testing.T) {
	h, err := hostFDMediator(hostFDEgressConfig{
		stateDir:        t.TempDir(),
		name:            "ws",
		mode:            "broker",
		limits:          egress.Limits{MaxBytesPerSec: 100, MaxTotalBytes: 200, MaxConcurrentConns: 3},
		resolvers:       []string{"1.1.1.1", "not-an-ip"},
		auditMaxBytes:   4096,
		auditMaxBackups: 2,
	})
	if err != nil {
		t.Fatalf("hostFDMediator: %v", err)
	}
	if h.Limits.MaxBytesPerSec != 100 || h.Limits.MaxTotalBytes != 200 || h.Limits.MaxConcurrentConns != 3 {
		t.Fatalf("Handler.Limits = %#v, want caps applied", h.Limits)
	}
	if len(h.Resolvers) != 1 { // the invalid entry is skipped, the valid one kept
		t.Fatalf("Handler.Resolvers = %#v, want exactly the one valid resolver", h.Resolvers)
	}
	identityLogger, ok := h.Logger.(egress.IdentityLogger)
	if !ok {
		t.Fatalf("Handler.Logger = %T, want egress.IdentityLogger", h.Logger)
	}
	if _, ok := identityLogger.Logger.(*egress.RotatingFileLogger); !ok {
		t.Fatalf("IdentityLogger.Logger = %T, want *egress.RotatingFileLogger when audit cap is set", identityLogger.Logger)
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
