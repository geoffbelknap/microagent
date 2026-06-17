package egress

import (
	"crypto/tls"
	"io"
	"net"
	"net/netip"
)

// readerConn lets crypto/tls read the already-buffered ClientHello: Read comes
// from the bufio.Reader (which holds the peeked bytes + the rest of conn),
// while Write/Close/deadlines go to the underlying conn.
type readerConn struct {
	net.Conn
	r io.Reader
}

func (c readerConn) Read(p []byte) (int, error) { return c.r.Read(p) }

// serveMITM terminates the guest TLS with a CA-signed leaf for sni, dials the
// real upstream over TLS (verifying its real certificate), and splices
// plaintext both ways. r holds the buffered ClientHello bytes.
func (h *Handler) serveMITM(raw net.Conn, r io.Reader, sni string, dst netip.AddrPort, unlisted bool) {
	serverCfg := &tls.Config{
		GetCertificate: func(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
			name := hello.ServerName
			if name == "" {
				name = sni
			}
			return h.CA.LeafFor(name)
		},
	}
	guestTLS := tls.Server(readerConn{Conn: raw, r: r}, serverCfg)
	if err := guestTLS.Handshake(); err != nil {
		h.Logger.Log("egress_mitm_handshake_error", map[string]any{"host": sni, "error": err.Error()})
		return
	}
	defer func() { _ = guestTLS.Close() }()
	upCfg := &tls.Config{ServerName: sni}
	if h.UpstreamRoots != nil {
		upCfg.RootCAs = h.UpstreamRoots
	}
	up, err := tls.Dial("tcp", dst.String(), upCfg)
	if err != nil {
		h.Logger.Log("egress_mitm_upstream_error", map[string]any{"host": sni, "dst": dst.String(), "error": err.Error()})
		return
	}
	defer func() { _ = up.Close() }()
	allowFields := map[string]any{"host": sni, "dst": dst.String(), "mitm": true}
	closeFields := map[string]any{"host": sni, "dst": dst.String(), "mitm": true}
	if unlisted {
		allowFields["unlisted"] = true
		closeFields["unlisted"] = true
	}
	h.Logger.Log("egress_allow", allowFields)
	// Swap-scoping: only HTTP-parse the guest->upstream stream when this
	// connection's SNI matches a swap entry. Non-matching connections (and the
	// h.Swaps==nil case) keep the byte-identical io.Copy splice, so gRPC,
	// websockets, and raw TLS to non-swap hosts are unaffected. The
	// upstream->guest direction is always a plain splice.
	swapRelevant := false
	if h.Swaps != nil {
		if _, ok := h.Swaps.Match(sni); ok {
			swapRelevant = true
		}
	}
	// Non-swap MITM is the common case: run the standard capped bidirectional
	// splice (volume + rate caps on the upstream-bound copy, fail-closed teardown
	// on a volume trip). With zero Limits this is byte-identical to the prior
	// io.Copy splice.
	if !swapRelevant {
		if h.cappedSplice(guestTLS, up, nil, dst, sni) {
			return // cap teardown already audited egress_cap_exceeded; skip egress_close
		}
		h.Logger.Log("egress_close", closeFields)
		return
	}
	// Swap path: the request stream is HTTP-parsed for credential injection, so it
	// is not a plain splice. The volume cap still applies — wrap the upstream
	// writer so injected requests are charged against the process-wide counter and
	// the rate limiter throttles them. A volume trip closes both ends so the flow
	// tears down; the reason is audited egress_cap_exceeded volume.
	var tripped bool
	upCapped := capWriter{h: h, w: up, limiter: h.newCapLimiter(), tripped: &tripped}
	errc := make(chan error, 2)
	go func() {
		errc <- injectRequests(guestTLS, upCapped, sni, &Swapper{Resolver: h.Resolver, Cache: h.tokenCache}, h.Swaps, h.Logger)
	}()
	go func() { _, e := io.Copy(guestTLS, up); errc <- e }()
	<-errc
	_ = up.Close()
	_ = guestTLS.Close()
	<-errc
	if tripped {
		h.Logger.Log("egress_cap_exceeded", map[string]any{
			"host": sni, "dst": dst.String(), "proto": "tcp", "reason": "volume",
			"limit": h.Limits.MaxTotalBytes,
		})
		return
	}
	h.Logger.Log("egress_close", closeFields)
}
