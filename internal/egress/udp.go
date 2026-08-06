package egress

import (
	"context"
	"encoding/binary"
	"io"
	"net"
	"net/netip"
	"sync"
	"time"
)

const (
	// maxUDPDatagram is the largest UDP payload (theoretical max 65507; round to
	// 64KiB). Receive buffers are sized to it so a full datagram is never split.
	maxUDPDatagram = 1 << 16
	// udpOOBSize is the recvmsg control buffer. One IP_ORIGDSTADDR cmsg
	// (sockaddr_in) fits comfortably; 1024 leaves slack for kernel padding.
	udpOOBSize = 1024
	// maxUDPFlows bounds the live flow table so a guest spraying datagrams across
	// many distinct (src,origDst) pairs cannot grow it without limit. When full,
	// an arbitrary existing flow is evicted and closed (mirrors the leaf cache's
	// bounded-eviction idiom in ca.go).
	maxUDPFlows = 4096
	// udpFlowIdle is the per-flow idle timeout: a flow with no datagram in either
	// direction for this long is closed and removed.
	udpFlowIdle = 30 * time.Second
	// udpSweepInterval is how often the sweeper scans for idle flows.
	udpSweepInterval = 5 * time.Second
)

// udpFlowKey identifies a UDP flow by guest source and original destination.
// Two distinct guests (or guest ports) hitting the same origDst are independent
// flows; replies must return to the correct guest src.
type udpFlowKey struct {
	src     netip.AddrPort
	origDst netip.AddrPort
}

// HandleUDPConn mediates a single already-accepted UDP guest flow whose
// original destination is known by the caller. It is used by host-owned
// datapaths such as Apple VF host-fd where there is no Linux TPROXY socket, but
// the datapath still has the guest source and original destination from its
// userspace network stack.
func (h *Handler) HandleUDPConn(ctx context.Context, guest net.Conn, src, origDst netip.AddrPort) {
	defer func() { _ = guest.Close() }()
	p := newUDPProxy(h)
	defer p.closeAll()
	p.replyTo = func(replyOrigDst, replyGuestSrc netip.AddrPort, payload []byte) error {
		if replyGuestSrc != src || replyOrigDst.Addr() != origDst.Addr() {
			return nil
		}
		_, err := guest.Write(payload)
		return err
	}
	buf := make([]byte, maxUDPDatagram)
	for {
		_ = guest.SetReadDeadline(time.Now().Add(udpFlowIdle))
		n, err := guest.Read(buf)
		if n > 0 {
			payload := make([]byte, n)
			copy(payload, buf[:n])
			p.handleUDPDatagram(src, origDst, payload)
		}
		if err != nil {
			if ctx.Err() != nil || err == io.EOF || neTimeout(err) {
				return
			}
			return
		}
	}
}

func neTimeout(err error) bool {
	if ne, ok := err.(net.Error); ok && ne.Timeout() {
		return true
	}
	return false
}

// isSTUNDatagram recognizes the RFC 5389/8489 wire header without interpreting
// attributes. The check is strict so random UDP:443 cannot bypass QUIC Initial
// inspection: the message needs the STUN magic cookie, aligned body length, and
// no trailing bytes.
func isSTUNDatagram(payload []byte) bool {
	const (
		stunHeaderLen   = 20
		stunMagicCookie = 0x2112a442
	)
	if len(payload) < stunHeaderLen || payload[0]&0xc0 != 0 {
		return false
	}
	bodyLen := int(binary.BigEndian.Uint16(payload[2:4]))
	return bodyLen%4 == 0 &&
		binary.BigEndian.Uint32(payload[4:8]) == stunMagicCookie &&
		stunHeaderLen+bodyLen == len(payload)
}

// udpFlow is one allowed guest<->upstream UDP destination. Multiple flows from
// one guest source share a udpAssociation so the host exposes exactly the
// source port the guest negotiated.
type udpFlow struct {
	key   udpFlowKey
	host  string
	assoc *udpAssociation

	mu        sync.Mutex
	lastSeen  time.Time
	closeOnce sync.Once
}

