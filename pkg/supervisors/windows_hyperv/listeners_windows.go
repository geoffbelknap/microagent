//go:build windows

package windows_hyperv

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Microsoft/go-winio"
	"github.com/Microsoft/go-winio/pkg/guid"
	"github.com/geoffbelknap/microagent/internal/egress"
	"github.com/geoffbelknap/microagent/pkg/secretxfer"
	"github.com/geoffbelknap/microagent/pkg/vmkit"
)

const maxWindowsHyperVResultBytes = 16 * 1024 * 1024

var dialHVSockPortHook = dialHVSockPort

type hvSocketListenerSet struct {
	listeners []net.Listener
	result    chan error
	once      sync.Once
}

func startRuntimeListeners(ctx context.Context, handle computeSystemHandle, req vmkit.Request) (runtimeListenerSet, error) {
	if req.Config == nil || (len(req.Config.VsockListeners) == 0 && !hasPortForwards(req.Config) && !hasExecBridge(req.Config)) {
		return nil, nil
	}
	for _, listener := range req.Config.VsockListeners {
		if !isAllowedHVSockTarget(req, listener.Target) {
			return nil, fmt.Errorf("windows-hyperv vsock listener %d target must be host:port, the secrets service, or the workspace result path", listener.Port)
		}
	}
	vmID, err := guid.FromString(handle.RuntimeID)
	if err != nil {
		return nil, fmt.Errorf("parse HCS runtime ID %q: %w", handle.RuntimeID, err)
	}
	set := &hvSocketListenerSet{}
	if hasResultTarget(req) {
		set.result = make(chan error, 1)
	}
	go copySerialPipe(serialPipePath(req.Identity.RuntimeID), serialLogPath(req))
	started := 0
	for _, listener := range req.Config.VsockListeners {
		// Resolve the secrets bundle before the listener (and the boot)
		// exists: an unresolvable reference fails the start, never a guest
		// waiting on a half-served bundle.
		var secrets *secretxfer.Server
		if listener.Target == secretxfer.ServerTarget {
			bundle, err := secretxfer.ResolveBundle(ctx, req.Config)
			if err != nil {
				_ = set.Close()
				return nil, fmt.Errorf("resolve secrets: %w", err)
			}
			secrets = secretxfer.NewServer(req.Identity.RuntimeID, req.Config.StateDir, bundle, secretxfer.OnDemandRefs(req.Config), req.Config.SecretsAudit)
		}
		l, err := winio.ListenHvsock(&winio.HvsockAddr{
			VMID:      vmID,
			ServiceID: winio.VsockServiceID(listener.Port),
		})
		if err != nil {
			_ = set.Close()
			return nil, fmt.Errorf("listen windows-hyperv hvsocket port %d: %w", listener.Port, err)
		}
		set.listeners = append(set.listeners, l)
		started++
		if secrets != nil {
			go secrets.Serve(l)
			continue
		}
		if listener.Target == secretxfer.CACertTarget {
			caCertPath := egressCACertPath(req)
			go func(l net.Listener) {
				for {
					conn, err := l.Accept()
					if err != nil {
						return
					}
					go serveCACertConn(conn, caCertPath)
				}
			}(l)
			continue
		}
		go set.serve(l, listener.Target, listener.Target == resultPath(req))
	}
	if req.Config.Network != nil {
		for _, forward := range req.Config.Network.PortForwards {
			if forward.Protocol != "" && forward.Protocol != "tcp" {
				continue
			}
			host := strings.TrimSpace(forward.Host)
			if host == "" {
				host = "127.0.0.1"
			}
			addr := net.JoinHostPort(host, strconv.Itoa(int(forward.HostPort)))
			l, err := net.Listen("tcp", addr)
			if err != nil {
				_ = set.Close()
				return nil, fmt.Errorf("listen windows-hyperv published tcp %s: %w", addr, err)
			}
			set.listeners = append(set.listeners, l)
			started++
			go servePublishedPortForward(l, vmID, forward)
		}
	}
	if hasExecBridge(req.Config) {
		addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(int(req.Config.ExecPort)))
		l, err := net.Listen("tcp", addr)
		if err != nil {
			_ = set.Close()
			return nil, fmt.Errorf("listen windows-hyperv structured exec %s: %w", addr, err)
		}
		set.listeners = append(set.listeners, l)
		started++
		go serveTCPToHVSockForward(l, vmID, uint32(guestExecPort(*req.Config)), "structured exec")
	}
	// Egress mediator front-end: when mediation is active for this workspace, bind
	// a dedicated per-VM hvsock service. The guest-side forwarder ships each
	// outbound connection — prefixed with an egress DestHeader naming the original
	// destination — to this port, and the host runs the internal/egress core on it
	// (policy/allowlist/TLS interception/audit/caps) with the destination injected
	// from the header. Build the egress.Handler from req.Config's egress fields +
	// the per-workspace CA cert/key, mirroring the firecracker --egress-mediator
	// wiring. Fail closed: a bind error closes the set and fails the start, like
	// every other listener here, so a mediated workspace never boots with the
	// mediator service silently absent.
	if egressMediationActive(req.Config) {
		handler, closeHandler, err := buildEgressHandler(req)
		if err != nil {
			_ = set.Close()
			return nil, fmt.Errorf("build windows-hyperv egress mediator: %w", err)
		}
		mediator := newEgressMediator(handler, req)
		l, err := winio.ListenHvsock(&winio.HvsockAddr{
			VMID:      vmID,
			ServiceID: winio.VsockServiceID(egress.DefaultMediatorVsockPort),
		})
		if err != nil {
			closeHandler()
			_ = set.Close()
			return nil, fmt.Errorf("listen windows-hyperv egress mediator hvsocket port %d: %w", egress.DefaultMediatorVsockPort, err)
		}
		set.listeners = append(set.listeners, l)
		started++
		go func(l net.Listener) {
			defer closeHandler()
			for {
				conn, err := l.Accept()
				if err != nil {
					return
				}
				go serveEgressMediatorConn(conn, mediator)
			}
		}(l)
	}
	if started == 0 {
		return nil, nil
	}
	return set, nil
}

