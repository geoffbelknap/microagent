package egress

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/netip"
	"os"
	"time"
)

// ReadyMarker is the prefix of the readiness line written to Options.Ready once
// the mediator is FULLY up — specifically AFTER the transparent UDP socket has
// opened, not merely after the TCP listener bound. The supervisor scans the
// mediator child's stdout/logfile for this marker so it never treats the
// mediator as ready during the window where the TCP listener accepts but the
// UDP socket has not yet opened (and could still fail, exiting the mediator).
// The bound TCP address follows the marker on the same line.
const ReadyMarker = "egress_ready"

// Options configures the mediator listener.
type Options struct {
	Mode         string // "guarded" (default, deny private ranges) or "mediated" (allow+audit all) or "strict" (deny non-allowlisted) or "" (normalizes to guarded)
	BindHost     string
	BindPort     int
	Allow        []string
	AuditLogPath string
	Logger       Logger                                 // optional; if nil and AuditLogPath set, a FileLogger is opened
	OrigDst      func(net.Conn) (netip.AddrPort, error) // optional; defaults to DefaultOrigDst
	Ready        io.Writer                              // optional; bound address written here once listening
	SniffTimeout time.Duration                          // optional; passed to Handler (Handler defaults to 2s when <=0)
	CACertPath   string                                 // if set with CAKeyPath, enables TLS interception
	CAKeyPath    string
	Passthrough  []string // allowed hosts that are NOT intercepted (L4 splice + audit)

	// Peers is the named-network member roster as "name=ip" pairs (this
	// workspace's own entry excluded). When non-empty, Serve builds a PeerCache and
	// hands it to the Handler so east-west VM↔VM flows whose bare destination IP
	// maps to a member are policed by the member's workspace name under the same
	// default-deny allowlist. A malformed entry aborts startup (fail-closed). Empty
	// for the nat/user paths (no roster), leaving the request path unchanged.
	Peers []string

	// SwapConfigPath, if set, points at a credential-swaps YAML file. Serve
	// reads + loads it into a SwapTable and stores it on the Handler. A load
	// error is fatal (fail-closed). UNUSED beyond loading this phase.
	SwapConfigPath string

	// UDPListen opens the transparent UDP socket the mediator serves. Defaults to
	// transparentUDPListener (IP_TRANSPARENT + IP_RECVORIGDSTADDR). Injectable for tests.
	UDPListen func(addr netip.AddrPort) (*net.UDPConn, error)

	// Limits bounds this mediator process's egress per ASK tenet 8 (rate, total
	// volume, and concurrent connections). The zero value is unlimited — the
	// current, uncapped behavior. Plumbed onto the Handler by Serve.
	Limits Limits

	// AuditMaxBytes and AuditMaxBackups configure the size-bounded rotating audit
	// log. When AuditMaxBytes > 0 (and Logger is nil and AuditLogPath is set),
	// Serve builds a RotatingFileLogger that caps each active file at AuditMaxBytes
	// and keeps at most AuditMaxBackups rotated files. AuditMaxBytes <= 0 keeps the
	// unbounded FileLogger (current behavior).
	AuditMaxBytes   int64
	AuditMaxBackups int
}

// Run binds BindHost:BindPort and serves until ctx is cancelled.
func Run(ctx context.Context, opts Options) error {
	ln, err := net.Listen("tcp", net.JoinHostPort(opts.BindHost, fmt.Sprintf("%d", opts.BindPort)))
	if err != nil {
		return fmt.Errorf("egress: listen: %w", err)
	}
	return Serve(ctx, ln, opts)
}

