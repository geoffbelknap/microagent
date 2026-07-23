package vmkit

import (
	"strings"
	"testing"
)

func TestFeatureContractsDeclareBackendSupport(t *testing.T) {
	features := FeatureContracts()
	if len(features) == 0 {
		t.Fatal("FeatureContracts returned no features")
	}
	seen := map[string]bool{}
	for _, feature := range features {
		if feature.ID == "" {
			t.Fatalf("feature without ID: %#v", feature)
		}
		if seen[feature.ID] {
			t.Fatalf("duplicate feature ID %q", feature.ID)
		}
		seen[feature.ID] = true
		if feature.OwnerPackage == "" {
			t.Fatalf("%s has no owner package", feature.ID)
		}
		if feature.Scope == "" {
			t.Fatalf("%s has no scope", feature.ID)
		}
		if len(feature.Backends) != 3 {
			t.Fatalf("%s backend support count = %d, want 3", feature.ID, len(feature.Backends))
		}
		for _, backend := range feature.Backends {
			if !IsKnownBackend(backend.Backend) {
				t.Fatalf("%s includes unknown backend %q", feature.ID, backend.Backend)
			}
			if backend.Required && !backend.Ready && backend.GapID == "" {
				t.Fatalf("%s requires %s but has no explicit gap record: %#v", feature.ID, backend.Backend, backend)
			}
		}
	}
}

func TestSnapshotFeatureIsBackendNeutralAcrossSupportedBackends(t *testing.T) {
	feature, ok := FeatureForCLICommand("snapshot")
	if !ok {
		t.Fatal("snapshot CLI command is not mapped to a feature contract")
	}
	if feature.ID != "workspace.snapshot" {
		t.Fatalf("snapshot feature = %q, want workspace.snapshot", feature.ID)
	}
	if feature.Scope != FeatureBackendNeutral || feature.Capability != FeatureCapabilitySnapshot {
		t.Fatalf("snapshot scope/capability = %s/%s, want backend-neutral/Snapshot", feature.Scope, feature.Capability)
	}
	assertFeatureSupport(t, feature, BackendLinuxKVM, true)
	assertFeatureSupport(t, feature, BackendAppleVF, true)
	assertFeatureSupport(t, feature, BackendWindowsHyperV, false)
	var appleVF FeatureBackend
	for _, backend := range feature.Backends {
		if backend.Backend == BackendAppleVF {
			appleVF = backend
			break
		}
	}
	if !appleVF.Required || !appleVF.Ready || appleVF.Status != "ready" || appleVF.GapID != "" || appleVF.Reason != "" {
		t.Fatalf("apple-vf snapshot support = %#v, want required ready support", appleVF)
	}
}

func TestFeatureContractsRequireSupportedBackendsOnly(t *testing.T) {
	for _, feature := range FeatureContracts() {
		if feature.Scope != FeatureBackendNeutral && feature.Scope != FeatureHostTooling {
			continue
		}
		required := map[string]bool{}
		for _, backend := range feature.Backends {
			if backend.Required {
				required[backend.Backend] = true
			}
		}
		if !required[BackendAppleVF] || !required[BackendLinuxKVM] {
			t.Fatalf("%s required backends = %#v, want apple-vf and linux-kvm", feature.ID, required)
		}
		if required[BackendWindowsHyperV] {
			t.Fatalf("%s marks experimental windows-hyperv as required: %#v", feature.ID, required)
		}
	}
}