// buildEgressHandler constructs the per-workspace egress.Handler the mediator
// front-end runs on each accepted hvsock stream. It mirrors the firecracker
// --egress-mediator wiring (egress.Options -> Handler): the same enforcement
// mode, EgressAllow allowlist policy, EgressPassthrough non-intercepted hosts,
// per-workspace CA (cert+key) for per-SNI TLS interception, bounded-operations
// caps, and a JSONL audit logger. OrigDst and Dial are left for the
// per-connection handler to inject (the header destination + a real net dialer),
// so this builds everything that is per-workspace and reused across connections.
// The returned close func releases the audit logger.
func buildEgressHandler(req vmkit.Request) (*egress.Handler, func(), error) {
	config := req.Config
	mode := vmkit.NormalizeEgressMode(config.EgressMode)
	policy, err := egress.NewPolicy(config.EgressAllow)
	if err != nil {
		return nil, nil, fmt.Errorf("egress allowlist: %w", err)
	}
	var passthrough *egress.Policy
	if len(config.EgressPassthrough) > 0 {
		passthrough, err = egress.NewPolicy(config.EgressPassthrough)
		if err != nil {
			return nil, nil, fmt.Errorf("egress passthrough: %w", err)
		}
	}
	// Per-workspace CA for TLS interception: load the cert+key minted at start.
	// Both must be present (the mint writes both) — a half-present pair fails
	// closed rather than silently disabling MITM.
	certPEM, err := os.ReadFile(egressCACertPath(req))
	if err != nil {
		return nil, nil, fmt.Errorf("read egress CA cert: %w", err)
	}
	keyPEM, err := os.ReadFile(egressCAKeyPath(req))
	if err != nil {
		return nil, nil, fmt.Errorf("read egress CA key: %w", err)
	}
	ca, err := egress.LoadCA(certPEM, keyPEM)
	if err != nil {
		return nil, nil, err
	}
	// JSONL audit log, size-bounded when the caps request it (ASK tenet 8) —
	// identical selection to egress.Serve. Lives next to the workspace's other
	// runtime artifacts.
	auditPath := filepath.Join(runtimeDir(req), "egress-mediator-audit.jsonl")
	if err := os.MkdirAll(filepath.Dir(auditPath), 0o700); err != nil {
		return nil, nil, fmt.Errorf("create egress audit dir: %w", err)
	}
	var logger egress.Logger
	var closeLogger func()
	if config.EgressAuditMaxBytes > 0 {
		rl, err := egress.NewRotatingFileLogger(auditPath, config.EgressAuditMaxBytes, config.EgressAuditMaxBackups)
		if err != nil {
			return nil, nil, err
		}
		logger = rl
		closeLogger = func() { _ = rl.Close() }
	} else {
		fl, err := egress.NewFileLogger(auditPath)
		if err != nil {
			return nil, nil, err
		}
		logger = fl
		closeLogger = func() { _ = fl.Close() }
	}
	h := &egress.Handler{
		Mode:        mode,
		Policy:      policy,
		Passthrough: passthrough,
		CA:          ca,
		Logger:      logger,
		Dial:        net.Dial,
		// OrigDst recovers the per-stream destination from the destConn wrapper the
		// front-end hands to Handle. ONE shared Handler serves every connection so
		// its process-wide caps counters (MaxTotalBytes/MaxConcurrentConns) bound the
		// whole workspace — never copy the Handler per connection (the atomic
		// counters would split). The dst travels on the conn, not on a cloned field.
		OrigDst: func(c net.Conn) (netip.AddrPort, error) {
			if dc, ok := c.(*destConn); ok {
				return dc.dst, nil
			}
			return netip.AddrPort{}, fmt.Errorf("egress mediator: connection is not a destConn")
		},
		Limits: egress.Limits{
			MaxBytesPerSec:     config.EgressMaxBytesPerSec,
			MaxTotalBytes:      config.EgressMaxTotalBytes,
			MaxConcurrentConns: config.EgressMaxConcurrentConns,
		},
	}
	return h, closeLogger, nil
}

