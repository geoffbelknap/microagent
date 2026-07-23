---
title: Choosing microagent
description: An honest comparison with containers, raw Firecracker, Mac VM managers, and hosted agent sandboxes.
---

<!-- docs-last-updated -->
_Last updated: 2026-07-23_

microagent runs AI agent workspaces in microVMs. Each workspace gets its own
real Linux VM - its own kernel, its own disk, its own network - not a
shared-kernel container, booted from the same OCI images you already build.
This page is for deciding whether that is the right tool for your job. It walks
the neighboring options honestly, and each section ends with the case for
choosing the other tool, because most of them are good at something microagent
deliberately does not do.

Vendor facts on this page were verified against primary sources in July 2026;
this market moves quickly, so treat the dated specifics as a snapshot.

## vs. containers and sandboxed container runtimes

A plain container shares the host kernel. That is fine for most workloads and
wrong for the ones microagent targets: a kernel-level exploit in untrusted code
crosses the boundary. Two projects harden the container stack without giving up
its ergonomics.

- **gVisor** describes itself as an "Application Kernel for Containers." It
  slots in as an OCI runtime (`runsc`) under Docker, Kubernetes, and
  containerd, intercepting guest syscalls with a user-space kernel to defend
  against kernel-bug exploitation from untrusted userspace code. It is a
  sandbox layer, not a VM.
- **Kata Containers** bills itself as "the speed of containers, the security of
  VMs": lightweight VMs that run through the standard container runtime
  (containerd, CRI-O) via hypervisors including QEMU, Cloud-Hypervisor, and
  Firecracker.

Both are container-stack technologies, and both are actively maintained (gVisor
ships weekly; Kata Containers 4.0.0 landed July 2026). microagent gives each
workspace its own kernel the way Kata does, but it is a standalone workspace
substrate - OCI-to-rootfs conversion, lifecycle, egress mediation, structured
exec - not a runtime shim you plug into Kubernetes.

