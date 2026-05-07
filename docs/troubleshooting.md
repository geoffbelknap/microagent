---
title: Troubleshooting
description: Common failure modes, what they mean, and how to fix them.
---

When something isn't working, **start with `microagent doctor`**. It checks the host backend, virtualization support, the supervisor binary, the default kernel, and the console surface, and tells you where the gap is. Most of the entries below are conditions doctor will flag.

This page is indexed by symptom — search for whatever you're seeing.

## Host setup

### `microagent doctor` reports KVM unavailable on Linux

The host doesn't have `/dev/kvm`, or the current process can't see it. KVM is required for Firecracker; without it, the workspace can't boot.

Common causes and fixes:

- **No virtualization support.** You're on a host without hardware virtualization (some VMs, some cloud instances). Move to a host with KVM-capable virtualization (most bare metal, most VPS providers, nested virtualization on supported hypervisors).
- **`kvm` kernel module not loaded.** `lsmod | grep kvm`. Load with `sudo modprobe kvm` (plus `kvm_intel` or `kvm_amd` for your CPU vendor).
- **User not in `kvm` group.** `ls -l /dev/kvm` should show group `kvm`. Add your user with `sudo usermod -aG kvm $USER` and log back in.
- **Sandboxed shell masking the device.** Some agent environments and container shells hide `/dev/kvm` from the process. Run `microagent` directly on the host, outside any sandboxed wrapper.

### `microagent doctor` reports KVM available, but `start` fails with permission denied

Your user can see `/dev/kvm` but can't open it. Same `kvm` group fix as above. Verify with `cat /dev/kvm` — if you get "Operation not permitted" it's a permissions problem; if you get "Invalid argument" you're fine (kernel just rejects raw reads).

### `firecracker` binary not found (Linux)