// destConn wraps a guest-forwarded egress stream with the original destination
// recovered from its DestHeader. The shared Handler's OrigDst type-asserts to
// this and returns dst, so a single Handler instance (with shared cap counters)
// can serve every connection while each connection still carries its own
// destination. It embeds net.Conn so all other I/O passes straight through to the
// underlying hvsock stream (the header has already been consumed before wrapping).
type destConn struct {
	net.Conn
	dst netip.AddrPort
}

// egressMediator bundles the shared egress.Handler with the per-workspace DNS
// upstream + forward round-trip the DNS-over-TCP path needs. The Handler carries
// the policy/allowlist/CA/caps/audit shared across every connection; resolver and
// dnsForward are only consulted for captured TCP/53 streams (DNS-over-TCP). One
// mediator instance serves every accepted connection so the Handler's atomic cap
// counters stay process-wide.
type egressMediator struct {
	handler  *egress.Handler
	resolver netip.AddrPort
	// dnsForward performs the resolver round-trip for a guest DNS-over-TCP query.
	// Injectable for tests; production wiring (newEgressMediator) sets it to
	// egress.DefaultDNSForwardTCP.
	dnsForward func(resolver netip.AddrPort, query []byte) ([]byte, error)
}

// newEgressMediator builds the per-workspace mediator: the shared handler plus
// the upstream resolver selected from the workspace's DNS config (the host's
// configured upstream, mirroring what the guest would have used over the NAT
// uplink before P5 removes it). DNS-over-TCP queries are forwarded to this
// upstream via egress.DefaultDNSForwardTCP.
func newEgressMediator(handler *egress.Handler, req vmkit.Request) *egressMediator {
	return &egressMediator{
		handler:    handler,
		resolver:   egressUpstreamResolver(req),
		dnsForward: egress.DefaultDNSForwardTCP,
	}
}

