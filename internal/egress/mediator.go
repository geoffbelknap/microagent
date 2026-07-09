package egress

import (
	"bufio"
	"context"
	"crypto/x509"
	"io"
	"net"
	"net/netip"
	"sync/atomic"
	"time"

	"golang.org/x/time/rate"
)

// Limits bounds a mediator process's egress per ASK tenet 8 (operations are
// bounded). All caps are per-mediator-process — i.e. per-workspace, since each
// mediated workspace runs its own mediator — and reset on restart. A zero value
// for any field means unlimited (the current, uncapped behavior), so the zero
// Limits is byte-identical to today.
//
// Recorded decision: caps are per-mediator-process and reset on restart;
// persistent cross-restart volume accounting is intentionally out of scope.
type Limits struct {
	// MaxBytesPerSec rate-limits the upstream-bound (guest->upstream) copy of each
	// flow. 0 = unlimited.
	MaxBytesPerSec int64
	// MaxTotalBytes caps the cumulative bytes the process forwards upstream across
	// BOTH the TCP splice AND the UDP forward (one process-wide counter). Once the
	// counter exceeds this, the breaching flow is torn down and audited
	// egress_cap_exceeded reason "volume"; the mediator keeps serving. 0 = unlimited.
	MaxTotalBytes int64
	// MaxConcurrentConns caps the number of concurrently mediated TCP connections.
	// A connection that would exceed it is refused fail-closed before dialing
	// upstream (egress_cap_exceeded reason "concurrency"). 0 = unlimited.
	MaxConcurrentConns int32
}

// maxTLSRecord is the largest TLS record (2^14 plaintext + 5-byte header). The
// capture reader is sized to it so sniffHost can peek a full ClientHello and
// extract SNI rather than silently falling back to the destination IP.
const maxTLSRecord = 1<<14 + 5

