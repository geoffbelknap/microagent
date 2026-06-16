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
// client falls back to the IP rather than hanging.
func sniffHost(br *bufio.Reader, dst netip.AddrPort, setDeadline func(time.Time) error, deadline time.Time) string {
	_ = setDeadline(deadline)
	defer func() { _ = setDeadline(time.Time{}) }()

	first, err := br.Peek(1)
	if err != nil || len(first) == 0 {
		return dst.Addr().String()
	}
	if first[0] == 0x16 { // TLS handshake
		if hdr, err := br.Peek(5); err == nil && len(hdr) == 5 {
			if full, err := br.Peek(5 + int(binary.BigEndian.Uint16(hdr[3:5]))); err == nil {
				if sni, ok := parseClientHelloSNI(full); ok {
					return sni
				}
			}
		}
		return dst.Addr().String()
	}
	peek, _ := br.Peek(2048) // returns available bytes even on short read
	if host := httpHostHeader(peek); host != "" {
		return host
	}
	return dst.Addr().String()
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
