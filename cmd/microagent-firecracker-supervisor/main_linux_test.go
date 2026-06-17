//go:build linux

package main

import (
	"reflect"
	"testing"
)

// TestEgressMediatorPeerFlagParse proves the repeatable --peer name=ip flag
// parses into egress.Options.Peers (in order), alongside the other required
// flags. This is the cmd-layer half of plumbing the named-network roster from the
// supervisor into the mediator.
func TestEgressMediatorPeerFlagParse(t *testing.T) {
	args := []string{
		"--bind-port", "41000",
		"--audit-log", "/state/ws/egress-access.jsonl",
		"--peer", "builder=10.44.1.3",
		"--peer", "db=10.44.1.4",
	}
	opts, err := parseEgressMediatorOptions(args)
	if err != nil {
		t.Fatalf("parseEgressMediatorOptions: %v", err)
	}
	want := []string{"builder=10.44.1.3", "db=10.44.1.4"}
	if !reflect.DeepEqual(opts.Peers, want) {
		t.Fatalf("Peers = %v, want %v", opts.Peers, want)
	}
}

// TestEgressMediatorNoPeerFlagIsNil proves omitting --peer leaves Peers empty
// (the nat/user paths pass no roster), so Serve skips PeerCache construction.
func TestEgressMediatorNoPeerFlagIsNil(t *testing.T) {
	opts, err := parseEgressMediatorOptions([]string{"--bind-port", "41000", "--audit-log", "/x.jsonl"})
	if err != nil {
		t.Fatalf("parseEgressMediatorOptions: %v", err)
	}
	if len(opts.Peers) != 0 {
		t.Fatalf("Peers = %v, want empty", opts.Peers)
	}
}
