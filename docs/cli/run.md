---
title: microagent run
description: Boot a microVM from an OCI image, run a command, and tear it down.
---

<!-- docs-last-updated -->
_Last updated: 2026-07-13_

```text
microagent run --image <ref> --exec "<command>" [flags]
microagent run [flags] <image> [command arg...]
```

`run` is the one-shot path. It fetches the image, builds a rootfs, boots the
microVM, runs `--setup` then `--exec`, prints the result, and removes scratch
state (unless `--keep` is set). Use [`create`](/cli/create/) instead when you
want the workspace to survive - `run` is for disposable work, `create` for a
named workspace you'll `start`, `connect` to, and come back to.

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

## Flags

Flags you'll actually use:

- `--exec <command>` - one shell command string, when argv form is awkward
- `--setup <command>` - prepare the guest before `--exec`; repeatable
- `-e KEY=VALUE` - set guest environment variables
- `-p [host:]hostPort:guestPort` - forward a TCP port to the guest
- `-v SRC:DST[:ro|rw]` - attach a named volume, tar bundle, or ext4 disk
- `--profile <name>` - size the VM (`tiny`, `small`, `medium`, `large`)
- `--keep` - keep state after the run so you can inspect the disk or `connect` to it
- `--timeout <seconds>` - kill the run if it outlives the deadline

The rest, grouped:

### Workspace basics

| Flag | Description |
|---|---|
| `--image <ref>` | OCI image reference |
| `--exec <command>` | Shell command to run |
| `--setup <command>` | Shell command to run before `--exec`. Repeatable |
| `--setup-file <path>` | Shell script file to run before `--exec`. Repeatable |
| `--image-command` | Run the image Entrypoint/Cmd |
| `--entrypoint <command>` | Command to run on start |
| `--shell <path>` | Console shell path for kept/named runs. Defaults to `/bin/sh` |
| `--hostname <name>` | Guest hostname. Defaults to the sanitized workspace name |
| `--env KEY=VALUE`, `-e` | Guest environment variable. Repeatable |
| `--name <name>` | Workspace name; a readable one like `run-brave-otter-4f9c` is generated when omitted. Also accepted as `--id` |
| `--file <path>` | Workspace spec file; flags override matching spec fields |
| `--restart <policy>` | For kept/named runs: `never`, `on-failure`, or `always` |
| `--timeout <seconds>` | Maximum wall-clock time before kill |
| `--ttl <seconds>` | Idle lease: reap the VM after this long with no `exec`/`connect`. `0` = permanent |
| `--keep` | Keep state after the command exits |
| `--rm` | Explicit disposable-run behavior (the default unless `--keep` is set) |
| `--dry-run` | Validate the configuration without writing state |
| `--service-command <cmd>` | Long-running VM service command. Only [`create`](/cli/create/) accepts it; `run` rejects it |

Activity on the workspace (an `exec` or `connect`) renews the `--ttl` lease, so
it only reaps VMs that have actually gone quiet.

### Resources & networking

| Flag | Description |
|---|---|
| `--profile <name>` | Resource profile: `tiny`, `small`, `medium`, or `large` |
| `--memory <MiB>` | Memory in MiB (default 512) |
| `--cpus <n>` | CPU count |
| `--size-mib <MiB>` | Rootfs disk size |
| `--network <mode>` | Network mode: `user` (default) or `isolated` |
| `--publish <mapping>`, `-p` | Forward `[host:]hostPort:guestPort[/tcp]`. Repeatable |

`--memory`, `--cpus`, and `--size-mib` override a single value while keeping
the profile. See [`profiles`](/cli/profiles/) for the exact sizes.

### Files, disks & volumes

| Flag | Description |
|---|---|
| `--disk n=p:/m:ro\|rw` | Attach an existing ext4 disk |
| `--bundle n=p:/m:ro\|rw` | Build a disk from a tar bundle |
| `-v, --volume SRC:DST[:ro\|rw]` | Attach a named volume, tar bundle, or ext4 disk image |
| `--output n=/guest/path` | Declare an output artifact path |

### Secrets & credentials

