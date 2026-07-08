//go:build linux

package firecracker

import (
	"context"
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

// brokerAccessLogPath is the per-workspace broker access trail: one JSONL
// record per brokered request, captured PRE-SWAP so the live credential is
// absent by construction (ASK tenet 2 — the trace is written by mediation,
// not the agent).
func brokerAccessLogPath(stateDir, name string) string {
	return filepath.Join(stateDir, name, "broker-access.jsonl")
}

// startBrokerListener serves the egress broker on the workspace's broker
// vsock UDS. The credential is resolved here, host-side, and lives only in
// this process's memory: the guest holds a reference (@secret:<name>) that
// the broker swaps just before originating its own upstream TLS.
//
// Fail-closed: an absent broker config, an unresolvable secret reference, or
// an unopenable access log aborts the start — a workspace must not boot
// half-brokered.
func startBrokerListener(opts Options, config *vmkit.Config, port uint32) (net.Listener, error) {
	if config == nil || config.Broker == nil {
		return nil, fmt.Errorf("firecracker vsock listener %d targets the egress broker but the workspace has no broker config", port)
	}
	bc := config.Broker
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
	var mu sync.Mutex
	tap := func(rec broker.TapRecord) {
		mu.Lock()
		defer mu.Unlock()
		if err := json.NewEncoder(logFile).Encode(rec); err != nil {
			fmt.Fprintf(os.Stderr, "egress broker: append access record: %v\n", err)
		}
	}

	term, err := broker.NewTerminate(bc.Upstream, resolve, tap)
	if err != nil {
		_ = logFile.Close()
		return nil, err
	}
	tunnel := &broker.Connect{OnTap: tap}

	path := firecrackerGuestVsockPath(opts, port)
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		_ = logFile.Close()
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		_ = logFile.Close()
		return nil, err
	}
	unixListener, err := net.Listen("unix", path)
	if err != nil {
		_ = logFile.Close()
		return nil, fmt.Errorf("listen broker vsock port %d: %w", port, err)
	}
	// Any local process reaching this socket can spend the workspace
	// credential: restrict it to the owner, like the secrets socket.
	if err := os.Chmod(path, 0o600); err != nil {
		_ = unixListener.Close()
		_ = logFile.Close()
		return nil, fmt.Errorf("restrict broker vsock socket %d: %w", port, err)
	}
	go func() {
		_ = http.Serve(unixListener, broker.Handler(term, tunnel))
		_ = logFile.Close()
	}()
	return unixListener, nil
}
