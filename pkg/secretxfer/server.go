package secretxfer

import (
	"context"
	"fmt"
	"net"
	"os"
	"time"

	"github.com/geoffbelknap/microagent/internal/netlimit"
	"github.com/geoffbelknap/microagent/pkg/secret"
	"github.com/geoffbelknap/microagent/pkg/vmkit"
)

// ServerTarget marks a vsock listener that serves the resolved secrets
// bundle rather than forwarding to a TCP target or writing a result file.
// Must equal the workspace package's sentinel.
const ServerTarget = "secrets://serve"

// ResolveBundle resolves every declared reference and loads every env file
// into an in-memory bundle. It fails closed: any unresolved reference,
// unreadable env file, invalid name, or duplicate name is an error and no
// partial bundle is returned. Plaintext-scheme use is warned to stderr.
func ResolveBundle(ctx context.Context, config *vmkit.Config) (Bundle, error) {
	if config == nil {
		return Bundle{}, nil
	}
	registry := secret.DefaultRegistry(os.Getenv, func(msg string) {
		fmt.Fprintln(os.Stderr, "warning: "+msg)
	})
	bundle := Bundle{ProtocolVersion: ProtocolVersion}
	seen := map[string]struct{}{}

	add := func(name string, value []byte) error {
		if !ValidName(name) {
			return fmt.Errorf("invalid secret name %q", name)
		}
		if _, dup := seen[name]; dup {
			return fmt.Errorf("duplicate secret name %q", name)
		}
		seen[name] = struct{}{}
		bundle.Secrets = append(bundle.Secrets, Entry{Name: name, Value: value})
		return nil
	}

	for _, ref := range config.Secrets {
		value, err := registry.Resolve(ctx, ref.Ref)
		if err != nil {
			return Bundle{}, fmt.Errorf("resolve secret %q: %w", ref.Name, err)
		}
		if err := add(ref.Name, value); err != nil {
			return Bundle{}, err
		}
	}
	for _, path := range config.SecretEnvFiles {
		fmt.Fprintf(os.Stderr, "warning: secrets env file %q is plaintext: not encrypted at rest, not for production\n", path)
		values, err := secret.LoadEnvFile(path)
		if err != nil {
			return Bundle{}, fmt.Errorf("load secrets env file %q: %w", path, err)
		}
		for name, value := range values {
			if err := add(name, []byte(value)); err != nil {
				return Bundle{}, err
			}
		}
	}
	return bundle, nil
}

// OnDemandRefs converts the config's on-demand declarations into the
// name->reference map the server resolves lazily.
func OnDemandRefs(config *vmkit.Config) map[string]string {
	if config == nil || len(config.OnDemandSecrets) == 0 {
		return nil
	}
	onDemand := make(map[string]string, len(config.OnDemandSecrets))
	for _, ref := range config.OnDemandSecrets {
		onDemand[ref.Name] = ref.Ref
	}
	return onDemand
}

// Server serves the materialized bundle (empty-name requests) and resolves
// on-demand secrets by name (lazy, per request). It optionally appends an
// audit record per access. It is transport-agnostic: supervisors hand it
// whatever listener carries the guest's vsock dial (a unix socket for
// Firecracker's CONNECT bridge).
type Server struct {
	runtimeID string
	sessionID string
	stateDir  string
	bundle    Bundle
	onDemand  map[string]string // name -> reference
	registry  *secret.Registry
	audit     bool
	maxConns  int
	timeout   time.Duration
}

const defaultServerConnectionTimeout = 30 * time.Second

// WithSessionID stamps subsequent access records with the concrete VM
// execution lifetime serving them.
func (s *Server) WithSessionID(sessionID string) *Server {
	s.sessionID = sessionID
	return s
}

var appendAccessRecord = AppendAccessRecord