// egressUpstreamResolver picks the upstream DNS resolver the mediator forwards
// guest DNS-over-TCP queries to. It prefers the first valid entry in the
// workspace's network DNS list (the host-configured upstream, e.g. the NAT
// gateway or an HCS-vended server) and falls back to a public resolver
// (1.1.1.1:53) when none is configured or parseable — so a mediated workspace
// can always resolve even if the network record carried no DNS server. The
// returned AddrPort always targets port 53.
func egressUpstreamResolver(req vmkit.Request) netip.AddrPort {
	const fallback = "1.1.1.1"
	server := fallback
	if req.Config != nil && req.Config.Network != nil {
		for _, d := range req.Config.Network.DNS {
			if ip, err := netip.ParseAddr(strings.TrimSpace(d)); err == nil {
				server = ip.String()
				break
			}
		}
	}
	ip, err := netip.ParseAddr(server)
	if err != nil {
		ip = netip.MustParseAddr(fallback)
	}
	return netip.AddrPortFrom(ip, 53)
}

// serveEgressMediatorConn services one guest-forwarded egress stream: it reads
// the egress DestHeader (the original destination the guest observed) off the
// front of the stream, then either (a) handles it as DNS-over-TCP when the
// header names TCP/53 — de-framing the query, resolving it through the shared
// core's filtering resolver-forwarder (handleDNS: policy + REFUSED synthesis +
// NameCache + audit) against the workspace upstream, and framing the response
// back — or (b) runs the shared internal/egress TCP/TLS core on the remainder
// with OrigDst injected to return the header's destination. DNS is deliberately
// kept off the TLS-MITM path (it is not TLS). A malformed/truncated header
// closes the conn fail-closed without dialing or resolving anything.
func serveEgressMediatorConn(conn net.Conn, m *egressMediator) {
	hdr, err := egress.ReadDestHeader(conn)
	if err != nil {
		fmt.Fprintf(os.Stderr, "read egress dest header from guest: %v\n", err)
		_ = conn.Close()
		return
	}
	// DNS-over-TCP: a captured TCP/53 stream is the guest's resolver (forced onto
	// TCP via resolv.conf "options use-vc"). Resolve it through the filtering
	// resolver-forwarder instead of the TLS-MITM/L4-splice path. The handler is
	// the single shared instance — handleDNS only touches policy/NameCache/audit,
	// never the cap counters, so sharing it here is correct.
	if hdr.Proto == "tcp" && hdr.Port == 53 {
		defer func() { _ = conn.Close() }()
		if err := m.handler.HandleDNSOverTCP(conn, m.resolver, m.dnsForward); err != nil {
			fmt.Fprintf(os.Stderr, "mediate egress DNS-over-TCP: %v\n", err)
		}
		return
	}
	dst, err := destHeaderAddrPort(hdr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "resolve egress dest header %s:%d: %v\n", hdr.Host, hdr.Port, err)
		_ = conn.Close()
		return
	}
	// Wrap the post-header stream so the SHARED handler's OrigDst recovers THIS
	// stream's destination from the conn — never copy the Handler (its atomic cap
	// counters are process-wide and must stay shared). Handle closes the wrapper
	// (and thus conn) on return.
	m.handler.Handle(&destConn{Conn: conn, dst: dst})
}

// destHeaderAddrPort converts a guest DestHeader into the netip.AddrPort the
// egress core's OrigDst contract returns. The guest sends the destination it
// observed; when it is an IP literal it is used directly, otherwise it is
// resolved to an IP here so the mediator has a concrete dial target while still
// policing by the sniffed SNI/Host. The header host is preserved for audit via
// the handler's own host sniffing.
func destHeaderAddrPort(hdr egress.DestHeader) (netip.AddrPort, error) {
	if ip, err := netip.ParseAddr(strings.TrimSpace(hdr.Host)); err == nil {
		return netip.AddrPortFrom(ip, hdr.Port), nil
	}
	ips, err := net.LookupIP(hdr.Host)
	if err != nil {
		return netip.AddrPort{}, err
	}
	for _, ip := range ips {
		if v4 := ip.To4(); v4 != nil {
			addr, ok := netip.AddrFromSlice(v4)
			if ok {
				return netip.AddrPortFrom(addr, hdr.Port), nil
			}
		}
	}
	return netip.AddrPort{}, fmt.Errorf("no IPv4 address for egress dest host %q", hdr.Host)
}

