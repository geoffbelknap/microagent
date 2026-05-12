---
title: microagent kernel
description: Install or verify a custom kernel.
---

```text
microagent kernel install [--url <url>] [--from <path>] [--sha256 <sum>] [--out <path>]
microagent kernel verify  --path <path> --sha256 <sum>
```

Most users can stick with `microagent run --image ...` and let `microagent`
download the default kernel automatically. `microagent kernel` is the manual
escape hatch.

## `install`

With no options, install the default kernel for the installed host backend and
architecture.

| Flag | Description |
|---|---|
| `--url <url>` | Download URL |
| `--from <path>` | Local kernel path |
| `--sha256 <sum>` | Expected SHA-256 |
| `--out <path>` | Output path (defaults to the writable kernel path for the host) |
| `--arch <arch>` | Guest architecture |

## `verify`

Verify a kernel file matches an expected SHA-256.

| Flag | Description |
|---|---|
| `--path <path>` | Kernel path |
| `--sha256 <sum>` | Expected SHA-256 |

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

## Default paths

| Host | Default kernel path |
|---|---|
| Apple VF, arm64 | `~/.microagent/kernels/apple-vf/arm64/Image` |
| Firecracker, amd64 | `~/.microagent/kernels/firecracker/amd64/Image` |

## Related

- [`doctor`](/cli/doctor/), [Backends](/concepts/backends/)
