package egress

import (
	"bufio"
	"crypto/x509"
	"io"
	"net"
	"net/netip"
	"time"
)

// maxTLSRecord is the largest TLS record (2^14 plaintext + 5-byte header). The
// capture reader is sized to it so sniffHost can peek a full ClientHello and
// extract SNI rather than silently falling back to the destination IP.
const maxTLSRecord = 1<<14 + 5

// Handler mediates one redirected connection: recover the original
// destination, sniff the host, enforce the allowlist, and forward or deny
// fail-closed. OrigDst and Dial are injectable for tests.
type Handler struct {
	// Mode selects enforcement: "mediated" allows + audits every destination
	// (MITM all TLS, nothing blocked); "strict" (or empty, the safe default)
	// denies non-allowlisted destinations fail-closed.
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

// Handle services one captured connection. It always closes conn.
func (h *Handler) Handle(conn net.Conn) {
	defer conn.Close()
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

	d := h.Policy.AllowHost(host)
	// Peer identity fallback: for a known east-west peer the authoritative identity
	// is its workspace name from the roster, not a guest-supplied SNI/Host (an
	// internal peer presents whatever internal cert/SNI it likes). When the
	// host-based decision denied, re-evaluate against the peer name — so an operator
	// who allowlisted the peer by name ("builder") permits the flow even when the
	// guest's SNI differs. This is what makes a peer flow that LOOKS like TLS (SNI
	// present) reach the L4-splice path instead of being denied on the SNI.
	if !d.Allow && peer != "" && host != peer {
		if nd := h.Policy.AllowHost(peer); nd.Allow {
			d = nd
		}
	}
	// Peer IP-literal fallback: a known peer that is still denied by name may be
	// allowed if the operator allowlisted its IP literal. Default-deny stands —
	// this only widens to an explicit IP entry, never beyond the allowlist. Tried
	// only when the by-name decision denied.
	if !d.Allow && peer != "" && peerIP != "" {
		if ipd := h.Policy.AllowHost(peerIP); ipd.Allow {
			d = ipd
		}
	}
	passthrough := h.Passthrough != nil && h.Passthrough.AllowHost(host).Allow
	// In mediated mode every destination is allowed (and audited); strict (or
	// empty) keeps the default-deny allowlist. allowed defaults to d.Allow when
	// Mode is unset, so an unspecified mode is safe.
	allowed := d.Allow || h.Mode == "mediated"
	// unlisted marks a destination permitted only because of mediated mode (it
	// is not on the allowlist) so the audit trail records the looser grant. A
	// passthrough host is explicitly listed — and strict would allow it too — so
	// it is never "unlisted".
	unlisted := allowed && !d.Allow && !passthrough
	if !allowed && !passthrough {
		denyFields := map[string]any{"host": host, "dst": dst.String(), "reason": d.Reason}
		addPeerFields(denyFields, peer, peerIP)
		h.Logger.Log("egress_deny", denyFields)
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
	defer up.Close()
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

	errc := make(chan error, 2)
	go func() { _, e := io.Copy(up, br); errc <- e }()   // guest -> upstream (buffered bytes first)
	go func() { _, e := io.Copy(conn, up); errc <- e }() // upstream -> guest
	<-errc
	h.Logger.Log("egress_close", closeFields)
}