// touch records activity so the idle sweeper does not reap an active flow.
func (f *udpFlow) touch(now time.Time) {
	f.mu.Lock()
	f.lastSeen = now
	f.mu.Unlock()
}

// idleSince reports whether the flow has been idle since the given cutoff.
func (f *udpFlow) idleSince(cutoff time.Time) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.lastSeen.Before(cutoff)
}

// udpAssociation is the single unconnected host socket bound to one guest
// source port. The outbound flow table decides which destinations may be sent
// to; the reader accepts return datagrams only from an IP with an active,
// policy-approved flow for this source. Ports are deliberately not required to
// match because protocols such as HomeKit RTP/RTCP negotiate asymmetric ports.
type udpAssociation struct {
	src              netip.AddrPort
	upstream         net.PacketConn
	flows            int
	peers            map[netip.Addr]*udpPeer
	loggedAsymmetric bool
}

type udpPeer struct {
	flows          int
	representative *udpFlow
}

// udpProxy owns the flow table and the injected forward/reply legs. All flow
// forwarding flows through handleUDPDatagram, the testable seam: it takes a
// decoded (src, origDst, payload) so the core is unit-testable without TPROXY
// (no real IP_ORIGDSTADDR cmsg delivery and no root).
type udpProxy struct {
	h       *Handler
	openUDP func(guestSrc, firstOrigDst netip.AddrPort) (net.PacketConn, error)
	replyTo func(origDst, guestSrc netip.AddrPort, payload []byte) error
	// dnsForward performs the resolver round-trip for a guest DNS query (UDP:53).
	// Injectable for tests; defaults (when nil) to defaultDNSForward.
	dnsForward func(resolver netip.AddrPort, query []byte) ([]byte, error)

	idle  time.Duration
	sweep time.Duration

	mu           sync.Mutex
	flows        map[udpFlowKey]*udpFlow
	quic         map[udpFlowKey]*quicInspection
	associations map[netip.AddrPort]*udpAssociation
	stopOnce     sync.Once
	stopped      chan struct{}
	// dnsWG tracks in-flight DNS-forward goroutines so closeAll can wait for them
	// to drain (each one owns a transient resolver socket and a transparent reply
	// socket); it keeps shutdown clean and lets tests observe completion.
	dnsWG sync.WaitGroup
}

// newUDPProxy builds a proxy with default idle/sweep timings, wiring the
// Handler's injected OpenUDP/ReplyTo (defaulting them lazily when nil).
func newUDPProxy(h *Handler) *udpProxy {
	return newUDPProxyWithIdle(h, udpFlowIdle, udpSweepInterval)
}

// newUDPProxyWithIdle is newUDPProxy with explicit idle/sweep timings (tests use
// short ones). It starts the background idle sweeper.
func newUDPProxyWithIdle(h *Handler, idle, sweep time.Duration) *udpProxy {
	open := h.OpenUDP
	var openAssoc func(netip.AddrPort, netip.AddrPort) (net.PacketConn, error)
	switch {
	case open != nil:
		openAssoc = func(src, _ netip.AddrPort) (net.PacketConn, error) {
			return open(src)
		}
	case h.DialUDP != nil:
		openAssoc = func(_, origDst netip.AddrPort) (net.PacketConn, error) {
			conn, err := h.DialUDP(origDst)
			if err != nil {
				return nil, err
			}
			return connectedPacketConn{Conn: conn, peer: origDst}, nil
		}
	default:
		openAssoc = func(src, origDst netip.AddrPort) (net.PacketConn, error) {
			return defaultOpenUDP(src, origDst)
		}
	}
	reply := h.ReplyTo
	if reply == nil {
		reply = transparentReply // platform impl (Linux real, others error stub)
	}
	p := &udpProxy{
		h:            h,
		openUDP:      openAssoc,
		replyTo:      reply,
		dnsForward:   defaultDNSForward,
		idle:         idle,
		sweep:        sweep,
		flows:        make(map[udpFlowKey]*udpFlow),
		quic:         make(map[udpFlowKey]*quicInspection),
		associations: make(map[netip.AddrPort]*udpAssociation),
		stopped:      make(chan struct{}),
	}
	go p.sweeper()
	return p
}

