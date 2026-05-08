---
title: Wire up the mediation channel
description: Move the body from one-request-per-restart to a stream of requests over a guest-to-host vsock contract.
---

The [simple-agent recipe](simple-agent.md) ships work into the body via `microagent cp` and retrieves it via `microagent --json result`. That works for a demo; it doesn't scale to "agent processing a stream of requests". For that, the body needs to talk to the host directly while it's running.

microagent-kit has a primitive for exactly this: the **mediation channel**. It's a guest-to-host vsock contract — the body initiates connections to a vsock port; the host listens at a host TCP target; microagent's supervisor proxies bytes between them. Required and fail-closed by default.

This recipe sketches the architecture, the body changes, and the host listener shape. It's a pattern guide, not a copy-paste demo — the right shape depends on your control plane.

## What changes from simple-agent

| Layer | simple-agent | with mediation |
|---|---|---|
| Request delivery | `microagent cp ./input.json demo:/workspace/input.json` per run | Body initiates a connection to mediation, host responds with `WorkRequest` |
| Result delivery | `microagent --json result demo` reads from output artifact | Body POSTs `WorkResult` over the same connection |
| Body lifecycle | One request per restart | Body loops; one process handles many requests |
| Host responsibility | Run `microagent` commands | Run a small mediation listener that speaks the protocol |

The protocol shapes — `WorkRequest`, `WorkResult`, `LifecycleSignal`, `ConstraintAck` — don't change. Only the transport.

## Declare the channel

In your spec:

```yaml
# microagent.yaml
mediation:
  enabled: true
  required: true
  port: 2048
  target: 127.0.0.1:9900
  failClosed: true
```

`port` is the guest-side vsock port the body connects to. `target` is the host-side address the supervisor forwards traffic to. `required: true` plus `failClosed: true` means the workspace refuses to start if the host listener isn't reachable — the right default. Drop them only for local development with `--mediation-optional`.

Equivalent CLI:

```bash
microagent create --file ./microagent.yaml \
  --mediation 2048=127.0.0.1:9900
```

The supervisor wires up the vsock listener inside the guest and bridges it to the host TCP target. The body just opens a connection to vsock port 2048 and reads/writes JSON.

## Body changes

The body's `process()` function in simple-agent reads one file and writes one file, then exits. With mediation, the body opens a long-lived connection and loops over it.

A skeleton (Python; same shape in any language):

```python
import socket
import json

VSOCK_PORT = 2048

# AF_VSOCK is socket.AF_VSOCK on Linux; CID_HOST is the conventional host CID.
CID_HOST = 2

def connect_to_mediation():
    s = socket.socket(socket.AF_VSOCK, socket.SOCK_STREAM)
    s.connect((CID_HOST, VSOCK_PORT))
    return s

def read_message(sock) -> dict:
    """Newline-delimited JSON over vsock."""
    buf = b""
    while True:
        chunk = sock.recv(4096)
        if not chunk:
            return None
        buf += chunk
        if b"\n" in buf:
            line, _, _ = buf.partition(b"\n")
            return json.loads(line)

def send_message(sock, msg: dict) -> None:
    sock.sendall(json.dumps(msg).encode() + b"\n")

def main():
    sock = connect_to_mediation()
    emit_lifecycle_signal(sock, "ready")

    while True:
        raw = read_message(sock)
        if raw is None:
            break  # host closed the channel
        req = WorkRequest.model_validate(raw)
        emit_lifecycle_signal(sock, "accepting", request_id=req.request_id)
        result = process(req)  # the same Claude/OpenAI/Gemini loop as before
        send_message(sock, result.model_dump(mode="json"))
        emit_lifecycle_signal(sock, "completed", request_id=req.request_id)
```

What's the same:

- `WorkRequest` / `WorkResult` shapes — unchanged. Same Pydantic models, same JSON.
- The model call inside `process()` — unchanged.
- Lifecycle signals — unchanged. They just go over the channel instead of stderr.

What's different:

- **No more file-based input/output.** The body never reads `/workspace/input.json` or writes `/workspace/result.json`.
- **Body is long-lived.** It runs until the host closes the channel.
- **One TCP/vsock framing decision to make.** Newline-delimited JSON is simplest; length-prefixed binary is sturdier. Pick one and document it.

## Host listener pattern

On the host, microagent's supervisor binds a TCP listener at `target` (`127.0.0.1:9900` in the example). When the guest body connects to vsock port 2048, the supervisor accepts the vsock connection and opens a corresponding TCP connection to the host listener. Bytes flow in both directions.

The listener's job is just to speak the protocol:

```python
# host_mediation.py — minimal listener (server-side)
import socket
import json
from queue import Queue

# Your control plane drops requests onto this queue.
work_queue: Queue[dict] = Queue()

def serve():
    srv = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
    srv.bind(("127.0.0.1", 9900))
    srv.listen(1)
    conn, _ = srv.accept()  # the body has connected

    while True:
        # Wait for the body to signal it's ready.
        signal = read_message(conn)
        if signal is None:
            break  # body disconnected
        if signal.get("signal") == "ready":
            req = work_queue.get()
            send_message(conn, req)
            # Wait for accepting / completed signals + the result, then loop.
            ...
```

The above is illustrative — your real listener will be more careful about lifecycle signals, timeouts, reconnection, and concurrent body processes. The shape is: read a signal, dispatch a request when the body is ready, read the result, repeat.

## Lifecycle, transport, and signals

The body emits `LifecycleSignal` events (`ready`, `accepting`, `completed`, `mediation_broken`, `constraints_outdated`, `quarantined`) on the same channel — just additional JSON-line messages. The host distinguishes signals from requests/results by the JSON shape (signals have a `signal` field; requests have `request_id` + `principal`).

If the channel breaks while a request is in flight, the body can't deliver its result. The host should treat in-flight requests as needing retry (idempotency is on you — `request_id` is the deduplication key).

If `mediation.required` is true (the default) and the host listener disappears, the supervisor closes the body's vsock connection. The body sees a clean EOF and exits. From microagent's perspective the workspace transitions to `failed`. Your control plane sees the disconnect and can decide whether to restart.

## What this still doesn't get you

- **Egress for credentials.** API keys still come in via `--env` unless you also route the body's egress through a host-side proxy. See [agency](https://github.com/geoffbelknap/agency) for that pattern.
- **Authorization.** The mediation channel is a transport. Whether a given `WorkRequest` should be processed at all (verified principal, scope, constraints version) is what the body's structural checks decide — your control plane has to populate the request shape correctly.
- **Multi-body coordination.** Each workspace has one body and one mediation channel. Coordinating across bodies is your control plane's job, not microagent-kit's.

## Related

- [Networking](../concepts/networking.md) — the mediation channel's place in the workspace network model.
- [Build a simple agent](simple-agent.md) — the file-based version this builds on.