// Handler mediates one redirected connection: recover the original
// destination, sniff the host, enforce the allowlist, and forward or deny
// fail-closed. OrigDst and Dial are injectable for tests.
type Handler struct {
	// Mode selects enforcement: "guarded" (default) denies link-local/metadata/RFC1918/ULA/loopback/CGNAT/east-west on
	// resolved IP while allowing public internet; "strict" denies non-allowlisted destinations fail-closed; empty
	// normalizes to "guarded".
	Mode          string
	Policy        *Policy
	Passthrough   *Policy
	CA            *CA
	UpstreamRoots *x509.CertPool

	// Swaps is the host-indexed credential-swap table loaded from
	// Options.SwapConfigPath. When non-nil, serveMITM HTTP-parses any
	// intercepted connection whose SNI matches an entry and injects the acquired
	// credential into each request (fail-closed). Nil when no swap config is
	// configured, in which case the request path is byte-identical to today.
	Swaps *SwapTable

	// Resolver dereferences secret refs ("env:EXAMPLE_KEY") for credential
	// swaps. Phase 3 wires the real KeyResolver; until then it is nil and any
	// live swap fails closed in Swapper.acquire (the static unit test injects a
	// fake resolver directly). Carried so serveMITM can build a Swapper.
	Resolver resolver
	// tokenCache backs the (later-phase) expiring credential strategies. Built by
	// Serve when a swap table is loaded; nil otherwise. The static strategy does
	// not use it.
	tokenCache *tokenCache

	Logger       Logger
	OrigDst      func(net.Conn) (netip.AddrPort, error)
	Dial         func(network, addr string) (net.Conn, error)
	SniffTimeout time.Duration

	// NameCache records DNS answer name->IP mappings observed by handleDNS (the
	// filtering DNS forwarder) so later UDP/raw-IP flows can be policed by the
	// hostname the guest resolved (reverse lookup of the flow's destination IP).
	// handleDNS and the future reverse-lookup policy share this one cache. Nil is
	// tolerated by handleDNS (it simply does not cache); production wiring sets it.
	NameCache *NameCache

	// Peers is the static name↔IP roster of a named network's members (built from
	// the network record, excluding this workspace's own entry). East-west VM↔VM
	// flows are often raw TCP or dialed by peer name → peer IP — no DNS the
	// mediator can observe — so a bare-IP destination is reverse-resolved here to
	// the peer's workspace name and policed by name under the same default-deny
	// allowlist as any external host. Authoritative (no expiry) and tried ahead of
	// the DNS NameCache for bare-IP destinations. Nil for the nat/user paths (no
	// roster); peerName nil-guards it.
	Peers *PeerCache

	// BindAddr is the mediator's own listen address (gateway:port). It is the
	// loop-guard reference: a captured connection or datagram whose recovered
	// original destination equals BindAddr is the mediator's own forwarding leg
	// (or a probe to the listener) folding back on itself — never genuine guest
	// egress. Forwarding it would make the mediator dial itself, accept, recover
	// the same address, and dial again, an unbounded self-loop. When BindAddr is
	// set (non-zero) such a destination is dropped and audited egress_loop_guard
	// instead of dialed. The zero value disables the guard (unit tests that do not
	// exercise it leave it unset).
	BindAddr netip.AddrPort

	// DialUDP opens the upstream leg of a UDP flow to origDst. Injectable for
	// tests; defaults (when nil) to a plain net.DialUDP to origDst. See udp.go.
	DialUDP func(origDst netip.AddrPort) (net.Conn, error)
	// ReplyTo sends an upstream reply back to the guest src spoofing the source
	// address as origDst (the TPROXY transparent-reply requirement). Injectable
	// for tests; defaults (when nil) to the platform transparent-socket impl
	// (transparentReply on Linux; an error stub elsewhere). See udp.go.
	ReplyTo func(origDst, guestSrc netip.AddrPort, payload []byte) error

	// Limits bounds this process's egress per ASK tenet 8. The zero value (all
	// fields 0) is unlimited — byte-identical to the pre-caps behavior.
	Limits Limits

	// bytesUsed is the process-wide cumulative byte counter covering BOTH the TCP
	// splice (guest->upstream copy) AND the UDP forward, so MaxTotalBytes bounds
	// total egress across tcp+udp. activeConns is the live mediated-TCP-connection
	// gauge for MaxConcurrentConns. Both are zero-valued and only consulted when
	// the corresponding Limits field is set, so an uncapped Handler never gates on
	// them beyond an unconditional cheap atomic Add.
	bytesUsed   atomic.Int64
	activeConns atomic.Int32
}

// brain returns a Brain view over the handler's policy fields — the one
// allowlist+guarded+swap+audit decision core shared with the UDP datapath and
// the wasm sandbox. It carries no per-connection state, so a fresh view per call
// is correct and cheap; the Handler keeps the byte-stream transport machinery
// (MITM, splice, byte/rate caps, peer/DNS reverse resolution) around it.
func (h *Handler) brain() *Brain {
	return &Brain{
		Mode:          h.Mode,
		Policy:        h.Policy,
		Swaps:         h.Swaps,
		Resolver:      h.Resolver,
		Cache:         h.tokenCache,
		Logger:        h.Logger,
		Limits:        h.Limits,
		UpstreamRoots: h.UpstreamRoots,
	}
}

// EnableSwaps wires a loaded swap table onto the handler with the same
// host-side resolver/cache setup used by Serve. It is for non-listener datapaths
// that instantiate Handler directly while still reusing mediator policy logic.
func (h *Handler) EnableSwaps(swaps *SwapTable) {
	h.Swaps = swaps
	if swaps == nil {
		h.Resolver = nil
		h.tokenCache = nil
		return
	}
	h.tokenCache = newTokenCache()
	h.Resolver = NewKeyResolver(func(msg string) {
		if h.Logger != nil {
			h.Logger.Log("egress_secret_warning", map[string]any{"warning": msg})
		}
	})
}

