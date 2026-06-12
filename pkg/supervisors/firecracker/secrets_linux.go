//go:build linux

package firecracker

import (
	"context"
	"fmt"
	"net"

	"github.com/geoffbelknap/microagent/pkg/secretxfer"
	"github.com/geoffbelknap/microagent/pkg/vmkit"
)

// secretsListenerTarget marks a vsock listener that serves the resolved secrets
// bundle rather than forwarding to a TCP target or writing a result file. Must
// equal the workspace package's sentinel.
const secretsListenerTarget = secretxfer.ServerTarget

// resolveSecretsBundle resolves every declared reference and loads every env
// file into an in-memory bundle via the shared secretxfer resolver. It fails
// closed: any unresolved reference, unreadable env file, invalid name, or
// duplicate name is an error and no partial bundle is returned.
func resolveSecretsBundle(ctx context.Context, config *vmkit.Config) (secretxfer.Bundle, error) {
	return secretxfer.ResolveBundle(ctx, config)
}

// secretsServer wraps the shared transport-agnostic server with the
// firecracker-private surface the supervisor and its tests use.
type secretsServer struct {
	*secretxfer.Server
}

func newSecretsServer(runtimeID, stateDir string, bundle secretxfer.Bundle, onDemand map[string]string, audit bool) *secretsServer {
	return &secretsServer{Server: secretxfer.NewServer(runtimeID, stateDir, bundle, onDemand, audit)}
}

func (s *secretsServer) handle(conn net.Conn) {
	s.Handle(conn)
}

// serveSecretsListener accepts guest connections and serves them via srv. It
// runs for the workspace lifetime (the path sub-project #4 reuses).
func serveSecretsListener(listener net.Listener, srv *secretsServer) {
	srv.Serve(listener)
}

// materializedSecretsDeclared reports whether the workspace has secrets written
// to the guest tmpfs (so a snapshot must purge them). On-demand-only secrets are
// never materialized, so they do not require a purge.
func materializedSecretsDeclared(config *vmkit.Config) bool {
	if config == nil {
		return false
	}
	return len(config.Secrets) > 0 || len(config.SecretEnvFiles) > 0
}

// sendGuestControl connects to the guest control listener over the firecracker
// CONNECT protocol and sends one control op, waiting for the guest's ack.
func sendGuestControl(opts Options, ctlPort uint32, op string) error {
	conn, reader, err := dialGuestVsock(vsockSocketPath(opts), ctlPort)
	if err != nil {
		return fmt.Errorf("connect guest control port %d: %w", ctlPort, err)
	}
	defer func() { _ = conn.Close() }()
	return secretxfer.SendControl(conn, reader, op)
}

func purgeGuestSecrets(opts Options, ctlPort uint32) error {
	return sendGuestControl(opts, ctlPort, secretxfer.OpPurge)
}

func rehydrateGuestSecrets(opts Options, ctlPort uint32) error {
	return sendGuestControl(opts, ctlPort, secretxfer.OpRehydrate)
}
