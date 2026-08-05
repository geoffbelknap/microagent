//go:build !windows

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
// (a channel endpoint pumped to/from the unix datagram socket shared with the
// Swift supervisor — each datagram is one Ethernet frame).
package applevfnet

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/netip"
	"os"
	"sync"
	"syscall"
	"time"

	"gvisor.dev/gvisor/pkg/buffer"
	"gvisor.dev/gvisor/pkg/tcpip"
	"gvisor.dev/gvisor/pkg/tcpip/adapters/gonet"
	"gvisor.dev/gvisor/pkg/tcpip/header"
	"gvisor.dev/gvisor/pkg/tcpip/link/channel"
	"gvisor.dev/gvisor/pkg/tcpip/link/ethernet"
	"gvisor.dev/gvisor/pkg/tcpip/network/arp"
	"gvisor.dev/gvisor/pkg/tcpip/network/ipv4"
	"gvisor.dev/gvisor/pkg/tcpip/network/ipv6"
	"gvisor.dev/gvisor/pkg/tcpip/stack"
	"gvisor.dev/gvisor/pkg/tcpip/transport/icmp"
	"gvisor.dev/gvisor/pkg/tcpip/transport/tcp"
	"gvisor.dev/gvisor/pkg/tcpip/transport/udp"
	"gvisor.dev/gvisor/pkg/waiter"
)

const (
	// nicID is the single NIC the gateway drives.
	nicID = tcpip.NICID(1)
	// frameMTU is the guest link MTU; matches the Swift attachment's MTU.
	frameMTU = 1500
	// maxFrame bounds a single datagram read (Ethernet + MTU + slack).
	maxFrame = 65536
	// udpForwardTimeout idles a NAT'd UDP flow out after inactivity.
	udpForwardTimeout = 60 * time.Second
)

// DialFunc opens a host-side connection for a guest flow. S1 uses a direct
// net.Dialer (plain NAT to the real network); S2 swaps in a dialer that routes
// through the egress mediator with the original destination preserved.
type DialFunc func(ctx context.Context, network, addr string) (net.Conn, error)

// UDPHandlerFunc mediates one accepted guest UDP flow. src is the guest source
// address and dst is the original destination the guest targeted.
type UDPHandlerFunc func(ctx context.Context, guest net.Conn, src, dst netip.AddrPort)

// Config parameterizes the gateway. GatewayIP is the guest's default gateway and
// the stack's own address; GuestIP is informational (the guest configures its
// own address via the supervisor's kernel cmdline).
type Config struct {
	GatewayIP   tcpip.Address
	GatewayIPv6 tcpip.Address
	GatewayMAC  tcpip.LinkAddress
	// Dial opens host-side connections; defaults to a direct net.Dialer.
	Dial DialFunc
	// UDPHandler handles guest UDP flows. When nil, UDP uses Dial like TCP.
	UDPHandler UDPHandlerFunc
	// Logf logs operational events; defaults to no-op.
	Logf func(format string, args ...any)
}

// Gateway is a running host-fd datapath. Close stops it and releases the stack.
type Gateway struct {
	stack *stack.Stack
	ep    *channel.Endpoint
	conn  *os.File
	cfg   Config
	ctx   context.Context
	stop  context.CancelFunc
	wg    sync.WaitGroup
	close sync.Once
}

func (c Config) logf(format string, args ...any) {
	if c.Logf != nil {
		c.Logf(format, args...)
	}
}

// Run builds the gateway over the given Ethernet-frame datagram socket (one
// datagram per frame) and blocks until the socket closes or ctx is cancelled.
func Run(ctx context.Context, conn *os.File, cfg Config) error {
	gw, err := newGateway(ctx, conn, cfg)
	if err != nil {
		return err
	}
	defer gw.Close()
	gw.wg.Add(1)
	go gw.pumpOutbound()
	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			gw.closeFrameSocket()
		case <-done:
		}
	}()
	err = gw.pumpInbound()
	close(done)
	return err
}

// RunFromFD is the subprocess entry point: it runs the datapath over an
// already-open datagram socket fd (inherited from the Swift supervisor) using a
// string gateway IP/MAC, so callers need not depend on gVisor types. gatewayMAC
// may be empty for a default.
func RunFromFD(ctx context.Context, fdNum int, gatewayIP, gatewayIPv6, gatewayMAC string, logf func(string, ...any)) error {
	return RunFromFDConfig(ctx, fdNum, gatewayIP, gatewayIPv6, gatewayMAC, Config{Logf: logf})
}