// Serve services connections on ln until ctx is cancelled, closing ln on return.
func Serve(ctx context.Context, ln net.Listener, opts Options) error {
	policy, err := NewPolicy(opts.Allow)
	if err != nil {
		_ = ln.Close()
		return err
	}
	logger := opts.Logger
	if logger == nil {
		if opts.AuditLogPath == "" {
			_ = ln.Close()
			return fmt.Errorf("egress: a logger or audit log path is required")
		}
		// Size-bounded rotating audit log when AuditMaxBytes > 0 (ASK tenet 8 —
		// bounded retention); otherwise the unbounded FileLogger (current behavior).
		if opts.AuditMaxBytes > 0 {
			rl, err := NewRotatingFileLogger(opts.AuditLogPath, opts.AuditMaxBytes, opts.AuditMaxBackups)
			if err != nil {
				_ = ln.Close()
				return err
			}
			defer func() { _ = rl.Close() }()
			logger = rl
		} else {
			fl, err := NewFileLogger(opts.AuditLogPath)
			if err != nil {
				_ = ln.Close()
				return err
			}
			defer func() { _ = fl.Close() }()
			logger = fl
		}
	}
	orig := opts.OrigDst
	if orig == nil {
		orig = DefaultOrigDst
	}
	if (opts.CACertPath == "") != (opts.CAKeyPath == "") {
		_ = ln.Close()
		return fmt.Errorf("egress: CACertPath and CAKeyPath must be set together")
	}
	var ca *CA
	if opts.CACertPath != "" && opts.CAKeyPath != "" {
		certPEM, rerr := os.ReadFile(opts.CACertPath)
		if rerr != nil {
			_ = ln.Close()
			return fmt.Errorf("egress: read CA cert: %w", rerr)
		}
		keyPEM, rerr := os.ReadFile(opts.CAKeyPath)
		if rerr != nil {
			_ = ln.Close()
			return fmt.Errorf("egress: read CA key: %w", rerr)
		}
		ca, rerr = LoadCA(certPEM, keyPEM)
		if rerr != nil {
			_ = ln.Close()
			return rerr
		}
	}
	var passthrough *Policy
	if len(opts.Passthrough) > 0 {
		passthrough, err = NewPolicy(opts.Passthrough)
		if err != nil {
			_ = ln.Close()
			return err
		}
	}
	// Named-network peer roster: build the static name↔IP PeerCache when a roster
	// is supplied. Fail closed — a malformed entry aborts startup so the mediator
	// never runs with a partially-resolvable roster that would silently police some
	// peers by bare IP. peers stays nil for the nat/user paths (no roster), leaving
	// the request path unchanged.
	var peers *PeerCache
	if len(opts.Peers) > 0 {
		peers, err = NewPeerCache(opts.Peers)
		if err != nil {
			_ = ln.Close()
			return err
		}
	}
	// Credential-swap config: load the host-indexed swap table if a path is set.
	// Fail closed — a read or parse error aborts startup so a later injection
	// phase never runs against a misconfigured (or absent) table that was
	// expected. swaps stays nil when no path is configured, leaving the request
	// path byte-identical to today.
	var swaps *SwapTable
	if opts.SwapConfigPath != "" {
		data, rerr := os.ReadFile(opts.SwapConfigPath)
		if rerr != nil {
			_ = ln.Close()
			return fmt.Errorf("egress: read swap config: %w", rerr)
		}
		swaps, rerr = LoadSwapTable(data)
		if rerr != nil {
			_ = ln.Close()
			return rerr
		}
	}
	// BindAddr is the mediator's own listen address; the Handler's loop guard
	// drops any captured connection/datagram whose recovered original destination
	// equals it (the mediator dialing itself). Derived from the actual listener so
	// a :0 auto-assigned port is captured. A parse failure leaves it zero, which
	// merely disables the loop guard (the nft rules still prevent the loop), so it
	// is not fatal here.
	bindAP, _ := netip.ParseAddrPort(ln.Addr().String())
	h := &Handler{Mode: opts.Mode, Policy: policy, Logger: logger, OrigDst: orig, Dial: net.Dial, CA: ca, Passthrough: passthrough, Peers: peers, SniffTimeout: opts.SniffTimeout, BindAddr: bindAP, Swaps: swaps, Limits: opts.Limits}
	// Build the token cache and the real secret resolver only when a swap table
	// is loaded. KeyResolver wraps microagent's standard secret registry (env /
	// file / dotenv / vault) so a swap's key_ref resolves host-side identically
	// to the rest of microagent. Plaintext-scheme warnings are routed into the
	// audit log (never secret material). With no swap table the Resolver stays
	// nil and the request path is byte-identical to today; a live swap with a
	// missing resolver would fail closed in Swapper.acquire regardless.
	if swaps != nil {
		h.tokenCache = newTokenCache()
		h.Resolver = NewKeyResolver(func(msg string) {
			logger.Log("egress_secret_warning", map[string]any{"warning": msg})
		})
	}
	logger.Log("egress_listen", map[string]any{"addr": ln.Addr().String(), "allow": opts.Allow})

	// Mediation always includes UDP: open the transparent UDP socket on the same
	// host:port as the TCP listener (different protocol). Deriving the bind from
	// the actual TCP listener address picks up a :0 auto-assigned port. Fail
	// closed: if the transparent socket cannot be opened (no TPROXY capability),
	// return the error so the supervisor's readiness check fails — no TCP-only
	// fallback.
	lnAP, err := netip.ParseAddrPort(ln.Addr().String())
	if err != nil {
		_ = ln.Close()
		return fmt.Errorf("egress: parse listener addr %q: %w", ln.Addr().String(), err)
	}
	udpListen := opts.UDPListen
	if udpListen == nil {
		udpListen = transparentUDPListener
	}
	udpConn, err := udpListen(lnAP)
	if err != nil {
		logger.Log("egress_udp_listen_error", map[string]any{"addr": lnAP.String(), "error": err.Error()})
		_ = ln.Close()
		return fmt.Errorf("egress: listen udp: %w", err)
	}
	// Close udpConn on every Serve return (e.g. the accept-error path below,
	// where ctx is still live). The ctx.Done goroutine also closes it to unblock
	// serveUDP's ReadMsgUDP promptly on cancellation; the redundant close is a
	// harmless no-op (returns ErrClosed, ignored).
	defer func() { _ = udpConn.Close() }()
	logger.Log("egress_udp_listen", map[string]any{"addr": udpConn.LocalAddr().String()})
	go serveUDP(udpConn, h)

	// Signal readiness ONLY now — after both the TCP listener bound and the
	// transparent UDP socket opened. Emitting an unambiguous marker (rather than
	// a bare address) lets the supervisor scan the child's logfile for a
	// post-UDP signal, closing the window where a TCP-only probe would pass
	// before UDP came up. The bound address follows for diagnostics.
	if opts.Ready != nil {
		fmt.Fprintf(opts.Ready, "%s %s\n", ReadyMarker, ln.Addr().String())
	}
	go func() {
		<-ctx.Done()
		_ = ln.Close()
		_ = udpConn.Close()
	}()
	for {
		conn, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("egress: accept: %w", err)
		}
		go h.Handle(conn)
	}
}