// defaultOpenUDP opens one unconnected UDP socket while preserving the guest
// socket's source port. UDP application protocols commonly negotiate a return
// endpoint out of band. Rebinding the source port here makes the peer's replies
// target a port the guest never opened, even though the forward leg is allowed.
// Leaving the socket unconnected permits asymmetric protocols to return from a
// different source port; readUpstreamAssociation still rejects every peer IP
// without an active, policy-approved outbound flow.
//
// One association owns each guest source, so SO_REUSEADDR is neither needed nor
// desirable. A real host-port collision fails closed: the caller audits
// egress_udp_dial_error and sends no datagram.
func defaultOpenUDP(guestSrc, origDst netip.AddrPort) (net.PacketConn, error) {
	network := "udp4"
	bindIP := net.IPv4zero
	if origDst.Addr().Is6() {
		network = "udp6"
		bindIP = net.IPv6unspecified
	}
	return net.ListenUDP(network, &net.UDPAddr{IP: bindIP, Port: int(guestSrc.Port())})
}

// connectedPacketConn adapts the legacy DialUDP test seam to the association
// interface. A connected fixture already fixes its peer, so WriteTo's address
// is intentionally ignored and ReadFrom reports RemoteAddr.
type connectedPacketConn struct {
	net.Conn
	peer netip.AddrPort
}

func (c connectedPacketConn) ReadFrom(p []byte) (int, net.Addr, error) {
	n, err := c.Read(p)
	return n, net.UDPAddrFromAddrPort(c.peer), err
}

func (c connectedPacketConn) WriteTo(p []byte, _ net.Addr) (int, error) {
	return c.Write(p)
}

// dnsForwardTimeout bounds the synchronous resolver round-trip so a slow or
// silent resolver cannot wedge the DNS handling of one datagram indefinitely.
const dnsForwardTimeout = 5 * time.Second

// defaultDNSForward is the production resolver round-trip used by handleDNS: dial
// the resolver the guest targeted, write the query, and read the single response
// within dnsForwardTimeout. The mediator dialing the real resolver is
// host-originated, so it is not re-captured by the tap REDIRECT, and the loop
// guard covers the self-addr case before we ever get here.
func defaultDNSForward(resolver netip.AddrPort, query []byte) ([]byte, error) {
	network := "udp4"
	if resolver.Addr().Is6() {
		network = "udp6"
	}
	conn, err := net.DialUDP(network, nil, net.UDPAddrFromAddrPort(resolver))
	if err != nil {
		return nil, err
	}
	defer func() { _ = conn.Close() }()
	if err := conn.SetDeadline(time.Now().Add(dnsForwardTimeout)); err != nil {
		return nil, err
	}
	if _, err := conn.Write(query); err != nil {
		return nil, err
	}
	buf := make([]byte, maxUDPDatagram)
	n, err := conn.Read(buf)
	if err != nil {
		return nil, err
	}
	return buf[:n], nil
}

