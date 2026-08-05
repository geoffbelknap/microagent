//go:build linux

package egress

import (
	"fmt"
	"net"
	"net/netip"
	"unsafe"

	"golang.org/x/sys/unix"
)

// soOriginalDst is the SOL_IP getsockopt returning an IPv4 REDIRECT flow's
// pre-NAT destination.
const soOriginalDst = 80

// IPv6 TCP uses TPROXY rather than REDIRECT. Live kernel validation found that
// IP6T_SO_ORIGINAL_DST can return the rewritten loopback address for nft REDIRECT
// flows; TPROXY preserves the destination directly as the accepted socket's local
// address. IPv6 UDP likewise gets IPV6_ORIGDSTADDR ancillary data from TPROXY.

// OriginalDestination returns the pre-REDIRECT destination of an accepted TCP
// connection — the address the guest originally dialed before nftables
// rewrote it to the mediator.
func OriginalDestination(conn *net.TCPConn) (netip.AddrPort, error) {
	if remote, ok := conn.RemoteAddr().(*net.TCPAddr); ok && remote.IP.To4() == nil {
		// IPv6 TCP is TPROXY-steered, so the accepted socket's local address is
		// the untouched destination. This avoids nft REDIRECT's inconsistent
		// IP6T_SO_ORIGINAL_DST behavior across kernel/nft combinations.
		local, ok := conn.LocalAddr().(*net.TCPAddr)
		if !ok {
			return netip.AddrPort{}, fmt.Errorf("egress: IPv6 local address is %T", conn.LocalAddr())
		}
		addr, ok := netip.AddrFromSlice(local.IP)
		if !ok {
			return netip.AddrPort{}, fmt.Errorf("egress: invalid IPv6 local address %q", local.IP)
		}
		return netip.AddrPortFrom(addr.Unmap(), uint16(local.Port)), nil
	}
	return originalDestinationV4(conn)
}

func originalDestinationV4(conn *net.TCPConn) (netip.AddrPort, error) {
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
