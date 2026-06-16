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
