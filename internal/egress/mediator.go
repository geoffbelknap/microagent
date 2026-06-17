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
	Logger        Logger
	OrigDst       func(net.Conn) (netip.AddrPort, error)
	Dial          func(network, addr string) (net.Conn, error)
	SniffTimeout  time.Duration

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

	d := h.Policy.AllowHost(host)
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
		h.Logger.Log("egress_deny", map[string]any{"host": host, "dst": dst.String(), "reason": d.Reason})
		return // fail-closed: no upstream dial
	}
	if isTLS && h.CA != nil && allowed && !passthrough {
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
	h.Logger.Log("egress_allow", allowFields)

	errc := make(chan error, 2)
	go func() { _, e := io.Copy(up, br); errc <- e }()   // guest -> upstream (buffered bytes first)
	go func() { _, e := io.Copy(conn, up); errc <- e }() // upstream -> guest
	<-errc
	h.Logger.Log("egress_close", closeFields)
}
