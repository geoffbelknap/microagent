package egress

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"time"
)

// dnstcp.go is the host-side DNS-over-TCP handler the windows-hyperv mediator
// front-end runs on a captured TCP/53 stream. The guest's resolver is forced
// onto TCP (resolv.conf "options use-vc"), the guest's nft OUTPUT REDIRECT
// captures the TCP/53 connection like any other TCP, and the guest forwarder
// ships it to the host mediator with a DestHeader{Proto:"tcp",Port:53}. Rather
// than send DNS through the TLS-MITM path (it is not TLS), the front-end routes
// port-53 streams here.
//
// The wire format on the stream (after the DestHeader has already been consumed
// by the front-end) is standard DNS-over-TCP framing (RFC 1035 §4.2.2): a
// 2-byte big-endian length prefix followed by the DNS message. We de-frame the
// query, run it through the SAME filtering resolver-forwarder the firecracker
// UDP path uses (Handler.handleDNS: parse + strict/mediated policy + REFUSED
// synthesis + NameCache population + audit), and re-frame the response. All of
// the policy/cache/audit logic is reused; only the TCP framing and the
// resolver round-trip transport are new here.

// dnsTCPReadTimeout bounds how long the handler waits for the guest to send the
// length-prefixed query before giving up. A guest that opened the TCP/53
// connection but never wrote a query must not pin a goroutine forever.
const dnsTCPReadTimeout = 10 * time.Second

// HandleDNSOverTCP services a captured DNS-over-TCP stream. A guest stub resolver
// forced onto TCP (resolv.conf "options use-vc") keeps the TCP/53 connection open
// and sends SUCCESSIVE queries on it — glibc in particular issues the A and AAAA
// lookups of one getaddrinfo() over the SAME connection (even with
// "single-request", which serializes them rather than splitting them onto two
// sockets). So the handler loops: it reads each 2-byte-length-prefixed query off
// rw, runs it through handleDNS (reusing the firecracker resolver/policy/cache/
// audit core) with the given upstream resolver and forward round-trip, writes the
// length-prefixed response back, and repeats until the guest closes the
// connection. forward performs the actual resolver round-trip (injected for tests;
// production wiring passes DefaultDNSForwardTCP).
//
// A clean EOF before the next length prefix is normal termination (the guest is
// done) and returns nil. A truncated/unframeable query mid-message, or a
// handleDNS error (parse/forward failure), returns an error so the caller closes
// the stream fail-closed; a policy DENY is NOT an error — handleDNS returns a
// synthesized REFUSED that is framed back to the guest, mirroring the UDP path's
// behavior. Servicing only the first query and closing the connection would break
// glibc's second (AAAA) query on the same socket and fail the whole lookup.
func (h *Handler) HandleDNSOverTCP(rw io.ReadWriter, resolver netip.AddrPort, forward func(resolver netip.AddrPort, query []byte) ([]byte, error)) error {
	for {
		if c, ok := rw.(net.Conn); ok {
			_ = c.SetReadDeadline(time.Now().Add(dnsTCPReadTimeout))
		}
		query, err := readDNSOverTCP(rw)
		if err != nil {
			// A clean EOF before the next query's length prefix is the guest closing
			// the connection after its last query — normal termination, not an error.
			if errors.Is(err, io.EOF) {
				return nil
			}
			return fmt.Errorf("egress: read DNS-over-TCP query: %w", err)
		}
		// Clear the read deadline before the (potentially blocking) forward + write
		// so a subsequent slow upstream is bounded by the forward's own timeout, not
		// the query-read deadline.
		if c, ok := rw.(net.Conn); ok {
			_ = c.SetReadDeadline(time.Time{})
		}
		resp, err := h.handleDNS(query, resolver, forward)
		if err != nil {
			// handleDNS already audited egress_dns_error; surface the error so the
			// caller closes the stream fail-closed (no response framed).
			return err
		}
		if resp == nil {
			continue
		}
		if err := writeDNSOverTCP(rw, resp); err != nil {
			return fmt.Errorf("egress: write DNS-over-TCP response: %w", err)
		}
	}
}

// readDNSOverTCP reads one 2-byte-length-prefixed DNS message from r (RFC 1035
// §4.2.2 TCP framing). A zero-length message is rejected (a query has at least a
// 12-byte header), and the length is bounded by uint16 so an attacker cannot
// request an unbounded allocation.
func readDNSOverTCP(r io.Reader) ([]byte, error) {
	var lenBuf [2]byte
	if _, err := io.ReadFull(r, lenBuf[:]); err != nil {
		return nil, fmt.Errorf("length prefix: %w", err)
	}
	n := binary.BigEndian.Uint16(lenBuf[:])
	if n == 0 {
		return nil, fmt.Errorf("zero-length DNS message")
	}
	msg := make([]byte, n)
	if _, err := io.ReadFull(r, msg); err != nil {
		return nil, fmt.Errorf("message body (%d bytes): %w", n, err)
	}
	return msg, nil
}

// writeDNSOverTCP frames msg with the 2-byte big-endian length prefix and writes
// it to w. A message longer than 65535 bytes cannot be framed (it would not fit
// the length field) and is rejected; a real DNS response never approaches this.
func writeDNSOverTCP(w io.Writer, msg []byte) error {
	if len(msg) > 0xFFFF {
		return fmt.Errorf("DNS message too long to frame: %d bytes", len(msg))
	}
	buf := make([]byte, 2+len(msg))
	binary.BigEndian.PutUint16(buf[:2], uint16(len(msg)))
	copy(buf[2:], msg)
	_, err := w.Write(buf)
	return err
}

// DefaultDNSForwardTCP is the production resolver round-trip for the host
// DNS-over-TCP handler: it dials the upstream resolver over TCP, sends the query
// with the 2-byte length prefix, and reads back the single length-prefixed
// response within dnsForwardTimeout. TCP (rather than the UDP defaultDNSForward
// the firecracker path uses) is host-OS-neutral and avoids any truncation
// concerns — the guest already forced TCP, so a TCP upstream is the natural
// match. The mediator dialing the real resolver is host-originated, so it is not
// re-captured.
func DefaultDNSForwardTCP(resolver netip.AddrPort, query []byte) ([]byte, error) {
	conn, err := net.DialTimeout("tcp", resolver.String(), dnsForwardTimeout)
	if err != nil {
		return nil, err
	}
	defer func() { _ = conn.Close() }()
	if err := conn.SetDeadline(time.Now().Add(dnsForwardTimeout)); err != nil {
		return nil, err
	}
	if err := writeDNSOverTCP(conn, query); err != nil {
		return nil, err
	}
	return readDNSOverTCP(conn)
}
