# per-task-identity

**Aha:** every task runs in its own throwaway microVM — fresh disk, its own
identity in the audit trail, gone when it's done — so nothing leaks between tasks.

A one-shot `microagent run` boots a microVM, runs the task, and tears it down
(`--rm` is the default). Each run gets a fresh rootfs and an identity microagent
records host-side: a per-call `requestID`, the `runtimeID` (your `--name`), and a
`role`. The task ([`task.py`](task.py)) leaves a marker on its disk and looks for
one a previous task might have left — and never finds it, because the disks are
never the same disk.

## Files

| File | Role |
|---|---|
| `microagent.yaml` | Workspace spec — bakes the task, declares the result artifact. |
| `task.py` | Looks for a marker from a prior task (never present), writes its own, reports what it saw. |

## Run

```bash
# Task A — its own microVM, named identity, disposable.
microagent run --file microagent.yaml --name task-a -e TASK_LABEL=task-a
#   {"task": "task-a", "saw_previous_task": null}

# Task B — a brand new microVM. It cannot see anything task-a wrote.
microagent run --file microagent.yaml --name task-b -e TASK_LABEL=task-b
#   {"task": "task-b", "saw_previous_task": null}
```

`saw_previous_task` is always `null`: each task is isolated by construction. The
`--name` you pass is the `runtimeID` stamped into the event trail, so an
operator can later say exactly which task did what.

To see the identity recorded host-side, keep one run instead of disposing it:

```bash
microagent create --file microagent.yaml --name audited-task -e TASK_LABEL=audited-task
microagent start audited-task
microagent events audited-task        # each event carries the identity block
microagent delete audited-task --yes
```

## Why this matters

A personal assistant handling one user's request shouldn't be able to see
another's leftovers, and when something goes wrong you want to know which task,
on whose behalf, did it. Disposable per-task microVMs give you both: hard
isolation between tasks (separate kernels and disks, not just separate
processes) and a named identity in every event. See
[State and identity](../../../docs/concepts/state-and-identity.md) for the full
identity block and where it shows up.
