package workspace

import (
	"errors"
	"strings"
	"testing"

	"github.com/geoffbelknap/microagent/pkg/vmkit"
)

// TestRequestRejectsBrokerOnUnsupportedBackends pins the fail-closed contract
// for broker endpoints on backends whose supervisor cannot serve the broker
// vsock listener: the shared library path must return the structured
// UnsupportedFeatureError — naming the backend and the recorded gap — instead
// of letting the supervisor die later with a protocol-shaped error.
func TestRequestRejectsBrokerOnUnsupportedBackends(t *testing.T) {
	for _, backend := range []string{vmkit.BackendAppleVF, vmkit.BackendWindowsHyperV} {
		opts := DefaultOptions()
		opts.Name = "ws"
		opts.StateDir = t.TempDir()
		opts.Backend = backend
		opts.Broker = &vmkit.BrokerConfig{
			Upstream: "https://api.example.com",
			Secret:   vmkit.SecretRef{Name: "tok", Ref: "env:TOK"},
		}
		_, err := Request(opts, "", "/tmp/rootfs.ext4", "req-1")
		if err == nil {
			t.Fatalf("Request(broker, %s) succeeded, want UnsupportedFeatureError", backend)
		}
		var unsupported vmkit.UnsupportedFeatureError
		if !errors.As(err, &unsupported) {
			t.Fatalf("Request(broker, %s) error = %v (%T), want UnsupportedFeatureError", backend, err, err)
		}
		if unsupported.Backend != backend || unsupported.GapID == "" {
			t.Fatalf("UnsupportedFeatureError = %#v, want backend %s with a gap ID", unsupported, backend)
		}
		if msg := err.Error(); !strings.Contains(msg, backend) {
			t.Fatalf("error %q must name the backend", msg)
		}
	}
}

// TestRootfsRequestRejectsBrokerOnUnsupportedBackends proves the same gate
// fires at build time (create), not only at start, so the operator learns
// about the gap before any rootfs work happens.
func TestRootfsRequestRejectsBrokerOnUnsupportedBackends(t *testing.T) {
	opts := DefaultOptions()
	opts.Name = "ws"
	opts.StateDir = t.TempDir()
	opts.Backend = vmkit.BackendAppleVF
	opts.Broker = &vmkit.BrokerConfig{
		Upstream: "https://api.example.com",
		Secret:   vmkit.SecretRef{Name: "tok", Ref: "env:TOK"},
	}
	_, err := rootfsRequest(opts, "/tmp/rootfs.ext4")
	var unsupported vmkit.UnsupportedFeatureError
	if !errors.As(err, &unsupported) {
		t.Fatalf("rootfsRequest(broker, apple-vf) error = %v (%T), want UnsupportedFeatureError", err, err)
	}
}

// TestRequestStillAllowsBrokerOnLinuxKVM guards the supported path.
func TestRequestStillAllowsBrokerOnLinuxKVM(t *testing.T) {
	opts := DefaultOptions()
	opts.Name = "ws"
	opts.StateDir = t.TempDir()
	opts.Backend = vmkit.BackendLinuxKVM
	opts.Broker = &vmkit.BrokerConfig{
		Upstream: "https://api.example.com",
		Secret:   vmkit.SecretRef{Name: "tok", Ref: "env:TOK"},
	}
	if _, err := Request(opts, "", "/tmp/rootfs.ext4", "req-1"); err != nil {
		t.Fatalf("Request(broker, linux-kvm): %v", err)
	}
}
