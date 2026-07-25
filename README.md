# microagent

Run AI agent workspaces in microVMs.

Each agent gets its own real Linux VM - its own kernel, its own disk, its own
network - not a shared-kernel container. microagent boots those VMs from the
same container (OCI) images you already build, converted into bootable disks.
Run something once and throw it away, keep a workspace around and come back to
it, or run a task and get a report of everything it tried to reach on the
network. Linux and macOS hosts are supported; on a new host, run
`microagent doctor` first.

The project is a Go library first. The `microagent` CLI is a thin shell over
the exported packages, so anything the CLI can do, your Go program can do
directly with typed options and typed results.

The current stable release is listed on the
[releases page](https://github.com/geoffbelknap/microagent/releases); see
[`CHANGELOG.md`](CHANGELOG.md) for what is in it.

## Install

```bash
brew install geoffbelknap/tap/microagent
```

This installs `microagent` and `microagent-supervisor`, a symlink to the
supervisor for your host. To build and install from source:

```bash
make install
```

Use `make dev` for a checkout-local development build plus a host readiness
check. On Linux, `make install` downloads the pinned Firecracker VMM into the
install prefix and installs host packages such as `passt` when possible. It
prints a compact summary by default; use `QUIET=0` for full package-manager and
download output. For details, see
[`docs/getting-started/install.md`](docs/getting-started/install.md).

## 30-second tour

```bash
microagent doctor                                # check the host

# one-shot: boot, run, tear down
microagent run docker.io/library/ubuntu:24.04 uname -a

# same, plus a report of what the task reached on the network
microagent dispatch docker.io/library/python:3.12-slim python -c 'print(2+2)'
```

`microagent run` also accepts the explicit form when you want shell command
parsing:

```bash
microagent run --image docker.io/library/ubuntu:24.04 --exec "uname -a"
```

If you omit a command, microagent uses the image's Entrypoint/Cmd. Common
container-style aliases are supported where they map cleanly to microVMs:
`-e/--env`, `-p/--publish`, `-v/--volume` for named volumes, tar bundles, and
ext4 disk images, `--name`, and `--rm`.

Private registries: log in once with `microagent registry login`, or point
`$REGISTRY_AUTH_FILE` at an existing auth file. microagent never reads Docker's
`config.json` or runs credential helpers; public images always pull
anonymously.

For workspaces that stick around:

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

You can also keep the workspace in a spec file. See
[`microagent.yaml`](docs/cli/spec.md) for the format.

Other useful commands:

- `microagent status <name>` prints structured status.
- `microagent exec <name> -- <argv...>` runs a structured command in a running workspace.
- MCP clients launch `microagent serve mcp` and drive workspaces through tools.
- `microagent model pull/list/delete/prune/serve` downloads, manages, and serves local HuggingFace GGUF model files.
- `microagent image pull/list/tag/delete/prune` manages reusable local rootfs baselines.
- `microagent cp` and `microagent artifact get` move files without entering a running VM.
- `microagent perf` measures boot and runtime footprint.

Driving microagent from an AI agent or a coding tool? The MCP server is the
agent-optimized interface: typed tools, bounded summaries, structured errors,
and action guidance instead of CLI text to scrape. Point your client at
`microagent serve mcp`; see
[`microagent serve`](docs/cli/serve.md) for per-client setup snippets.

## Docs

Pick the path that matches what you're doing:

| Trying it out (CLI) | |
|---|---|
| [Install](docs/getting-started/install.md) | Homebrew, source, host check |
| [Choosing microagent](docs/getting-started/choosing-microagent.md) | Honest comparison with containers, raw Firecracker, Mac VM managers, and hosted sandboxes |
| [Quickstart](docs/getting-started/quickstart.md) | Boot, run a command, tear down with `microagent run` |
| [First agent](docs/getting-started/cli/first-agent.md) | An LLM body running inside a microVM (Anthropic / OpenAI / Gemini) |
| [`microagent init`](docs/cli/init.md) | Scaffold a starter agent body in one command |
| [Persistent workspaces](docs/guides/persistent-workspaces.md) | Create, start, halt, connect, delete |
| [CLI reference](docs/cli/index.md) | Every subcommand |
| [FAQ](docs/getting-started/faq.md) | Short answers to common questions |

| Embedding microagent from Go | |
|---|---|
| [Library overview](docs/library/index.md) | When to use the library, main packages, and integration path |
| [First program](docs/getting-started/library/first-program.md) | A handful of lines that boots a VM, runs a command, tears down |
| [Go library](docs/library/go.md) | Exported packages and CLI ↔ library mapping |

| Reference and operations | |
|---|---|
| [Guides](docs/guides/index.md) | Step-by-step walkthroughs |
| [Host requirements](docs/concepts/backends.md) | What Linux, macOS, and WSL hosts need |
| [Network modes](docs/concepts/networking.md) | `user`, `isolated`, published ports, and what status reports |
| [Storage](docs/concepts/storage.md) | Rootfs disks, named volumes, tar bundles, and stopped-disk copy |
| [Limitations](docs/concepts/limitations.md) | Deliberate refusals - bind mounts, `--privileged`, compose, and more - and where to go instead |
| [Security](docs/security.md) | Trust boundary; see [`SECURITY.md`](SECURITY.md) for disclosure |
| [Troubleshooting](docs/troubleshooting.md) | Common failure modes, indexed by symptom |
| [Glossary](docs/concepts/glossary.md) | The handful of words the docs lean on: workspace, rootfs, egress, broker |

## Where microagent stops

microagent owns everything at the VM boundary: it turns container images into
bootable disks, boots and supervises the VMs, wires up their networking and
console, runs commands inside them with typed results, and moves files in and
out. It deliberately stops there - your code (or your agent framework) decides
*what* to run and *why*. Planning loops, LLM calls, policy, credential
decisions, and audit interpretation belong to the layer above; microagent is
the VM layer underneath them.

[microagency](https://github.com/geoffbelknap/microagency) is one example of
that layer: an MCP gateway built on this substrate that keeps credentials and
large results out of the model's context.

It is also not a container engine. Container-style conveniences (`-e`, `-p`,
`-v`, `--name`, `--rm`, [named volumes](docs/concepts/storage.md),
[user-mode networking](docs/guides/networking.md)) are supported where they map
cleanly to a real VM boundary; container-engine APIs, compose projects, pods,
privileged mode, and host directory bind mounts are not.

## Project

- [`CONTRIBUTING.md`](CONTRIBUTING.md) - development setup and PR conventions
- [`SECURITY.md`](SECURITY.md) - reporting a security issue
- [`CHANGELOG.md`](CHANGELOG.md) - release notes and unreleased changes
- License: [`Apache-2.0`](LICENSE)
