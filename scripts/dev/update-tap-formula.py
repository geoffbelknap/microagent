#!/usr/bin/env python3
"""Update one platform's Homebrew source pin and caveats.

The first update splits the shared source pin while retaining the other
platform. Resource definitions are untouched. Promotion must move forward in
source history; invalid formula shapes fail before writing.
"""
from __future__ import annotations

import argparse
import os
import re
import sys
import subprocess


SOURCE = 'https://github.com/geoffbelknap/microagent.git'
PIN = re.compile(
    r'  url "https://github.com/geoffbelknap/microagent.git",\n'
    r'      revision: "([0-9a-f]{40})"\n  version "([^"\n]+)"\n'
)


def platform_block(platform: str, revision: str, version: str) -> str:
    return (f'  # microagent source: {platform}\n'
            f'  on_{platform} do\n'
            f'    url "{SOURCE}",\n'
            f'        revision: "{revision}"\n'
            f'    version "{version}"\n'
            f'  end\n  # end microagent source: {platform}\n')


def block_pattern(platform: str) -> re.Pattern:
    return re.compile(
        rf'  # microagent source: {platform}\n'
        rf'  on_{platform} do\n'
        rf'    url "https://github.com/geoffbelknap/microagent.git",\n'
        rf'        revision: "([0-9a-f]{{40}})"\n'
        rf'    version "([^"\n]+)"\n'
        rf'  end\n  # end microagent source: {platform}\n'
    )


def source_pin(text: str, platform: str) -> tuple[str, str]:
    if '# microagent source:' in text and PIN.search(text):
        raise ValueError('mixed shared and platform-specific source pins')
    matches = list(block_pattern(platform).finditer(text))
    if not matches and '# microagent source:' not in text:
        matches = list(PIN.finditer(text))
    if len(matches) != 1:
        raise ValueError(f'expected exactly one {platform} source pin')
    return matches[0].group(1, 2)


def update_platform(text: str, platform: str, revision: str, version: str,
                    macos_baseline: str | None = None) -> str:
    if not re.fullmatch(r'[0-9a-f]{40}', revision):
        raise ValueError('revision must be a full commit SHA')
    if not re.fullmatch(r'[0-9]+\.[0-9]+\.[0-9]+(?:-latest\.[0-9]+)?', version):
        raise ValueError('version must be a stable or latest channel version')
    if '# microagent source:' not in text:
        old_revision, old_version = source_pin(text, platform)
        mac_revision, mac_version = source_pin(macos_baseline or text, 'macos')
        replacement = (platform_block('linux', old_revision, old_version) + '\n' +
                       platform_block('macos', mac_revision, mac_version))
        text = PIN.sub(lambda _: replacement, text, count=1)
    # Validate both sides before modifying either. Resource URLs are untouched.
    source_pin(text, 'linux')
    source_pin(text, 'macos')
    return block_pattern(platform).sub(
        lambda _: platform_block(platform, revision, version), text, count=1)


def update_caveats(text: str, caveats: str, platform: str, baseline: str | None) -> str:
    legacy = re.compile(r'\n  def caveats\n    <<~EOS\n(.*?)    EOS\n  end\n', re.S)
    scoped = re.compile(r'\n  def caveats\n    if OS.mac\?\n(.*?)    else\n(.*?)    end\n  end\n', re.S)

    def heredoc(value: str) -> str:
        lines = ''.join('        ' + line + '\n' for line in value.strip('\n').splitlines())
        return '      <<~EOS\n' + lines + '      EOS\n'

    match = scoped.search(text)
    if match:
        mac, linux = match.groups()
    else:
        old = legacy.search(text)
        value = '\n'.join(line[6:] for line in old[1].splitlines()) if old else ''
        mac = linux = heredoc(value)
        if baseline is not None:
            old_mac = legacy.search(baseline)
            mac = heredoc('\n'.join(line[6:] for line in old_mac[1].splitlines()) if old_mac else '')
            if scoped.search(baseline):
                mac = scoped.search(baseline)[1]
    if platform == 'macos':
        mac = heredoc(caveats)
    else:
        linux = heredoc(caveats)
    block = '\n  def caveats\n    if OS.mac?\n' + mac + '    else\n' + linux + '    end\n  end\n'
    if match:
        return scoped.sub(lambda _: block, text, count=1)
    if legacy.search(text):
        return legacy.sub(lambda _: block, text, count=1)
    if '\n  def caveats\n' in text or '\n  test do\n' not in text:
        raise ValueError('unrecognized caveats or missing test block')
    return text.replace('\n  test do\n', block + '\n  test do\n', 1)


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--formula", required=True, help="path to the tap formula file to update")
    parser.add_argument("--version", required=True)
    parser.add_argument("--revision", required=True)
    parser.add_argument("--caveats", required=True, help="path to the repo-owned caveats source file")
    parser.add_argument("--platform", choices=("linux", "macos"), required=True)
    parser.add_argument("--macos-baseline", help="stable formula used when first splitting the latest formula")
    parser.add_argument("--source-repo", required=True, help="full source checkout used to reject backward promotion")
    args = parser.parse_args()

    with open(args.formula) as fh:
        text = fh.read()

    baseline = None
    if args.macos_baseline:
        with open(args.macos_baseline) as fh:
            baseline = fh.read()
        if args.formula.endswith('microagent-latest.rb'):
            mac_revision, mac_version = source_pin(baseline, 'macos')
            count = subprocess.check_output([
                'git', '-C', args.source_repo, 'rev-list', '--count', mac_revision], text=True).strip()
            baseline = update_platform(baseline, 'macos', mac_revision, mac_version + '-latest.' + count)
    try:
        old_revision, _ = source_pin(text, args.platform)
        # The first latest-channel migration uses the stable Mac pin.
        if args.platform == 'macos' and baseline and '# microagent source:' not in text:
            old_revision, _ = source_pin(baseline, 'macos')
        updated = update_platform(text, args.platform, args.revision, args.version, baseline)
        subprocess.run(['git', '-C', args.source_repo, 'merge-base', '--is-ancestor',
                        old_revision, args.revision], check=True)
        text = updated
    except (ValueError, subprocess.CalledProcessError) as exc:
        raise SystemExit(f'{args.formula}: invalid or backward promotion: {exc}') from exc

    if os.path.exists(args.caveats):
        with open(args.caveats) as fh:
            text = update_caveats(text, fh.read(), args.platform, baseline)
    else:
        print(f"warning: {args.caveats} not found; leaving caveats untouched", file=sys.stderr)

    with open(args.formula, "w") as fh:
        fh.write(text)


if __name__ == "__main__":
    main()
