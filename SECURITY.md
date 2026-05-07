# Reporting a security issue

Report security issues privately through GitHub's "Report a vulnerability" flow on the [microagent-kit repository](https://github.com/geoffbelknap/microagent-kit/security). Don't open public issues for security-sensitive reports.

Include in your report:

- the affected version or commit
- host operating system and backend
- reproduction steps
- impact and any known mitigations

## Response

Maintainers will acknowledge reports as soon as practical, investigate with the reporter, and coordinate disclosure timing for confirmed vulnerabilities.

## Supported versions

Security fixes target the latest released version and `main`. Older releases may receive fixes when the patch is small and the affected version is still in active use.

## Trust boundary

For what `microagent-kit` does and doesn't enforce — kernel verification, image pinning, supervisor signing — see [`docs/security.md`](docs/security.md).
