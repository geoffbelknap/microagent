# microagent

Run AI agent workspaces in microVMs.

Each agent gets its own Linux microVM — kernel, rootfs, state, lifecycle. Boot from an OCI image and tear down, or keep the workspace around and halt/resume it later. Linux uses Firecracker; macOS uses Apple Virtualization.framework; Windows Hyper-V support is experimental. Identity, policy, credentials, and control-plane decisions live in your code, not in this one.

The project is a Go library first. The `microagent` CLI is a thin shell over
the exported packages, so anything the CLI can do, your Go program can do
directly with typed options and typed results.

## Install

```bash
brew install geoffbelknap/tap/microagent
```

This installs `microagent` and `microagent-supervisor`, a symlink to the correct supervisor for your host. To build from source, see [`docs/getting-started/install.md`](docs/getting-started/install.md).

## 30-second tour

```bash
microagent doctor                                # check the host

# one-shot: boot, run, tear down
microagent run docker.io/library/ubuntu:24.04 uname -a
```

`microagent run` also accepts the explicit form when you want shell command
parsing:

```bash
microagent run --image docker.io/library/ubuntu:24.04 --exec "uname -a"
```

If you omit a command, microagent uses the image's Entrypoint/Cmd. Common
container-style aliases are supported where they map cleanly to microVMs:
`-e/--env`, `-p/--publish`, `-v/--volume` for tar/ext4 inputs, `--name`, and
`--rm`.

Private registry pulls use standard registry credential configuration from
`$DOCKER_CONFIG/config.json` or `~/.docker/config.json`, including configured
credential helpers.

For workspaces that stick around — halt, resume, copy files in, attach a console:

```bash
microagent create research \
  --image docker.io/library/ubuntu:24.04 \
  --profile medium

microagent start research
microagent exec research -- uname -a            # structured stdout/stderr/exit code
microagent connect research                     # interactive console
microagent halt research                         # clean shutdown, disk preserved
microagent start research                        # boots the same disk back up
microagent delete research
```

The same workspace can be expressed declaratively — see [`microagent.yaml`](docs/cli/spec.md) for the spec format.

Other useful surfaces:

- `microagent inspect <name>` — structured alias for `status`
- `microagent exec <name> -- <argv...>` — run a structured command in a running workspace
- `microagent rm <name>` — alias for `delete`
- `microagent images pull/list/tag/rm/prune` — manage reusable local rootfs baselines
- `microagent cp` and `microagent artifacts get` — move files without entering a running VM
- `microagent perf` — measure boot and runtime footprint

## What it owns

The VM boundary. Kernel management, OCI-to-rootfs builds, local image records, VM lifecycle (`run`, `create`, `start`, `halt`, `quarantine`, `stop`, `kill`, `delete`), networking and vsock wiring, serial console, file transfer for stopped disks, structured results, declared artifacts, runtime verification, lifecycle events, and backend supervisors.

## What it doesn't own

Planning loops, LLM calls, tool mediation, policy decisions, credential brokering, audit interpretation. Other projects own those — `microagent` is the substrate they sit on.

It also does not expose container-engine APIs, compose projects, pods,
privileged mode, namespace/device controls, host directory bind mounts, or named
volumes. MicroAgent accepts only the subset that maps cleanly to a microVM
boundary.

## Docs

Pick the path that matches what you're doing:

| Trying it out (CLI) | |
|---|---|
| [Install](docs/getting-started/install.md) | Homebrew, source, host check |
| [First microVM](docs/getting-started/cli/first-microvm.md) | Boot, run a command, tear down with `microagent run` |
| [First agent](docs/getting-started/cli/first-agent.md) | An LLM body running inside a microVM (Anthropic / OpenAI / Gemini) |
| [Named workspaces](docs/getting-started/cli/named-workspaces.md) | Create, start, stop, resume |
| [CLI reference](docs/cli/index.md) | Every subcommand |

| Embedding microagent from Go | |
|---|---|
| [Library overview](docs/library/index.md) | When to use the library, main packages, and integration path |
| [First program](docs/getting-started/library/first-program.md) | A handful of lines that boots a VM, runs a command, tears down |
| [Go library](docs/library/go.md) | Exported package surface and CLI ↔ library mapping |
| [Supervisor protocol](docs/protocol/index.md) | JSON protocol if you're going below the library |

| Reference and operations | |
|---|---|
| [Concepts](docs/concepts/architecture.md) | Architecture, backends, networking, state, [glossary](docs/concepts/glossary.md) |
| [Recipes](docs/recipes/index.md) | End-to-end examples |
| [Security](docs/security.md) | Trust boundary; see [`SECURITY.md`](SECURITY.md) for disclosure |
| [Troubleshooting](docs/troubleshooting.md) | Common failure modes, indexed by symptom |

## Project

- [`CONTRIBUTING.md`](CONTRIBUTING.md) — development setup and PR conventions
- [`SECURITY.md`](SECURITY.md) — reporting a security issue
- [`CHANGELOG.md`](CHANGELOG.md) — release notes and unreleased changes
- License: [`Apache-2.0`](LICENSE)
