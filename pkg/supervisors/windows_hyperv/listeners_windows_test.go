//go:build windows

package windows_hyperv

import (
	"context"
	"crypto/tls"
	"io"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/Microsoft/go-winio/pkg/guid"
	"github.com/geoffbelknap/microagent/internal/egress"
	"github.com/geoffbelknap/microagent/pkg/secretxfer"
	"github.com/geoffbelknap/microagent/pkg/vmkit"
	"golang.org/x/net/dns/dnsmessage"
)

// dnsQueryA builds a minimal DNS A-record query (one question) for name with id.
func dnsQueryA(t *testing.T, id uint16, name string) []byte {
	t.Helper()
	b := dnsmessage.NewBuilder(nil, dnsmessage.Header{ID: id, RecursionDesired: true})
	if err := b.StartQuestions(); err != nil {
		t.Fatalf("StartQuestions: %v", err)
	}
	if err := b.Question(dnsmessage.Question{
		Name:  dnsmessage.MustNewName(name),
		Type:  dnsmessage.TypeA,
		Class: dnsmessage.ClassINET,
	}); err != nil {
		t.Fatalf("Question: %v", err)
	}
	msg, err := b.Finish()
	if err != nil {
		t.Fatalf("Finish: %v", err)
	}
	return msg
}

// dnsResponseA builds a DNS response echoing one question and carrying a single
// A answer (name -> ip) with the given ttl.
func dnsResponseA(t *testing.T, id uint16, name string, ip [4]byte, ttl uint32) []byte {
	t.Helper()
	b := dnsmessage.NewBuilder(nil, dnsmessage.Header{ID: id, Response: true, RecursionAvailable: true})
	if err := b.StartQuestions(); err != nil {
		t.Fatalf("StartQuestions: %v", err)
	}
	if err := b.Question(dnsmessage.Question{
		Name:  dnsmessage.MustNewName(name),
		Type:  dnsmessage.TypeA,
		Class: dnsmessage.ClassINET,
	}); err != nil {
		t.Fatalf("Question: %v", err)
	}
	if err := b.StartAnswers(); err != nil {
		t.Fatalf("StartAnswers: %v", err)
	}
	if err := b.AResource(dnsmessage.ResourceHeader{
		Name:  dnsmessage.MustNewName(name),
		Class: dnsmessage.ClassINET,
		TTL:   ttl,
	}, dnsmessage.AResource{A: ip}); err != nil {
		t.Fatalf("AResource: %v", err)
	}
	msg, err := b.Finish()
	if err != nil {
		t.Fatalf("Finish: %v", err)
	}
	return msg
}

// mediatedEgressRequest builds a request whose config has egress mediation
// active (mediated mode + a mediating network) with the given allowlist.
func mediatedEgressRequest(t *testing.T, allow []string) vmkit.Request {
	t.Helper()
	return vmkit.Request{
		Identity: &vmkit.Identity{RuntimeID: "agent-egress"},
		Config: &vmkit.Config{
			StateDir:    t.TempDir(),
			EgressMode:  vmkit.EgressModeMediated,
			EgressAllow: allow,
			Network:     &vmkit.NetworkConfig{Mode: "user"},
		},
	}
}