Install Firecracker from [its releases](https://github.com/firecracker-microvm/firecracker/releases) and put it on `PATH`. Alternatively:

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

For a custom kernel or air-gapped install, see [`microagent kernel`](cli/kernel.md).

### `microagent kernel verify` reports a SHA mismatch

The kernel file on disk doesn't match its expected SHA-256. Either it's corrupted or someone replaced it. Don't ignore this — booting from a kernel you don't trust is the textbook supply-chain failure.

```bash
microagent kernel install   # reinstall from the trusted source
microagent kernel verify --path ~/.microagent/kernels/<backend>/<arch>/Image \
                         --sha256 <expected>
```

If `install` doesn't produce the expected SHA either, the source you're pulling from is wrong — see the [release notes](releases/index.md) for the SHA expected for your microagent-kit version.

## Workspace lifecycle

### `microagent delete` refuses while the VM is running (Firecracker)

The recorded VM process is still alive. By design, `delete` won't tear down a workspace whose VM hasn't been stopped — it'd orphan the process.

```bash
microagent halt <name>     # clean disk-preserving stop
# or
microagent stop <name>     # graceful (SIGTERM)
microagent kill <name>     # hard (SIGKILL) if stop doesn't return
microagent delete <name>
```

### `microagent start` fails because the workspace is `quarantined`

Quarantined workspaces preserve disk and event history while host-side network and mediation paths are severed. They can't be `start`ed directly — that's the whole point of the state.

```bash
microagent halt <name>     # or stop / kill
microagent start <name>    # boots the preserved disk back up
```

See [glossary](concepts/glossary.md) for the full lifecycle vocabulary.

### Workspace boots but the entrypoint exits immediately

Look at the serial log:

```bash
microagent logs <name>
```

Usual causes:

- **`files:` source missing.** Check the `microagent.yaml` `files:` paths — they're resolved relative to the spec file's directory. A typo or wrong relative path means the file never lands in the rootfs.
- **Entrypoint references a path that wasn't created.** `setup:` runs first; `mkdir -p` your target directories there if they don't exist in the base image.
- **Missing dependency.** If `setup:` did a `pip install` and a dependency is wrong, the entrypoint sees `ImportError`. The serial log shows the Python traceback.
- **Wrong shebang or interpreter.** A shell script entrypoint without `#!/bin/sh` and without an explicit `bash`/`sh` wrapper won't execute.

### `microagent --json status` shows `verification.divergence`

Recorded hashes (image digest, kernel SHA, rootfs SHA, injected init SHA) don't match what's currently on disk. Something changed under the workspace's feet — investigate before you `start`.

```bash
microagent --json status <name> | jq '.verification.divergence'
```

The `field` and `expected` / `actual` values tell you which artifact diverged. Common case: someone reinstalled the kernel without re-creating the workspace, so the workspace still references the old kernel hash.

## Networking

### Apple VF `bridged` mode fails closed before start

```text
error: networking entitlement (com.apple.vm.networking) required for bridged mode
```

Apple gates native bridged networking behind the restricted `com.apple.vm.networking` entitlement. Open-source builds can't self-sign it, and `sudo` doesn't bypass the check.

Fixes:

- **Use a different mode.** `nat` and `isolated` work without the entitlement. Most agent workloads only need `nat` for outbound traffic and `--publish` for inbound TCP. Bridged is for cases where the workspace needs its own L2 presence on the host network.
- **Sign the supervisor with the entitlement.** Possible if you have an Apple Developer account with the right capabilities. Out of scope for this project; consult Apple's docs.

### Firecracker `bridged` fails with "host-prerequisite-not-configured"

Linux bridged mode needs:

- `iproute2` installed (`ip` command available)
- The named host interface to be an existing Linux bridge
- Permission to create and attach a TAP device (typically `CAP_NET_ADMIN` or root)

Fixes:

- `sudo apt install iproute2` (or your distro's equivalent)
- Create a bridge if you don't have one: `sudo ip link add br0 type bridge && sudo ip link set br0 up`
- Run with sufficient privileges, or grant the supervisor capabilities via `setcap`

If your environment can't satisfy all three, use `nat` instead.

### Required mediation channel fails closed

```text
error: mediation channel required but unreachable
```

The workspace declared a mediation channel as required (the default) but the host listener isn't reachable. The body refuses to start.

Fixes:

- **Stand up the mediation listener** at the declared `host:port` before `microagent start`.
- **For development only**, pass `--mediation-optional` on `microagent create` to allow startup without the channel. Don't ship this — the channel is fail-closed for a reason ([security](security.md)).

## Console

### `microagent connect` hangs waiting for a shell prompt

By default, `connect` waits for the guest shell to be ready before attaching or sending input. If the guest hasn't reached its shell (still booting, entrypoint hasn't returned to a shell, or never will), it waits.

Fixes:

- **Look at the serial log** to see what the guest is doing: `microagent logs <name>`.
- **Disable the wait** if you know the workspace doesn't expose an interactive shell: `microagent connect <name> --ready-timeout 0`. You'll see raw serial output without the readiness gate.
- **Check workspace state** with `microagent --json status <name>` — if it's `failed`, the guest never reached a usable state and connect won't help.

### `microagent connect` says "console input is unavailable"

The backend's console capability is fine but this specific workspace doesn't have a console input endpoint yet. Common causes:

- The workspace is `prepared` but not `running`. Start it first.
- The backend created the runtime files but the serial input FIFO hasn't appeared yet (race window during start). Wait a moment and retry.

## Image and rootfs

### `microagent rootfs build` rejects a mutable tag

```text
error: image reference is mutable, pass --allow-mutable to override
```

`rootfs build` defaults to refusing tag references (e.g. `ubuntu:24.04`) because they're not reproducible — the same tag can resolve to different content tomorrow. Two paths:

- **Pin by digest** (recommended for production):
  `microagent rootfs build --image docker.io/library/ubuntu@sha256:...`
- **Override** (development only): pass `--allow-mutable`.

`microagent create` and `microagent run` are looser — they accept tags by default and record the resolved digest in the workspace's verification record. See [security](security.md) for the trust-boundary discussion.

### `microagent images pull` is slow or fails

- **Slow:** the OCI registry is slow, or the layers are large. Look at the registry/network rather than microagent.
- **Disk space:** pulls land under `~/.microagent/images/`. Check disk space; prune old records with `microagent images prune --delete`.

## Still stuck?

- Run `microagent --json doctor` for the full host capability report.
- Run `microagent --json status <name>` for the workspace state plus verification details.
- Check `microagent logs <name>` for serial output — most guest-side issues surface there.
- File an issue at [github.com/geoffbelknap/microagent-kit/issues](https://github.com/geoffbelknap/microagent-kit/issues) with the doctor output and the failing command.
