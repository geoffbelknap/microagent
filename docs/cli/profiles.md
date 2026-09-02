---
title: microagent profiles
description: List exact resource profiles.
---

<!-- docs-last-updated -->
_Last updated: 2026-07-25_

```text
microagent profiles
```

`profiles` prints the built-in workspace resource profiles. Profiles are named
shortcuts for memory, CPU count, and rootfs disk size; `create`, `run`, and
`start` accept `--profile <name>`. Use it when you want the exact numbers
behind a profile name.

## Examples

List the profiles, then size a workspace with one:

```bash
microagent profiles
microagent create research --image docker.io/library/ubuntu:24.04 --profile medium
```

## Profiles

| Profile | Memory MiB | CPUs | Disk MiB |
|---|---:|---:|---:|
| `tiny` | 256 | 1 | 512 |
| `small` | 512 | 2 | 1024 |
| `medium` | 2048 | 2 | 8192 |
| `large` | 4096 | 4 | 16384 |

## Flags

`profiles` takes no flags of its own.

See [global flags](index.md#global-flags) for `--output`/`--json`.

## Exit status

`profiles` exits `0` on success.

## Related

- [`create`](create.md) - size a persistent workspace with `--profile`
- [`run`](run.md) - size a one-shot run with `--profile`
- [`start`](start.md) - resize on start with `--profile`