// serveUDP runs the TPROXY receive loop on a transparent UDP socket: it decodes
// each datagram's guest source and original destination (recovered from the
// IP_ORIGDSTADDR ancillary message via parseUDPOrigDst, since the TPROXY rule
// steers traffic without rewriting the destination) and hands it to the proxy's
// per-datagram seam. It returns when conn is closed.
func serveUDP(conn *net.UDPConn, h *Handler) {
	p := newUDPProxy(h)
	defer p.closeAll()

	buf := make([]byte, maxUDPDatagram)
	oob := make([]byte, udpOOBSize)
	for {
		n, oobn, _, src, err := conn.ReadMsgUDP(buf, oob)
		if err != nil {
			return // socket closed or fatal read error: stop the loop
		}
		srcAP := src.AddrPort()
		origDst, err := parseUDPOrigDst(oob[:oobn])
		if err != nil {
			// Without the original destination we cannot enforce policy or
			// forward correctly; drop fail-closed and audit the parse failure.
			h.Logger.Log("egress_udp_origdst_error", map[string]any{
				"src":   srcAP.String(),
				"error": err.Error(),
			})
			continue
		}
		// Copy the payload: buf is reused on the next read, but the flow's
		// forward and the reader goroutine may outlive this iteration.
		payload := make([]byte, n)
		copy(payload, buf[:n])
		p.handleUDPDatagram(srcAP, origDst, payload)
	}
}

// handleUDPDatagram is the testable core: enforce policy, look up or create the
// flow, and forward the payload upstream. It is the seam factored out of the
// ReadMsgUDP loop so the proxy can be unit-tested with synthesized
// (src, origDst, payload) tuples — no TPROXY, no cmsg delivery, no root.
func (p *udpProxy) handleUDPDatagram(src, origDst netip.AddrPort, payload []byte) {
	// Loop guard (mirrors the TCP path): a datagram whose recovered original
	// destination is the mediator's own bind address is the mediator folding back
	// on itself. Drop + audit instead of opening an upstream socket to ourselves.
	if p.h.isOwnBindAddr(origDst) {
		p.h.Logger.Log("egress_loop_guard", map[string]any{"dst": origDst.String(), "proto": "udp"})
		return
	}
	// DNS (UDP:53) is a one-shot request/response handled by the filtering
	// resolver-forwarder, never a flow: forwarding it through the normal flow path
	// would let DNS tunnel out unfiltered and leak a flow per query. handleDNS
	// enforces the strict hostname allowlist, forwards permitted queries, caches
	// name->IP mappings (so later UDP/raw-IP flows can be policed by hostname), and
	// has already audited the outcome. We reply with the spoofed source = the
	// resolver the guest targeted (origDst) so its stub resolver accepts the answer.
	if origDst.Port() == 53 {
		// Forward the query in its own goroutine: handleDNS does a blocking resolver
		// round-trip (up to dnsForwardTimeout), and the serveUDP receive loop is
		// single-threaded. Doing it inline stalls the loop for the duration of every
		// query — and because a guest's resolver fires several queries back to back
		// (A + AAAA, often to multiple resolvers in resolv.conf), serializing them in
		// the loop holds the host's network stack busy long enough that the guest's
		// concurrent upstream TCP connection (the actual fetch) never receives its
		// response and times out at 0 bytes. Per-datagram goroutines decouple each
		// query so DNS forwarding never stalls other UDP datagrams or the wider
		// egress path. payload is already a per-datagram copy (serveUDP) so the
		// goroutine owns it safely; dnsWG lets closeAll drain in-flight forwards.
		p.dnsWG.Add(1)
		go func() {
			defer p.dnsWG.Done()
			p.serveDNS(src, origDst, payload)
		}()
		return
	}
	key := udpFlowKey{src: src, origDst: origDst}
	host := origDst.Addr().String()
	if p.h.NameCache != nil {
		if name, ok := p.h.NameCache.HostForIP(origDst.Addr()); ok {
			host = name
		}
	}

	stunUDP443 := origDst.Port() == 443 && isSTUNDatagram(payload)
	if origDst.Port() == 443 && !stunUDP443 {
		p.mu.Lock()
		if flow := p.flows[key]; flow != nil {
			host = flow.host
			p.mu.Unlock()
			p.forwardUDPDatagram(src, origDst, payload, host, "quic")
			return
		}
		inspection := p.quic[key]
		if inspection == nil {
			if len(p.quic) >= maxQUICInspections {
				for stale := range p.quic {
					delete(p.quic, stale)
					break
				}
			}
			inspection = &quicInspection{}
			p.quic[key] = inspection
		}
		quicHost, complete, err := inspection.add(payload)
		if err != nil {
			delete(p.quic, key)
			p.mu.Unlock()
			p.h.Logger.Log("egress_udp_deny", map[string]any{
				"host": host, "dst": origDst.String(), "reason": err.Error(),
				"signal": SignalQUICUDP443,
			})
			return
		}
		if !complete {
			p.mu.Unlock()
			return
		}
		buffered := inspection.buffered
		delete(p.quic, key)
		p.mu.Unlock()
		for _, datagram := range buffered {
			p.forwardUDPDatagram(src, origDst, datagram, quicHost, "quic")
		}
		return
	}
	protocol := ""
	if stunUDP443 {
		protocol = "stun"
	}
	p.forwardUDPDatagram(src, origDst, payload, host, protocol)
}

