package egress

import (
	"encoding/binary"
	"fmt"
	"net/netip"
)

// afInet is AF_INET, which is 2 on every platform (POSIX). Using a plain
// constant rather than golang.org/x/sys/afInet — which is undefined on
// non-Linux builds — lets the SO_ORIGINAL_DST parser cross-compile for
// non-Linux targets. The parser itself only ever runs on Linux.
const afInet = 0x2

// afInet6 is AF_INET6, which is 10 on every Linux architecture. Keeping the
// value here lets the sockaddr parser remain buildable on non-Linux targets;
// only the Linux original-destination adapters call it in production.
const afInet6 = 0xa

// parseOriginalDstV4 decodes an IPv4 struct sockaddr_in returned by
// SO_ORIGINAL_DST:
// family(2, host order) port(2, big-endian) addr(4). It returns the IPv4
// AddrPort.
//
// Defense in depth: it first checks the family field is AF_INET. A sockaddr
// carrying any other family (notably sockaddr_in6) is anomalous on this parser.
// Rejecting it — rather than
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

// parseOriginalDstV6 decodes a Linux struct sockaddr_in6:
// family(2, host order), port(2, network order), flowinfo(4), address(16), and
// scope ID(4). The mediated guest uses global-scope ULA addresses, so a nonzero
// scope ID is neither needed nor carried in netip.AddrPort.
func parseOriginalDstV6(b []byte) (netip.AddrPort, error) {
	if len(b) < 28 {
		return netip.AddrPort{}, fmt.Errorf("egress: short IPv6 sockaddr (%d bytes)", len(b))
	}
	if family := binary.NativeEndian.Uint16(b[0:2]); family != afInet6 {
		return netip.AddrPort{}, fmt.Errorf("egress: sockaddr family %d is not AF_INET6 (%d)", family, afInet6)
	}
	port := binary.BigEndian.Uint16(b[2:4])
	var raw [16]byte
	copy(raw[:], b[8:24])
	return netip.AddrPortFrom(netip.AddrFrom16(raw), port), nil
}
