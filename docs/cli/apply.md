---
title: microagent apply
description: Apply supported workspace spec changes without rebuilding the rootfs.
---

<!-- docs-last-updated -->
_Last updated: 2026-08-12_

```text
microagent apply --file <path> [--state-dir <dir>]
```

`apply` updates the persisted workspace manifest from a spec file. It is for
small declarative changes that do not need a rootfs rebuild.

Today it supports:

- restart policy changes
- network intent changes while the workspace is stopped
- live port-forward host bind changes when the workspace is running, provided
  the backend supports live network apply (otherwise `apply` errors and asks
  for a halt/start)
- stopped-workspace egress mode and allowlist changes from the spec's `agent`
  block; `lockAllowlist: true` replaces the allowlist and clears old
  passthrough grants

## Examples

Apply an updated spec to its workspace:

```bash
microagent apply --file ./homebridge.yaml
```

If the workspace is running and only the host bind changed, `apply` restarts
the host-side port forwarder and leaves the VM running. It can live-reload this
kind of change:

```yaml
network:
  mode: user
  forwards:
    - host: 0.0.0.0
      hostPort: 8581
      guestPort: 8581
      protocol: tcp
```

The host bind can change, but the network mode, host port, guest port, and
protocol must stay the same. Changes to ports, guest wiring, network mode,
resources, files, setup, image, service command, or a running workspace's
egress policy still require `halt`/`start` or recreating the workspace.

## Flags

`--file` names the spec to apply; the other flags matter only off the
defaults.

| Flag | Description |
|---|---|
| `--file <path>` | Workspace spec file |
| `--state-dir <dir>` | State directory holding the workspace record (default `~/.microagent/`) |
| `--backend <name>` | Backend identity override |
| `--arch <arch>` | Guest architecture |
| `--supervisor <path>` | Override the installed host backend supervisor path |

See [global flags](/cli/#global-flags) for `--output`/`--json`/`--supervisor`.

## Unsupported changes while running

`apply` does not silently no-op an unsupported change. While the workspace
is running, only a live host-bind change is applied. Anything more makes
`apply` error and point you at halt and start; nothing is written. That
includes a different network mode, added or removed forwards, changed host or
guest ports, and any egress policy change. When the spec matches the current
manifest, `apply` reports the workspace state with no applied changes.

## Exit status

`apply` exits `0` when the changes are applied, or when the spec already
matches the manifest. It exits nonzero when the workspace cannot be found, the
spec is invalid, or the requested change is unsupported while the workspace is
running.

## Related

- [`create`](/cli/create/) - create the workspace the spec describes
- [`spec`](/cli/spec/) - the full workspace spec format
- [`status`](/cli/status/) - confirm the applied state
