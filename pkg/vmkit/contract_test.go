package vmkit

import (
	"strings"
	"testing"
)

func TestRuntimeContractCoversSupportedBackends(t *testing.T) {
	contract := NewRuntimeContract()
	if !contains(contract.Backends, BackendAppleVF) || !contains(contract.Backends, BackendLinuxKVM) || len(contract.Backends) != 2 {
		t.Fatalf("backends = %#v", contract.Backends)
	}
	for _, command := range []string{"prepare", "start", "run", "inspect", "halt", "quarantine", "pause", "resume", "snapshot", "stop", "kill", "delete"} {
		if !contractHasItem(contract.Commands, command) {
			t.Fatalf("contract missing command %q", command)
		}
	}
	for _, state := range []VMState{StatePrepared, StateStarting, StateRunning, StateHalted, StateQuarantined, StateStopped, StateFailed} {
		if !contractHasState(contract.States, state) {
			t.Fatalf("contract missing state %q", state)
		}
	}
}

func TestRuntimeContractCoversAgentRuntimeChannels(t *testing.T) {
	contract := NewRuntimeContract()
	for _, signal := range []string{"guestReady", "shellReady", "execReady", "resultReady", "mediationReady"} {
		if !contractHasItem(contract.ReadinessSignals, signal) {
			t.Fatalf("contract missing readiness signal %q", signal)
		}
	}
	for _, field := range []string{"identity", "backend", "resultPath", "startedAt", "completedAt", "exitCode", "stdout", "stderr", "error"} {
		if !contractHasItem(contract.ResultFields, field) {
			t.Fatalf("contract missing result field %q", field)
		}
	}
	for _, channel := range []string{"ingress", "egress"} {
		if !contractHasItem(contract.ArtifactChannels, channel) {
			t.Fatalf("contract missing artifact channel %q", channel)
		}
	}
	for _, field := range []string{"enabled", "required", "port", "target", "failClosed"} {
		if !contains(contract.Mediation.Fields, field) {
			t.Fatalf("contract mediation fields = %#v, missing %q", contract.Mediation.Fields, field)
		}
	}
	if contract.Verification.Name != "verification" {
		t.Fatalf("verification = %#v", contract.Verification)
	}
	if contract.CapabilityComposition.Name != "capabilityComposition" {
		t.Fatalf("capability composition = %#v", contract.CapabilityComposition)
	}
	if len(contract.Durability.Tiers) == 0 || len(contract.Durability.Transitions) == 0 {
		t.Fatalf("durability contract = %#v", contract.Durability)
	}
	if len(contract.Persistence.Tiers) == 0 || len(contract.Persistence.Artifacts) == 0 {
		t.Fatalf("persistence contract = %#v", contract.Persistence)
	}
}

// TestRuntimeContractQuarantineSemantics: quarantine STOPS the runtime. It
// preserves disk and event history and severs host-side paths, but the VM does
// not keep running — so RuntimeMayContinue must be false. Claiming otherwise
// told callers a severed agent was still live and made the resume ladder's
// memory-preserving rung look reachable when it never was. Evidence is captured
// before containment, not preserved by it.
func TestRuntimeContractQuarantineSemantics(t *testing.T) {
	contract := NewRuntimeContract()
	for _, state := range contract.States {
		if state.Name == StateQuarantined {
			if !state.DiskPreserved || !state.EventHistoryKept {
				t.Fatalf("quarantine must preserve disk and events: %#v", state)
			}
			if state.RuntimeMayContinue {
				t.Fatalf("quarantine stops the runtime; RuntimeMayContinue must be false: %#v", state)
			}
			if !strings.Contains(state.Description, "stop") {
				t.Fatalf("quarantined description must say the runtime stops: %q", state.Description)
			}
			return
		}
	}
	t.Fatal("contract missing quarantined state")
}

func TestRuntimeContractPausedSemantics(t *testing.T) {
	contract := NewRuntimeContract()
	for _, state := range contract.States {
		if state.Name == StatePaused {
			if !state.DiskPreserved || !state.EventHistoryKept || !state.RuntimeMayContinue {
				t.Fatalf("paused state = %#v", state)
			}
			return
		}
	}
	t.Fatal("contract missing paused state")
}

func TestRuntimeContractParityScopeNamesSupportedBackends(t *testing.T) {
	scope := NewRuntimeContract().Parity.Scope
	if want := "Supported Firecracker and Apple VF"; !strings.Contains(scope, want) {
		t.Fatalf("parity scope = %q, want %q", scope, want)
	}
}

func contractHasItem(items []ContractItem, name string) bool {
	for _, item := range items {
		if item.Name == name {
			return true
		}
	}
	return false
}

func contractHasState(states []ContractState, name VMState) bool {
	for _, state := range states {
		if state.Name == name {
			return true
		}
	}
	return false
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
