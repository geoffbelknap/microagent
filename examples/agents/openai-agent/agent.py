"""Minimal OpenAI Agents SDK agent.

Illustrative one-shot agent. The OPENAI_API_KEY in the environment is a worthless
placeholder; microagent's cred-swap injects the real key host-side at the egress
edge, so the key never enters this VM. Verify against the current openai-agents
docs (https://openai.github.io/openai-agents-python/) before relying on it.
"""

from agents import Agent, Runner


def main() -> None:
    agent = Agent(
        name="Assistant",
        instructions="You are concise. Answer in one sentence.",
    )
    result = Runner.run_sync(agent, "What is a microVM?")
    print(result.final_output)


if __name__ == "__main__":
    main()
