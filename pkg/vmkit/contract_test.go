package vmkit

import "testing"

func TestRuntimeContractCoversBothBackends(t *testing.T) {
	contract := NewRuntimeContract()
	if !contains(contract.Backends, BackendAppleVF) || !contains(contract.Backends, BackendFirecracker) {
		t.Fatalf("backends = %#v", contract.Backends)
	}
	for _, command := range []string{"prepare", "start", "run", "inspect", "halt", "quarantine", "stop", "kill", "delete"} {
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
	for _, signal := range []string{"guestReady", "shellReady", "resultReady", "mediationReady"} {
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
}

func TestRuntimeContractQuarantineSemantics(t *testing.T) {
	contract := NewRuntimeContract()
	for _, state := range contract.States {
		if state.Name == StateQuarantined {
			if !state.DiskPreserved || !state.EventHistoryKept || !state.RuntimeMayContinue {
				t.Fatalf("quarantined state = %#v", state)
			}
			return
		}
	}
	t.Fatal("contract missing quarantined state")
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
