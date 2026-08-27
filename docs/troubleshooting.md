---
title: Troubleshooting
description: Find the failure you're seeing and fix it with the right tool.
---

<!-- docs-last-updated -->
_Last updated: 2026-08-27_

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

### `mke2fs` or `debugfs` not found (rootfs builds fail)

The rootfs builder needs `mke2fs` to format ext4 disks and `debugfs` to apply
OCI ownership, modes, xattrs, and special-file metadata without host root. A Homebrew install
of microagent brings it in as a dependency (`e2fsprogs`), so this entry
mostly applies to source and manual installs. On Linux it's usually
installed already; on macOS it isn't shipped by default.

```bash
# macOS
brew install e2fsprogs
```

Homebrew installs e2fsprogs keg-only, without linking it into `PATH`. That is
fine: every command that builds a rootfs looks in `PATH` first, then in
`/opt/homebrew/opt/e2fsprogs/sbin` and `/usr/local/opt/e2fsprogs/sbin`. For a
binary somewhere else, `microagent rootfs build` and `microagent run` accept
`--mke2fs <path>` and `--debugfs <path>`.

### `debugfs` not found (rootfs builds fail)

The rootfs builder also needs `debugfs` (part of the same `e2fsprogs`
package as `mke2fs`) to preserve the image's declared uid/gid and mode bits
- including setuid, setgid, and sticky - on the built ext4 image, independent
of whichever host user ran the build. It resolves the same way `mke2fs`
does: `PATH` first, then the Homebrew keg-only locations above. For a binary
somewhere else, pass `--debugfs <path>`.

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

## Workspace lifecycle

### `start` fails with `listen unix ... bind: invalid argument` on Linux

The Firecracker backend exceeded Linux's pathname Unix-socket limit. Microagent
creates sockets below `<state-dir>/<workspace>/`; the longest possible name is
`vsock.sock_4294967295`. To leave room for every valid vsock port, keep the
UTF-8 byte length of `--state-dir` plus the workspace-name length at 84 or less.

