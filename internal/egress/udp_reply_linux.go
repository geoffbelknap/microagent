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

// transparentReply sends payload to the guest src with the source address
// spoofed to origDst — the TPROXY reply requirement. The guest sent its datagram
// to origDst and the kernel steered it to the mediator without rewriting the
// destination, so the reply must appear to come FROM origDst or the guest's
// socket will discard it (wrong source). This is achieved by binding a socket
// IP_TRANSPARENT to origDst and sending from it; CAP_NET_ADMIN/CAP_NET_RAW
// (held by the mediator namespace) is required to bind a foreign source.
//
// A fresh socket per reply is correct but not cheap; the spoofed source must
// equal the specific flow's origDst, and origDst varies per flow. A future
// optimization (out of scope here) could pool transparent sockets keyed by
// origDst. The rootless spike proved this path delivers spoofed-source replies.
func transparentReply(origDst, guestSrc netip.AddrPort, payload []byte) error {
	var ctrlErr error
	lc := net.ListenConfig{
		Control: func(_, _ string, c syscall.RawConn) error {
			if cerr := c.Control(func(fd uintptr) {
				if e := unix.SetsockoptInt(int(fd), unix.IPPROTO_IP, unix.IP_TRANSPARENT, 1); e != nil {
					ctrlErr = fmt.Errorf("egress: set IP_TRANSPARENT on reply socket: %w", e)
				}
			}); cerr != nil {
				return cerr
			}
			return ctrlErr
		},
	}
	pc, err := lc.ListenPacket(context.Background(), "udp4", origDst.String())
	if err != nil {
		return fmt.Errorf("egress: bind transparent reply socket to %s: %w", origDst, err)
	}
	defer pc.Close()
	if _, err := pc.WriteTo(payload, net.UDPAddrFromAddrPort(guestSrc)); err != nil {
		return fmt.Errorf("egress: spoofed-source reply to %s: %w", guestSrc, err)
	}
	return nil
}
