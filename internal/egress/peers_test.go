package egress

import (
	"net/netip"
	"testing"
)

func TestPeerCacheNameByIP(t *testing.T) {
	pc, err := NewPeerCache([]string{"builder=10.44.1.3", "db=10.44.1.4"})
	if err != nil {
		t.Fatalf("NewPeerCache: %v", err)
	}
	if name, ok := pc.NameByIP(netip.MustParseAddr("10.44.1.3")); !ok || name != "builder" {
		t.Fatalf("NameByIP(10.44.1.3) = %q,%v; want builder,true", name, ok)
	}
	if name, ok := pc.NameByIP(netip.MustParseAddr("10.44.1.4")); !ok || name != "db" {
		t.Fatalf("NameByIP(10.44.1.4) = %q,%v; want db,true", name, ok)
	}
	// Unknown IP is not a peer.
	if name, ok := pc.NameByIP(netip.MustParseAddr("10.44.1.99")); ok {
		t.Fatalf("NameByIP(10.44.1.99) = %q,%v; want not ok", name, ok)
	}
}

func TestNewPeerCacheRejectsMalformed(t *testing.T) {
	cases := []struct {
		name  string
		pairs []string
	}{
		{"missing equals", []string{"builder"}},
		{"unparseable ip", []string{"builder=not-an-ip"}},
		{"empty name", []string{"=10.44.1.3"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if pc, err := NewPeerCache(tc.pairs); err == nil {
				t.Fatalf("NewPeerCache(%v) = %+v, nil; want error", tc.pairs, pc)
			}
		})
	}
}

// TestNewPeerCacheNilEmpty proves the constructor tolerates an empty roster (no
// peers) — the nat/user call sites pass nil.
func TestNewPeerCacheNilEmpty(t *testing.T) {
	pc, err := NewPeerCache(nil)
	if err != nil {
		t.Fatalf("NewPeerCache(nil): %v", err)
	}
	if _, ok := pc.NameByIP(netip.MustParseAddr("10.44.1.3")); ok {
		t.Fatal("empty PeerCache must not resolve any IP")
	}
}
