# kill-switch

**Aha:** something looked like an injection — one `quarantine` severs the
agent's network *and* mediation instantly, while keeping its disk and logs for
forensics.

`quarantine` is the containment verb, not a shutdown. It cuts a running
workspace's host-side network and mediation, records the state as
`quarantined`, and leaves the VM process, disk, serial logs, and `events.json`
in place so you can investigate. The agent ([`worker.py`](worker.py)) loops and
reports whether it can still reach out each round, so the cut is visible the
moment it happens.

## Files

| File | Role |
|---|---|
| `microagent.yaml` | Workspace spec — bakes the long-running worker. |
| `worker.py` | Loops forever, reporting `egress=ok` / `egress=BLOCKED` each round. |

## Run

```bash
# Boot a long-running agent with normal outbound network.
microagent create --file microagent.yaml --network nat
microagent start kill-switch

# Watch it work — each round reports whether it can still reach out.
microagent logs --follow kill-switch
#   2026-06-14T16:40:02Z round=2 egress=ok
#   2026-06-14T16:40:04Z round=3 egress=ok

# Something looks wrong. Hit the kill switch (from another shell):
microagent quarantine kill-switch

# The VM keeps running, but it's cut off — the logs flip:
#   2026-06-14T16:40:10Z round=6 egress=BLOCKED
microagent status kill-switch        # state: quarantined

# Forensics are preserved. Inspect, then take it down.
microagent events kill-switch
microagent kill kill-switch
```

A quarantined workspace is a forensic state: you must `halt`, `stop`, or `kill`
it before it can `start` again.

## Why this matters

When an agent that reads untrusted content starts behaving oddly, you want to
contain it *now* without destroying the evidence of what it did. `halt` and
`stop` shut the VM down; `quarantine` freezes its reach while keeping the VM and
its state intact — the difference between pulling the plug and isolating a
patient. Pair it with [`ask-the-host`](../ask-the-host/): a broker that spots a
policy violation can quarantine the workspace as its response.
