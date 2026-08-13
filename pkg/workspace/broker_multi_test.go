package workspace

import (
	"strings"
	"testing"

	"github.com/geoffbelknap/microagent/pkg/broker"
	"github.com/geoffbelknap/microagent/pkg/vmkit"
)

// TestEffectiveBrokersPrecedence covers the precedence rules effectiveBrokers
// must follow: the explicit Brokers set wins when present, a lone legacy
// Broker folds into a one-element set, and neither configured yields nil.
func TestEffectiveBrokersPrecedence(t *testing.T) {
	set := []*vmkit.BrokerConfig{
		{Upstream: "https://a.example.com", Secret: vmkit.SecretRef{Name: "a", Ref: "env:A"}, Assurance: vmkit.BrokerAssuranceTrustedUpstream},
		{Upstream: "https://b.example.com", Secret: vmkit.SecretRef{Name: "b", Ref: "env:B"}, Assurance: vmkit.BrokerAssuranceTrustedUpstream},
	}
	solo := &vmkit.BrokerConfig{Upstream: "https://solo.example.com", Secret: vmkit.SecretRef{Name: "s", Ref: "env:S"}, Assurance: vmkit.BrokerAssuranceTrustedUpstream}

	if out := effectiveBrokers(Options{Brokers: set}); len(out) != 2 {
		t.Fatalf("Brokers set: effectiveBrokers = %+v, want the 2-element set", out)
	}
	if out := effectiveBrokers(Options{Broker: solo}); len(out) != 1 || out[0] != solo {
		t.Fatalf("Broker only: effectiveBrokers = %+v, want [solo]", out)
	}
	if out := effectiveBrokers(Options{}); out != nil {
		t.Fatalf("neither set: effectiveBrokers = %+v, want nil", out)
	}

	// Both set is an operator error, surfaced by the normalizing wrapper (not
	// by effectiveBrokers itself, which only resolves precedence).
	if _, err := normalizeEffectiveBrokers(Options{Brokers: set, Broker: solo}); err == nil {
		t.Fatal("normalizeEffectiveBrokers accepted both Broker and Brokers set")
	}
}

// TestRequestRegistersListenerPerBrokerEndpoint asserts Request(...) allocates
// one vsock listener per normalized endpoint and threads the full normalized
// set onto Config.Brokers.
func TestRequestRegistersListenerPerBrokerEndpoint(t *testing.T) {
	opts := DefaultOptions()
	opts.Name = "ws"
	opts.StateDir = t.TempDir()
	opts.Backend = vmkit.BackendLinuxKVM
	opts.Brokers = []*vmkit.BrokerConfig{
		{
			Upstream:    "https://a.example.com",
			Secret:      vmkit.SecretRef{Name: "a", Ref: "env:TOK_A"},
			GuestListen: "127.0.0.1:18888",
			VsockPort:   1032,
			Assurance:   vmkit.BrokerAssuranceTrustedUpstream,
		},
		{
			Upstream:    "https://b.example.com",
			Secret:      vmkit.SecretRef{Name: "b", Ref: "env:TOK_B"},
			GuestListen: "127.0.0.1:18889",
			VsockPort:   1033,
			Assurance:   vmkit.BrokerAssuranceTrustedUpstream,
		},
	}

	req, err := Request(opts, "", "/tmp/rootfs.ext4", "req-1")
	if err != nil {
		t.Fatalf("Request: %v", err)
	}
	if len(req.Config.Brokers) != 2 {
		t.Fatalf("Config.Brokers = %+v, want 2 endpoints", req.Config.Brokers)
	}

	wantPorts := map[uint32]bool{1032: false, 1033: false}
	for _, l := range req.Config.VsockListeners {
		if l.Target != broker.ListenerTarget {
			continue
		}
		if _, ok := wantPorts[l.Port]; ok {
			wantPorts[l.Port] = true
		}
	}
	for port, seen := range wantPorts {
		if !seen {
			t.Fatalf("no broker listener registered at port %d: %+v", port, req.Config.VsockListeners)
		}
	}
}

