package egress

import (
	"bufio"
	"encoding/binary"
	"net/netip"
	"strings"
	"time"
)

// sniffHost determines the destination host from the first bytes of a
// connection without consuming them: SNI for TLS, the Host header for HTTP,
// else the original-destination IP. deadline bounds the peek so a silent
// client falls back to the IP rather than hanging. isTLS reports whether the
// connection opened with a TLS ClientHello (byte 0x16).
func sniffHost(br *bufio.Reader, dst netip.AddrPort, setDeadline func(time.Time) error, deadline time.Time) (host string, isTLS bool) {
	_ = setDeadline(deadline)
	defer func() { _ = setDeadline(time.Time{}) }()

	first, err := br.Peek(1)
	if err != nil || len(first) == 0 {
		return dst.Addr().String(), false
	}
	if first[0] == 0x16 { // TLS handshake
		if hdr, err := br.Peek(5); err == nil && len(hdr) == 5 {
			if full, err := br.Peek(5 + int(binary.BigEndian.Uint16(hdr[3:5]))); err == nil {
				if sni, ok := parseClientHelloSNI(full); ok {
					return sni, true
				}
			}
		}
		return dst.Addr().String(), true
	}
	// Peek only what the first read already buffered — do NOT block waiting for
	// more bytes (a short request never reaches a fixed count, which would stall
	// until the deadline). The Peek(1) above already forced one read, so the
	// request headers are typically fully buffered here.
	peek, _ := br.Peek(br.Buffered())
	if h := httpHostHeader(peek); h != "" {
		return h, false
	}
	return dst.Addr().String(), false
}

// httpHostHeader returns the Host header value (port stripped) from raw HTTP
// request bytes, or "" if absent.
func httpHostHeader(b []byte) string {
	for _, line := range strings.Split(string(b), "\r\n") {
		if len(line) >= 5 && strings.EqualFold(line[:5], "Host:") {
			host := strings.TrimSpace(line[5:])
			if i := strings.LastIndexByte(host, ':'); i > 0 {
				host = host[:i]
			}
			return host
		}
	}
	return ""
}
