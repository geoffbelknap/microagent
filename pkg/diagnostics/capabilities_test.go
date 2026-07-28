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
		EgressTProxyReady:       true,
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
	for _, capability := range []vmkit.FeatureCapability{
		vmkit.FeatureCapabilityPauseResume,
		vmkit.FeatureCapabilitySnapshotCreate,
		vmkit.FeatureCapabilitySnapshotRestore,
		vmkit.FeatureCapabilitySnapshotFork,
	} {
		if d := byCap[capability]; d.Ready || len(d.Missing) == 0 {
			t.Errorf("%s should be not-ready with named missing prereqs without supervisor/binary: %#v", capability, d)
		}
	}
	if d := byCap[vmkit.FeatureCapabilityBrokerEndpoints]; d.Ready {
		t.Errorf("broker endpoints should not be ready without supervisor/vsock: %#v", d)
	}
	if d := byCap[vmkit.FeatureCapabilityLiveNetworkApply]; !d.Ready {
		t.Errorf("live network apply should be ready with pasta+userns: %#v", d)
	}
}

// TestDeriveCapabilityDiagnosticsAppleVF exercises the apple-vf L1 registry
// from host facts shaped like the supervisor's real `host` response.
func TestDeriveCapabilityDiagnosticsAppleVF(t *testing.T) {
	// A healthy macOS 14+ host: every declared capability reads ready.
	ready := &vmkit.HostSupport{
		Backend:                 vmkit.BackendAppleVF,
		SupervisorAvailable:     true,
		FrameworkAvailable:      true,
		VirtualizationSupported: true,
		SnapshotAvailable:       true,
	}
	deriveCapabilityDiagnostics(ready)
	if len(ready.Capabilities) != len(vmkit.DeclaredCapabilities(vmkit.BackendAppleVF)) {
		t.Fatalf("capabilities = %#v", ready.Capabilities)
	}
	for _, c := range ready.Capabilities {
		if !c.Ready {
			t.Errorf("capability %q not ready on a healthy macOS 14+ host: %#v", c.Capability, c)
		}
	}

	// A macOS 13-shaped host: the framework is present but save/restore is
	// not, so snapshot must read not-ready while the rest stay ready.
	noSaveRestore := &vmkit.HostSupport{
		Backend:                 vmkit.BackendAppleVF,
		SupervisorAvailable:     true,
		FrameworkAvailable:      true,
		VirtualizationSupported: true,
	}
	deriveCapabilityDiagnostics(noSaveRestore)
	for _, c := range noSaveRestore.Capabilities {
		switch c.Capability {
		case vmkit.FeatureCapabilityPauseResume,
			vmkit.FeatureCapabilitySnapshotCreate,
			vmkit.FeatureCapabilitySnapshotRestore,
			vmkit.FeatureCapabilitySnapshotFork:
			if c.Ready {
				t.Errorf("%s must not be ready without save/restore support: %#v", c.Capability, c)
			}
		default:
			if !c.Ready {
				t.Errorf("capability %q should stay ready without save/restore: %#v", c.Capability, c)
			}
		}
	}

	down := &vmkit.HostSupport{Backend: vmkit.BackendAppleVF}
	deriveCapabilityDiagnostics(down)
	for _, c := range down.Capabilities {
		if c.Capability == vmkit.FeatureCapabilityOfflineFileCopy {
			if !c.Ready {
				t.Errorf("offline file copy should not require a running supervisor: %#v", c)
			}
			continue
		}
		if c.Ready {
			t.Errorf("capability %q should not be ready without a supervisor: %#v", c.Capability, c)
		}
	}
}
