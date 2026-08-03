package egress

import (
	"bufio"
	"crypto/tls"
	"io"
	"net"
	"net/netip"
	"strings"
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
	// HTTP/1.x requests are parsed so MITM mode can enforce semantic request
	// controls (including DoH) and credential swaps. Non-HTTP plaintext keeps the
	// raw splice, preserving HTTP/2, gRPC, and other TLS protocols.
	br := bufio.NewReader(guestTLS)
	isHTTP := looksLikeHTTPRequest(br)
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
	if !isHTTP && !swapRelevant {
		if h.cappedSplice(readerConn{Conn: guestTLS, r: br}, up, nil, dst, sni) {
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
		errc <- relayHTTPRequests(br, upCapped, sni, &Swapper{Resolver: h.Resolver, Cache: h.tokenCache}, h.Swaps, h.Logger)
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

func looksLikeHTTPRequest(br *bufio.Reader) bool {
	b, err := br.Peek(8)
	if err != nil && len(b) == 0 {
		return false
	}
	line := string(b)
	for _, method := range []string{"GET ", "POST ", "PUT ", "PATCH ", "DELETE ", "HEAD ", "OPTIONS ", "CONNECT ", "TRACE "} {
		if strings.HasPrefix(line, method) {
			return true
		}
	}
	return false
}
