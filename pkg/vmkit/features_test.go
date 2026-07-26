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
		if len(feature.Backends) != 2 {
			t.Fatalf("%s backend support count = %d, want 2", feature.ID, len(feature.Backends))
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

func TestOperationContractsAreUniqueAndOwned(t *testing.T) {
	features := map[string]bool{}
	for _, feature := range FeatureContracts() {
		features[feature.ID] = true
	}
	ids := map[OperationID]bool{}
	cli := map[string]OperationID{}
	mcp := map[string]OperationID{}
	for _, operation := range OperationContracts() {
		if operation.ID == "" || ids[operation.ID] {
			t.Fatalf("invalid or duplicate operation ID %q", operation.ID)
		}
		ids[operation.ID] = true
		if !features[operation.FeatureID] {
			t.Fatalf("operation %s references unknown feature %q", operation.ID, operation.FeatureID)
		}
		for _, command := range operation.CLICommands {
			if prior := cli[command]; prior != "" {
				t.Fatalf("CLI command %q belongs to both %s and %s", command, prior, operation.ID)
			}
			cli[command] = operation.ID
		}
		for _, tool := range operation.MCPTools {
			if prior := mcp[tool]; prior != "" {
				t.Fatalf("MCP tool %q belongs to both %s and %s", tool, prior, operation.ID)
			}
			mcp[tool] = operation.ID
		}
		if len(operation.CLICommands) > 0 || len(operation.MCPTools) > 0 {
			if operation.RequestType == "" || operation.ResultType == "" {
				t.Errorf("%s adapter operation has no request/result type ownership", operation.ID)
			}
			if operation.Effect == "" || operation.Idempotency == "" {
				t.Errorf("%s adapter operation has incomplete effect/idempotency policy", operation.ID)
			}
			if operation.Effect == OperationEffectRead && len(operation.SideEffects) != 0 {
				t.Errorf("%s read operation declares side effects %#v", operation.ID, operation.SideEffects)
			}
			if operation.Effect != OperationEffectRead && len(operation.SideEffects) == 0 {
				t.Errorf("%s mutating operation has no declared side effects", operation.ID)
			}
		}
	}
}

func TestLifecycleOperationsDeclareIndependentPolicies(t *testing.T) {
	tests := []struct {
		id          OperationID
		effect      OperationEffect
		idempotency OperationIdempotency
	}{
		{OperationWorkspaceCreate, OperationEffectMutation, OperationIdempotencyReplayable},
		{OperationWorkspaceStart, OperationEffectMutation, OperationIdempotencyReplayable},
		{OperationWorkspaceInspect, OperationEffectRead, OperationIdempotencyReadOnly},
		{OperationWorkspaceWait, OperationEffectRead, OperationIdempotencyReadOnly},
		{OperationWorkspaceStop, OperationEffectMutation, OperationIdempotencyReplayable},
		{OperationWorkspaceHalt, OperationEffectDestructive, OperationIdempotencyReplayable},
		{OperationWorkspaceKill, OperationEffectDestructive, OperationIdempotencyReplayable},
		{OperationWorkspaceQuarantine, OperationEffectDestructive, OperationIdempotencyReplayable},
		{OperationWorkspaceDelete, OperationEffectDestructive, OperationIdempotencyReplayable},
		{OperationWorkspaceList, OperationEffectRead, OperationIdempotencyReadOnly},
		{OperationWorkspaceClone, OperationEffectMutation, OperationIdempotencyKeyedReplay},
	}
	for _, test := range tests {
		operation, ok := OperationContractByID(test.id)
		if !ok {
			t.Fatalf("missing operation %q", test.id)
		}
		if operation.Effect != test.effect || operation.Idempotency != test.idempotency {
			t.Errorf("%s policy = %s/%s, want %s/%s", test.id, operation.Effect, operation.Idempotency, test.effect, test.idempotency)
		}
		if test.effect == OperationEffectRead {
			if len(operation.SideEffects) != 0 {
				t.Errorf("%s read policy has side effects %#v", test.id, operation.SideEffects)
			}
		} else if len(operation.SideEffects) != 2 {
			t.Errorf("%s mutation side effects = %#v", test.id, operation.SideEffects)
		}
	}
}

func TestWorkspaceOperationsDeclareIndependentPolicies(t *testing.T) {
	tests := []struct {
		id          OperationID
		effect      OperationEffect
		idempotency OperationIdempotency
	}{
		{OperationWorkspaceDispatch, OperationEffectMutation, OperationIdempotencyKeyedReplay},
		{OperationWorkspaceExec, OperationEffectMutation, OperationIdempotencyKeyedReplay},
		{OperationWorkspaceConsole, OperationEffectMutation, OperationIdempotencyNotIdempotent},
		{OperationWorkspaceResult, OperationEffectRead, OperationIdempotencyReadOnly},
		{OperationWorkspaceLogs, OperationEffectRead, OperationIdempotencyReadOnly},
		{OperationWorkspaceEvents, OperationEffectRead, OperationIdempotencyReadOnly},
		{OperationWorkspaceStats, OperationEffectRead, OperationIdempotencyReadOnly},
		{OperationWorkspaceEgress, OperationEffectRead, OperationIdempotencyReadOnly},
		{OperationNetworkInspect, OperationEffectRead, OperationIdempotencyReadOnly},
		{OperationWorkspaceApply, OperationEffectMutation, OperationIdempotencyKeyedReplay},
		{OperationWorkspaceCost, OperationEffectRead, OperationIdempotencyReadOnly},
	}
	for _, test := range tests {
		operation, ok := OperationContractByID(test.id)
		if !ok {
			t.Fatalf("missing operation %q", test.id)
		}
		if operation.Effect != test.effect || operation.Idempotency != test.idempotency {
			t.Errorf("%s policy = %s/%s, want %s/%s", test.id, operation.Effect, operation.Idempotency, test.effect, test.idempotency)
		}
	}
}

func TestResourceOperationsDeclareIndependentPolicies(t *testing.T) {
	reads := []OperationID{
		OperationVolumeList, OperationVolumeInspect, OperationImageList,
		OperationModelList, OperationModelRunners, OperationModelPolicyCheck,
		OperationModelPolicyEval,
	}
	for _, id := range reads {
		operation, ok := OperationContractByID(id)
		if !ok {
			t.Fatalf("missing operation %q", id)
		}
		if operation.Effect != OperationEffectRead || operation.Idempotency != OperationIdempotencyReadOnly {
			t.Errorf("%s policy = %s/%s, want read/read_only", id, operation.Effect, operation.Idempotency)
		}
	}
	replayable := []OperationID{
		OperationVolumeCreate, OperationVolumeDelete, OperationImagePull,
		OperationImagePush, OperationImageTag, OperationImageDelete,
		OperationImagePrune, OperationModelPull,
	}
	for _, id := range replayable {
		operation, ok := OperationContractByID(id)
		if !ok {
			t.Fatalf("missing operation %q", id)
		}
		if operation.Effect == OperationEffectRead || operation.Idempotency != OperationIdempotencyReplayable {
			t.Errorf("%s policy = %s/%s, want mutation/replayable", id, operation.Effect, operation.Idempotency)
		}
	}
	keyed := []OperationID{
		OperationModelRemove, OperationModelPrune, OperationModelServe, OperationModelStop,
	}
	for _, id := range keyed {
		operation, ok := OperationContractByID(id)
		if !ok {
			t.Fatalf("missing operation %q", id)
		}
		if operation.Effect == OperationEffectRead || operation.Idempotency != OperationIdempotencyKeyedReplay {
			t.Errorf("%s policy = %s/%s, want mutation/keyed_replay", id, operation.Effect, operation.Idempotency)
		}
	}
}

func TestHostOperationsDeclareIndependentPolicies(t *testing.T) {
	reads := []OperationID{
		OperationKernelVerify, OperationKernelList, OperationKernelCheck,
		OperationSecretCheck, OperationSecretAudit, OperationPerfFootprint,
		OperationHostInspect, OperationDoctorCheck, OperationProfilesList,
		OperationContractGet, OperationDescribe,
	}
	for _, id := range reads {
		operation, ok := OperationContractByID(id)
		if !ok {
			t.Fatalf("missing operation %q", id)
		}
		if operation.Effect != OperationEffectRead || operation.Idempotency != OperationIdempotencyReadOnly {
			t.Errorf("%s policy = %s/%s, want read/read_only", id, operation.Effect, operation.Idempotency)
		}
	}
	for _, id := range []OperationID{OperationKernelInstall, OperationRootfsBuild} {
		operation, ok := OperationContractByID(id)
		if !ok {
			t.Fatalf("missing operation %q", id)
		}
		if operation.Effect != OperationEffectMutation ||
			operation.Idempotency != OperationIdempotencyKeyedReplay ||
			operation.Confirmation != OperationConfirmationPreview {
			t.Errorf("%s policy = %#v", id, operation)
		}
	}
}

func TestSnapshotOperationsDeclareNarrowCapabilities(t *testing.T) {
	tests := []struct {
		command    string
		id         OperationID
		capability FeatureCapability
	}{
		{"pause", "workspace.pause", FeatureCapabilityPauseResume},
		{"resume", "workspace.resume", FeatureCapabilityPauseResume},
		{"snapshot", "snapshot.create", FeatureCapabilitySnapshotCreate},
		{"start --from-snapshot", "snapshot.restore", FeatureCapabilitySnapshotRestore},
		{"create --from-snapshot", "snapshot.fork", FeatureCapabilitySnapshotFork},
	}
	for _, test := range tests {
		operation, ok := OperationForCLICommand(test.command)
		if !ok {
			t.Fatalf("missing operation for %q", test.command)
		}
		if operation.ID != test.id || len(operation.RequiredCapabilities) != 1 || operation.RequiredCapabilities[0] != test.capability {
			t.Errorf("%q operation = %#v, want %s requiring %s", test.command, operation, test.id, test.capability)
		}
	}
}

func TestSnapshotOperationsDeclareIndependentPolicies(t *testing.T) {
	tests := []struct {
		id          OperationID
		effect      OperationEffect
		idempotency OperationIdempotency
	}{
		{OperationWorkspacePause, OperationEffectMutation, OperationIdempotencyReplayable},
		{OperationWorkspaceResume, OperationEffectMutation, OperationIdempotencyReplayable},
		{OperationSnapshotCreate, OperationEffectMutation, OperationIdempotencyKeyedReplay},
		{OperationSnapshotRestore, OperationEffectMutation, OperationIdempotencyNotIdempotent},
		{OperationSnapshotFork, OperationEffectMutation, OperationIdempotencyNotIdempotent},
		{OperationSnapshotList, OperationEffectRead, OperationIdempotencyReadOnly},
		{OperationSnapshotDelete, OperationEffectDestructive, OperationIdempotencyReplayable},
	}
	for _, test := range tests {
		operation, ok := OperationContractByID(test.id)
		if !ok {
			t.Fatalf("missing operation %q", test.id)
		}
		if operation.Effect != test.effect || operation.Idempotency != test.idempotency {
			t.Errorf("%s policy = %s/%s, want %s/%s", test.id, operation.Effect, operation.Idempotency, test.effect, test.idempotency)
		}
		if test.effect == OperationEffectRead {
			if len(operation.SideEffects) != 0 {
				t.Errorf("%s read policy has side effects %#v", test.id, operation.SideEffects)
			}
		} else if len(operation.SideEffects) != 2 {
			t.Errorf("%s mutation side effects = %#v", test.id, operation.SideEffects)
		}
	}
}

func TestExecAndConsoleDeclareIndependentCapabilities(t *testing.T) {
	tests := []struct {
		command    string
		id         OperationID
		featureID  string
		capability FeatureCapability
	}{
		{"exec", OperationWorkspaceExec, "workspace.exec", FeatureCapabilityStructuredExec},
		{"connect", OperationWorkspaceConsole, "workspace.console", FeatureCapabilityConsole},
	}
	for _, test := range tests {
		operation, ok := OperationForCLICommand(test.command)
		if !ok {
			t.Fatalf("missing operation for %q", test.command)
		}
		if operation.ID != test.id || operation.FeatureID != test.featureID ||
			len(operation.RequiredCapabilities) != 1 || operation.RequiredCapabilities[0] != test.capability {
			t.Errorf("%q operation = %#v, want %s owned by %s requiring %s", test.command, operation, test.id, test.featureID, test.capability)
		}
	}
}

func TestFileOperationsDeclareOfflineAndLiveCapabilities(t *testing.T) {
	tests := []struct {
		id         OperationID
		capability FeatureCapability
	}{
		{OperationFileCopyOffline, FeatureCapabilityOfflineFileCopy},
		{OperationFileCopyLive, FeatureCapabilityLiveFileCopy},
		{OperationArtifactRead, FeatureCapabilityOfflineFileCopy},
		{OperationArtifactList, FeatureCapabilityOfflineFileCopy},
		{OperationArtifactGet, FeatureCapabilityOfflineFileCopy},
		{OperationWorkspaceCommit, FeatureCapabilityOfflineFileCopy},
	}
	for _, test := range tests {
		operation, ok := OperationContractByID(test.id)
		if !ok {
			t.Fatalf("missing operation %q", test.id)
		}
		if len(operation.RequiredCapabilities) != 1 || operation.RequiredCapabilities[0] != test.capability {
			t.Errorf("%s capabilities = %#v, want %s", test.id, operation.RequiredCapabilities, test.capability)
		}
	}

	for _, backend := range []string{BackendLinuxKVM, BackendAppleVF} {
		offline, _ := OperationContractByID(OperationFileCopyOffline)
		if ready, reason := BackendSupportsOperation(backend, offline); !ready {
			t.Errorf("%s offline file copy not ready: %s", backend, reason)
		}
		live, _ := OperationContractByID(OperationFileCopyLive)
		if ready, _ := BackendSupportsOperation(backend, live); ready {
			t.Errorf("%s live file copy unexpectedly ready", backend)
		}
		gap := NewUnsupportedOperationError(backend, live, "live file copy")
		if gap.Capability != FeatureCapabilityLiveFileCopy || gap.GapID == "" {
			t.Errorf("%s live file copy gap = %#v", backend, gap)
		}
	}
}

func TestFileOperationsDeclareIndependentPolicies(t *testing.T) {
	tests := []struct {
		id          OperationID
		effect      OperationEffect
		idempotency OperationIdempotency
	}{
		{OperationFileCopyOffline, OperationEffectMutation, OperationIdempotencyKeyedReplay},
		{OperationArtifactList, OperationEffectRead, OperationIdempotencyReadOnly},
		{OperationArtifactGet, OperationEffectMutation, OperationIdempotencyKeyedReplay},
		{OperationWorkspaceCommit, OperationEffectMutation, OperationIdempotencyKeyedReplay},
	}
	for _, test := range tests {
		operation, ok := OperationContractByID(test.id)
		if !ok {
			t.Fatalf("missing operation %q", test.id)
		}
		if operation.Effect != test.effect || operation.Idempotency != test.idempotency {
			t.Errorf("%s policy = %s/%s, want %s/%s", test.id, operation.Effect, operation.Idempotency, test.effect, test.idempotency)
		}
		if test.effect == OperationEffectRead {
			if len(operation.SideEffects) != 0 {
				t.Errorf("%s read policy has side effects %#v", test.id, operation.SideEffects)
			}
		} else if len(operation.SideEffects) != 2 {
			t.Errorf("%s mutation side effects = %#v", test.id, operation.SideEffects)
		}
	}
}

func TestNetworkOperationsDeclarePublishAndLiveApplyCapabilities(t *testing.T) {
	tests := []struct {
		id         OperationID
		capability FeatureCapability
	}{
		{OperationWorkspaceApply, FeatureCapabilityNetworkPublish},
		{OperationNetworkPublish, FeatureCapabilityNetworkPublish},
		{OperationNetworkApplyLive, FeatureCapabilityLiveNetworkApply},
	}
	for _, test := range tests {
		operation, ok := OperationContractByID(test.id)
		if !ok {
			t.Fatalf("missing operation %q", test.id)
		}
		if len(operation.RequiredCapabilities) != 1 || operation.RequiredCapabilities[0] != test.capability {
			t.Errorf("%s capabilities = %#v, want %s", test.id, operation.RequiredCapabilities, test.capability)
		}
		for _, backend := range []string{BackendLinuxKVM, BackendAppleVF} {
			if ready, reason := BackendSupportsOperation(backend, operation); !ready {
				t.Errorf("%s %s not ready: %s", backend, test.id, reason)
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
	wantCapabilities := []FeatureCapability{
		FeatureCapabilityPauseResume,
		FeatureCapabilitySnapshotCreate,
		FeatureCapabilitySnapshotRestore,
		FeatureCapabilitySnapshotFork,
	}
	if len(feature.RequiredCapabilities) != len(wantCapabilities) {
		t.Fatalf("snapshot required capabilities = %#v, want %#v", feature.RequiredCapabilities, wantCapabilities)
	}
	for i, want := range wantCapabilities {
		if feature.RequiredCapabilities[i] != want {
			t.Fatalf("snapshot required capability %d = %q, want %q", i, feature.RequiredCapabilities[i], want)
		}
	}
	assertFeatureSupport(t, feature, BackendLinuxKVM, true)
	assertFeatureSupport(t, feature, BackendAppleVF, true)
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

func TestSnapshotOperationErrorsNameTheirCapability(t *testing.T) {
	feature, ok := FeatureForCLICommand("snapshot")
	if !ok {
		t.Fatal("snapshot CLI command is not mapped to a feature contract")
	}
	for _, test := range []struct {
		operation  string
		capability FeatureCapability
	}{
		{"pause", FeatureCapabilityPauseResume},
		{"snapshot create", FeatureCapabilitySnapshotCreate},
		{"snapshot restore", FeatureCapabilitySnapshotRestore},
		{"snapshot fork", FeatureCapabilitySnapshotFork},
	} {
		err := NewUnsupportedFeatureCapabilityError("unsupported", feature, test.operation, test.capability)
		if err.Capability != test.capability {
			t.Errorf("%s capability = %q, want %q", test.operation, err.Capability, test.capability)
		}
		if !strings.Contains(err.Reason, string(test.capability)) {
			t.Errorf("%s reason = %q, want capability name", test.operation, err.Reason)
		}
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
	for _, backend := range []string{BackendAppleVF} {
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

// TestSnapshotFeatureCarriesNoScopedGaps: the snapshot capability — including
// forensic capture, whose apple-vf gap closed when the Apple VF supervisor
// learned to retain guest secrets — is supported on linux-kvm and apple-vf
// with no scoped gaps. The contract command description names the
// capture-before-contain ordering so consumers do not expect to snapshot a
// contained workspace.
func TestSnapshotFeatureCarriesNoScopedGaps(t *testing.T) {
	feature, ok := FeatureForCLICommand("snapshot")
	if !ok || feature.ID != "workspace.snapshot" {
		t.Fatalf("snapshot is not mapped to workspace.snapshot (ok=%v id=%q)", ok, feature.ID)
	}
	assertFeatureSupport(t, feature, BackendLinuxKVM, true)
	assertFeatureSupport(t, feature, BackendAppleVF, true)
	if len(feature.Gaps) != 0 {
		t.Fatalf("workspace.snapshot must carry no scoped gaps (forensic capture is supported on both backends), got %#v", feature.Gaps)
	}
	// The runtime contract must not advertise capturing a contained workspace —
	// quarantine stops the runtime, so that capture is unreachable. It must
	// instead tell consumers to capture BEFORE containing.
	var desc string
	for _, cmd := range NewRuntimeContract().Commands {
		if cmd.Name == "snapshot" {
			desc = cmd.Description
		}
	}
	if !strings.Contains(desc, "running or paused") || !strings.Contains(desc, "BEFORE containing") {
		t.Fatalf("snapshot description must require a live VM and name capture-before-contain: %q", desc)
	}
}
