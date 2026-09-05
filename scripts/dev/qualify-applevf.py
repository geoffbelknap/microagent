#!/usr/bin/env python3
"""Validate one trusted microagent revision on a physical Apple-silicon Mac.

Creates a detached worktree and retains it with private logs. No GitHub status
is written unless --record is supplied. Does not install software or promote a
formula. Run directly on the host, outside a sandbox.
"""
from __future__ import annotations

import argparse
from datetime import datetime, timezone
import hashlib
import json
import os
from pathlib import Path
import platform
import re
import shutil
import subprocess
import sys
import tempfile
import time


REPO = 'geoffbelknap/microagent'
SCENARIOS = [
    'mcp-lifecycle', 'public-surface', 'lifecycle-deep', 'networking-deep',
    'transport-deep', 'supervision-deep', 'volumes', 'commit-images',
    'secrets', 'health', 'exec-stream', 'model-serving',
    'applevf-direct-console', 'applevf-snapshot',
]
STATUS = 'applevf-qualified'


def output(*args: str, cwd: Path | None = None) -> str:
    return subprocess.check_output(args, cwd=cwd, text=True).strip()


def clean_environment(llama_server: str | None = None) -> dict[str, str]:
    # A prepared binary or test-skip override must not qualify other code.
    env = {key: value for key, value in os.environ.items()
           if not key.startswith('MICROAGENT_')}
    env['MICROAGENT_E2E_BACKEND'] = 'apple-vf'
    env['MICROAGENT_E2E_REQUIRE_VM'] = '1'
    if llama_server:
        env['MICROAGENT_LLAMA_SERVER'] = llama_server
    return env


def resolve_llama_server(value: str | None) -> str:
    candidate = value or os.environ.get('MICROAGENT_LLAMA_SERVER') or shutil.which('llama-server')
    if not candidate:
        raise ValueError('model-serving requires llama-server; install it and pass --llama-server /path/to/llama-server')
    path = Path(candidate).expanduser().resolve()
    if not path.is_file() or not os.access(path, os.X_OK):
        raise ValueError(f'llama-server is not executable: {path}')
    return str(path)


def status(sha: str, state: str) -> None:
    subprocess.run([
        'gh', 'api', f'repos/{REPO}/statuses/{sha}',
        '-f', f'state={state}', '-f', f'context={STATUS}',
        '-f', f'description=Apple VF qualification {state}',
    ], check=True, stdout=subprocess.DEVNULL)


