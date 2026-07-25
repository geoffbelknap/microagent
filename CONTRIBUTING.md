# Contributing

Thanks for helping improve `microagent`.

This repository owns the VM boundary: kernels, OCI-to-rootfs conversion, VM
lifecycle commands, backend supervisors, state reporting, runtime verification,
readiness, results, artifacts, and networking/vsock wiring. Policy, credential
mediation, orchestration, LLM calls, audit meaning, and memory systems belong in
upstream projects.

## Development Setup

Install Go (the version `go.mod` requires or newer) and, on macOS, Xcode
command line tools with Swift. Linux Firecracker work requires KVM and
`/dev/vhost-vsock`. Every build entry point (`make build`, `make dev`,
`make install`) checks for the build tools first; in an interactive shell
with Homebrew available it offers to install a missing or outdated Go for
you, and otherwise tells you what to install.

```bash
go test ./...
```

On macOS, build the Apple VF supervisor:

```bash
swift build --package-path supervisors/applevf --disable-sandbox
```

## Checks

Run the cheap checks before opening a PR:

```bash
go test ./...
make lint          # golangci-lint run (includes go vet and gofmt drift)
python3 scripts/dev/markdown-link-check.py
python3 scripts/dev/docs-last-updated.py --check
python3 scripts/dev/docs-parity.py
python3 scripts/dev/docs-style.py
```

Formatting is enforced by lint; fix drift with `make fmt`.

For code that changes shared runtime behavior, also run:

```bash
go test -race ./...
make smoke-contract
```

Run live backend smokes on hosts with the right virtualization support:

```bash
make smoke
make smoke-rootfs
```

The hosted CI is layered into tiers (all on GitHub-hosted runners):

- **Tier 0 — PR gate (`.github/workflows/ci.yaml`):** Go tests + lint, docs/shell
  checks, and the *portable* (no-VM) E2E scenarios. Fast, deterministic, blocks
  merge:
  ```bash
  scripts/dev/microagent-e2e.sh contract help-usage registry-auth text-output init
  ```
- **Tier 1 — core E2E (`.github/workflows/e2e-core.yaml`):** runs on every PR. One
  isolated parallel job per *core* VM scenario (`scripts/dev/microagent-e2e.sh
  --list-tier core`), each with one automatic retry. Blocks merge.
- **Tier 2 — full E2E (`.github/workflows/e2e-full.yaml`):** the *broad* scenario
  set, one job per scenario, run nightly, on release tags, and on demand via the
  `run-full-ci` PR label. Gates releases, not every PR. A non-blocking
  **quarantine** lane (`--list-tier quarantine`) holds known-flaky scenarios —
  tracked, never blocking — until fixed.

Reliability comes from per-scenario isolation plus condition-based waits (the
`e2e_wait_*` helpers in `e2e-lib.sh`, tunable via `MICROAGENT_E2E_WAIT_TIMEOUT`),
not runner horsepower; if a job still flakes, escalating its `runs-on` to a
larger runner is a one-line change.

Other lanes:

- macOS Apple VF parity runs in `ci.yaml` (the `macos-supervisor` job); a
  self-hosted Apple-silicon runner is the release source of truth for
  Virtualization.framework behavior.
> **Cutover:** `.github/workflows/live-linux-parity.yaml` (the legacy monolithic
> suite) runs in parallel as a safety net during the transition; it is retired
> once `e2e-full` is green over several consecutive nights.

Run the full suite locally:

```bash
scripts/dev/microagent-e2e.sh
```

Feature E2E scenarios are backend-agnostic. They describe the shared
microagent contract first and select a backend lane from the host, or from
`MICROAGENT_E2E_BACKEND=firecracker|applevf` when you need to force one:

```bash
scripts/dev/microagent-e2e.sh \
  public-surface \
  lifecycle \
  networking \
  transport \
  supervision
```

List scenarios with `scripts/dev/microagent-e2e.sh --list` (each shows its
platform and requirement). Before fresh live runs, use
`scripts/dev/cleanup-temp.sh` in dry-run mode to check for preserved temporary
state from failed tests; pass `--yes` only when the candidates are safe to
delete. Successful E2E scenarios are expected to remove their own temporary
state.

### Feature coverage

Beyond the contract/lifecycle/networking/transport/supervision lanes, dedicated
scenarios cover each shipped feature: `init` (project scaffold, no VM),
`volumes` (named-volume registry + attach-by-name persistence + single-attach),
`commit-images` (rootfs → OCI commit into the local layout), `health` (health
probe config + boot), `exec-stream` (streaming structured exec), and
`survive-reboot` (boot-unit install/uninstall).

### Model mediation validation

