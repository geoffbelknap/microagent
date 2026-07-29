"""Minimal Claude Agent SDK agent.

Illustrative one-shot agent. The ANTHROPIC_API_KEY in the environment is the
literal reference @secret:..., which the broker endpoint swaps for the real
key host-side, so the key never enters this VM. Verify against the current
claude-agent-sdk docs (https://docs.claude.com/en/api/agent-sdk/overview) before
relying on it.
"""

import anyio
from claude_agent_sdk import query


async def main() -> None:
    async for message in query(prompt="What is a microVM? Answer in one sentence."):
        print(message)


if __name__ == "__main__":
    anyio.run(main)
