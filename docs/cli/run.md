---
title: microagent run
description: Boot a microVM from an OCI image, run a command, and tear it down.
---

<!-- docs-last-updated -->
_Last updated: 2026-08-12_

```text
microagent run --image <ref> --exec "<command>" [flags]
microagent run [flags] <image> [command arg...]
```

`run` is the one-shot path. It fetches the image, builds a rootfs, boots the
microVM, runs `--setup` then `--exec`, prints the command's output, and removes
scratch state (unless `--keep` is set). Cleanup happens on failure as well as
success, so iterating on a broken image does not accumulate orphaned records.
The guest's stderr and serial log are captured into the result before the disk
is discarded. Use [`create`](/cli/create/) instead
when you want the workspace to survive - `run` is for disposable work, `create`
for a named workspace you'll `start`, `connect` to, and come back to.

On a terminal, `run` behaves like running the command locally. Live progress
(image pull, rootfs build, boot) goes to stderr, the guest command's stdout and
stderr land on the matching host streams, and the guest exit code becomes the
CLI exit code. With `--keep`, the workspace name is printed to stderr so you
can `connect` to it or inspect it later. The full workspace metadata (rootfs
path, kernel, resources, timings) is available with `--json` or, for kept
workspaces, via [`status`](/cli/status/).

The positional form is useful when you already think in image-plus-command
terms. If no command is provided, microagent runs the image's Entrypoint/Cmd.
Use `--exec` when you want one shell command string instead of an argv-style
command.

## Examples

Run a single command and throw the VM away:

```bash
microagent run docker.io/library/ubuntu:24.04 uname -a
```

Run the image's default command:

```bash
microagent run docker.io/library/busybox:1.36
```

Use container-style aliases:

```bash
microagent run \
  -e FOO=bar \
  -p 127.0.0.1:8080:80 \
  --rm \
  docker.io/library/ubuntu:24.04 \
  printenv FOO
```

With `--json` before the subcommand, `run` prints the structured result. A
trimmed example:

```json
{
  "workspace": "run-brave-otter-4f9c",
  "state_dir": "/home/user/.microagent",
  "restart": "never",
  "resources": { "memory_mib": 512, "cpu_count": 2, "size_mib": 1024 },
  "rootfs_path": "/home/user/.microagent/workspaces/run-.../rootfs.ext4",
  "kernel_path": "/home/user/.microagent/kernels/linux-kvm/amd64/Image",
  "final_state": "stopped",
  "result": {
    "started_at": "2026-06-01T12:00:00Z",
    "exited_at": "2026-06-01T12:00:01Z",
    "exit_code": 0,
    "stdout": "Linux 6.1.0 ...\n"
  },
  "response": { "ok": true, "backend": "linux-kvm" }
}
```

Container-style `-v` is intentionally narrow. It accepts tar archives
as bundles and ext4 disk images as attached disks:

```bash
microagent run \
  -v /tmp/config.tar:/config:ro \
  -v /tmp/workspace.ext4:/workspace:rw \
  docker.io/library/ubuntu:24.04 \
  ls /config /workspace
```

Attach a [named volume](/cli/volume/) by name with `-v data:/work` for
persistent, VM-independent storage. Host directory bind mounts are not exposed:
package a directory as a tar archive for ingress, attach an ext4 disk, use
`microagent cp` with a stopped workspace, and declare `--output` paths for
egress.

Unsupported container-engine features such as compose projects, pods,
privileged mode, namespace flags, devices, and host bind mounts fail with
targeted guidance instead of being silently translated into microVM behavior.

Run with a named resource profile:

```bash
microagent run \
  --image docker.io/library/ubuntu:24.04 \
  --profile medium \
  --exec "apt-get update"
```

Run setup commands first:

```bash
microagent run \
  --image docker.io/library/busybox:1.36 \
  --setup "mkdir -p /workspace" \
  --setup "echo ready > /workspace/status" \
  --exec "cat /workspace/status"
```

Use a custom kernel:

```bash
microagent run \
  --image docker.io/library/ubuntu:24.04 \
  --exec "uname -a" \
  --kernel /tmp/Image
```

## What runs

One rule, no precedence to memorize: pick one way to say what executes,
and everything else composes or is rejected.

