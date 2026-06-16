package egress

import (
	"encoding/binary"
	"fmt"
	"net/netip"
)

// parseOriginalDstV4 decodes an IPv4 struct sockaddr_in. SO_ORIGINAL_DST is
// IPv4-only; IPv6 original-destination recovery (IP6T_SO_ORIGINAL_DST) is not
// supported. It decodes a struct sockaddr_in (the SO_ORIGINAL_DST result):
// family(2) port(2, big-endian) addr(4). It returns the IPv4 AddrPort.
func parseOriginalDstV4(b []byte) (netip.AddrPort, error) {
	if len(b) < 8 {
		return netip.AddrPort{}, fmt.Errorf("egress: short sockaddr (%d bytes)", len(b))
	}
	port := binary.BigEndian.Uint16(b[2:4])
	addr := netip.AddrFrom4([4]byte{b[4], b[5], b[6], b[7]})
	return netip.AddrPortFrom(addr, port), nil
}
