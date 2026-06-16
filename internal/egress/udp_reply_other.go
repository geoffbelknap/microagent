//go:build !linux

package egress

import (
	"fmt"
	"net/netip"
)

// transparentReply is the spoofed-source UDP reply path. It binds a socket
// IP_TRANSPARENT to origDst so the reply appears to come FROM the original
// destination (the TPROXY requirement). IP_TRANSPARENT is Linux-only; on other
// platforms this returns an error so the package still builds (e.g. for
// cross-platform CI). Tests inject a stub ReplyTo and never reach this.
func transparentReply(_, _ netip.AddrPort, _ []byte) error {
	return fmt.Errorf("egress: transparent spoofed-source udp reply is only supported on linux")
}
