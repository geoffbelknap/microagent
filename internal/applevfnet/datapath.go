// Package applevfnet is the Apple VF host-fd egress datapath: a userspace
// network stack (gVisor tcpip) that owns the guest's only NIC over a
// VZFileHandleNetworkDeviceAttachment socket, acts as the guest's L3 gateway,
// and routes guest flows out to the real network — or, with mediation on, into
// the egress mediator. This is the enforcement edge of the apple-vf
// `applevf-host-fd-gateway` capture provider: the guest has no other uplink, so
// egress cannot bypass it.
//
// gVisor's tcpip stack is pure Go and portable to darwin; only its Linux
// fdbased link endpoint is not, so this package supplies its own link endpoint
// over the unix datagram socket shared with the Swift supervisor (added in a
// later step).
package applevfnet

import (
	"gvisor.dev/gvisor/pkg/tcpip"
	"gvisor.dev/gvisor/pkg/tcpip/network/arp"
	"gvisor.dev/gvisor/pkg/tcpip/network/ipv4"
	"gvisor.dev/gvisor/pkg/tcpip/stack"
	"gvisor.dev/gvisor/pkg/tcpip/transport/icmp"
	"gvisor.dev/gvisor/pkg/tcpip/transport/tcp"
	"gvisor.dev/gvisor/pkg/tcpip/transport/udp"
)

// newStack builds the userspace network stack with the protocol handlers the
// gateway needs: IPv4 + ARP at the network layer; TCP, UDP, and ICMP at the
// transport layer. IPv6 is intentionally absent — like Firecracker, the
// apple-vf provider drops guest IPv6 fail-closed until a v6 datapath is
// justified.
func newStack() *stack.Stack {
	return stack.New(stack.Options{
		NetworkProtocols: []stack.NetworkProtocolFactory{
			ipv4.NewProtocol,
			arp.NewProtocol,
		},
		TransportProtocols: []stack.TransportProtocolFactory{
			tcp.NewProtocol,
			udp.NewProtocol,
			icmp.NewProtocol4,
		},
	})
}

// stackError adapts a tcpip.Error to the error interface for callers.
type stackError struct {
	op  string
	err tcpip.Error
}

func (e *stackError) Error() string { return e.op + ": " + e.err.String() }
