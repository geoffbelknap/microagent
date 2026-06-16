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
}

// DefaultOrigDst recovers the original destination for a *net.TCPConn (the
// concrete type the mediator's TCP listener yields). It panics if conn is not
// a *net.TCPConn, which would be a wiring error, not a runtime condition.
func DefaultOrigDst(c net.Conn) (netip.AddrPort, error) {
	return OriginalDestination(c.(*net.TCPConn))
}

// Handle services one captured connection. It always closes conn.
func (h *Handler) Handle(conn net.Conn) {
	defer conn.Close()
	dst, err := h.OrigDst(conn)
	if err != nil {
		h.Logger.Log("egress_origdst_error", map[string]any{"error": err.Error()})
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
	// is not on the allowlist) so the audit trail records the looser grant.
	unlisted := allowed && !d.Allow
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
