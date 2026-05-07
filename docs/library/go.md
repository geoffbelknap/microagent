---
title: Go library
description: Use microagent-kit packages directly from Go.
---

`microagent-kit` has these exported Go packages today:

| Package | Purpose |
|---|---|
| `pkg/vmkit` | supervisor request/response types, validation, and executable supervisor client |
| `pkg/workspace` | workspace lifecycle API, options, defaults, request construction, backend supervisor selection, and backend-neutral helpers |
| `pkg/kernel` | kernel default manifest, install, verify, and support checks |
| `pkg/imagecache` | reusable rootfs image cache indexing, pull, tag, remove, and prune |
| `pkg/diagnostics` | backend host diagnostics and support summaries |
| `pkg/rootfs` | OCI image and tar bundle conversion into ext4 disks |
| `pkg/supervisors/firecracker` | Linux Firecracker supervisor implementation |

The CLI is an adapter over these packages. Go callers should use the library
directly for workspace lifecycle operations instead of shelling out to
`microagent`.

## Supervisor Types

Use `pkg/vmkit` when you need the shared request/response schema or want to
call an executable supervisor.

```go
package main

import (
	"context"
	"fmt"

	"github.com/geoffbelknap/microagent-kit/pkg/vmkit"
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
`github.com/geoffbelknap/microagent-kit/pkg/supervisors/firecracker` directly:

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

## Rootfs Builder

Use `pkg/rootfs` when your program needs to build a VM rootfs from an OCI image
without shelling out to `microagent rootfs`.

```go
package main

import (
	"context"

	"github.com/geoffbelknap/microagent-kit/pkg/rootfs"
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

```go
package main

import (
	"context"

	"github.com/geoffbelknap/microagent-kit/pkg/vmkit"
	"github.com/geoffbelknap/microagent-kit/pkg/workspace"
)

func main() {
	opts := workspace.DefaultOptions()
	opts.Name = "agency-task-1"
	opts.ImageRef = "docker.io/library/ubuntu@sha256:..."
	opts.Backend = vmkit.BackendFirecracker
	opts.KernelPath = "/home/me/.microagent/kernels/firecracker/amd64/Image"
	opts.StateDir = "/home/me/.microagent"
	opts.MemoryMiB = 2048
	opts.CPUCount = 2
	opts.ExecCommand = "printf hello"

	result, err := workspace.Run(context.Background(), opts)
	if err != nil {
		panic(err)
	}
	_ = result.Response
}
```

The lifecycle API includes:

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

## Image Cache API

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

The CLI contains presentation, flag parsing, and terminal-oriented behavior.
MicroVM orchestration and management capabilities are exposed through the Go
packages.