| Flag | Description |
|---|---|
| `--secret NAME=<scheme>:<ref>` | Deliver a secret to `/run/secrets/NAME`. Repeatable. See [`secret`](/cli/secret/) |
| `--secrets-env-file <path>` | Deliver every key in a dotenv file as a secret |
| `--secret-on-demand NAME=<scheme>:<ref>` | Secret fetched at runtime via `$MICROAGENT_SECRETS_SOCK`, never written to tmpfs. Repeatable |
| `--secrets-audit` | Log every secret access (`microagent secret audit`) |

### Egress & broker

| Flag | Description |
|---|---|
| `--egress <mode>` | `broker` (default), `mitm`, or `off` |
| `--egress-lock-allowlist` | Only allowlisted hosts are reachable. Works in `broker` or `mitm` |
| `--egress-allow <host>` | Allowlist a destination: exact host or `.suffix`. Repeatable |
| `--egress-passthrough <host>` | Allowed host forwarded opaquely, never TLS-intercepted (for cert-pinned/mTLS endpoints). Repeatable |
| `--egress-policy <path>` | Policy file declaring `allow[]`/`passthrough[]`; unioned with the flags. Requires `--egress broker` or `mitm` |
| `--egress-swap-config <path>` | Credential-swap config (YAML). Requires `--egress mitm` |
| `--cred-swap PROVIDER[=ref]` | Swap in a built-in provider's API key host-side. Repeatable; requires `--egress mitm` |
| `--broker-upstream <url>` | Egress broker upstream base URL |
| `--broker-secret NAME=<scheme>:<ref>` | Broker credential reference. Required with `--broker-upstream` |
| `--broker-env KEY[=VALUE]` | Guest env var pointed at the broker; empty `VALUE` = broker URL. Repeatable |
| `--broker-proxy` | Also set `HTTPS_PROXY`/`HTTP_PROXY` in the guest to the broker |
| `--broker-capture` | Opt in to raw capture of pre-swap broker requests (off by default) |
| `--broker-ca <path>` | PEM bundle the broker's upstream TLS client trusts (default: system roots) |
| `--broker-endpoint <spec>` | One broker endpoint as `;`-separated `key=value` pairs. Repeatable |
| `--mediation p=host:port` | Guest-to-host [mediation channel](/concepts/glossary/) — a vsock (VM socket) path into your host control plane |
| `--mediation-optional` | Allow startup when mediation is unavailable |

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

### Model runner & mediation

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

### Low-level & output

| Flag | Description |
|---|---|
| `--backend <name>` | Backend identity override |
| `--kernel <path>` | Custom kernel path |
| `--state-dir <dir>` | State directory (default `~/.microagent/`) |
| `--guest-init <path>` | Guest init path |
| `--arch <arch>` | Guest architecture |
| `--result-port <port>` | Vsock result port |
| `--mke2fs <path>` | mke2fs binary path |
| `--supervisor <path>` | Override the installed host backend supervisor path |

See [global flags](/cli/#global-flags) for `--json`/`--text`/`--output`/`--mode`/`--supervisor`.

## Image references

`--image` accepts both digest-pinned references (`docker.io/library/ubuntu@sha256:…`) and mutable tags. Both are allowed here. For repeatable runs in CI or production, pin by digest. [`microagent rootfs build`](/cli/rootfs/) is the stricter path - it rejects mutable tags unless you pass `--allow-mutable`. See [security](/security/) for the rationale.

## Exit status

`run` exits `0` when the one-shot run completes; nonzero when the workspace
fails to build or boot, or when the run cannot complete. The guest command's own
exit code is *not* propagated to the CLI exit status; it is reported in the
result instead - in the text output as `Exit code:` and in JSON under
`result.exit_code`. Use [`exec`](/cli/exec/) when you need the guest exit code
to drive the shell. In AX mode a failure is written as a structured error
envelope.

## Related

- [`create`](/cli/create/) - keep the workspace between starts
- [`kernel install`](/cli/kernel/) - manage kernels explicitly
- [`rootfs build`](/cli/rootfs/) - build a rootfs without booting