// RunFromFDConfig is RunFromFD with an explicit gateway config.
func RunFromFDConfig(ctx context.Context, fdNum int, gatewayIP, gatewayIPv6, gatewayMAC string, cfg Config) error {
	ip := net.ParseIP(gatewayIP)
	if ip == nil || ip.To4() == nil {
		return fmt.Errorf("applevfnet: gateway IP %q must be IPv4", gatewayIP)
	}
	cfg.GatewayIP = tcpip.AddrFromSlice(ip.To4())
	if gatewayIPv6 != "" {
		ip6 := net.ParseIP(gatewayIPv6)
		if ip6 == nil || ip6.To4() != nil {
			return fmt.Errorf("applevfnet: gateway IPv6 %q must be IPv6", gatewayIPv6)
		}
		cfg.GatewayIPv6 = tcpip.AddrFromSlice(ip6.To16())
	}
	if gatewayMAC != "" {
		hw, err := net.ParseMAC(gatewayMAC)
		if err != nil {
			return fmt.Errorf("applevfnet: gateway MAC %q: %w", gatewayMAC, err)
		}
		cfg.GatewayMAC = tcpip.LinkAddress(hw)
	}
	conn := os.NewFile(uintptr(fdNum), "applevf-egress-datapath")
	if conn == nil {
		return fmt.Errorf("applevfnet: fd %d is not a valid file", fdNum)
	}
	defer func() { _ = conn.Close() }()
	return Run(ctx, conn, cfg)
}

func newGateway(ctx context.Context, conn *os.File, cfg Config) (*Gateway, error) {
	if cfg.Dial == nil {
		d := &net.Dialer{Timeout: 15 * time.Second}
		cfg.Dial = d.DialContext
	}
	if cfg.GatewayMAC == "" {
		cfg.GatewayMAC = tcpip.LinkAddress("\x0a\x00\x00\x00\x00\x01")
	}
	s := newStack()
	ep := channel.New(512, frameMTU, cfg.GatewayMAC)
	if err := s.CreateNIC(nicID, ethernet.New(ep)); err != nil {
		return nil, &stackError{"create NIC", err}
	}
	// The gateway answers ARP for, and owns, GatewayIP.
	protoAddr := tcpip.ProtocolAddress{
		Protocol:          ipv4.ProtocolNumber,
		AddressWithPrefix: cfg.GatewayIP.WithPrefix(),
	}
	if err := s.AddProtocolAddress(nicID, protoAddr, stack.AddressProperties{}); err != nil {
		return nil, &stackError{"add gateway address", err}
	}
	if cfg.GatewayIPv6.Len() != 0 {
		protoAddr6 := tcpip.ProtocolAddress{
			Protocol:          ipv6.ProtocolNumber,
			AddressWithPrefix: cfg.GatewayIPv6.WithPrefix(),
		}
		if err := s.AddProtocolAddress(nicID, protoAddr6, stack.AddressProperties{}); err != nil {
			return nil, &stackError{"add IPv6 gateway address", err}
		}
	}
	// Promiscuous + spoofing let the stack accept guest packets addressed to any
	// destination (so the forwarders catch connections to arbitrary hosts) and
	// reply from the spoofed destination address.
	if err := s.SetPromiscuousMode(nicID, true); err != nil {
		return nil, &stackError{"promiscuous", err}
	}
	if err := s.SetSpoofing(nicID, true); err != nil {
		return nil, &stackError{"spoofing", err}
	}
	s.AddRoute(tcpip.Route{Destination: header.IPv4EmptySubnet, NIC: nicID})
	if cfg.GatewayIPv6.Len() != 0 {
		s.AddRoute(tcpip.Route{Destination: header.IPv6EmptySubnet, NIC: nicID})
	}

	gctx, cancel := context.WithCancel(ctx)
	gw := &Gateway{stack: s, ep: ep, conn: conn, cfg: cfg, ctx: gctx, stop: cancel}
	gw.installForwarders()
	return gw, nil
}

// installForwarders wires the TCP and UDP NAT forwarders: every guest connection
// is accepted and spliced to a host-side connection opened by cfg.Dial.
func (gw *Gateway) installForwarders() {
	tcpFwd := tcp.NewForwarder(gw.stack, 0, 2048, func(r *tcp.ForwarderRequest) {
		id := r.ID()
		dst := net.JoinHostPort(addrString(id.LocalAddress), fmt.Sprint(id.LocalPort))
		outbound, err := gw.cfg.Dial(gw.ctx, "tcp", dst)
		if err != nil {
			gw.cfg.logf("apple-vf egress: dial tcp %s: %v", dst, err)
			r.Complete(true)
			return
		}
		var wq waiter.Queue
		ep, terr := r.CreateEndpoint(&wq)
		if terr != nil {
			_ = outbound.Close()
			r.Complete(true)
			return
		}
		r.Complete(false)
		go splice(gonet.NewTCPConn(&wq, ep), outbound)
	})
	gw.stack.SetTransportProtocolHandler(tcp.ProtocolNumber, tcpFwd.HandlePacket)

	udpFwd := udp.NewForwarder(gw.stack, func(r *udp.ForwarderRequest) bool {
		id := r.ID()
		dst := net.JoinHostPort(addrString(id.LocalAddress), fmt.Sprint(id.LocalPort))
		var wq waiter.Queue
		ep, terr := r.CreateEndpoint(&wq)
		if terr != nil {
			return true
		}
		guest := gonet.NewUDPConn(&wq, ep)
		if gw.cfg.UDPHandler != nil {
			srcAP := addrPort(id.RemoteAddress, id.RemotePort)
			dstAP := addrPort(id.LocalAddress, id.LocalPort)
			go gw.cfg.UDPHandler(gw.ctx, guest, srcAP, dstAP)
			return true
		}
		outbound, err := gw.cfg.Dial(gw.ctx, "udp", dst)
		if err != nil {
			_ = guest.Close()
			gw.cfg.logf("apple-vf egress: dial udp %s: %v", dst, err)
			return true // consume the datagram; nothing to splice it to
		}
		go spliceUDP(guest, outbound)
		return true
	})
	gw.stack.SetTransportProtocolHandler(udp.ProtocolNumber, udpFwd.HandlePacket)
}

