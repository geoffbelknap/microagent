---
title: microagent contract
description: Print the runtime fields integrations rely on.
---

<!-- docs-last-updated -->
_Last updated: 2026-08-14_

```text
microagent [--json] contract
```

`contract` prints the fields a host integration can rely on: lifecycle
commands, states, readiness signals, result fields, artifact channels,
operation-level durability guarantees, and persistence tiers. It also reports
artifact ownership, mediation fields, verification, and the capabilities
declared by each supported backend. The `backendCapabilities` field identifies declared
capability support; use `microagent doctor` to determine whether those
capabilities are ready on the current host. Use `contract` when you are building
an agent runtime or host integration and need a JSON description of what
microagent reports.

## Examples

```bash
microagent --json contract
```

## Flags

`contract` takes no flags of its own.

See [global flags](/cli/#global-flags) for `--output`/`--json`.

## Exit status

`contract` exits `0` on success.

## Related

- [State and identity](/concepts/state-and-identity/) - lifecycle states and readiness
- [Host requirements](/concepts/backends/) - what the current host must provide