// addBytesOverCap adds n to the process-wide cumulative byte counter (shared by
// the TCP splice and the UDP forward) and reports whether the cumulative total
// has now exceeded MaxTotalBytes. When the cap is 0 (unlimited) it still tracks
// the count but never reports an overage, so the hot path stays a single atomic
// Add and a cheap compare.
func (h *Handler) addBytesOverCap(n int64) bool {
	total := h.bytesUsed.Add(n)
	limit := h.Limits.MaxTotalBytes
	return limit > 0 && total > limit
}

// errVolumeCap is returned by capWriter.Write once the process-wide byte cap is
// exceeded, so the splice tears the breaching flow down. It is a sentinel, never
// surfaced to the guest.
type errVolumeCap struct{}

func (errVolumeCap) Error() string { return "egress: total byte cap exceeded" }

// capWriter wraps the upstream-bound leg of a flow with two ASK-tenet-8 controls:
// it (1) charges every byte against the process-wide cumulative counter and
// returns errVolumeCap once MaxTotalBytes is exceeded (so io.Copy stops and the
// caller tears the flow down), and (2) when a rate limiter is set, blocks each
// write until the token bucket admits it (MaxBytesPerSec). With no cap and no
// limiter it is a thin passthrough, so an uncapped flow is byte-identical to a
// bare io.Copy aside from one atomic Add per write.
type capWriter struct {
	h       *Handler
	w       io.Writer
	limiter *rate.Limiter
	tripped *bool // set true when the cap trips, so the caller distinguishes a cap teardown from a normal close
}

func (cw capWriter) Write(p []byte) (int, error) {
	if cw.limiter != nil && len(p) > 0 {
		// Reserve/wait for the bytes about to be written. WaitN blocks until the
		// bucket has len(p) tokens (or returns on context). A best-effort
		// background wait is fine here: the flow is torn down on any write error.
		_ = cw.limiter.WaitN(context.Background(), len(p))
	}
	n, err := cw.w.Write(p)
	if n > 0 && cw.h.addBytesOverCap(int64(n)) {
		if cw.tripped != nil {
			*cw.tripped = true
		}
		// Return the bytes actually written plus the sentinel so io.Copy stops.
		if err == nil {
			err = errVolumeCap{}
		}
	}
	return n, err
}

// newCapLimiter builds the per-flow rate limiter for the upstream-bound copy when
// MaxBytesPerSec is set, or nil (unlimited) otherwise. The burst is one limit's
// worth of bytes (at least one max-size record) so a single large write is not
// permanently rejected by a too-small burst.
func (h *Handler) newCapLimiter() *rate.Limiter {
	bps := h.Limits.MaxBytesPerSec
	if bps <= 0 {
		return nil
	}
	burst := int(bps)
	if burst < maxTLSRecord {
		burst = maxTLSRecord
	}
	return rate.NewLimiter(rate.Limit(bps), burst)
}

// cappedSplice runs the bidirectional copy for a forwarded flow with the volume
// and rate caps applied to the upstream-bound (guest->upstream) direction. guest
// and upstream are the two ends; guestRead is the guest-side reader to copy FROM
// (the buffered bufio.Reader on the L4 path so peeked bytes are forwarded first,
// or nil to read directly from guest). On a volume-cap trip it closes BOTH ends
// so the opposite copy unblocks and the flow is fully torn down, then audits
// egress_cap_exceeded reason "volume". It returns whether the cap tripped so the
// caller can choose its close-event audit. A nil/zero Limits makes this a plain
// bidirectional io.Copy (byte-identical to the prior splice) apart from the
// counter Add.
func (h *Handler) cappedSplice(guest, upstream net.Conn, guestRead io.Reader, dst netip.AddrPort, host string) bool {
	if guestRead == nil {
		guestRead = guest
	}
	var tripped bool
	cw := capWriter{h: h, w: upstream, limiter: h.newCapLimiter(), tripped: &tripped}
	errc := make(chan error, 2)
	go func() { _, e := io.Copy(cw, guestRead); errc <- e }()   // guest -> upstream (capped)
	go func() { _, e := io.Copy(guest, upstream); errc <- e }() // upstream -> guest
	<-errc
	// Tear the flow down: close both ends so the second copy unblocks. This is
	// what bounds the breach — no NEW bytes escape once the cap is hit.
	_ = upstream.Close()
	_ = guest.Close()
	<-errc
	if tripped {
		h.Logger.Log("egress_cap_exceeded", map[string]any{
			"host": host, "dst": dst.String(), "proto": "tcp", "reason": "volume",
			"limit": h.Limits.MaxTotalBytes,
		})
	}
	return tripped
}

