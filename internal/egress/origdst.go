package egress

import (
	"encoding/binary"
	"fmt"
	"net/netip"
)

// afInet is AF_INET, which is 2 on every platform (POSIX). Using a plain
// constant rather than golang.org/x/sys/afInet — which is undefined on
// non-Linux builds — lets this IPv4-only SO_ORIGINAL_DST parser cross-compile
// for non-Linux targets. The parser
// itself only ever runs on Linux; this changes no Linux behaviour.
const afInet = 0x2

// parseOriginalDstV4 decodes an IPv4 struct sockaddr_in. SO_ORIGINAL_DST is
// IPv4-only; IPv6 original-destination recovery (IP6T_SO_ORIGINAL_DST) is not
// supported. It decodes a struct sockaddr_in (the SO_ORIGINAL_DST result):
// family(2, host order) port(2, big-endian) addr(4). It returns the IPv4
// AddrPort.
//
// Defense in depth: it first checks the family field is AF_INET. The steering
// firewall is IPv4-only today (REDIRECT/TPROXY match nfproto ipv4) and guests
// are IPv4-only, so a sockaddr carrying any other family (notably a
// sockaddr_in6 with family AF_INET6) is anomalous. Rejecting it — rather than
// blindly reading bytes [4:8] of a v6 flowinfo/addr as a v4 address — prevents
// the mediator from acting on a bogus, misparsed destination.
func parseOriginalDstV4(b []byte) (netip.AddrPort, error) {
	if len(b) < 8 {
		return netip.AddrPort{}, fmt.Errorf("egress: short sockaddr (%d bytes)", len(b))
	}
	// sa_family_t is a host-byte-order uint16 at offset 0.
	if family := binary.NativeEndian.Uint16(b[0:2]); family != afInet {
		return netip.AddrPort{}, fmt.Errorf("egress: sockaddr family %d is not AF_INET (%d); IPv6/other original-destination not supported", family, afInet)
	}
	port := binary.BigEndian.Uint16(b[2:4])
	addr := netip.AddrFrom4([4]byte{b[4], b[5], b[6], b[7]})
	return netip.AddrPortFrom(addr, port), nil
}
