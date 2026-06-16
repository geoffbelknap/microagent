//go:build !linux

package egress

import (
	"fmt"
	"net"
	"net/netip"
)

// OriginalDestination recovers a connection's pre-REDIRECT destination via the
// Linux-only SO_ORIGINAL_DST getsockopt. The transparent-capture mediator is
// only supported on Linux; on other platforms this returns an error so the
// package still builds (e.g. for cross-platform CI).
func OriginalDestination(conn *net.TCPConn) (netip.AddrPort, error) {
	return netip.AddrPort{}, fmt.Errorf("egress: original-destination recovery is only supported on linux")
}
