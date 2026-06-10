# microagent

Go CLI/library plus backend supervisors for running Linux workspaces in
microVMs.

## Scope

This repository owns the VM pieces:

- run, create, start, status/inspect, halt, quarantine, stop, kill,
  delete/rm, supervise, clone, cp, logs, connect, result, ps, network,
  artifacts, images, prune, perf, rootfs, kernel, host, doctor, and contract
  commands
- rootfs builds from OCI images
- local image records and reusable rootfs baselines
- guest metadata and identity propagation
- serial console, block-device, network, and vsock wiring
- readiness, structured results, declared artifacts, and event history
- AX output mode for agent-facing structured CLI responses
- the MCP stdio adapter over the existing workspace/image/copy/artifact APIs
- cleanup, state files, and stale temporary artifact policy
- Firecracker supervisor
- Apple Virtualization.framework supervisor protocol
- experimental Windows Hyper-V supervisor boundary

## Non-goals

- Do not implement orchestration, planning, LLM calls, agent-side tools, or
  memory. MCP tool wrappers are allowed only as adapters over microagent-owned
  substrate operations.
- Do not implement policy, audit meaning, credential mediation, or enforcement
  decisions. Other projects own those.
- Do not turn the MCP endpoint into a planner, policy engine, or agent
  framework. It is an adapter over microagent's existing substrate APIs.
- Do not become a general-purpose Mac VM manager. Lima, Tart, vfkit, and Lume
  already serve that space.
- Do not grow rootfs build logic into a general image scanner, signer, or
  registry management tool.
- Do not become a container engine. Container-style conveniences are allowed
  only when they map cleanly to microVM semantics. Do not implement
  container-engine APIs, compose projects, pods, privileged mode,
  namespace/device controls, or host directory bind mounts. Named volumes are
  allowed only as the microVM analog: platform-managed, single-attach ext4
  disks addressable by name, with a lifecycle independent of any one VM. Do not
  implement the Docker volume model — daemon-managed, driver-based, or
  concurrently-shared volumes — which does not map to microVM semantics.

## Design rules

- Keep public output structured and machine-readable.
- Keep AX mode and MCP responses structured, typed, and stable enough for agent
  clients to consume without log scraping.
- Keep the Apple VF supervisor usable from Go, Python, Rust, Node, and shell scripts.
- Treat state changes as API output, not log strings.
- Keep halt, quarantine, readiness, result, artifact, and verification semantics backend-neutral.
- Preserve explicit identity in requests, state files, and events.
- Keep backend details behind supervisor boundaries.
- Fail closed on invalid VM config.
- Prefer narrow protocols over shell-string execution.
- Keep container-style aliases honest and narrow: `run IMAGE [COMMAND ARG...]`,
  `-e/--env`, `-p/--publish`, `-v/--volume` for tar/ext4 inputs, `--name`,
  and `--rm` are convenience forms, not a container runtime contract.
- Keep private registry support limited to standard pull credentials read from
  `$DOCKER_CONFIG/config.json` or `~/.docker/config.json`; do not write login
  state or broker credentials.

## Testing rules

- Run live Firecracker, network, and E2E tests outside sandboxed environments.
  KVM, `/dev/vhost-vsock`, `/dev/net/tun`, networking tools, and cleanup checks
  must reflect the real host.
- Use `scripts/dev/microagent-e2e.sh --list` to see E2E scenarios. Feature
  suites should be backend-neutral by default: `public-surface`,
  `lifecycle-deep`, `networking-deep`, `transport-deep`, and
  `supervision-deep` must run the current host backend selected by
  `MICROAGENT_E2E_BACKEND`. Firecracker-only or Apple-VF-only scenarios are
  host implementation probes and must be named as such.
- Before fresh live runs, use `scripts/dev/cleanup-temp.sh` in dry-run mode to
  identify preserved stale state. Delete only after confirming the candidates
  are test-owned and safe.
- Do not put transient test-run notes in `docs/`; anything in `docs/` becomes
  part of the docs site. Use Notion or another tracking system for run notes.
- When command output, flags, runtime semantics, or operator workflows change,
  update README/docs and run `python3 scripts/dev/markdown-link-check.py` and
  `python3 scripts/dev/docs-last-updated.py --check` and
  `python3 scripts/dev/docs-parity.py`.
- When MCP tools, AX envelopes, readiness fields, or structured exec semantics
  change, update `docs/cli/serve.md`, `docs/cli/exec.md`,
  `docs/concepts/state-and-identity.md`, `docs/protocol/runtime-contract.md`,
  and `docs/library/go.md` as applicable.
- Keep release/install docs aligned with the Homebrew tap: only stable
  releases ship to the tap (`microagent`). Release candidates are git tags
  validated by local builds and the tag-gated live CI suites; they are not
  published as a formula.

## Project boundary

Other projects supply policy, audit meaning, identity, and user intent. This
project owns kernels, rootfs conversion, VM commands, runtime verification, and
state reporting.
