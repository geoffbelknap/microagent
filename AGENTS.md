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

## Platform support

- Linux (`linux-kvm` / Firecracker) and macOS (`apple-vf` / Apple
  Virtualization.framework) are the supported release targets. For shared
  backend-neutral features, strive for parity across these two supported
  backends by default. If parity is blocked by a real platform capability,
  entitlement, API behavior, or supervisor boundary, document the blocker and
  keep the backend-specific difference behind the supervisor boundary.
- WSL is an intended compatibility lane through the Linux backend when the
  underlying Linux prerequisites are available. Keep it working when changes
  touch Linux host assumptions, and use intermittent WSL parity passes to catch
  drift. Do not create a separate WSL product contract or make WSL a standing
  release gate.
- Windows Hyper-V (`windows-hyperv`) is experimental. Do not use Hyper-V to set
  the supported-platform parity bar, and do not expand Hyper-V parity or make
  it release-blocking unless the user explicitly scopes that work and there is
  a named owner for host access, CI signal, docs, and maintenance.
- Keep backend-neutral request/response, AX, MCP, readiness, result, artifact,
  and verification shapes stable where a backend implements a feature. Treat
  Linux and macOS as the parity targets for supported behavior; do not treat
  experimental Hyper-V coverage as a required parity gate.

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
- Put product behavior in library packages first. CLI commands and MCP tools
  are adapters over `pkg/*` and `internal/*` APIs; they must not be the only
  implementation of workspace semantics, validation, capability gates, or
  backend policy.
- Every user-facing workspace feature must have a library-owned contract before
  it is considered done. Treat every microagent feature as backend-neutral in
  product intent by default; backend-specific code is an implementation detail
  behind the library and supervisor boundaries.
- If a supported backend cannot implement a feature yet, record it as an
  explicit backend gap in the library contract with status, reason, and the
  named capability or implementation blocker. Expose unsupported behavior as a
  structured result from the shared library path. Do not bury support decisions
  in CLI parsing, MCP handlers, or supervisor-specific command branches.
- Do not introduce Linux-only or macOS-only product features. If a platform API
  makes parity hard, land the shared contract plus explicit gap record first,
  then close the backend implementation gap as follow-up work.
- Keep CLI, AX, and MCP surfaces on the same library path. A feature available
  through one adapter should either be available through the others or have a
  documented contract reason for the difference.
- Keep the Apple VF supervisor usable from Go, Python, Rust, Node, and shell scripts.
- Treat state changes as API output, not log strings.
- Keep halt, quarantine, readiness, result, artifact, verification, and new
  backend-neutral feature semantics aligned across supported backends by
  default.
- Preserve explicit identity in requests, state files, and events.
- Keep backend details behind supervisor boundaries.
- Fail closed on invalid VM config.
- Prefer narrow protocols over shell-string execution.
- Keep container-style aliases honest and narrow: `run IMAGE [COMMAND ARG...]`,
  `-e/--env`, `-p/--publish`, `-v/--volume` for tar/ext4 inputs, `--name`,
  and `--rm` are convenience forms, not a container runtime contract.
- Keep private registry support Docker-free: read pull credentials only from
  `$REGISTRY_AUTH_FILE` or microagent's own `~/.microagent/auth.json` (written
  by `microagent registry login`). Never read `~/.docker/config.json` or invoke
  docker credential helpers; public images always pull anonymously. Do not
  broker credentials.

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
  coverage. For release validation, backend-neutral feature suites must run the
  current supported host backend selected by `MICROAGENT_E2E_BACKEND`.
  Experimental Hyper-V runs are diagnostic unless explicitly scoped as part of
  the task. Backend-specific scenarios are host implementation probes and must
  be named as such.
- When adding or changing a user-facing workspace feature, add or update tests
  that prove the feature is declared in the library contract, mapped consistently
  from CLI and MCP adapters, and implemented for Linux KVM and Apple VF or
  recorded as an explicit backend gap with structured unsupported behavior.
- Before fresh live runs, use `scripts/dev/cleanup-temp.sh` in dry-run mode to
  identify preserved stale state. Delete only after confirming the candidates
  are test-owned and safe.
- Do not put internal docs or transient test-run notes in `docs/`; anything in
  `docs/` becomes part of the public docs site. Keep internal docs outside the
  public repository; use Notion for internal decisions, plans, and running
  notes. Keep `AGENTS.md` limited to repo working instructions.
- Public docs are for people using microagent. Optimize them for installing,
  running workspaces, embedding the Go library, operating the CLI/MCP surfaces,
  and troubleshooting real failures. Do not publish implementation plans,
  parity debt, backend support inventories, release-readiness matrices, or
  developer notes in `docs/`.
- Prefer runnable examples over abstract explanation. A good page starts with
  what the user can do, then gives the few concepts needed to make the example
  predictable.
- Keep language concise and direct. Avoid textbook-style architecture tours,
  stale capability tables, and backend caveats unless they change a user's
  command, host prerequisite, or troubleshooting path.
- Supported backends should read as supported. Do not ask users to compare
  Linux/macOS parity tables in public docs; describe the current supported
  behavior and use `doctor`, install docs, and troubleshooting pages for host
  prerequisites and current failure modes.
- When command output, flags, runtime semantics, or operator workflows change,
  update README/docs and run `python3 scripts/dev/markdown-link-check.py` and
  `python3 scripts/dev/docs-last-updated.py --check` and
  `python3 scripts/dev/docs-parity.py`.
- When MCP tools, AX envelopes, readiness fields, or structured exec semantics
  change, update `docs/cli/serve.md`, `docs/cli/exec.md`,
  `docs/concepts/state-and-identity.md`, and `docs/library/go.md` as
  applicable.
- Keep release/install docs aligned with the Homebrew tap: stable releases
  ship as `microagent`, and every merge to main refreshes `microagent-latest`
  (a source-build formula pinned to the merged commit, bumped by
  latest.yaml). Release candidates are git tags validated by local builds and
  the tag-gated live CI suites; they are not published as a formula.

## PR workflow

- Open normal pull requests, not draft pull requests, unless the user
  explicitly asks for a draft.
- Before opening or updating a PR, check whether the branch is behind its base
  branch and update it automatically when possible.
- After pushing changes to an existing PR branch, update the PR without waiting
  for another prompt.
- Prefer squash merge for focused task PRs when the repository supports it. Use
  merge commits only when preserving branch structure is intentional.
- Enable auto-merge on PRs by default when required checks and review gates
  cover the change and the user has not asked to leave the PR unmerged.

## Project boundary

Other projects supply policy, audit meaning, identity, and user intent. This
project owns kernels, rootfs conversion, VM commands, runtime verification, and
state reporting. Kernel build and release machinery belongs in the private
companion `microagent-kernels` repository; this repository should consume and
verify tagged kernel artifacts without duplicating that workflow.
