package workspace

import (
	"strings"
	"testing"

	"github.com/geoffbelknap/microagent/pkg/vmkit"
)

// TestNormalizeBrokersEmpty asserts the back-compat nil case: no brokers
// configured produces no error and no output.
func TestNormalizeBrokersEmpty(t *testing.T) {
	out, err := normalizeBrokers(nil)
	if err != nil {
		t.Fatalf("normalizeBrokers(nil): %v", err)
	}
	if out != nil {
		t.Fatalf("normalizeBrokers(nil) = %+v, want nil", out)
	}

	out, err = normalizeBrokers([]*vmkit.BrokerConfig{})
	if err != nil {
		t.Fatalf("normalizeBrokers(empty): %v", err)
	}
	if out != nil {
		t.Fatalf("normalizeBrokers(empty) = %+v, want nil", out)
	}
}

// TestNormalizeBrokersSingleMatchesLegacyDefaults asserts a single endpoint
// with zero transport fields comes out identical to what normalizeBrokerConfig
// produces today, so the 1-element case is provably unchanged.
func TestNormalizeBrokersSingleMatchesLegacyDefaults(t *testing.T) {
	in := &vmkit.BrokerConfig{
		Upstream:  "https://api.example.com",
		Secret:    vmkit.SecretRef{Name: "api", Ref: "env:CI_TOKEN"},
		Assurance: vmkit.BrokerAssuranceTrustedUpstream,
	}
	out, err := normalizeBrokers([]*vmkit.BrokerConfig{in})
	if err != nil {
		t.Fatalf("normalizeBrokers: %v", err)
	}
	if len(out) != 1 {
		t.Fatalf("normalizeBrokers returned %d endpoints, want 1", len(out))
	}
	legacy, err := normalizeBrokerConfig(in)
	if err != nil {
		t.Fatalf("normalizeBrokerConfig: %v", err)
	}
	if out[0].VsockPort != legacy.VsockPort {
		t.Fatalf("VsockPort = %d, want %d (legacy normalizeBrokerConfig)", out[0].VsockPort, legacy.VsockPort)
	}
	if out[0].GuestListen != legacy.GuestListen {
		t.Fatalf("GuestListen = %q, want %q (legacy normalizeBrokerConfig)", out[0].GuestListen, legacy.GuestListen)
	}
	if out[0].VsockPort != DefaultBrokerPort {
		t.Fatalf("VsockPort = %d, want default %d", out[0].VsockPort, DefaultBrokerPort)
	}
	if out[0].GuestListen != DefaultBrokerGuestListen {
		t.Fatalf("GuestListen = %q, want default %q", out[0].GuestListen, DefaultBrokerGuestListen)
	}
}

// TestNormalizeBrokersAutoAssignsDistinctTransport asserts that two endpoints
// which both leave transport fields zero are assigned distinct, increasing
// ports/guest-listens, in input order.
func TestNormalizeBrokersAutoAssignsDistinctTransport(t *testing.T) {
	first := &vmkit.BrokerConfig{
		Upstream:  "https://one.example.com",
		Secret:    vmkit.SecretRef{Name: "one", Ref: "env:ONE_TOKEN"},
		Assurance: vmkit.BrokerAssuranceTrustedUpstream,
	}
	second := &vmkit.BrokerConfig{
		Upstream:  "https://two.example.com",
		Secret:    vmkit.SecretRef{Name: "two", Ref: "env:TWO_TOKEN"},
		Assurance: vmkit.BrokerAssuranceTrustedUpstream,
	}
	out, err := normalizeBrokers([]*vmkit.BrokerConfig{first, second})
	if err != nil {
		t.Fatalf("normalizeBrokers: %v", err)
	}
	if len(out) != 2 {
		t.Fatalf("normalizeBrokers returned %d endpoints, want 2", len(out))
	}
	if out[0].Upstream != first.Upstream || out[1].Upstream != second.Upstream {
		t.Fatalf("input order not preserved: %+v", out)
	}
	if out[0].VsockPort != DefaultBrokerPort {
		t.Fatalf("out[0].VsockPort = %d, want %d", out[0].VsockPort, DefaultBrokerPort)
	}
	if out[1].VsockPort != DefaultBrokerPort+1 {
		t.Fatalf("out[1].VsockPort = %d, want %d", out[1].VsockPort, DefaultBrokerPort+1)
	}
	if out[0].GuestListen != DefaultBrokerGuestListen {
		t.Fatalf("out[0].GuestListen = %q, want %q", out[0].GuestListen, DefaultBrokerGuestListen)
	}
	if out[1].GuestListen != "127.0.0.1:18889" {
		t.Fatalf("out[1].GuestListen = %q, want 127.0.0.1:18889", out[1].GuestListen)
	}
	if out[0].VsockPort == out[1].VsockPort {
		t.Fatalf("both endpoints got VsockPort %d, want distinct", out[0].VsockPort)
	}
	if out[0].GuestListen == out[1].GuestListen {
		t.Fatalf("both endpoints got GuestListen %q, want distinct", out[0].GuestListen)
	}
}

