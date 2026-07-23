---
title: Limitations
description: What microagent deliberately doesn't do, and where to go instead.
---

<!-- docs-last-updated -->
_Last updated: 2026-07-23_

Some of these look like missing features. They're refusals: each one keeps a
real microVM boundary honest instead of faking a container-engine behavior
that doesn't map to it. See
[Coming from Docker: what's intentionally different](/getting-started/coming-from-docker/#whats-intentionally-different)
for the fuller version of several of these.

## No host directory bind mounts

There is no `-v /host/dir:/guest/dir`. Everything the guest reads or writes is
a block device - the rootfs or an attached ext4 disk - so the guest never
shares a live host filesystem. Package a directory as a tar bundle for
ingress, attach an ext4 disk, or use `microagent cp` against a stopped
workspace for file transfer. See [Storage](/concepts/storage/) and
[Coming from Docker: no bind mounts](/getting-started/coming-from-docker/#no-bind-mounts).

## No `--privileged`

The guest already has its own kernel and full root inside the microVM, so
there's no privileged mode to escalate to - host access stays on the host.
See [Coming from Docker: no --privileged](/getting-started/coming-from-docker/#no---privileged).

## No compose projects, pods, or container-engine API

microagent isn't a container engine. Compose projects, pods, privileged mode,
namespace flags, devices, and host bind mounts fail with targeted guidance
instead of being silently translated into microVM behavior. Run one image at
a time with `run`/`create`; script coordination across multiple workspaces in
your own tooling. See [`microagent run`](/cli/run/#docker-style-conveniences).

## Windows Hyper-V is experimental, not a supported-parity target

Linux and macOS are the supported release targets. Windows Hyper-V is useful
for evaluation on Windows hosts, but it isn't held to the same parity bar and
isn't a release gate. See
[Platform support](/concepts/platform-support/) and
[Host requirements: Windows Hyper-V](/concepts/backends/#windows-hyper-v).

## Intel Macs aren't supported

macOS support requires Apple silicon and Apple Virtualization.framework -
there's no Intel Mac backend. See
[Host requirements: macOS](/concepts/backends/#macos).

## Named volumes are single-attach, not concurrently shared

A named volume is a managed ext4 disk with a lifecycle independent of any one
workspace, but at most one running workspace holds it at a time - two VMs
never mount the same volume read-write. This is the microVM analog of a
container volume, not the Docker model of a daemon-managed, driver-based,
concurrently shared volume. Hand data between workspaces by writing to the
volume in one and attaching it to the next. See
[Storage: named volumes](/concepts/storage/#named-volumes).

## No image build command

microagent doesn't build images. Build with the tooling you already use
(Docker, Buildah, your CI) and point `microagent run`/`rootfs build` at the
result - it consumes standard OCI images. To capture changes a workspace made
to its rootfs, [`microagent commit`](/cli/commit/) snapshots a stopped
workspace back into an OCI image. See
[Coming from Docker: no build command](/getting-started/coming-from-docker/#no-build-command).

## See also

- [FAQ](/getting-started/faq/) - short answers to common questions
- [Boundaries](/concepts/boundaries/) - what microagent owns versus what your runtime supplies
- [Coming from Docker](/getting-started/coming-from-docker/) - the full command map plus what's different
