---
title: Deliver secrets
description: Get credentials into the guest without writing them to disk, plus on-demand fetch and audit.
---

<!-- docs-last-updated -->
_Last updated: 2026-07-13_

Use secrets when a workload needs credentials without writing them into the
rootfs, the manifest, or a snapshot. The guest can read materialized secrets
from `/run/secrets`, or fetch declared secrets on demand. microagent is a
secret conduit, not a store: it passes operator-owned plaintext through
(loudly warned) or resolves a reference from an external secret manager,
holding the value only in host process memory.

A secret is declared as `NAME=<scheme>:<ref>`. The reference names *where* the
value lives, never the value itself, so it is safe on a command line. The
[`secret`](/cli/secret/) reference is the canonical source for schemes and
semantics; this guide is the walkthrough.

## 1. Check that a reference resolves

`secret check` resolves references and reports byte lengths without ever
printing the value:

```bash
export API_TOKEN=tk-demo-0123456789abcdef0123456789abcdef
microagent secret check API_KEY=env:API_TOKEN
```

```text
API_KEY  ok  source=env  bytes=40  warning: secret scheme "env" is plaintext: not encrypted at rest, not for production
```

:::caution
`env:`, `file:`, and `dotenv:` are plaintext schemes - they read your own
host-side environment or files and warn on every resolve. They are fine for
development. For production, use an external manager scheme such as
`vault:<mount>/data/<path>#<field>`, which reads HashiCorp Vault KV v2 using
`VAULT_ADDR`/`VAULT_TOKEN`. For any other secret manager, `helper:<ref>` runs
the executable named by `MICROAGENT_SECRET_HELPER` with `<ref>` as its one
argument and takes the secret from its stdout - e.g.
`API_KEY=helper:prod/api-key` with `MICROAGENT_SECRET_HELPER=/usr/local/bin/op-read`.
Prefer it when your secrets live behind a CLI (1Password, AWS, `pass`, ...)
that microagent has no built-in scheme for.
:::

Unknown schemes, missing schemes, and references that resolve empty all fail
closed - never a silent empty secret. `check` exits nonzero on any failure so
scripts can gate on it.

## 2. Deliver a secret to a run

Declare secrets on `run` or `create` with `--secret`:

```bash
microagent run --secret API_KEY=env:API_TOKEN \
  docker.io/library/alpine:3.20 sh -c "ls -l /run/secrets && wc -c /run/secrets/API_KEY"
```

```text
-r--------    1 root     root            40 Jun 11 09:13 API_KEY
40 /run/secrets/API_KEY
```

The host resolves the reference at start (failing closed before the microVM
boots if it can't), and the guest writes one file per secret into a tmpfs at
`/run/secrets` - memory only, never the rootfs. The workspace manifest stores
the *reference*, and it is re-resolved on every start, so rotating the backing
value takes effect on the next boot.

## 3. Deliver a whole dotenv file

`--secrets-env-file` turns every key in a dotenv file into a delivered secret:

```bash
printf 'DB_PASSWORD=hunter2-but-longer\nAPP_ENV=dev\n' > /tmp/app.env
microagent create app --image docker.io/library/alpine:3.20 --secrets-env-file /tmp/app.env
microagent start app
microagent exec app -- ls /run/secrets
```

```text
APP_ENV
DB_PASSWORD
```

Same plaintext warning applies: the dotenv file is your file, on your disk,
re-read at each start.

## 4. Fetch secrets on demand

Some secrets shouldn't sit in a file at all. `--secret-on-demand` declares a
reference that is never materialized. The host resolves it lazily, per fetch,
so rotation and revocation at the backend take effect immediately:

```bash
microagent create vaulted --image docker.io/library/python:3.12-alpine \
  --secret-on-demand DB_PASSWORD=dotenv:/tmp/app.env#DB_PASSWORD \
  --secrets-audit
microagent start vaulted
```

The guest exposes a UNIX socket at `$MICROAGENT_SECRETS_SOCK`
(`/run/secrets-api.sock`). The workload sends `GET <name>` and reads one JSON
line with the base64-encoded value:

```bash
microagent exec vaulted -- python3 -c "import socket,os; s=socket.socket(socket.AF_UNIX); s.connect(os.environ['MICROAGENT_SECRETS_SOCK']); s.sendall(b'GET DB_PASSWORD\n'); print(s.recv(4096).decode())"
```

```text
{"ok":true,"value":"aHVudGVyMi1idXQtbG9uZ2Vy"}
```

The value lives only in the workload's memory. A name not declared on-demand
is denied.

## 5. Read the audit log

`--secrets-audit` (declared above) makes the host append one record per
access (boot materialization and every on-demand fetch) to a per-workspace
append-only log. Records carry the time, name, access type, and result, never
the value:

```bash
microagent secret audit vaulted
```

```text
2026-06-11T09:16:32.221898563Z	DB_PASSWORD	on-demand	ok
```

## Snapshots scrub the tmpfs

A [snapshot](/guides/snapshots-and-forking/) captures guest RAM, so microagent
purges `/run/secrets` (zero-overwrite, then remove) before the memory file is
written and rehydrates it after resume, restore, or fork - automatically,
fail-closed, no flags. An on-demand value your workload copied into its own
memory is yours to manage; on-demand minimizes residency but cannot guarantee
zero.

## Clean up

```bash
microagent halt app && microagent delete app --yes
microagent halt vaulted && microagent delete vaulted --yes
rm /tmp/app.env
```

## Related

- **Schemes, flags, and delivery semantics in full** - the [`secret`](/cli/secret/) reference.
- **What the trust boundary covers** - [security](/security/).
- **Snapshot purge and rehydrate in context** - [snapshot and fork workspaces](/guides/snapshots-and-forking/).
