package egress

import (
	"bytes"
	"encoding/binary"
	"io"
	"net/netip"
	"testing"

	"golang.org/x/net/dns/dnsmessage"
)

// readTCPDNSMessage reads one 2-byte-length-prefixed DNS message from r (the
// DNS-over-TCP framing, RFC 1035 §4.2.2). Test helper mirroring what a guest
// resolver's TCP read loop does.
func readTCPDNSMessage(t *testing.T, r io.Reader) []byte {
	t.Helper()
	var lenBuf [2]byte
	if _, err := io.ReadFull(r, lenBuf[:]); err != nil {
		t.Fatalf("read DNS-over-TCP length prefix: %v", err)
	}
	n := binary.BigEndian.Uint16(lenBuf[:])
	msg := make([]byte, n)
	if _, err := io.ReadFull(r, msg); err != nil {
		t.Fatalf("read DNS-over-TCP body (%d bytes): %v", n, err)
	}
	return msg
}

// writeTCPDNSMessage frames msg with the 2-byte length prefix and writes it to
// w, simulating a guest resolver sending a query over TCP.
func writeTCPDNSMessage(t *testing.T, w io.Writer, msg []byte) {
	t.Helper()
	var lenBuf [2]byte
	binary.BigEndian.PutUint16(lenBuf[:], uint16(len(msg)))
	if _, err := w.Write(lenBuf[:]); err != nil {
		t.Fatalf("write DNS-over-TCP length prefix: %v", err)
	}
	if _, err := w.Write(msg); err != nil {
		t.Fatalf("write DNS-over-TCP body: %v", err)
	}
}

// TestHandleDNSOverTCPResolvesAndFrames asserts the DNS-over-TCP handler reads a
// length-prefixed query, forwards an allowlisted name via the injected forward,
// caches the answer name->IP, audits an allow, and writes the length-prefixed
// response back verbatim.
func TestHandleDNSOverTCPResolvesAndFrames(t *testing.T) {
	pol, _ := NewPolicy([]string{"allowed.example.com"})
	log := &BufferLogger{}
	h := &Handler{Mode: "strict", Policy: pol, Logger: log, NameCache: NewNameCache()}

	want := buildResponseWithA(t, 0x4242, "allowed.example.com.", "allowed.example.com.",
		[4]byte{203, 0, 113, 7}, 300)
	var forwardedQuery []byte
	forward := func(r netip.AddrPort, q []byte) ([]byte, error) {
		forwardedQuery = append([]byte(nil), q...)
		return want, nil
	}

	query := buildQuery(t, 0x4242, "allowed.example.com.", dnsmessage.TypeA)
	var in, out bytes.Buffer
	writeTCPDNSMessage(t, &in, query)

	resolver := netip.MustParseAddrPort("1.1.1.1:53")
	rw := &readWriter{r: &in, w: &out}
	if err := h.HandleDNSOverTCP(rw, resolver, forward); err != nil {
		t.Fatalf("HandleDNSOverTCP: %v", err)
	}

	if !bytes.Equal(forwardedQuery, query) {
		t.Errorf("forwarded query did not match the de-framed query")
	}
	got := readTCPDNSMessage(t, &out)
	if !bytes.Equal(got, want) {
		t.Errorf("response not written back verbatim through the TCP framing")
	}
	host, ok := h.NameCache.HostForIP(netip.AddrFrom4([4]byte{203, 0, 113, 7}))
	if !ok || host != "allowed.example.com" {
		t.Errorf("HostForIP = (%q,%v), want (%q,true)", host, ok, "allowed.example.com")
	}
	assertEvent(t, log, "egress_dns_allow")
}

// TestHandleDNSOverTCPDeniedReturnsRefused asserts a strict-mode non-allowlisted
// name is never forwarded and the handler writes a length-prefixed REFUSED back,
// matching the core's DNS deny convention (synthesizeRefused).
func TestHandleDNSOverTCPDeniedReturnsRefused(t *testing.T) {
	pol, _ := NewPolicy([]string{"allowed.example.com"})
	log := &BufferLogger{}
	h := &Handler{Mode: "strict", Policy: pol, Logger: log, NameCache: NewNameCache()}

	forwardCalled := false
	forward := func(netip.AddrPort, []byte) ([]byte, error) {
		forwardCalled = true
		return nil, nil
	}

	query := buildQuery(t, 0x0099, "blocked.example.com.", dnsmessage.TypeA)
	var in, out bytes.Buffer
	writeTCPDNSMessage(t, &in, query)

	rw := &readWriter{r: &in, w: &out}
	if err := h.HandleDNSOverTCP(rw, netip.MustParseAddrPort("1.1.1.1:53"), forward); err != nil {
		t.Fatalf("HandleDNSOverTCP: %v", err)
	}
	if forwardCalled {
		t.Error("forward was called for a denied query; want NOT forwarded")
	}
	got := readTCPDNSMessage(t, &out)
	var p dnsmessage.Parser
	hdr, err := p.Start(got)
	if err != nil {
		t.Fatalf("parse refused response: %v", err)
	}
	if hdr.RCode != dnsmessage.RCodeRefused {
		t.Errorf("RCode = %v, want %v", hdr.RCode, dnsmessage.RCodeRefused)
	}
	assertEvent(t, log, "egress_dns_deny")
}

// TestHandleDNSOverTCPRejectsTruncatedPrefix asserts a truncated length prefix
// is an error (the conn is closed by the caller fail-closed) and nothing is
// forwarded.
func TestHandleDNSOverTCPRejectsTruncatedPrefix(t *testing.T) {
	h := &Handler{Mode: "mediated", Logger: &BufferLogger{}, NameCache: NewNameCache()}
	forward := func(netip.AddrPort, []byte) ([]byte, error) {
		t.Fatal("forward must not be called on a truncated prefix")
		return nil, nil
	}
	in := bytes.NewReader([]byte{0x00}) // one byte: not a full 2-byte length prefix
	var out bytes.Buffer
	rw := &readWriter{r: in, w: &out}
	if err := h.HandleDNSOverTCP(rw, netip.MustParseAddrPort("1.1.1.1:53"), forward); err == nil {
		t.Fatal("HandleDNSOverTCP err=nil on truncated prefix, want error")
	}
	if out.Len() != 0 {
		t.Errorf("wrote %d bytes on a truncated prefix; want 0", out.Len())
	}
}

// readWriter joins a separate reader and writer into one io.ReadWriter so a test
// can feed a query in and capture the framed response out.
type readWriter struct {
	r io.Reader
	w io.Writer
}

func (rw *readWriter) Read(p []byte) (int, error)  { return rw.r.Read(p) }
func (rw *readWriter) Write(p []byte) (int, error) { return rw.w.Write(p) }
