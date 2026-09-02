---
title: Run microagent from a Go program
description: Boot a microVM, run a command, and tear it down - in a few lines of Go.
---

<!-- docs-last-updated -->
_Last updated: 2026-07-25_

*If you'd rather drive microagent from the command line, see the [quickstart](../quickstart.md) instead.*

The project is a Go library; the CLI is a thin shell over it. This page
shows the smallest useful program: it boots a Linux microVM, runs a command
inside it, captures the output, and tears the VM down.

The library does not require agent semantics. The same code is all you need
whether you're building an agent runtime on top, or just want microVMs you can
script from Go.

## Prerequisites

1. [Install the CLI](../install.md) - the library and the CLI ship
   together. The library also finds its companion binaries (the supervisor and
   guest init) next to the installed `microagent` on your `PATH` - see
   [companion binary resolution](../../library/go.md#companion-binary-resolution).
2. Run `microagent doctor` to confirm the host can boot microVMs.
3. Install the default kernel so the library can find it on disk:

   ```bash
   microagent kernel install
   ```

   You only need to do this once per host. (`microagent run` from the CLI
   installs it lazily; library callers ask for it explicitly.)

## A whole program

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

	res, err := workspace.Run(context.Background(), opts)
	if err != nil {
		panic(err)
	}
	if res.Result != nil {
		fmt.Print(res.Result.Stdout)
	}
}
```

`workspace.DefaultOptions()` picks the host backend (Firecracker on Linux,
Apple Virtualization.framework on macOS), the
guest architecture, the default kernel path, and the default state directory.
You override only what your program needs to set - here, the workspace name,
the OCI image, and the command to run.

`workspace.Run` builds the rootfs from the image, boots the VM, runs the
command, captures the result, and - when the run *succeeds* - removes the
scratch state. A failed run keeps its state under
`~/.microagent/workspaces/demo-vm/` for debugging, and re-running with the
same fixed name will then collide. The two-line fix:

```go
// After a failed run: delete the leftover state, or use a fresh name.
_, _ = workspace.Control(context.Background(), opts, "delete")
```

See the [Run lifecycle contract](../../library/go.md#run-lifecycle-contract) for the
full story (timeouts, `Keep`, cleanup rules).

`workspace.Run` returns a `workspace.Result`, whose nested `Result` field (a `*GuestResult`)
holds the guest's output - so `res.Result.Stdout` contains the guest's stdout
and `res.Result.ExitCode` carries its exit code. The doubled name is just the
outer `workspace.Result` struct's `Result` field, not a typo.

## What just happened

1. The library resolved the installed host backend, architecture, and kernel for
   this host.
2. It pulled the OCI image and converted it to an ext4 rootfs.
3. It booted the VM, ran your command, captured stdout/stderr/exit code into
   `res.Result`.
4. It shut the VM down and, because the run succeeded (and `opts.Keep` was not
   set), removed the scratch state directory. A failed run would have left the
   state behind for inspection.

## Where to next

- Keep a workspace around between runs and inspect it as it lives - see the
  [`workspace.Create`, `Start`, `Inspect`, `Control`](../../library/go.md) functions.
- Build a rootfs without booting anything - `pkg/rootfs`.
- Talk to the lower-level supervisor interface without going through
  `pkg/workspace` - see [`pkg/vmkit`](../../library/go.md#supervisor-types).
- Already agent-flavored: the library treats every workspace as a
  `workload`-role identity by default. `opts.Name` becomes the `RuntimeID`
  carried in requests, state files, and events. For enforcement-role
  components or custom requests, build a `vmkit.Request` directly -
  see [`pkg/vmkit`](../../library/go.md#supervisor-types).

For the full set of exported packages and CLI ↔ library mapping, see the
[Go library reference](../../library/go.md).
