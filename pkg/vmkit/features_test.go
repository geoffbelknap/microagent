package vmkit

import "testing"

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

func TestSnapshotFeatureIsBackendNeutralWithExplicitAppleVFGap(t *testing.T) {
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
	assertFeatureSupport(t, feature, BackendAppleVF, false)
	assertFeatureSupport(t, feature, BackendWindowsHyperV, false)
	var appleVF FeatureBackend
	for _, backend := range feature.Backends {
		if backend.Backend == BackendAppleVF {
			appleVF = backend
			break
		}
	}
	if !appleVF.Required || appleVF.Ready || appleVF.Status != "open" || appleVF.GapID != "gap.apple-vf.snapshot" {
		t.Fatalf("apple-vf snapshot support = %#v, want required open gap", appleVF)
	}
}

func TestFeatureLookupCoversRepresentativeAdapters(t *testing.T) {
	for _, command := range []string{
		"create", "start", "status", "dispatch", "exec", "apply", "cp", "artifact", "commit", "pause", "resume", "snapshot", "model", "volume", "image", "kernel", "rootfs build", "host", "doctor", "contract",
	} {
		if _, ok := FeatureForCLICommand(command); !ok {
			t.Fatalf("CLI command %q has no feature contract", command)
		}
	}
	for _, tool := range []string{
		"workspace.create", "workspace.start", "workspace.exec", "workspace.dispatch", "workspace.apply", "workspace.commit", "snapshot.create", "snapshot.list", "snapshot.delete", "models.serve", "volume.create", "images.pull", "kernel.install", "rootfs.build", "contract.get",
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
