# Security

`microagent-kit` starts Linux microVMs from host-side requests. Treat kernel
images, root filesystems, and request files as executable input.

This repo does not make policy decisions, broker credentials, interpret audit
records, sign images, generate SBOMs, or scan rootfs contents. Do those checks
before handing Microagent a VM request.

## Supported Versions

Security fixes target the latest released version and `main`. Older releases
may receive fixes when the patch is small and the affected version is still in
active use.

## Reporting

Please report security issues privately through GitHub's "Report a
vulnerability" flow for this repository. Do not open public issues for
security-sensitive reports.

Include:

- the affected version or commit
- host operating system and backend
- reproduction steps
- impact and any known mitigations

## Response

Maintainers will acknowledge reports as soon as practical, investigate with the
reporter, and coordinate disclosure timing for confirmed vulnerabilities.
