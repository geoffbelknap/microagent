---
title: Go library
description: Use microagent packages directly from Go.
---

<!-- docs-last-updated -->
_Last updated: 2026-07-09_

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
`workload`-role identity by default - you get caller-visible identity for
free without writing agent-aware code, and you can drop down to
[`pkg/vmkit`](#supervisor-types) when you need a different role or a custom
supervisor request.

## Exported packages

`microagent` has these exported Go packages today:

| Package | Purpose |
|---|---|
| `pkg/vmkit` | supervisor request/response types, validation, and executable supervisor client |
| `pkg/workspace` | workspace lifecycle API, options, defaults, request construction, backend supervisor selection, and shared helpers |
| `pkg/kernel` | kernel default manifest, install, verify, and support checks |
| `pkg/imagecache` | reusable rootfs image cache indexing, pull, tag, remove, and prune |
| `pkg/diagnostics` | backend host diagnostics and support summaries |
| `pkg/perf` | boot, footprint, and steady-state performance measurements |
| `pkg/rootfs` | OCI image and tar bundle conversion into ext4 disks |
| `pkg/supervisors/firecracker` | Linux Firecracker supervisor implementation |

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
| `pkg/vmkit` | `Request`, `Response`, `Config`, `Identity`, `Disk`, `NetworkConfig`, `PortForward`, `MediationConfig`, `VsockListener`, `RuntimeArtifacts`, `ArtifactRef`, `RuntimeResult`, `Event`, `VMState`, `StateUnknown`, `RoleWorkload`, `Supervisor`, `SupervisorClient`, `ExecutableSupervisor`, `NewRuntimeContract`, `FeatureContract`, `FeatureScope`, `FeatureBackendNeutral`, `FeatureCapability`, `FeatureCapabilityStructuredExec`, `FeatureBackend`, `FeatureGap`, `UnsupportedFeatureError`, `FeatureContracts`, `FeatureBackendSupport`, `BackendSupportsFeature`, `FeatureForCLICommand`, `FeatureForMCPTool`, `NewUnsupportedFeatureError`, `IsKnownBackend`, `Capabilities`, `BackendCapabilities`, `SnapshotManifest`, `SnapshotArtifact`, `SnapshotInfo`, `SnapshotManifestName`, `SnapshotVMStateName`, `SnapshotMemoryName`, `SnapshotRootfsName`, `SnapshotAppleVFMachineState`, `SnapshotAppleVFConfig`, `SnapshotsDir`, `SnapshotDir`, `SnapshotRootfsArtifact`, `SnapshotMachineStateArtifacts`, `FirecrackerSnapshotArtifacts`, `AppleVFSnapshotArtifacts`, `WriteSnapshotManifest`, `ReadSnapshotManifest`, `ListSnapshots`, `RemoveSnapshot`, `MaterializedSecretsDeclared`, `ValidateSnapshotSecretCapture`, `ValidateSnapshotSecretRestore`, `EgressModeGuarded`, `EgressModeBroker`, `EgressMediationOn`, `EgressModeForgesCerts`, `NetworkModeMediates`, `NormalizeEgressMode`, `EgressPolicy`, `EgressCaps`, `NormalizeEgressPolicy` |
| `pkg/workspace` | `Options`, `OptionsFromRequest`, `EgressPolicyFromOptions`, `DefaultOptions`, `Spec`, `SpecApplyOptions`, `ApplyResult`, `Manifest`, `ModelRunnerSpec`, `ModelMediationSpec`, `Result`, `GuestResult`, `CopyResult`, `ListEntry`, `SuperviseOptions`, `SuperviseResult`, `ConsoleOptions`, `ShellTarget`, `ConsoleReadTimeoutError`, `ConsoleCompletionUnknownError`, `ShellReadinessProbeMode`, `ShellReadinessProbeTCP`, `ShellReadinessSignalWithMode`, `DefaultModelGuestPort`, `ExecReadyProbeTimeout`, `ExecReadyWait`, `ExecMaxTransientRetries`, `ExecPort`, `ExecPortForName`, `ExecRetryExhaustedError`, `ExecRetryMetadata`, `ExecReadinessSignal`, `Create`, `CreateFromSnapshot`, `Run`, `Start`, `Inspect`, `Status`, `ResultStatus`, `ArtifactsFor`, `GetArtifact`, `Copy`, `GuestRootfsLayerTar`, `Clone`, `ReadLogs`, `ReadEvents`, `EventsPath`, `ReadEgressAudit`, `EgressAuditPath`, `EgressEvent`, `EgressAuditSummary`, `SummarizeEgressAudit`, `RunDispatch`, `DispatchResult`, `SampleStats`, `Stats`, `Network`, `List`, `Control`, `Pause`, `Resume`, `Snapshot`, `SnapshotList`, `SnapshotRemove`, `Apply`, `Exec`, `ExecWithMetadata`, `ExecStream`, `MarkActivity`, `IsRetryableExecTransient`, `DialConsole`, `SendConsoleCommand`, `ConsoleTarget`, `ProbeShellCommand`, `Supervise`, `ReadSpec`, `ApplySpec`, `ApplySpecFile`, `ReadManifest`, `WriteManifest`, `ProfileNames`, `LookupProfile` |
| `pkg/kernel` | `InstallOptions`, `InstallResult`, `Install`, `VerifyOptions`, `VerifyResult`, `Verify`, `Default` |
| `pkg/imagecache` | `PullOptions`, `Record`, `PruneResult`, `Pull`, `Find`, `List`, `Tag`, `Remove`, `Prune`, `ReadIndex`, `FromProvenance` |
| `pkg/diagnostics` | `Options`, `Check`, `EgressTProxyRemediation` |
| `pkg/perf` | `BootOptions`, `BootReport`, `FootprintReport`, `SteadyReport`, `Iteration`, `Summary`, `RSSSample`, `RSSSummary`, `Boot`, `Footprint`, `Steady`, `ProcessRSSKiB`, `ParseRSSKiB`, `SampleProcessRSS`, `SummarizeIterations`, `SummarizeRSSSamples` |
| `pkg/rootfs` | `BuildRequest`, `BundleRequest`, `BundleProvenance`, `Builder`, `NewBuilder`, `NormalizeRequest`, `NormalizeBundleRequest`, `Platfodelete`, `Provenance` |
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

For backend-independent code, depend on the interface:

```go
func inspect(ctx context.Context, supervisor vmkit.Supervisor, req vmkit.Request) (vmkit.Response, error) {
	req.Command = "inspect"
	return supervisor.Do(ctx, req)
}
```

`vmkit.NewRuntimeContract()` also exposes the runtime feature records used by
the CLI and MCP adapters. Use `vmkit.FeatureContracts()` when your integration
needs to map commands or MCP tools back to library-owned behavior, and use
`vmkit.FeatureForCLICommand()` / `vmkit.FeatureForMCPTool()` when checking
adapter coverage. If a library path rejects a feature, return
`vmkit.NewUnsupportedFeatureError()` so CLI and MCP callers see the same
structured error shape.

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

`rootfs.BuildEmptyVolume(ctx, outputPath, sizeBytes)` writes an empty ext4
filesystem wrapped in a VHD footer, sized to `sizeBytes`. It backs named volumes
on VHD-lane backends (windows-hyperv), whose hosts have no `mke2fs`: the image is
built in-process and the guest gets writable capacity once `microagent-guestinit`
releases the reserved-space padding at the mountpoint on first mount. It fails
closed on non-VHD hosts. The `pkg/volume` package calls it automatically when the
backend's capabilities select the VHD disk format, so most callers manage volumes
through `volume.Create` rather than calling this directly.

## Workspace API

Use `pkg/workspace` when your program wants to create, run, start, inspect, and
control named workspaces without parsing CLI flags.

`workspace.DefaultOptions()` picks the host backend (Firecracker on Linux,
Apple Virtualization.framework on macOS), guest architecture, default kernel
path, and default state directory. You override only what your program needs.
The [common pattern](#the-common-pattern) example at the top of this page shows
the canonical `DefaultOptions` + `Run` call.

`Result.Result` is a `*GuestResult` with `Stdout`, `Stderr`, and `ExitCode`
captured from the guest. `Result.Response` carries the supervisor's structured
response (state, identity, verification).

Paired model workspaces set `Options.Model`, `Options.ModelRunner`, and
`Options.ModelMediation`. `ModelRunnerSpec` stores the runner selection
(`llamacpp`, `vllm`, or `custom`), GPU intent, backend model id,
custom command template, and repeatable runner args. `ModelMediationSpec` stores
the mediation mode and policy source (`PolicyFile` or `PolicyURL`). Runner env is
transient and is intentionally not persisted to workspace manifests.

For non-defaults - backend override, custom kernel, sized memory/CPUs, networking
- set the matching `Options` fields before calling `Run`. The lifecycle API:

| Function | Purpose |
|---|---|
| `workspace.Create` | Build and prepare a named workspace rootfs and manifest |
| `workspace.Run` | Build, run, collect result state, and optionally clean up |
| `workspace.Start` | Start an existing named workspace from its manifest |
| `workspace.Inspect` | Ask the backend supervisor for current runtime state |
| `workspace.Status` | Read enriched local workspace status from state files |
| `workspace.ResultStatus` | Read status plus guest result output |
| `workspace.ArtifactsFor` | Read declared ingress and egress artifacts |
| `workspace.GetArtifact` | Copy a declared output artifact from a stopped workspace |
| `workspace.Copy` | Copy files between the host and a stopped workspace disk |
| `workspace.GuestRootfsLayerTar` | Export a stopped workspace's filesystem as an OCI layer tar via a guest maintenance boot (backends without host ext4 tooling) |
| `workspace.Clone` | Clone a stopped/prepared workspace |
| `workspace.ReadLogs` | Read a workspace serial log |
| `workspace.ReadEvents` | Read the recorded lifecycle event history |
| `workspace.ReadEgressAudit` | Read the egress mediator's recorded allow/deny/MITM/DNS/UDP decisions (`EgressEvent`, from `EgressAuditPath`) |
| `workspace.SampleStats` | Sample CPU, memory, and I/O for a running workspace |
| `workspace.Network` | Read configured and runtime network state |
| `workspace.List` | List named workspaces from local state |
| `workspace.Control` | Halt, quarantine, stop, kill, or delete a workspace |
| `workspace.Pause` / `workspace.Resume` | Freeze and thaw a running workspace's vCPUs in place |
| `workspace.Snapshot` | Capture a tagged memory-plus-disk snapshot of a running or paused workspace |
| `workspace.CreateFromSnapshot` | Fork a new workspace from another workspace's snapshot and resume it |
| `workspace.SnapshotList` / `workspace.SnapshotRemove` | List or delete a workspace's snapshots (host-side) |
| `workspace.Supervise` | Run the optional restart-policy loop for a workspace |
| `workspace.ReadManifest` / `workspace.WriteManifest` | Manage workspace manifests directly |

Installers and embedding programs that need to mirror packaged resolution can
use `workspace.FirecrackerSupervisorPathFromExecutable` to derive the Linux
supervisor companion path from a `bin/microagent` executable.

Console helpers return `workspace.WorkspaceNotFoundError` when the requested
workspace has no runtime or event state. Use `errors.Is` to classify missing
workspaces separately from stopped, halted, or quarantined workspaces.
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

Structured exec uses `workspace.Exec(ctx, opts, request)` with
`pkg/workspace/exec/protocol.ExecRequest`. A nil Go error means the host reached
the guest exec service and decoded a structured result; nonzero guest exit codes
remain in `ExecResult.ExitCode` (JSON tag `exit_code`) and are not Go errors.
Use `workspace.ExecWithMetadata` when callers need retry accounting via
`workspace.ExecRetryMetadata`. Transient transport failures are retried by the
shared exec layer before the final result or `workspace.ExecRetryExhaustedError`
is returned. `workspace.IsRetryableExecTransient` exposes the same classifier
for callers that need to reason about exec transport errors directly.
`readiness.execReady` uses the same protocol with a no-op command to verify the
service end-to-end.

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
| `microagent result` | `workspace.ResultStatus` |
| `microagent list` / `microagent ls` / `microagent ps` | `workspace.List` |
| `microagent halt` / `microagent quarantine` / `microagent stop` / `microagent kill` / `microagent delete` | `workspace.Control` (one function, action picked via options) |
| `microagent pause` / `microagent resume` | `workspace.Pause` / `workspace.Resume` |
| `microagent snapshot` create / list / delete | `workspace.Snapshot` / `workspace.SnapshotList` / `workspace.SnapshotRemove` |
| `microagent apply` | `workspace.Apply` |
| `microagent supervise` | `workspace.Supervise` |
| `microagent connect` | `workspace.DialConsole` / `SendConsoleCommand` (raw terminal mode stays CLI-only) |
| `microagent exec` | `workspace.Exec` |
| `microagent logs` | `workspace.ReadLogs` |
| `microagent events` | `workspace.ReadEvents` |
| `microagent egress` | `workspace.ReadEgressAudit` |
| `microagent stats` | `workspace.SampleStats` |
| `microagent cp` | `workspace.Copy` |
| `microagent clone` | `workspace.Clone` |
| `microagent commit` / `microagent image push` | `commit.Commit` / `commit.Push` |
| `microagent artifact` / `microagent artifact get` | `workspace.ArtifactsFor` / `workspace.GetArtifact` |
| `microagent network` | `workspace.Network` |
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
| `microagent model serve` | Alias for `microagent model serve` / `modelrunner.Ensure` |
| `microagent version` | CLI-only build metadata output |
| `microagent.yaml` (spec parsing) | `workspace.ReadSpec` / `ApplySpecFile` |

The library calls take options structs and return typed responses. For reusable
runtime behavior, prefer the package API. The remaining CLI-only commands are
presentation concerns such as `help`, `version`, and raw terminal mode around an
already-open console connection.