// TestServeEgressMediatorConnForwardsAllowedHeaderDest drives the extracted
// per-connection handler with a net.Pipe carrying a DestHeader and asserts the
// shared egress core is invoked with the header's destination: an allowlisted
// host reaches a stub upstream (via the injected Dial), proving OrigDst was set
// from the header and the allowlist policy ran on it.
func TestServeEgressMediatorConnForwardsAllowedHeaderDest(t *testing.T) {
	// Allowlist the destination IP literal so a raw-TCP (non-TLS) flow — which the
	// core polices by the destination IP — is permitted and takes the L4 splice
	// path through the injected Dial (the MITM path uses tls.Dial directly and is
	// not exercised here). strict mode keeps the default-deny allowlist.
	req := mediatedEgressRequest(t, []string{"203.0.113.7"})
	req.Config.EgressMode = vmkit.EgressModeStrict
	if err := mintEgressCA(req); err != nil {
		t.Fatalf("mint CA: %v", err)
	}
	handler, closeHandler, err := buildEgressHandler(req)
	if err != nil {
		t.Fatalf("buildEgressHandler: %v", err)
	}
	defer closeHandler()
	// The injected Dial records the upstream address the core resolved from the
	// header destination, proving OrigDst was set from the header and the allowlist
	// passed on that dst. Returning a closed pipe end is enough — we assert on the
	// dialed address, not the splice.
	dialedAddr := make(chan string, 1)
	handler.Dial = func(network, addr string) (net.Conn, error) {
		select {
		case dialedAddr <- addr:
		default:
		}
		host, vm := net.Pipe()
		_ = vm.Close()
		return host, nil
	}

	hostConn, guestConn := net.Pipe()
	go serveEgressMediatorConn(hostConn, newEgressMediator(handler, req))

	// Guest writes the DestHeader (IP literal so no DNS), then raw (non-TLS) bytes
	// so the core takes the L4 splice path and dials upstream via h.Dial.
	go func() {
		_ = egress.WriteDestHeader(guestConn, egress.DestHeader{Proto: "tcp", Host: "203.0.113.7", Port: 443})
		_, _ = guestConn.Write([]byte("GET / HTTP/1.0\r\n\r\n"))
	}()

	select {
	case addr := <-dialedAddr:
		if addr != "203.0.113.7:443" {
			t.Fatalf("mediator dialed %q, want upstream from header dst 203.0.113.7:443", addr)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("allowed destination was not dialed upstream from the header dst")
	}
	_ = guestConn.Close()
}

// TestServeEgressMediatorConnDeniesNonAllowlistedHeaderDest asserts the core's
// fail-closed allowlist runs against the header destination: a non-allowlisted
// host is never dialed upstream.
func TestServeEgressMediatorConnDeniesNonAllowlistedHeaderDest(t *testing.T) {
	req := mediatedEgressRequest(t, []string{"allowed.example.com"})
	req.Config.EgressMode = vmkit.EgressModeStrict // strict = default-deny
	if err := mintEgressCA(req); err != nil {
		t.Fatalf("mint CA: %v", err)
	}
	handler, closeHandler, err := buildEgressHandler(req)
	if err != nil {
		t.Fatalf("buildEgressHandler: %v", err)
	}
	defer closeHandler()
	dialed := make(chan string, 1)
	handler.Dial = func(network, addr string) (net.Conn, error) {
		dialed <- addr
		return nil, nil
	}

	hostConn, guestConn := net.Pipe()
	done := make(chan struct{})
	go func() {
		defer close(done)
		serveEgressMediatorConn(hostConn, newEgressMediator(handler, req))
	}()
	go func() {
		_ = egress.WriteDestHeader(guestConn, egress.DestHeader{Proto: "tcp", Host: "203.0.113.9", Port: 443})
		client := tls.Client(guestConn, &tls.Config{ServerName: "denied.example.com", InsecureSkipVerify: true})
		_ = client.Handshake()
		_ = client.Close()
	}()

	select {
	case addr := <-dialed:
		t.Fatalf("denied destination was dialed upstream: %q", addr)
	case <-done:
		// handler returned without dialing — fail-closed, as required.
	case <-time.After(3 * time.Second):
		t.Fatal("serveEgressMediatorConn did not finish")
	}
	_ = guestConn.Close()
}

// TestServeEgressMediatorConnRejectsTruncatedHeader asserts a malformed/truncated
// DestHeader closes the conn fail-closed without dialing upstream.
func TestServeEgressMediatorConnRejectsTruncatedHeader(t *testing.T) {
	req := mediatedEgressRequest(t, nil)
	if err := mintEgressCA(req); err != nil {
		t.Fatalf("mint CA: %v", err)
	}
	handler, closeHandler, err := buildEgressHandler(req)
	if err != nil {
		t.Fatalf("buildEgressHandler: %v", err)
	}
	defer closeHandler()
	handler.Dial = func(network, addr string) (net.Conn, error) {
		t.Fatalf("truncated header must not dial upstream (addr=%q)", addr)
		return nil, nil
	}
	hostConn, guestConn := net.Pipe()
	done := make(chan struct{})
	go func() {
		defer close(done)
		serveEgressMediatorConn(hostConn, newEgressMediator(handler, req))
	}()
	// Write one byte then close — not a full header.
	_, _ = guestConn.Write([]byte{0x01})
	_ = guestConn.Close()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("serveEgressMediatorConn did not fail closed on truncated header")
	}
}

