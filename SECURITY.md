# Security

`microagent-vmkit` is a host-side lifecycle library for agent microVMs. Treat
kernel images, root filesystems, and runtime request files as executable input.

This repo does not make policy decisions, broker credentials, interpret audit
records, or verify rootfs contents. Callers own those checks before handing a
request to the VM lifecycle layer.

Please report security issues privately to the repository owner before opening a
public issue.
