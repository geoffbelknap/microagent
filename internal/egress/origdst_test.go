package egress

import (
	"encoding/binary"
	"testing"
)

func TestParseOriginalDstV4(t *testing.T) {
	// struct sockaddr_in: family(2, host order) port(2, big-endian) addr(4)
	b := []byte{0x02, 0x00, 0x01, 0xBB, 140, 82, 112, 3} // port 0x01BB=443
	ap, err := parseOriginalDstV4(b)
	if err != nil {
		t.Fatalf("parseOriginalDstV4: %v", err)
	}
	if ap.String() != "140.82.112.3:443" {
		t.Fatalf("AddrPort = %q, want 140.82.112.3:443", ap.String())
	}
}

func TestParseOriginalDstV4ShortInput(t *testing.T) {
	if _, err := parseOriginalDstV4([]byte{0x02, 0x00}); err == nil {
		t.Fatal("expected error for short sockaddr")
	}
}

// TestParseOriginalDstRejectsV6Sockaddr is the mediator-level defense-in-depth
// guard: parseOriginalDstV4 must REJECT a sockaddr_in6-shaped buffer (family
// AF_INET6) rather than silently misparsing the first 4 address bytes of the
// v6 flowinfo/addr as a v4 address. SO_ORIGINAL_DST is IPv4-only and the steering
// firewall is v4-only today, so a v6 sockaddr arriving here is anomalous and must
// be surfaced as an error, not coerced into a bogus v4 AddrPort. The family field
// sits at byte offset 0 (native-endian uint16); on little-endian AF_INET6 (10)
// is {0x0A, 0x00, ...}.
func TestParseOriginalDstRejectsV6Sockaddr(t *testing.T) {
	// struct sockaddr_in6 (28 bytes): family(2) port(2) flowinfo(4) addr(16) scope(4).
	buf := make([]byte, 28)
	// Native-endian family = AF_INET6 (10 on Linux). Use the literal so this
	// OS-neutral parser test builds on every host (no golang.org/x/sys/unix).
	binary.NativeEndian.PutUint16(buf[0:2], uint16(10))
	// port 0x01BB (443), big-endian — to prove we don't just fall through to a v4 parse.
	buf[2], buf[3] = 0x01, 0xBB
	// flowinfo + first addr bytes set to non-zero so a v4 misparse would yield a
	// plausible-looking (but wrong) address rather than 0.0.0.0.
	buf[4], buf[5], buf[6], buf[7] = 140, 82, 112, 3
	if _, err := parseOriginalDstV4(buf); err == nil {
		t.Fatal("expected error for AF_INET6 sockaddr, got nil (v6 buffer misparsed as v4)")
	}
}
