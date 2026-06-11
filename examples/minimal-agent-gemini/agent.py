#!/usr/bin/env python3
"""Minimal body that calls Google Gemini with workspace tools.

Sibling of ``examples/minimal-body``. Same body protocol shapes, same tool
set, same /workspace boundary — only the model and SDK differ.

Reads a ``WorkRequest`` from /workspace/input.json, runs an agentic loop with
``bash``, ``read_file``, and ``write_file`` tools that execute inside the
microVM's /workspace, and writes a ``WorkResult`` to /workspace/result.json.
Lifecycle signals stream to stderr as JSON lines.

Uses the ``google-genai`` SDK (the newer Python SDK, replacing
``google-generativeai``). Function calling is declared via
``types.FunctionDeclaration`` and the chat loop manages turn state through a
``Chat`` session.
"""

from __future__ import annotations

import os
import subprocess
import sys
from datetime import datetime, timezone
from pathlib import Path

from google import genai
from google.genai import types
from pydantic import BaseModel

from protocol import (
    Constraints,
    LifecycleSignal,
    LifecycleSignalKind,
    WorkRequest,
    WorkResult,
    WorkStatus,
)

AGENT_ID = "minimal-body-gemini-1"
CONSTRAINTS_PATH = Path("/agent/constraints.json")
SYSTEM_PROMPT_PATH = Path("/agent/system_prompt.md")
INPUT_PATH = Path("/workspace/input.json")
RESULT_PATH = Path("/workspace/result.json")
WORKSPACE_ROOT = Path("/workspace").resolve()

SYSTEM_PROMPT = SYSTEM_PROMPT_PATH.read_text()
client = genai.Client(api_key=os.environ["GEMINI_API_KEY"])

BASH_DECL = types.FunctionDeclaration(
    name="bash",
    description="Run a shell command in /workspace. Returns exit code, stdout, and stderr.",
    parameters=types.Schema(
        type=types.Type.OBJECT,
        properties={"command": types.Schema(type=types.Type.STRING)},
        required=["command"],
    ),
)

READ_FILE_DECL = types.FunctionDeclaration(
    name="read_file",
    description="Read a file from disk and return its contents. Path must be absolute.",
    parameters=types.Schema(
        type=types.Type.OBJECT,
        properties={"path": types.Schema(type=types.Type.STRING)},
        required=["path"],
    ),
)

WRITE_FILE_DECL = types.FunctionDeclaration(
    name="write_file",
    description="Write content to a file on disk. Creates or overwrites. Path must be absolute.",
    parameters=types.Schema(
        type=types.Type.OBJECT,
        properties={
            "path": types.Schema(type=types.Type.STRING),
            "content": types.Schema(type=types.Type.STRING),
        },
        required=["path", "content"],
    ),
)

TOOLS = [types.Tool(function_declarations=[BASH_DECL, READ_FILE_DECL, WRITE_FILE_DECL])]


def now() -> datetime:
    return datetime.now(timezone.utc)


def emit(model: BaseModel) -> None:
    print(model.model_dump_json(), file=sys.stderr, flush=True)


def tool_env() -> dict[str, str]:
    blocked = ("API_KEY", "TOKEN", "SECRET", "CREDENTIAL")
    return {
        key: value
        for key, value in os.environ.items()
        if not any(marker in key.upper() for marker in blocked)
    }


def workspace_path(raw: str) -> Path:
    path = Path(raw)
    if not path.is_absolute():
        path = WORKSPACE_ROOT / path
    resolved = path.resolve(strict=False)
    if not resolved.is_relative_to(WORKSPACE_ROOT):
        raise ValueError(f"path must stay under {WORKSPACE_ROOT}: {raw}")
    return resolved


def execute_tool(name: str, args: dict) -> str:
    if name == "bash":
        r = subprocess.run(
            ["bash", "-c", args["command"]],
            capture_output=True,
            text=True,
            timeout=30,
            cwd="/workspace",
            env=tool_env(),
        )
        return f"exit_code={r.returncode}\nstdout:\n{r.stdout}\nstderr:\n{r.stderr}"
    if name == "read_file":
        return workspace_path(args["path"]).read_text()
    if name == "write_file":
        path = workspace_path(args["path"])
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(args["content"])
        return f"wrote {len(args['content'])} bytes to {args['path']}"
    return f"unknown tool: {name}"


def process(req: WorkRequest) -> WorkResult:
    chat = client.chats.create(
        model="gemini-2.5-flash",
        config=types.GenerateContentConfig(
            system_instruction=SYSTEM_PROMPT,
            tools=TOOLS,
        ),
    )

    response = chat.send_message(req.content)

    while True:
        function_calls = []
        if response.candidates and response.candidates[0].content:
            for part in response.candidates[0].content.parts or []:
                if part.function_call:
                    function_calls.append(part.function_call)

        if not function_calls:
            break

        function_responses = []
        for fc in function_calls:
            result = execute_tool(fc.name, dict(fc.args))
            function_responses.append(
                types.Part(function_response=types.FunctionResponse(
                    name=fc.name,
                    response={"result": result},
                ))
            )

        response = chat.send_message(function_responses)

    final_text = response.text or ""
    return WorkResult(
        request_id=req.request_id,
        status=WorkStatus.completed,
        content=final_text,
        completed_at=now(),
        audit_ref=req.audit_ref,
    )


def main() -> int:
    Constraints.model_validate_json(CONSTRAINTS_PATH.read_text())  # validate envelope

    emit(LifecycleSignal(
        signal=LifecycleSignalKind.ready,
        agent_id=AGENT_ID,
        observed_at=now(),
    ))

    req = WorkRequest.model_validate_json(INPUT_PATH.read_text())

    emit(LifecycleSignal(
        signal=LifecycleSignalKind.accepting,
        agent_id=AGENT_ID,
        observed_at=now(),
        request_id=req.request_id,
    ))

    result = process(req)
    RESULT_PATH.write_text(result.model_dump_json(indent=2))

    emit(LifecycleSignal(
        signal=LifecycleSignalKind.completed,
        agent_id=AGENT_ID,
        observed_at=now(),
        request_id=req.request_id,
    ))

    return 0


if __name__ == "__main__":
    sys.exit(main())
