package diagnostics

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/geoffbelknap/microagent/pkg/vmkit"
)

// stubE2fsprogsLookup pins the e2fsprogs resolver so apple-vf offline-copy
// diagnostics do not depend on whether the test host has brew's e2fsprogs.
func stubE2fsprogsLookup(t *testing.T, found bool) {
	t.Helper()
	orig := lookupE2fsprogsTool
	lookupE2fsprogsTool = func(name string) (string, bool) {
		if found {
			return "/stub/" + name, true
		}
		return name, false
	}
	t.Cleanup(func() { lookupE2fsprogsTool = orig })
}

// setEgressDatapathBin points MICROAGENT_EGRESS_DATAPATH_BIN at a real
// executable file, so apple-vf egress diagnostics do not depend on the test
// binary's own path or ambient environment.
func setEgressDatapathBin(t *testing.T) {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "microagent")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv(vmkit.EgressDatapathBinEnv, bin)
}

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
	stubE2fsprogsLookup(t, true)
	setEgressDatapathBin(t)

	// A healthy macOS 14+ host: every declared capability reads ready.
	ready := &vmkit.HostSupport{
		Backend:                 vmkit.BackendAppleVF,
		SupervisorAvailable:     true,
		FrameworkAvailable:      true,
		VirtualizationSupported: true,
		PauseResumeAvailable:    true,
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

	// A macOS 13-shaped host: the framework and VZVirtualMachine pause are
	// present but save/restore is not, so snapshot must read not-ready while
	// pause/resume and the rest stay ready.
	noSaveRestore := &vmkit.HostSupport{
		Backend:                 vmkit.BackendAppleVF,
		SupervisorAvailable:     true,
		FrameworkAvailable:      true,
		VirtualizationSupported: true,
		PauseResumeAvailable:    true,
	}
	deriveCapabilityDiagnostics(noSaveRestore)
	for _, c := range noSaveRestore.Capabilities {
		switch c.Capability {
		case vmkit.FeatureCapabilitySnapshotCreate,
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

// TestOfflineFileCopyAppleVFCheck proves the offline-copy L1 verifies the
// e2fsprogs tools the copy/commit/artifact paths actually exec, instead of
// claiming ready unconditionally, and names each missing tool with the brew
// remediation.
func TestOfflineFileCopyAppleVFCheck(t *testing.T) {
	t.Run("all tools present", func(t *testing.T) {
		stubE2fsprogsLookup(t, true)
		ready, missing := offlineFileCopyAppleVFCheck(&vmkit.HostSupport{Backend: vmkit.BackendAppleVF})
		if !ready || len(missing) != 0 {
			t.Fatalf("ready = %t, missing = %#v, want ready with nothing missing", ready, missing)
		}
	})
	t.Run("tools missing", func(t *testing.T) {
		stubE2fsprogsLookup(t, false)
		ready, missing := offlineFileCopyAppleVFCheck(&vmkit.HostSupport{Backend: vmkit.BackendAppleVF})
		if ready {
			t.Fatal("ready = true without e2fsprogs")
		}
		if len(missing) != len(e2fsprogsTools) {
			t.Fatalf("missing = %#v, want one entry per tool %v", missing, e2fsprogsTools)
		}
		for i, tool := range e2fsprogsTools {
			if !strings.Contains(missing[i], tool) || !strings.Contains(missing[i], "brew install e2fsprogs") {
				t.Errorf("missing[%d] = %q, want to name %q and the brew remediation", i, missing[i], tool)
			}
		}
	})
}

// TestEgressMediationAppleVFCheck proves the egress-mediation L1 verifies the
// datapath binary the supervisor will exec — resolved the way the boot path
// resolves it — instead of only requiring the supervisor. A supervisor with no
// working MICROAGENT_EGRESS_DATAPATH_BIN fails every mediated-egress boot, so
// that must read not-ready with the env var named.
func TestEgressMediationAppleVFCheck(t *testing.T) {
	host := &vmkit.HostSupport{Backend: vmkit.BackendAppleVF, SupervisorAvailable: true}

	t.Run("datapath binary resolves", func(t *testing.T) {
		setEgressDatapathBin(t)
		ready, missing := egressMediationAppleVFCheck(host)
		if !ready || len(missing) != 0 {
			t.Fatalf("ready = %t, missing = %#v, want ready with nothing missing", ready, missing)
		}
	})
	t.Run("datapath binary does not exist", func(t *testing.T) {
		t.Setenv(vmkit.EgressDatapathBinEnv, filepath.Join(t.TempDir(), "no-such-binary"))
		ready, missing := egressMediationAppleVFCheck(host)
		if ready {
			t.Fatal("ready = true with a nonexistent egress datapath binary")
		}
		if len(missing) != 1 || !strings.Contains(missing[0], vmkit.EgressDatapathBinEnv) || !strings.Contains(missing[0], "--egress-datapath") {
			t.Fatalf("missing = %#v, want one entry naming %s and --egress-datapath", missing, vmkit.EgressDatapathBinEnv)
		}
	})
	t.Run("datapath binary is not executable", func(t *testing.T) {
		bin := filepath.Join(t.TempDir(), "microagent")
		if err := os.WriteFile(bin, []byte("data"), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Setenv(vmkit.EgressDatapathBinEnv, bin)
		ready, missing := egressMediationAppleVFCheck(host)
		if ready {
			t.Fatal("ready = true with a non-executable egress datapath binary")
		}
		if len(missing) != 1 || !strings.Contains(missing[0], "not executable") {
			t.Fatalf("missing = %#v, want one entry reporting the binary is not executable", missing)
		}
	})
	t.Run("supervisor missing is still reported", func(t *testing.T) {
		setEgressDatapathBin(t)
		ready, missing := egressMediationAppleVFCheck(&vmkit.HostSupport{Backend: vmkit.BackendAppleVF})
		if ready || len(missing) != 1 || missing[0] != "supervisor" {
			t.Fatalf("ready = %t, missing = %#v, want not-ready with only the supervisor missing", ready, missing)
		}
	})
}
