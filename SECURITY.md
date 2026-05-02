# Security

`microagent-kit` starts Linux microVMs from host-side requests. Treat kernel
images, root filesystems, and request files as executable input.

This repo does not make policy decisions, broker credentials, interpret audit
records, sign images, generate SBOMs, or scan rootfs contents. Do those checks
before handing Microagent a VM request.

Please report security issues privately to the repository owner before opening a
public issue.