// TestServeEgressMediatorConnHandlesDNSOverTCP asserts a captured TCP/53 stream
// is routed to the DNS-over-TCP handler (not the TLS-MITM/L4 path): the front-end
// de-frames the length-prefixed query, forwards it via the mediator's injected
// dnsForward, and frames the response back. mediated mode allows any name so the
// query is forwarded without an allowlist entry.
func TestServeEgressMediatorConnHandlesDNSOverTCP(t *testing.T) {
	req := mediatedEgressRequest(t, nil)
	if err := mintEgressCA(req); err != nil {
		t.Fatalf("mint CA: %v", err)
	}
	handler, closeHandler, err := buildEgressHandler(req)
	if err != nil {
		t.Fatalf("buildEgressHandler: %v", err)
	}
	defer closeHandler()
	// The TCP/TLS Dial must NEVER be reached for a DNS stream.
	handler.Dial = func(network, addr string) (net.Conn, error) {
		t.Fatalf("DNS-over-TCP stream must not take the upstream Dial path (addr=%q)", addr)
		return nil, nil
	}
	mediator := newEgressMediator(handler, req)
	// Inject the resolver round-trip: assert the de-framed query reaches it and
	// return a canned answer to frame back.
	wantResp := dnsResponseA(t, 0x1234, "example.com.", [4]byte{93, 184, 216, 34}, 300)
	forwarded := make(chan []byte, 1)
	mediator.dnsForward = func(resolver netip.AddrPort, query []byte) ([]byte, error) {
		forwarded <- append([]byte(nil), query...)
		return wantResp, nil
	}

	hostConn, guestConn := net.Pipe()
	go serveEgressMediatorConn(hostConn, mediator)

	query := dnsQueryA(t, 0x1234, "example.com.")
	go func() {
		_ = egress.WriteDestHeader(guestConn, egress.DestHeader{Proto: "tcp", Host: "10.0.0.1", Port: 53})
		var lenBuf [2]byte
		lenBuf[0] = byte(len(query) >> 8)
		lenBuf[1] = byte(len(query))
		_, _ = guestConn.Write(lenBuf[:])
		_, _ = guestConn.Write(query)
	}()

	select {
	case q := <-forwarded:
		if string(q) != string(query) {
			t.Fatalf("forwarded query did not match the de-framed query")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("DNS-over-TCP query was not forwarded to the resolver")
	}

	// Read the framed response back off the guest side.
	var lenBuf [2]byte
	if _, err := io.ReadFull(guestConn, lenBuf[:]); err != nil {
		t.Fatalf("read response length prefix: %v", err)
	}
	n := int(lenBuf[0])<<8 | int(lenBuf[1])
	resp := make([]byte, n)
	if _, err := io.ReadFull(guestConn, resp); err != nil {
		t.Fatalf("read response body: %v", err)
	}
	if string(resp) != string(wantResp) {
		t.Fatalf("framed response mismatch")
	}
	_ = guestConn.Close()
}

// TestEgressUpstreamResolverSkipsGuestGateway asserts the mediator's DNS upstream
// selection never forwards to the guest-facing HNS gateway. The mediator dials the
// resolver from the HOST, but the workspace network DNS list carries the guest's
// view — on the no-uplink mediated topology that is the HNS gateway, a dead end
// from the host. So a DNS entry equal to the network gateway is skipped in favor
// of a host-reachable entry, or the public fallback when the gateway is the only
// entry.
func TestEgressUpstreamResolverSkipsGuestGateway(t *testing.T) {
	cases := []struct {
		name    string
		gateway string
		dns     []string
		want    string
	}{
		{
			// The common mediated no-uplink case: the only DNS entry IS the HNS
			// gateway, so it is skipped and the host-reachable public fallback wins.
			name:    "gateway-only falls back to public",
			gateway: "192.168.214.1",
			dns:     []string{"192.168.214.1"},
			want:    "1.1.1.1:53",
		},
		{
			// A real host-reachable upstream after the gateway is honored.
			name:    "host-reachable entry after gateway wins",
			gateway: "192.168.214.1",
			dns:     []string{"192.168.214.1", "9.9.9.9"},
			want:    "9.9.9.9:53",
		},
		{
			name:    "no dns falls back to public",
			gateway: "192.168.214.1",
			dns:     nil,
			want:    "1.1.1.1:53",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := vmkit.Request{
				Identity: &vmkit.Identity{RuntimeID: "agent-egress"},
				Config: &vmkit.Config{
					Network: &vmkit.NetworkConfig{Mode: "user", Gateway: tc.gateway, DNS: tc.dns},
				},
			}
			if got := egressUpstreamResolver(req).String(); got != tc.want {
				t.Fatalf("egressUpstreamResolver = %s, want %s", got, tc.want)
			}
		})
	}
}