func (p *udpProxy) forwardUDPDatagram(src, origDst netip.AddrPort, payload []byte, host, protocol string) {
	dst := origDst.String()
	nameDestinationMismatch := protocol == "quic" && (p.h.NameCache == nil || !p.h.NameCache.HostMatchesIP(host, origDst.Addr()))

	// Same decision sequence as the TCP path and the wasm sandbox, via the shared
	// Brain.Evaluate: the default-deny allowlist plus the inside-deny on the
	// resolved destination IP (so SNI/hostname spoofing cannot bypass it). UDP has
	// no passthrough/peer concept, so candidates is nil and passthrough is false;
	// the deny audit keeps the UDP-specific event names.
	v := p.h.brain().Evaluate(host, nil, origDst.Addr(), false)
	if p.h.AllowlistLocked && nameDestinationMismatch {
		v.Allowed = false
		v.Unlisted = false
		v.Reason = "asserted host not bound to destination"
	}
	allowed := v.Allowed
	unlisted := v.Unlisted // not explicitly on the allowlist (allow-broad grant)

	if !allowed {
		// fail-closed: no upstream socket, drop the datagram.
		event := "egress_udp_deny"
		reason := v.Reason
		if v.Inside {
			event = "egress_udp_internal_deny"
			reason = "inside: internal destination denied"
		}
		fields := map[string]any{
			"host":     host,
			"dst":      dst,
			"reason":   reason,
			"internal": v.Inside,
			"signal":   SignalDenied,
		}
		if nameDestinationMismatch {
			fields["signal"] = SignalNameDestinationMismatch
		}
		p.h.Logger.Log(event, fields)
		return
	}

	now := time.Now()
	key := udpFlowKey{src: src, origDst: origDst}

	p.mu.Lock()
	flow, ok := p.flows[key]
	if !ok {
		assoc := p.associations[src]
		if assoc == nil {
			up, err := p.openUDP(src, origDst)
			if err != nil {
				p.mu.Unlock()
				p.h.Logger.Log("egress_udp_dial_error", map[string]any{
					"host": host, "dst": dst, "src": src.String(), "error": err.Error(),
				})
				return
			}
			assoc = &udpAssociation{
				src:      src,
				upstream: up,
				peers:    make(map[netip.Addr]*udpPeer),
			}
			p.associations[src] = assoc
			go p.readUpstreamAssociation(assoc)
		}
		flow = &udpFlow{
			key:      key,
			host:     host,
			assoc:    assoc,
			lastSeen: now,
		}
		p.flows[key] = flow
		p.addFlowToAssociationLocked(flow)
		evicted, closedAssoc := p.evictIfOverfullLocked(key)

		allowFields := map[string]any{
			"host": host,
			"dst":  dst,
			"src":  src.String(),
		}
		if local := flow.assoc.upstream.LocalAddr(); local != nil {
			allowFields["upstream_src"] = local.String()
		}
		if unlisted {
			allowFields["unlisted"] = true
		}
		if protocol != "" {
			allowFields["protocol"] = protocol
		}
		if nameDestinationMismatch {
			allowFields["signal"] = SignalNameDestinationMismatch
		}
		p.h.Logger.Log("egress_udp_allow", allowFields)
		p.mu.Unlock()
		if evicted != nil {
			p.closeFlow(evicted, "evicted")
		}
		if closedAssoc != nil {
			_ = closedAssoc.upstream.Close()
		}
	} else {
		p.mu.Unlock()
	}

	flow.touch(now)
	// Volume cap (ASK tenet 8): charge this datagram against the SAME process-wide
	// cumulative counter the TCP splice uses, so MaxTotalBytes bounds total egress
	// across tcp+udp. Once exceeded, tear the breaching flow down (drop the
	// datagram, close the upstream socket) and audit egress_cap_exceeded volume.
	// The mediator keeps serving other flows; no NEW bytes escape this flow. A
	// zero cap (unlimited) never trips, so the path is byte-identical to today.
	if p.h.addBytesOverCap(int64(len(payload))) {
		p.h.Logger.Log("egress_cap_exceeded", map[string]any{
			"host": host, "dst": dst, "proto": "udp", "reason": "volume",
			"limit": p.h.Limits.MaxTotalBytes,
		})
		p.removeFlow(flow)
		return
	}
	if _, err := flow.assoc.upstream.WriteTo(payload, net.UDPAddrFromAddrPort(origDst)); err != nil {
		// Upstream write failure: tear the destination flow down. When it was
		// the last flow for this guest source, removeFlow also closes the shared
		// association so the next datagram can create a clean socket.
		p.removeFlow(flow)
	}
}

