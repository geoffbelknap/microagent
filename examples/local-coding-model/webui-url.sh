#!/bin/sh
set -eu

MICROAGENT_BIN="${MICROAGENT_BIN:-microagent}"
MODEL_REF="${MODEL_REF:-Qwen/Qwen2.5-Coder-1.5B-Instruct-GGUF/qwen2.5-coder-1.5b-instruct-q4_k_m.gguf}"

RUNNERS_JSON=$("$MICROAGENT_BIN" --json model runners)

printf "%s\n" "$RUNNERS_JSON" | python3 -c '
import json
import sys

def normalized(ref):
    for prefix in ("https://huggingface.co/", "huggingface.co/", "hf.co/"):
        if ref.startswith(prefix):
            ref = ref[len(prefix):]
            break
    return ref.replace("@main/", "/")

model_ref = sys.argv[1]
want = normalized(model_ref)
data = json.load(sys.stdin)
for runner in data.get("runners", []):
    if normalized(runner.get("model_ref", "")) == want:
        host = runner.get("host") or "127.0.0.1"
        port = runner.get("port")
        print(f"http://{host}:{port}/")
        raise SystemExit(0)

print(f"no running model runner found for {model_ref}", file=sys.stderr)
raise SystemExit(1)
' "$MODEL_REF"
