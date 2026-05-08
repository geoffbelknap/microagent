---
title: Stability
description: What microagent-kit promises will keep working, what it doesn't, and how to detect changes before they bite.
---

If you're integrating with microagent-kit — orchestrators, host control planes, agent runtimes — you need to know what you can lean on. This page sets the boundary.

The short version:

| Surface | Stability |
|---|---|
| CLI commands and their JSON output | Stable across minor releases. Additive changes are fine. |
| `microagent.yaml` spec format | Stable. New fields are additive. |
| Supervisor protocol (`vmkit.Request`/`Response`, command names, lifecycle states) | Stable. Versioned implicitly by the field set. |
| Runtime contract (`microagent --json contract`) | Stable, with an explicit version (`agent-runtime.v1`). |
| Go library APIs in `pkg/workspace`, `pkg/vmkit`, `pkg/kernel`, `pkg/imagecache`, `pkg/diagnostics`, `pkg/rootfs`, `pkg/supervisors/firecracker` | Stable across minor releases. |
| State directory layout under `~/.microagent/` | **Not stable.** Inspect via the CLI/library, not by reading files directly. |
| Backend supervisor scratch files (`runtime.json`, `firecracker.json`, etc.) | **Not stable.** These are operational artifacts, not API. |
| Smoke test scripts, Make target names | **Not stable.** Internal CI infrastructure. |
| Default kernel SHA | **Versioned with releases.** Each release notes the SHA it ships against. |

If you're depending on something that isn't in the "stable" list, expect it to change without notice.

## What's stable

### CLI commands

Every command listed in [the CLI reference](cli/index.md), with the flags and JSON output documented there, is stable. Additive changes (new optional flags, new fields in JSON output) can land in any minor release. Removals or shape changes won't happen without a major version bump and notice.

JSON output is the contract. Text output is for humans and may be reformatted at any time.

### Workspace spec

`microagent.yaml` ([reference](cli/spec.md)) is the declarative form. Existing fields keep working; new fields are additive.

When new fields land — like the `files:` field — older spec files keep validating. The CLI doesn't reject unknown fields outright, so spec files written for newer microagent versions degrade gracefully when read by older ones (the new field is simply ignored, with the consequences that implies).

### Supervisor protocol

The shared JSON request/response format documented in [`docs/protocol/`](protocol/index.md) is stable. That includes:

- The command names (`host`, `check`, `prepare`, `start`, `run`, `console`, `inspect`, `halt`, `quarantine`, `stop`, `kill`, `delete`)
- The lifecycle state names (`prepared`, `starting`, `running`, `halted`, `quarantined`, `stopped`, `failed`)
- The `Request` and `Response` field shapes for each command
- The mediation, readiness, result, artifacts, and verification blocks

Both Firecracker and Apple VF supervisors implement the same protocol. Anything calling the supervisor as a JSON subprocess, or via `pkg/vmkit`, can rely on these.

### Runtime contract

`microagent --json contract` returns the backend-neutral runtime contract, versioned as `agent-runtime.v1`. The version string is part of the response — consumers should check it before trusting the contents. New fields are additive; any breaking change increments the version.

This is the recommended way for agent-runtime builders to discover what microagent-kit guarantees, programmatically, rather than scraping documentation.

### Go library

The packages exported under `pkg/`:

| Package | What's stable |
|---|---|
| `pkg/vmkit` | `Request`, `Response`, supervisor `interface`, executable supervisor client. |
| `pkg/workspace` | The lifecycle functions (`Run`, `Create`, `Start`, `Status`, `Inspect`, `Control`, `Copy`, `Clone`, `ReadLogs`, `Network`, `List`, `ResultStatus`, `ArtifactsFor`, `GetArtifact`, `Supervise`, `ReadManifest`, `WriteManifest`) and their option/result types. |
| `pkg/kernel` | `Install`, `Verify`, default manifest. |
| `pkg/imagecache` | `Pull`, `List`, `Tag`, `Remove`, `Prune`. |
| `pkg/diagnostics` | `Check` and the support summary types. |
| `pkg/rootfs` | `Builder`, `BuildRequest`, `BuildResponse`, `BundleRequest`. |
| `pkg/supervisors/firecracker` | The Linux Firecracker supervisor implementation. |

Anything under `internal/` is, by Go convention, not part of the API surface.

## What isn't stable

### State directory layout

`~/.microagent/` (or whatever `--state-dir` points at) is microagent's working directory. The exact filenames, directory structure, and on-disk format inside are operational details. **Don't read these files directly** — use:

- `microagent ps` / `microagent --json status` for workspace state
- `microagent --json result` for guest results
- `microagent artifacts get` for declared output files
- `microagent logs` for serial output

The CLI's job is to give you a stable view of state. The on-disk layout's job is to support that view, and it can change.

### Backend supervisor scratch files

`runtime.json`, `firecracker.json`, console FIFOs, vsock sockets, TAP device names — all internal. They're documented in [`docs/protocol/firecracker.md`](protocol/firecracker.md) so operators can inspect them when debugging, not as a stable contract.

### Default kernel SHA

The default kernel ships pinned per release. Each release in [`docs/releases/`](releases/index.md) names the kernel SHA it was validated against. If you're in production and you care about kernel provenance, pin to the SHA from a specific release rather than relying on whatever the latest microagent-kit pulls down.

### Build and CI infrastructure

Smoke targets in the root `Makefile`, scripts under `scripts/`, helper Go binaries in `cmd/` other than `microagent` itself — internal. They're for the project's own development and validation, not consumers.

## Detecting changes

**For the runtime contract:** check the version in `microagent --json contract` before treating its fields as authoritative. If the version increments past what you tested against, validate before trusting new behavior.

**For the supervisor protocol and Go API:** rely on the test suite — the project's own smokes exercise the surfaces it commits to. Pin your microagent-kit dependency by version (Go modules) or pin the binary by digest (Homebrew tap, OCI artifact).

**For the on-disk layout** and other internal surfaces: don't depend on them. If you find yourself wanting to, that's a signal we're missing a CLI or library affordance — open an issue.

## Versioning

microagent-kit follows semantic versioning at the project level. Until 1.0:

- Breaking changes can land in minor releases but will be called out in the [release notes](releases/index.md).
- The runtime contract has its own version (currently `agent-runtime.v1`) which is independent of the project version — it bumps only when the contract itself changes shape.

After 1.0 the usual semver guarantees apply.

## Filing stability concerns

If something in the "stable" list above changed in a way that broke your integration, that's a bug — file an issue at [github.com/geoffbelknap/microagent-kit/issues](https://github.com/geoffbelknap/microagent-kit/issues) with the version you were on and what broke. If something in the "not stable" list is becoming load-bearing for you, that's a signal we should promote it. File an issue saying so.