func TestFeatureLookupCoversPublicAdapters(t *testing.T) {
	for _, command := range []string{
		"init",
		"run",
		"dispatch",
		"create",
		"create --from-snapshot",
		"start",
		"start --from-snapshot",
		"apply",
		"clone",
		"commit",
		"cp",
		"artifact",
		"network",
		"network status",
		"model",
		"volume",
		"supervise",
		"connect",
		"exec",
		"list",
		"ls",
		"ps",
		"status",
		"wait",
		"result",
		"logs",
		"events",
		"egress",
		"stats",
		"snapshot",
		"secret",
		"secret check",
		"secret audit",
		"profiles",
		"image",
		"perf",
		"halt",
		"quarantine",
		"pause",
		"resume",
		"stop",
		"kill",
		"delete",
		"contract",
		"host",
		"doctor",
		"kernel",
		"rootfs build",
	} {
		if _, ok := FeatureForCLICommand(command); !ok {
			t.Fatalf("CLI command %q has no feature contract", command)
		}
	}
	for _, tool := range []string{
		"microagent.describe",
		"workspace.create",
		"workspace.start",
		"workspace.wait",
		"workspace.exec",
		"workspace.dispatch",
		"workspace.halt",
		"workspace.kill",
		"workspace.quarantine",
		"workspace.pause",
		"workspace.resume",
		"workspace.delete",
		"workspace.list",
		"workspace.inspect",
		"workspace.result",
		"workspace.stats",
		"workspace.logs",
		"workspace.events",
		"workspace.egress",
		"workspace.clone",
		"workspace.apply",
		"workspace.commit",
		"workspace.estimate_cost",
		"artifacts.list",
		"artifacts.get",
		"snapshot.create",
		"snapshot.list",
		"snapshot.delete",
		"network.inspect",
		"volume.create",
		"volume.list",
		"volume.inspect",
		"volume.delete",
		"images.pull",
		"images.list",
		"images.push",
		"images.tag",
		"images.delete",
		"images.prune",
		"models.pull",
		"models.list",
		"models.remove",
		"models.prune",
		"models.serve",
		"models.stop",
		"models.runners",
		"models.policy.validate",
		"models.policy.evaluate",
		"profiles.list",
		"host.inspect",
		"doctor.check",
		"contract.get",
		"kernel.verify",
		"kernel.install",
		"rootfs.build",
		"cp",
	} {
		if _, ok := FeatureForMCPTool(tool); !ok {
			t.Fatalf("MCP tool %q has no feature contract", tool)
		}
	}
}

func assertFeatureSupport(t *testing.T, feature FeatureContract, backend string, want bool) {
	t.Helper()
	got, _ := BackendSupportsFeature(backend, feature)
	if got != want {
		t.Fatalf("BackendSupportsFeature(%s, %s) = %v, want %v", backend, feature.ID, got, want)
	}
}

// TestBrokerFeatureDeclaresBackendGaps pins broker credential-injection
// endpoints as a declared capability with explicit gap records where the
// supervisor cannot serve the broker vsock listener target. Before this, a
// broker workspace on apple-vf failed at start with a misleading protocol
// error ("vsock listener target must be host:port...") and nothing in the
// contract recorded the feature as linux-kvm-only.
func TestBrokerFeatureDeclaresBackendGaps(t *testing.T) {
	feature, ok := FeatureForCLICommand("create --broker-upstream")
	if !ok {
		t.Fatal("create --broker-upstream is not mapped to a feature contract")
	}
	if feature.ID != "workspace.broker" {
		t.Fatalf("broker feature = %q, want workspace.broker", feature.ID)
	}
	if feature.Scope != FeatureBackendNeutral || feature.Capability != FeatureCapabilityBrokerEndpoints {
		t.Fatalf("broker scope/capability = %s/%s, want backend-neutral/BrokerEndpoints", feature.Scope, feature.Capability)
	}
	assertFeatureSupport(t, feature, BackendLinuxKVM, true)
	assertFeatureSupport(t, feature, BackendAppleVF, false)
	assertFeatureSupport(t, feature, BackendWindowsHyperV, false)
	for _, backend := range []string{BackendAppleVF, BackendWindowsHyperV} {
		gap, ok := featureGapForBackend(feature, backend)
		if !ok {
			t.Fatalf("broker feature has no explicit gap record for %s", backend)
		}
		if gap.ID == "" || gap.Status == "" || gap.Reason == "" {
			t.Fatalf("broker gap for %s is incomplete: %#v", backend, gap)
		}
	}
	err := NewUnsupportedFeatureError(BackendAppleVF, feature, "broker endpoints")
	if err.GapID == "" || err.Reason == "" {
		t.Fatalf("UnsupportedFeatureError missing gap detail: %#v", err)
	}
	if msg := err.Error(); !strings.Contains(msg, "apple-vf") || !strings.Contains(msg, "broker endpoints") {
		t.Fatalf("error %q must name the backend and the operation", msg)
	}
}
