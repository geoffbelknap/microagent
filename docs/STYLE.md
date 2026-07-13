---
title: Docs style guide
description: House rules for prose in the microagent docs.
---

<!-- docs-last-updated -->
_Last updated: 2026-07-13_

These docs should read like a careful engineer explaining a tool to a
colleague: plain statements of behavior, concrete examples, no performance.
The structural bar is unchanged: task-first pages, examples lead, jargon
linked to the glossary at first use, deep technical detail only under
`docs/library/`. This page is about the prose itself.

## State behavior, don't sell it

Write what the tool does. Cut sentences that explain why the doc is telling
you, or that dramatize a design decision.

> **Not this:** This fail-closed behavior is the point: an enforcement failure
> can never silently widen what the agent can do.
>
> **This:** If enforcement isn't available, the workspace doesn't start.

No aphorisms or keynote fragments ("evasion made conspicuous", "the fork
diverges; the source doesn't notice"). If a sentence would sound good on a
slide, rewrite it as a plain description.

## Spend emphasis sparingly

- Bold is for terms the reader scans for: flag names at the start of a list
  item, a genuine warning. At most one bold phrase per section beyond those.
- Never bold a mid-sentence clause for drama ("the workspace **fails closed -
  it refuses to start**"). Say it once, unbolded, in its own sentence.
- Cut intensifiers: *actually*, *actual*, *real*, *genuinely*, *simply*,
  *just*. Keep them only when they draw a real contrast ("deletes the
  baseline, not just the record").

## Banned phrases

These have appeared often enough to become tells. Don't use them:

- "and friends" (name the items or say "and the related commands")
- "In plain terms:" / "Put simply:" (just be plain)
- "Be clear-eyed", "the tell", "textbook", "wander into"
- "This is the point" / "is the core of" / "That's the whole idea"
- "Note that", "It's important to", "It's worth noting"

## Keep sentences short

Aim for an average around 20 words. One aside per sentence, maximum — if a
sentence has two em-dash or parenthetical asides, split it. Concept pages are
the usual offenders; they must meet the same bar as guides.

## Lists and templates

- Bold lead-ins in bullets are for reference lists of named things (glossary
  entries, flags, error causes). Ordinary lists don't need them.
- "Related" sections are plain links with a short reason:
  `- [Run a service](/guides/run-a-service/) — keep a server running in a workspace.`
  Vary the phrasing; don't stamp the same template on every page.
- Section headings that repeat verbatim across pages ("Flags you'll actually
  use") read as generated. Use plain ones ("Common flags") and let the content
  differ.

## Voice

- Second person, present tense, contractions welcome.
- It's fine to be direct ("Don't ignore a SHA mismatch") — direct is not the
  same as dramatic.
- Humor is allowed rarely and never in error paths, security pages, or
  troubleshooting.

## Mechanical checks

Two scripts gate docs changes; run both before opening a PR:

```bash
python3 scripts/dev/docs-parity.py          # every --help flag appears on its CLI page
python3 scripts/dev/docs-last-updated.py    # refresh the last-updated stamps
```