- The command comes from exactly one of: a positional `COMMAND ARG...`,
  `--exec <command>`, or `--image-command` (the image's own Entrypoint/Cmd —
  also the default when you pass none of the three).
- Passing two of them is rejected, not resolved: positional + `--exec`,
  and `--image-command` + `--exec`, are both errors.
- `--setup` / `--setup-file` compose with whichever you picked: setup runs
  first, in the same boot, before the command.
- `--shell` only sets the console shell used by `connect`; it never changes
  what runs.
- `--entrypoint` and `--service-command` belong to [`create`](/cli/create/) —
  the entrypoint is what later `start`s boot, and a one-shot has no later
  starts — so `run` rejects both.

## Flags

Common flags:

- `--exec <command>` - one shell command string, when argv form is awkward
- `--setup <command>` - prepare the guest before `--exec`; repeatable
- `-e KEY=VALUE` - set guest environment variables
- `-p [host:]hostPort:guestPort` - forward a TCP port to the guest
- `-v SRC:DST[:ro|rw]` - attach a named volume, tar bundle, or ext4 disk
- `--profile <name>` - size the VM (`tiny`, `small`, `medium`, `large`)
- `--keep` - keep state after the run so you can inspect the disk or `connect` to it
- `--timeout <seconds>` - kill the run if it outlives the deadline

The rest, grouped the same way `run --help` groups them:

### Core

| Flag | Description |
|---|---|
| `--image <ref>` | OCI image reference |
| `--exec <command>` | Shell command to run |
| `--setup <command>` | Shell command to run before `--exec`. Repeatable |
| `--setup-file <path>` | Shell script file to run before `--exec`. Repeatable |
| `--image-command` | Run the image Entrypoint/Cmd |
| `--allow-guest-setuid` | Keep setuid/setgid bits from the image (default: stripped; see `create`) |
| `--shell <path>` | Console shell path for kept/named runs. Defaults to `/bin/sh` |
| `--hostname <name>` | Guest hostname. Defaults to the sanitized workspace name |
| `--purpose <text>` | Opaque caller purpose recorded verbatim in workspace audit identity |
| `--correlation-id <id>` | Opaque caller correlation ID recorded verbatim in workspace audit identity |
| `--env KEY=VALUE` | Guest environment variable. Repeatable |
| `--disk n=p:/m:ro\|rw` | Attach an existing ext4 disk |
| `--bundle n=p:/m:ro\|rw` | Build a disk from a tar bundle |
| `--name <name>` | Workspace name; a readable one like `run-brave-otter-4f9c` is generated when omitted. Also accepted as `--id` |
| `--file <path>` | Workspace spec file; flags override matching spec fields |
| `--restart <policy>` | For kept/named runs: `never`, `on-failure`, or `always` |
| `--profile <name>` | Resource profile: `tiny`, `small`, `medium`, or `large` |
| `--memory <MiB>` | Memory in MiB (default 512) |
| `--cpus <n>` | CPU count |
| `--size-mib <MiB>` | Rootfs disk size (default: grows to fit the image) |
| `--network <mode>` | Network mode: `user` (default) or `isolated` |
| `--mediation p=host:port` | Guest-to-host [mediation channel](/concepts/glossary/) — a vsock (VM socket) path into your host control plane |
| `--mediation-optional` | Allow startup when mediation is unavailable |
| `--result-port <port>` | Vsock result port |
| `--timeout <seconds>` | Maximum wall-clock time before kill. Enforced by the supervisor itself, so the run dies at the deadline even if the invoking process is gone |
| `--ttl <seconds>` | Lifetime lease from VM start; activity does not renew it. `0` = permanent |
| `--keep` | Keep state after the command exits |
| `--serial-log-bytes <n>` | Console log bytes inlined in the structured result as a tail (default 8192; `-1` inlines the full log; the full log is always at `serial_path` while state is kept) |
| `--rm` | Explicit disposable-run behavior (the default unless `--keep` is set) |
| `--dry-run` | Validate the configuration — including the same offline image-ref parse a real run performs first — and return the prepared plan (guest command, kernel, resources, network) without writing state or booting |
| `--service-command <cmd>` | Long-running VM service command. Only [`create`](/cli/create/) accepts it; `run` rejects it |
| `--backend <name>` | Backend identity override |
| `--kernel <path>` | Custom kernel path |
| `--state-dir <dir>` | State directory (default `~/.microagent/`) |
| `--guest-init <path>` | Guest init path |
| `--arch <arch>` | Guest architecture (`arm64`/`aarch64`, `amd64`/`x86_64`) |
| `--mke2fs <path>` | mke2fs binary path |
| `--debugfs <path>` | debugfs binary path used to apply OCI filesystem metadata |
| `--supervisor <path>` | Override the installed host backend supervisor path |

