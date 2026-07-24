package diagnostics

import (
	"testing"

	"github.com/geoffbelknap/microagent/pkg/vmkit"
)

// TestCapabilityDiagnosticCoverage is the true-north guarantee: every capability
// a backend declares must have a registered L1 diagnostic, so a declared
// capability can never ship without an instance-level check.
func TestCapabilityDiagnosticCoverage(t *testing.T) {
	for _, backend := range []string{vmkit.BackendLinuxKVM, vmkit.BackendAppleVF} {
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
// windows-hyperv is experimental and intentionally unwired.
func TestDeriveCapabilityDiagnosticsUnwiredBackend(t *testing.T) {
	host := &vmkit.HostSupport{Backend: vmkit.BackendWindowsHyperV}
	deriveCapabilityDiagnostics(host)
	if host.Capabilities != nil {
		t.Errorf("windows-hyperv has no wired L1 registry; want nil capabilities, got %#v", host.Capabilities)
	}
}

// TestDeriveCapabilityDiagnosticsAppleVF exercises the apple-vf L1 registry from
// constructed host facts (the real facts come from the external supervisor and
// need macOS validation, but the derivation logic is testable here).
func TestDeriveCapabilityDiagnosticsAppleVF(t *testing.T) {
	ready := &vmkit.HostSupport{Backend: vmkit.BackendAppleVF, SupervisorAvailable: true, FrameworkAvailable: true}
	deriveCapabilityDiagnostics(ready)
	if len(ready.Capabilities) != len(vmkit.DeclaredCapabilities(vmkit.BackendAppleVF)) {
		t.Fatalf("capabilities = %#v", ready.Capabilities)
	}
	for _, c := range ready.Capabilities {
		if !c.Ready {
			t.Errorf("capability %q not ready with supervisor+framework: %#v", c.Capability, c)
		}
	}
	down := &vmkit.HostSupport{Backend: vmkit.BackendAppleVF}
	deriveCapabilityDiagnostics(down)
	for _, c := range down.Capabilities {
		if c.Ready {
			t.Errorf("capability %q should not be ready without a supervisor: %#v", c.Capability, c)
		}
	}
}
