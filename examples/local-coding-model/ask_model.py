#!/usr/bin/env python3
import json
import os
import sys
import urllib.error
import urllib.request


def main() -> int:
    base_url = os.environ.get("OPENAI_BASE_URL") or os.environ.get("MICROAGENT_MODEL_URL")
    if not base_url:
        print("OPENAI_BASE_URL or MICROAGENT_MODEL_URL is required", file=sys.stderr)
        return 2

    prompt = os.environ.get(
        "LOCAL_CODING_PROMPT",
        "Write a small Python function named slugify(text) and include two tests.",
    )
    payload = {
        "model": os.environ.get("OPENAI_MODEL", "local-coding-model"),
        "messages": [
            {
                "role": "system",
                "content": "You are a concise coding assistant. Return only code and brief test notes.",
            },
            {"role": "user", "content": prompt},
        ],
        "temperature": 0.2,
        "max_tokens": 512,
    }

    url = base_url.rstrip("/") + "/chat/completions"
    req = urllib.request.Request(
        url,
        data=json.dumps(payload).encode("utf-8"),
        headers={"Content-Type": "application/json"},
        method="POST",
    )

    try:
        with urllib.request.urlopen(req, timeout=120) as resp:
            data = json.loads(resp.read().decode("utf-8"))
    except urllib.error.HTTPError as exc:
        body = exc.read().decode("utf-8", errors="replace")
        print(f"model request failed: HTTP {exc.code}: {body}", file=sys.stderr)
        return 1
    except Exception as exc:
        print(f"model request failed: {exc}", file=sys.stderr)
        return 1

    content = data.get("choices", [{}])[0].get("message", {}).get("content", "")
    result = {
        "base_url": base_url,
        "prompt": prompt,
        "content": content,
        "raw": data,
    }
    os.makedirs("/workspace", exist_ok=True)
    with open("/workspace/result.json", "w", encoding="utf-8") as f:
        json.dump(result, f, indent=2)
        f.write("\n")
    print(content)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