Activity on the workspace does not renew the `--ttl` lease; the deadline is a
hard operational bound. `--memory`, `--cpus`, and `--size-mib`
override a single value while keeping the profile - see [`profiles`](/cli/profiles/)
for the exact sizes.

### Container-style aliases

Shorthand for the guest env, port-publish, and volume/bundle/disk flags above -
see [container-style convenience](/cli/#container-style-convenience) on the
index page:

| Flag | Description |
|---|---|
| `--env KEY=VALUE`, `-e` | Guest environment variable. Repeatable |
| `--publish <mapping>`, `-p` | Forward `[host:]hostPort:guestPort[/tcp]`. A concrete IPv4 host bind is preserved as the guest application's local address. Repeatable |
| `-v, --volume SRC:DST[:ro\|rw]` | Attach a named volume, tar bundle, or ext4 disk image |

### Egress & broker

| Flag | Description |
|---|---|
| `--secret NAME=<scheme>:<ref>` | Deliver a secret to `/run/secrets/NAME`. Repeatable. See [`secret`](/cli/secret/) |
| `--secrets-env-file <path>` | Deliver every key in a dotenv file as a secret |
| `--acknowledge-capability-risk <reason>` | Record why the operator accepts private data plus injected files/disks plus unmediated outbound access |
| `--secret-on-demand NAME=<scheme>:<ref>` | Secret fetched at runtime via `$MICROAGENT_SECRETS_SOCK`, never written to tmpfs. Repeatable |
| `--secrets-audit` | Log every secret access (`microagent secret audit`) |
| `--egress <mode>` | `broker` (default), `mitm`, or `off` |
| `--egress-lock-allowlist` | Only allowlisted hosts are reachable. Works in `broker` or `mitm` |
| `--egress-allow <host>` | Allowlist a destination: exact host or `.suffix`. Repeatable |
| `--egress-passthrough <host>` | Allowed host forwarded opaquely, never TLS-intercepted (for cert-pinned/mTLS endpoints). Repeatable |
| `--egress-policy <path>` | Policy file declaring `allow[]`/`passthrough[]`; unioned with the flags. Requires `--egress broker` or `mitm` |
| `--egress-swap-config <path>` | Credential-swap config (YAML). Requires `--egress mitm` |
| `--egress-max-total-bytes <n>` | Cumulative mediated egress bytes before the breaching flow is torn down. Defaults to 50 GiB under `broker`/`mitm`; `0` = unlimited |
| `--egress-max-bps <n>` | Per-flow mediated egress rate in bytes/sec. Defaults to 100 MiB/s under `broker`/`mitm`; `0` = unlimited |
| `--egress-max-conns <n>` | Concurrently mediated TCP connections. Defaults to 256 under `broker`/`mitm`; `0` = unlimited |
| `--cred-swap PROVIDER[=ref]` | Swap in a built-in provider's API key host-side. Repeatable; requires `--egress mitm` |
| `--broker-upstream <url>` | Egress broker upstream base URL. Broker endpoints require the `linux-kvm` backend |
| `--broker-secret NAME=<scheme>:<ref>` | Broker credential reference. Required with `--broker-upstream` |
| `--broker-env KEY[=VALUE]` | Guest env var pointed at the broker; empty `VALUE` = broker URL. Repeatable |
| `--broker-proxy` | Also set `HTTPS_PROXY`/`HTTP_PROXY` in the guest to the broker |
| `--broker-capture` | Opt in to raw capture of pre-swap broker requests (off by default) |
| `--broker-ca <path>` | PEM bundle the broker's upstream TLS client trusts (default: system roots) |
| `--broker-endpoint <spec>` | One broker endpoint as `;`-separated `key=value` pairs. Repeatable |

The default `broker` mode forwards traffic opaquely — no certificate is forged
and no CA is installed in the guest; TLS interception exists only in `mitm`,
which is why `--egress-swap-config` and `--cred-swap` require it. Broker
credentials and cred-swap refs are always references (`env:NAME` / `file:PATH`
/ `vault:PATH`), never literal secrets, and the guest never holds the real
key. For the full semantics — modes, allow vs passthrough, credential swap,
the broker decision stream — see
[egress mediation](/concepts/egress-mediation/) and the
[allowlist how-to](/guides/egress-allowlist/).

A `--broker-endpoint` spec bundles `upstream=<url>;secret=NAME=<scheme>:<ref>;base-url-env=KEY[=VALUE];ca=<path>;proxy;capture`
into one flag; repeat it for multiple endpoints, and don't combine it with the
individual `--broker-*` flags.

### Model runner

| Flag | Description |
|---|---|
| `--model <ref>` | Pair the run with a locally served HuggingFace GGUF model. See [`model`](/cli/model/) |
| `--model-token <token>` | HuggingFace token for auto-pull; defaults to `HF_TOKEN` / `HUGGING_FACE_HUB_TOKEN` |
| `--model-runner <backend>` | Runner backend: `llamacpp`, `vllm`, or `custom` |
| `--model-gpu <mode>` | GPU intent: `off`, `on`, or `auto` |
| `--model-runner-model <id>` | Backend model id for runners such as vLLM |
| `--model-runner-served-model <name>` | OpenAI-compatible served model name |
| `--model-runner-command <template>` | Custom OpenAI-compatible host runner command template |
| `--model-runner-name <name>` | Custom host model runner name override |
| `--model-runner-health-path <path>` | Custom host model runner health probe path |
| `--model-runner-arg <arg>` | Extra runner argument. Repeatable |
| `--model-runner-env KEY=VALUE` | Extra runner env for this invocation. Repeatable; not persisted |
| `--model-mediation <mode>` | Model mediation mode: `off`, `local-allow`, or `policy` |
| `--model-policy-file <path>` | Policy file for `--model-mediation policy` |
| `--model-policy-url <url>` | External policy endpoint for `--model-mediation policy` |
| `--model-policy-timeout <duration>` | Policy timeout, such as `250ms` or `2s` |

`--model` injects `MICROAGENT_MODEL_URL` / `OPENAI_BASE_URL` into the guest;
with `--keep`, the ref persists and later `start`s re-pair the model.

### Output

| Flag | Description |
|---|---|
| `--output n=/guest/path` | Declare an output artifact path |

See [global flags](/cli/#global-flags) for `--output`/`--json`/`--supervisor`.

## Image references

`--image` accepts both digest-pinned references (`docker.io/library/ubuntu@sha256:…`) and mutable tags. Both are allowed here. For repeatable runs in CI or production, pin by digest. [`microagent rootfs build`](/cli/rootfs/) is the stricter path - it rejects mutable tags unless you pass `--allow-mutable`. See [security](/security/) for the rationale.

Repeat runs of an unchanged image skip the build entirely. Every run still
resolves the tag's manifest digest from the registry, then clones the
recorded rootfs baseline for the image when one exists (the first build of
any image records one). When no baseline exists, the run falls back to the
digest-keyed [build-stage cache](/cli/rootfs/), which skips the layer
download. A tag
that moved upstream is fetched fresh — caches never decide what a tag
means, only whether bytes need rebuilding. The run's JSON result records
the path taken in `image.builder`/`image.base_source`.

## Exit status

In human mode `run` propagates the guest command's exit code as the CLI exit
status, matching [`exec`](/cli/exec/). The status is `0` when the command
succeeds, the command's own nonzero code when it fails, and `1` when the
workspace fails to build, boot, or complete.

## Related

- [`create`](/cli/create/) - keep the workspace between starts
- [`kernel install`](/cli/kernel/) - manage kernels explicitly
- [`rootfs build`](/cli/rootfs/) - build a rootfs without booting
