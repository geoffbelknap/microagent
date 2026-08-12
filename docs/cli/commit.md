---
title: microagent commit
description: Turn a stopped workspace's rootfs into an OCI image.
---

<!-- docs-last-updated -->
_Last updated: 2026-08-12_

```text
microagent commit <workspace> <image-ref> [options]
```

`commit` snapshots a stopped workspace's rootfs into a single-layer OCI
image, closing the loop with the OCI→rootfs realize path used by `create`/`run`.
The image is written to a local OCI image layout under
`<state-dir>/images/oci`; push it to a registry with
[`image push`](/cli/image/) or the `--push` flag.

By default, `<image-ref>` must start with `local/`; loopback registry refs are
also accepted for local development. A globally meaningful registry target
requires `--allow-registry-shadow`. The override is explicit because committed
images resolve locally before the same reference is fetched from a registry.

The rootfs is extracted unprivileged with `debugfs`, so the workspace must be
stopped (committing a running or paused workspace is refused to avoid reading a
live disk). Before extraction, `commit` runs `e2fsck` to reconcile the ext4
filesystem and stops if that check cannot complete. For a live memory-plus-disk
checkpoint instead of a distributable image, use [`snapshot`](/cli/snapshot/).
File contents, modes, and symlinks are preserved; because extraction is
unprivileged, original file ownership is not preserved - committed layers
record the current user. The committed image's architecture defaults to the
guest architecture. OCI runtime defaults recorded with the workspace — user,
environment, entrypoint/command, working directory, stop signal, exposed
ports, volumes, and labels — are copied into the new image config.

## Examples

Halt, commit, and push:

```bash
microagent halt research
microagent commit research registry.example.com/team/research:v1 --allow-registry-shadow
microagent image push registry.example.com/team/research:v1
```

Or commit and push in one step:

```bash
microagent commit research registry.example.com/team/research:v1 --push --allow-registry-shadow
```

## Flags

Common flags:

- `--push` - push to the registry in the same step
- `--allow-registry-shadow` - allow an explicit registry target
- `--arch <arch>` - only when the image should target a non-guest architecture

The complete set:

| Flag | Description |
|---|---|
| `--push` | Push to the registry immediately after committing |
| `--allow-registry-shadow` | Allow a commit target whose identity belongs to a registry |
| `--arch <arch>` | OCI image architecture (defaults to the guest architecture) |
| `--debugfs <path>` | `debugfs` binary path used to extract the rootfs |
| `--backend <name>` | Backend identity override |
| `--state-dir <dir>` | State directory holding the workspace and image layout (default `~/.microagent/`) |

Registry credentials resolve without any Docker dependency, the same as image
pulls: from `$REGISTRY_AUTH_FILE` (the convention shared with
Podman/Skopeo/Buildah) or `~/.microagent/auth.json` (written by
[`microagent registry login`](/cli/registry/)). Docker's config is never read.

## Exit status

`commit` exits `0` on success. It exits nonzero when the target reference is not
allowed or the workspace cannot be found, is running, or is paused. It also
exits nonzero when filesystem reconciliation or rootfs extraction fails, or -
with `--push` - the registry push fails.

## Related

- [`image`](/cli/image/) - `image push` and the local image records
- [`create`](/cli/create/) - realize an OCI image into a workspace
- [`clone`](/cli/clone/) - copy a workspace without making an image
