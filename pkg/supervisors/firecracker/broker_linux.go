//go:build linux

package firecracker

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"

	"github.com/geoffbelknap/microagent/pkg/broker"
	"github.com/geoffbelknap/microagent/pkg/secret"
	"github.com/geoffbelknap/microagent/pkg/vmkit"
)

// brokerAccessLogPath is the per-workspace broker decision stream: one JSONL
// record per brokered request — verdict plus minimized metadata, no content
// (ASK tenet 2 — the trace is written by mediation, not the agent). The live
// credential is absent by construction: records carry reference names, never
// values.
func brokerAccessLogPath(stateDir, name string) string {
	return filepath.Join(stateDir, name, "broker-access.jsonl")
}

// brokerCaptureLogPath is the governed raw-capture file: pre-swap requests
// (references verbatim, never live secrets), written only when the operator
// opted in via BrokerConfig.Capture.
func brokerCaptureLogPath(stateDir, name string) string {
	return filepath.Join(stateDir, name, "broker-capture.jsonl")
}

// appendJSONL returns a mutex-serialized JSONL appender onto f.
func appendJSONL[T any](f *os.File, what string) func(T) {
	var mu sync.Mutex
	return func(rec T) {
		mu.Lock()
		defer mu.Unlock()
		if err := json.NewEncoder(f).Encode(rec); err != nil {
			fmt.Fprintf(os.Stderr, "egress broker: append %s record: %v\n", what, err)
		}
	}
}

// brokerForPort finds the endpoint in config.Brokers whose VsockPort matches,
// so a listener started for that port serves the right upstream/secret. For
// back-compat it falls back to the legacy single config.Broker field when
// Brokers is empty — the legacy scheme only ever registered one broker
// listener, so there is no ambiguity to resolve by port. This is what lets
// state persisted before multi-endpoint brokers existed keep starting on a
// supervisor binary that has since migrated to Brokers.
func brokerForPort(config *vmkit.Config, port uint32) *vmkit.BrokerConfig {
	for _, bc := range config.Brokers {
		if bc.VsockPort == port {
			return bc
		}
	}
	if len(config.Brokers) == 0 {
		return config.Broker
	}
	return nil
}

// upstreamClientWithCA builds an *http.Client whose upstream TLS trusts only
// the CA certificate(s) in the PEM bundle at path. Fail-closed: an unreadable
// file or a bundle with no valid certificate is an error — the caller must
// never fall back to a client that trusts system roots instead.
func upstreamClientWithCA(path string) (*http.Client, error) {
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

// startBrokerListener serves one egress broker endpoint on the workspace's
// broker vsock UDS at port. The credential is resolved here, host-side, and
// lives only in this process's memory: the guest holds a reference
// (@secret:<name>) that the broker swaps just before originating its own
// upstream TLS.
//
// Fail-closed: no endpoint configured for this port, an unresolvable secret
// reference, an unreadable/invalid UpstreamCAFile, or an unopenable access
// log aborts the start — a workspace must not boot half-brokered.
func startBrokerListener(opts Options, config *vmkit.Config, port uint32) (net.Listener, error) {
	if config == nil {
		return nil, fmt.Errorf("firecracker vsock listener %d targets the egress broker but the workspace has no broker config", port)
	}
	bc := brokerForPort(config, port)
	if bc == nil {
		return nil, fmt.Errorf("firecracker vsock listener %d targets the egress broker but no broker endpoint is configured for that port", port)
	}
	registry := secret.DefaultRegistry(os.Getenv, func(msg string) {
		fmt.Fprintln(os.Stderr, "warning: "+msg)
	})
	value, err := registry.Resolve(context.Background(), bc.Secret.Ref)
	if err != nil {
		return nil, fmt.Errorf("egress broker: resolve secret %q: %w", bc.Secret.Name, err)
	}
	live := string(value)
	secretName := bc.Secret.Name
	resolve := func(name string) (string, bool) {
		if name == secretName {
			return live, true
		}
		return "", false
	}

	logPath := brokerAccessLogPath(opts.StateDir, opts.Name)
	if err := os.MkdirAll(filepath.Dir(logPath), 0o700); err != nil {
		return nil, err
	}
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, fmt.Errorf("egress broker: open access log: %w", err)
	}
	var captureFile *os.File
	closeLogs := func() {
		_ = logFile.Close()
		if captureFile != nil {
			_ = captureFile.Close()
		}
	}
	onDecision := appendJSONL[broker.DecisionRecord](logFile, "decision")

	term, err := broker.NewTerminate(bc.Upstream, resolve, nil)
	if err != nil {
		closeLogs()
		return nil, err
	}
	if bc.UpstreamCAFile != "" {
		client, err := upstreamClientWithCA(bc.UpstreamCAFile)
		if err != nil {
			closeLogs()
			return nil, fmt.Errorf("egress broker: upstream CA: %w", err)
		}
		term.Client = client
	}
	term.OnDecision = onDecision
	tunnel := &broker.Connect{OnDecision: onDecision}

	// Raw capture is a governed opt-in: only when the manifest declares it
	// does the capture file exist at all. Fail-closed like the access log — a
	// workspace must not boot half-observed.
	if bc.Capture {
		capturePath := brokerCaptureLogPath(opts.StateDir, opts.Name)
		captureFile, err = os.OpenFile(capturePath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			closeLogs()
			return nil, fmt.Errorf("egress broker: open capture log: %w", err)
		}
		term.OnCapture = appendJSONL[broker.CaptureRecord](captureFile, "capture")
	}

	path := firecrackerGuestVsockPath(opts, port)
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		closeLogs()
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		closeLogs()
		return nil, err
	}
	unixListener, err := net.Listen("unix", path)
	if err != nil {
		closeLogs()
		return nil, fmt.Errorf("listen broker vsock port %d: %w", port, err)
	}
	// Any local process reaching this socket can spend the workspace
	// credential: restrict it to the owner, like the secrets socket.
	if err := os.Chmod(path, 0o600); err != nil {
		_ = unixListener.Close()
		closeLogs()
		return nil, fmt.Errorf("restrict broker vsock socket %d: %w", port, err)
	}
	go func() {
		_ = http.Serve(unixListener, broker.Handler(term, tunnel))
		closeLogs()
	}()
	return unixListener, nil
}
