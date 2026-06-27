---
title: Build agents on the mediation channel
description: Declare the guest-to-host vsock contract, listen on the host, and loop requests in the agent.
---

<!-- docs-last-updated -->
_Last updated: 2026-06-27_

Use mediation when an agent needs to keep running while your host control plane
sends work and reads results. Requests stream in, results stream out, and the
workspace does not restart between them. This page covers the declaration
syntax, the host listener, the agent loop, and the failure semantics.

The mediation channel is a guest-to-host vsock contract, separate from
ordinary networking. The agent connects to a vsock port inside the guest, the
host listens at a TCP target, and the supervisor proxies bytes between them.
The [simple-agent guide](/guides/simple-agent/) ships work in with
`microagent cp` and reads results with `microagent --json result` - one
request per restart. Mediation is what you reach for when that stops being
enough.

| Layer | File-based (simple-agent) | Mediation |
|---|---|---|
| Request delivery | `cp` a file per run | Host sends `WorkRequest` over the channel |
| Result delivery | `--json result` reads an artifact | Agent sends `WorkResult` back |
| Agent lifecycle | One request per restart | One process, many requests |
| Host's job | Run `microagent` commands | Run a listener that speaks the protocol |

The protocol shapes - `WorkRequest`, `WorkResult`, `LifecycleSignal`,
`ConstraintAck` - stay the same. Only the transport changes.

## 1. Declare the channel

In the workspace spec:

```yaml
# microagent.yaml
mediation:
  enabled: true
  required: true
  port: 2048
  target: 127.0.0.1:9900
  failClosed: true
```

Or on the command line:

```bash
microagent create agent --image docker.io/library/python:3.12-alpine \
  --mediation 2048=127.0.0.1:9900
```

`port` is the guest-side vsock port the agent connects to; `target` is the
host-side TCP address the supervisor forwards to. The CLI form declares the
channel required and fail-closed (the right default). Pass
`--mediation-optional` only for development paths where the workspace may run
without a host listener.

## 2. Run the host listener

The listener is yours - it speaks newline-delimited JSON (or whatever framing
you choose) and dispatches your control plane's work. A minimal one that
serves a single request:

```python
# listener.py - accept one agent, send one request, read the result
import json, socket

srv = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
srv.bind(("127.0.0.1", 9900))
srv.listen(1)

while True:
    conn, _ = srv.accept()
    reader = conn.makefile("r")
    line = reader.readline()
    if not line:
        continue                            # readiness probe: connect-and-close
    break                                   # the agent connected

signal = json.loads(line)                   # {"signal": "ready"}
request = {"request_id": "req-001", "principal": "operator", "content": "ping"}
conn.sendall(json.dumps(request).encode() + b"\n")
while True:
    msg = json.loads(reader.readline())
    if "signal" in msg:                     # lifecycle signals share the channel
        print("signal:", msg["signal"])
        continue
    print("result:", msg)
    break
```

One non-obvious line: microagent itself probes the target with a bounded TCP
connect to compute `mediationReady`, so a listener must tolerate
connect-and-close probes (that's the `if not line: continue`).

A production listener handles lifecycle signals, timeouts, reconnection, and
concurrent agents; the loop shape stays read a signal, dispatch when ready,
read the result, repeat.

## 3. Connect from the agent

Inside the guest, the agent opens the vsock port and loops. CID 2 is the
conventional host address; AF_VSOCK needs a Linux guest, which every
microagent workspace is.

```python
# agent.py - runs inside the guest
import json, socket

CID_HOST, VSOCK_PORT = 2, 2048
sock = socket.socket(socket.AF_VSOCK, socket.SOCK_STREAM)
sock.connect((CID_HOST, VSOCK_PORT))
reader = sock.makefile("r")

def send(msg):
    sock.sendall(json.dumps(msg).encode() + b"\n")

def process(req):
    # Your model loop goes here. This stub just answers ping with pong.
    print("agent: got request", req["request_id"], flush=True)
    return {"request_id": req["request_id"], "ok": True, "content": "pong"}

send({"signal": "ready"})
while True:
    line = reader.readline()
    if not line:
        break                                # host closed the channel
    req = json.loads(line)
    send({"signal": "accepting", "request_id": req["request_id"]})
    send(process(req))
    send({"signal": "completed", "request_id": req["request_id"]})
```

What carries over from the file-based agent unchanged: the `WorkRequest` /
`WorkResult` shapes, and the model call inside `process()`. What changes: no
more `/workspace/input.json` or result file, and the agent is long-lived:
it runs until the host closes the channel. Pick one framing rule (newline-
delimited JSON is simplest, length-prefixed is sturdier) and document it.

Run the pair end to end:

```bash
python3 listener.py &
microagent start agent
microagent exec agent --stdin ./agent.py -- sh -c "cat > /tmp/agent.py && python3 /tmp/agent.py"
```

```text
agent: got request req-001
signal: accepting
result: {'request_id': 'req-001', 'ok': True, 'content': 'pong'}
```

(In a real deployment the agent is the workspace's entrypoint, not an `exec` -
this is the wiring check.)

## 4. Understand the failure semantics

Required mediation fails closed at the channel, and the workspace's readiness
reports it. Start the workspace without a listener and `--json status` shows:

```text
"mediationReady": {
  "ready": false,
  "detail": "mediation required=true failClosed=true port=2048 target=127.0.0.1:9900; mediation target unreachable ...",
  "error": "required mediation target is unreachable"
}
```

Gate on `mediationReady` before dispatching work. While the target is
unreachable the agent's vsock connection cannot carry traffic - the channel is
severed, not silently degraded. If the channel breaks with a request in
flight, the agent can't deliver its result; treat in-flight requests as
needing retry, with `request_id` as the deduplication key.

The agent emits `LifecycleSignal` messages (`ready`, `accepting`, `completed`,
`mediation_broken`, `constraints_outdated`, `quarantined`) on the same
channel. Hosts tell signals from results by shape: signals carry a `signal`
field, requests carry `request_id` and `principal`.

[`quarantine`](/cli/quarantine/) severs mediation along with networking -
that's the containment story the channel is designed to support.

## Clean up

```bash
microagent halt agent
microagent delete agent --yes
```

## Related

- **Egress for credentials** - mediation carries requests, not API keys. Route model calls through a host-side proxy that holds the key; see [agency](https://github.com/geoffbelknap/agency) for an implementation.
- **The file-based flow this replaces** - [build a simple agent](/guides/simple-agent/).
- **Where mediation sits in the network model** - [Networking](/concepts/networking/).
- **Snapshot interplay** - mediation sessions reset on restore and fork; see [snapshots and forking](/guides/snapshots-and-forking/).
