---
title: Go library
description: Use microagent packages directly from Go.
---

<!-- docs-last-updated -->
_Last updated: 2026-08-03_

*New to the library? Start with the [library overview](/library/) or the
[smallest useful Go program](/getting-started/library/first-program/). This
page is the package reference.*

## The common pattern

Most programs want exactly this: boot a microVM from an OCI image, run a
command inside it, read the output. `workspace.Run` does all of it in one
call:

```go
package main

import (
	"context"
	"fmt"

	"github.com/geoffbelknap/microagent/pkg/workspace"
)

func main() {
	opts := workspace.DefaultOptions()
	opts.Name = "demo-vm"
	opts.ImageRef = "docker.io/library/ubuntu:24.04"
	opts.ExecCommand = "uname -a"

	result, err := workspace.Run(context.Background(), opts)
	if err != nil {
		panic(err)
	}
	if result.Result != nil {
		fmt.Print(result.Result.Stdout)
	}
}
```

`DefaultOptions` picks the host backend, kernel, and state directory; you set
only what your program cares about. The rest of this page covers the packages
behind this call and the lower-level APIs around it.

## Use it as a generic microVM toolkit

The library doesn't require agent semantics. The same packages back the CLI's
agent-flavored workflows and any program that just wants microVMs: build a
rootfs from an OCI image, boot a VM, run a command, tear it down. The
high-level [`pkg/workspace`](#workspace-api) API treats every workspace as a
`workload`-role identity by default. You get caller-visible identity for
free without writing agent-aware code, and you can drop down to
[`pkg/vmkit`](#supervisor-types) when you need a different role or a custom
supervisor request.

## Exported packages

These are the core packages — the ones most embedding programs import first:

| Package | Purpose |
|---|---|
| `pkg/vmkit` | supervisor request/response types, validation, and executable supervisor client |
| `pkg/workspace` | workspace lifecycle API, options, defaults, request construction, backend supervisor selection, and shared helpers |
| `pkg/operation` | stable operation error categories shared by library, CLI, and MCP adapters |
| `pkg/kernel` | kernel default manifest, install, verify, update checks, and support checks |
| `pkg/imagecache` | reusable rootfs image cache indexing, pull, tag, remove, and prune |
| `pkg/diagnostics` | backend host diagnostics and support summaries |
| `pkg/perf` | boot, footprint, and steady-state performance measurements |
| `pkg/rootfs` | OCI image and tar bundle conversion into ext4 disks |
| `pkg/supervisors/firecracker` | Linux Firecracker supervisor implementation |

A second tier of supporting packages backs specific CLI workflows. Import them
when the CLI ↔ library mapping table at the bottom of this page points at them:

| Package | Purpose |
|---|---|
| `pkg/workspace/exec/protocol` | the structured exec wire protocol (`ExecRequest`, `ExecResult`, `ExecStatus`); every `workspace.Exec` caller imports this |
| `pkg/workspace/exec/client` | host-side client for the guest structured exec service (used internally by `workspace.Exec`; import it only for custom transports) |
| `pkg/scaffold` | generates a starter agent project (`microagent.yaml` plus supporting files); backs `microagent init` |
| `pkg/commit` | snapshots a stopped workspace's rootfs back into an OCI image and pushes it; backs `microagent commit` / `microagent image push` |
| `pkg/model` | manages local GGUF model files pulled from Hugging Face (pull, list, remove, prune) |
| `pkg/modelrunner` | manages host-local model server processes (llama.cpp, vLLM, or custom commands); `modelrunner.Ensure` starts or reuses a runner |
| `pkg/volume` | user-defined named volumes: VM-independent ext4 disks created, attached, and removed by name |
| `pkg/secret` | resolves scheme-prefixed secret references (`env:`/`file:`/`dotenv:`/`vault:`/`helper:`) to values held host-side only |
| `pkg/registryauth` | OCI registry credential login, logout, and listing without a Docker dependency |

The CLI is an adapter over these packages. Go callers should use the library
directly for workspace lifecycle operations instead of shelling out to
`microagent`.

## Documented API

These exported symbols are part of the documented Go API. Helper functions and
backend plumbing can remain exported for package boundaries without being
promoted here, but caller-facing symbols should be added to this page when they
are introduced.

| Package | Documented symbols |
|---|---|
| `pkg/vmkit` | `Request`, `Response`, `Config`, `Identity`, `Disk`, `NetworkConfig`, `PortForward`, `MediationConfig`, `VsockListener`, `RuntimeArtifacts`, `ArtifactRef`, `RuntimeResult`, `Event`, `VMState`, `StateUnknown`, `RoleWorkload`, `Supervisor`, `SupervisorClient`, `ExecutableSupervisor`, `NewRuntimeContract`, `FeatureContract`, `OperationContract`, `OperationID`, `OperationEffect`, `OperationIdempotency`, `OperationConfirmation`, `OperationSideEffect`, `OperationTypeID`, `OperationEffectRead`, `OperationEffectMutation`, `OperationEffectDestructive`, `OperationIdempotencyReadOnly`, `OperationIdempotencyReplayable`, `OperationIdempotencyKeyedReplay`, `OperationIdempotencyNotIdempotent`, `OperationConfirmationPreview`, `OperationSideEffectHostState`, `OperationSideEffectWorkspaceState`, `OperationWorkspaceDispatch`, `OperationWorkspaceExec`, `OperationWorkspaceConsole`, `OperationWorkspaceResult`, `OperationWorkspaceLogs`, `OperationWorkspaceEvents`, `OperationWorkspaceStats`, `OperationWorkspaceEgress`, `OperationWorkspaceCost`, `OperationWorkspaceObserve`, `OperationWorkspaceCreate`, `OperationWorkspaceStart`, `OperationWorkspaceInspect`, `OperationWorkspaceWait`, `OperationWorkspaceStop`, `OperationWorkspaceHalt`, `OperationWorkspaceKill`, `OperationWorkspaceQuarantine`, `OperationWorkspaceDelete`, `OperationWorkspaceList`, `OperationWorkspaceClone`, `OperationFileCopyOffline`, `OperationFileCopyLive`, `OperationArtifactList`, `OperationArtifactGet`, `OperationArtifactRead`, `OperationWorkspaceCommit`, `OperationWorkspaceApply`, `OperationNetworkPublish`, `OperationNetworkApplyLive`, `OperationNetworkInspect`, `OperationWorkspacePause`, `OperationWorkspaceResume`, `OperationSnapshotCreate`, `OperationSnapshotRestore`, `OperationSnapshotFork`, `OperationSnapshotList`, `OperationSnapshotDelete`, `OperationSnapshotCatalog`, `OperationVolumeCreate`, `OperationVolumeList`, `OperationVolumeInspect`, `OperationVolumeDelete`, `OperationVolumeResize`, `OperationWorkspaceResize`, `FeatureCapabilityResize`, `OperationImagePull`, `OperationImageList`, `OperationImagePush`, `OperationImageTag`, `OperationImageDelete`, `OperationImagePrune`, `OperationModelPull`, `OperationModelList`, `OperationModelRemove`, `OperationModelPrune`, `OperationModelServe`, `OperationModelStop`, `OperationModelRunners`, `OperationModelPolicyCheck`, `OperationModelPolicyEval`, `OperationKernelInstall`, `OperationKernelVerify`, `OperationKernelList`, `OperationKernelCheck`, `OperationRootfsBuild`, `OperationHostInspect`, `OperationDoctorCheck`, `OperationProfilesList`, `OperationContractGet`, `OperationDescribe`, `OperationProjectInit`, `OperationSecretCheck`, `OperationSecretAudit`, `OperationPerfBoot`, `OperationPerfFootprint`, `OperationPerfSteady`, `OperationSupervise`, `OperationBrokerConfigure`, `OperationRegistryLogin`, `OperationRegistryLogout`, `OperationRegistryList`, `OperationServeMCP`, `OperationHostGC`, `OperationPing`, `FeatureScope`, `FeatureBackendNeutral`, `FeatureCapability`, `FeatureCapabilityStructuredExec`, `FeatureCapabilityNetworkPublish`, `FeatureCapabilityOfflineFileCopy`, `FeatureCapabilityLiveFileCopy`, `FeatureCapabilityEgressMediation`, `CapabilityTier`, `CapabilityTierCore`, `CapabilityTierSafety`, `CapabilityTierFeature`, `CapabilityTierOf`, `VerdictOK`, `VerdictDegraded`, `VerdictFailed`, `FeatureBackend`, `FeatureGap`, `UnsupportedFeatureError`, `FeatureContracts`, `OperationContracts`, `FeatureBackendSupport`, `BackendSupportsFeature`, `BackendSupportsOperation`, `FeatureForCLICommand`, `FeatureForMCPTool`, `OperationForCLICommand`, `OperationForMCPTool`, `OperationContractByID`, `NewUnsupportedFeatureError`, `NewUnsupportedFeatureCapabilityError`, `NewUnsupportedOperationError`, `ContractDurability`, `ContractDurabilityTier`, `ContractDurabilityTransition`, `DurabilityTier`, `DurabilityRuntime`, `DurabilityWorkspace`, `DurabilitySnapshot`, `DurabilityIndependent`, `DurabilityEffect`, `DurabilityPreserved`, `DurabilityDiscarded`, `DurabilityCaptured`, `DurabilityRestored`, `DurabilityCopied`, `DurabilityReset`, `DurabilityRemoved`, `DurabilityNotGuaranteed`, `DurabilityContract`, `ContractPersistence`, `ContractPersistenceTier`, `PersistedArtifact`, `PersistenceTier`, `PersistenceRecoverable`, `PersistenceOperational`, `PersistenceAudit`, `PersistenceEvidence`, `PersistenceContract`, `IsKnownBackend`, `Capabilities`, `BackendCapabilities`, `EgressDatapathBinEnv`, `ResolveEgressDatapathBin`, `SnapshotManifest`, `SnapshotArtifact`, `SnapshotInfo`, `SnapshotManifestName`, `SnapshotVMStateName`, `SnapshotMemoryName`, `SnapshotRootfsName`, `SnapshotAppleVFMachineState`, `SnapshotAppleVFConfig`, `SnapshotsDir`, `SnapshotDir`, `SnapshotStagingParent`, `PublishSnapshotDir`, `SnapshotRootfsArtifact`, `SnapshotMachineStateArtifacts`, `FirecrackerSnapshotArtifacts`, `AppleVFSnapshotArtifacts`, `WriteSnapshotManifest`, `ReadSnapshotManifest`, `ListSnapshots`, `RemoveSnapshot`, `MaterializedSecretsDeclared`, `ValidateSnapshotSecretCapture`, `ValidateSnapshotSecretRestore`, `EgressModeBroker`, `EgressModeMITM`, `EgressModeOff`, `EgressMediationOn`, `EgressModeForgesCerts`, `NetworkModeMediates`, `ValidateEgressMode`, `ResolveEgressModeDefault`, `EgressPolicy`, `EgressCaps`, `NormalizeEgressPolicy`, `ReadinessSignal`, `RuntimeReadiness`, `MediationReadinessSignal`, `BrokerConfig`, `BrokerListenerTarget`, `ValidateBackendVsockListeners`, `GuestBootParam`, `GuestBootParams`, `AppleVFUndecodedConfigFields` |
| `pkg/workspace` | `Options`, `OptionsFromRequest`, `EgressPolicyFromOptions`, `DefaultOptions`, `Spec`, `SpecApplyOptions`, `ApplyResult`, `Manifest`, `CapabilityComposition`, `EvaluateCapabilityComposition`, `ModelRunnerSpec`, `ModelMediationSpec`, `Result`, `GuestResult`, `CopyResult`, `ListEntry`, `SuperviseOptions`, `SuperviseResult`, `ConsoleOptions`, `ShellTarget`, `ConsoleReadTimeoutError`, `ConsoleCompletionUnknownError`, `ShellReadinessProbeMode`, `ShellReadinessProbeTCP`, `ShellReadinessSignalWithMode`, `DefaultModelGuestPort`, `ExecReadyProbeTimeout`, `ExecReadyWait`, `ExecMaxTransientRetries`, `CleanStopSyncTimeout`, `ExecPort`, `ExecPortForName`, `ExecRetryExhaustedError`, `ExecRetryMetadata`, `ExecReadinessSignal`, `Create`, `CreateFromSnapshot`, `Run`, `RandomName`, `Start`, `Inspect`, `Status`, `Wait`, `WaitOptions`, `WaitResult`, `WaitTimeoutError`, `WaitStateOK`, `IsWaitTerminalState`, `ResultStatus`, `ArtifactsFor`, `GetArtifact`, `Copy`, `GuestRootfsLayerTar`, `Clone`, `ReadLogs`, `ReadSupervisorLogs`, `ReadEvents`, `EventsPath`, `ReadEgressAudit`, `EgressAuditPath`, `ReadBrokerAccess`, `BrokerAccessPath`, `MergeEgressEvents`, `EgressEvent`, `AuditIntegrityError`, `EgressAuditSummary`, `SummarizeEgressAudit`, `RunDispatch`, `DispatchResult`, `SampleStats`, `Stats`, `Network`, `List`, `Control`, `Pause`, `Resume`, `DefaultSnapshotTag`, `DefaultForensicSnapshotTag`, `Snapshot`, `SnapshotForensic`, `SnapshotList`, `SnapshotRemove`, `Apply`, `Exec`, `ExecWithMetadata`, `ExecStream`, `MarkActivity`, `IsRetryableExecTransient`, `DialConsole`, `SendConsoleCommand`, `ConsoleTarget`, `ProbeShellCommand`, `Supervise`, `ReadSpec`, `ApplySpec`, `ApplySpecFile`, `ReadManifest`, `WriteManifest`, `ProfileNames`, `LookupProfile`, `NormalizeArch`, `ValidateArch`, `WorkspaceNotFoundError`, `Supervisor`, `FirecrackerSupervisorPathFromExecutable`, `LookupE2fsprogsTool`, `Mke2fsPath`, `Resize2fsPath`, `Resize`, `ResizeOptions`, `ResizeResult`, `ShellReadinessProbeCommand`, `ParseBrokerConfig`, `ParseBrokerEndpoints` |
| `pkg/kernel` | `InstallOptions`, `InstallResult`, `Install`, `VerifyOptions`, `VerifyResult`, `Verify`, `DefaultSource`, `Support`, `CheckUpdate` |
| `pkg/imagecache` | `PullOptions`, `Record`, `PruneResult`, `Pull`, `Find`, `List`, `Tag`, `Remove`, `Prune`, `ReadIndex`, `FromProvenance` |
| `pkg/diagnostics` | `Options`, `Check`, `DeriveVerdict`, `EgressTProxyRemediation` |
| `pkg/perf` | `BootOptions`, `BootReport`, `FootprintReport`, `SteadyReport`, `Iteration`, `Summary`, `RSSSample`, `RSSSummary`, `Boot`, `Footprint`, `Steady`, `ProcessRSSKiB`, `ParseRSSKiB`, `SampleProcessRSS`, `SummarizeIterations`, `SummarizeRSSSamples` |
| `pkg/rootfs` | `BuildRequest`, `BundleRequest`, `BundleProvenance`, `Builder`, `NewBuilder`, `NormalizeRequest`, `NormalizeBundleRequest`, `Platform`, `Provenance` |
| `pkg/supervisors/firecracker` | `Supervisor` |

## Supervisor types

Use `pkg/vmkit` when you need the shared request/response schema or want to
call an executable supervisor.

```go
package main

import (
	"context"
	"fmt"

	"github.com/geoffbelknap/microagent/pkg/vmkit"
)

func main() {
	resp, err := vmkit.ExecutableSupervisor{
		Path: "microagent-firecracker-supervisor",
	}.Do(context.Background(), vmkit.Request{Command: "host"})
	if err != nil {
		panic(err)
	}
	fmt.Println(resp.Backend, resp.OK)
}
```

On Linux, Go callers can also use
`github.com/geoffbelknap/microagent/pkg/supervisors/firecracker` directly. The
package name is `firecracker`; alias it if you like. The `req` below is any
`vmkit.Request` you construct:

```go
import (
	firecrackersupervisor "github.com/geoffbelknap/microagent/pkg/supervisors/firecracker"
)

resp, err := firecrackersupervisor.Supervisor{}.Do(ctx, req)
```

`firecracker.ResolveBinaryFrom(anchor)` is the one place the Firecracker VMM
binary is located: `MICROAGENT_FIRECRACKER`, then `PATH`, then
`../libexec/firecracker` relative to `anchor` (normally the supervisor
executable). The supervisor's boot path and `doctor`'s probe both call it, so
a diagnostic verdict and an actual boot can never resolve different binaries.
Pass the supervisor's path as the anchor to see exactly what a boot would
use.

For backend-independent code, depend on the interface:

```go
func inspect(ctx context.Context, supervisor vmkit.Supervisor, req vmkit.Request) (vmkit.Response, error) {
	req.Command = "inspect"
	return supervisor.Do(ctx, req)
}
```

`vmkit.NewRuntimeContract()` exposes high-level feature records, lifecycle
durability, the persisted-artifact catalog, and the typed operation registry
shared by the CLI and MCP adapters. `vmkit.PersistenceContract()` classifies
every microagent-owned file family as recoverable, operational, audit, or
evidence and declares its permissions, writer, cleanup owner, integrity,
retention, and recovery behavior. Use
`vmkit.OperationContracts()` for stable operation identities, adapter aliases,
operation-level capability requirements, and canonical request/result type IDs.
Library code should resolve stable IDs with `vmkit.OperationContractByID()`;
adapter coverage can use
`vmkit.OperationForCLICommand()` and `vmkit.OperationForMCPTool()`. If a
library path rejects an operation, return `vmkit.NewUnsupportedOperationError()`
so CLI and MCP callers see the same structured error shape.

## Rootfs builder

Use `pkg/rootfs` when your program needs to build a VM rootfs from an OCI image
without shelling out to `microagent rootfs`.

```go
package main

import (
	"context"

	"github.com/geoffbelknap/microagent/pkg/rootfs"
)

func main() {
	_, err := rootfs.NewBuilder().Build(context.Background(), rootfs.BuildRequest{
		ImageRef:   "docker.io/library/ubuntu:24.04",
		Platform:   rootfs.Platform{OS: "linux", Architecture: "amd64"},
		OutputPath: "/tmp/rootfs.ext4",
		StateDir:   "/tmp/microagent-build",
		SizeMiB:    2048,
	})
	if err != nil {
		panic(err)
	}
}
```

`rootfs.ValidateImageRef(ref)` checks that a reference parses — the same
normalization and parse the builder runs first, with no network or filesystem
access. Call it to reject a doomed configuration before spending anything on
it. `workspace.Create` and `workspace.Run` run the same check ahead of their
dry-run returns, so a dry run and a real run refuse the same references with
the same error.

Set `BuildRequest.BaseCacheDir` to reuse extracted base image content across
builds; `rootfs.BaseCacheDirFor(stateDir)` derives the standard location the
CLI uses (honoring its environment override). The builder resolves the
manifest digest from the source on every build and consults the cache by
that digest, so a cached build and a fresh build of the same digest are
byte-identical inputs. The returned `Provenance.BaseSource` says which
happened (`rootfs.BaseSourceRegistry`, `rootfs.BaseSourceLocalLayout`, or
`rootfs.BaseSourceCache`). `rootfs.ClearBaseCache(dir, selector)` removes
entries (each reported as a `rootfs.BaseCacheEntry`) — the image cache's
delete/prune purge path is its main caller.

## Workspace API

Use `pkg/workspace` when your program wants to create, run, start, inspect, and
control named workspaces without parsing CLI flags.

`workspace.DefaultOptions()` picks the host backend (Firecracker on Linux or
Apple Virtualization.framework on macOS),
guest architecture, default kernel path, and default state directory. You
override only what your program needs. The
[common pattern](#the-common-pattern) example at the top of this page shows
the canonical `DefaultOptions` + `Run` call.

Leave `Options.Name` empty and `Run` mints a readable, collision-safe name via
`RandomName` (for example `run-brave-otter-4f9c`); `RunDispatch` does the same
with a `dispatch-` prefix. `RandomName(prefix)` is exported for programs that
want the same naming for workspaces they create themselves.

`Result.Result` is a `*GuestResult` with `Stdout`, `Stderr`, and `ExitCode`
captured from the guest. `Result.Response` carries the supervisor's structured
response (state, identity, verification).

Status responses expose declared egress coverage through
`Response.EgressCapture`. When the backend can observe its mediator process,
`EgressCapture.Live` is non-nil and reports current liveness; nil means the
backend did not make a liveness observation. Quiescent workspace states also
recompute and enforce the recorded rootfs hash, while running workspaces
measure it without treating expected guest writes as divergence.
`EgressCapture.EncryptedDNS` is an `EgressEncryptedDNSCoverage` value. It is
`EgressEncryptedDNSDeniedHTTP1` for `mitm`, where recognizable HTTP/1 DNS
requests are denied, and `EgressEncryptedDNSOpaque` for `broker`, where TLS
request content is not observable.

### Run lifecycle contract

`workspace.Run` has three behaviors worth knowing before you ship:

- **The run phase is bounded by `Options.Timeout`.** `DefaultOptions` sets it
  to `workspace.DefaultTimeout` (5 minutes). The rootfs build and image pull
  run first, under your own `ctx`; `Run` then wraps `Options.Timeout` around
  the supervisor run — the boot and your guest command. A
  guest command that legitimately needs more than 5 minutes (a compile, a
  large in-guest download) is killed at the deadline. Raise `Options.Timeout`
  for long-running commands; the tighter of it and the parent `ctx` deadline
  wins.
- **Cleanup happens only on success.** When the run completes and the
  supervisor reports OK, `Run` removes the scratch state — unless
  `Options.Keep` is set. A run that *fails* (boot error, supervisor error,
  timeout) deliberately leaves its state directory under
  `<StateDir>/workspaces/<name>/` for debugging. Remove it explicitly with
  `workspace.Control(ctx, opts, "delete")`, or pick a fresh name for the next
  attempt. `run` is one-shot and discards by default; `Create`+`Start` are the
  durable path where `delete` is always the explicit removal.
- **A command is required.** `Run` returns an error when `Options.ExecCommand`
  is empty, unless `Options.UseImageCommand` is set to run the OCI image's own
  entrypoint/command instead. With an empty `Options.Name`, `Run` generates a
  unique `run-<timestamp>` name; with an empty `Options.ImageRef` it falls back
  to the default per-architecture Python image.

### Options field reference

`workspace.Options` is a plain value struct — copy it freely; each call gets
its own copy. The exported fields, grouped by concern:

**Workspace identity and image**

| Field | Meaning |
|---|---|
| `Name` | workspace name; becomes the `RuntimeID` in requests, state paths, and events |
| `ImageRef` | OCI image reference the rootfs is built from |
| `Hostname` | guest hostname |
| `Architecture` | guest architecture (`amd64`/`arm64`) |
| `Backend` | backend id; must match the host backend (`ValidateHostBackend`) |
| `Profile` / `ProfileExplicit` | named resource profile (`tiny`/`small`/`medium`/`large`) and whether it was set explicitly |

**Command and lifecycle**

| Field | Meaning |
|---|---|
| `ExecCommand` | one-shot command `Run` executes in the guest |
| `UseImageCommand` | run the image's own entrypoint/command instead of `ExecCommand` |
| `ServiceCommand` | long-running service command for durable workspaces |
| `Entrypoint` | entrypoint override |
| `SetupCommands` | commands run once during rootfs preparation |
| `ConsoleShell` | absolute guest path of the interactive console shell |
| `RestartPolicy` | restart policy for `Supervise` (default `never`) |
| `Health` | health-check declaration |
| `Timeout` | run deadline (default 5 minutes; see the lifecycle contract above) |
| `LeaseSeconds` | idle-TTL lease renewed by real activity (`MarkActivity`) |
| `Keep` | retain scratch state after a successful `Run` |
| `SerialLogMaxBytes` | bytes of console log inlined in `Result.SerialLog` as a tail (0 = `workspace.DefaultSerialLogMaxBytes`, 8192; negative = full log). `Result.SerialLogBytes`/`SerialLogTruncated` report the full size and whether the inline copy is an excerpt; the full log stays at `Result.SerialPath` while the workspace is kept |
| `DryRun` | validate and prepare without booting |
| `PrepareForStart` | prepare state for a later `Start` |
| `FromSnapshot` | restore in place from this snapshot tag on `Start` |
| `MaintenanceBoot` | boot with only shell/exec channels (no service, no secrets) for file operations |
| `SerialInput` | enable serial console input |

**Resources and network**

| Field | Meaning |
|---|---|
| `MemoryMiB`, `CPUCount`, `SizeMiB` | explicit resource sizing (override the profile) |
| `SpecMemory`, `SpecCPU`, `SpecSize` | mark which resources came from an explicit spec |
| `Network` | `vmkit.NetworkConfig`: mode (`user` default), port forwards, DNS, routes. A forward bound to a concrete IPv4 `Host` preserves that address as the guest application's local address |
| `Mediation` | optional `vmkit.MediationConfig` guest-to-host mediation channel |
| `ResultPort`, `ShellPort`, `ExecPort`, `GuestShellPort`, `GuestExecPort` | vsock/TCP port assignments (defaulted per workspace name) |
| `VsockListeners` | extra host vsock listeners |
| `BakedVsockUDSPath` | source snapshot's baked vsock path when starting from a snapshot |

**Files, disks, and outputs**

| Field | Meaning |
|---|---|
| `Files` | host files copied into the rootfs at build time |
| `Disks` | extra data disks (including named managed volumes) |
| `Outputs` | declared egress artifacts retrievable by name with `GetArtifact` |
| `Env` | guest environment variables |

**Secrets and credentials**

| Field | Meaning |
|---|---|
| `Secrets` | name → scheme-prefixed reference, resolved and delivered at start |
| `SecretEnvFiles` | dotenv file paths re-read on each start |
| `OnDemandSecrets` | lazy references, never materialized in the guest |
| `SecretsAudit` | append every secret access to the audit log |
| `CapabilityRiskAcknowledgement` | operator reason accepting a hazardous complete grant set; persisted and reported, never treated as a validation bypass |
| `CredSwapProviders` | parsed `--cred-swap` specs; provider keys injected host-side, never in the guest |

**Egress and broker**

| Field | Meaning |
|---|---|
| `EgressMode` | `broker` (default), `mitm`, or `off` |
| `EgressAllow` | allowlisted egress destination hosts |
| `EgressPassthrough` | allowed hosts that are not TLS-intercepted |
| `EgressAllowlistLocked` | restrict egress to allowlisted destinations only |
| `EgressSwapConfigPath` | operator credential-swap config (host-side injection) |
| `Broker` / `Brokers` | egress broker endpoint(s); setting both is rejected — see [the broker section](#egress-broker-from-the-library) |

`EvaluateCapabilityComposition` derives stable categories from the complete
effective options. `Create`, `Run`, snapshot forks, and `Start` reject the
combination of guest private data, injected external content, and unmediated
outbound before boot unless `CapabilityRiskAcknowledgement` records an operator
reason. The same `CapabilityComposition` is returned in structured results and
stored in the workspace manifest.

**Model pairing**

| Field | Meaning |
|---|---|
| `Model` | canonical model ref the workspace is paired with (persisted; re-paired each start) |
| `ModelTarget` | host `host:port` of a paired model server, realized as a guest→host vsock channel |
| `ModelRunner` | `ModelRunnerSpec`: runner selection, GPU intent, backend model id, command template, args |
| `ModelMediation` | `ModelMediationSpec`: mediation mode and policy source |

**Advanced paths and hooks**

| Field | Meaning |
|---|---|
| `StateDir` | state root (default `~/.microagent`) |
| `KernelPath` / `KernelExplicit` | kernel image path and whether it was set explicitly |
| `SupervisorPath` | supervisor companion binary override |
| `GuestInitPath` | `microagent-guestinit` binary override |
| `Mke2fsPath` | `mke2fs` binary used for ext4 builds |
| `Verification` | pre-computed runtime verification to attach |
| `Progress` | `rootfs.ProgressFunc` callback for build/pull progress |

Paired model workspaces set `Options.Model`, `Options.ModelRunner`, and
`Options.ModelMediation`. `ModelRunnerSpec` stores the runner selection
(`llamacpp`, `vllm`, or `custom`), GPU intent, backend model id,
custom command template, and repeatable runner args. `ModelMediationSpec` stores
the mediation mode, policy source (`PolicyFile` or `PolicyURL`), and an optional
`PolicyTimeout` for policy fetches. Runner env is transient and is intentionally
not persisted to workspace manifests.

For non-defaults - backend override, custom kernel, sized memory/CPUs, networking
- set the matching `Options` fields before calling `Run`. The lifecycle API:

| Function | Purpose |
|---|---|
| `workspace.Create` | Build and prepare a named workspace rootfs and manifest |
| `workspace.Run` | Build, run, collect result state, and optionally clean up |
| `workspace.Start` | Start an existing named workspace from its manifest |
| `workspace.Delete` | Stop or kill a live workspace as requested, then delete its disk and state. Idempotent and explicit: an absent workspace returns success with `Deleted` false and no event, so retried teardown never fails but a caller can tell nothing was removed |
| `workspace.DeleteOptions` | Select graceful stop or forced kill before deletion |
| `workspace.DeleteResult` | The shared lifecycle `Response`, plus `Deleted` distinguishing "removed" from "was already absent" |
| `workspace.Absent` | Report whether nothing of the workspace exists (no records, no root directory); a partially created workspace is present |
| `workspace.Inspect` | Ask the backend supervisor for current runtime state |
| `workspace.Status` | Read enriched local workspace status from state files |
| `workspace.VolumeDiskUsage` | Measure a named volume's backing image: provisioned, filesystem-used, and host-allocated MiB (`vmkit.DiskUsage`; nil when unreadable) |
| `vmkit.DiskAssessmentOverprovisioned` / `vmkit.DiskAssessmentNearlyFull` | The advisory `DiskUsage.Assessment` verdict values |
| `vmkit.DiskUsage` | The three sizes of a disk image — provisioned capacity, filesystem-used, and sparse host allocation — carried as `RootfsUsage` on inspect/status responses, with an advisory `Assessment` (`overprovisioned` / `nearly-full`) |
| `workspace.Resize` | Grow or shrink a stopped workspace's rootfs disk in place (`ResizeOptions`; refuses while running or while snapshots exist; returns `ResizeResult` with the new `vmkit.DiskUsage`) |
| `workspace.Wait` | Block until the workspace reaches a terminal state (`WaitOptions` bounds it; returns a `WaitResult`, or `WaitTimeoutError` on timeout) |
| `workspace.ResultStatus` | Read status plus guest result output |
| `workspace.ArtifactsFor` | Read declared ingress and egress artifacts |
| `workspace.GetArtifact` | Copy a declared output artifact from a stopped workspace |
| `workspace.Copy` | Copy files between the host and a stopped workspace disk |
| `workspace.GuestRootfsLayerTar` | Export a stopped workspace's filesystem as an OCI layer tar via a guest maintenance boot (backends without host ext4 tooling) |
| `workspace.Clone` | Clone a stopped/prepared workspace |
| `workspace.ReadLogs` | Read a workspace serial log |
| `workspace.ReadSupervisorLogs` | Read a workspace's host-side supervisor companion logs |
| `workspace.ReadEvents` | Read the concurrency-safe, bounded lifecycle event history; malformed persistence returns an error |
| `workspace.ReadEgressAudit` | Read the egress mediator's recorded allow/deny/MITM/DNS/UDP decisions (`EgressEvent`, from `EgressAuditPath`) |
| `workspace.ReadBrokerAccess` | Read the egress broker's per-request decision records (`EgressEvent`, from `BrokerAccessPath`) |
| `workspace.MergeEgressEvents` | Merge the mediator and broker streams into one time-ordered view |
| `workspace.SampleStats` | Sample CPU, memory, and I/O for a running workspace |
| `workspace.Network` | Read configured and runtime network state |
| `workspace.List` | List named workspaces from local state |
| `workspace.Control` | Run a lifecycle control action (`halt`, `quarantine`, `pause`, `resume`, `stop`, `kill`, `delete`, `gc`). For `quarantine` this is the raw containment primitive and does **not** capture evidence — use `workspace.Quarantine` for the verb-level behavior |
| `workspace.Quarantine` | Capture evidence, then contain. Takes a `workspace.QuarantineOptions` and returns a `workspace.QuarantineResult`. Containment stops the runtime, so the forensic capture happens first; it is best-effort and never blocks containment, and a failure is reported in the result |
| `workspace.QuarantineOptions` | `SkipCapture` contains without capturing (accepting the loss of volatile state); `CaptureTag` overrides the generated tag |
| `workspace.QuarantineResult` | `Response`, evidence capture fields, and an `IncidentReceipt` summarizing host-observed lifecycle, egress, broker, and secret-access records for the quarantined session |
| `workspace.IncidentReceipt` | Self-contained, session-scoped incident summary. It reports destinations, byte counts, and secret names and outcomes without storing content or secret values; `Complete` and `Errors` make audit-read failures explicit |
| `workspace.BrokerAuditSummary` | Broker request totals, destination verdicts, byte counts, and the names of swapped secret references |
| `workspace.SecretAuditSummary` | Secret-access totals grouped by secret name and outcome; values are never present |
| `workspace.ForensicCaptureTagPrefix` | Tag prefix (`forensic-`) for automatic quarantine captures, so they are identifiable on sight and never collide with operator tags |
| `workspace.Pause` / `workspace.Resume` | Freeze and thaw a running workspace's vCPUs in place |
| `workspace.Snapshot` | Capture a tagged memory-plus-disk snapshot of a running or paused workspace (quarantine stops the runtime, so capture before containing) |
| `workspace.SnapshotForensic` | Capture for investigation rather than restore: the guest secret purge is skipped, because credential material is the evidence and lives only in volatile memory. The manifest records secrets as materialized and NOT purged, which the restore path refuses — so a forensic capture can never be rehydrated as a workspace, and its flags mark it as secret-bearing for protected custody |
| `workspace.CreateFromSnapshot` | Fork a new workspace from another workspace's snapshot and resume it |
| `workspace.SnapshotList` / `workspace.SnapshotRemove` | List or delete a workspace's snapshots (host-side) |
| `vmkit.SafeSnapshotTag` | Report whether a snapshot tag is a bounded, path-safe identifier |
| `workspace.Supervise` | Run the optional restart-policy loop for a workspace |
| `workspace.ReadManifest` / `workspace.WriteManifest` | Manage workspace manifests directly |

`workspace.Control(ctx, opts, command)` takes the action as its positional
`command` argument — one of `halt`, `quarantine`, `pause`, `resume`, `stop`,
`kill`, `delete`, or `gc` — and rejects anything else. The CLI's `stop` is a
registry-level alias of `halt`: typing `microagent stop` runs the identical
graceful-shutdown mechanism and records the identical `halted` outcome on a
clean exit. Calling `workspace.Control(ctx, opts, "stop")` directly, though,
is a distinct library command — same shutdown mechanism as `halt`, but it
records the terminal state `stopped`, not `halted`.
Both clean commands make a two-second, best-effort structured exec request for
the guest to run `sync` before dispatching shutdown. The attempt and outcome are
written to lifecycle event history. A failed or timed-out flush never lets the
guest block the halt; the control operation proceeds. `kill` and raw quarantine
do not make this preparation request.
The terminal event also carries `vmkit.LifecycleAudit`. It combines the
provenance-labeled `Options.Caller`, `Options.Purpose`, host-declared manifest
commands, notification ownership, and a bounded best-effort guest process
snapshot. Guest processes are explicitly `guestReported`; kill skips that
request, and capture failures do not block control. `workspace.Quarantine`
links a successful forensic capture through the audit record's evidence
reference.

| Type or function | Purpose |
|---|---|
| `vmkit.CallerAttribution` | Adapter channel plus optional caller-supplied subject and delegated authority, with explicit assurance |
| `vmkit.DeclaredWork` | One host-recorded manifest command and its role |
| `vmkit.GuestProcess` | One bounded, guest-reported process observation |
| `vmkit.WorkInFlight` | Declared work, guest observations, capture status, and evidence reference |
| `vmkit.NotificationRecord` | Records that notification was not performed and remains caller-owned |
| `vmkit.ValidateLifecycleAudit` | Validates provenance vocabulary and bounds before supervisor dispatch |
| `workspace.LifecycleInspectTimeout` | Maximum delay allowed for the guest process snapshot |
`delete` also removes the local state directory after the supervisor
confirms; `gc` sweeps expired-lease workspaces (a declared TTL whose activity
marker has gone idle). `workspace.Pause` and `workspace.Resume` are thin
wrappers over the `pause` and `resume` actions and share their capability
gate.

### Copying files and artifacts

`workspace.Copy(ctx, stateDir, debugfsPath, source, target)` and
`workspace.GetArtifact(ctx, stateDir, debugfsPath, name, artifactName, target)`
operate on a *stopped* workspace's disk. On hosts with ext4 tooling they shell
out to `debugfs` from e2fsprogs: pass the binary path in `debugfsPath`. There
is no in-library default — pass `"debugfs"` to resolve via `PATH` (the CLI's
own default is a `PATH` lookup with a
`/opt/homebrew/opt/e2fsprogs/sbin/debugfs` fallback for macOS Homebrew); an
empty string fails.

### Companion binary resolution

`pkg/workspace` boots VMs through companion binaries that ship with the CLI:
`microagent-firecracker-supervisor` and `microagent-guestinit` on Linux,
`microagent-applevf-supervisor` on macOS. The library resolves them relative
to an *install base*: first the running executable's own directory (and its
`../libexec`), then the directory of `microagent` found on `PATH`. The second
base is what makes resolution work for embedders: your program's
`os.Executable()` is not microagent's install prefix. Without a
`microagent` install on `PATH` (or explicit overrides) the lookup falls back
to bare names like `microagent-firecracker-supervisor`, which fail unless
those are themselves on `PATH`. The same two-base resolution covers the
packaged kernel (`../libexec/kernels/<backend>/<arch>/Image`).

Deploying an embedder to a host without the microagent CLI therefore means
shipping the companions yourself. Either place them next to your binary,
set `Options.SupervisorPath` / `Options.GuestInitPath` /
`Options.KernelPath`, or set the `MICROAGENT_FIRECRACKER_SUPERVISOR` /
`MICROAGENT_APPLEVF_SUPERVISOR` environment variables (checked before the
install-base search). Installers that need to mirror packaged resolution can
use `workspace.FirecrackerSupervisorPathFromExecutable` (and the
`AppleVFSupervisorPathFromExecutable` / `GuestInitPathFromExecutable`
equivalents) to derive a companion path from a `bin/microagent` executable.
`workspace.Supervisor(opts)` returns the resolved `vmkit.Supervisor` for the
selected backend if you want to verify resolution up front.

### Error classification

Application operations return `operation.Error` for stable categories:
validation, state conflict, not found, resource exhaustion, unsupported
behavior, policy denial, and transient failure. Use `errors.As` or
`operation.IsKind`; do not classify an operation by matching its message:

```go
result, err := workspace.Start(ctx, opts)
if operation.IsKind(err, operation.ErrorConflict) {
	// inspect current workspace state before retrying
}
```

The error unwraps its cause, so ordinary `errors.Is` and `errors.As` checks
continue to work. CLI and MCP render the same category independently of the
message wording.

`workspace.WorkspaceNotFoundError` is the structured "no such workspace"
error. It is returned by the console helpers, `workspace.Exec` (and the other
structured-exec entry points), `workspace.SampleStats`, and
`workspace.Status` when a workspace has no runtime or event state. It
supports `errors.Is`, so classify missing workspaces separately from stopped,
halted, or quarantined ones:

```go
if _, err := workspace.Status(opts); errors.Is(err, workspace.WorkspaceNotFoundError{}) {
	// never created, or already deleted
}
```

### Concurrency

`Options` is a plain value: every lifecycle function takes its own copy, so
sharing a template `Options` across goroutines and customizing per call is
safe. Structured exec is safe to run concurrently against one running
workspace. Each `Exec`/`ExecStream` call dials its own connection and the
guest service handles each connection in its own goroutine, running each
command as an independent guest process. Lifecycle mutations are a different
story — `Run`, `Start`, `Control`, `Snapshot`, `Copy`, and the other
lifecycle functions read and
write shared per-workspace state files. The package makes no documented
guarantee about concurrent mutations of the *same* workspace name. Treat the
lifecycle of a given workspace as single-threaded (operations on different
workspace names are independent) unless you have verified a specific
interleaving yourself.

### Console helpers

`workspace.SendConsoleCommand` returns `workspace.ConsoleReadTimeoutError` when
`--send`-style command completion is not observed before the send timeout; the
error carries any partial output captured before the deadline.
If the console connection closes before the completion marker arrives,
`SendConsoleCommand` returns `workspace.ConsoleCompletionUnknownError` with any
partial output captured before EOF. `workspace.ProbeShellCommand` uses the same
marker protocol with a no-op command to verify a shell target can complete a
command round trip. Callers that need explicit readiness semantics can use
`workspace.ShellReadinessSignalWithMode` with `workspace.ShellReadinessProbeTCP`
for TCP accept reachability or the command probe mode for end-to-end command
readiness.

### Structured exec

Structured exec uses `workspace.Exec(ctx, opts, request)` with
`pkg/workspace/exec/protocol.ExecRequest` — callers import the `protocol`
package for the request and result types. Build a request with
`protocol.NewExecRequest(argv)` (which sets the protocol version and
single-response mode) and set what you need:

- `Argv` — the command as an argument vector; no shell is implied.
- `Env`, `Cwd`, `Stdin` — environment, working directory, and stdin bytes
  (stdin is capped at `protocol.DefaultOutputLimitBytes`).
- `TimeoutMS` — per-command guest-side timeout; the service default is
  `protocol.DefaultTimeout` (5 minutes).
- `OutputLimitBytesStdout` / `OutputLimitBytesStderr` — per-stream capture
  caps, each at most `protocol.DefaultOutputLimitBytes` (10 MiB). Output
  beyond a cap is dropped and the result's `StdoutTruncated` /
  `StderrTruncated` flag is set.

A nil Go error means the host reached the guest exec service and decoded a
structured result — it says nothing about the command's outcome. Check
`ExecResult.Status` (`protocol.ExecStatus`) first:

| Status | Meaning |
|---|---|
| `exited` | the process ran to completion; `ExitCode` is set |
| `signaled` | the process was killed by a signal |
| `timed_out` | the per-command timeout elapsed and the process was terminated |
| `failed_to_start` | the command never started (bad path, invalid request) |

`ExecResult.ExitCode` is a `*int` (JSON tag `exit_code`) and is **nil unless
`Status` is `exited`** — dereferencing it unconditionally panics exactly when
a command times out or fails to start. Guard it:

```go
result, err := workspace.Exec(ctx, opts, req)
if err != nil { /* transport / workspace-state error */ }
switch {
case result.Status == protocol.ExecStatusExited && *result.ExitCode == 0:
	// success
case result.Status == protocol.ExecStatusExited:
	// nonzero guest exit — not a Go error
default:
	// timed_out, signaled, or failed_to_start: ExitCode is nil
}
```

Nonzero guest exit codes are not Go errors. Use `workspace.ExecWithMetadata`
when callers need retry accounting via `workspace.ExecRetryMetadata`.
Transient transport failures are retried by the shared exec layer before the
final result or `workspace.ExecRetryExhaustedError` is returned.
`workspace.IsRetryableExecTransient` exposes the same classifier for callers
that need to reason about exec transport errors directly. The `execReady`
readiness signal in the [runtime contract](#supervisor-types)
(`vmkit.RuntimeReadiness.ExecReady`, produced by
`workspace.ExecReadinessSignal`) uses the same protocol with a no-op command
to verify the service end-to-end. `Exec` gates on it for up to
`workspace.ExecReadyWait` after start so an immediate post-start command does
not surface a transient failure.

`workspace.ExecStream(ctx, opts, request, onChunk)` runs the same request in
streaming mode: the guest emits stdout/stderr chunk frames as the command runs
(delivered to `onChunk`) followed by a terminal result frame. In stream mode the
returned `ExecResult` carries status, exit code, timing, and truncation flags but
not the output bytes - those arrive as chunks. The CLI exposes this as
`microagent exec --stream`.

`workspace.MarkActivity(opts)` stamps a per-workspace `activity` marker file
(mtime = last genuine use) and is called on each real exec/connect. The deadman
watcher and gc sweep read it to measure idleness, so a declared `--ttl` lease is
renewed by activity and only an idle VM is reaped. Internal readiness probes do
not call it, so background liveness traffic cannot keep an abandoned VM alive.

### Egress broker from the library

The egress broker is a host-side forward proxy served on a vsock listener: the
guest reaches an upstream through it, the broker injects the workspace's
credential upstream, and the guest only ever holds a reference. From the
library it is declared per workspace with `Options.Broker`
(`*vmkit.BrokerConfig`) or `Options.Brokers` (multiple endpoints — setting
both is rejected). `vmkit.BrokerConfig` carries the terminate-mode `Upstream`
base URL, the host-side-only `Secret` reference, guest wiring (`GuestListen`,
`BaseURLEnv`, `Proxy`), the raw-capture opt-in (`Capture`), an optional
`UpstreamCAFile`, and the `ConnectAllowlist` for the proxy tunnel. Transport
defaults (vsock port, guest listen address) are filled at request time.

Rather than constructing the struct by hand, use the shared parsers — they
fail closed on partial declarations and pasted literal secrets, so every
surface (CLI, Agentfile, MCP, your program) builds an identical broker:

```go
cfg, err := workspace.ParseBrokerConfig(
	"https://api.anthropic.com",             // upstream
	"ANTHROPIC_API_KEY=env:ANTHROPIC_API_KEY", // NAME=<scheme>:<ref>, held host-side
	[]string{"ANTHROPIC_BASE_URL"},          // base-URL env keys pointed at the broker
	false,                                   // proxy: set HTTPS_PROXY/HTTP_PROXY in the guest
	false,                                   // capture: governed raw-capture opt-in
	"",                                      // ca: PEM bundle for a private upstream cert
)
if err != nil {
	panic(err)
}
opts.Broker = cfg
```

`workspace.ParseBrokerEndpoints(specs)` parses the repeatable
`;`-separated `key=value` endpoint spec strings (the `--broker-endpoint`
grammar) into `Options.Brokers`. For what the broker enforces and how it
relates to `EgressMode` and MITM mediation, see
[egress mediation](/concepts/egress-mediation/).

Mediation readiness uses `vmkit.MediationReadinessSignal(ctx, mediation, state,
observedAt, timeout)` to apply the shared live reachability contract. It returns
ready only when the workspace is running and the declared mediation target
accepts a bounded TCP probe. Required mediation target failures include an
error; optional mediation target failures are reported as not ready without a
hard error.

## Kernel API

Use `pkg/kernel` when your program wants microagent to manage backend kernel
assets directly.

```go
result, err := kernel.Install(ctx, kernel.InstallOptions{
	Backend:      vmkit.BackendLinuxKVM,
	Architecture: "amd64",
})
if err != nil {
	panic(err)
}

verified, err := kernel.Verify(kernel.VerifyOptions{
	Path:   result.Path,
	SHA256: result.SHA256,
})
if err != nil {
	panic(err)
}
_ = verified
```

`kernel.DefaultSource()` returns the production manifest source: the TUF trust
root embedded in the binary plus the canonical metadata/targets URLs. Pass it
(or your own `ManifestSource`) to `kernel.FetchTargets` and feed the targets to
`kernel.CheckUpdate` to classify an installed kernel as `current`, `optional`
(behind latest but at the security floor), `security` (missing security
fixes), or `unknown`. `kernel.Support(backend, arch)` reports whether a
kernel image is present at the resolved path for a backend/architecture pair
without installing anything.

You normally don't call `Install` for the boot path: `workspace.EnsureKernel`
runs inside `workspace.Create`/`Run`/`Start`/`CreateFromSnapshot` and installs
the default kernel into the managed per-user path when none is present.
Importing `pkg/kernel` is what arms it - the package registers the installer at
init via `workspace.RegisterKernelInstaller` (a `workspace.KernelInstaller`
func; the indirection exists because `pkg/kernel` depends on `pkg/workspace`).
A program that imports only `pkg/workspace` performs no implicit downloads: a
missing kernel surfaces at boot. Add a blank import of `pkg/kernel` (or ship a
kernel yourself) to choose the behavior you want. An explicit
`Options.KernelPath` is always used as-is.

## Image cache API

Use `pkg/imagecache` when an orchestrator wants reusable rootfs baselines.

```go
record, err := imagecache.Pull(ctx, imagecache.PullOptions{
	StateDir:     "/home/me/.microagent",
	ImageRef:     "docker.io/library/ubuntu@sha256:...",
	Architecture: "amd64",
})
if err != nil {
	panic(err)
}
_ = record
```

`workspace.CanReuseRootfsBaseline(opts)` is the predicate that decides
whether a workspace can clone a pulled baseline instead of building. It is
true only when nothing would bake workspace-specific content into the
rootfs: no guest command or image command, no explicit size, no env, files,
disks, published ports, or custom console shell. The hostname does not
disqualify reuse — it reaches the guest on the kernel command line at
boot, not through the rootfs. Wire a resolver into
`Options.RootfsBaseline` (typically `imagecache.Find`) and
`workspace.Create` clones automatically when the predicate holds.
The store seeds itself: `Options.RootfsBaselineSave` (wired by the CLI to
`imagecache.SaveBaseline`) records the first plain build of an image as a
baseline, so later creates and runs clone without an explicit `image pull`.
Baseline records carry the hash of the guest init they were built with
(`workspace.GuestInitSHA256`); reuse requires it to match the init the
workspace would inject. `workspace.CopyFile` reflinks on filesystems that
support it (btrfs/XFS/APFS), so a clone is metadata-only where possible;
`workspace.CopyFileReplace` is the overwrite variant.
`workspace.BaselineSatisfiesSize(prov, opts)` is the companion check the
clone path applies to the resolved baseline. Its recorded bytes must cover
the workspace's effective size (profile-implied sizes the predicate cannot
see), or the clone falls through to a real build.

### The per-boot config disk

Nothing per-workspace is baked into a rootfs. Each boot, the lifecycle
assembles a guest run config (`workspace.GuestBootConfig` →
`workspace.GuestRunConfig`: command, mode, env, ports, mounts, forwards,
console shell, maintenance flag). It writes the config with any declared
files as a raw tar stream to the workspace's config disk
(`workspace.WriteConfigDisk`; path from `workspace.ConfigDiskFile`). The
supervisor attaches that file read-only as the last block device
(`vmkit.Config.ConfigDiskPath`; guest device path math in
`vmkit.VirtioBlockDevice`) and names it on the kernel command line. Declared
file contents are captured once at create into a durable archive
(`workspace.WriteFilesArchive`, path from `workspace.FilesArchivePath`;
modes parsed by `rootfs.ParseFileMode`) so later boots deliver the
create-time bytes even if the sources change. Each regeneration re-records
the disk's hash in the manifest verification block via
`workspace.RefreshManifestVerificationConfig`, so the command and files the
guest runs never escape attestation.

## Diagnostics API

Use `pkg/diagnostics` for host preflight checks.

```go
resp, err := diagnostics.Check(ctx, diagnostics.Options{
	Backend: vmkit.BackendLinuxKVM,
	Arch:    "amd64",
})
if err != nil {
	// resp still carries structured support details when available, so
	// inspect both. Surface the error rather than swallowing it.
	log.Printf("diagnostics.Check: %v", err)
}
_ = resp
```

The CLI contains presentation, flag parsing, build metadata output, and raw
terminal handling. microVM orchestration and management capabilities are exposed
through the Go packages, and the mapping below shows the equivalent library
entry points.

## CLI ↔ library mapping

If you already know the CLI, this is the lookup for the equivalent library call:

| CLI command | Library call |
|---|---|
| `microagent run` | [`workspace.Run`](#workspace-api) |
| `microagent dispatch` | `workspace.RunDispatch` |
| `microagent init` | `scaffold.Generate` |
| `microagent create` | `workspace.Create` |
| `microagent start` | `workspace.Start` |
| `microagent create --from-snapshot` | `workspace.CreateFromSnapshot` |
| `microagent start --from-snapshot` | `workspace.Start` with `Options.FromSnapshot` |
| `microagent status` | `workspace.Status` (local) / `workspace.Inspect` (live, via supervisor) |
| `microagent wait` / `microagent start --wait` | `workspace.Wait` |
| `microagent result` | `workspace.ResultStatus` |
| `microagent list` / `microagent ls` / `microagent ps` | `workspace.List` |
| `microagent halt` / `microagent stop` / `microagent kill` / `microagent delete` | `workspace.Control` (one function; the action is the positional `command` argument: `halt`, `quarantine`, `pause`, `resume`, `stop`, `kill`, `delete`, or `gc`) |
| `microagent quarantine` | `workspace.Quarantine` (captures evidence, then contains). `workspace.Control(ctx, opts, "quarantine")` is the containment primitive without the capture |
| `microagent pause` / `microagent resume` | `workspace.Pause` / `workspace.Resume` |
| `microagent snapshot` create / list / delete | `workspace.Snapshot` / `workspace.SnapshotList` / `workspace.SnapshotRemove` |
| `microagent apply` | `workspace.Apply` |
| `microagent supervise` | `workspace.Supervise` |
| `microagent connect` | `workspace.DialConsole` / `SendConsoleCommand` (raw terminal mode stays CLI-only) |
| `microagent exec` | `workspace.Exec` |
| `microagent logs` | `workspace.ReadLogs` |
| `microagent events` | `workspace.ReadEvents` (text lifecycle view) / `workspace.ReadTrajectory` (structured joined view) |
| `microagent egress` | `workspace.ReadEgressAudit` + `workspace.ReadBrokerAccess` (merged via `workspace.MergeEgressEvents`) |
| `microagent stats` | `workspace.SampleStats` |
| `microagent cp` | `workspace.Copy` |
| `microagent clone` | `workspace.Clone` |
| `microagent commit` / `microagent image push` | `commit.Commit` / `commit.Push` |
| `microagent artifact` / `microagent artifact get` | `workspace.ArtifactsFor` / `workspace.GetArtifact` |
| `microagent network` | `workspace.Network` |

`workspace.NewSessionID` creates an execution-lifetime identifier for callers
that build raw requests. `workspace.TrajectoryRecord` is the common envelope
returned by `workspace.ReadTrajectory`; its `Raw` field preserves the complete
source record. The joined view includes constraint revisions from
`workspace.ReadConstraintHistory`. Each revision carries an independently
reconstructable manifest snapshot plus config-disk and verification hashes.
`workspace.ConstraintRevision` is the complete record, and
`workspace.ConstraintHistoryPath` returns its host-owned path. Retention uses
`workspace.DefaultMaxConstraintRevisions`.

Status responses summarize the bounded history in `Response.ConstraintHistory`.
The field uses `vmkit.ConstraintHistoryStatus`; its oldest and latest entries
are `vmkit.ConstraintRevisionRef` values without embedded manifests.

`workspace.Options.Purpose` and `CorrelationID` are opaque caller
context persisted in `vmkit.Identity`; the library records them verbatim and
does not use them for policy decisions. For lifecycle mutations, set
`Options.Purpose` to the operator's reason before calling `Control`,
`Quarantine`, or `Delete`; adapters expose that same value as `reason`.
Set `Options.Caller` only from caller context your integration already holds.
Use `Assurance: "caller_asserted"` when microagent has not authenticated the
subject; leaving the caller unset produces a library-channel attribution with
`unavailable` assurance rather than a fabricated principal.
| `microagent model` | `model.Pull` / `model.List` / `model.Remove` / `model.Prune` / `modelrunner.Ensure` |
| `microagent volume` | `volume.Create` / `volume.List` / `volume.Get` / `volume.Remove` / `volume.Attach` |
| `microagent secret check` | `secret.DefaultRegistry` / `secret.Registry.Check` |
| `microagent doctor` / `microagent host` | [`diagnostics.Check`](#diagnostics-api) |
| `microagent contract` | `vmkit.NewRuntimeContract` |
| `microagent kernel install` / `microagent kernel verify` | [`kernel.Install`](#kernel-api) / `kernel.Verify` |
| `microagent rootfs build` | `rootfs.Builder.Build` |
| `microagent image` | [`imagecache.Pull`](#image-cache-api) / `List` / `Tag` / `Remove` / `Prune` |
| `microagent registry` login / logout / list | `registryauth.Login` / `registryauth.Logout` / `registryauth.List` |
| `microagent perf` / `microagent perf boot` / `microagent perf footprint` / `microagent perf steady` | `perf.Boot` / `Footprint` / `Steady` |
| `microagent profiles` | `workspace.ProfileNames` / `workspace.LookupProfile` |
| `microagent serve mcp` | CLI-only MCP stdio transport over the existing package APIs |
| `microagent model serve` | `modelrunner.Ensure` (start or reuse a host-local model runner process) |
| `microagent version` | CLI-only build metadata output |
| `microagent.yaml` (spec parsing) | `workspace.ReadSpec` / `ApplySpecFile` |

The library calls take options structs and return typed responses. For reusable
runtime behavior, prefer the package API. The remaining CLI-only commands are
presentation concerns such as `help`, `version`, and raw terminal mode around an
already-open console connection.