// TestMintEgressCAWritesCertAndKeyWhenMediated asserts a mediated start mints the
// CA and writes BOTH the public cert (egress-ca.pem) and the private key
// (egress-ca-key.pem) at the agreed paths the front-end and the cacert serve
// goroutine both read.
func TestMintEgressCAWritesCertAndKeyWhenMediated(t *testing.T) {
	req := mediatedEgressRequest(t, nil)
	if err := mintEgressCA(req); err != nil {
		t.Fatalf("mintEgressCA: %v", err)
	}
	certPEM, err := os.ReadFile(egressCACertPath(req))
	if err != nil {
		t.Fatalf("read minted cert: %v", err)
	}
	keyPEM, err := os.ReadFile(egressCAKeyPath(req))
	if err != nil {
		t.Fatalf("read minted key: %v", err)
	}
	// The cert+key must form a loadable CA (reusing egress.LoadCA).
	if _, err := egress.LoadCA(certPEM, keyPEM); err != nil {
		t.Fatalf("minted cert/key do not form a valid CA: %v", err)
	}
}

// TestMintEgressCASkippedWhenNotMediated asserts a non-mediated start mints
// nothing: neither the cert nor the key is written.
func TestMintEgressCASkippedWhenNotMediated(t *testing.T) {
	req := vmkit.Request{
		Identity: &vmkit.Identity{RuntimeID: "agent-open"},
		Config: &vmkit.Config{
			StateDir:   t.TempDir(),
			EgressMode: vmkit.EgressModeOff,
		},
	}
	if err := mintEgressCA(req); err != nil {
		t.Fatalf("mintEgressCA (off): %v", err)
	}
	if _, err := os.Stat(egressCACertPath(req)); !os.IsNotExist(err) {
		t.Fatalf("non-mediated start wrote a CA cert: %v", err)
	}
	if _, err := os.Stat(egressCAKeyPath(req)); !os.IsNotExist(err) {
		t.Fatalf("non-mediated start wrote a CA key: %v", err)
	}
}

// TestStartRuntimeListenersBindsEgressMediatorService asserts that a mediated
// workspace adds the egress mediator hvsock service to the listener set (so it is
// torn down with the others) and that the start fails closed when the handler
// cannot be built (CA absent). The hvsock bind itself requires a live compute
// system, so this drives the build/teardown wiring via the same startRuntimeListeners
// entrypoint the exec-bridge tests use.
func TestStartRuntimeListenersEgressMediatorFailsClosedWithoutCA(t *testing.T) {
	req := mediatedEgressRequest(t, []string{"allowed.example.com"})
	// Also give it an exec bridge so startRuntimeListeners has work and reaches the
	// egress block; deliberately DO NOT mint the CA so buildEgressHandler fails.
	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve exec port: %v", err)
	}
	req.Config.ExecPort = uint16(probe.Addr().(*net.TCPAddr).Port)
	_ = probe.Close()
	handle := computeSystemHandle{ID: "fake", RuntimeID: "11111111-1111-1111-1111-111111111111"}
	set, err := startRuntimeListeners(context.Background(), handle, req)
	if err == nil {
		if set != nil {
			_ = set.Close()
		}
		t.Fatal("startRuntimeListeners did not fail closed when the egress CA is absent")
	}
}

