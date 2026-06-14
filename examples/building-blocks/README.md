# Building blocks

Small, single-idea examples. Each one isolates *one* microagent primitive, runs
in a couple of commands, and is short enough to read in a sitting. They're meant
as starting points to lift into your own project, not a framework to adopt.

The [top-level examples](../) (`minimal-agent`, …) show a whole agent end to
end. These show one idea at a time.

| Block | The aha |
|---|---|
| [`local-coder`](local-coder/) | Run a coding agent against a model on your own machine — no API key, no cloud — fixing failing tests inside a throwaway microVM. |
| [`deny-egress`](deny-egress/) | Boot an agent that *physically cannot* reach the network — one flag, fail-closed — and watch it still do its local work. |
| [`ask-the-host`](ask-the-host/) | The agent doesn't *hold* the dangerous capability; it asks the host over a mediation channel, and the host decides and logs it. |
| [`creds-it-cant-read`](creds-it-cant-read/) | The agent uses a credential that never lands in its filesystem or a snapshot — and every access is in an audit log. |
| [`kill-switch`](kill-switch/) | One `quarantine` severs a running agent's network and mediation instantly, while keeping its disk and logs for forensics. |
| [`quarantined-reader`](quarantined-reader/) | The part that reads attacker-controlled text has no tools and no network and emits only structured JSON — so an injection has nothing to act on. |
| [`per-task-identity`](per-task-identity/) | Every task runs in its own throwaway microVM — fresh disk, its own identity in the audit trail — so nothing leaks between tasks. |

The theme is running an agent **securely** as a personal assistant or knowledge
worker — containing its blast radius, keeping credentials out of its reach, and
mediating the dangerous actions through code you control. `deny-egress` removes
the exfiltration path; `ask-the-host` gives the contained agent a narrow,
audited way to still do real work; `creds-it-cant-read` keeps credentials out of
its reach; `kill-switch` is the panic button when something looks wrong;
`quarantined-reader` neutralizes prompt injection before it reaches the
privileged agent; `per-task-identity` isolates and names each task; and
`local-coder` keeps the whole loop on your own machine. They compose: read top
to bottom and they tell one secure-assistant story; each stands alone as a
copy-paste starter.
