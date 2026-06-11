# Demo files

The operator-supplied files for the [simple-agent recipe](../../../docs/guides/simple-agent.md). They live here so the recipe can `microagent cp` them into a workspace by repo path, instead of asking you to retype JSON into your terminal.

| File | What it is |
|---|---|
| `constraints.json` | The constraint envelope (version 1). |
| `system_prompt.md` | The system prompt - tells the agent it has tools and should use them. |
| `input-001.json` | First request: ask the agent to write and run a small Python script. |
| `input-002.json` | Second request: ask the agent to read the file from the first run and explain it - demonstrates workspace persistence across halt/resume. |

These are example files - swap them for your own once you've worked through the walkthrough.
