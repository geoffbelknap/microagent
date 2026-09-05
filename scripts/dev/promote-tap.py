#!/usr/bin/env python3
"""Prepare a platform-specific formula update; never commit or push it."""
from __future__ import annotations

import argparse
import json
from pathlib import Path
import re
import subprocess
import sys
import tempfile


REPO = 'geoffbelknap/microagent'


def git(source: Path, *args: str) -> str:
    return subprocess.check_output(['git', '-C', str(source), *args], text=True).strip()


def require_qualification(data: dict) -> None:
    statuses = [s for s in data.get('statuses', []) if s.get('context') == 'applevf-qualified']
    # GitHub returns newest statuses first. A failed or pending rerun supersedes
    # an older success; never search for any historical successful run.
    if not statuses or statuses[0].get('state') != 'success':
        raise ValueError('macOS promotion requires successful applevf-qualified status on this exact commit')


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--source', required=True, type=Path)
    parser.add_argument('--tap', required=True, type=Path)
    parser.add_argument('--platform', required=True, choices=('linux', 'macos'))
    parser.add_argument('--channel', required=True, choices=('stable', 'latest'))
    parser.add_argument('--ref', required=True)
    args = parser.parse_args()
    if args.channel == 'stable':
        if not re.fullmatch(r'v[0-9]+\.[0-9]+\.[0-9]+', args.ref):
            parser.error('stable promotion requires a vMAJOR.MINOR.PATCH release tag')
    elif not re.fullmatch(r'[0-9a-f]{40}', args.ref):
        parser.error('latest promotion requires a full lowercase commit SHA')
    revision = git(args.source, 'rev-parse', '--verify', args.ref + '^{commit}')
    git(args.source, 'merge-base', '--is-ancestor', revision, 'origin/main')
    if args.platform == 'macos':
        data = json.loads(subprocess.check_output([
            'gh', 'api', f'repos/{REPO}/commits/{revision}/status'], text=True))
        require_qualification(data)
    name = 'microagent' if args.channel == 'stable' else 'microagent-latest'
    if args.channel == 'stable':
        version = args.ref[1:]
    else:
        tags = [tag for tag in git(args.source, 'tag', '--merged', revision).splitlines()
                if re.fullmatch(r'v[0-9]+\.[0-9]+\.[0-9]+', tag)]
        if not tags:
            raise ValueError('latest promotion requires a reachable stable version tag')
        base = max(tags, key=lambda tag: tuple(map(int, tag[1:].split('.'))))[1:]
        version = base + '-latest.' + git(args.source, 'rev-list', '--count', revision)
    # Read prose from the selected source without executing its build scripts.
    caveats = git(args.source, 'show', f'{revision}:packaging/homebrew/{name}.caveats')
    with tempfile.TemporaryDirectory() as tmp:
        path = Path(tmp) / 'caveats'
        path.write_text(caveats + '\n' if caveats else '')
        subprocess.run([
            sys.executable, str(Path(__file__).with_name('update-tap-formula.py')),
            '--formula', str(args.tap / f'{name}.rb'),
            '--platform', args.platform, '--version', version, '--revision', revision,
            '--source-repo', str(args.source), '--caveats', str(path),
            '--macos-baseline', str(args.tap / 'microagent.rb'),
        ], check=True)
    print(f'{name}: {args.platform} source set to {version} ({revision})')


if __name__ == '__main__':
    try:
        main()
    except (ValueError, subprocess.CalledProcessError) as exc:
        sys.exit(str(exc))
