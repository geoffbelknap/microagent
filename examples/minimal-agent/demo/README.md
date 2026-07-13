# Demo files

Operator-supplied inputs for the generated agent. They exist so you can
`microagent cp` a ready-made request into the workspace instead of retyping
JSON into your terminal. The walkthrough that uses them is the
[simple-agent guide](https://github.com/geoffbelknap/microagent/blob/main/docs/guides/simple-agent.md).

| File | What it is |
|---|---|
| `constraints.json` | The constraint envelope (version 1). |
| `system_prompt.md` | The system prompt - tells the agent it has tools and should use them. |
| `input-001.json` | First request: install a package with pip, write and run a script that renders a table of the largest files under /usr. |
| `input-002.json` | Second request: modify the script from the first run and re-run it - demonstrates workspace persistence across halt/resume. |
| `hello.json` | A minimal first task: write and run a small Python script that prints a greeting and the kernel version. |
| `clone-and-test.json` | Clone a small public Python repo, install its dependencies, and run its pytest suite. |
| `analyze-file.json` | Clean a messy CSV (`data/sales-sample.csv`, copy it to /workspace first) and write a findings report. |
| `data/sales-sample.csv` | Deliberately messy sales data for `analyze-file.json`: mixed date formats, a duplicate row, missing values, an outlier. |

These are example files - swap them for your own once you've worked through the walkthrough.
