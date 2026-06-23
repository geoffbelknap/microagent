//go:build linux

package main

import (
	"testing"

	"github.com/geoffbelknap/microagent/internal/egress"
)

// TestEgressMediatorNoPeerRoster proves the user-mode mediator carries no peer
// roster (the named-network --peer flag has been removed), so Serve skips
// PeerCache construction.
func TestEgressMediatorNoPeerRoster(t *testing.T) {
	opts, err := parseEgressMediatorOptions([]string{"--bind-port", "41000", "--audit-log", "/x.jsonl"})
	if err != nil {
		t.Fatalf("parseEgressMediatorOptions: %v", err)
	}
	if len(opts.Peers) != 0 {
		t.Fatalf("Peers = %v, want empty", opts.Peers)
	}
}

// TestEgressMediatorFlagsParseLimits proves the bounded-operations flags parse
// into egress.Options.Limits and the rotating-audit-log config (ASK tenet 8).
func TestEgressMediatorFlagsParseLimits(t *testing.T) {
	args := []string{
		"--bind-port", "41000",
		"--audit-log", "/state/ws/egress-access.jsonl",
		"--max-bps", "1048576",
		"--max-bytes", "10485760",
		"--max-conns", "8",
		"--audit-max-bytes", "5242880",
		"--audit-max-backups", "3",
	}
	opts, err := parseEgressMediatorOptions(args)
	if err != nil {
		t.Fatalf("parseEgressMediatorOptions: %v", err)
	}
	want := egress.Limits{MaxBytesPerSec: 1048576, MaxTotalBytes: 10485760, MaxConcurrentConns: 8}
	if opts.Limits != want {
		t.Fatalf("Limits = %+v, want %+v", opts.Limits, want)
	}
	if opts.AuditMaxBytes != 5242880 {
		t.Fatalf("AuditMaxBytes = %d, want 5242880", opts.AuditMaxBytes)
	}
	if opts.AuditMaxBackups != 3 {
		t.Fatalf("AuditMaxBackups = %d, want 3", opts.AuditMaxBackups)
	}
}

// TestEgressMediatorNoLimitFlagsAreZero proves omitting the cap flags leaves the
// Limits and audit-rotation config zero (unlimited = current behavior).
func TestEgressMediatorNoLimitFlagsAreZero(t *testing.T) {
	opts, err := parseEgressMediatorOptions([]string{"--bind-port", "41000", "--audit-log", "/x.jsonl"})
	if err != nil {
		t.Fatalf("parseEgressMediatorOptions: %v", err)
	}
	if opts.Limits != (egress.Limits{}) {
		t.Fatalf("Limits = %+v, want zero (unlimited)", opts.Limits)
	}
	if opts.AuditMaxBytes != 0 || opts.AuditMaxBackups != 0 {
		t.Fatalf("audit rotation = (%d,%d), want zero (unbounded)", opts.AuditMaxBytes, opts.AuditMaxBackups)
	}
}
