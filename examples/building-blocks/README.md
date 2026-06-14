# Building blocks

Small, single-idea examples. Each one isolates *one* microagent primitive, runs
in a couple of commands, and is short enough to read in a sitting. They're meant
as starting points to lift into your own project, not a framework to adopt.

The [top-level examples](../) (`minimal-agent`, …) show a whole agent end to
end. These show one idea at a time.

| Block | The aha |
|---|---|
| [`local-coder`](local-coder/) | Run a coding agent against a model on your own machine — no API key, no cloud — fixing failing tests inside a throwaway microVM. |

More blocks land here over time; the theme is running an agent **securely** as a
personal assistant or knowledge worker — containing its blast radius, keeping
credentials out of its reach, and mediating the dangerous actions through code
you control.