Model mediation has one required contributor pressure gate that does not need a
GPU, llama.cpp, vLLM, HuggingFace access, or a real model:

```bash
scripts/dev/microagent-e2e.sh model-mediation-pressure-ci
```

Use these opt-in lanes when changing model runner or mediation behavior:

```bash
# Policy generation/evaluation only; no VM or runner.
MICROAGENT_E2E_MODEL_MEDIATION_RUNNER_POLICY_ONLY=1 \
  scripts/dev/microagent-e2e-model-mediation-runner.sh

# Functional fake-runner mediation matrix; no GPU or real model.
MICROAGENT_E2E_MODEL_MEDIATION_RUNNER_FAKE=1 \
  scripts/dev/microagent-e2e.sh model-mediation-runner-fake

# llama.cpp, CPU by default.
MICROAGENT_E2E_MODEL_MEDIATION_LLAMA=1 \
  MICROAGENT_LLAMA_SERVER=/path/to/llama-server \
  scripts/dev/microagent-e2e.sh model-mediation-llamacpp

# llama.cpp with explicit GPU offload.
MICROAGENT_E2E_MODEL_MEDIATION_LLAMA=1 \
  MICROAGENT_E2E_MODEL_MEDIATION_LLAMA_GPU=1 \
  MICROAGENT_LLAMA_SERVER=/path/to/llama-server \
  scripts/dev/microagent-e2e.sh model-mediation-llamacpp

# vLLM GPU lane from a local checkout.
MICROAGENT_E2E_MODEL_MEDIATION_VLLM=1 \
  MICROAGENT_E2E_MODEL_MEDIATION_VLLM_REPO=../vllm \
  scripts/dev/microagent-e2e.sh model-mediation-vllm
```

For Linux x86_64 NVIDIA hosts, `scripts/dev/build-llama-cuda.sh --llama-dir
../llama.cpp` builds a reproducible CUDA-enabled `llama-server` without
installing CUDA packages into the system. Hardware pressure runs are opt-in:
set the adapter-specific `*_PRESSURE=1` switch and use
`*_PRESSURE_PRESET=hardware` for bounded warn-gate collection.

### Validating a new machine (WSL, macOS, Linux)

Run the whole suite — it self-selects what the host supports and **skips with a
reason** instead of failing when a prerequisite is missing:

```bash
scripts/dev/microagent-e2e.sh
```

A preflight line reports `os/arch/wsl/vm`, and a final
`PASSED / SKIPPED / FAILED` summary tells you exactly what was validated. Each
scenario declares a requirement:

- `none` — always runs (CLI surfaces, scaffold).
- `vm` — needs a microVM backend; skips with a reason when `/dev/kvm` +
  Firecracker (Linux) or Apple Virtualization.framework (macOS) is absent.

On **WSL2**, enable nested virtualization and `/dev/kvm` to exercise the `vm`
lane. Run `microagent doctor` for a capability readout.

Live Firecracker tests must run outside sandboxed environments on Linux hosts
with KVM, `/dev/kvm`, `/dev/vhost-vsock`, `/dev/net/tun`, Firecracker on
`PATH` or `MICROAGENT_FIRECRACKER`, permission to create TAP/bridge/NAT state,
and the network prerequisites documented by
`scripts/dev/microagent-e2e-linux-network-setup.sh`. Apple VF tests must run on
Apple silicon macOS with the supervisor built and signed as described in
[Backends](docs/concepts/backends.md). The macOS lane is exposed through the
same runner:

```bash
scripts/dev/applevf-supervisor-build.sh
scripts/dev/microagent-e2e.sh \
  public-surface \
  lifecycle \
  networking \
  transport \
  supervision
```

Before release, the Apple VF lane must pass portable public CLI behavior,
lifecycle/substrate, connect/logs/ps, user/isolated networking, TCP publish,
mediation/vsock transport, supervision/restart behavior, quarantine cleanup,
results, artifacts, attached disks, and text/JSON output on an Apple silicon
host. The `applevf-*` scenarios are targeted backend diagnostics for narrower
failures; `applevf-direct-console` is a direct-supervisor smoke check.

## Pull Requests

- Keep changes narrowly scoped.
- Include docs updates when command output, runtime semantics, or operator
  workflows change. `scripts/dev/docs-style.py` checks the prose; keep it
  passing.
- Prefer JSON/API outputs and tests over log scraping.
- Do not widen this project into policy, orchestration, credential mediation,
  image signing, image scanning, or LLM/tool execution.
- Call out any live smoke tests that could not be run.

## Security

Do not open public issues for security-sensitive reports. Follow
[`SECURITY.md`](SECURITY.md).
