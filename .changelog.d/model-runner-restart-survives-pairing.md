### A model runner restart no longer silently breaks paired workspaces (Linux/KVM)

Restarting a model runner (`microagent model stop` + `model serve`, including
any config change that forces a restart) used to leave every workspace
already paired to it silently broken: the runner came back on a new port,
the guest's forward still targeted the old one, and the agent inside saw a
bare `ECONNRESET` with nothing in any log naming the cause. The workspace
kept reporting `running` and `model runners` kept reporting the runner
healthy — nothing connected the two facts. The only fix was `microagent halt
<ws> && microagent start <ws>`.

On Linux/KVM, the guest-facing model forward now resolves the current runner
for its paired model on every connection instead of dialing the host:port
captured once at workspace start, so a runner restart no longer breaks
already-running workspaces at all. `microagent model stop` also now prints
the names of any workspaces still paired with the stopped runner, so an
operator who wants the fail-loud path can see who is affected before
restarting.

This is a Linux/KVM-only fix for now — Apple VF workspaces paired to a model
runner still need a manual restart after a runner restart, tracked as a
known backend gap.
