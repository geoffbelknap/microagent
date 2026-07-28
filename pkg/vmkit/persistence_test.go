package vmkit

import (
	"reflect"
	"strings"
	"testing"
)

func TestPersistenceContractIsCompleteAndUnambiguous(t *testing.T) {
	contract := PersistenceContract()
	knownTiers := map[PersistenceTier]bool{}
	for _, tier := range contract.Tiers {
		if tier.Name == "" || tier.Description == "" || tier.Integrity == "" || tier.Retention == "" || tier.Recovery == "" {
			t.Fatalf("incomplete persistence tier: %#v", tier)
		}
		if knownTiers[tier.Name] {
			t.Fatalf("duplicate persistence tier %q", tier.Name)
		}
		knownTiers[tier.Name] = true
	}
	for _, tier := range []PersistenceTier{PersistenceRecoverable, PersistenceOperational, PersistenceAudit, PersistenceEvidence} {
		if !knownTiers[tier] {
			t.Errorf("missing persistence tier %q", tier)
		}
	}

	seenIDs := map[string]bool{}
	seenPaths := map[string]string{}
	for _, artifact := range contract.Artifacts {
		if artifact.ID == "" || artifact.Path == "" || artifact.Scope == "" || artifact.Writer == "" ||
			artifact.Mode == "" || artifact.Atomicity == "" || artifact.Integrity == "" ||
			artifact.Ordering == "" || artifact.Retention == "" || artifact.CleanupOwner == "" ||
			artifact.Recovery == "" {
			t.Fatalf("incomplete persisted artifact: %#v", artifact)
		}
		if !knownTiers[artifact.Tier] {
			t.Errorf("%s has unknown tier %q", artifact.ID, artifact.Tier)
		}
		if seenIDs[artifact.ID] {
			t.Errorf("duplicate persisted artifact id %q", artifact.ID)
		}
		seenIDs[artifact.ID] = true
		pathKey := artifact.Scope + ":" + artifact.Path
		if prior := seenPaths[pathKey]; prior != "" {
			t.Errorf("%s and %s classify the same path %q", prior, artifact.ID, pathKey)
		}
		seenPaths[pathKey] = artifact.ID
	}
}

func TestPersistenceContractCoversOwnedArtifactFamilies(t *testing.T) {
	got := map[string]bool{}
	for _, artifact := range PersistenceContract().Artifacts {
		got[artifact.ID] = true
	}
	for _, id := range []string{
		"build.working-set",
		"build.base-cache",
		"images.cache",
		"models.cache",
		"kernels.cache",
		"runtime.state",
		"runtime.latest-event",
		"runtime.result",
		"workspace.manifest",
		"workspace.rootfs",
		"workspace.disks",
		"workspace.egress-ca",
		"volumes.registry",
		"volumes.disks",
		"snapshots.standard",
		"events.lifecycle",
		"audit.egress",
		"audit.broker",
		"audit.broker-capture",
		"audit.secret-access",
		"logs.serial",
		"runtime.plumbing",
		"runtime.activity",
		"host-workers.runtime",
		"model-runners.runtime",
		"snapshots.forensic",
		"registry.credentials",
	} {
		if !got[id] {
			t.Errorf("missing persisted artifact family %q", id)
		}
	}
}

func TestPersistenceContractPinsSensitiveBoundaries(t *testing.T) {
	byID := map[string]PersistedArtifact{}
	for _, artifact := range PersistenceContract().Artifacts {
		byID[artifact.ID] = artifact
	}
	forensic := byID["snapshots.forensic"]
	if forensic.Tier != PersistenceEvidence || !forensic.ContainsSecret {
		t.Fatalf("forensic persistence contract = %#v", forensic)
	}
	if !strings.Contains(forensic.Retention, "explicit") || !strings.Contains(forensic.Integrity, "hash") {
		t.Fatalf("forensic retention/integrity is not explicit: %#v", forensic)
	}
	for _, id := range []string{"events.lifecycle", "audit.egress", "audit.broker", "audit.secret-access"} {
		artifact := byID[id]
		if artifact.Tier != PersistenceAudit || artifact.Ordering == "" {
			t.Errorf("%s audit contract = %#v", id, artifact)
		}
	}
	if !byID["registry.credentials"].ContainsSecret {
		t.Fatal("registry credentials must be classified as secret-bearing")
	}
}

func TestRuntimeContractPublishesPersistenceContract(t *testing.T) {
	if !reflect.DeepEqual(NewRuntimeContract().Persistence, PersistenceContract()) {
		t.Fatal("runtime contract persistence differs from canonical persistence contract")
	}
}
