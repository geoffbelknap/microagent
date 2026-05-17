---
title: microagent contract
description: Print the backend-neutral agent runtime contract.
---

<!-- docs-last-updated -->
_Last updated: 2026-05-17_

```text
microagent [--json] contract
```

`contract` reports backend-neutral runtime semantics: lifecycle commands,
states, readiness signals, result fields, artifact channels, mediation fields,
and verification. Stable backends implement the full surface; experimental
backends may support a smaller command set while preserving the same response
shapes for supported commands.

The JSON output is intended for agent-runtime builders and host integrations
that need a machine-readable contract.

## Example

```bash
microagent --json contract
```

## Related

- [Runtime contract](/protocol/runtime-contract/)
- [Supervisor protocol](/protocol/)
- [Backends](/concepts/backends/)
