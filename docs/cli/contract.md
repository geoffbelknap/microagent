---
title: microagent contract
description: Print the backend-neutral agent runtime contract.
---

```text
microagent [--json] contract
```

`contract` reports the runtime semantics that Firecracker and Apple VF must
share: lifecycle commands, states, readiness signals, result fields, artifact
channels, mediation fields, and verification.

The JSON output is intended for agent-runtime builders and host integrations
that need a machine-readable contract.

## Example

```bash
microagent --json contract
```

## Related

- [Runtime contract](../protocol/runtime-contract.md)
- [Supervisor protocol](../protocol/index.md)
- [Backends](../concepts/backends.md)
