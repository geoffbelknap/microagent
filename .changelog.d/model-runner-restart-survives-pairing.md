The model forward in the firecracker vsock listener now resolves the current
runner for a paired model on each connection instead of using the static
host:port captured at workspace start. Restarting a model runner no longer
silently breaks every workspace paired to it — the forward picks up the new
port automatically.

`microagent model stop` now prints the names of any workspaces still paired
with the stopped runner so the operator can decide whether to restart them.