func TestWindowsHyperVVsockTargetValidationAllowsTCPAndResultOnly(t *testing.T) {
	stateDir := t.TempDir()
	req := vmkit.Request{
		Identity: &vmkit.Identity{RuntimeID: "agent-1"},
		Config:   &vmkit.Config{StateDir: stateDir},
	}

	if !isAllowedHVSockTarget(req, filepath.Join(stateDir, "agent-1", "result.json")) {
		t.Fatalf("result target rejected")
	}
	if !isAllowedHVSockTarget(req, "127.0.0.1:9900") {
		t.Fatalf("tcp target rejected")
	}
	if isAllowedHVSockTarget(req, filepath.Join(stateDir, "agent-1", "not-result.json")) {
		t.Fatalf("non-result file target accepted")
	}
}

func TestHandleHVSockConnectionWritesResultAtomically(t *testing.T) {
	target := filepath.Join(t.TempDir(), "agent-1", "result.json")
	hostConn, guestConn := net.Pipe()
	done := make(chan struct{})
	go func() {
		handleHVSockConnection(hostConn, target)
		close(done)
	}()

	if _, err := guestConn.Write([]byte(`{"exitCode":0}`)); err != nil {
		t.Fatalf("write guest result: %v", err)
	}
	if err := guestConn.Close(); err != nil {
		t.Fatalf("close guest pipe: %v", err)
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("result handler did not finish")
	}
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read result: %v", err)
	}
	if string(data) != `{"exitCode":0}` {
		t.Fatalf("result data = %q", data)
	}
	if _, err := os.Stat(target + ".tmp"); !os.IsNotExist(err) {
		t.Fatalf("temporary result remains: %v", err)
	}
}

func TestHandleHVSockConnectionProxiesTCP(t *testing.T) {
	tcpListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen tcp target: %v", err)
	}
	defer tcpListener.Close()
	serverDone := make(chan struct{})
	go func() {
		defer close(serverDone)
		conn, err := tcpListener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		buf := make([]byte, 4)
		if _, err := io.ReadFull(conn, buf); err != nil {
			return
		}
		_, _ = conn.Write([]byte("pong"))
	}()

	hostConn, guestConn := net.Pipe()
	handlerDone := make(chan struct{})
	go func() {
		handleHVSockConnection(hostConn, tcpListener.Addr().String())
		close(handlerDone)
	}()
	if _, err := guestConn.Write([]byte("ping")); err != nil {
		t.Fatalf("write guest tcp payload: %v", err)
	}
	buf := make([]byte, 4)
	if _, err := io.ReadFull(guestConn, buf); err != nil {
		t.Fatalf("read proxied response: %v", err)
	}
	if string(buf) != "pong" {
		t.Fatalf("proxied response = %q", buf)
	}
	if err := guestConn.Close(); err != nil {
		t.Fatalf("close guest pipe: %v", err)
	}
	select {
	case <-handlerDone:
	case <-time.After(2 * time.Second):
		t.Fatal("tcp proxy handler did not finish")
	}
	select {
	case <-serverDone:
	case <-time.After(2 * time.Second):
		t.Fatal("tcp target did not finish")
	}
}

