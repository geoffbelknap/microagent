---
title: Platform support
description: Know where microagent is meant to run and how to check a host.
---

<!-- docs-last-updated -->
_Last updated: 2026-06-27_

microagent supports Linux and macOS hosts. The docs describe the behavior you
should expect on supported hosts in the current release. Start with the command
you want to run; use `doctor` when the host itself is the question.

- **Linux** is supported through the Linux KVM backend.
- **macOS on Apple silicon** is supported through Apple Virtualization.framework.
- **WSL** is a compatibility lane through the Linux backend when WSL exposes
  the Linux host capabilities microagent needs. Run `microagent doctor` inside
  WSL before creating workspaces.
- **Windows Hyper-V** is experimental. Use it for evaluation unless a release
  note or task explicitly scopes Windows host support for your workflow.

## Check the host

`microagent doctor` reports the active backend and the host capabilities
microagent can see from the current shell.

```bash
microagent doctor
```

If `doctor` reports a missing prerequisite, fix that host capability before
starting a workspace. For the detailed prerequisite list, see
[Host requirements](/concepts/backends/).
