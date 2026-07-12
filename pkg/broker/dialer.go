package broker

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"time"
)

// ErrTunnelDenied is returned by a Connect.Dial implementation to signal that a
// CONNECT tunnel was refused by egress governance — an inside/infrastructure
// destination — as opposed to a transient upstream failure. Connect.ServeHTTP
// distinguishes it from an ordinary dial error and maps it to a fail-closed deny
// carrying the SignalDenied signal, so a downstream consumer can halt or
// quarantine on the attempt.
var ErrTunnelDenied = errors.New("broker: tunnel destination denied")

// GuardedDialer builds a Connect.Dial that governs the tunnel destination.
// Without it, a CONNECT tunnel is a bare dial on a guest-controlled host — an
// open forward proxy that can reach the metadata service, loopback, or any
// private host. GuardedDialer closes that: it resolves the host, denies
// fail-closed if ANY resolved address is classified inside by IsInside, and
// dials the classified IP literal itself — never re-resolving — so a DNS rebind
// between the check and the dial cannot swap an allowed answer for an inside
// one.
//
// GuardedDialer is mechanism, not policy: the caller supplies the classifier
// (production reuses the egress mediator's inside classifier so the CONNECT path
// and the NIC datapath deny the same address space). The zero value is unusable;
// IsInside is required.
type GuardedDialer struct {
	// IsInside classifies a resolved destination IP as inside/infrastructure
	// (link-local/metadata, loopback, RFC1918/ULA, CGNAT, unspecified). Required.
	IsInside func(netip.Addr) bool
	// Resolve maps a hostname to its candidate addresses. Nil uses the system
	// resolver. Injected in tests for deterministic resolution.
	Resolve func(host string) ([]netip.Addr, error)
	// Base opens the upstream connection to the (already-classified) IP literal.
	// Nil uses a 10s TCP dialer.
	Base func(network, addr string) (net.Conn, error)
}

func (g GuardedDialer) resolve(host string) ([]netip.Addr, error) {
	if g.Resolve != nil {
		return g.Resolve(host)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return net.DefaultResolver.LookupNetIP(ctx, "ip", host)
}

func (g GuardedDialer) base(network, addr string) (net.Conn, error) {
	if g.Base != nil {
		return g.Base(network, addr)
	}
	return net.DialTimeout(network, addr, 10*time.Second)
}

// Dial is the Connect.Dial implementation. addr is the guest-supplied CONNECT
// target ("host:port"); host may be a name or an IP literal.
func (g GuardedDialer) Dial(network, addr string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, fmt.Errorf("broker: malformed tunnel target %q: %w", addr, err)
	}

	// An IP literal is classified as-is (no DNS); a name is resolved and every
	// answer classified. A single inside answer denies the whole tunnel.
	if ip, err := netip.ParseAddr(host); err == nil {
		if g.IsInside(ip) {
			return nil, fmt.Errorf("%w: %s", ErrTunnelDenied, ip)
		}
		return g.base(network, addr)
	}

	ips, err := g.resolve(host)
	if err != nil {
		return nil, fmt.Errorf("broker: resolve tunnel host %q: %w", host, err)
	}
	if len(ips) == 0 {
		return nil, fmt.Errorf("broker: tunnel host %q resolved to no addresses", host)
	}
	for _, ip := range ips {
		if g.IsInside(ip) {
			return nil, fmt.Errorf("%w: %s -> %s", ErrTunnelDenied, host, ip)
		}
	}
	// Anti-rebind: dial the exact address just classified, not the hostname, so
	// the base dialer cannot re-resolve to a different (inside) answer.
	return g.base(network, net.JoinHostPort(ips[0].String(), port))
}