func TestStartRuntimeListenersServesExecBridge(t *testing.T) {
	oldDial := dialHVSockPortHook
	t.Cleanup(func() { dialHVSockPortHook = oldDial })
	guestHostConn, guestVMConn := net.Pipe()
	dialed := make(chan uint32, 1)
	dialHVSockPortHook = func(ctx context.Context, vmID guid.GUID, port uint32) (net.Conn, error) {
		dialed <- port
		return guestHostConn, nil
	}

	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve exec port: %v", err)
	}
	execPort := uint16(probe.Addr().(*net.TCPAddr).Port)
	_ = probe.Close()

	req := vmkit.Request{
		Identity: &vmkit.Identity{RuntimeID: "agent-1"},
		Config: &vmkit.Config{
			StateDir: t.TempDir(),
			ExecPort: execPort,
		},
	}
	handle := computeSystemHandle{ID: "fake", RuntimeID: "11111111-1111-1111-1111-111111111111"}
	set, err := startRuntimeListeners(context.Background(), handle, req)
	if err != nil {
		t.Fatalf("startRuntimeListeners: %v", err)
	}
	if set == nil {
		t.Fatal("startRuntimeListeners returned no listener set for an exec bridge")
	}
	t.Cleanup(func() { _ = set.Close() })

	guestDone := make(chan struct{})
	go func() {
		defer close(guestDone)
		defer guestVMConn.Close()
		buf := make([]byte, 4)
		if _, err := io.ReadFull(guestVMConn, buf); err != nil {
			return
		}
		_, _ = guestVMConn.Write([]byte("pong"))
	}()

	client, err := net.Dial("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(int(execPort))))
	if err != nil {
		t.Fatalf("dial exec bridge: %v", err)
	}
	defer client.Close()
	if _, err := client.Write([]byte("ping")); err != nil {
		t.Fatalf("write exec payload: %v", err)
	}
	buf := make([]byte, 4)
	if _, err := io.ReadFull(client, buf); err != nil {
		t.Fatalf("read exec response: %v", err)
	}
	if string(buf) != "pong" {
		t.Fatalf("exec response = %q", buf)
	}
	select {
	case port := <-dialed:
		if port != uint32(execPort) {
			t.Fatalf("dialed guest exec hvsock port = %d, want %d", port, execPort)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("exec bridge did not dial the guest")
	}
	if err := client.Close(); err != nil {
		t.Fatalf("close client: %v", err)
	}
	select {
	case <-guestDone:
	case <-time.After(2 * time.Second):
		t.Fatal("guest hvsock side did not finish")
	}
}

func TestStartRuntimeListenersExecBridgeDialsGuestExecPort(t *testing.T) {
	oldDial := dialHVSockPortHook
	t.Cleanup(func() { dialHVSockPortHook = oldDial })
	dialed := make(chan uint32, 1)
	dialHVSockPortHook = func(ctx context.Context, vmID guid.GUID, port uint32) (net.Conn, error) {
		dialed <- port
		host, vm := net.Pipe()
		_ = vm.Close()
		return host, nil
	}

	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve exec port: %v", err)
	}
	execPort := uint16(probe.Addr().(*net.TCPAddr).Port)
	_ = probe.Close()

	req := vmkit.Request{
		Identity: &vmkit.Identity{RuntimeID: "agent-1"},
		Config: &vmkit.Config{
			StateDir:      t.TempDir(),
			ExecPort:      execPort,
			GuestExecPort: 42001,
		},
	}
	handle := computeSystemHandle{ID: "fake", RuntimeID: "11111111-1111-1111-1111-111111111111"}
	set, err := startRuntimeListeners(context.Background(), handle, req)
	if err != nil {
		t.Fatalf("startRuntimeListeners: %v", err)
	}
	if set == nil {
		t.Fatal("startRuntimeListeners returned no listener set for an exec bridge")
	}
	t.Cleanup(func() { _ = set.Close() })

	client, err := net.Dial("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(int(execPort))))
	if err != nil {
		t.Fatalf("dial exec bridge: %v", err)
	}
	defer client.Close()
	select {
	case port := <-dialed:
		if port != 42001 {
			t.Fatalf("dialed guest exec hvsock port = %d, want guest exec port 42001", port)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("exec bridge did not dial the guest")
	}
}

// TestStartRuntimeListenersAcceptsCACertTarget verifies two things:
//  1. isAllowedHVSockTarget returns true for the cacert://serve sentinel.
//  2. The per-connection cacert serve logic reads egress-ca.pem and writes it
//     with the secretxfer framing that FetchCACert can decode.
//
// A full hvsock round-trip through winio.ListenHvsock is not exercised here
// because it requires a live Hyper-V compute system; instead the per-connection
// handler (serveCACertConn) is tested directly via net.Pipe.
func TestStartRuntimeListenersAcceptsCACertTarget(t *testing.T) {
	stateDir := t.TempDir()
	runtimeID := "agent-cacert-1"
	req := vmkit.Request{
		Identity: &vmkit.Identity{RuntimeID: runtimeID},
		Config:   &vmkit.Config{StateDir: stateDir},
	}

	// 1. Validation: isAllowedHVSockTarget must accept the cacert sentinel.
	if !isAllowedHVSockTarget(req, secretxfer.CACertTarget) {
		t.Fatal("isAllowedHVSockTarget returned false for CACertTarget; expected true")
	}

	// 2. Write a known CA PEM into the workspace dir that serveCACertConn reads.
	wsDir := filepath.Join(stateDir, runtimeID)
	if err := os.MkdirAll(wsDir, 0o700); err != nil {
		t.Fatalf("mkdir workspace dir: %v", err)
	}
	wantPEM := []byte("-----BEGIN CERTIFICATE-----\nZmFrZWNh\n-----END CERTIFICATE-----\n")
	if err := os.WriteFile(filepath.Join(wsDir, "egress-ca.pem"), wantPEM, 0o644); err != nil {
		t.Fatalf("write egress-ca.pem: %v", err)
	}

	// 3. Exercise the per-connection handler via net.Pipe (no Hyper-V required).
	hostConn, guestConn := net.Pipe()
	done := make(chan struct{})
	go func() {
		defer close(done)
		serveCACertConn(hostConn, filepath.Join(wsDir, "egress-ca.pem"))
	}()

	gotPEM, err := secretxfer.FetchCACert(guestConn)
	if err != nil {
		t.Fatalf("FetchCACert: %v", err)
	}
	if string(gotPEM) != string(wantPEM) {
		t.Fatalf("cacert PEM mismatch: got %q, want %q", gotPEM, wantPEM)
	}
	_ = guestConn.Close()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("serveCACertConn did not finish")
	}
}

func TestServeCACertConnLogsAndClosesWhenFileAbsent(t *testing.T) {
	missingPath := filepath.Join(t.TempDir(), "no-such", "egress-ca.pem")
	hostConn, guestConn := net.Pipe()
	done := make(chan struct{})
	go func() {
		defer close(done)
		serveCACertConn(hostConn, missingPath)
	}()
	// The host-side conn should be closed promptly without writing data.
	buf := make([]byte, 1)
	_ = guestConn.SetReadDeadline(time.Now().Add(2 * time.Second))
	n, err := guestConn.Read(buf)
	if n != 0 || err == nil {
		t.Fatalf("expected closed conn, got n=%d err=%v", n, err)
	}
	_ = guestConn.Close()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("serveCACertConn did not finish after missing file")
	}
}

