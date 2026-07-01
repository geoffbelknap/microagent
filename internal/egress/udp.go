package egress

import (
	"context"
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
		if replyOrigDst != origDst || replyGuestSrc != src {
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

// udpFlow is one live guest<->upstream UDP association. The upstream conn
// carries datagrams to/from origDst; a single reader goroutine pumps upstream
// replies back to the guest via the proxy's ReplyTo (spoofing source=origDst).
type udpFlow struct {
	key      udpFlowKey
	host     string
	upstream net.Conn

	mu       sync.Mutex
	lastSeen time.Time
	closed   bool
	done     chan struct{} // closed to stop the reader goroutine
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

// udpProxy owns the flow table and the injected forward/reply legs. All flow
// forwarding flows through handleUDPDatagram, the testable seam: it takes a
// decoded (src, origDst, payload) so the core is unit-testable without TPROXY
// (no real IP_ORIGDSTADDR cmsg delivery and no root).
type udpProxy struct {
	h       *Handler
	dialUDP func(origDst netip.AddrPort) (net.Conn, error)
	replyTo func(origDst, guestSrc netip.AddrPort, payload []byte) error
	// dnsForward performs the resolver round-trip for a guest DNS query (UDP:53).
	// Injectable for tests; defaults (when nil) to defaultDNSForward.
	dnsForward func(resolver netip.AddrPort, query []byte) ([]byte, error)

	idle  time.Duration
	sweep time.Duration

	mu       sync.Mutex
	flows    map[udpFlowKey]*udpFlow
	stopOnce sync.Once
	stopped  chan struct{}
	// dnsWG tracks in-flight DNS-forward goroutines so closeAll can wait for them
	// to drain (each one owns a transient resolver socket and a transparent reply
	// socket); it keeps shutdown clean and lets tests observe completion.
	dnsWG sync.WaitGroup
}

// newUDPProxy builds a proxy with default idle/sweep timings, wiring the
// Handler's injected DialUDP/ReplyTo (defaulting them lazily when nil).
func newUDPProxy(h *Handler) *udpProxy {
	return newUDPProxyWithIdle(h, udpFlowIdle, udpSweepInterval)
}

// newUDPProxyWithIdle is newUDPProxy with explicit idle/sweep timings (tests use
// short ones). It starts the background idle sweeper.
func newUDPProxyWithIdle(h *Handler, idle, sweep time.Duration) *udpProxy {
	dial := h.DialUDP
	if dial == nil {
		dial = defaultDialUDP
	}
	reply := h.ReplyTo
	if reply == nil {
		reply = transparentReply // platform impl (Linux real, others error stub)
	}
	p := &udpProxy{
		h:          h,
		dialUDP:    dial,
		replyTo:    reply,
		dnsForward: defaultDNSForward,
		idle:       idle,
		sweep:      sweep,
		flows:      make(map[udpFlowKey]*udpFlow),
		stopped:    make(chan struct{}),
	}
	go p.sweeper()
	return p
}

// defaultDialUDP opens a connected UDP socket to origDst (production default for
// the upstream leg when no DialUDP is injected).
func defaultDialUDP(origDst netip.AddrPort) (net.Conn, error) {
	return net.DialUDP("udp4", nil, net.UDPAddrFromAddrPort(origDst))
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
	conn, err := net.DialUDP("udp4", nil, net.UDPAddrFromAddrPort(resolver))
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
	// Resolve the destination IP back to the hostname the guest looked up (reverse
	// lookup against names the mediator's DNS forwarder has vended) so the allowlist
	// can match by hostname. An uncached/unlisted IP keeps the bare-IP host, which
	// strict denies. nil-guarded: callers without a NameCache fall back to the IP.
	host := origDst.Addr().String()
	if p.h.NameCache != nil {
		if name, ok := p.h.NameCache.HostForIP(origDst.Addr()); ok {
			host = name
		}
	}

	// Same decision sequence as the TCP path and the wasm sandbox, via the shared
	// Brain.Evaluate: the default-deny allowlist plus the guarded inside-deny on
	// the resolved destination IP (so SNI/hostname spoofing cannot bypass it).
	// UDP has no passthrough/peer concept, so candidates is nil and passthrough is
	// false; the deny audit keeps the UDP-specific event names.
	v := p.h.brain().Evaluate(host, nil, origDst.Addr(), false)
	allowed := v.Allowed
	unlisted := v.Unlisted // not explicitly on the allowlist (guarded-public grant)

	dst := origDst.String()
	if !allowed {
		// fail-closed: no upstream socket, drop the datagram.
		event := "egress_udp_deny"
		reason := v.Reason
		if v.Inside {
			event = "egress_udp_internal_deny"
			reason = "guarded: internal destination denied"
		}
		p.h.Logger.Log(event, map[string]any{
			"host":     host,
			"dst":      dst,
			"reason":   reason,
			"internal": v.Inside,
		})
		return
	}

	now := time.Now()
	key := udpFlowKey{src: src, origDst: origDst}

	p.mu.Lock()
	flow, ok := p.flows[key]
	if !ok {
		up, err := p.dialUDP(origDst)
		if err != nil {
			p.mu.Unlock()
			p.h.Logger.Log("egress_udp_dial_error", map[string]any{
				"host": host, "dst": dst, "error": err.Error(),
			})
			return
		}
		p.evictIfFullLocked()
		flow = &udpFlow{
			key:      key,
			host:     host,
			upstream: up,
			lastSeen: now,
			done:     make(chan struct{}),
		}
		p.flows[key] = flow
		go p.readUpstream(flow)

		allowFields := map[string]any{"host": host, "dst": dst}
		if unlisted {
			allowFields["unlisted"] = true
		}
		p.h.Logger.Log("egress_udp_allow", allowFields)
	}
	p.mu.Unlock()

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
	if _, err := flow.upstream.Write(payload); err != nil {
		// Upstream write failure: tear the flow down so a fresh one is created
		// on the next datagram (and the reader goroutine is reclaimed).
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
	if resp != nil {
		_ = p.replyTo(origDst, src, resp)
	}
}

// evictIfFullLocked drops one arbitrary flow when the table is at capacity, so
// inserting the new flow keeps the table bounded. Caller holds p.mu.
func (p *udpProxy) evictIfFullLocked() {
	if len(p.flows) < maxUDPFlows {
		return
	}
	for k, victim := range p.flows {
		delete(p.flows, k)
		p.closeFlow(victim, "evicted")
		break
	}
}

// readUpstream pumps datagrams from the flow's upstream socket back to the guest
// src with source spoofed to origDst (via replyTo). It exits when the flow's
// upstream is closed (Read errors) — which removeFlow/closeFlow trigger.
func (p *udpProxy) readUpstream(flow *udpFlow) {
	buf := make([]byte, maxUDPDatagram)
	for {
		n, err := flow.upstream.Read(buf)
		if err != nil {
			return // upstream closed (by idle sweep, eviction, or write-failure teardown)
		}
		flow.touch(time.Now())
		if err := p.replyTo(flow.key.origDst, flow.key.src, buf[:n]); err != nil {
			p.h.Logger.Log("egress_udp_reply_error", map[string]any{
				"host": flow.host, "dst": flow.key.origDst.String(), "error": err.Error(),
			})
			// Keep the flow: a transient reply failure should not orphan an
			// otherwise-live association; the idle sweeper still bounds it.
		}
	}
}

// removeFlow removes a flow from the table (if still present) and closes it.
func (p *udpProxy) removeFlow(flow *udpFlow) {
	p.mu.Lock()
	if cur, ok := p.flows[flow.key]; ok && cur == flow {
		delete(p.flows, flow.key)
	}
	p.mu.Unlock()
	p.closeFlow(flow, "")
}

// closeFlow closes a flow's upstream socket, stops its reader, and (when reason
// is non-empty) audits egress_udp_close. It is idempotent.
func (p *udpProxy) closeFlow(flow *udpFlow, reason string) {
	flow.mu.Lock()
	if flow.closed {
		flow.mu.Unlock()
		return
	}
	flow.closed = true
	close(flow.done)
	flow.mu.Unlock()

	_ = flow.upstream.Close() // unblocks readUpstream's Read
	fields := map[string]any{"host": flow.host, "dst": flow.key.origDst.String()}
	if reason != "" {
		fields["reason"] = reason
	}
	p.h.Logger.Log("egress_udp_close", fields)
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
	for k, flow := range p.flows {
		if flow.idleSince(cutoff) {
			dead = append(dead, flow)
			delete(p.flows, k)
		}
	}
	p.mu.Unlock()
	for _, flow := range dead {
		p.closeFlow(flow, "idle")
	}
}

// closeAll stops the sweeper and closes every live flow. Safe to call once when
// serveUDP returns (or from tests via defer); idempotent for the sweeper stop.
func (p *udpProxy) closeAll() {
	p.stopOnce.Do(func() { close(p.stopped) })
	p.mu.Lock()
	flows := make([]*udpFlow, 0, len(p.flows))
	for k, flow := range p.flows {
		flows = append(flows, flow)
		delete(p.flows, k)
	}
	p.mu.Unlock()
	for _, flow := range flows {
		p.closeFlow(flow, "shutdown")
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
