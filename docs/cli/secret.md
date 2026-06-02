---
title: microagent secret
description: Resolve and validate secret references without writing secrets to disk.
---

<!-- docs-last-updated -->
_Last updated: 2026-06-02_

```text
microagent secret check NAME=<scheme>:<ref> [NAME=<scheme>:<ref> ...]
```

microagent is a **secret conduit, not a store**. It never owns secrets at rest:
it either passes operator-owned plaintext through (loudly warned) or resolves a
reference from an external secret manager, holding the value only in host
process memory. There is no encrypted store, keyring, or `secret set/ls/rm`.

A secret is declared as `NAME=<scheme>:<ref>`, where the scheme selects the
source and the reference names *where* the value lives — never the value
itself. References go on the command line; secret values do not.

## Schemes

| Scheme | Reference | Source |
|---|---|---|
| `env` | `env:VAR` | The CLI process's own environment variable `VAR` (plaintext, warned) |
| `file` | `file:PATH` | The file's raw contents (plaintext, warned) |
| `dotenv` | `dotenv:PATH#KEY` | `KEY` from a dotenv file (plaintext, warned) |
| `vault` | `vault:<mount>/data/<path>#<field>` | HashiCorp Vault KV v2 (secure) |

The three plaintext schemes read the operator's own files on the host and emit a
`not encrypted at rest, not for production` warning on every resolve. The
`vault` scheme reads a KV v2 secret read-only using `VAULT_ADDR` and
`VAULT_TOKEN`; auth, sealed, and not-found conditions surface as clear errors.

Unknown schemes, references missing a scheme, and values that resolve empty all
**fail closed** with an error — never a silent empty secret.

## `secret check`

`check` validates that one or more references resolve. For each entry it reports
`ok`, the source scheme, the resolved value's **byte length**, and any plaintext
warning. It **never prints the secret value**. If any entry fails to resolve,
the command exits non-zero so scripts can gate on it.

## Output

Place the global `--json` flag before the subcommand for a JSON array; see
[global flags](/cli/#global-flags). `--mode ax` (or piping to a non-terminal)
also produces JSON.

## Examples

```bash
export API_TOKEN=...           # operator's own environment
microagent secret check API=env:API_TOKEN
```

```text
API	ok	source=env	bytes=40	warning: secret scheme "env" is plaintext: not encrypted at rest, not for production
```

```bash
export VAULT_ADDR=https://vault.internal:8200
export VAULT_TOKEN=...
microagent --json secret check DB=vault:secret/data/app#db_password
```

```json
[
  {
    "name": "DB",
    "ok": true,
    "source": "vault",
    "bytes": 32
  }
]
```

## Scope

This command is the host-side resolution layer. Delivering resolved secrets into
a workspace guest over vsock (a tmpfs `/run/secrets`, on-demand fetch, and
snapshot purge/rehydrate) is built on top of this layer and is not part of
`secret check`.

## Related

- [`run`](/cli/run/)
- [`create`](/cli/create/)
- [security](/security/)
