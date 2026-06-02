//go:build linux

package firecracker

import (
	"context"
	"fmt"
	"net"
	"os"
	"time"

	"github.com/geoffbelknap/microagent/pkg/secret"
	"github.com/geoffbelknap/microagent/pkg/secretxfer"
	"github.com/geoffbelknap/microagent/pkg/vmkit"
)

// secretsListenerTarget marks a vsock listener that serves the resolved secrets
// bundle rather than forwarding to a TCP target or writing a result file. Must
// equal the workspace package's sentinel.
const secretsListenerTarget = "secrets://serve"

// resolveSecretsBundle resolves every declared reference and loads every env
// file into an in-memory bundle. It fails closed: any unresolved reference,
// unreadable env file, invalid name, or duplicate name is an error and no
// partial bundle is returned. Plaintext-scheme use is warned to stderr.
func resolveSecretsBundle(ctx context.Context, config *vmkit.Config) (secretxfer.Bundle, error) {
	if config == nil {
		return secretxfer.Bundle{}, nil
	}
	registry := secret.DefaultRegistry(os.Getenv, func(msg string) {
		fmt.Fprintln(os.Stderr, "warning: "+msg)
	})
	bundle := secretxfer.Bundle{ProtocolVersion: secretxfer.ProtocolVersion}
	seen := map[string]struct{}{}

	add := func(name string, value []byte) error {
		if !secretxfer.ValidName(name) {
			return fmt.Errorf("invalid secret name %q", name)
		}
		if _, dup := seen[name]; dup {
			return fmt.Errorf("duplicate secret name %q", name)
		}
		seen[name] = struct{}{}
		bundle.Secrets = append(bundle.Secrets, secretxfer.Entry{Name: name, Value: value})
		return nil
	}

	for _, ref := range config.Secrets {
		value, err := registry.Resolve(ctx, ref.Ref)
		if err != nil {
			return secretxfer.Bundle{}, fmt.Errorf("resolve secret %q: %w", ref.Name, err)
		}
		if err := add(ref.Name, value); err != nil {
			return secretxfer.Bundle{}, err
		}
	}
	for _, path := range config.SecretEnvFiles {
		fmt.Fprintf(os.Stderr, "warning: secrets env file %q is plaintext: not encrypted at rest, not for production\n", path)
		values, err := secret.LoadEnvFile(path)
		if err != nil {
			return secretxfer.Bundle{}, fmt.Errorf("load secrets env file %q: %w", path, err)
		}
		for name, value := range values {
			if err := add(name, []byte(value)); err != nil {
				return secretxfer.Bundle{}, err
			}
		}
	}
	return bundle, nil
}

// secretsServer serves the materialized bundle (empty-name requests, #2) and
// resolves on-demand secrets by name (lazy, per request). It optionally appends
// an audit record per access.
type secretsServer struct {
	runtimeID string
	stateDir  string
	bundle    secretxfer.Bundle
	onDemand  map[string]string // name -> reference
	registry  *secret.Registry
	audit     bool
}

func newSecretsServer(runtimeID, stateDir string, bundle secretxfer.Bundle, onDemand map[string]string, audit bool) *secretsServer {
	return &secretsServer{
		runtimeID: runtimeID,
		stateDir:  stateDir,
		bundle:    bundle,
		onDemand:  onDemand,
		registry: secret.DefaultRegistry(os.Getenv, func(msg string) {
			fmt.Fprintln(os.Stderr, "warning: "+msg)
		}),
		audit: audit,
	}
}

func (s *secretsServer) record(name, access, result string) {
	if !s.audit {
		return
	}
	_ = secretxfer.AppendAccessRecord(secretxfer.AccessLogPath(s.stateDir, s.runtimeID), secretxfer.AccessRecord{
		At:        time.Now().UTC().Format(time.RFC3339Nano),
		RuntimeID: s.runtimeID,
		Name:      name,
		Access:    access,
		Result:    result,
	})
}

func (s *secretsServer) serve(listener net.Listener) {
	for {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		go func(c net.Conn) {
			defer c.Close()
			s.handle(c)
		}(conn)
	}
}

func (s *secretsServer) handle(conn net.Conn) {
	var req secretxfer.Request
	if err := secretxfer.DecodeMessage(conn, &req); err != nil {
		return
	}
	if req.ProtocolVersion != secretxfer.ProtocolVersion {
		return
	}
	if req.Name == "" {
		// Materialized bundle (boot delivery, #2).
		for _, e := range s.bundle.Secrets {
			s.record(e.Name, "materialize", "ok")
		}
		bundle := s.bundle
		bundle.ProtocolVersion = secretxfer.ProtocolVersion
		_ = secretxfer.EncodeMessage(conn, bundle)
		return
	}
	ref, ok := s.onDemand[req.Name]
	if !ok {
		s.record(req.Name, "on-demand", "denied")
		_ = secretxfer.EncodeMessage(conn, secretxfer.GetResponse{
			ProtocolVersion: secretxfer.ProtocolVersion,
			Name:            req.Name,
			Error:           "secret is not declared on-demand",
		})
		return
	}
	value, err := s.registry.Resolve(context.Background(), ref)
	if err != nil {
		s.record(req.Name, "on-demand", "error")
		_ = secretxfer.EncodeMessage(conn, secretxfer.GetResponse{
			ProtocolVersion: secretxfer.ProtocolVersion,
			Name:            req.Name,
			Error:           "resolve failed",
		})
		return
	}
	s.record(req.Name, "on-demand", "ok")
	_ = secretxfer.EncodeMessage(conn, secretxfer.GetResponse{
		ProtocolVersion: secretxfer.ProtocolVersion,
		Name:            req.Name,
		Value:           value,
	})
}

// serveSecretsListener accepts guest connections and serves them via srv. It
// runs for the workspace lifetime (the path sub-project #4 reuses).
func serveSecretsListener(listener net.Listener, srv *secretsServer) {
	srv.serve(listener)
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
	defer conn.Close()
	return secretxfer.SendControl(conn, reader, op)
}

func purgeGuestSecrets(opts Options, ctlPort uint32) error {
	return sendGuestControl(opts, ctlPort, secretxfer.OpPurge)
}

func rehydrateGuestSecrets(opts Options, ctlPort uint32) error {
	return sendGuestControl(opts, ctlPort, secretxfer.OpRehydrate)
}
