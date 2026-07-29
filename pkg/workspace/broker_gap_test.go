package workspace

import (
	"errors"
	"testing"

	"github.com/geoffbelknap/microagent/pkg/vmkit"
)

// TestRequestComposesBrokerListenerOnSupportedBackends pins the supported
// path on both release backends: a declared broker endpoint composes the
// broker vsock listener into the request. apple-vf gained the capability when
// the `--broker-serve` companion landed; before that, the request failed
// closed with the recorded gap.
func TestRequestComposesBrokerListenerOnSupportedBackends(t *testing.T) {
	for _, backend := range []string{vmkit.BackendLinuxKVM, vmkit.BackendAppleVF} {
		opts := DefaultOptions()
		opts.Name = "ws"
		opts.StateDir = t.TempDir()
		opts.Backend = backend
		opts.Broker = &vmkit.BrokerConfig{
			Upstream: "https://api.example.com",
			Secret:   vmkit.SecretRef{Name: "tok", Ref: "env:TOK"},
		}
		req, err := Request(opts, "", "/tmp/rootfs.ext4", "req-1")
		if err != nil {
			t.Fatalf("Request(broker, %s): %v", backend, err)
		}
		found := false
		for _, listener := range req.Config.VsockListeners {
			if listener.Target == vmkit.BrokerListenerTarget {
				found = true
			}
		}
		if !found {
			t.Fatalf("Request(broker, %s) composed no broker listener: %#v", backend, req.Config.VsockListeners)
		}
		if len(req.Config.Brokers) == 0 {
			t.Fatalf("Request(broker, %s) carried no normalized broker config", backend)
		}
	}
}

// TestRequestRejectsBrokerOnUnknownBackends keeps the fail-closed contract:
// a backend with no declared capabilities (the zero value grants nothing)
// must return the structured UnsupportedFeatureError instead of letting a
// supervisor die later with a protocol-shaped error.
func TestRequestRejectsBrokerOnUnknownBackends(t *testing.T) {
	opts := DefaultOptions()
	opts.Name = "ws"
	opts.StateDir = t.TempDir()
	opts.Backend = "no-such-backend"
	opts.Broker = &vmkit.BrokerConfig{
		Upstream: "https://api.example.com",
		Secret:   vmkit.SecretRef{Name: "tok", Ref: "env:TOK"},
	}
	_, err := Request(opts, "", "/tmp/rootfs.ext4", "req-1")
	if err == nil {
		t.Fatal("Request(broker, unknown backend) succeeded, want UnsupportedFeatureError")
	}
	var unsupported vmkit.UnsupportedFeatureError
	if !errors.As(err, &unsupported) {
		t.Fatalf("Request(broker, unknown backend) error = %v (%T), want UnsupportedFeatureError", err, err)
	}
}