// DefaultOrigDst recovers the original destination for a *net.TCPConn (the
// concrete type the mediator's TCP listener yields). It panics if conn is not
// a *net.TCPConn, which would be a wiring error, not a runtime condition.
func DefaultOrigDst(c net.Conn) (netip.AddrPort, error) {
	return OriginalDestination(c.(*net.TCPConn))
}

// isOwnBindAddr reports whether dst is the mediator's own listen address — the
// loop-guard condition. The comparison is on the unmapped form so an IPv4 dst
// and an IPv4-in-IPv6 BindAddr (or vice versa) still match. A zero BindAddr
// disables the guard (returns false), so unit tests that do not set it are
// unaffected.
func (h *Handler) isOwnBindAddr(dst netip.AddrPort) bool {
	if !h.BindAddr.IsValid() {
		return false
	}
	return h.BindAddr.Port() == dst.Port() &&
		h.BindAddr.Addr().Unmap() == dst.Addr().Unmap()
}

// peerName reverse-resolves a destination IP to its named-network peer workspace
// name via the static PeerCache. It returns false when no roster is configured
// (the nat/user paths leave Peers nil) or when a is not a known peer.
func (h *Handler) peerName(a netip.Addr) (string, bool) {
	if h.Peers == nil {
		return "", false
	}
	return h.Peers.NameByIP(a)
}

// addPeerFields stamps the resolved named-network peer identity onto an audit
// field map. The peer name is stamped only when known; peer_ip is stamped
// whenever set, which includes an IP-only peer (a private/internal destination on
// a named network with no resolvable name) so a denied east-west flow remains
// legible. A no-op when both are empty (external host, or no roster), so
// external-host audit records are byte-identical to before.
func addPeerFields(fields map[string]any, peer, peerIP string) {
	if peer != "" {
		fields["peer"] = peer
	}
	if peerIP != "" {
		fields["peer_ip"] = peerIP
	}
}

// isEastWestAddr reports whether a destination address is internal/east-west: a
// private (RFC1918 / ULA), link-local, or loopback address — the addressing space
// a named network's peers occupy. Public/external destinations return false. It is
// the discriminator for surfacing peer_ip on an IP-only peer's audit (a peer not
// in the roster by name) without leaking the destination IP onto external-host
// audit records.
func isEastWestAddr(a netip.Addr) bool {
	return a.IsPrivate() || a.IsLinkLocalUnicast() || a.IsLoopback()
}

// Egress mode constants for the Handler.Mode field. Mirror the vmkit constants
// without introducing a package dependency; they must stay in sync.
const (
	egressModeGuarded = "guarded"
	egressModeBroker  = "broker"
)

// allowsBroad reports whether the mode grants public destinations by default,
// denying only the inside/infrastructure: guarded and broker. strict resolves
// and permits only allowlisted names; off runs no mediator. guarded and broker
// share this allow-broad decision — they differ only in termination (broker
// splices opaquely instead of forging per-SNI certificates).
func allowsBroad(mode string) bool {
	return mode == egressModeGuarded || mode == egressModeBroker
}

var cgnatPrefix = netip.MustParsePrefix("100.64.0.0/10")

// isInsideAddr reports whether a is "the inside" — infrastructure the guest
// must not reach under guarded mode. Matched on the resolved IP after Unmap so
// IPv4-mapped IPv6 (e.g. ::ffff:169.254.169.254) cannot bypass it.
func isInsideAddr(a netip.Addr) bool {
	a = a.Unmap()
	return a.IsLoopback() ||
		a.IsLinkLocalUnicast() || a.IsLinkLocalMulticast() ||
		a.IsPrivate() || // RFC1918 + IPv6 ULA fc00::/7
		a.IsUnspecified() ||
		cgnatPrefix.Contains(a)
}

