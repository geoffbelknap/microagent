# microagent

Go CLI/library plus backend supervisors for running Linux workspaces in
microVMs.

## Scope

This repository owns the VM pieces:

- CLI/API surfaces documented under `docs/cli/` and implemented in
  `cmd/microagent`: workspace lifecycle and control, structured exec,
  connect/logs/events/stats/result, file copy and artifacts, snapshots and
  forks, image/commit, network, volume, model, secret, rootfs, kernel, host,
  doctor, contract, perf, serve, AX, and MCP surfaces
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
- Do not implement policy, audit meaning, credential decisions, or enforcement
  decisions. Other projects own those. microagent performs the deterministic
  credential-protection mechanism (swap from operator config; the real secret
  never enters the guest); it does not broker or decide credentials.
- Do not turn the MCP endpoint into a planner, policy engine, or agent
  framework. It is an adapter over microagent's existing substrate APIs.
- Do not become a general-purpose Mac VM manager. Lima, Tart, vfkit, and Lume
  already serve that space.
- Do not grow rootfs build logic into a general image scanner, signer, or
  registry management tool.
- Do not become a container engine. Container-style conveniences are allowed
  only when they map cleanly to microVM semantics.
- Do not implement container-engine APIs, compose projects, pods, privileged
  mode, namespace/device controls, or host directory bind mounts.
- Named volumes are allowed only as the microVM analog: platform-managed,
  single-attach ext4 disks addressable by name, with a lifecycle independent of
  any one VM.
- Do not implement the Docker volume model — daemon-managed, driver-based, or
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

## Collaboration rules

- Treat a writable worktree as single-writer. If multiple people or agents are
  working in parallel, create separate branches and separate `git worktree`
  checkouts instead of sharing the same checkout.
- Use one task branch per focused change. Keep branch names descriptive, and use
  the `codex/` prefix for Codex-created branches unless the user asks for
  another name.
- Before editing, run `git status --short --branch --untracked-files=all` and
  inspect existing local changes. Do not overwrite, reformat, stage, or delete
  changes you did not make.
- If the current branch changes, or unexpected modified or untracked files
  appear while you are working, pause and report the conflict instead of
  guessing ownership.
- Keep changes scoped to the files needed for the assigned task. If a cleanup
  crosses ownership boundaries or overlaps another in-flight change, split it
  into a separate branch or ask for coordination.
- Use pull requests as the integration point for parallel work. Do not merge
  several agents' local changes together in one shared worktree unless the user
  explicitly asks you to reconcile them.
- Give concurrent tool runs separate writable caches when the tool uses locks or
  mutable cache state. For example, set `GOLANGCI_LINT_CACHE` to a task-specific
  directory under `/tmp` when running lint in parallel.

## Testing rules

- Run live Firecracker, network, and E2E tests outside sandboxed environments.
  KVM, `/dev/vhost-vsock`, `/dev/net/tun`, networking tools, and cleanup checks
  must reflect the real host.
- Use `scripts/dev/microagent-e2e.sh --list` as the source of truth for E2E
  scenario names, coverage classes, platform requirements, and backend
  coverage. Backend-neutral feature suites must run the current host backend
  selected by `MICROAGENT_E2E_BACKEND`. Backend-specific scenarios are host
  implementation probes and must be named as such.
- Before fresh live runs, use `scripts/dev/cleanup-temp.sh` in dry-run mode to
  identify preserved stale state. Delete only after confirming the candidates
  are test-owned and safe.
- Do not put internal docs or transient test-run notes in `docs/`; anything in
  `docs/` becomes part of the public docs site. Keep internal docs outside the
  public repository.
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

## PR workflow

- Open normal pull requests, not draft pull requests, unless the user
  explicitly asks for a draft.
- Before opening or updating a PR, check whether the branch is behind its base
  branch and update it automatically when possible.
- After pushing changes to an existing PR branch, update the PR without waiting
  for another prompt.
- Enable auto-merge on PRs by default when required checks and review gates
  cover the change and the user has not asked to leave the PR unmerged.

## Project boundary

Other projects supply policy, audit meaning, identity, and user intent. This
project owns kernels, rootfs conversion, VM commands, runtime verification, and
state reporting. Kernel build and release machinery belongs in the private
companion `microagent-kernels` repository; this repository should consume and
verify tagged kernel artifacts without duplicating that workflow.
