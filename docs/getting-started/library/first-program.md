---
title: Run microagent from a Go program
description: Boot a microVM, run a command, and tear it down - in a few lines of Go.
---

<!-- docs-last-updated -->
_Last updated: 2026-06-01_

*If you'd rather drive microagent from the command line, see [run your first microVM](/getting-started/cli/first-microvm/) instead.*

The project is a Go library; the CLI is a thin shell over it. This page
shows the smallest useful program: it boots a Linux microVM, runs a command
inside it, captures the output, and tears the VM down.

The library does not require agent semantics. The same code is all you need
whether you're building an agent runtime on top, or just want microVMs you can
script from Go.

## Prerequisites

1. [Install the CLI](/getting-started/install/) - the library and the CLI ship
   together.
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
Apple Virtualization.framework on macOS), the guest architecture, the default
kernel path, and the default state directory. You override only what your
program needs to set - here, the workspace name, the OCI image, and the
command to run.

`workspace.Run` builds the rootfs from the image, boots the VM, runs the
command, captures the result, and removes the scratch state when it's done.
The returned `Result.Result.Stdout` contains the guest's stdout;
`Result.Result.ExitCode` carries its exit code.

## What just happened

1. The library resolved the installed host backend, architecture, and kernel for
   this host.
2. It pulled the OCI image and converted it to an ext4 rootfs.
3. It booted the VM, ran your command, captured stdout/stderr/exit code into
   `Result.Result`.
4. It shut the VM down and removed the scratch state directory.

## Where to next

- Keep a workspace around between runs and inspect it as it lives - see the
  [`workspace.Create`, `Start`, `Inspect`, `Control`](/library/go/) functions.
- Build a rootfs without booting anything - `pkg/rootfs`.
- Talk to a supervisor directly without going through `pkg/workspace` - see
  [`pkg/vmkit`](/library/go/#supervisor-types) and the
  [supervisor protocol](/protocol/).
- Already agent-flavored: the library treats every workspace as a
  `workload`-role identity by default. `opts.Name` becomes the `RuntimeID`
  carried in requests, state files, and events. For enforcement-role
  components or custom requests, build a `vmkit.Request` directly -
  see [`pkg/vmkit`](/library/go/#supervisor-types).

For the full set of exported packages and CLI ↔ library mapping, see the
[Go library reference](/library/go/).