func NewServer(runtimeID, stateDir string, bundle Bundle, onDemand map[string]string, audit bool) *Server {
	return &Server{
		runtimeID: runtimeID,
		stateDir:  stateDir,
		bundle:    bundle,
		onDemand:  onDemand,
		registry: secret.DefaultRegistry(os.Getenv, func(msg string) {
			fmt.Fprintln(os.Stderr, "warning: "+msg)
		}),
		audit:    audit,
		maxConns: netlimit.DefaultMaxConnections,
		timeout:  defaultServerConnectionTimeout,
	}
}

func (s *Server) record(name, access, result string) error {
	if !s.audit {
		return nil
	}
	return appendAccessRecord(AccessLogPath(s.stateDir, s.runtimeID), AccessRecord{
		At:          time.Now().UTC().Format(time.RFC3339Nano),
		RuntimeID:   s.runtimeID,
		SessionID:   s.sessionID,
		EventID:     newAccessID("event"),
		OperationID: newAccessID("operation"),
		Name:        name,
		Access:      access,
		Result:      result,
	})
}

// Serve accepts a bounded number of guest connections for the workspace
// lifetime. Excess connections are closed, and each request has a deadline so
// an idle guest cannot retain host descriptors or goroutines indefinitely.
func (s *Server) Serve(listener net.Listener) {
	maxConns := s.maxConns
	if maxConns <= 0 {
		maxConns = netlimit.DefaultMaxConnections
	}
	timeout := s.timeout
	if timeout <= 0 {
		timeout = defaultServerConnectionTimeout
	}
	limited := netlimit.New(listener, maxConns)
	defer func() { _ = limited.Close() }()
	for {
		conn, err := limited.Accept()
		if err != nil {
			return
		}
		go func(c net.Conn) {
			defer func() { _ = c.Close() }()
			ctx, cancel := context.WithTimeout(context.Background(), timeout)
			defer cancel()
			_ = c.SetDeadline(time.Now().Add(timeout))
			s.handle(ctx, c)
		}(conn)
	}
}

// Handle serves a single guest connection: one decoded request, one
// response. The caller owns the connection lifetime.
func (s *Server) Handle(conn net.Conn) {
	s.handle(context.Background(), conn)
}

func (s *Server) handle(ctx context.Context, conn net.Conn) {
	var req Request
	if err := DecodeMessage(conn, &req); err != nil {
		return
	}
	if req.ProtocolVersion != ProtocolVersion {
		return
	}
	if req.Name == "" {
		// Materialized bundle (boot delivery).
		for _, e := range s.bundle.Secrets {
			if err := s.record(e.Name, "materialize", "ok"); err != nil {
				return
			}
		}
		bundle := s.bundle
		bundle.ProtocolVersion = ProtocolVersion
		_ = EncodeMessage(conn, bundle)
		return
	}
	ref, ok := s.onDemand[req.Name]
	if !ok {
		errorText := "secret is not declared on-demand"
		if err := s.record(req.Name, "on-demand", "denied"); err != nil {
			errorText = "secret access audit failed"
		}
		_ = EncodeMessage(conn, GetResponse{
			ProtocolVersion: ProtocolVersion,
			Name:            req.Name,
			Error:           errorText,
		})
		return
	}
	value, err := s.registry.Resolve(ctx, ref)
	if err != nil {
		errorText := "resolve failed"
		if auditErr := s.record(req.Name, "on-demand", "error"); auditErr != nil {
			errorText = "secret access audit failed"
		}
		_ = EncodeMessage(conn, GetResponse{
			ProtocolVersion: ProtocolVersion,
			Name:            req.Name,
			Error:           errorText,
		})
		return
	}
	if err := s.record(req.Name, "on-demand", "ok"); err != nil {
		_ = EncodeMessage(conn, GetResponse{
			ProtocolVersion: ProtocolVersion,
			Name:            req.Name,
			Error:           "secret access audit failed",
		})
		return
	}
	_ = EncodeMessage(conn, GetResponse{
		ProtocolVersion: ProtocolVersion,
		Name:            req.Name,
		Value:           value,
	})
}