def run_step(command: list[str], checkout: Path, env: dict[str, str], log) -> None:
    print('+ ' + ' '.join(command), flush=True)
    log.write('\n+ ' + ' '.join(command) + '\n')
    log.flush()
    # Keep the complete output on disk while showing it as it arrives. Reading
    # the log avoids pipe buffering and lets quiet builds emit a heartbeat.
    with Path(log.name).open() as reader:
        reader.seek(log.tell())
        with subprocess.Popen(command, cwd=checkout, env=env, stdout=log,
                              stderr=subprocess.STDOUT) as process:
            started = heartbeat = time.monotonic()
            try:
                while True:
                    chunk = reader.read()
                    if chunk:
                        print(chunk, end='', flush=True)
                    if process.poll() is not None:
                        print(reader.read(), end='', flush=True)
                        break
                    now = time.monotonic()
                    if now - heartbeat >= 20:
                        print(f'.. {command[0]} still running ({int(now - started)}s elapsed)', flush=True)
                        heartbeat = now
                    time.sleep(0.2)
            except KeyboardInterrupt:
                process.terminate()
                raise
            if process.returncode:
                raise subprocess.CalledProcessError(process.returncode, command)


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--ref', required=True, help='trusted release tag or full commit SHA')
    parser.add_argument('--output-dir', type=Path, help='new directory for checkout, logs, and result.json')
    parser.add_argument('--record', action='store_true', help='record qualification as a GitHub commit status')
    parser.add_argument('--llama-server', help='host llama-server executable required by model-serving; defaults to MICROAGENT_LLAMA_SERVER or PATH')
    args = parser.parse_args()
    if platform.system() != 'Darwin' or platform.machine() != 'arm64':
        parser.error('requires a physical Apple-silicon macOS host')
    if not re.fullmatch(r'(?:v[0-9]+\.[0-9]+\.[0-9]+(?:-rc\.[0-9]+)?|[0-9a-f]{40})', args.ref):
        parser.error('--ref must be a release tag or a full lowercase commit SHA')
    for tool in ['git', 'go', 'swift', 'codesign', 'python3'] + (['gh'] if args.record else []):
        if not shutil.which(tool):
            parser.error(f'{tool} is required; install it before qualifying a candidate')
    try:
        llama_server = resolve_llama_server(args.llama_server)
    except ValueError as exc:
        parser.error(str(exc))
    source = Path(__file__).resolve().parents[2]
    remote = output('git', 'remote', 'get-url', 'origin', cwd=source)
    if remote not in [f'https://github.com/{REPO}.git', f'https://github.com/{REPO}',
                      f'git@github.com:{REPO}.git']:
        parser.error(f'origin must be {REPO}')
    # Fetch only the explicitly selected trusted revision. Never execute a PR
    # chosen by a remote queue or move the caller's branch/working files.
    subprocess.run(['git', 'fetch', 'origin', args.ref], cwd=source, check=True)
    sha = output('git', 'rev-parse', 'FETCH_HEAD^{commit}', cwd=source)
    if re.fullmatch(r'[0-9a-f]{40}', args.ref) and sha != args.ref:
        raise ValueError('fetched commit differs from the selected SHA')
    if args.output_dir:
        args.output_dir.mkdir(parents=True, exist_ok=False, mode=0o700)
        directory = args.output_dir.resolve()
    else:
        root = Path.home() / 'Library' / 'Logs' / 'microagent' / 'qualification'
        root.mkdir(parents=True, exist_ok=True)
        directory = Path(tempfile.mkdtemp(prefix=sha[:12] + '-', dir=root))
    checkout = directory / 'checkout'
    result = {
        'schema_version': 1, 'commit': sha, 'requested_ref': args.ref,
        'started_at': datetime.now(timezone.utc).isoformat(),
        'host': {'os': platform.platform(), 'arch': platform.machine()},
        'scenarios': SCENARIOS, 'status': 'failure', 'record_requested': args.record,
        'log': str(directory / 'qualification.log'),
        'llama_server': llama_server,
    }
    print(f'Candidate: {sha}\nResults: {directory}', flush=True)
    pending = False
    record_failed = False
    try:
        if args.record:
            status(sha, 'pending')
            pending = True
        subprocess.run(['git', 'worktree', 'add', '--detach', str(checkout), sha],
                       cwd=source, check=True)
        env = clean_environment(llama_server)
        with (directory / 'qualification.log').open('w') as log:
            for command in [
                ['sw_vers'], ['go', 'version'], ['swift', '--version'],
                [llama_server, '--version'],
                ['scripts/dev/cleanup-temp.sh'],
                ['scripts/dev/build-local.sh', '--quiet'],
                ['go', 'test', './...'],
                ['swift', 'test', '--package-path', 'supervisors/applevf', '--disable-sandbox'],
                ['scripts/dev/microagent-e2e.sh', '--require-vm', *SCENARIOS],
            ]:
                run_step(command, checkout, env, log)
        dirty = output('git', 'status', '--porcelain', cwd=checkout)
        if dirty or output('git', 'rev-parse', 'HEAD', cwd=checkout) != sha:
            raise ValueError('candidate checkout changed during qualification; inspect the retained checkout')
        binaries = [checkout / '.build' / 'dev' / name for name in
                    ['microagent', 'microagent-guestinit-arm64', 'microagent-applevf-supervisor']]
        result['artifacts'] = {
            str(path.relative_to(checkout)): hashlib.sha256(path.read_bytes()).hexdigest()
            for path in binaries
        }
        result['status'] = 'success'
    except (OSError, ValueError, subprocess.CalledProcessError, KeyboardInterrupt) as exc:
        result['error'] = str(exc) or 'interrupted'
    finally:
        result['finished_at'] = datetime.now(timezone.utc).isoformat()
        (directory / 'result.json').write_text(json.dumps(result, indent=2) + '\n')
        if pending:
            # A publishing failure must not print success. Local evidence is
            # retained and an earlier successful status has already been cleared.
            try:
                status(sha, result['status'])
            except subprocess.CalledProcessError:
                print(f'Could not record status. Local result: {directory / "result.json"}', file=sys.stderr)
                record_failed = True
    print(f'Qualification {result["status"]}: {directory / "result.json"}')
    print(f'Retained checkout: {checkout}')
    return 0 if result['status'] == 'success' and not record_failed else 1


if __name__ == '__main__':
    os.umask(0o077)
    sys.exit(main())
