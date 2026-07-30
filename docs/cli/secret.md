---
title: microagent secret
description: Resolve and validate secret references without writing secrets to disk.
---

<!-- docs-last-updated -->
_Last updated: 2026-07-30_

```text
microagent secret check NAME=<scheme>:<ref> [NAME=<scheme>:<ref> ...]   Validate secret references
microagent secret audit <workspace> [--state-dir <dir>]                 Read a workspace's secret-access log
```

microagent is a secret conduit, not a store. It never owns secrets at rest:
it either passes operator-owned plaintext through (loudly warned) or resolves a
reference from an external secret manager, holding the value only in host
process memory. There is no encrypted store, keyring, or `secret set/ls/delete`.

A secret is declared as `NAME=<scheme>:<ref>`, where the scheme selects the
source and the reference names *where* the value lives - never the value
itself. References go on the command line; secret values do not.

## Examples

Give a workspace an API key from your own environment:

```bash
microagent run IMAGE --secret API_KEY=env:MY_KEY
```

The guest reads the value from `/run/secrets/API_KEY`; see
[Delivery to the guest](#delivery-to-the-guest) for how it gets there.

Validate that a reference resolves:

```bash
export API_TOKEN=...           # operator's own environment
microagent secret check API=env:API_TOKEN
```

```text
API	ok	source=env	bytes=40	warning: secret scheme "env" is plaintext: not encrypted at rest, not for production
```

Check a Vault reference with machine output:

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
`VAULT_TOKEN`; auth, sealed, and not-found conditions return clear errors.

Unknown schemes, references missing a scheme, and values that resolve empty all
fail closed with an error - never a silent empty secret.

## `check`

`check` validates that one or more references resolve. For each entry it reports
`ok`, the source scheme, the resolved value's byte length, and any plaintext
warning. It never prints the secret value. If any entry fails to resolve,
the command exits nonzero so scripts can gate on it.

`secret check` is the host-side resolution layer; it never delivers to a guest.
The delivery described below is built on the same resolver.

## `audit`

`audit` prints the per-workspace secret-access log written when a workspace was
created with `--secrets-audit`:

```bash
microagent secret audit app
microagent --json secret audit app
```

Records carry `at`, `name`, `access` (`materialize`/`on-demand`), and `result`
(`ok`/`denied`/`error`) - never the value. Audit granularity is per-workspace
plus secret name (the vsock channel is anonymous). There is no guest CLI.

## Delivery to the guest

Declare secrets on `run`, `create`, or `start`:

```text
microagent run IMAGE --secret API_KEY=vault:secret/data/app#api_key
microagent create --name app --secret API_KEY=env:CI_TOKEN --secrets-env-file /etc/app.env
```

- `--secret NAME=<scheme>:<ref>` (repeatable) and `--secrets-env-file PATH` are
  stored in the workspace manifest as references and paths - never values -
  and are re-resolved on every start/resume. `NAME` must be a safe
  identifier (the shape of an environment-variable name).
- At start the host resolves every reference (failing closed: an unresolved
  reference aborts the start before the VM boots) and serves the resolved bundle
  on a dedicated vsock port advertised to the guest via the kernel cmdline.
- The guest mounts a tmpfs at `/run/secrets` (`0700`) and writes one file
  per secret (`/run/secrets/<NAME>`, mode `0400`, value verbatim). Values never
  touch the rootfs, the manifest, or any disk snapshot. If delivery fails the
  guest aborts boot - a workload never runs without its declared secrets.
- Backing credentials (such as `VAULT_TOKEN`) stay on the host; only resolved
  values cross vsock, which is a host-to-guest-only transport.

## On-demand secrets and the audited tier

Some secrets are better fetched only when needed rather than written to a tmpfs
file. Declare these with `--secret-on-demand`:

```bash
microagent create --name app \
  --secret-on-demand DB_PASSWORD=vault:secret/data/app#db \
  --secrets-audit
```

- `--secret-on-demand NAME=<scheme>:<ref>` (repeatable) is persisted as a
  reference and never materialized to `/run/secrets`. The host resolves it
  lazily, per fetch, so backend rotation/revocation takes effect immediately.
  A name not declared on-demand is denied.
- When on-demand secrets are declared, the guest runs an agent on a UNIX socket
  whose path is exported to the workload as `$MICROAGENT_SECRETS_SOCK`
  (`/run/secrets-api.sock`, mode `0600`). The workload sends `GET <name>\n` and
  reads one JSON line:

  ```text
  GET DB_PASSWORD
  {"ok":true,"value":"<base64-of-the-value>"}
  ```

  On failure the line is `{"ok":false,"error":"..."}`. The value lives only in
  the workload's memory - never on the rootfs, the tmpfs, or any disk snapshot.
- `--secrets-audit` (workspace-level) turns on the audited tier: the host appends
  one record per access (boot materialization and every on-demand fetch) to a
  per-workspace append-only log. Read it with [`secret audit`](#audit).

## Snapshots: purge and rehydrate

A snapshot captures the full guest RAM, so the `/run/secrets` tmpfs would land in
the memory image (and travel into every restore and fork). When a workspace with
materialized secrets is snapshotted, microagent automatically:

- **Purges** `/run/secrets` while the VM is still running, just before snapshot
  create's internal pause - each file is overwritten with zeros (scrubbing the
  captured RAM) and removed. Fail-closed: if the guest can't confirm the
  purge, snapshot create is aborted and no memory file is written, so a
  snapshot of a secrets-bearing workspace never contains un-purged plaintext.
- **Rehydrates** the source after it resumes, and rehydrates the guest after a
  `--from-snapshot` restore or fork - re-fetching the bundle and rewriting the
  files (references are re-resolved, so a fork gets its own resolved secrets).

This is automatic when secrets are declared; there are no flags. Plain `pause` /
`resume` is unaffected (it keeps RAM in memory, writes no disk artifact).
`SecretsPurged` is recorded in the snapshot manifest as provenance.

Boundary: only the tmpfs microagent owns is scrubbed. An on-demand value a
workload copied into its own memory is captured in the snapshot and cannot be
scrubbed - on-demand minimizes residency but does not guarantee zero.

Forensic captures intentionally skip this purge so investigators can preserve
volatile evidence. They may therefore contain every secret present in guest
memory, including microagent-delivered values and workload copies. Forensic
captures are non-restorable, retained evidence; store them outside
workload-readable paths, restrict access, protect every copy, and delete them
under your evidence-retention process. See [forensic
captures](/cli/snapshot/#forensic-captures).

## Flags

`check` takes no flags of its own; `audit` takes only `--state-dir`.

| Flag | Description |
|---|---|
| `--state-dir <dir>` | State directory holding the workspace audit log (`audit` only, default `~/.microagent/`) |

See [global flags](/cli/#global-flags) for `--output`/`--json`.
Piping to a non-terminal also produces JSON.

## Exit status

`secret check` exits `0` when every reference resolves; nonzero when any entry
fails to resolve. `secret audit` exits `0` when the workspace is found; nonzero
when it cannot be found or the log cannot be read.

## Related

- [`run`](/cli/run/) - declare secrets on a one-shot run
- [`create`](/cli/create/) - declare secrets on a persistent workspace
- [Deliver secrets](/guides/secrets/) - the delivery walkthrough
- [security](/security/) - the trust boundary this design follows