The normal 63-character workspace-name check covers syntax only. A deeply
nested custom state directory can require a shorter name. Retry with a short
state directory, such as `/var/tmp/microagent`, or a shorter workspace name.
See the [socket path budget](/concepts/state-and-identity/#linux-firecracker-socket-path-budget)
for the full calculation.

### `exec` times out after `pause` then `resume` on Linux

Firecracker v1.16.x has a vsock defect on a bare pause/resume cycle. The
resume arms an internal receive gate that only a snapshot's transport reset
can clear, so every later host-initiated vsock connection hangs. Structured
exec and the model bridge stop answering, while the guest itself keeps
running and `logs` still works. The upstream fix
([firecracker#6100](https://github.com/firecracker-microvm/firecracker/pull/6100))
is merged but not yet in a release.

Until a release with the fix ships and the pinned Firecracker moves past
v1.16, avoid `pause`/`resume` on Linux when the workspace needs exec
afterward. `halt` then `start`, or `snapshot` then restore, both recover a
working exec path. macOS workspaces are unaffected.

### `microagent delete` asks "Stop and delete it?"

The recorded VM process is still alive. `delete` won't tear it down silently -
it prompts first, and `--yes` (or `--force`) answers the prompt and stops the
VM for you before deleting.

```bash
microagent delete <name> --yes   # stop the running VM, then delete

# or shut it down yourself first:
microagent halt <name>           # clean disk-preserving shutdown
microagent delete <name>
```

### `microagent start` fails because the workspace is `quarantined`

Quarantined workspaces preserve disk and event history while host-side network and mediation paths are severed. `start` refuses them until you move the workspace out of the state:

```bash
microagent halt <name>     # stop is an alias; or kill for a hard terminate
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

Recorded hashes don't match what's currently on disk. Treat kernel or
injected-init divergence as suspicious until you understand it. New workspaces
keep their injected init under the workspace state directory, so a package
upgrade does not remove it. Older workspaces use the recorded init SHA-256 as
the embedded content identity when their former installation path is gone.

Rootfs hashes are enforced while the workspace is still `prepared`; after a
workspace has started, the rootfs is the writable VM disk and normal guest boot
activity can change it.

```bash
microagent --json status <name> | jq '.verification.divergence'
```

The `field` and `expected` / `actual` values tell you which artifact diverged. Common case: someone reinstalled the kernel without re-creating the workspace, so the workspace still references the old kernel hash.

For repeatable deployments, prefer digest-pinned image refs such as
`docker.io/library/python@sha256:...`, install kernels with an explicit
`--sha256`, and check `.verification.ok` before `start`.

## Networking

For any entry in this section, `microagent --json network <name>` shows the
runtime IP, subnet, gateway, DNS, and route assigned to the guest.

### Firecracker `user` mode workspace won't start

`user` mode needs three things:

- `pasta` on `PATH` (from the `passt` package - `apt install passt` on Debian/Ubuntu, `dnf install passt` on Fedora; Homebrew installs it as a microagent dependency).
- Unprivileged user namespaces enabled. Check `sysctl user.max_user_namespaces` (returns a non-zero count when enabled). Some distros also gate this via `kernel.unprivileged_userns_clone` - set both to `1` if either is `0`. On Ubuntu 23.10+ this is additionally restricted by AppArmor - see below.
- `/dev/net/tun` readable by the calling user.

`microagent doctor` reports each of these - start there to find the missing piece.

If your host doesn't allow unprivileged user namespaces and you can't change that policy, use `--network isolated` when the guest does not need network access.

### Fedora/SELinux: `Couldn't open PID file … pasta.pid: Permission denied`

Newer Fedora-family hosts (including atomic desktops such as Bazzite and
Silverblue) ship an SELinux policy that confines `pasta` in its own domain
(`pasta_t`). In enforcing mode that policy denies pasta access outside its
expected locations — including writing its pid file into microagent's
workspace state dir under your home directory — so `user` mode networking
fails before the VM boots. The denial is recorded in the SELinux audit log,
not in pasta's own output, which makes the bare `Permission denied` easy to
misread as a microagent bug.

`microagent doctor` runs a pasta start probe against your state dir and names
this condition when it applies. To fix it, either:

```sh
sudo semanage permissive -a pasta_t
```

which stops the blocking while still logging every denial (reverse it later
with `-d`), or use `--network isolated` when the guest does not need network
access. Check for the denials themselves with
`journalctl -b --grep='avc.*pasta'`.

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

### A workspace reports Stopped but a VM is still running

Workspaces started before microagent recorded the user-mode network's
namespace init can be left behind if `pasta` died on its own. An OOM kill, a
crash, or an operator clearing what looked like a stray network helper can
all take it down. `pasta` only serves the network; the microVM runs in a
namespace anchored by a separate process. Killing `pasta` alone leaves the
guest executing while the workspace record shows no live process, so `halt`,
`kill`, and `quarantine` report success without stopping anything.

Workspaces started by a current build record that process and take it down on
every stop and `gc`, so this does not recur. To clear one stranded by an older
build, find it and kill it by hand:

```bash
ps -eo pid,args | grep firecracker
kill <pid>
```

### Required mediation channel fails closed

```text
error: mediation channel required but unreachable
```

The workspace declared a mediation channel as required (the default) but the host listener isn't reachable. The workspace starts, but readiness reports a `mediationReady` error and the channel is severed until a listener connects - no traffic can flow until then.

Fixes:

- **Stand up the mediation listener** at the declared `host:port` before `microagent start`.
- **For development only**, pass `--mediation-optional` on `microagent create` to allow startup without the channel. Don't ship this - the channel is fail-closed for a reason ([security](/security/)).

## Egress mediation

### Start fails with `broker: assurance is required`

Broker endpoints require an explicit response-side trust contract. Add
`--broker-assurance semantic --broker-grant ./grant.yaml` for a finite request
and response capability, or choose `--broker-assurance trusted-upstream` only
when you trust the upstream to handle the injected credential and its response.
For a repeatable endpoint spec, add `assurance=semantic;grant=./grant.yaml` or
`assurance=trusted-upstream`. Existing manifests without assurance fail closed;
update the declaration and recreate or apply the workspace. See
[semantic broker grants](/guides/broker-grants/).

### Start fails with `broker endpoint ...: secret ... did not resolve`

```text
broker endpoint https://api.anthropic.com: secret "anthropic" did not resolve: secret "env:ANTHROPIC_API_KEY" resolved to an empty value; fix the reference source, verify with `microagent secret check anthropic=env:ANTHROPIC_API_KEY`, then start again
```

A workspace with a [broker endpoint](/cli/create/) resolves its secret
reference on the host at every start, because the reference points at a live
source: an environment variable, a dotenv file, or Vault. Start refuses to
launch a workspace whose broker could never serve it. Restore the source the
reference names (export the variable, put the file back), confirm with
`microagent secret check`, and start again. If the reference resolves at
start but the source disappears before the broker companion spawns, the
workspace fails with the companion's own error in `microagent events` and
the companion log next to the workspace state.

### A mediated (`broker` or `mitm`) workspace fails to start with a TPROXY error

```text
egress: UDP mediation (TPROXY) unavailable for workspace research — ensure the host kernel provides TPROXY support (e.g. the nft_tproxy/xt_TPROXY module) or use --egress off
```

[Egress mediation](/concepts/egress-mediation/) runs inside the workspace's
own user namespace and mediates UDP and DNS via Linux TPROXY. That needs the
`nft_tproxy` kernel module, which a rootless workspace can't load itself.
Most hosts autoload it the first time a mediated boot installs its steering
rule. When the module is missing and the autoload can't fire, a mediated
workspace (the default `broker` mode, or `mitm`) refuses to start rather
than run with an unmediated UDP/DNS channel.

Fixes:

- **Load the module once, as root:** `sudo modprobe nft_tproxy` (its
  dependency loads with it). The workspace's netns can then install its own
  TPROXY rules. [`microagent doctor`](/cli/doctor/) verifies this with a real
  probe rule and reports the result.
- **Drop mediation** if you don't want it: `--egress off`.

### An allowed host's TLS connection fails under `mitm`

In `--egress mitm` mode, a destination you allowlisted (`--egress-allow`)
still fails its TLS handshake - typically a client-side certificate error in
the guest, or an `egress_mitm_handshake_error` / `egress_mitm_upstream_error`
record in `microagent egress <name>`. (The default `broker` mode never forges
certificates, so this symptom only appears when you opted into `mitm`.)

Cause: in `mitm` mode microagent intercepts TLS with a per-workspace CA.
Some clients reject the injected CA's leaf certificate:

- **Certificate pinning** - the client only trusts a specific certificate or key,
  not the per-workspace CA.
- **Mutual TLS** - the upstream demands a client certificate the mediator can't
  present.
- **A client with its own root store** - it ignores the CA microagent installed
  in the system trust store, so the mediator's leaf is untrusted.

Fix: mark the host **passthrough** so it is allowed and audited but not
intercepted - the original server certificate reaches the client untouched:

```bash
microagent create research --egress mitm \
  --egress-allow api.openai.com \
  --egress-passthrough pinned.example.com
```

The trade-off: a passthrough connection is forwarded as an opaque L4 byte stream.
microagent records *that* the connection happened (and how much data crossed
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

`rootfs build` defaults to refusing tag references (for example `ubuntu:24.04`) because they're not reproducible - the same tag can resolve to different content tomorrow. Two paths:

- **Pin by digest** (recommended for production):
  `microagent rootfs build --image docker.io/library/ubuntu@sha256:...`
- **Override** (development only): pass `--allow-mutable`.

`microagent create` and `microagent run` are looser - they accept tags by default and record the resolved digest in the workspace's verification record. See [security](/security/) for the trust-boundary discussion.

### The image doesn't fit the workspace disk

```text
rootfs contents need about 1183 MiB but the rootfs disk size is 1024 MiB; give the workspace a larger disk, for example --size-mib 2048, or drop the pinned size to let the disk grow to fit
```

By default the rootfs disk grows to fit the image, so this error only appears when you pinned a size that the unpacked image exceeds. The pinned size comes from `--size-mib` on the command line or `sizeMiB` in a spec file. Raise it, or remove it and let the disk size itself.

Older releases used a fixed disk size (1024 MiB by default) and surfaced this as a raw `mke2fs` failure: `build ext4 rootfs: ... Could not allocate block in ext2 filesystem`. There the fix is `--size-mib 2048`, a bigger `--profile`, or a smaller image such as `python:3.12-slim`.

### `sudo`, `su`, or `ping` fails inside the guest with "permission denied"

The rootfs build strips setuid and setgid bits from the image by default. A
workload running as its declared user has no use for them, and a setuid `su`
inherited from a stock base image is a route to root inside the guest that
nothing asked for. The workspace's image provenance records what was removed:
`setuid_policy` is `stripped` and `setuid_stripped` lists the paths.

For an image that needs a non-root user with working `sudo` (the devcontainer
pattern), create the workspace with `--allow-guest-setuid`. The choice is
per-workspace and recorded in the provenance as `preserved`.

### `microagent image pull` is slow or fails

- **Slow:** the OCI registry is slow, or the layers are large. Look at the registry/network rather than microagent.
- **Disk space:** pulls land under `~/.microagent/images/`. Check disk space; prune old records with `microagent image prune --purge`.

## Still stuck?

- Run `microagent --json doctor` for the full host capability report.
- Run `microagent --json status <name>` for the workspace state plus verification details.
- Check `microagent logs <name>` for serial output - most guest-side issues appear there.
- File an issue at [github.com/geoffbelknap/microagent/issues](https://github.com/geoffbelknap/microagent/issues) with the doctor output and the failing command.
