//go:build linux

package egress

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"syscall"

	"golang.org/x/sys/unix"
)

// parseUDPOrigDst recovers the pre-TPROXY destination of a UDP datagram from
// the recvmsg out-of-band control buffer. The TPROXY mangle rule steers the
// guest's datagram to the mediator socket WITHOUT rewriting its destination, so
// (unlike TCP's SO_ORIGINAL_DST) the original dst is delivered as an
// IP_ORIGDSTADDR ancillary message rather than read back from the socket. The
// socket must have been opened with IP_RECVORIGDSTADDR enabled (see
// transparentUDPListener) for the kernel to attach this cmsg.
//
// It is the UDP analog of parseOriginalDstV4. IPv4-only: IPV6_ORIGDSTADDR is
// not supported.
func parseUDPOrigDst(oob []byte) (netip.AddrPort, error) {
	msgs, err := unix.ParseSocketControlMessage(oob)
	if err != nil {
		return netip.AddrPort{}, fmt.Errorf("egress: parse udp control message: %w", err)
	}
	for i := range msgs {
		m := &msgs[i]
		if m.Header.Level != unix.IPPROTO_IP || m.Header.Type != unix.IP_ORIGDSTADDR {
			continue
		}
		// Body is a struct sockaddr_in: family(2, host order) port(2,
		// big-endian) addr(4). parseOriginalDstV4 decodes that same layout.
		return parseOriginalDstV4(m.Data)
	}
	return netip.AddrPort{}, fmt.Errorf("egress: no IP_ORIGDSTADDR control message in %d cmsg(s)", len(msgs))
}

// transparentUDPListener opens a UDP socket able to receive TPROXY-steered
// datagrams addressed to foreign destinations (IP_TRANSPARENT) and to report
// each datagram's original destination via an IP_ORIGDSTADDR ancillary message
// (IP_RECVORIGDSTADDR). Both options are set on the fd before bind. Binding
// transparent requires CAP_NET_ADMIN/CAP_NET_RAW, which the mediator namespace
// holds; recovery of the original dst is done per-datagram with parseUDPOrigDst.
func transparentUDPListener(addr netip.AddrPort) (*net.UDPConn, error) {
	var ctrlErr error
	lc := net.ListenConfig{
		Control: func(_, _ string, c syscall.RawConn) error {
			if cerr := c.Control(func(fd uintptr) {
				if e := unix.SetsockoptInt(int(fd), unix.IPPROTO_IP, unix.IP_TRANSPARENT, 1); e != nil {
					ctrlErr = fmt.Errorf("egress: set IP_TRANSPARENT: %w", e)
					return
				}
				if e := unix.SetsockoptInt(int(fd), unix.IPPROTO_IP, unix.IP_RECVORIGDSTADDR, 1); e != nil {
					ctrlErr = fmt.Errorf("egress: set IP_RECVORIGDSTADDR: %w", e)
					return
				}
			}); cerr != nil {
				return cerr
			}
			return ctrlErr
		},
	}
	pc, err := lc.ListenPacket(context.Background(), "udp4", addr.String())
	if err != nil {
		return nil, fmt.Errorf("egress: listen transparent udp %s: %w", addr, err)
	}
	uc, ok := pc.(*net.UDPConn)
	if !ok {
		pc.Close()
		return nil, fmt.Errorf("egress: transparent udp listener is %T, not *net.UDPConn", pc)
	}
	return uc, nil
}
