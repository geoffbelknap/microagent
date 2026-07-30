---
title: microagent kernel
description: List, check, install, or verify the guest kernel.
---

<!-- docs-last-updated -->
_Last updated: 2026-07-30_

```text
microagent kernel list [--all] [--backend <name>] [--arch <arch>]            List available kernels
microagent kernel check [--backend <name>] [--arch <arch>]                   Check the installed kernel
microagent kernel install [--channel <ch>] [--version <ver>] [--url <url>]   Install a kernel
    [--from <path>] [--sha256 <sum>] [--out <path>]
microagent kernel verify [--path <path>] [--sha256 <sum>]                    Verify a kernel checksum
```

`kernel` manages the guest kernel the microVMs boot. Most users can stick with
`microagent run IMAGE [COMMAND ARG...]` and let `microagent` install the latest
signed kernel automatically. Use `kernel` when you need to list, check, or
install a specific kernel.

Architecture flags accept `arm64`/`aarch64` or `amd64`/`x86_64`. Other values
are rejected before microagent resolves a managed kernel path, reads a file,
downloads a manifest, or writes a kernel.

Available kernels come from a cryptographically signed manifest on
`kernels.microagent.sh`. `list`, `check`, and `install` fetch that manifest and
verify it against a TUF root embedded in the binary before trusting any entry.
A tampered or unsigned manifest yields an error rather than a bad kernel.

## Examples

List the kernels available for this host:

```bash
microagent kernel list
```

Check whether the installed kernel is current - and whether any gap is
security-relevant:

```bash
microagent kernel check
```

Install the latest signed kernel, or a specific version or channel:

```bash
microagent kernel install
microagent kernel install --version 6.1.155
microagent kernel install --channel lts
```

Kernels are published on channels (default `lts`); `--channel` selects one.

Install from a URL with an explicit checksum (custom kernel outside the manifest):

```bash
microagent kernel install \
  --url https://example.com/Image \
  --sha256 4bbe8b2fd19f78fea4bf02d52a67482227a896c90a63f272b6a084fa46a416c0
```

Verify an existing kernel:

```bash
microagent kernel verify \
  --path ~/.microagent/kernels/linux-kvm/amd64/Image \
  --sha256 4bbe8b2fd19f78fea4bf02d52a67482227a896c90a63f272b6a084fa46a416c0
```

## `list`

`list` prints the kernels available in the signed manifest for the host
backend and architecture, or every backend with `--all`.

| Flag | Description |
|---|---|
| `--all` | List kernels for all backends/architectures |
| `--arch <arch>` | Guest architecture (`arm64`/`aarch64`, `amd64`/`x86_64`) |
| `--backend <name>` | Backend identity override |

## `check`

`check` reports whether the installed kernel is `current`, `optional` (behind
the latest but at or above the security floor), `security` (below the floor -
missing security fixes), or `unknown`. The installed version is resolved by
matching the local kernel's checksum against the signed manifest.

| Flag | Description |
|---|---|
| `--arch <arch>` | Guest architecture (`arm64`/`aarch64`, `amd64`/`x86_64`) |
| `--backend <name>` | Backend identity override |

## `install`

With no options, `install` downloads the latest signed kernel for the host
backend and architecture. `--version` pins a specific manifest version; `--url`
or `--from` install a custom kernel outside the manifest.

| Flag | Description |
|---|---|
| `--version <ver>` | Install a specific manifest version (default: latest) |
| `--url <url>` | Download URL (custom kernel) |
| `--from <path>` | Local kernel path (custom kernel) |
| `--sha256 <sum>` | Expected SHA-256 |
| `--out <path>` | Output path (defaults to the writable kernel path for the host) |
| `--arch <arch>` | Guest architecture (`arm64`/`aarch64`, `amd64`/`x86_64`) |
| `--backend <name>` | Backend identity override |

## `verify`

`verify` checks that a kernel file matches an expected SHA-256. Both flags are
optional: `--path` defaults to the installed kernel path for the host. Without
`--sha256` the command only reports the computed sum with `"ok": false` and
`"verified": false` — it is not a verification. Pass `--sha256` (for example a
value from [`kernel list`](#list)) to actually verify against a trusted hash.

| Flag | Description |
|---|---|
| `--path <path>` | Kernel path (default: the installed kernel path) |
| `--sha256 <sum>` | Expected SHA-256; omit to print the computed sum |
| `--arch <arch>` | Guest architecture (`arm64`/`aarch64`, `amd64`/`x86_64`) |
| `--backend <name>` | Backend identity override |

## Default paths

| Host | Default kernel path |
|---|---|
| Apple VF, arm64 | `~/.microagent/kernels/apple-vf/arm64/Image` |
| Linux KVM, amd64 | `~/.microagent/kernels/linux-kvm/amd64/Image` |

## Exit status

`kernel` subcommands exit `0` on success. They exit nonzero when the manifest
cannot be fetched or verified, an architecture is unsupported, the download
fails, the checksum does not match, or the kernel file cannot be read or written.

## Related

- [`doctor`](/cli/doctor/) - reports whether the kernel is installed
- [Backends](/concepts/backends/) - kernel expectations per backend
