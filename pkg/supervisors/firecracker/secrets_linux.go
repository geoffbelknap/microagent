//go:build linux

package firecracker

import (
	"context"
	"fmt"
	"net"
	"os"

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

// serveSecretsListener accepts guest connections and serves the resolved bundle
// to each. It runs for the workspace lifetime (the path sub-project #4 reuses).
func serveSecretsListener(listener net.Listener, bundle secretxfer.Bundle) {
	for {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		go func(c net.Conn) {
			defer c.Close()
			_ = secretxfer.ServeBundle(c, c, bundle)
		}(conn)
	}
}
