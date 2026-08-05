//go:build linux

package egress

import (
	"context"
	"fmt"
	"net"
	"syscall"

	"golang.org/x/sys/unix"
)

func listenTCP(addr string) (net.Listener, error) {
	var ctrlErr error
	lc := net.ListenConfig{Control: func(_, _ string, c syscall.RawConn) error {
		if err := c.Control(func(fd uintptr) {
			// A wildcard IPv6 socket serves both families. Both options are needed:
			// IPv4 still arrives through REDIRECT, while IPv6 TCP is TPROXY-steered
			// with its original destination intact.
			if e := unix.SetsockoptInt(int(fd), unix.IPPROTO_IP, unix.IP_TRANSPARENT, 1); e != nil {
				ctrlErr = fmt.Errorf("set IP_TRANSPARENT: %w", e)
				return
			}
			if e := unix.SetsockoptInt(int(fd), unix.IPPROTO_IPV6, unix.IPV6_TRANSPARENT, 1); e != nil {
				ctrlErr = fmt.Errorf("set IPV6_TRANSPARENT: %w", e)
			}
		}); err != nil {
			return err
		}
		return ctrlErr
	}}
	return lc.Listen(context.Background(), "tcp", addr)
}
