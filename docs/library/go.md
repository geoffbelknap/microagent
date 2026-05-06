---
title: Go library
description: Use microagent-kit packages directly from Go.
---

`microagent-kit` has these exported Go packages today:

| Package | Purpose |
|---|---|
| `pkg/vmkit` | supervisor request/response types, validation, and executable supervisor client |
| `pkg/rootfs` | OCI image and tar bundle conversion into ext4 disks |
| `pkg/supervisors/firecracker` | Linux Firecracker supervisor implementation |

The CLI still owns the workspace orchestration code under `cmd/microagent`.
The full high-level `run`, `create`, `start`, `stop`, and `delete` workflow is
not exported as a stable Go package API yet.

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

## Still CLI-Only

The intended architecture is a reusable Go library plus CLI. The exported
library already covers rootfs builds, supervisor data types, and the
Firecracker supervisor. The complete high-level workspace API still needs to
move out of `cmd/microagent` before Go callers can drive both backends with the
same convenience API the CLI uses.
