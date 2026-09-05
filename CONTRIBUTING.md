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
  --list-tier core`), each with one automatic retry. Blocks merge. VM jobs set
  `MICROAGENT_E2E_REQUIRE_VM=1`, so a runner that cannot boot a microVM fails
  the job instead of skipping every scenario and reporting suite OK.
- **Tier 2 — full E2E (`.github/workflows/e2e-full.yaml`):** the *broad* scenario
  set, one job per scenario, run nightly, on release tags, and on demand via the
  `run-full-ci` PR label. Gates releases, not every PR. A non-blocking
  **quarantine** lane (`--list-tier quarantine`) holds known-flaky scenarios —
  tracked, never blocking — until fixed. A weekly **flake report**
  (`.github/workflows/e2e-flake-report.yaml`) aggregates first-attempt scenario
  failures that the automatic retries absorbed, so flake debt stays visible.

Reliability comes from per-scenario isolation plus condition-based waits (the
`e2e_wait_*` helpers in `e2e-lib.sh`, tunable via `MICROAGENT_E2E_WAIT_TIMEOUT`),
not runner horsepower; if a job still flakes, escalating its `runs-on` to a
larger runner is a one-line change.

Other lanes:

- macOS checks run in `ci.yaml`: Go tests, Swift build and tests, and
  supervisor-only lifecycle checks. Live qualification runs on a physical
  Apple-silicon host when promoting a Mac build. See [Mac qualification](#mac-qualification).

> **Cutover:** `.github/workflows/live-linux-parity.yaml` (the legacy monolithic
> suite) runs in parallel as a safety net during the transition; it is retired
> once `e2e-full` is green over several consecutive nights.

Run the full suite locally:

```bash
scripts/dev/microagent-e2e.sh
```

Feature E2E scenarios are backend-agnostic. They describe the shared
microagent contract first and select a backend lane from the host, or from
`MICROAGENT_E2E_BACKEND=linux-kvm|apple-vf` when you need to force one:

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

# Functional fake-runner mediation matrix; no GPU or real model. Runs by
# default in the nightly broad tier and in local suite runs.
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

### Mac qualification

Linux releases proceed independently of Mac qualification. Existing Mac
capabilities retain their execution and security contracts. New features may
reach Linux first; unsupported requests must fail through the shared library.

To qualify a trusted release tag on a physical Apple-silicon Mac:

```bash
python3 scripts/dev/qualify-applevf.py --ref v0.10.0 --record
```

Use a full commit SHA to qualify a latest-channel candidate. The command
fetches that revision into a detached worktree, builds the host and guest
binaries, and runs the Go, Swift, and live workspace suites. Run it outside a
sandbox. Install build tools and host prerequisites beforehand; qualification
never installs packages or changes your working checkout.

Results stay under `~/Library/Logs/microagent/qualification/`. Each run retains
its checkout, private log, and `result.json`, including the source SHA, host,
scenario list, and build hashes. The temporary-state cleanup check is read-only;
review stale candidates separately before deleting them.

`--record` posts an `applevf-qualified` status on the tested commit. It marks
an in-progress run as pending, and a failed run replaces an earlier success.
Omit `--record` to keep results local. A passing run does not publish a formula.
The compatibility command `applevf-live-attest.sh` qualifies the current clean
checkout's commit through the same path.

After successful qualification, promote that platform and channel explicitly:

```bash
gh workflow run update-homebrew-tap.yml \
  -f tag=v0.10.0 -f platform=macos -f channel=stable
```

For a latest build, pass the tested full SHA as `tag` and set `channel=latest`.
Promotion requires the new qualification status on that exact commit; an old
`applevf-live` status is insufficient. The selected commit must be on main's
history and cannot precede the installed formula's source revision.

Linux publication changes only Linux's formula pin. Both Mac formula pins
remain at their previous revisions until explicitly promoted. During the first
split, the Mac latest formula takes the existing stable Mac source. Mac builds
may skip intervening Linux versions. Release candidates cannot enter the stable
formula.

After inspecting a retained run, remove its checkout with `git worktree remove
<run-directory>/checkout`. Remove the remaining logs only when no longer needed.

## Pull Requests

- Keep changes narrowly scoped.
- Include docs updates when command output, runtime semantics, or operator
  workflows change. `scripts/dev/docs-style.py` checks the prose; keep it
  passing.
- Add release notes as a new `.changelog.d/<short-slug>.md` fragment rather
  than editing `CHANGELOG.md` directly; see
  [`.changelog.d/README.md`](.changelog.d/README.md). CI rejects a direct
  `CHANGELOG.md` edit outside a release.
- Prefer JSON/API outputs and tests over log scraping.
- Do not widen this project into policy, orchestration, credential mediation,
  image signing, image scanning, or LLM/tool execution.
- Call out any live smoke tests that could not be run.

## Security

Do not open public issues for security-sensitive reports. Follow
[`SECURITY.md`](SECURITY.md).
