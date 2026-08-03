//go:build linux

package firecracker

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"

	"github.com/geoffbelknap/microagent/internal/egress"
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

	path := firecrackerGuestVsockPath(opts, port)
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	unixListener, err := net.Listen("unix", path)
	if err != nil {
		return nil, fmt.Errorf("listen broker vsock port %d: %w", port, err)
	}
	// Any local process reaching this socket can spend the workspace
	// credential: restrict it to the owner, like the secrets socket.
	if err := os.Chmod(path, 0o600); err != nil {
		_ = unixListener.Close()
		return nil, fmt.Errorf("restrict broker vsock socket %d: %w", port, err)
	}
	if err := broker.StartEndpointServer(unixListener, broker.EndpointServerOptions{
		RuntimeID:      opts.Name,
		SessionID:      opts.SessionID,
		Endpoint:       bc,
		Resolve:        resolve,
		AccessLogPath:  brokerAccessLogPath(opts.StateDir, opts.Name),
		CaptureLogPath: brokerCaptureLogPath(opts.StateDir, opts.Name),
		IsInside:       egress.IsInside,
	}); err != nil {
		_ = unixListener.Close()
		return nil, err
	}
	return unixListener, nil
}

// brokerHandler builds an endpoint's HTTP handler through the shared portable
// core; reusing egress.IsInside keeps the brokered tunnel and the NIC
// datapath denying the same address space. See broker.EndpointHandler for the
// CONNECT gating semantics.
func brokerHandler(bc *vmkit.BrokerConfig, term *broker.Terminate, onDecision broker.OnDecision) http.Handler {
	return broker.EndpointHandler(bc, term, onDecision, egress.IsInside)
}

// upstreamClientWithCA is the shared fail-closed CA-pinned upstream client.
func upstreamClientWithCA(path string) (*http.Client, error) {
	return broker.UpstreamClientWithCA(path)
}
