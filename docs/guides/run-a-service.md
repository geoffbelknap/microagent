---
title: Run a service
description: Run Postgres in a workspace with a published port, a named volume, and a restart policy.
---

<!-- docs-last-updated -->
_Last updated: 2026-07-13_

This example runs Postgres 17 in a microVM, reachable from the host on
`127.0.0.1:5432`, with its data on a named volume that survives halt and
restart. The same pattern works for any long-running service image.

## 1. Create a volume for the data

```bash
microagent volume create pgdata --size-mib 1024
```

The database files live here, independent of the workspace - you can delete
and recreate the server without touching the data.

## 2. Create the workspace

```bash
microagent create pg \
  --image docker.io/library/postgres:17 \
  --image-command \
  --profile medium \
  -e POSTGRES_PASSWORD=devpassword \
  -e PGDATA=/var/lib/postgresql/data/pgdata \
  -v pgdata:/var/lib/postgresql/data \
  -p 127.0.0.1:5432:5432 \
  --restart on-failure
```

Five flags do the work:

- `--image-command` runs the image's Entrypoint/Cmd - the stock postgres
  entrypoint - as the long-running VM service. For a command the image doesn't
  define, use `--service-command "<cmd>"` instead.
- `-e PGDATA=...` points Postgres at a *subdirectory* of the mount. An ext4
  volume has a `lost+found` directory at its root, and initdb refuses a
  non-empty data directory - the subdirectory sidesteps that.
- `-v pgdata:/var/lib/postgresql/data` attaches the named volume.
- `-p 127.0.0.1:5432:5432` publishes the guest port to the host; no special
  network mode needed.
- `--restart on-failure` records the restart policy, enforced when the
  workspace runs under [`supervise`](/cli/supervise/).

A throwaway dev password in an env var is fine here; for real credentials,
see [deliver secrets](/guides/secrets/).

## 3. Start it and wait for ready

```bash
microagent start pg
microagent exec pg -- pg_isready -U postgres
```

```text
/var/run/postgresql:5432 - accepting connections
```

First boot runs initdb, so give it a few seconds before the ready check
passes.

## 4. Use it

From inside the guest, with the image's own psql over the local socket:

```bash
microagent exec pg -- psql -U postgres -c "CREATE TABLE visits (id serial PRIMARY KEY, note text);"
microagent exec pg -- psql -U postgres -c "INSERT INTO visits (note) VALUES ('first boot');"
```

```text
CREATE TABLE
INSERT 0 1
```

From the host, through the published port, with any Postgres client:

```bash
psql postgres://postgres:devpassword@127.0.0.1:5432/postgres -c "SELECT 1;"
```

The host listener accepts the TCP connection and the supervisor bridges it
over vsock to the guest's port 5432.

## 5. Halt, restart, and check the data survived

```bash
microagent halt pg
microagent start pg
microagent exec pg -- pg_isready -U postgres
microagent exec pg -- psql -U postgres -c "SELECT * FROM visits;"
```

```text
 id |    note
----+------------
  1 | first boot
(1 row)
```

The table is still there. The volume holds the database; the workspace just
runs the server.

## Clean up

```bash
microagent stop pg
microagent delete pg --yes
microagent volume delete pgdata
```

`stop` asks the service to shut down gracefully. If the guest doesn't exit in
time, the workspace is marked failed and you follow up with [`kill`](/cli/kill/).
Run `volume delete` last: it removes the data for real.

## Related

- [`examples/homebridge`](https://github.com/geoffbelknap/microagent/tree/main/examples/homebridge) — a complete service: setup script, supervised service command, `restart: always`, and a published port.
- [Volumes and data](/guides/volumes-and-data/) — more on volumes, disks, and bundles.
- [Networking guide](/guides/networking/) — give a workspace outbound access and publish a port.
- [Networking concepts](/concepts/networking/) — port-forward mechanics and network modes.
