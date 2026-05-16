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
scripts/dev/microagent-e2e.sh help-usage text-output
```

The full suite is gated by `.github/workflows/live-linux-parity.yaml` on a
self-hosted Linux runner labeled `linux`, `x64`, and `kvm`:

```bash
scripts/dev/microagent-e2e.sh
```

Live Firecracker tests must run outside sandboxed environments on Linux hosts
with KVM, `/dev/vhost-vsock`, Firecracker on `PATH` or `MICROAGENT_FIRECRACKER`,
and the network prerequisites documented by
`scripts/dev/microagent-e2e-linux-network-setup.sh`. Apple VF tests must run on
macOS with the supervisor built and signed as described in the docs.

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
