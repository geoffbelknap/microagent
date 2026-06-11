---
title: microagent kernel
description: Install or verify a custom kernel.
---

<!-- docs-last-updated -->
_Last updated: 2026-06-11_

```text
microagent kernel install [--url <url>] [--from <path>] [--sha256 <sum>] [--out <path>]   Install a kernel
microagent kernel verify  --path <path> --sha256 <sum>                                    Verify a kernel checksum
```

`kernel` manages the guest kernel the microVMs boot. Most users can stick with
`microagent run IMAGE [COMMAND ARG...]` and let `microagent` download the
default kernel automatically - `kernel` is the manual escape hatch for custom
kernels and explicit verification.

## Examples

Install the default kernel:

```bash
microagent kernel install
```

Install from a URL with an explicit checksum:

```bash
microagent kernel install \
  --url https://example.com/Image \
  --sha256 4bbe8b2fd19f78fea4bf02d52a67482227a896c90a63f272b6a084fa46a416c0
```

Verify an existing kernel:

```bash
microagent kernel verify \
  --path ~/.microagent/kernels/firecracker/amd64/Image \
  --sha256 4bbe8b2fd19f78fea4bf02d52a67482227a896c90a63f272b6a084fa46a416c0
```

## `install`

With no options, `install` downloads the default kernel for the installed host
backend and architecture.

| Flag | Description |
|---|---|
| `--url <url>` | Download URL |
| `--from <path>` | Local kernel path |
| `--sha256 <sum>` | Expected SHA-256 |
| `--out <path>` | Output path (defaults to the writable kernel path for the host) |
| `--arch <arch>` | Guest architecture |
| `--backend <name>` | Backend identity override |

## `verify`

`verify` checks that a kernel file matches an expected SHA-256.

| Flag | Description |
|---|---|
| `--path <path>` | Kernel path |
| `--sha256 <sum>` | Expected SHA-256 |
| `--arch <arch>` | Guest architecture |
| `--backend <name>` | Backend identity override |

## Default paths

| Host | Default kernel path |
|---|---|
| Apple VF, arm64 | `~/.microagent/kernels/apple-vf/arm64/Image` |
| Firecracker, amd64 | `~/.microagent/kernels/firecracker/amd64/Image` |

## Exit status

`kernel install` and `kernel verify` exit `0` on success; nonzero when the
download fails, the checksum does not match, or the kernel file cannot be read
or written. In AX mode a failure is written as a structured error envelope.

## Related

- [`doctor`](/cli/doctor/) - reports whether the default kernel is installed
- [Backends](/concepts/backends/) - kernel expectations per backend