// pumpInbound reads Ethernet frames from the socket and injects them into the
// stack. It returns when the socket closes or ctx is cancelled.
func (gw *Gateway) pumpInbound() error {
	buf := make([]byte, maxFrame)
	for {
		n, err := gw.conn.Read(buf)
		if err != nil {
			if gw.ctx.Err() != nil || err == io.EOF {
				return nil
			}
			return fmt.Errorf("apple-vf egress: read frame: %w", err)
		}
		if n == 0 {
			continue
		}
		frame := make([]byte, n)
		copy(frame, buf[:n])
		pkt := stack.NewPacketBuffer(stack.PacketBufferOptions{
			Payload: buffer.MakeWithData(frame),
		})
		gw.ep.InjectInbound(header.IPv4ProtocolNumber, pkt)
		pkt.DecRef()
	}
}

// pumpOutbound drains frames the stack emits and writes them to the socket.
func (gw *Gateway) pumpOutbound() {
	defer gw.wg.Done()
	for {
		pkt := gw.ep.ReadContext(gw.ctx)
		if pkt == nil {
			return
		}
		view := pkt.ToView()
		_, err := gw.conn.Write(view.AsSlice())
		view.Release()
		pkt.DecRef()
		if err != nil {
			gw.cfg.logf("apple-vf egress: write frame: %v", err)
			return
		}
	}
}

// Close stops the gateway.
func (gw *Gateway) Close() {
	gw.stop()
	gw.closeFrameSocket()
	gw.ep.Close()
	gw.stack.Close()
	gw.wg.Wait()
}

func (gw *Gateway) closeFrameSocket() {
	gw.close.Do(func() {
		if gw.conn == nil {
			return
		}
		_ = syscall.Shutdown(int(gw.conn.Fd()), syscall.SHUT_RDWR)
		_ = gw.conn.Close()
	})
}

func addrString(a tcpip.Address) string {
	return net.IP(a.AsSlice()).String()
}

func addrPort(a tcpip.Address, port uint16) netip.AddrPort {
	addr, ok := netip.AddrFromSlice(a.AsSlice())
	if !ok {
		return netip.AddrPort{}
	}
	return netip.AddrPortFrom(addr.Unmap(), port)
}

// newStack builds the userspace network stack with the protocol handlers the
// gateway needs: IPv4, IPv6, and ARP at the network layer; TCP, UDP, and ICMP
// at the transport layer. IPv6 forwarders share the same transport handlers,
// so every v6 TCP/UDP flow crosses the same mediator callbacks as IPv4.
func newStack() *stack.Stack {
	return stack.New(stack.Options{
		NetworkProtocols: []stack.NetworkProtocolFactory{
			ipv4.NewProtocol,
			ipv6.NewProtocol,
			arp.NewProtocol,
		},
		TransportProtocols: []stack.TransportProtocolFactory{
			tcp.NewProtocol,
			udp.NewProtocol,
			icmp.NewProtocol4,
			icmp.NewProtocol6,
		},
	})
}

// stackError adapts a tcpip.Error to the error interface for callers.
type stackError struct {
	op  string
	err tcpip.Error
}

func (e *stackError) Error() string { return e.op + ": " + e.err.String() }

// splice copies bidirectionally between a guest TCP conn and its host conn,
// closing both when either direction ends.
func splice(guest, host net.Conn) {
	var once sync.Once
	closeBoth := func() { once.Do(func() { _ = guest.Close(); _ = host.Close() }) }
	go func() { defer closeBoth(); _, _ = io.Copy(host, guest) }()
	_, _ = io.Copy(guest, host)
	closeBoth()
}

// spliceUDP relays datagrams between a guest UDP flow and its host conn, idling
// out after udpForwardTimeout of inactivity.
func spliceUDP(guest, host net.Conn) {
	var once sync.Once
	closeBoth := func() { once.Do(func() { _ = guest.Close(); _ = host.Close() }) }
	copyOne := func(dst, src net.Conn) {
		defer closeBoth()
		buf := make([]byte, maxFrame)
		for {
			_ = src.SetReadDeadline(time.Now().Add(udpForwardTimeout))
			n, err := src.Read(buf)
			if n > 0 {
				if _, werr := dst.Write(buf[:n]); werr != nil {
					return
				}
			}
			if err != nil {
				return
			}
		}
	}
	go copyOne(host, guest)
	copyOne(guest, host)
}
