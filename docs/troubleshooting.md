---
title: Troubleshooting
description: Find the failure you're seeing and fix it with the right tool.
---

<!-- docs-last-updated -->
_Last updated: 2026-07-13_

When something isn't working, **start with `microagent doctor`**. It checks the host backend, virtualization support, the supervisor binary, the default kernel, and console support, and tells you where the gap is. Most of the entries below are conditions doctor will flag.

Each symptom type has a tool that answers it fastest: host problems (missing KVM, binaries, permissions) are [`doctor`](/cli/doctor/)'s job; boot problems and anything the guest printed are in [`logs`](/cli/logs/). Questions about what state a workspace is in belong to [`status`](/cli/status/), and when you need the history of how it got there, read [`events`](/cli/events/).

This page is indexed by symptom - search for whatever you're seeing.

## Host setup

### `microagent doctor` reports KVM unavailable on Linux

The host doesn't have `/dev/kvm`, or the current process can't see it. KVM is required for Firecracker; without it, the workspace can't boot.

Common causes and fixes:

- **No virtualization support.** You're on a host without hardware virtualization (some VMs, some cloud instances). Move to a host with KVM-capable virtualization (most bare metal, most VPS providers, nested virtualization on supported hypervisors).
- **`kvm` kernel module not loaded.** `lsmod | grep kvm`. Load with `sudo modprobe kvm` (plus `kvm_intel` or `kvm_amd` for your CPU vendor).
- **User not in `kvm` group.** `ls -l /dev/kvm` should show group `kvm`. Add your user with `sudo usermod -aG kvm $USER` and log back in.
- **Sandboxed shell masking the device.** Some agent environments and container shells hide `/dev/kvm` from the process. Run `microagent` directly on the host, outside any sandboxed wrapper.

### `microagent doctor` reports KVM available, but `start` fails with permission denied

Your user can see `/dev/kvm` but can't open it. Same `kvm` group fix as above. Verify with `cat /dev/kvm` - if you get "Operation not permitted" it's a permissions problem; if you get "Invalid argument" you're fine (kernel just rejects raw reads).

### `firecracker` binary not found (Linux)

