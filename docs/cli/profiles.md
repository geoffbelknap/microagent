---
title: microagent profiles
description: List exact resource profiles.
---

```text
microagent profiles
```

`profiles` prints the built-in workspace resource profiles. Profiles are named
shortcuts for memory, CPU count, and rootfs disk size. `create`, `run`, and
`start` accept `--profile <name>`.

## Profiles

| Profile | Memory MiB | CPUs | Disk MiB |
|---|---:|---:|---:|
| `tiny` | 256 | 1 | 512 |
| `small` | 512 | 2 | 1024 |
| `medium` | 2048 | 2 | 8192 |
| `large` | 4096 | 4 | 16384 |

## Example

```bash
microagent profiles
microagent create research --image docker.io/library/ubuntu:24.04 --profile medium
```

## Related

- [`create`](create.md)
- [`run`](run.md)
- [`start`](start.md)
