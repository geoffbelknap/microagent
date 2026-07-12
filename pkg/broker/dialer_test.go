package broker

import (
	"errors"
	"net"
	"net/netip"
	"testing"
)

// testInside is a stand-in destination classifier for the guarded dialer tests:
// loopback, link-local (which covers the 169.254.169.254 metadata address), and
// RFC1918/ULA private addresses are "inside". Production wires the egress
// mediator's own classifier; the dialer only depends on the func, not its
// contents.
func testInside(a netip.Addr) bool {
	a = a.Unmap()
	return a.IsLoopback() || a.IsLinkLocalUnicast() || a.IsPrivate() || a.IsUnspecified()
}

// recordingBase is a base dialer that records the address it was asked to dial
// and hands back a live (piped) connection, so a test can assert both whether
// the base was reached and exactly what destination it was given.
type recordingBase struct {
	addr   string
	called bool
}

func (r *recordingBase) dial(network, addr string) (net.Conn, error) {
	r.called = true
	r.addr = addr
	c, _ := net.Pipe()
	return c, nil
}

// TestGuardedDialerDeniesInsideIPLiteral: a CONNECT straight to the metadata
// address (a link-local literal) is refused fail-closed with ErrTunnelDenied and
// the base dialer is never reached.
func TestGuardedDialerDeniesInsideIPLiteral(t *testing.T) {
	base := &recordingBase{}
	g := GuardedDialer{IsInside: testInside, Base: base.dial}
	_, err := g.Dial("tcp", "169.254.169.254:80")
	if !errors.Is(err, ErrTunnelDenied) {
		t.Fatalf("err = %v, want ErrTunnelDenied", err)
	}
	if base.called {
		t.Fatal("base dialer reached for an inside destination; must fail closed before dialing")
	}
}

// TestGuardedDialerDeniesResolvedInside: a hostname that resolves to an inside
// address is refused after resolution — the guard classifies the resolved IP,
// not the guest-supplied name.
func TestGuardedDialerDeniesResolvedInside(t *testing.T) {
	base := &recordingBase{}
	g := GuardedDialer{
		IsInside: testInside,
		Resolve:  func(string) ([]netip.Addr, error) { return []netip.Addr{netip.MustParseAddr("127.0.0.1")}, nil },
		Base:     base.dial,
	}
	_, err := g.Dial("tcp", "sneaky.example.com:443")
	if !errors.Is(err, ErrTunnelDenied) {
		t.Fatalf("err = %v, want ErrTunnelDenied", err)
	}
	if base.called {
		t.Fatal("base dialer reached for a host resolving inside; must fail closed")
	}
}

// TestGuardedDialerDeniesWhenAnyResolvedInside: if resolution returns a mix, a
// single inside answer denies the whole tunnel (a rebind that returns one public
// and one inside answer cannot smuggle the inside one through).
func TestGuardedDialerDeniesWhenAnyResolvedInside(t *testing.T) {
	base := &recordingBase{}
	g := GuardedDialer{
		IsInside: testInside,
		Resolve: func(string) ([]netip.Addr, error) {
			return []netip.Addr{netip.MustParseAddr("93.184.216.34"), netip.MustParseAddr("10.0.0.5")}, nil
		},
		Base: base.dial,
	}
	if _, err := g.Dial("tcp", "mixed.example.com:443"); !errors.Is(err, ErrTunnelDenied) {
		t.Fatalf("err = %v, want ErrTunnelDenied", err)
	}
	if base.called {
		t.Fatal("base dialer reached despite an inside answer in the resolution set")
	}
}

// TestGuardedDialerDialsClassifiedIPLiteral: an allowed hostname is dialed at the
// resolved IP literal, never re-resolved by the base dialer — the anti-rebind
// guarantee, so the address that was classified is the address that is dialed.
func TestGuardedDialerDialsClassifiedIPLiteral(t *testing.T) {
	base := &recordingBase{}
	g := GuardedDialer{
		IsInside: testInside,
		Resolve:  func(string) ([]netip.Addr, error) { return []netip.Addr{netip.MustParseAddr("93.184.216.34")}, nil },
		Base:     base.dial,
	}
	c, err := g.Dial("tcp", "public.example.com:443")
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	_ = c.Close()
	if !base.called {
		t.Fatal("base dialer not reached for an allowed public destination")
	}
	if base.addr != "93.184.216.34:443" {
		t.Fatalf("base dialed %q, want the classified IP literal 93.184.216.34:443 (anti-rebind)", base.addr)
	}
}

// TestGuardedDialerAllowsPublicLiteral: a public IP literal passes straight
// through to the base dialer.
func TestGuardedDialerAllowsPublicLiteral(t *testing.T) {
	base := &recordingBase{}
	g := GuardedDialer{IsInside: testInside, Base: base.dial}
	c, err := g.Dial("tcp", "93.184.216.34:443")
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	_ = c.Close()
	if base.addr != "93.184.216.34:443" {
		t.Fatalf("base dialed %q, want 93.184.216.34:443", base.addr)
	}
}

// TestGuardedDialerRejectsMalformedAddr: an unparseable target fails closed
// rather than being handed to the base dialer.
func TestGuardedDialerRejectsMalformedAddr(t *testing.T) {
	base := &recordingBase{}
	g := GuardedDialer{IsInside: testInside, Base: base.dial}
	if _, err := g.Dial("tcp", "no-port-here"); err == nil {
		t.Fatal("expected error for malformed addr")
	}
	if base.called {
		t.Fatal("base dialer reached for a malformed addr")
	}
}
