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
- cleanup, state files, and stale temporary artifact policy
- Firecracker supervisor
- Apple Virtualization.framework supervisor protocol
- experimental Windows Hyper-V supervisor boundary

## Non-goals

- Do not implement orchestration, planning, LLM calls, tools, or memory.
- Do not implement policy, audit meaning, credential mediation, or enforcement
  decisions. Other projects own those.
- Do not become a general-purpose Mac VM manager. Lima, Tart, vfkit, and Lume
  already serve that space.
- Do not grow rootfs build logic into a general image scanner, signer, or
  registry management tool.
- Do not become a container engine. Container-style conveniences are allowed
  only when they map cleanly to microVM semantics. Do not implement
  container-engine APIs, compose projects, pods, privileged mode,
  namespace/device controls, host directory bind mounts, or named volumes.

## Design rules

- Keep public output structured and machine-readable.
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
- Use `scripts/dev/microagent-e2e.sh --list` to see E2E scenarios. The portable
  set is `contract help-usage registry-auth text-output`; the full Linux live
  suite also includes `public-surface lifecycle-matrix networking mediation
  supervision`.
- Before fresh live runs, use `scripts/dev/cleanup-temp.sh` in dry-run mode to
  identify preserved stale state. Delete only after confirming the candidates
  are test-owned and safe.
- Do not put transient test-run notes in `docs/`; anything in `docs/` becomes
  part of the docs site. Use Notion or another tracking system for run notes.
- When command output, flags, runtime semantics, or operator workflows change,
  update README/docs and run `python3 scripts/dev/markdown-link-check.py` and
  `python3 scripts/dev/docs-last-updated.py --check` and
  `python3 scripts/dev/docs-parity.py`.

## Project boundary

Other projects supply policy, audit meaning, identity, and user intent. This
project owns kernels, rootfs conversion, VM commands, runtime verification, and
state reporting.
