package workspace

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
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
			Upstream:  "https://api.example.com",
			Secret:    vmkit.SecretRef{Name: "tok", Ref: "env:TOK"},
			Assurance: vmkit.BrokerAssuranceTrustedUpstream,
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
		Upstream:  "https://api.example.com",
		Secret:    vmkit.SecretRef{Name: "tok", Ref: "env:TOK"},
		Assurance: vmkit.BrokerAssuranceTrustedUpstream,
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

// TestPreflightBrokerSecrets pins the fail-closed start contract: a broker
// secret reference that cannot resolve on the host must be a structured
// error before any supervisor process spawns, never a silent guest death
// after a start that reported success.
func TestPreflightBrokerSecrets(t *testing.T) {
	ctx := context.Background()

	t.Run("nil and empty broker sets pass", func(t *testing.T) {
		if err := preflightBrokerSecrets(ctx, nil); err != nil {
			t.Fatalf("nil brokers: %v", err)
		}
		if err := preflightBrokerSecrets(ctx, []*vmkit.BrokerConfig{nil}); err != nil {
			t.Fatalf("nil entry: %v", err)
		}
	})

	t.Run("resolvable env reference passes", func(t *testing.T) {
		t.Setenv("PREFLIGHT_BROKER_OK", "value")
		brokers := []*vmkit.BrokerConfig{{
			Upstream: "https://api.example.com",
			Secret:   vmkit.SecretRef{Name: "tok", Ref: "env:PREFLIGHT_BROKER_OK"},
		}}
		if err := preflightBrokerSecrets(ctx, brokers); err != nil {
			t.Fatalf("resolvable ref: %v", err)
		}
	})

	t.Run("unset env reference fails closed with guidance", func(t *testing.T) {
		brokers := []*vmkit.BrokerConfig{{
			Upstream: "https://api.example.com",
			Secret:   vmkit.SecretRef{Name: "tok", Ref: "env:PREFLIGHT_BROKER_DEFINITELY_UNSET"},
		}}
		err := preflightBrokerSecrets(ctx, brokers)
		if err == nil {
			t.Fatal("unresolvable ref passed preflight")
		}
		for _, want := range []string{`secret "tok" did not resolve`, "microagent secret check", "https://api.example.com"} {
			if !strings.Contains(err.Error(), want) {
				t.Fatalf("error %q missing %q", err.Error(), want)
			}
		}
	})

	t.Run("missing dotenv file fails closed", func(t *testing.T) {
		brokers := []*vmkit.BrokerConfig{{
			Upstream: "https://api.example.com",
			Secret:   vmkit.SecretRef{Name: "tok", Ref: "dotenv:" + filepath.Join(t.TempDir(), "absent.env") + "#KEY"},
		}}
		if err := preflightBrokerSecrets(ctx, brokers); err == nil {
			t.Fatal("missing dotenv file passed preflight")
		}
	})

	t.Run("unreadable upstream CA fails closed", func(t *testing.T) {
		t.Setenv("PREFLIGHT_BROKER_OK", "value")
		brokers := []*vmkit.BrokerConfig{{
			Upstream:       "https://api.example.com",
			Secret:         vmkit.SecretRef{Name: "tok", Ref: "env:PREFLIGHT_BROKER_OK"},
			UpstreamCAFile: filepath.Join(t.TempDir(), "absent-ca.pem"),
		}}
		err := preflightBrokerSecrets(ctx, brokers)
		if err == nil || !strings.Contains(err.Error(), "upstream CA") {
			t.Fatalf("err = %v, want unreadable CA failure", err)
		}
	})
}

// TestStartFailsClosedWhenBrokerSecretUnresolvable mirrors the missing-kernel
// preflight precedent: Start must return the structured broker error before
// any supervisor process is spawned or running state is written.
func TestStartFailsClosedWhenBrokerSecretUnresolvable(t *testing.T) {
	dir := t.TempDir()
	// The preflight is backend-neutral; use the backend this platform can
	// actually start so normalizeLifecycleOptions does not reject it first.
	backend := vmkit.BackendLinuxKVM
	arch := "amd64"
	if runtime.GOOS == "darwin" {
		backend = vmkit.BackendAppleVF
		arch = "arm64"
	}
	opts := Options{
		Name:           "broker-preflight",
		StateDir:       dir,
		Backend:        backend,
		Architecture:   arch,
		KernelPath:     filepath.Join(dir, "kernel"),
		KernelExplicit: true,
		SupervisorPath: filepath.Join(dir, "missing-supervisor"),
		Profile:        "small",
		RestartPolicy:  "never",
		MemoryMiB:      512,
		CPUCount:       2,
		SizeMiB:        128,
		Network:        vmkit.NetworkConfig{Mode: "isolated"},
		Broker: &vmkit.BrokerConfig{
			Upstream:  "https://api.example.com",
			Secret:    vmkit.SecretRef{Name: "tok", Ref: "env:START_BROKER_PREFLIGHT_UNSET"},
			Assurance: vmkit.BrokerAssuranceTrustedUpstream,
		},
	}
	if err := WriteManifest(opts); err != nil {
		t.Fatalf("WriteManifest: %v", err)
	}
	rootfsPath := WorkspaceRootfsPath(dir, opts.Name, backend)
	if err := os.WriteFile(rootfsPath, []byte("rootfs"), 0o644); err != nil {
		t.Fatalf("write rootfs: %v", err)
	}

	_, err := Start(context.Background(), opts)
	if err == nil || !strings.Contains(err.Error(), `secret "tok" did not resolve`) {
		t.Fatalf("Start err = %v, want broker secret preflight failure", err)
	}
	if _, statErr := os.Stat(filepath.Join(dir, opts.Name, "runtime.json")); !os.IsNotExist(statErr) {
		t.Fatalf("runtime state exists after preflight failure: %v", statErr)
	}
}
