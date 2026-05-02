# Helper Protocol

`microagent-applevf-helper` is a standalone process. It reads one JSON request
from stdin, writes one JSON response to stdout, and exits.

Diagnostics go to stderr. Exit code `0` means the response has `"ok": true`.
Nonzero exit means the response has `"ok": false` or the request could not be
decoded.

## Request

```json
{
  "command": "prepare",
  "identity": {
    "requestID": "req-1",
    "runtimeID": "agent-1",
    "role": "workload",
    "backend": "apple-vf"
  },
  "config": {
    "kernelPath": "/tmp/kernel",
    "rootfsPath": "/tmp/rootfs.ext4",
    "stateDir": "/tmp/microagent",
    "memoryMiB": 512,
    "cpuCount": 2
  }
}
```

Commands:

- `host`
- `check`
- `prepare`
- `start`
- `inspect`
- `stop`
- `kill`
- `delete`

`host` does not require `identity` or `config`. `inspect`, `stop`, `kill`, and
`delete` require `identity` and `config.stateDir`. `check`, `prepare`, and
`start` require the full config.

## Response

```json
{
  "ok": true,
  "backend": "apple-vf",
  "event": {
    "identity": {
      "requestID": "req-1",
      "runtimeID": "agent-1",
      "role": "workload",
      "backend": "apple-vf"
    },
    "state": "prepared",
    "observedAt": "2026-05-02T00:00:00Z"
  }
}
```

Host responses use `host` instead of `event`:

```json
{
  "ok": true,
  "backend": "apple-vf",
  "host": {
    "backend": "apple-vf",
    "architecture": "arm64",
    "frameworkAvailable": true,
    "virtualizationSupported": true
  }
}
```
