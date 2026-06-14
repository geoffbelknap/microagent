# creds-it-cant-read

**Aha:** the agent uses a credential that never lands in its filesystem or a
snapshot — and every access is in an audit log.

A `--secret-on-demand` secret is never materialized to `/run/secrets`. The agent
([`agent.py`](agent.py)) fetches it over the secrets socket
(`$MICROAGENT_SECRETS_SOCK`) at the moment it needs it, uses it, and never
writes it down. `--secrets-audit` records every fetch host-side. A name the
operator didn't declare on-demand is refused.

## Files

| File | Role |
|---|---|
| `microagent.yaml` | Workspace spec — bakes the agent, declares the result artifact. |
| `agent.py` | Fetches `API_TOKEN` on demand and uses it; confirms it's not on disk and that an undeclared name is denied. |

## Run

```bash
# A demo value on the host. (env: is plaintext — fine for a demo; use vault: in production.)
export API_TOKEN=tk-demo-0123456789abcdef0123456789abcdef

# Declare the secret on-demand (never written to tmpfs) and turn on the audit log.
microagent create --file microagent.yaml \
  --secret-on-demand API_TOKEN=env:API_TOKEN \
  --secrets-audit
microagent start creds-it-cant-read

# What the agent saw — note the value itself never appears.
microagent artifact get creds-it-cant-read result ./out/ && cat ./out/result.json

# What the host recorded — one line per access, never the value.
microagent secret audit creds-it-cant-read
```

```json
{
  "fetched": "API_TOKEN",
  "length_bytes": 40,
  "on_disk": false,
  "undeclared_secret_denied": true
}
```

```text
2026-06-14T16:40:00Z   API_TOKEN   on-demand   ok
```

## Why this matters

A credential that lives in the guest filesystem is a credential a prompt-injected
or buggy agent can read, copy, and leak — and a snapshot quietly carries it
forever. On-demand fetch keeps the value in the host's control and the guest's
memory only, scopes it to the moment of use, and leaves an audit trail you can
review. The broker in [`ask-the-host`](../ask-the-host/) is the natural place to
hold these. For schemes (including `vault:`) and the full delivery semantics,
see [Deliver secrets](../../../docs/guides/secrets.md).