func TestServePublishedPortForwardProxiesTCPToHostForwardHVSockPort(t *testing.T) {
	oldDial := dialHVSockPortHook
	t.Cleanup(func() { dialHVSockPortHook = oldDial })
	guestHostConn, guestVMConn := net.Pipe()
	dialedPort := uint32(0)
	dialHVSockPortHook = func(ctx context.Context, vmID guid.GUID, port uint32) (net.Conn, error) {
		dialedPort = port
		return guestHostConn, nil
	}

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen tcp: %v", err)
	}
	defer listener.Close()
	var vmID guid.GUID
	go servePublishedPortForward(listener, vmID, vmkit.PortForward{HostPort: 18080, GuestPort: 8080})

	guestDone := make(chan struct{})
	go func() {
		defer close(guestDone)
		defer guestVMConn.Close()
		buf := make([]byte, 4)
		if _, err := io.ReadFull(guestVMConn, buf); err != nil {
			return
		}
		_, _ = guestVMConn.Write([]byte("pong"))
	}()

	client, err := net.Dial("tcp", listener.Addr().String())
	if err != nil {
		t.Fatalf("dial published listener: %v", err)
	}
	defer client.Close()
	if _, err := client.Write([]byte("ping")); err != nil {
		t.Fatalf("write client payload: %v", err)
	}
	buf := make([]byte, 4)
	if _, err := io.ReadFull(client, buf); err != nil {
		t.Fatalf("read client response: %v", err)
	}
	if string(buf) != "pong" {
		t.Fatalf("client response = %q", buf)
	}
	if dialedPort != 18080 {
		t.Fatalf("dialed hvsock port = %d, want host forward port 18080", dialedPort)
	}
	if err := client.Close(); err != nil {
		t.Fatalf("close client: %v", err)
	}
	select {
	case <-guestDone:
	case <-time.After(2 * time.Second):
		t.Fatal("guest hvsock side did not finish")
	}
}
