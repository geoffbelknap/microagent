---
title: Go library
description: Use microagent packages directly from Go.
---

<!-- docs-last-updated -->
_Last updated: 2026-05-22_

*New to the library? Start with the [library overview](/library/) or the
[smallest useful Go program](/getting-started/library/first-program/). This
page is the package reference.*

## Use it as a generic microVM toolkit

The library doesn't require agent semantics. The same packages back the CLI's
agent-flavored workflows and any program that just wants microVMs: build a
rootfs from an OCI image, boot a VM, run a command, tear it down. The
high-level [`pkg/workspace`](#workspace-api) API treats every workspace as a
`workload`-role identity by default — you get caller-visible identity for
free without writing agent-aware code, and you can drop down to
[`pkg/vmkit`](#supervisor-types) when you need a different role or a custom
supervisor request.

## Exported packages

`microagent` has these exported Go packages today:

| Package | Purpose |
|---|---|
| `pkg/vmkit` | supervisor request/response types, validation, and executable supervisor client |
| `pkg/workspace` | workspace lifecycle API, options, defaults, request construction, backend supervisor selection, and backend-neutral helpers |
| `pkg/kernel` | kernel default manifest, install, verify, and support checks |
| `pkg/imagecache` | reusable rootfs image cache indexing, pull, tag, remove, and prune |
| `pkg/diagnostics` | backend host diagnostics and support summaries |
| `pkg/perf` | boot, footprint, and steady-state performance measurements |
| `pkg/rootfs` | OCI image and tar bundle conversion into ext4 disks |
| `pkg/supervisors/firecracker` | Linux Firecracker supervisor implementation |

The CLI is an adapter over these packages. Go callers should use the library
directly for workspace lifecycle operations instead of shelling out to
`microagent`.

## Public surface guard

The docs parity check treats these symbols as the public Go surface that should
stay visible in docs. Helper functions and backend plumbing can remain exported
for package boundaries without being promoted here, but new caller-facing
symbols should be added to this page when they are introduced.

| Package | Documented symbols |
|---|---|
| `pkg/vmkit` | `Request`, `Response`, `Config`, `Identity`, `Disk`, `NetworkConfig`, `PortForward`, `MediationConfig`, `VsockListener`, `RuntimeArtifacts`, `ArtifactRef`, `RuntimeResult`, `Event`, `VMState`, `StateUnknown`, `RoleWorkload`, `Supervisor`, `SupervisorClient`, `ExecutableSupervisor`, `NewRuntimeContract` |
| `pkg/workspace` | `Options`, `OptionsFromRequest`, `DefaultOptions`, `Spec`, `SpecApplyOptions`, `ApplyResult`, `Manifest`, `Result`, `GuestResult`, `CopyResult`, `ListEntry`, `SuperviseOptions`, `SuperviseResult`, `ConsoleOptions`, `ShellTarget`, `ConsoleReadTimeoutError`, `ConsoleCompletionUnknownError`, `ShellReadinessProbeMode`, `ShellReadinessProbeTCP`, `ShellReadinessSignalWithMode`, `ExecReadyProbeTimeout`, `ExecPort`, `ExecPortForName`, `ExecReadinessSignal`, `Create`, `Run`, `Start`, `Inspect`, `Status`, `ResultStatus`, `ArtifactsFor`, `GetArtifact`, `Copy`, `Clone`, `ReadLogs`, `Network`, `List`, `Control`, `Apply`, `Exec`, `DialConsole`, `SendConsoleCommand`, `ConsoleTarget`, `ProbeShellCommand`, `Supervise`, `ReadSpec`, `ApplySpec`, `ApplySpecFile`, `ReadManifest`, `WriteManifest`, `LookupProfile` |
| `pkg/kernel` | `InstallOptions`, `InstallResult`, `Install`, `VerifyOptions`, `VerifyResult`, `Verify`, `Default` |
| `pkg/imagecache` | `PullOptions`, `Record`, `PruneResult`, `Pull`, `Find`, `List`, `Tag`, `Remove`, `Prune`, `ReadIndex`, `FromProvenance` |
| `pkg/diagnostics` | `Options`, `Check` |
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
`github.com/geoffbelknap/microagent/pkg/supervisors/firecracker` directly:

```go
resp, err := firecrackersupervisor.Supervisor{}.Do(ctx, req)
```

For backend-independent code, depend on the interface:

```go
func inspect(ctx context.Context, supervisor vmkit.Supervisor, req vmkit.Request) (vmkit.Response, error) {
	req.Command = "inspect"
	return supervisor.Do(ctx, req)
}
```

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

## Workspace API

Use `pkg/workspace` when your program wants to create, run, start, inspect, and
control named workspaces without parsing CLI flags.

`workspace.DefaultOptions()` picks the host backend (Firecracker on Linux,
Apple Virtualization.framework on macOS), guest architecture, default kernel
path, and default state directory. You override only what your program needs.

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

`Result.Result` is a `*GuestResult` with `Stdout`, `Stderr`, and `ExitCode`
captured from the guest. `Result.Response` carries the supervisor's structured
response (state, identity, verification).

For non-defaults — backend override, custom kernel, sized memory/CPUs, networking
— set the matching `Options` fields before calling `Run`. The lifecycle API:

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
| `workspace.Clone` | Clone a stopped/prepared workspace |
| `workspace.ReadLogs` | Read a workspace serial log |
| `workspace.Network` | Read configured and runtime network state |
| `workspace.List` | List named workspaces from local state |
| `workspace.Control` | Halt, quarantine, stop, kill, or delete a workspace |
| `workspace.Supervise` | Run the optional restart-policy loop for a workspace |
| `workspace.ReadManifest` / `workspace.WriteManifest` | Manage workspace manifests directly |

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
remain in `ExecResult.exit_code` and are not Go errors. `readiness.execReady`
uses the same protocol with a no-op command to verify the service end-to-end.

## Kernel API

Use `pkg/kernel` when your program wants microagent to manage backend kernel
assets directly.

```go
result, err := kernel.Install(ctx, kernel.InstallOptions{
	Backend:      vmkit.BackendFirecracker,
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
	Backend: vmkit.BackendFirecracker,
	Arch:    "amd64",
})
if err != nil {
	// resp still contains structured support details when available.
}
_ = resp
```

The CLI contains presentation, flag parsing, build metadata output, and raw
terminal handling. microVM orchestration and management capabilities are exposed
through the Go packages, and the mapping below is checked by
`scripts/dev/docs-parity.py`.

## CLI ↔ library mapping

If you already know the CLI, this is the lookup for the equivalent library call:

| CLI command | Library call |
|---|---|
| `microagent run` | [`workspace.Run`](#workspace-api) |
| `microagent create` | `workspace.Create` |
| `microagent start` | `workspace.Start` |
| `microagent status` / `microagent inspect` | `workspace.Status` (local) / `workspace.Inspect` (live, via supervisor) |
| `microagent result` | `workspace.ResultStatus` |
| `microagent ps` | `workspace.List` |
| `microagent halt` / `microagent quarantine` / `microagent stop` / `microagent kill` / `microagent delete` / `microagent rm` | `workspace.Control` (one function, action picked via options) |
| `microagent apply` | `workspace.Apply` |
| `microagent supervise` | `workspace.Supervise` |
| `microagent connect` | `workspace.DialConsole` / `SendConsoleCommand` (raw terminal mode stays CLI-only) |
| `microagent logs` | `workspace.ReadLogs` |
| `microagent cp` | `workspace.Copy` |
| `microagent clone` | `workspace.Clone` |
| `microagent artifacts` / `microagent artifacts get` | `workspace.ArtifactsFor` / `workspace.GetArtifact` |
| `microagent network` | `workspace.Network` |
| `microagent doctor` / `microagent host` | [`diagnostics.Check`](#diagnostics-api) |
| `microagent contract` | `vmkit.Contract` |
| `microagent kernel install` / `microagent kernel verify` | [`kernel.Install`](#kernel-api) / `kernel.Verify` |
| `microagent rootfs build` | `rootfs.Builder.Build` |
| `microagent images` / `microagent prune` | [`imagecache.Pull`](#image-cache-api) / `List` / `Tag` / `Remove` / `Prune` |
| `microagent perf` / `microagent perf boot` / `microagent perf footprint` / `microagent perf steady` | `perf.Boot` / `Footprint` / `Steady` |
| `microagent profiles` | `workspace.Profiles` / `workspace.ProfileNames` |
| `microagent serve mcp` | CLI-only MCP stdio transport over the existing package APIs |
| `microagent version` | CLI-only build metadata output |
| `microagent.yaml` (spec parsing) | `workspace.ReadSpec` / `ApplySpecFile` |

The library calls take options structs and return typed responses. For reusable
runtime behavior, prefer the package API. The remaining CLI-only surfaces are
presentation concerns such as `help`, `version`, and raw terminal mode around an
already-open console connection.
