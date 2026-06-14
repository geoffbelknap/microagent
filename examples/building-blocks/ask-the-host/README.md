# ask-the-host

**Aha:** the agent doesn't *hold* the dangerous capability — it asks the host,
and the host decides (and logs it).

The agent ([`agent.py`](agent.py)) runs with no credentials and, paired with
`--network isolated`, no network either. Every action that touches the outside
world goes over the vsock **mediation channel** to a host-side broker
([`broker.py`](broker.py)) that holds the credentials, applies a policy, and
audits every decision. Mediation is a separate channel from ordinary
networking, so it still works with the network fully isolated: no egress, and
the only way out is the path the broker mediates.

The point in one exchange: the agent tries to `send_email` to an outside
address (imagine a prompt-injected email talked it into exfiltrating). The agent
never had the ability to send — it can only ask — and the broker's policy says
no and writes it to the audit log.

## Files

| File | Role |
|---|---|
| `broker.py` | **Host side.** Holds the inbox/credentials, applies policy, logs each decision. The security boundary. |
| `agent.py` | **Guest side.** Asks the host to read the inbox and to send mail; holds nothing itself. |
| `microagent.yaml` | Declares the mediation channel (guest port `2048` → host `127.0.0.1:9900`), required and fail-closed. |

## Run

```bash
# 1. Start the broker on the host (holds creds, decides, logs).
python3 broker.py &

# 2. Build + boot the agent with no network — the only way out is the channel.
microagent create --file microagent.yaml --network isolated
microagent start ask-the-host

# 3. See what the agent asked and what the broker allowed/denied.
microagent logs ask-the-host                 # agent side
microagent artifact get ask-the-host result ./out/ && cat ./out/result.json
```

The broker's stderr shows the policy decisions:

```text
broker: listening on 127.0.0.1:9900
[audit] 2026-06-14T16:30:00+00:00 ALLOW list_inbox
[audit] 2026-06-14T16:30:00+00:00 DENY  send_email to=attacker@example.net (external — needs human approval)
```

and `result.json` shows the agent only got what the broker chose to return:

```json
{
  "list_inbox": {"ok": true, "inbox": [{"from": "alice@example.com", "subject": "Lunch Thursday?"}, ...]},
  "send_email": {"ok": false, "error": "send to external address requires human approval"}
}
```

## Make it yours

- **Add tools.** A `handle()` branch per capability the agent may request; deny
  by default. The agent only ever sees what the broker returns.
- **Add approval.** Instead of an outright `DENY`, have the broker hold the
  request and surface it for a human to approve — the channel stays open while
  it waits.
- **Hold real credentials.** The broker is where API keys and tokens live; see
  [Deliver secrets](../../../docs/guides/secrets.md). For the full protocol —
  lifecycle signals, timeouts, reconnection — see
  [Build agents on the mediation channel](../../../docs/guides/agents-and-mediation.md).