// Handle services one captured connection. It always closes conn.
func (h *Handler) Handle(conn net.Conn) {
	defer func() { _ = conn.Close() }()
	dst, err := h.OrigDst(conn)
	if err != nil {
		h.Logger.Log("egress_origdst_error", map[string]any{"error": err.Error()})
		return
	}
	// Loop guard: a connection whose recovered destination is the mediator's own
	// bind address is the mediator folding back on itself (a readiness probe to
	// the listener, or a residual self-dial). Forwarding it would dial the
	// listener, accept, recover the same address, and dial again forever. Drop +
	// audit instead of dialing. This is defense-in-depth: the supervisor's nft
	// rules also keep the mediator's own traffic out of the capture path, but this
	// breaks any residual loop cheaply and unconditionally.
	if h.isOwnBindAddr(dst) {
		h.Logger.Log("egress_loop_guard", map[string]any{"dst": dst.String(), "proto": "tcp"})
		return
	}
	// Concurrency cap (ASK tenet 8): admit this connection into the live gauge and
	// refuse fail-closed if it would exceed MaxConcurrentConns — BEFORE any
	// upstream dial on either the MITM or L4 path. The increment is unconditional
	// (so the gauge is always accurate); the refusal is gated on the cap, so an
	// uncapped Handler (MaxConcurrentConns==0) admits everything exactly as before.
	// Decrement on every return via defer, so the slot frees when the flow closes.
	active := h.activeConns.Add(1)
	defer h.activeConns.Add(-1)
	if h.Limits.MaxConcurrentConns > 0 && active > h.Limits.MaxConcurrentConns {
		h.Logger.Log("egress_cap_exceeded", map[string]any{
			"dst": dst.String(), "proto": "tcp", "reason": "concurrency",
			"limit": h.Limits.MaxConcurrentConns,
		})
		return // fail-closed: no upstream dial
	}
	timeout := h.SniffTimeout
	if timeout <= 0 {
		timeout = 2 * time.Second
	}
	br := bufio.NewReaderSize(conn, maxTLSRecord)
	host, isTLS := sniffHost(br, dst, conn.SetReadDeadline, time.Now().Add(timeout))

	// Raw-IP fallback: when no SNI/Host was sniffed, sniffHost returns the bare
	// destination IP. Reverse-resolve it so a raw-IP TCP connection to a
	// previously-resolved allowlisted host (or a named-network peer) is matched by
	// name. Order, for a bare-IP destination only: the static PeerCache first (the
	// authoritative name↔IP roster of named-network members), then the DNS
	// NameCache (observed DNS answers). The SNI/Host path is left untouched. Both
	// lookups are nil-guarded for callers without the respective cache.
	//
	// peer/peerIP carry the resolved peer identity (if any) for the audit trail and
	// for the deny-time IP-literal fallback below. A peer match also satisfies the
	// reverse lookup, so the DNS NameCache is only consulted when no peer matched.
	var peer, peerIP string
	// isPeer classifies the destination as an internal named-network peer (east-west)
	// vs an external/public host. It gates the MITM guard below: a peer destination
	// NEVER takes serveMITM — east-west TLS is L4-spliced (splice + audit + allowlist
	// + fail-closed, NO MITM) so a self-signed/internal peer cert is not broken by
	// interception (and verification is never silently disabled — ASK tenet 6). MITM
	// applies only to external/public TLS. Nil-guarded via peerName.
	isPeer := false
	if name, ok := h.peerName(dst.Addr()); ok {
		isPeer = true
		peer = name
		peerIP = dst.Addr().String()
		if host == dst.Addr().String() {
			host = name
		}
	}
	// Audit legibility for an IP-only peer: a private/internal destination on a
	// named network whose IP is NOT in the roster has no resolvable name, but its
	// egress_deny (and any allow) should still surface the destination IP so the
	// east-west flow is legible in the audit. peerIP is set from the destination IP
	// for any private/internal address even without a name match; a public/external
	// destination leaves peerIP empty so its audit records stay byte-identical.
	// Only meaningful when a roster is configured (a named network); the nat/user
	// paths leave Peers nil and so never enter this branch.
	if peerIP == "" && h.Peers != nil && isEastWestAddr(dst.Addr()) {
		peerIP = dst.Addr().String()
	}
	if peer == "" && h.NameCache != nil && host == dst.Addr().String() {
		if name, ok := h.NameCache.HostForIP(dst.Addr()); ok {
			host = name
		}
	}

	passthrough := h.Passthrough != nil && h.Passthrough.AllowHost(host).Allow
	// One decision sequence — shared with the UDP datapath and the wasm sandbox
	// via Brain.Evaluate: the default-deny allowlist (against the sniffed host and
	// any resolved east-west peer identity — its workspace name and IP literal, so
	// an operator who allowlists a peer by name or IP permits the flow even when
	// the guest's SNI differs), plus the guarded inside-deny on the resolved
	// destination IP (which also defeats DNS rebinding, since the IP — not a
	// guest-supplied name — is classified). passthrough is the byte-stream
	// transport's own concern, applied here and excluded from the verdict's Allowed
	// so the MITM/L4 branching below stays put.
	b := h.brain()
	v := b.Evaluate(host, []string{peer, peerIP}, dst.Addr(), passthrough)
	allowed := v.Allowed
	unlisted := v.Unlisted
	if !allowed && !passthrough {
		denyFields := map[string]any{"host": host, "dst": dst.String()}
		addPeerFields(denyFields, peer, peerIP)
		b.AuditDeny(v, denyFields)
		return // fail-closed: no upstream dial
	}
	// MITM applies only to external/public TLS. A named-network peer destination
	// (isPeer) is excluded: east-west TLS is L4-spliced below (splice + audit +
	// allowlist + fail-closed) so a peer's self-signed/internal cert is not broken
	// by interception, and upstream verification is never silently disabled.
	if isTLS && h.CA != nil && allowed && !passthrough && !isPeer {
		h.serveMITM(conn, br, host, dst, unlisted)
		return
	}
	// Non-MITM path: passthrough / plain-HTTP / raw TCP -> L4 splice.
	up, err := h.Dial("tcp", dst.String())
	if err != nil {
		h.Logger.Log("egress_dial_error", map[string]any{"host": host, "dst": dst.String(), "error": err.Error()})
		return
	}
	defer func() { _ = up.Close() }()
	allowFields := map[string]any{"host": host, "dst": dst.String()}
	closeFields := map[string]any{"host": host, "dst": dst.String()}
	if unlisted {
		allowFields["unlisted"] = true
		closeFields["unlisted"] = true
	}
	// East-west legibility: a peer flow takes this L4-splice path (NO MITM), so
	// stamp mitm:false to make the external-vs-peer split explicit in the audit —
	// external MITM flows log mitm:true. Only on a peer flow; plain external
	// HTTP/passthrough L4 records stay byte-identical (no mitm key).
	if isPeer {
		allowFields["mitm"] = false
		closeFields["mitm"] = false
	}
	addPeerFields(allowFields, peer, peerIP)
	addPeerFields(closeFields, peer, peerIP)
	h.Logger.Log("egress_allow", allowFields)

	// L4 splice with the volume/rate caps applied to the upstream-bound copy. The
	// buffered ClientHello/HTTP bytes in br are forwarded first (guestRead=br). On
	// a volume-cap trip the flow is torn down (both ends closed) and audited
	// egress_cap_exceeded; otherwise this is byte-identical to the prior splice.
	if h.cappedSplice(conn, up, br, dst, host) {
		return // cap teardown already audited egress_cap_exceeded; skip egress_close
	}
	h.Logger.Log("egress_close", closeFields)
}
