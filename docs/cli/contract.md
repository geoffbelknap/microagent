---
title: microagent contract
description: Print the backend-neutral agent runtime contract.
---

<!-- docs-last-updated -->
_Last updated: 2026-06-13_

```text
microagent [--json] contract
```

`contract` prints the backend-neutral runtime contract: lifecycle commands,
states, readiness signals, result fields, artifact channels, mediation fields,
and verification. Backends share the same response shapes; a backend may
support a smaller command set for commands it does not yet implement while
preserving those shapes for the commands it supports. Use it when you're
building an agent
runtime or host integration and need a machine-readable statement of what the
substrate guarantees.

## Examples

```bash
microagent --json contract
```

## Flags

`contract` takes no flags of its own.

See [global flags](/cli/#global-flags) for `--json`/`--text`/`--output`/`--mode`.

## Exit status

`contract` exits `0` on success. In AX mode a failure is written as a
structured error envelope.

## Related

- [Runtime contract](/protocol/runtime-contract/) - the contract, explained
- [Supervisor protocol](/protocol/) - the JSON shapes underneath
- [Backends](/concepts/backends/) - which backends implement which surface
