package main

import (
	"context"
	"flag"
	"fmt"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/geoffbelknap/microagent/internal/egress"
	"github.com/geoffbelknap/microagent/pkg/broker"
	"github.com/geoffbelknap/microagent/pkg/secret"
	"github.com/geoffbelknap/microagent/pkg/vmkit"
	"github.com/geoffbelknap/microagent/pkg/workspace"
)

// runBrokerServe is the hidden companion mode the apple-vf supervisor spawns
// to serve one broker endpoint on an owner-only unix socket — the same
// portable endpoint server (broker.StartEndpointServer) the Firecracker vsock
// companion uses, so the two backends cannot drift on credential handling,
// decision records, or CONNECT gating. The credential arrives as a
// scheme-prefixed reference, is resolved here host-side, and lives only in
// this process's memory; the supervisor terminates the companion with the VM.
func runBrokerServe(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("broker-serve", flag.ContinueOnError)
	stateDir := fs.String("state-dir", "", "workspace state directory")
	name := fs.String("name", "", "workspace name")
	sessionID := fs.String("session-id", "", "workspace execution session identity")
	listen := fs.String("listen", "", "unix socket path to serve on")
	upstream := fs.String("upstream", "", "terminate-mode upstream base URL")
	secretSpec := fs.String("secret", "", "credential reference: NAME=<scheme>:<ref> (reference only, never a value)")
	proxy := fs.Bool("proxy", false, "serve the governed CONNECT (HTTPS_PROXY) tunnel")
	var connectAllow multiFlag
	fs.Var(&connectAllow, "connect-allow", "restrict the CONNECT tunnel to this upstream host (repeatable)")
	upstreamCA := fs.String("upstream-ca", "", "PEM bundle the upstream TLS client trusts instead of system roots")
	capture := fs.Bool("capture", false, "enable governed raw capture of pre-swap requests")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *stateDir == "" || *name == "" || *listen == "" || *upstream == "" || *secretSpec == "" {
		return fmt.Errorf("usage: microagent --broker-serve --state-dir <dir> --name <name> --listen <socket> --upstream <url> --secret NAME=<scheme>:<ref>")
	}
	secretName, secretRef, ok := strings.Cut(*secretSpec, "=")
	if !ok || secretName == "" || secretRef == "" {
		return fmt.Errorf("--secret must be NAME=<scheme>:<ref>")
	}

	registry := secret.DefaultRegistry(os.Getenv, func(msg string) {
		fmt.Fprintln(os.Stderr, "warning: "+msg)
	})
	value, err := registry.Resolve(ctx, secretRef)
	if err != nil {
		return fmt.Errorf("egress broker: resolve secret %q: %w", secretName, err)
	}
	live := string(value)
	resolve := func(name string) (string, bool) {
		if name == secretName {
			return live, true
		}
		return "", false
	}

	if err := os.Remove(*listen); err != nil && !os.IsNotExist(err) {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(*listen), 0o700); err != nil {
		return err
	}
	listener, err := net.Listen("unix", *listen)
	if err != nil {
		return fmt.Errorf("listen broker socket %s: %w", *listen, err)
	}
	// Any local process reaching this socket can spend the workspace
	// credential: restrict it to the owner, like the Firecracker broker UDS.
	if err := os.Chmod(*listen, 0o600); err != nil {
		_ = listener.Close()
		return fmt.Errorf("restrict broker socket %s: %w", *listen, err)
	}
	endpoint := &vmkit.BrokerConfig{
		Upstream:         *upstream,
		Secret:           vmkit.SecretRef{Name: secretName, Ref: secretRef},
		Proxy:            *proxy,
		ConnectAllowlist: connectAllow,
		UpstreamCAFile:   *upstreamCA,
		Capture:          *capture,
	}
	if err := broker.StartEndpointServer(listener, broker.EndpointServerOptions{
		RuntimeID:     *name,
		SessionID:     *sessionID,
		Endpoint:      endpoint,
		Resolve:       resolve,
		AccessLogPath: workspace.BrokerAccessPath(*stateDir, *name),
		// Keep in lockstep with the Firecracker companion's capture path.
		CaptureLogPath: filepath.Join(*stateDir, *name, "broker-capture.jsonl"),
		IsInside:       egress.IsInside,
	}); err != nil {
		_ = listener.Close()
		return err
	}

	// Serve until the supervisor terminates us with the VM. The orphan check
	// is the backstop for a supervisor that dies without delivering SIGTERM:
	// a companion holding a live credential must never outlive its VM.
	waitCtx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()
	parent := os.Getppid()
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-waitCtx.Done():
			_ = listener.Close()
			_ = os.Remove(*listen)
			return nil
		case <-ticker.C:
			if os.Getppid() != parent {
				_ = listener.Close()
				_ = os.Remove(*listen)
				return nil
			}
		}
	}
}