func hasResultTarget(req vmkit.Request) bool {
	if req.Config == nil || req.Identity == nil {
		return false
	}
	for _, listener := range req.Config.VsockListeners {
		if listener.Target == resultPath(req) {
			return true
		}
	}
	return false
}

func isAllowedHVSockTarget(req vmkit.Request, target string) bool {
	if _, ok := parseTCPAddr(target); ok {
		return true
	}
	if target == secretxfer.ServerTarget {
		return true
	}
	if target == secretxfer.CACertTarget {
		return true
	}
	return target == resultPath(req)
}

// serveCACertConn sends the egress CA certificate PEM (at caCertPath) to conn
// using the secretxfer length-prefix framing, then closes conn. If the file is
// absent or unreadable, the error is logged to stderr and the conn is closed
// without writing any data.
func serveCACertConn(conn net.Conn, caCertPath string) {
	defer func() { _ = conn.Close() }()
	pem, err := os.ReadFile(caCertPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "read egress-ca.pem for hvsock guest: %v\n", err)
		return
	}
	if err := secretxfer.ServeCACert(conn, pem); err != nil {
		fmt.Fprintf(os.Stderr, "serve egress CA to hvsock guest: %v\n", err)
	}
}

func copySerialPipe(pipePath, target string) {
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		return
	}
	file, err := os.OpenFile(target, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer func() { _ = file.Close() }()
	timeout := 30 * time.Second
	conn, err := winio.DialPipe(pipePath, &timeout)
	if err != nil {
		return
	}
	defer func() { _ = conn.Close() }()
	_, _ = io.Copy(file, conn)
}

func (s *hvSocketListenerSet) serve(listener net.Listener, target string, resultTarget bool) {
	for {
		conn, err := listener.Accept()
		if err != nil {
			if resultTarget {
				s.result <- err
			}
			return
		}
		if resultTarget {
			s.acceptResult(conn, target)
			return
		}
		go handleHVSockConnection(conn, target)
	}
}

func (s *hvSocketListenerSet) acceptResult(conn net.Conn, target string) {
	defer func() { _ = conn.Close() }()
	s.result <- writeHVSockResult(conn, target)
}

func handleHVSockConnection(conn net.Conn, target string) {
	defer func() { _ = conn.Close() }()
	if tcpTarget, ok := parseTCPAddr(target); ok {
		remote, err := net.Dial("tcp", tcpTarget)
		if err != nil {
			fmt.Fprintf(os.Stderr, "connect windows-hyperv hvsocket target %s: %v\n", tcpTarget, err)
			return
		}
		defer func() { _ = remote.Close() }()
		go func() {
			_, _ = io.Copy(remote, conn)
			closeWriteConn(remote)
		}()
		_, _ = io.Copy(conn, remote)
		closeWriteConn(conn)
		return
	}
	if err := writeHVSockResult(conn, target); err != nil {
		fmt.Fprintf(os.Stderr, "write windows-hyperv hvsocket result %s: %v\n", target, err)
	}
}

func servePublishedPortForward(listener net.Listener, vmID guid.GUID, forward vmkit.PortForward) {
	serveTCPToHVSockForward(listener, vmID, uint32(forward.HostPort), "published tcp")
}

func serveTCPToHVSockForward(listener net.Listener, vmID guid.GUID, guestPort uint32, label string) {
	for {
		conn, err := listener.Accept()
		if err != nil {
			fmt.Fprintf(os.Stderr, "accept windows-hyperv %s connection: %v\n", label, err)
			return
		}
		go proxyTCPToHVSock(conn, vmID, guestPort)
	}
}

