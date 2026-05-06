---
title: microagent contract
description: Print the backend-neutral agent runtime contract.
---

```text
microagent contract [--json]
```

`contract` reports the runtime semantics that Firecracker and Apple VF must
share: lifecycle commands, states, readiness signals, result fields, artifact
channels, mediation fields, verification, and parity rules.

The JSON output is intended for agent-runtime builders and backend conformance
tests.

## Example

```bash
microagent contract --json
```

## Related

- [Runtime parity contract](/protocol/runtime-contract/)
- [Supervisor protocol](/protocol/)
- [Backends](/concepts/backends/)