// TestRootfsRequestMergesEveryEndpointGuestEnv asserts rootfsRequest merges
// every broker endpoint's guest env, so each endpoint's declared base-URL env
// var points at its own guest listener, distinctly.
func TestRootfsRequestMergesEveryEndpointGuestEnv(t *testing.T) {
	opts := DefaultOptions()
	opts.Name = "ws"
	opts.StateDir = t.TempDir()
	// DefaultOptions resolves the host backend (apple-vf on a Mac), and
	// brokers are gated on the backend capability: pin the supported backend.
	opts.Backend = vmkit.BackendLinuxKVM
	opts.Brokers = []*vmkit.BrokerConfig{
		{
			Upstream:    "https://a.example.com",
			Secret:      vmkit.SecretRef{Name: "a", Ref: "env:TOK_A"},
			GuestListen: "127.0.0.1:18888",
			VsockPort:   1032,
			BaseURLEnv:  map[string]string{"A_BASE_URL": ""},
			Assurance:   vmkit.BrokerAssuranceTrustedUpstream,
		},
		{
			Upstream:    "https://b.example.com",
			Secret:      vmkit.SecretRef{Name: "b", Ref: "env:TOK_B"},
			GuestListen: "127.0.0.1:18889",
			VsockPort:   1033,
			BaseURLEnv:  map[string]string{"B_BASE_URL": ""},
			Assurance:   vmkit.BrokerAssuranceTrustedUpstream,
		},
	}

	cfg, err := GuestBootConfig(opts)
	if err != nil {
		t.Fatalf("GuestBootConfig: %v", err)
	}
	env := envMap(cfg.Env)
	if env["A_BASE_URL"] != "http://127.0.0.1:18888" {
		t.Fatalf("A_BASE_URL = %q, want endpoint A's listener", env["A_BASE_URL"])
	}
	if env["B_BASE_URL"] != "http://127.0.0.1:18889" {
		t.Fatalf("B_BASE_URL = %q, want endpoint B's listener", env["B_BASE_URL"])
	}
	if env["A_BASE_URL"] == env["B_BASE_URL"] {
		t.Fatal("both endpoints' base URLs resolved to the same value")
	}
}

// TestRequestSingleLegacyBrokerBackCompat asserts a workspace configured with
// only the legacy single Broker (no Brokers set) still yields exactly one
// broker vsock listener and one merged guest base-URL env, matching
// pre-multi-endpoint behavior.
func TestRequestSingleLegacyBrokerBackCompat(t *testing.T) {
	opts := DefaultOptions()
	opts.Name = "ws"
	opts.StateDir = t.TempDir()
	opts.Backend = vmkit.BackendLinuxKVM
	opts.Broker = &vmkit.BrokerConfig{
		Upstream:   "https://api.example.com",
		Secret:     vmkit.SecretRef{Name: "api", Ref: "env:CI_TOKEN"},
		BaseURLEnv: map[string]string{"EXAMPLE_BASE_URL": ""},
		Assurance:  vmkit.BrokerAssuranceTrustedUpstream,
	}

	req, err := Request(opts, "", "/tmp/rootfs.ext4", "req-1")
	if err != nil {
		t.Fatalf("Request: %v", err)
	}
	if len(req.Config.Brokers) != 1 {
		t.Fatalf("Config.Brokers = %+v, want exactly 1 endpoint", req.Config.Brokers)
	}
	brokerListeners := 0
	for _, l := range req.Config.VsockListeners {
		if l.Target == broker.ListenerTarget {
			brokerListeners++
		}
	}
	if brokerListeners != 1 {
		t.Fatalf("broker vsock listeners = %d, want 1", brokerListeners)
	}

	cfg, err := GuestBootConfig(opts)
	if err != nil {
		t.Fatalf("GuestBootConfig: %v", err)
	}
	if envMap(cfg.Env)["EXAMPLE_BASE_URL"] != "http://"+DefaultBrokerGuestListen {
		t.Fatalf("EXAMPLE_BASE_URL = %q, want the single broker's guest listen URL", envMap(cfg.Env)["EXAMPLE_BASE_URL"])
	}
}

// envMap turns the wire-format KEY=VALUE list into a map for assertions.
func envMap(entries []string) map[string]string {
	out := map[string]string{}
	for _, entry := range entries {
		if key, value, ok := strings.Cut(entry, "="); ok {
			out[key] = value
		}
	}
	return out
}
