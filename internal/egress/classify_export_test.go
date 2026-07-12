package egress

import (
	"net/netip"
	"testing"
)

// TestIsInsideExport pins the exported classifier the broker's CONNECT guard
// reuses: the same inside/infrastructure address space the NIC datapath denies.
func TestIsInsideExport(t *testing.T) {
	inside := []string{
		"169.254.169.254",  // link-local / metadata
		"127.0.0.1",        // loopback
		"10.0.0.5",         // RFC1918
		"192.168.1.1",      // RFC1918
		"172.16.0.1",       // RFC1918
		"100.64.0.1",       // CGNAT
		"fc00::1",          // IPv6 ULA
		"::ffff:127.0.0.1", // IPv4-mapped loopback must not slip through
		"0.0.0.0",          // unspecified
	}
	for _, s := range inside {
		if !IsInside(netip.MustParseAddr(s)) {
			t.Errorf("IsInside(%s) = false, want true", s)
		}
	}
	outside := []string{"93.184.216.34", "8.8.8.8", "2606:4700:4700::1111"}
	for _, s := range outside {
		if IsInside(netip.MustParseAddr(s)) {
			t.Errorf("IsInside(%s) = true, want false", s)
		}
	}
}
