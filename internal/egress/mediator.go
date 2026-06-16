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
	if !d.Allow && !passthrough {
		h.Logger.Log("egress_deny", map[string]any{"host": host, "dst": dst.String(), "reason": d.Reason})
		return // fail-closed: no upstream dial
	}
	if isTLS && h.CA != nil && d.Allow && !passthrough {
		h.serveMITM(conn, br, host, dst)
		return
	}
	// Non-MITM path: passthrough / plain-HTTP / raw TCP -> L4 splice.
	up, err := h.Dial("tcp", dst.String())
	if err != nil {
		h.Logger.Log("egress_dial_error", map[string]any{"host": host, "dst": dst.String(), "error": err.Error()})
		return
	}
	defer up.Close()
	h.Logger.Log("egress_allow", map[string]any{"host": host, "dst": dst.String()})

	errc := make(chan error, 2)
	go func() { _, e := io.Copy(up, br); errc <- e }()   // guest -> upstream (buffered bytes first)
	go func() { _, e := io.Copy(conn, up); errc <- e }() // upstream -> guest
	<-errc
	h.Logger.Log("egress_close", map[string]any{"host": host, "dst": dst.String()})
}
