---
title: microagent apply
description: Apply supported workspace spec changes without rebuilding the rootfs.
---

```text
microagent apply --file <path> [--state-dir <dir>]
```

`apply` updates the persisted workspace manifest from a spec file. It is for
small declarative changes that do not need a rootfs rebuild.

Today it supports:

- restart policy changes
- network intent changes while the workspace is stopped
- live Firecracker port-forward host bind changes when the workspace is running

For a running Firecracker workspace, `apply` can live-reload this kind of
change:

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
resources, files, setup, image, or service command still require `stop`/`start`
or recreating the workspace.

## Example

```bash
microagent apply --file ./homebridge.yaml
```

If the workspace is running and only the Firecracker host bind changed,
`apply` restarts the host-side port-forwarder and leaves the VM running.

## Flags

| Flag | Description |
|---|---|
| `--file <path>` | Workspace spec file |
| `--state-dir <dir>` | State directory holding the workspace record |
| `--backend <name>` | Backend identity override |
| `--arch <arch>` | Guest architecture |
| `--supervisor <path>` | Override the installed host backend supervisor path |
