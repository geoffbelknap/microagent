# Changelog

Use this file for release notes and the rolling list of changes that have not
been cut into a release yet.

## Unreleased

- Fixed Firecracker `user` networking on hosts with older pasta releases
  (including the stock Ubuntu 24.04 package): pasta was invoked without a
  `--` option terminator, so getopt-permuting versions tried to parse the
  supervisor's `--request-json` flag as their own and aborted.
- The live Linux parity workflow now runs on GitHub-hosted KVM runners on
  every push to main; it previously targeted a self-hosted runner label that
  was never registered, so the suite had never executed in CI.
- Centralized backend differences in a declarative `vmkit.BackendCapabilities`
  table (structured exec, live network apply, rootfs format, runtime-state
  ownership, detached-start style, shell transport and probing, block-device
  naming). The workspace layer now consults capabilities instead of branching
  on backend constants; unknown backends get zero capabilities (fail closed).
  No behavior change for existing backends.
- Workspace dispatch errors now preserve the underlying error chain, so Go
  library callers can use `errors.Is`/`errors.As` (for example to reach the
  supervisor's `*exec.ExitError`). Error message text is unchanged.
- CI now collects unit-test coverage on the Linux job (summary in the log, full
  profile uploaded as an artifact) and caches the Apple VF supervisor Swift
  build on the macOS job.
- README links to the releases page instead of hardcoding the latest version.
- **Security:** `model pull` now verifies downloads against the upstream
  digest. The expected LFS sha256 for the file is fetched from the Hugging
  Face paths-info API before the download, and the pull fails closed — on a
  digest mismatch, on a file that is not LFS-tracked, or when the upstream
  digest cannot be resolved — without writing a blob or an index record.
- **Security:** debugfs requests (`cp`, `artifacts get`) are no longer built
  by raw string concatenation. Every `-R` argument is validated (no quotes,
  control characters, or option-like leading dashes) and double-quoted, and
  remote workspace paths are validated in the copy layer itself so
  manifest-derived artifact paths get the same checks as CLI endpoints. As a
  side effect, local target directories containing spaces now work with
  `cp`/`artifacts get`.
- **Security:** OCI layer extraction now rejects entry names, hardlink
  targets, and symlink targets containing backslashes, which the
  slash-oriented traversal checks previously treated as plain name characters
  but Windows filesystem APIs treat as path separators. The Windows symlink
  marker fallback is also written through the `os.Root` sandbox instead of a
  host-path join outside it.
- **Breaking (Go library):** `workspace.ExecWithMetadata` now returns
  `(ExecResult, ExecRetryMetadata, error)` with the error last, matching Go
  convention. CLI and MCP behavior are unchanged.
- Tightened host state-file permissions. Files written under the state dir
  (workspace manifests, runtime/process state, events, network/volume/image/
  model indexes, snapshot manifests, serial and supervisor logs, volume disks)
  are now created `0600`, and new state directories `0700`, so workspace
  topology and runtime configuration are no longer readable by other users on
  the host. Existing files keep their modes; user-requested outputs (`cp`,
  artifact exports, `init` scaffolds, kernel downloads) are unchanged.
- Secret-access audit appends and Hyper-V event-log appends now report file
  close errors instead of silently dropping a possibly unflushed record.
- Adopted golangci-lint (`.golangci.yaml`, enforced in CI alongside the
  existing race tests; `go vet` is included in the lint run). Added package
  documentation for the exported Go packages and removed dead code left over
  from the CLI-to-library refactor. New `make fmt`, `make lint`, and
  `make test-race` targets.
- Unified the pinned Go toolchain across all CI workflows (1.26.4).
- Completed user-defined named networks: workspaces join a network with
  `create`/`run` `--network-name <name>`. Each member gets a stable IP from the
  network subnet (persisted in the registry, surviving stop/start), members
  share a per-network managed Linux bridge so they reach each other directly,
  and `/etc/hosts` name resolution is injected at boot via the kernel-cmdline →
  guest-init seam (parallel to DNS). Deleting a workspace frees its address; the
  shared bridge is reaped once the last member stops. Firecracker/Linux only
  (requires `net.ipv4.ip_forward=1` and CAP_NET_ADMIN, as with `nat` mode);
  Apple Virtualization.framework NAT cannot share a subnet. `/etc/hosts` is a
  boot-time snapshot — restart a member to pick up peers that joined later.
  Builds on the named-network registry (`pkg/network`) added earlier.
- Brought Apple Virtualization.framework validation up to the backend-neutral
  E2E surface. The unified `scripts/dev/microagent-e2e.sh` runner now selects
  the Darwin/Apple VF scenarios on macOS, covering public CLI surface,
  lifecycle, networking, transport/mediation, supervision, volumes,
  commit-images, secrets, health, streaming exec, and Apple VF host probes.
  The latest full macOS arm64 run on `main` passed 23 scenarios with no skips
  or failures.
- Fixed Apple VF networking and transport parity issues found by the expanded
  suite: guest DHCP/DNS setup for Apple VF boots, publish/workspace-connect
  smoke races, external-DNS-independent networking probes, optional mediation
  readiness semantics, and TCP-vsock bridge connection handling.
- Added host networking readiness/setup visibility for Linux privileged network
  modes. `doctor` and host diagnostics now report IP-forwarding and supervisor
  `CAP_NET_ADMIN` readiness, and `host setup-networking` can persist
  `ip_forward` plus set the supervisor capability on Linux while stubbing
  unsupported hosts explicitly.
- Added managed named volumes: `microagent volume create/ls/inspect/rm` and
  attach-by-name with `--volume <name>:/mount`. A named volume is a
  platform-managed ext4 disk with a lifecycle independent of any one workspace
  (new `pkg/volume` registry at `<state-dir>/volumes/index.json` plus a backing
  `<name>.ext4`), the in-boundary analog of a container volume. Volumes are
  single-attach: at most one running workspace holds a volume at a time, a stale
  holder (a stopped or crashed workspace) is reclaimed automatically, and
  deleting a workspace releases the volumes it held. This is deliberately not the
  Docker volume model — no daemon, no drivers, no concurrent sharing.
- Added `microagent commit <workspace> <image-ref>` and `microagent images push`
  to snapshot a stopped workspace's rootfs back into an OCI image and push it,
  closing the previously one-way OCI→rootfs loop. commit extracts the rootfs
  unprivileged via `debugfs`, assembles a single-layer OCI image (new
  `pkg/ociimage`), and writes it to a local OCI image layout under
  `<state-dir>/images/oci`; `images push` (or `commit --push`) copies it to the
  registry with the standard Docker pull credentials. Unprivileged extraction
  does not preserve file ownership (content, modes, and symlinks are preserved).
- Added `supervise --install` / `--uninstall` to survive host reboot. `--install`
  writes and registers an OS init unit (systemd user unit on Linux, launchd agent
  on macOS) that runs `supervise <name>` at boot, so a long-running workspace
  survives a reboot without microagent adding a persistent daemon. The unit file
  is always written; if automatic registration can't run, the manual enable
  command is reported. Backed by the new `pkg/superviseunit`.
- Added user-defined named networks: `microagent network create/ls/rm`. A named
  network is a VM-independent record (new `pkg/network` registry at
  `<state-dir>/networks/index.json`) with an auto-allocated `/24` from
  `10.44.0.0/16` (or an explicit `--subnet`) and a gateway. `rm` fails closed
  while members exist unless `--force`. This is the registry foundation for
  multi-workspace networking; joining workspaces and cross-VM connectivity +
  name resolution are realized by the backend supervisor (follow-up).
- Implemented streaming structured exec (`exec --stream` / `workspace.ExecStream`).
  The guest now delivers stdout/stderr as incremental chunk frames followed by a
  terminal result frame, so long-running commands stream output live instead of
  buffering until completion. AX mode keeps emitting a single structured
  envelope. Previously `stream` mode was reserved but unimplemented.
- Added a `health:` block to the workspace spec and restart-on-unhealthy to
  `supervise`. An exec probe (guest command, Firecracker) or httpGet probe
  (host-side GET to a published port) runs while the workspace is running; after
  `retries` consecutive failures the wedged VM is force-killed and the restart
  policy (`on-failure`/`always`) restarts it. Closes the gap where supervise
  only restarted on exit, not on alive-but-wedged.
- Added `microagent init <name>` to scaffold a starter agent body project — a
  `microagent.yaml` spec, a provider-specific `body.py` (Anthropic, OpenAI, or
  Gemini via `--provider`), the shared `protocol.py`, and a runnable demo
  request. Fails closed on existing files unless `--force`. Backed by the new
  `pkg/scaffold` package.
- Refreshed README, install, architecture, boundaries, library, and MCP docs for
  the stable `v0.1.45` release, `microagent-rc` Homebrew formula, and current
  AX/MCP substrate boundary.

## v0.1.45 - 2026-06-01

- Added AX output mode for agent-facing structured CLI responses and errors.
- Added the `microagent serve mcp` stdio endpoint with workspace lifecycle,
  status, inspect, exec, cost-estimate, mutation-preview, idempotency, and
  capability-manifest tools.
- Added the structured exec protocol, guest service, host client, CLI command,
  and MCP wiring.
- Added runtime readiness signals for guest, shell, structured exec, result,
  and mediation state.
- Added mediation target readiness probing for running workspaces, with
  fail-closed errors for required mediation and non-error not-ready status for
  optional mediation.
- Added bounded retry handling for transient MCP structured-exec connection
  failures, including retryable error metadata and retry-exhaustion details.
- Added fast status/inspect readiness behavior for non-live workspace states.
- Expanded Linux/Firecracker E2E coverage for lifecycle, networking,
  mediation/transport, supervision, public CLI surface, and runtime contracts.
- Renamed the project, Go module, Homebrew formula references, and docs from
  `microagent-kit` to `microagent`; the CLI name and `~/.microagent` state
  layout are unchanged.
- Hardened workspace/rootfs security behavior from the May 2026 findings pass.
- Added Apple VF end-to-end mediation validation for guest-to-host vsock,
  host replies, and structured guest results.
- Fixed Apple VF mediation listener setup and transient socket copy handling.
- Added Linux Firecracker validation fixes for NAT and host firewall behavior.
- Project governance, contribution, conduct, and security guidance.
- CI coverage for Linux Go checks, documentation links, shell scripts, and dependency vulnerability scanning.
