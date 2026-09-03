package broker

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/geoffbelknap/microagent/internal/netlimit"
	"github.com/geoffbelknap/microagent/pkg/vmkit"
)

const (
	brokerReadHeaderTimeout = 10 * time.Second
	brokerReadTimeout       = 30 * time.Second
	brokerIdleTimeout       = 60 * time.Second
	brokerMaxHeaderBytes    = 64 << 10
)

// EndpointServerOptions wires one configured broker endpoint into a served
// listener with its decision/capture logs. It is the portable core shared by
// the Firecracker vsock companion and the apple-vf broker companion: both
// resolve the credential and the listener their own way, then hand the
// serving to StartEndpointServer so the two backends cannot drift.
type EndpointServerOptions struct {
	RuntimeID string
	SessionID string
	// Endpoint is the operator's broker endpoint declaration.
	Endpoint *vmkit.BrokerConfig
	// Resolve maps the endpoint's secret name to the live credential value,
	// which stays host-side only.
	Resolve SecretResolver
	// AccessLogPath receives one DecisionRecord per brokered request
	// (fail-closed: an unopenable log refuses to serve).
	AccessLogPath string
	// CaptureLogPath receives governed raw captures. Used only when the
	// endpoint declares Capture; fail-closed like the access log.
	CaptureLogPath string
	// IsInside classifies a resolved destination as inside/infrastructure for
	// the governed CONNECT tunnel (production passes the egress mediator's
	// classifier so the tunnel and the NIC datapath deny the same space).
	// Required when the endpoint enables Proxy.
	IsInside func(netip.Addr) bool
}

// StartEndpointServer serves one broker endpoint on listener. It returns
// after wiring the handler; serving continues until the listener closes, at
// which point the logs are closed too.
func StartEndpointServer(listener net.Listener, opts EndpointServerOptions) error {
	bc := opts.Endpoint
	if bc == nil {
		return fmt.Errorf("egress broker: no endpoint configured")
	}
	if err := vmkit.ValidateBrokerSecurity(bc); err != nil {
		return fmt.Errorf("egress broker: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(opts.AccessLogPath), 0o700); err != nil {
		return err
	}
	logFile, err := os.OpenFile(opts.AccessLogPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("egress broker: open access log: %w", err)
	}
	var captureFile *os.File
	closeLogs := func() {
		_ = logFile.Close()
		if captureFile != nil {
			_ = captureFile.Close()
		}
	}
	appendDecision := appendEndpointJSONL[DecisionRecord](logFile, "decision")
	onDecision := func(record DecisionRecord) {
		record.RuntimeID = opts.RuntimeID
		record.SessionID = opts.SessionID
		record.EventID = newDecisionID("event")
		record.OperationID = newDecisionID("operation")
		appendDecision(record)
	}

	var term *Terminate
	if bc.Assurance == vmkit.BrokerAssuranceSemantic {
		term, err = NewSemanticTerminate(bc.Upstream, opts.Resolve, nil, bc.Grant)
	} else {
		term, err = NewTerminate(bc.Upstream, opts.Resolve, nil)
	}
	if err != nil {
		closeLogs()
		return err
	}
	if bc.UpstreamCAFile != "" {
		client, err := UpstreamClientWithCA(bc.UpstreamCAFile)
		if err != nil {
			closeLogs()
			return fmt.Errorf("egress broker: upstream CA: %w", err)
		}
		term.Client = client
	}
	term.OnDecision = onDecision

	// Raw capture is a governed opt-in: only when the manifest declares it
	// does the capture file exist at all. Fail-closed like the access log — a
	// workspace must not boot half-observed.
	if bc.Capture {
		captureFile, err = os.OpenFile(opts.CaptureLogPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			closeLogs()
			return fmt.Errorf("egress broker: open capture log: %w", err)
		}
		term.OnCapture = appendEndpointJSONL[CaptureRecord](captureFile, "capture")
	}

	handler := EndpointHandler(bc, term, onDecision, opts.IsInside)
	limited := netlimit.New(listener, netlimit.DefaultMaxConnections)
	server := newEndpointHTTPServer(handler)
	go func() {
		_ = server.Serve(limited)
		_ = limited.Close()
		closeLogs()
	}()
	return nil
}

func newEndpointHTTPServer(handler http.Handler) *http.Server {
	return &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: brokerReadHeaderTimeout,
		ReadTimeout:       brokerReadTimeout,
		IdleTimeout:       brokerIdleTimeout,
		MaxHeaderBytes:    brokerMaxHeaderBytes,
	}
}

// EndpointHandler builds an endpoint's HTTP handler, gating the CONNECT
// (HTTPS_PROXY) tunnel. The tunnel is served ONLY when the endpoint enables
// the proxy — a terminate-only/base-URL endpoint passes a nil Connect, so
// Handler answers CONNECT with 405 and the endpoint is never an open forward
// proxy. When the proxy is enabled, every tunnel is governed: the guarded
// dialer denies inside/infrastructure destinations fail-closed and re-checks
// the resolved IP against DNS rebinding, and the operator's ConnectAllowlist
// (when non-empty) locks the tunnel to named hosts. A proxy endpoint with no
// inside-address classifier keeps the tunnel disabled (CONNECT answers 405)
// — fail closed, never an ungoverned tunnel.
func EndpointHandler(bc *vmkit.BrokerConfig, term *Terminate, onDecision OnDecision, isInside func(netip.Addr) bool) http.Handler {
	if bc != nil && bc.Assurance == vmkit.BrokerAssuranceSemantic &&
		(term == nil || term.Assurance != vmkit.BrokerAssuranceSemantic || term.Grant == nil) {
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "broker: semantic endpoint handler is not bound to a semantic grant", http.StatusServiceUnavailable)
		})
	}
	if !bc.Proxy || isInside == nil {
		return Handler(term, nil)
	}
	tunnel := &Connect{
		OnDecision: onDecision,
		Policy:     AllowlistPolicy(bc.ConnectAllowlist),
		Assurance:  bc.Assurance,
		Dial:       GuardedDialer{IsInside: isInside}.Dial,
	}
	return Handler(term, tunnel)
}

// UpstreamClientWithCA builds an *http.Client whose upstream TLS trusts only
// the given PEM bundle. An unreadable file or a bundle with no valid
// certificate is an error — the caller must never fall back to a client that
// trusts system roots instead.
func UpstreamClientWithCA(path string) (*http.Client, error) {
	pemBytes, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read upstream CA file %q: %w", path, err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pemBytes) {
		return nil, fmt.Errorf("upstream CA file %q: no valid PEM certificate found", path)
	}
	return &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool}}}, nil
}

// appendEndpointJSONL returns a mutex-serialized JSONL appender onto f.
func appendEndpointJSONL[T any](f *os.File, what string) func(T) {
	var mu sync.Mutex
	return func(record T) {
		data, err := json.Marshal(record)
		if err != nil {
			fmt.Fprintf(os.Stderr, "egress broker: encode %s record: %v\n", what, err)
			return
		}
		mu.Lock()
		defer mu.Unlock()
		if _, err := f.Write(append(data, '\n')); err != nil {
			fmt.Fprintf(os.Stderr, "egress broker: append %s record: %v\n", what, err)
		}
	}
}
