//go:build linux

package egress

import (
	"fmt"
	"net"
	"net/netip"
	"unsafe"

	"golang.org/x/sys/unix"
)

// soOriginalDst is the SOL_IP getsockopt returning the pre-DNAT destination.
const soOriginalDst = 80

// Future: IPv6 mediation
//
// Egress mediation is IPv4-only today. The guest gets an IPv4-only tap address,
// the steering firewall matches nfproto ipv4 (TCP REDIRECT in the nat chain, UDP
// TPROXY in the mangle chain), and this file's OriginalDestination recovers the
// pre-REDIRECT v4 destination via SO_ORIGINAL_DST (a sockaddr_in). A guest v6
// flow would NOT be captured by the v4-only steering, so for mediated workspaces
// the supervisor installs a fail-closed DROP of all guest IPv6 egress
// (buildEgressV6DropRule / nftFilterPreroutingChain in
// pkg/supervisors/firecracker/egress_linux.go). v6 is dropped, never leaked.
//
// When a v6 guest-networking story exists, mediation extends as follows — none of
// this is enabled today:
//
//   - originalDestinationV6(conn): recover the pre-REDIRECT v6 destination with
//     getsockopt(fd, SOL_IPV6, IP6T_SO_ORIGINAL_DST) into a struct sockaddr_in6
//     (28 bytes: family(2) port(2) flowinfo(4) addr(16) scope_id(4)). IP6T_SO_
//     ORIGINAL_DST is the ip6tables/SOL_IPV6 analog of SO_ORIGINAL_DST (its
//     numeric optname differs from the v4 soOriginalDst=80; confirm against the
//     running kernel's <linux/netfilter_ipv6/ip6_tables.h> before wiring).
//   - parseOriginalDstV6(b): decode that sockaddr_in6 — assert family == AF_INET6
//     (the mirror of parseOriginalDstV4's AF_INET guard), read port big-endian
//     from b[2:4], and the 16-byte address from b[8:24] into netip.AddrFrom16.
//   - Steering: an `ip6 nat prerouting` REDIRECT analog matching `meta nfproto
//     ipv6` + (TCP|UDP) to the mediator port (the v6 mirror of
//     buildEgressRedirectRule), or an `ip6` TPROXY in a v6 mangle/prerouting
//     chain for UDP (the v6 mirror of buildEgressTProxyRule). The mediator binds a
//     transparent v6 socket on the gateway's v6 address:port.
//
// Enabling v6 mediation requires, as prerequisites:
//   1. the tap/network plan to assign the guest a v6 address (the plan is v4-only
//      today, which is why no v6 leak is possible);
//   2. buildEgressRedirectRule / buildEgressTProxyRule to STOP rejecting non-IPv4
//      subnets/mediator addrs (they currently error on v6 by design);
//   3. replacing the fail-closed v6 DROP rule with the v6 REDIRECT/TPROXY steering
//      above (drop only while v6 is unmediated).
//
// Recorded decisions:
//   - Ship v4-only mediation + a v6 fail-closed DROP now. The guest is v4-only, so
//     this captures all real egress while guaranteeing no v6 channel escapes
//     mediation ("mediation is complete" holds for the not-yet-mediated v6 path).
//   - Defer the v6 REDIRECT/TPROXY path until a v6 guest-networking story exists.
//     Building v6 steering before the guest has a v6 address would be dead,
//     untestable code; the DROP rule is the correct fail-closed placeholder until
//     then.
//   - Keep parseOriginalDstV4's AF_INET family guard as permanent defense in depth
//     even after v6 lands: the v4 and v6 recovery paths stay distinct getsockopts
//     decoding distinct sockaddr families, and each asserts its own family.

// OriginalDestination returns the pre-REDIRECT destination of an accepted TCP
// connection — the address the guest originally dialed before nftables
// rewrote it to the mediator.
func OriginalDestination(conn *net.TCPConn) (netip.AddrPort, error) {
	raw, err := conn.SyscallConn()
	if err != nil {
		return netip.AddrPort{}, err
	}
	var buf [16]byte
	size := uint32(len(buf))
	var serr error
	if cerr := raw.Control(func(fd uintptr) {
		_, _, e := unix.Syscall6(unix.SYS_GETSOCKOPT, fd,
			uintptr(unix.SOL_IP), uintptr(soOriginalDst),
			uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&size)), 0)
		if e != 0 {
			serr = e
		}
	}); cerr != nil {
		return netip.AddrPort{}, cerr
	}
	if serr != nil {
		return netip.AddrPort{}, fmt.Errorf("egress: SO_ORIGINAL_DST: %w", serr)
	}
	return parseOriginalDstV4(buf[:size])
}