Choose gVisor or Kata Containers when you already run a Docker or Kubernetes
stack and want defense-in-depth isolation inside that stack, keeping the OCI and
CRI interfaces your platform is already built around. Sources:
[gVisor](https://gvisor.dev/), [Kata Containers](https://katacontainers.io/).

## vs. scripting Firecracker yourself

microagent boots on [Firecracker](https://firecracker-microvm.github.io) on
Linux, so a fair question is why not drive Firecracker directly. `firectl`, the
canonical CLI for raw Firecracker, calls itself "a basic command-line tool that
lets you run arbitrary Firecracker MicroVMs." To use it you supply a Firecracker
binary, an uncompressed `vmlinux` kernel, and a rootfs image yourself; there is
no OCI conversion, no kernel distribution, and no snapshot, lifecycle, or egress
tooling. Its last tagged release is v0.2.0 from October 2022, with only
dependency and CVE maintenance since - not archived, but not a growing platform.

That is the layer microagent fills. On top of raw Firecracker primitives it adds:

- **OCI-to-rootfs conversion** - it turns the container images you already build
  into bootable ext4 disks ([`microagent rootfs`](/cli/rootfs/)).
- **Kernel distribution and verification** - it fetches and verifies a pinned
  guest kernel instead of leaving you to build and track one
  ([`microagent kernel`](/cli/kernel/)).
- **Lifecycle** - create, start, halt, resume, and delete a named workspace
  whose disk survives between boots
  ([persistent workspaces](/guides/persistent-workspaces/)).
- **Readiness and structured results** - status and lifecycle events you can
  sequence work on, with typed results instead of scraped logs
  ([state and identity](/concepts/state-and-identity/)).
- **Egress mediation** - control and audit of what a workspace reaches on the
  network ([egress mediation](/concepts/egress-mediation/)).
- **Structured exec** - run a command and get exit code, stdout, and stderr
  back as typed data ([`microagent exec`](/cli/exec/)).

Firecracker's own design figures - roughly 125 ms to guest init, under 5 MiB
memory overhead per microVM - are real, but they are best-case numbers (serial
console off, minimal kernel and rootfs) and measure init start, not workload
readiness.

Choose raw Firecracker with `firectl` when you are building your own platform and
want the primitives without any opinions layered on top - and are prepared to
build the image, kernel, lifecycle, and egress layers yourself. Source:
[firectl](https://github.com/firecracker-microvm/firectl).

## vs. Mac VM managers

On macOS, tools like Lima (with Colima) and Tart already run Linux and macOS
VMs well, and they are worth using for what they are built for.

- **Lima** "launches Linux virtual machines with automatic file sharing and port
  forwarding (similar to WSL2)," aimed at running containerd, Docker, Podman, or
  Kubernetes on a Mac. It is a CNCF incubating project, actively maintained
  (Lima v2.2.0, July 2026); **Colima** is a container-runtime convenience layer
  on top of it (v0.10.3, June 2026).
- **Tart** is "a virtualization toolset to build, run and manage macOS and Linux
  virtual machines on Apple Silicon" using Apple's Virtualization.framework,
  aimed at CI/CD pipelines and reproducible local dev environments (v2.34.0,
  July 2026).

These are general-purpose VM managers, not agent-workspace products: none offers
per-workspace egress mediation and audit, conversion of arbitrary OCI
application images into bootable rootfs, or an agent-facing structured exec and
result API. microagent is deliberately not a general Mac VM manager - Lima,
Colima, and Tart already serve that space, and it does not try to displace them.

Choose Lima or Colima when you want a lightweight, scriptable Linux VM on macOS
(or Linux) with file-sharing and port-forwarding to run a container stack,
WSL2-style, and you do not need per-workload egress policy or an agent exec API.
Choose Tart when you need to build and manage VMs on Apple Silicon for CI/CD or
reproducible dev - especially when a real macOS VM is required. Sources:
[Lima](https://github.com/lima-vm/lima), [Tart](https://tart.run/).

## vs. hosted agent-sandbox services

A growing set of services run agent code for you: Modal, Daytona, E2B,
Cloudflare, Runloop, and Fly's Sprites, among others. They remove the operational
work of running microVMs yourself. Two things are worth knowing before you pick
one, and neither is a reason to avoid hosted services - they are trade-offs to
weigh against running your own substrate.

**"Hosted sandbox" does not mean microVM, and the boundary differs per vendor.**
Modal's security docs state its compute is "containerized and virtualized using
gVisor" - a user-space kernel, not a hypervisor. Daytona runs sandboxes "as
Linux containers by default" on the Sysbox runtime, with VM and Kata sandbox
classes only as opt-in. E2B says on its homepage that its sandboxes run on
Firecracker (its docs describe them only as on-demand Linux VMs). Cloudflare and
Runloop both claim per-sandbox VM isolation - Runloop's phrase is "microVM-level
hardware isolation between tenants" - without naming a hypervisor in their docs.
So "each agent gets its own microVM" is true for some of these and not others,
as of July 2026.

**In documented cases, workload data and credentials do transit the hosted
control plane.** These are primary-sourced facts, stated neutrally:

- Modal routes function inputs and outputs through its us-east servers by
  default and stores them on Modal infrastructure (encrypted at rest, deleted
  within about seven days); Modal commits to not accessing payloads without
  permission.
- Daytona disclosed CVE-2026-31431, in which API credentials passed via its
  CLI or SDK could be read from sandbox memory by anyone with shell access to
  the sandbox. It was disclosed and patched in April 2026, with no observed
  exploitation and the Sysbox isolation boundary not breached.
- Cloudflare's recommended credential pattern deliberately keeps real secrets
  out of the sandbox - a Worker proxy or outbound handler injects the real
  credential at request time - which means the secret lives in customer Worker
  code on Cloudflare-run infrastructure, just not inside the sandbox.

On price, the headline Linux compute rates have converged: E2B and Daytona both
list about $0.05/vCPU-hr, billed per second, with hard runtime caps in places
(E2B caps sandbox runtime at 1 hour on Hobby, 24 hours on Pro), as of July 2026.

Fly's **Sprites** are the fair exception to "hosted sandboxes are ephemeral":
each is a hardware-isolated VM with a 100 GB durable root filesystem and
checkpoint/restore in about one second, implemented as cheap metadata
operations - the closest hosted analog to microagent's stateful snapshot and
resume story. Those creation and restore figures are Fly's own demo numbers, not
independent benchmarks.

Choose a hosted service when you want zero-ops elastic scale, per-second billing
with no idle hardware, or a mature SDK ecosystem (E2B and Modal), and choose
Sprites when you want hosted plus stateful. Choose microagent when credential
custody dominates - it runs on your own hardware, so no secret or workload data
crosses a third-party plane, and it can keep the real secret out of the guest
entirely, holding values only in host process memory
([deliver secrets](/guides/secrets/)) - and when data locality, no runtime caps,
your own hardware economics, and auditability of every egress decision matter
more than offloading operations. Sources:
[Modal security](https://modal.com/docs/guide/security),
[Daytona trust center](https://trust.daytona.io/),
[E2B pricing](https://e2b.dev/pricing),
[Cloudflare sandbox security](https://developers.cloudflare.com/sandbox/concepts/security/),
[Runloop pricing](https://runloop.ai/pricing),
[Fly Sprites](https://fly.io/sprites).

## Where to go next

If microagent fits, the [quickstart](/getting-started/quickstart/) boots your
first microVM and runs a command inside it.