// TestNormalizeBrokersExplicitPortSkippedByAutoAssign asserts that an
// explicitly-set VsockPort is respected and the auto-assigned endpoint skips
// it rather than colliding.
func TestNormalizeBrokersExplicitPortSkippedByAutoAssign(t *testing.T) {
	explicit := &vmkit.BrokerConfig{
		Upstream:  "https://one.example.com",
		Secret:    vmkit.SecretRef{Name: "one", Ref: "env:ONE_TOKEN"},
		VsockPort: DefaultBrokerPort,
		Assurance: vmkit.BrokerAssuranceTrustedUpstream,
	}
	auto := &vmkit.BrokerConfig{
		Upstream:  "https://two.example.com",
		Secret:    vmkit.SecretRef{Name: "two", Ref: "env:TWO_TOKEN"},
		Assurance: vmkit.BrokerAssuranceTrustedUpstream,
	}
	out, err := normalizeBrokers([]*vmkit.BrokerConfig{explicit, auto})
	if err != nil {
		t.Fatalf("normalizeBrokers: %v", err)
	}
	if out[0].VsockPort != DefaultBrokerPort {
		t.Fatalf("out[0].VsockPort = %d, want %d", out[0].VsockPort, DefaultBrokerPort)
	}
	if out[1].VsockPort != DefaultBrokerPort+1 {
		t.Fatalf("out[1].VsockPort (auto) = %d, want %d (skip the explicit collision)", out[1].VsockPort, DefaultBrokerPort+1)
	}
}

// TestNormalizeBrokersRejectsDuplicateExplicitVsockPort asserts fail-closed
// behaviour when two endpoints explicitly claim the same host vsock port.
func TestNormalizeBrokersRejectsDuplicateExplicitVsockPort(t *testing.T) {
	a := &vmkit.BrokerConfig{
		Upstream:  "https://one.example.com",
		Secret:    vmkit.SecretRef{Name: "one", Ref: "env:ONE_TOKEN"},
		VsockPort: 1032,
		Assurance: vmkit.BrokerAssuranceTrustedUpstream,
	}
	b := &vmkit.BrokerConfig{
		Upstream:  "https://two.example.com",
		Secret:    vmkit.SecretRef{Name: "two", Ref: "env:TWO_TOKEN"},
		VsockPort: 1032,
		Assurance: vmkit.BrokerAssuranceTrustedUpstream,
	}
	if _, err := normalizeBrokers([]*vmkit.BrokerConfig{a, b}); err == nil {
		t.Fatalf("normalizeBrokers accepted duplicate explicit VsockPort")
	}
}

// TestNormalizeBrokersRejectsMultipleProxy asserts fail-closed behaviour when
// more than one endpoint claims the single HTTPS_PROXY slot.
func TestNormalizeBrokersRejectsMultipleProxy(t *testing.T) {
	a := &vmkit.BrokerConfig{
		Upstream:  "https://one.example.com",
		Secret:    vmkit.SecretRef{Name: "one", Ref: "env:ONE_TOKEN"},
		Proxy:     true,
		Assurance: vmkit.BrokerAssuranceTrustedUpstream,
	}
	b := &vmkit.BrokerConfig{
		Upstream:  "https://two.example.com",
		Secret:    vmkit.SecretRef{Name: "two", Ref: "env:TWO_TOKEN"},
		Proxy:     true,
		Assurance: vmkit.BrokerAssuranceTrustedUpstream,
	}
	if _, err := normalizeBrokers([]*vmkit.BrokerConfig{a, b}); err == nil {
		t.Fatalf("normalizeBrokers accepted two endpoints with Proxy=true")
	}
}

// TestNormalizeBrokersRejectsDuplicateBaseURLEnvKey asserts fail-closed
// behaviour when two endpoints both declare the same guest base-URL env key,
// since the later endpoint's value would silently overwrite the earlier one's
// in the merged guest env.
func TestNormalizeBrokersRejectsDuplicateBaseURLEnvKey(t *testing.T) {
	a := &vmkit.BrokerConfig{
		Upstream:   "https://one.example.com",
		Secret:     vmkit.SecretRef{Name: "one", Ref: "env:ONE_TOKEN"},
		BaseURLEnv: map[string]string{"SHARED_URL": "http://127.0.0.1:18888"},
		Assurance:  vmkit.BrokerAssuranceTrustedUpstream,
	}
	b := &vmkit.BrokerConfig{
		Upstream:   "https://two.example.com",
		Secret:     vmkit.SecretRef{Name: "two", Ref: "env:TWO_TOKEN"},
		BaseURLEnv: map[string]string{"SHARED_URL": "http://127.0.0.1:18889"},
		Assurance:  vmkit.BrokerAssuranceTrustedUpstream,
	}
	_, err := normalizeBrokers([]*vmkit.BrokerConfig{a, b})
	if err == nil {
		t.Fatalf("normalizeBrokers accepted two endpoints with duplicate BaseURLEnv key")
	}
	if !strings.Contains(err.Error(), "SHARED_URL") {
		t.Fatalf("error = %q, want it to name the duplicated key SHARED_URL", err.Error())
	}
}

// TestNormalizeBrokersSurfacesPerEndpointValidation asserts a per-endpoint
// validation failure (here: a literal, non-reference secret) surfaces through
// normalizeBrokers exactly as normalizeBrokerConfig rejects it today — proving
// reuse rather than a parallel validator.
func TestNormalizeBrokersSurfacesPerEndpointValidation(t *testing.T) {
	valid := &vmkit.BrokerConfig{
		Upstream:  "https://one.example.com",
		Secret:    vmkit.SecretRef{Name: "one", Ref: "env:ONE_TOKEN"},
		Assurance: vmkit.BrokerAssuranceTrustedUpstream,
	}
	literalSecret := &vmkit.BrokerConfig{
		Upstream:  "https://two.example.com",
		Secret:    vmkit.SecretRef{Name: "two", Ref: "sk-pasted-literal"},
		Assurance: vmkit.BrokerAssuranceTrustedUpstream,
	}
	_, err := normalizeBrokers([]*vmkit.BrokerConfig{valid, literalSecret})
	if err == nil {
		t.Fatalf("normalizeBrokers accepted a literal (non-reference) broker secret")
	}
	if !strings.Contains(err.Error(), "must be <scheme>:<ref>") {
		t.Fatalf("error = %q, want it to surface normalizeBrokerConfig's message", err.Error())
	}
}
