---
title: microagent run
description: Boot a microVM from an OCI image, run a command, and tear it down.
---

<!-- docs-last-updated -->
_Last updated: 2026-06-19_

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
  "workspace": "run-1730000000000000000",
  "state_dir": "/home/user/.microagent",
  "restart": "never",
  "resources": { "memory_mib": 512, "cpu_count": 2, "size_mib": 4096 },
  "rootfs_path": "/home/user/.microagent/workspaces/run-.../rootfs.ext4",
  "kernel_path": "/home/user/.microagent/kernels/linux-kvm/amd64/vmlinux",
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

The complete set:

| Flag | Description |
|---|---|
| `--image <ref>` | OCI image reference |
| `--exec <command>` | Shell command to run |
| `--setup <command>` | Shell command to run before `--exec`. Repeatable |
| `--setup-file <path>` | Shell script file to run before `--exec`. Repeatable |
| `--image-command` | Run the image Entrypoint/Cmd |
| `--entrypoint <command>` | Command to run on start |
| `--shell <path>` | Interactive console shell path for kept/named runs. Defaults to `/bin/sh` |
| `--hostname <name>` | Guest hostname. Defaults to the workspace name sanitized as a Linux hostname |
| `--env KEY=VALUE` | Guest environment variable. Repeatable |
| `-e KEY=VALUE` | Alias for `--env` |
| `--secret NAME=<scheme>:<ref>` | Deliver a secret to `/run/secrets/NAME` over vsock. Repeatable. See [`secret`](/cli/secret/) |
| `--secrets-env-file <path>` | Deliver every key in a dotenv file as a secret (plaintext, re-read each start) |
| `--secret-on-demand NAME=<scheme>:<ref>` | Declare an on-demand secret fetched at runtime via `$MICROAGENT_SECRETS_SOCK`, never written to tmpfs. Repeatable. See [`secret`](/cli/secret/) |
| `--secrets-audit` | Append every secret access to the workspace audit log (`microagent secret audit`) |
| `--disk n=p:/m:ro\|rw` | Attach an existing ext4 disk |
| `--bundle n=p:/m:ro\|rw` | Build a disk from a tar bundle |
| `-v, --volume SRC:DST[:ro\|rw]` | Container-style safe volume alias for tar bundles and ext4 disk images |
| `--output n=/guest/path` | Declare an output artifact path |
| `--file <path>` | Workspace spec file; flags override matching spec fields |
| `--restart <policy>` | Restart policy for kept/named runs: `never`, `on-failure`, or `always` |
| `--network <mode>` | Network mode: `user`, `nat`, `isolated`, or `named` |
| `--network-interface <if>` | Host interface identifier or display name (only used by the unsupported `bridged` mode) |
| `--network-name <name>` | Join a user-defined [named network](/cli/network/) by name (implies named mode) |
| `--publish <mapping>` | Forward `[host:]hostPort:guestPort[/tcp]` |
| `-p <mapping>` | Alias for `--publish` |
| `--name <name>` | Workspace name; generated when omitted. Also accepted as `--id` |
| `--backend <name>` | Backend identity override |
| `--kernel <path>` | Custom kernel path |
| `--state-dir <dir>` | State directory (default `~/.microagent/`) |
| `--guest-init <path>` | Guest init path |
| `--arch <arch>` | Guest architecture |
| `--profile <name>` | Resource profile: `tiny`, `small`, `medium`, or `large` |
| `--mediation p=host:port` | Declare the guest-to-host mediation vsock channel |
| `--mediation-optional` | Allow startup when mediation is unavailable |
| `--egress <mode>` | [Egress mediation](/concepts/egress-mediation/) mode: `mediated` (default; capture and audit everything, block nothing), `strict` (deny non-allowlisted), or `off` |
| `--egress-allow <host>` | Allowlisted egress destination (TLS-intercepted). Repeatable; an exact host or a `.suffix` matching the apex and subdomains. See the [allowlist how-to](/guides/egress-allowlist/) |
| `--egress-passthrough <host>` | Allowed egress destination that is **not** TLS-intercepted (forwarded opaquely). Repeatable. For cert-pinned / mTLS endpoints |
| `--egress-policy <path>` | Egress policy file (`.yaml`/`.yml`/`.json`) declaring `allow[]` / `passthrough[]`; unioned with the flags. Requires `--egress mediated` or `strict` |
| `--egress-swap-config <path>` | Credential-swap config (YAML): for an allowlisted, intercepted host the mediator injects the real credential host-side so the guest never holds it. Requires `--egress mediated` or `strict`. See [credential swap](/concepts/egress-mediation/#credential-swap) |
| `--model <ref>` | Pair the run with a locally served HuggingFace GGUF model and inject `MICROAGENT_MODEL_URL` / `OPENAI_BASE_URL`; with `--keep`, the ref persists and later `start`s re-pair. See [`model`](/cli/model/) |
| `--model-token <token>` | HuggingFace token for model auto-pull; defaults to `HF_TOKEN` or `HUGGING_FACE_HUB_TOKEN` when omitted |
| `--model-runner <backend>` | Model runner backend: `llamacpp`, `vllm`, or `custom` |
| `--model-gpu <mode>` | Model runner GPU intent: `off`, `on`, or `auto` |
| `--model-runner-model <id>` | Backend model id for runners such as vLLM |
| `--model-runner-served-model <name>` | OpenAI-compatible served model name for runners such as vLLM |
| `--model-runner-command <template>` | Custom OpenAI-compatible host runner command template |
| `--model-runner-arg <arg>` | Extra model runner argument. Repeatable |
| `--model-runner-env KEY=VALUE` | Extra model runner environment for this invocation. Repeatable; values are not persisted |
| `--model-mediation <mode>` | Model mediation mode: `off`, `local-allow`, or `policy` |
| `--model-policy-file <path>` | Structured model mediation policy file for `--model-mediation policy` |
| `--model-policy-url <url>` | External model mediation policy endpoint for `--model-mediation policy` |
| `--model-policy-timeout <duration>` | Model mediation policy timeout, such as `250ms` or `2s` |
| `--memory <MiB>` | Memory in MiB (default 512) |
| `--cpus <n>` | CPU count |
| `--size-mib <MiB>` | Rootfs disk size |
| `--result-port <port>` | Vsock result port |
| `--timeout <seconds>` | Maximum wall-clock time before kill |
| `--keep` | Keep state after the command exits |
| `--rm` | Explicit disposable-run behavior. This is the default unless `--keep` is set |
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
