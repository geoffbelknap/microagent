# Contributing

Thanks for helping improve `microagent`.

This repository owns the VM boundary: kernels, OCI-to-rootfs conversion, VM
lifecycle commands, backend supervisors, state reporting, runtime verification,
readiness, results, artifacts, and networking/vsock wiring. Policy, credential
mediation, orchestration, LLM calls, audit meaning, and memory systems belong in
upstream projects.

## Development Setup

Install Go and, on macOS, Xcode command line tools with Swift. Linux
Firecracker work requires KVM and `/dev/vhost-vsock`.

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
go vet ./...
python3 scripts/dev/markdown-link-check.py
python3 scripts/dev/docs-last-updated.py --check
python3 scripts/dev/docs-parity.py
```

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

The hosted CI workflow runs the portable microagent E2E scenarios:

```bash
scripts/dev/microagent-e2e.sh contract help-usage registry-auth text-output
```

Live backend validation is split by host capability:

- Hosted CI is the portable gate. It should not assume KVM, Hyper-V, or Apple
  Virtualization.framework access.
- Linux release parity is gated by `.github/workflows/live-linux-parity.yaml`
  on trusted `main` pushes or manual dispatch on a self-hosted runner labeled
  `linux`, `x64`, and `kvm`.
- macOS Apple VF parity is a local/mac-agent lane unless a self-hosted Apple
  silicon runner is explicitly available. Hosted macOS runners are not treated
  as the release source of truth for Virtualization.framework behavior.
- Windows Hyper-V remains experimental and gated by its own self-hosted
  Windows runner when available.

Run the live Linux lane on the self-hosted KVM runner:

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

List scenarios with `scripts/dev/microagent-e2e.sh --list`. Before fresh live
runs, use `scripts/dev/cleanup-temp.sh` in dry-run mode to check for preserved
temporary state from failed tests; pass `--yes` only when the candidates are
safe to delete. Successful E2E scenarios are expected to remove their own
temporary state.

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
lifecycle/substrate, connect/logs/ps, NAT/user/isolated networking, TCP publish,
mediation/vsock transport, supervision/restart behavior, quarantine cleanup,
results, artifacts, attached disks, and text/JSON output on an Apple silicon
host. The `applevf-*` scenarios are targeted backend diagnostics for narrower
failures; `applevf-direct-console` is a direct-supervisor smoke check. Bridged
mode is release-relevant only on hosts with Apple's restricted
`com.apple.vm.networking` entitlement; public ad-hoc builds should instead prove
the fail-closed entitlement error.

## Pull Requests

- Keep changes narrowly scoped.
- Include docs updates when command output, runtime semantics, or operator
  workflows change.
- Prefer JSON/API outputs and tests over log scraping.
- Do not widen this project into policy, orchestration, credential mediation,
  image signing, image scanning, or LLM/tool execution.
- Call out any live smoke tests that could not be run.

## Security

Do not open public issues for security-sensitive reports. Follow
[`SECURITY.md`](SECURITY.md).
