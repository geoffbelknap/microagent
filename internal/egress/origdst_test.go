package egress

import "testing"

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