func proxyTCPToHVSock(conn net.Conn, vmID guid.GUID, guestPort uint32) {
	defer func() { _ = conn.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	hvsock, err := dialHVSockPortHook(ctx, vmID, guestPort)
	if err != nil {
		fmt.Fprintf(os.Stderr, "connect windows-hyperv guest hvsocket port %d: %v\n", guestPort, err)
		return
	}
	defer func() { _ = hvsock.Close() }()
	done := make(chan struct{}, 2)
	go func() {
		_, _ = io.Copy(hvsock, conn)
		closeWriteConn(hvsock)
		done <- struct{}{}
	}()
	go func() {
		_, _ = io.Copy(conn, hvsock)
		closeWriteConn(conn)
		done <- struct{}{}
	}()
	<-done
	_ = conn.Close()
	_ = hvsock.Close()
}

func dialHVSockPort(ctx context.Context, vmID guid.GUID, guestPort uint32) (net.Conn, error) {
	return winio.Dial(ctx, &winio.HvsockAddr{
		VMID:      vmID,
		ServiceID: winio.VsockServiceID(guestPort),
	})
}

// shellHVSockProbeHook lets tests substitute a deterministic shell probe.
var shellHVSockProbeHook = probeShellHVSock

// probeShellHVSock dials the guest shell service over hv_sock and reports how
// long the dial took. A successful dial means the guest shell channel accepts.
// The hv_sock transport can hold a connect attempt far past context
// cancellation while the guest is still booting, so the dial runs in its own
// goroutine and the probe returns at the timeout regardless.
func probeShellHVSock(ctx context.Context, state runtimeState, timeout time.Duration) (time.Duration, error) {
	runtimeID := strings.TrimSpace(state.ComputeSystemRuntimeID)
	if runtimeID == "" {
		return 0, fmt.Errorf("windows-hyperv shell probe requires compute system runtime ID in runtime state")
	}
	vmID, err := guid.FromString(runtimeID)
	if err != nil {
		return 0, fmt.Errorf("parse HCS runtime ID %q: %w", runtimeID, err)
	}
	dialCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	start := time.Now()
	type dialResult struct {
		conn net.Conn
		err  error
	}
	resultCh := make(chan dialResult, 1)
	go func() {
		conn, err := dialHVSockPortHook(dialCtx, vmID, uint32(guestShellPort(state.Config)))
		resultCh <- dialResult{conn: conn, err: err}
	}()
	select {
	case result := <-resultCh:
		elapsed := time.Since(start)
		if result.err != nil {
			return elapsed, result.err
		}
		_ = result.conn.Close()
		return elapsed, nil
	case <-dialCtx.Done():
		// Reap the dial if it ever completes so the connection does not leak.
		go func() {
			if result := <-resultCh; result.err == nil {
				_ = result.conn.Close()
			}
		}()
		return time.Since(start), fmt.Errorf("dial timed out after %s: %w", timeout, dialCtx.Err())
	}
}

func writeHVSockResult(conn net.Conn, target string) error {
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		return err
	}
	tmp := target + ".tmp"
	file, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(file, io.LimitReader(conn, maxWindowsHyperVResultBytes+1))
	closeErr := file.Close()
	if copyErr != nil {
		_ = os.Remove(tmp)
		return copyErr
	}
	if closeErr != nil {
		_ = os.Remove(tmp)
		return closeErr
	}
	info, err := os.Stat(tmp)
	if err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if info.Size() > maxWindowsHyperVResultBytes {
		_ = os.Remove(tmp)
		return fmt.Errorf("windows-hyperv result for %s exceeded %d bytes", target, maxWindowsHyperVResultBytes)
	}
	return os.Rename(tmp, target)
}

func (s *hvSocketListenerSet) Wait(ctx context.Context) error {
	if s.result == nil {
		return nil
	}
	select {
	case err := <-s.result:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *hvSocketListenerSet) Close() error {
	var err error
	s.once.Do(func() {
		for _, listener := range s.listeners {
			if closeErr := listener.Close(); closeErr != nil && err == nil {
				err = closeErr
			}
		}
	})
	return err
}

func parseTCPAddr(target string) (string, bool) {
	host, port, err := net.SplitHostPort(target)
	if err != nil || host == "" || port == "" || strings.ContainsAny(host, `/\`) {
		return "", false
	}
	if _, err := strconv.ParseUint(port, 10, 16); err != nil {
		return "", false
	}
	return net.JoinHostPort(host, port), true
}

func closeWriteConn(conn net.Conn) {
	type closeWriter interface {
		CloseWrite() error
	}
	if writer, ok := conn.(closeWriter); ok {
		_ = writer.CloseWrite()
	}
}
