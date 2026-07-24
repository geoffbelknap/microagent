package diagnostics

import (
	"testing"

	"github.com/geoffbelknap/microagent/pkg/vmkit"
)

// TestCapabilityDiagnosticCoverage is the true-north guarantee: every capability
// a backend declares must have a registered L1 diagnostic, so a declared
// capability can never ship without an instance-level check.
func TestCapabilityDiagnosticCoverage(t *testing.T) {
	for _, backend := range []string{vmkit.BackendLinuxKVM} {
		checks := capabilityChecksForBackend(backend)
		for _, capability := range vmkit.DeclaredCapabilities(backend) {
			if _, ok := checks[capability]; !ok {
				t.Errorf("backend %s declares capability %q but has no L1 diagnostic registered", backend, capability)
			}
		}
	}
}

func TestDeriveCapabilityDiagnosticsReady(t *testing.T) {
	host := &vmkit.HostSupport{
		Backend:                 vmkit.BackendLinuxKVM,
		SupervisorAvailable:     true,
		FrameworkAvailable:      true,
		GuestInitAvailable:      true,
		VsockAvailable:          true,
		UserNetworkingAvailable: true,
		UserNamespacesAvailable: true,
	}
	deriveCapabilityDiagnostics(host)
	if len(host.Capabilities) != len(vmkit.DeclaredCapabilities(vmkit.BackendLinuxKVM)) {
		t.Fatalf("capabilities = %#v", host.Capabilities)
	}
	for _, c := range host.Capabilities {
		if !c.Declared || !c.Ready || len(c.Missing) != 0 {
			t.Errorf("capability %q = %#v, want declared+ready with no missing", c.Capability, c)
		}
	}
}

func TestDeriveCapabilityDiagnosticsMissingPrereqs(t *testing.T) {
	// No supervisor, no firecracker binary, no vsock: snapshot/broker/exec
	// degrade; live-network-apply stays ready on pasta + user namespaces.
	host := &vmkit.HostSupport{
		Backend:                 vmkit.BackendLinuxKVM,
		UserNetworkingAvailable: true,
		UserNamespacesAvailable: true,
	}
	deriveCapabilityDiagnostics(host)
	byCap := map[vmkit.FeatureCapability]vmkit.CapabilityDiagnostic{}
	for _, c := range host.Capabilities {
		byCap[c.Capability] = c
	}
	if d := byCap[vmkit.FeatureCapabilitySnapshot]; d.Ready || len(d.Missing) == 0 {
		t.Errorf("snapshot should be not-ready with named missing prereqs without supervisor/binary: %#v", d)
	}
	if d := byCap[vmkit.FeatureCapabilityBrokerEndpoints]; d.Ready {
		t.Errorf("broker endpoints should not be ready without supervisor/vsock: %#v", d)
	}
	if d := byCap[vmkit.FeatureCapabilityLiveNetworkApply]; !d.Ready {
		t.Errorf("live network apply should be ready with pasta+userns: %#v", d)
	}
}

// TestDeriveCapabilityDiagnosticsUnwiredBackend confirms a backend with no
// registry produces no capability rows (rather than misleading not-ready ones).
func TestDeriveCapabilityDiagnosticsUnwiredBackend(t *testing.T) {
	host := &vmkit.HostSupport{Backend: vmkit.BackendAppleVF}
	deriveCapabilityDiagnostics(host)
	if host.Capabilities != nil {
		t.Errorf("apple-vf has no wired L1 registry; want nil capabilities, got %#v", host.Capabilities)
	}
}