Homebrew and `make install` install the pinned upstream Firecracker VMM under
the microagent prefix as `libexec/firecracker`. If `doctor` cannot find it,
reinstall microagent or install Firecracker from
[its releases](https://github.com/firecracker-microvm/firecracker/releases) and
put it on `PATH`. Alternatively:

- Install under `<prefix>/libexec/firecracker` (Homebrew puts it there automatically).
- Set `MICROAGENT_FIRECRACKER=/path/to/firecracker` in your environment.

### `mke2fs` not found (rootfs builds fail)

The rootfs builder needs `mke2fs` to format ext4 disks. On Linux it's usually installed (`e2fsprogs`); on macOS it isn't shipped by default.

```bash
# macOS
brew install e2fsprogs
# Pass the path explicitly the first time:
microagent rootfs build --mke2fs /opt/homebrew/opt/e2fsprogs/sbin/mke2fs ...
```

Any `microagent rootfs build` or `microagent run` that uses non-default sizing accepts `--mke2fs <path>`.

### Default kernel not installed

First `microagent run` installs the default kernel automatically. If you'd rather do it explicitly:

```bash
microagent kernel install
```

For a custom kernel or air-gapped install, see [`microagent kernel`](/cli/kernel/).

### `microagent kernel verify` reports a SHA mismatch

The kernel file on disk doesn't match its expected SHA-256. Either it's corrupted or someone replaced it. Don't ignore a mismatch - never boot from a kernel you don't trust.

```bash
microagent kernel install   # reinstall from the trusted source
microagent kernel verify --path ~/.microagent/kernels/<backend>/<arch>/Image \
                         --sha256 <expected>
```

If `install` doesn't produce the expected SHA either, the source you're pulling from is wrong. Reinstall from a trusted kernel URL and pass the expected `--sha256` explicitly.

### `microagent doctor --backend windows-hyperv` reports HCS access denied

Windows Hyper-V uses Windows Host Compute Service directly. The current user
must be able to create and manage HCS compute systems.

Common fixes:

- Run from an elevated shell.
- Add the user to the local **Hyper-V Administrators** group, then sign out and
  back in so the new token has the group.
- Confirm Windows features for Hyper-V and Windows Hypervisor Platform are
  enabled.

### `microagent doctor --backend windows-hyperv` reports HCN/HNS unavailable

The Windows networking service used by Hyper-V is not reachable. Start with the
Windows services for Host Network Service and Host Compute Service, then rerun:

```powershell
microagent --json doctor --backend windows-hyperv
```

Published TCP networking, mediation, and guest-to-host listener targets use
Hyper-V sockets. If HCN/HNS is unavailable, Windows Hyper-V fails closed before
attaching a network endpoint.

## Workspace lifecycle

### `microagent delete` asks "Stop and delete it?"

The recorded VM process is still alive. `delete` won't tear it down silently -
it prompts first, and `--yes` (or `--force`) answers the prompt and stops the
VM for you before deleting.

```bash
microagent delete <name> --yes   # stop the running VM, then delete

# or shut it down yourself first:
microagent halt <name>           # clean disk-preserving stop
microagent delete <name>
```

### `microagent start` fails because the workspace is `quarantined`

Quarantined workspaces preserve disk and event history while host-side network and mediation paths are severed. `start` refuses them until you move the workspace out of the state:

```bash
microagent halt <name>     # or stop / kill
microagent start <name>    # boots the preserved disk back up
```

See [glossary](/concepts/glossary/) for the full lifecycle vocabulary.

### Workspace boots but the entrypoint exits immediately

Look at the serial log:

```bash
microagent logs <name>
```

Usual causes:

- **`files:` source missing.** Check the `microagent.yaml` `files:` paths - they're resolved relative to the spec file's directory. A typo or wrong relative path means the file never lands in the rootfs.
- **Entrypoint references a path that wasn't created.** `setup:` runs first; `mkdir -p` your target directories there if they don't exist in the base image.
- **Missing dependency.** If `setup:` did a `pip install` and a dependency is wrong, the entrypoint sees `ImportError`. The serial log shows the Python traceback.
- **Wrong shebang or interpreter.** A shell script entrypoint without `#!/bin/sh` and without an explicit `bash`/`sh` wrapper won't execute.

### `microagent --json status` shows `verification.divergence`

Recorded hashes don't match what's currently on disk. Treat kernel or injected-init divergence as suspicious until you understand it. Rootfs hashes are enforced while the workspace is still `prepared`; after a workspace has started, the rootfs is the writable VM disk and normal guest boot activity can change it.

```bash
microagent --json status <name> | jq '.verification.divergence'
```

The `field` and `expected` / `actual` values tell you which artifact diverged. Common case: someone reinstalled the kernel without re-creating the workspace, so the workspace still references the old kernel hash.

For repeatable deployments, prefer digest-pinned image refs such as
`docker.io/library/python@sha256:...`, install kernels with an explicit
`--sha256`, and check `.verification.ok` before `start`.

## Networking

### Firecracker `user` mode workspace won't start

`user` mode needs three things:

- `pasta` on `PATH` (from the `passt` package - `apt install passt` on Debian/Ubuntu, `dnf install passt` on Fedora; Homebrew installs it as a microagent dependency).
- Unprivileged user namespaces enabled. Check `sysctl user.max_user_namespaces` (returns a non-zero count when enabled). Some distros also gate this via `kernel.unprivileged_userns_clone` - set both to `1` if either is `0`. On Ubuntu 23.10+ this is additionally restricted by AppArmor - see below.
- `/dev/net/tun` readable by the calling user.

`microagent doctor` reports each of these - start there to find the missing piece.

If your host doesn't allow unprivileged user namespaces and you can't change that policy, use `--network isolated` when the guest does not need network access.

### Ubuntu 24.04: `write failed /proc/self/uid_map: Operation not permitted`

Ubuntu 23.10+ (including stock Ubuntu 24.04 cloud images and GitHub-hosted
runners) ships `kernel.apparmor_restrict_unprivileged_userns=1`. Under this
restriction, creating a user namespace still *succeeds*, but AppArmor confines
the process that created it so its own uid-map write is denied. The classic
sysctls look permissive, yet every rootless workspace boot fails. Symptoms:

- The workspace serial log shows the supervisor's user-namespace jail dying
  with `unshare: write failed /proc/self/uid_map: Operation not permitted`.
- `pasta` (user-mode networking) fails with
  `Couldn't write to /proc/self/uid_map: Operation not permitted`.

`microagent doctor` detects this: its user-namespace probe performs the same
self-written uid-map setup the supervisor jail and `pasta` use, so a host under
this restriction reports `userNamespacesAvailable: false` with the AppArmor
remediation instead of a false `ok`.

Fixes (either one):

- **Turn off the restriction** (simplest; this is a policy knob, not a kernel
  rebuild):

  ```bash
  sudo sysctl -w kernel.apparmor_restrict_unprivileged_userns=0
  # persist across reboots:
  printf 'kernel.apparmor_restrict_unprivileged_userns = 0\n' | sudo tee /etc/sysctl.d/99-microagent-userns.conf
  ```

- **Keep the restriction, grant a targeted exception.** The jail enters its
  namespace through `unshare(1)` and user networking through `pasta`, so those
  binaries need an AppArmor profile with the `userns` permission. For example,
  for `unshare`:

  ```text
  # /etc/apparmor.d/microagent-unshare
  abi <abi/4.0>,
  include <tunables/global>
  profile microagent-unshare /usr/bin/unshare flags=(unconfined) {
    userns,
  }
  ```

  Load it with `sudo apparmor_parser -r /etc/apparmor.d/microagent-unshare`
  (and mirror it for `/usr/bin/pasta` if you use `--network user`). Re-run
  `microagent doctor` to confirm the probe passes.

Use `microagent --json network <name>` to inspect the runtime IP, subnet,
gateway, DNS, and route that were assigned to the guest.

### Required mediation channel fails closed

```text
error: mediation channel required but unreachable
```

The workspace declared a mediation channel as required (the default) but the host listener isn't reachable. The workspace starts, but readiness reports a `mediationReady` error and the channel is severed until a listener connects - no traffic can flow until then.

Fixes:

- **Stand up the mediation listener** at the declared `host:port` before `microagent start`.
- **For development only**, pass `--mediation-optional` on `microagent create` to allow startup without the channel. Don't ship this - the channel is fail-closed for a reason ([security](/security/)).

## Egress mediation

### A mediated (`broker` or `mitm`) workspace fails to start with a TPROXY error

```text
egress: UDP mediation (TPROXY) unavailable for workspace research — load the TPROXY kernel modules or use --egress off
```

[Egress mediation](/concepts/egress-mediation/) runs inside the workspace's own
user namespace and mediates UDP and DNS via Linux TPROXY, which needs kernel
modules (`nft_tproxy`, `nf_tproxy_ipv4`, `xt_socket`, `nf_socket_ipv4`) that a
rootless workspace can't load itself. When they're missing, a mediated
workspace (the default `broker` mode, or `mitm`) **fails closed** - it refuses
to start rather than run with an unmediated UDP/DNS channel.

Fixes:

- **Load the kernel modules once, as root:**
  `sudo modprobe nft_tproxy nf_tproxy_ipv4 xt_socket nf_socket_ipv4`. Once the
  modules are present the workspace's netns can install its own TPROXY rules.
  `microagent doctor` reports whether they're in place.
- **Drop mediation** if you genuinely don't want it: `--egress off`.

This is the intended fail-closed behavior - an enforcement gap never silently
widens what the agent can reach. See [`doctor`](/cli/doctor/).

### An allowed host's TLS connection fails under `mitm`

In `--egress mitm` mode, a destination you allowlisted (`--egress-allow`)
still fails its TLS handshake - typically a client-side certificate error in
the guest, or an `egress_mitm_handshake_error` / `egress_mitm_upstream_error`
record in `microagent egress <name>`. (The default `broker` mode never forges
certificates, so this symptom only appears when you opted into `mitm`.)

Cause: in `mitm` mode microagent **intercepts TLS** with a per-workspace CA.
Some clients reject the injected CA's leaf certificate:

- **Certificate pinning** - the client only trusts a specific certificate or key,
  not the per-workspace CA.
- **Mutual TLS** - the upstream demands a client certificate the mediator can't
  present.
- **A client with its own root store** - it ignores the CA microagent installed
  in the system trust store, so the mediator's leaf is untrusted.

Fix: mark the host **passthrough** so it is allowed and audited but **not
intercepted** - the original server certificate reaches the client untouched:

```bash
microagent create research --egress mitm \
  --egress-allow api.openai.com \
  --egress-passthrough pinned.example.com
```

The trade-off: a passthrough connection is forwarded as an opaque L4 byte stream,
so microagent records *that* the connection happened (and how much data crossed
it) but **cannot inspect the payload.** You're trading content visibility for
compatibility. See [allow vs passthrough](/concepts/egress-mediation/#allow-vs-passthrough)
for the full discussion and [`microagent egress`](/cli/egress/) for the audit
records.

## Console

### `microagent connect` hangs waiting for a shell prompt

By default, `connect` waits for the guest shell to be ready before attaching or sending input. If the guest hasn't reached its shell (still booting, entrypoint hasn't returned to a shell, or never will), it waits.

Fixes:

- **Look at the serial log** to see what the guest is doing: `microagent logs <name>`.
- **Disable the wait** if you know the workspace doesn't expose an interactive shell: `microagent connect <name> --ready-timeout 0`. You'll see raw serial output without the readiness gate.
- **Check workspace state** with `microagent --json status <name>` - if it's `failed`, the guest never reached a usable state and connect won't help.

### `microagent connect` says "console input is unavailable"

The backend's console capability is fine but this specific workspace doesn't have a console input endpoint yet. Common causes:

- The workspace is `prepared` but not `running`. Start it first.
- The backend created the runtime files but the serial input FIFO hasn't appeared yet (race window during start). Wait a moment and retry.

## Image and rootfs

### `microagent rootfs build` rejects a mutable tag

```text
error: image reference is mutable, pass --allow-mutable to override
```

`rootfs build` defaults to refusing tag references (e.g. `ubuntu:24.04`) because they're not reproducible - the same tag can resolve to different content tomorrow. Two paths:

- **Pin by digest** (recommended for production):
  `microagent rootfs build --image docker.io/library/ubuntu@sha256:...`
- **Override** (development only): pass `--allow-mutable`.

`microagent create` and `microagent run` are looser - they accept tags by default and record the resolved digest in the workspace's verification record. See [security](/security/) for the trust-boundary discussion.

### The image doesn't fit the workspace disk

```text
rootfs contents need about 1100 MiB but the rootfs disk size is 1024 MiB; give the workspace a larger disk, for example --profile medium or --size 2048
```

The unpacked image is bigger than the workspace's disk. The default `small` profile allocates a 1024 MiB disk, and some popular images (for example `docker.io/library/python:3.12`, about 1 GiB unpacked) don't fit. Any of these fixes work:

- **Bigger profile:** `--profile medium` (8 GiB disk).
- **Explicit size:** `--size 2048` (MiB).
- **Smaller image:** most language images ship a `-slim` variant, e.g. `python:3.12-slim`.

Older releases surface this as a raw `mke2fs` failure: `build ext4 rootfs: ... Could not allocate block in ext2 filesystem`. Same cause, same fixes.

### `microagent image pull` is slow or fails

- **Slow:** the OCI registry is slow, or the layers are large. Look at the registry/network rather than microagent.
- **Disk space:** pulls land under `~/.microagent/images/`. Check disk space; prune old records with `microagent image prune --delete`.

## Still stuck?

- Run `microagent --json doctor` for the full host capability report.
- Run `microagent --json status <name>` for the workspace state plus verification details.
- Check `microagent logs <name>` for serial output - most guest-side issues appear there.
- File an issue at [github.com/geoffbelknap/microagent/issues](https://github.com/geoffbelknap/microagent/issues) with the doctor output and the failing command.
