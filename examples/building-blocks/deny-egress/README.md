# deny-egress

**Aha:** boot an agent that *physically cannot* reach the network — one flag,
fail-closed — and watch it still do its local work.

`--network isolated` gives the guest no network device at all. There's no
firewall to misconfigure and no allowlist to leak through: the interface simply
isn't there. The agent ([`check.py`](check.py)) proves it by trying to reach the
internet (which must fail), then summarizing a local file (which must succeed).

## Files

| File | Role |
|---|---|
| `microagent.yaml` | Workspace spec — bakes the check script and a local data file, declares the result artifact. |
| `check.py` | Tries one outbound connection (wants it blocked), does local work, writes the verdict. Exits nonzero if egress was *not* blocked. |
| `notes.txt` | A little local data for the agent to work on without a network. |

## Run

```bash
# Build + boot with no guest network device.
microagent create --file microagent.yaml --network isolated
microagent start deny-egress

# Read the verdict.
microagent artifact get deny-egress result ./out/
cat ./out/result.json
```

```json
{
  "egress_blocked": true,
  "egress_detail": "OSError: [Errno 101] Network is unreachable",
  "local_work_ok": true,
  "word_count": 21
}
```

Flip the switch to see the contrast — the same image on the default network
reaches straight out:

```bash
microagent run docker.io/library/python:3.12-slim \
  python3 -c "import socket; socket.create_connection(('1.1.1.1',53),timeout=3); print('reached the internet')"
```

## Why this matters

A personal-assistant or knowledge-worker agent has the lethal combination:
private data, exposure to untrusted content (emails, web pages, documents), and
a way to send things out. Remove the third leg and a prompt-injected agent has
nowhere to exfiltrate *to*. `isolated` is the bluntest, most reliable version of
that — and when the agent still needs to do real outside work, you pair it with
[`ask-the-host`](../ask-the-host/): the only sanctioned path out becomes a vsock
channel a broker you control mediates and audits.