// serveDNS forwards one guest DNS query (UDP:53) through the filtering resolver
// and replies to the guest. It is run in its own goroutine (handleUDPDatagram
// dispatches it) so the blocking resolver round-trip in handleDNS never stalls
// the single-threaded serveUDP receive loop. handleDNS enforces the strict
// hostname allowlist, forwards permitted queries, caches name->IP mappings, and
// has already audited the outcome. The reply is sent with the spoofed source =
// the resolver the guest targeted (origDst) so its stub resolver accepts it.
func (p *udpProxy) serveDNS(src, origDst netip.AddrPort, query []byte) {
	resp, err := p.h.handleDNS(query, origDst, p.dnsForward)
	if err != nil {
		return // handleDNS already audited egress_dns_error; drop fail-closed
	}
	if resp == nil {
		return
	}
	// A reply failure means the guest never sees an answer the audit trail says
	// was allowed and forwarded — it must leave its own trace (this exact gap
	// hid the EADDRINUSE reply-bind collision on hosts where pasta mirrors a
	// host UDP :53 listener into the netns). Mirrors readUpstream's
	// egress_udp_reply_error.
	if rerr := p.replyTo(origDst, src, resp); rerr != nil {
		p.h.Logger.Log("egress_dns_reply_error", map[string]any{
			"resolver": origDst.String(),
			"src":      src.String(),
			"error":    rerr.Error(),
		})
	}
}

// evictIfOverfullLocked drops one arbitrary flow other than keep after a new
// flow makes the table exceed its bound. Inserting before eviction ensures a
// shared association cannot be closed when keep uses the same guest source.
// Caller holds p.mu. The caller closes returned resources after unlocking.
func (p *udpProxy) evictIfOverfullLocked(keep udpFlowKey) (*udpFlow, *udpAssociation) {
	if len(p.flows) <= maxUDPFlows {
		return nil, nil
	}
	for k, victim := range p.flows {
		if k == keep {
			continue
		}
		delete(p.flows, k)
		p.removeFlowFromAssociationLocked(victim)
		return victim, p.removeAssociationIfUnusedLocked(victim.key.src)
	}
	return nil, nil
}

