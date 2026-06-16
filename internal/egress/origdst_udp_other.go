//go:build !linux

package egress

import (
	"fmt"
	"net"
	"net/netip"
)

// parseUDPOrigDst recovers a UDP datagram's pre-TPROXY destination from the
// recvmsg control buffer via the Linux-only IP_ORIGDSTADDR ancillary message.
// The transparent-capture mediator is only supported on Linux; on other
// platforms this returns an error so the package still builds (e.g. for
// cross-platform CI).
func parseUDPOrigDst(_ []byte) (netip.AddrPort, error) {
	return netip.AddrPort{}, fmt.Errorf("egress: udp original-destination recovery is only supported on linux")
}

// transparentUDPListener opens an IP_TRANSPARENT + IP_RECVORIGDSTADDR UDP
// socket for receiving TPROXY-steered traffic. Linux-only; on other platforms
// this returns an error so the package still builds.
func transparentUDPListener(_ netip.AddrPort) (*net.UDPConn, error) {
	return nil, fmt.Errorf("egress: transparent udp listener is only supported on linux")
}
