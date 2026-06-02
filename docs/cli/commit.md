---
title: microagent commit
description: Snapshot a stopped workspace's rootfs back into an OCI image.
---

<!-- docs-last-updated -->
_Last updated: 2026-06-02_

```text
microagent commit <workspace> <image-ref> [options]
```

`commit` snapshots a **stopped** workspace's rootfs into a single-layer OCI
image, closing the loop with the OCI→rootfs realize path used by `create`/`run`.
The image is written to a local OCI image layout under
`<state-dir>/images/oci`; push it to a registry with
[`images push`](/cli/images/) or the `--push` flag.

The rootfs is extracted unprivileged with `debugfs`, so the workspace must be
stopped (committing a running or paused workspace is refused to avoid reading a
live disk). File contents, modes, and symlinks are preserved; because extraction
is unprivileged, original file **ownership is not preserved** — committed layers
record the current user. The committed image's architecture defaults to the
guest architecture.

## Options

| Flag | Description |
|---|---|
| `--push` | Push to the registry immediately after committing |
| `--arch <arch>` | OCI image architecture (defaults to the guest architecture) |
| `--debugfs <path>` | `debugfs` binary path used to extract the rootfs |
| `--backend <name>` | Backend identity override |
| `--state-dir <dir>` | State directory holding the workspace and image layout (default `~/.microagent/`) |

Registry credentials come from the standard Docker config
(`$DOCKER_CONFIG/config.json` or `~/.docker/config.json`), the same as image
pulls.

## Example

```bash
microagent halt research
microagent commit research registry.example.com/team/research:v1
microagent images push registry.example.com/team/research:v1

# Or commit and push in one step:
microagent commit research registry.example.com/team/research:v1 --push
```

## Related

- [`images`](/cli/images/) — `images push` and the local image records
- [`create`](/cli/create/) — realize an OCI image into a workspace
- [`clone`](/cli/clone/)