// readUpstreamAssociation pumps datagrams from one guest-source association
// back to the guest. An exact active destination tuple is preferred, but a
// different source port on an active peer IP is also a valid stateful reply:
// RTP/RTCP and similar protocols negotiate asymmetric send/receive ports.
// Traffic from every other IP is dropped and audited.
func (p *udpProxy) readUpstreamAssociation(assoc *udpAssociation) {
	buf := make([]byte, maxUDPDatagram)
	for {
		n, fromAddr, err := assoc.upstream.ReadFrom(buf)
		if err != nil {
			return // association closed after its final flow is removed
		}
		from, ok := addrPortFromNetAddr(fromAddr)
		if !ok {
			p.h.Logger.Log("egress_udp_reply_deny", map[string]any{
				"src": assoc.src.String(), "peer": fromAddr.String(),
				"reason": "unrecognized reply address", "signal": SignalDenied,
			})
			continue
		}

		p.mu.Lock()
		if p.associations[assoc.src] != assoc {
			p.mu.Unlock()
			return
		}
		flow := p.replyFlowLocked(assoc, from)
		logAsymmetric := false
		if flow != nil {
			flow.touch(time.Now())
			if from.Port() != flow.key.origDst.Port() && !assoc.loggedAsymmetric {
				assoc.loggedAsymmetric = true
				logAsymmetric = true
			}
		}
		p.mu.Unlock()
		if flow == nil {
			p.h.Logger.Log("egress_udp_reply_deny", map[string]any{
				"src": assoc.src.String(), "peer": from.String(),
				"reason": "reply peer has no active allowed flow", "signal": SignalDenied,
			})
			continue
		}
		if logAsymmetric {
			p.h.Logger.Log("egress_udp_reply_port_change", map[string]any{
				"src": assoc.src.String(), "dst": flow.key.origDst.String(),
				"peer": from.String(),
			})
		}

		if err := p.replyTo(from, assoc.src, buf[:n]); err != nil {
			p.h.Logger.Log("egress_udp_reply_error", map[string]any{
				"host": flow.host, "dst": flow.key.origDst.String(),
				"peer": from.String(), "error": err.Error(),
			})
		}
	}
}

func addrPortFromNetAddr(addr net.Addr) (netip.AddrPort, bool) {
	switch a := addr.(type) {
	case *net.UDPAddr:
		return a.AddrPort(), true
	default:
		ap, err := netip.ParseAddrPort(addr.String())
		return ap, err == nil
	}
}

// replyFlowLocked returns an active flow that authorizes a reply from peer.
// Exact endpoint matches win; otherwise an active flow to the same IP grants
// the asymmetric-port response. Caller holds p.mu.
func (p *udpProxy) replyFlowLocked(assoc *udpAssociation, peer netip.AddrPort) *udpFlow {
	if flow := p.flows[udpFlowKey{src: assoc.src, origDst: peer}]; flow != nil {
		return flow
	}
	if active := assoc.peers[peer.Addr()]; active != nil {
		return active.representative
	}
	return nil
}

// addFlowToAssociationLocked records a new policy-approved destination in its
// source association. Caller holds p.mu.
func (p *udpProxy) addFlowToAssociationLocked(flow *udpFlow) {
	assoc := flow.assoc
	assoc.flows++
	peer := assoc.peers[flow.key.origDst.Addr()]
	if peer == nil {
		peer = &udpPeer{}
		assoc.peers[flow.key.origDst.Addr()] = peer
	}
	peer.flows++
	peer.representative = flow
}

// removeFlowFromAssociationLocked removes one destination from its source
// association. Re-selecting a representative can scan the bounded flow table,
// but the per-datagram reply path remains O(1). Caller holds p.mu and has
// already removed flow from p.flows.
func (p *udpProxy) removeFlowFromAssociationLocked(flow *udpFlow) {
	assoc := flow.assoc
	assoc.flows--
	addr := flow.key.origDst.Addr()
	peer := assoc.peers[addr]
	if peer == nil {
		return
	}
	peer.flows--
	if peer.flows == 0 {
		delete(assoc.peers, addr)
		return
	}
	if peer.representative != flow {
		return
	}
	peer.representative = nil
	for _, candidate := range p.flows {
		if candidate.assoc == assoc && candidate.key.origDst.Addr() == addr {
			peer.representative = candidate
			return
		}
	}
}

