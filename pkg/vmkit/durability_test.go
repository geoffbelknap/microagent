package vmkit

import "testing"

func TestDurabilityContractCoversLifecycleTransitions(t *testing.T) {
	contract := DurabilityContract()
	if len(contract.Tiers) != 4 {
		t.Fatalf("tiers = %#v, want four lifetime boundaries", contract.Tiers)
	}
	seen := map[string]bool{}
	for _, transition := range contract.Transitions {
		if transition.Operation == "" || seen[transition.Operation] {
			t.Fatalf("invalid or duplicate transition %q", transition.Operation)
		}
		seen[transition.Operation] = true
		for field, effect := range map[string]DurabilityEffect{
			"memory": transition.Memory, "processes": transition.Processes,
			"networkConnections": transition.NetworkConnections, "rootfs": transition.Rootfs,
			"identity": transition.Identity, "eventHistory": transition.EventHistory,
			"results": transition.Results, "artifactDeclarations": transition.ArtifactDeclarations,
			"snapshots": transition.Snapshots, "namedVolumes": transition.NamedVolumes,
		} {
			if !knownDurabilityEffect(effect) {
				t.Errorf("%s %s = %q, want a declared durability effect", transition.Operation, field, effect)
			}
		}
	}
	for _, operation := range []string{"pause", "resume", "halt", "stop", "kill", "quarantine", "snapshot.create", "snapshot.restore", "snapshot.fork", "delete"} {
		if !seen[operation] {
			t.Errorf("missing durability transition %q", operation)
		}
	}
}

func TestDurabilityContractPinsDestructiveBoundaries(t *testing.T) {
	deleteTransition := durabilityTransition(t, "delete")
	if deleteTransition.Rootfs != DurabilityRemoved ||
		deleteTransition.Identity != DurabilityRemoved ||
		deleteTransition.EventHistory != DurabilityRemoved ||
		deleteTransition.Snapshots != DurabilityRemoved {
		t.Fatalf("delete transition = %#v", deleteTransition)
	}
	if deleteTransition.NamedVolumes != DurabilityPreserved {
		t.Fatalf("delete must preserve independently managed volumes: %#v", deleteTransition)
	}

	quarantine := durabilityTransition(t, "quarantine")
	if quarantine.Memory != DurabilityDiscarded || quarantine.Processes != DurabilityDiscarded {
		t.Fatalf("quarantine must not imply runtime preservation: %#v", quarantine)
	}
	if quarantine.Rootfs != DurabilityPreserved || quarantine.EventHistory != DurabilityPreserved {
		t.Fatalf("quarantine must preserve workspace evidence: %#v", quarantine)
	}
}

func TestDurabilityContractDistinguishesRestoreAndFork(t *testing.T) {
	restore := durabilityTransition(t, "snapshot.restore")
	if restore.Identity != DurabilityPreserved || restore.EventHistory != DurabilityPreserved {
		t.Fatalf("in-place restore must preserve host identity and history: %#v", restore)
	}
	fork := durabilityTransition(t, "snapshot.fork")
	if fork.Identity != DurabilityReset || fork.EventHistory != DurabilityReset {
		t.Fatalf("fork must create fresh host identity and history: %#v", fork)
	}
	for _, transition := range []ContractDurabilityTransition{restore, fork} {
		if transition.NetworkConnections != DurabilityReset {
			t.Fatalf("%s must require guest connections to reconnect: %#v", transition.Operation, transition)
		}
	}
}

func durabilityTransition(t *testing.T, operation string) ContractDurabilityTransition {
	t.Helper()
	for _, transition := range DurabilityContract().Transitions {
		if transition.Operation == operation {
			return transition
		}
	}
	t.Fatalf("missing durability transition %q", operation)
	return ContractDurabilityTransition{}
}

func knownDurabilityEffect(effect DurabilityEffect) bool {
	switch effect {
	case DurabilityPreserved, DurabilityDiscarded, DurabilityCaptured,
		DurabilityRestored, DurabilityCopied, DurabilityReset,
		DurabilityRemoved, DurabilityNotGuaranteed:
		return true
	default:
		return false
	}
}
