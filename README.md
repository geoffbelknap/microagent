# microagent-kit

Run AI agent workspaces in microVMs.

Each agent gets its own Linux microVM — kernel, rootfs, state, lifecycle. Boot from an OCI image and tear down, or keep the workspace around and halt/resume it later. Linux uses Firecracker; macOS uses Apple Virtualization.framework. Identity, policy, credentials, and control-plane decisions live in your code, not in this one.

`microagent-kit` is a Go library; the `microagent` CLI is a thin shell over it. Anything the CLI can do, your program can do directly.

## Install

```bash
brew install geoffbelknap/tap/microagent-kit
```

This installs `microagent` and the supervisor for your host (`microagent-firecracker-supervisor` on Linux, `microagent-applevf-supervisor` on macOS). To build from source, see [`docs/getting-started/install.md`](docs/getting-started/install.md).

## 30-second tour

```bash
microagent doctor                                # check the host

microagent run \                                 # one-shot: boot, run, tear down
  --image docker.io/library/ubuntu:24.04 \
  --exec "uname -a"
```

For workspaces that stick around — halt, resume, copy files in, attach a console:

```bash
microagent create research \
  --image docker.io/library/ubuntu:24.04 \
  --profile medium

microagent start research
microagent connect research --send "uname -a"   # send a line, capture output
microagent halt research                         # clean shutdown, disk preserved
microagent start research                        # boots the same disk back up
microagent delete research
```

The same workspace can be expressed declaratively — see [`microagent.yaml`](docs/cli/spec.md) for the spec format.

## What it owns

The VM boundary. Kernel management, OCI-to-rootfs builds, VM lifecycle (`run`, `create`, `start`, `halt`, `quarantine`, `stop`, `kill`, `delete`), networking and vsock wiring, structured results, declared artifacts, runtime verification, and lifecycle events.

## What it doesn't own

Planning loops, LLM calls, tool mediation, policy decisions, credential brokering, audit interpretation. Other projects own those — `microagent-kit` is the substrate they sit on.

## Docs

Pick the path that matches what you're doing:

| Trying it out (CLI) | |
|---|---|
| [Install](docs/getting-started/install.md) | Homebrew, source, host check |
| [First microVM](docs/getting-started/cli/first-microvm.md) | Boot, run a command, tear down with `microagent run` |
| [First agent](docs/getting-started/cli/first-agent.md) | An LLM body running inside a microVM (Anthropic / OpenAI / Gemini) |
| [Named workspaces](docs/getting-started/cli/named-workspaces.md) | Create, start, stop, resume |
| [CLI reference](docs/cli/index.md) | Every subcommand |

| Building with the library (Go) | |
|---|---|
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