// removeFlow removes a flow from the table (if still present) and closes it.
func (p *udpProxy) removeFlow(flow *udpFlow) {
	p.mu.Lock()
	var assoc *udpAssociation
	if cur, ok := p.flows[flow.key]; ok && cur == flow {
		delete(p.flows, flow.key)
		p.removeFlowFromAssociationLocked(flow)
		assoc = p.removeAssociationIfUnusedLocked(flow.key.src)
	}
	p.mu.Unlock()
	p.closeFlow(flow, "")
	if assoc != nil {
		_ = assoc.upstream.Close()
	}
}

// removeAssociationIfUnusedLocked removes and returns src's association when no
// flow still references it. Caller holds p.mu.
func (p *udpProxy) removeAssociationIfUnusedLocked(src netip.AddrPort) *udpAssociation {
	assoc := p.associations[src]
	if assoc == nil || assoc.flows != 0 {
		return nil
	}
	delete(p.associations, src)
	return assoc
}

// closeFlow audits one removed destination flow. Association sockets are closed
// separately, after the final flow for their guest source disappears.
func (p *udpProxy) closeFlow(flow *udpFlow, reason string) {
	flow.closeOnce.Do(func() {
		fields := map[string]any{"host": flow.host, "dst": flow.key.origDst.String()}
		if reason != "" {
			fields["reason"] = reason
		}
		p.h.Logger.Log("egress_udp_close", fields)
	})
}

// sweeper periodically reaps idle flows until the proxy is stopped.
func (p *udpProxy) sweeper() {
	t := time.NewTicker(p.sweep)
	defer t.Stop()
	for {
		select {
		case <-p.stopped:
			return
		case <-t.C:
			p.reapIdle()
		}
	}
}

// reapIdle closes and removes every flow idle longer than p.idle.
func (p *udpProxy) reapIdle() {
	cutoff := time.Now().Add(-p.idle)
	p.mu.Lock()
	var dead []*udpFlow
	var associations []*udpAssociation
	for k, flow := range p.flows {
		if flow.idleSince(cutoff) {
			dead = append(dead, flow)
			delete(p.flows, k)
			p.removeFlowFromAssociationLocked(flow)
		}
	}
	for key, inspection := range p.quic {
		if inspection.lastSeen.Before(cutoff) {
			delete(p.quic, key)
		}
	}
	for src, assoc := range p.associations {
		if unused := p.removeAssociationIfUnusedLocked(src); unused != nil {
			associations = append(associations, assoc)
		}
	}
	p.mu.Unlock()
	for _, flow := range dead {
		p.closeFlow(flow, "idle")
	}
	for _, assoc := range associations {
		_ = assoc.upstream.Close()
	}
}

// closeAll stops the sweeper and closes every live flow. Safe to call once when
// serveUDP returns (or from tests via defer); idempotent for the sweeper stop.
func (p *udpProxy) closeAll() {
	p.stopOnce.Do(func() { close(p.stopped) })
	p.mu.Lock()
	flows := make([]*udpFlow, 0, len(p.flows))
	associations := make([]*udpAssociation, 0, len(p.associations))
	for k, flow := range p.flows {
		flows = append(flows, flow)
		delete(p.flows, k)
	}
	for src, assoc := range p.associations {
		associations = append(associations, assoc)
		delete(p.associations, src)
	}
	for key := range p.quic {
		delete(p.quic, key)
	}
	p.mu.Unlock()
	for _, flow := range flows {
		p.closeFlow(flow, "shutdown")
	}
	for _, assoc := range associations {
		_ = assoc.upstream.Close()
	}
	// Drain in-flight DNS-forward goroutines so their transient sockets are closed
	// before the proxy is considered shut down.
	p.dnsWG.Wait()
}

// flowCount returns the number of live flows (test helper).
func (p *udpProxy) flowCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.flows)
}
