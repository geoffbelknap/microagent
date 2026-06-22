#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"

usage() {
  cat >&2 <<'USAGE'
usage: scripts/dev/build-local.sh [--output PATH] [--arch ARCH] [--quiet]

Builds a local microagent CLI plus the host backend supervisor and Linux
guest-init companions expected by the CLI resolver. On Linux it also builds the
Firecracker supervisor; on macOS it builds the Apple VF supervisor. The default
CLI path is .build/dev/microagent.

Options:
  --output PATH   CLI output path (default: .build/dev/microagent)
  --arch ARCH     guest architecture (default: host arch mapped to arm64/amd64)
  --quiet         build without printing usage hints
  -h, --help      show this help
USAGE
}

default_arch() {
  case "$(uname -m)" in
    arm64|aarch64)
      printf '%s\n' arm64
      ;;
    x86_64|amd64)
      printf '%s\n' amd64
      ;;
    *)
      uname -m
      ;;
  esac
}

resolve_firecracker() {
  local prefix

  if [ -n "${MICROAGENT_FIRECRACKER:-}" ] && [ -x "${MICROAGENT_FIRECRACKER:-}" ]; then
    printf '%s\n' "$MICROAGENT_FIRECRACKER"
    return 0
  fi
  if command -v firecracker >/dev/null 2>&1; then
    command -v firecracker
    return 0
  fi
  if [ -x "$HOME/.local/libexec/firecracker" ]; then
    printf '%s\n' "$HOME/.local/libexec/firecracker"
    return 0
  fi
  if [ -x "/usr/local/libexec/firecracker" ]; then
    printf '%s\n' "/usr/local/libexec/firecracker"
    return 0
  fi
  if command -v brew >/dev/null 2>&1; then
    prefix="$(brew --prefix microagent 2>/dev/null || true)"
    if [ -n "$prefix" ] && [ -x "$prefix/libexec/firecracker" ]; then
      printf '%s\n' "$prefix/libexec/firecracker"
      return 0
    fi
  fi
  return 1
}

dev_version() {
  local base sha dirty

  base="$(release_line_version)"
  sha="local"
  dirty=""

  if git -C "$ROOT" rev-parse --is-inside-work-tree >/dev/null 2>&1; then
    sha="$(git -C "$ROOT" rev-parse --short=7 HEAD)"
    if ! git -C "$ROOT" diff --quiet --ignore-submodules -- ||
      ! git -C "$ROOT" diff --cached --quiet --ignore-submodules -- ||
      [ -n "$(git -C "$ROOT" status --porcelain --untracked-files=normal)" ]; then
      dirty="-dirty"
    fi
  fi

  printf '%s-%s%s\n' "$base" "$sha" "$dirty"
}

release_line_version() {
  local tag

  tag=""
  if git -C "$ROOT" rev-parse --is-inside-work-tree >/dev/null 2>&1; then
    while IFS= read -r tag; do
      case "$tag" in
        *-*)
          continue
          ;;
        *)
          break
          ;;
      esac
    done < <(git -C "$ROOT" tag --list 'v[0-9]*.[0-9]*.[0-9]*' --sort=-v:refname)
  fi

  if [ -z "$tag" ]; then
    printf '0.0.0\n'
    return
  fi

  tag="${tag#v}"
  printf '%s\n' "$tag"
}

output="${MICROAGENT_DEV_CLI:-$ROOT/.build/dev/microagent}"
arch="${MICROAGENT_DEV_ARCH:-$(default_arch)}"
quiet=0

while [ "$#" -gt 0 ]; do
  case "$1" in
    --output)
      if [ "$#" -lt 2 ]; then
        usage
        exit 2
      fi
      output="$2"
      shift
      ;;
    --arch)
      if [ "$#" -lt 2 ]; then
        usage
        exit 2
      fi
      arch="$2"
      shift
      ;;
    --quiet)
      quiet=1
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      usage
      exit 2
      ;;
  esac
  shift
done

case "$arch" in
  arm64|amd64)
    ;;
  *)
    echo "unsupported guest arch: $arch" >&2
    exit 2
    ;;
esac

mkdir -p "$(dirname "$output")"
output_dir="$(cd "$(dirname "$output")" && pwd -P)"
output_name="$(basename "$output")"
cli_path="$output_dir/$output_name"
guest_init_path="$output_dir/microagent-guestinit-$arch"
supervisor_path="$output_dir/microagent-applevf-supervisor"
firecracker_supervisor_path="$output_dir/microagent-firecracker-supervisor"
libexec_dir="$(cd "$output_dir/.." && pwd -P)/libexec"
firecracker_vmm_path="$libexec_dir/firecracker"
version="$(dev_version)"
ldflags="-X main.version=$version"

(
  cd "$ROOT"
  go build -ldflags "$ldflags" -o "$cli_path" ./cmd/microagent
  GOOS=linux GOARCH="$arch" CGO_ENABLED=0 go build -o "$guest_init_path" ./cmd/microagent-guestinit
)

if [ "$(uname -s)" = "Linux" ]; then
  # The CLI resolver looks for microagent-firecracker-supervisor next to the
  # CLI (see pkg/diagnostics.DefaultFirecrackerSupervisorPathFromExecutable),
  # so build it alongside the CLI to make the dev build self-sufficient.
  (
    cd "$ROOT"
    go build -o "$firecracker_supervisor_path" ./cmd/microagent-firecracker-supervisor
  )
  if firecracker_source="$(resolve_firecracker)"; then
    mkdir -p "$libexec_dir"
    ln -sf "$firecracker_source" "$firecracker_vmm_path"
  fi
fi

if [ "$(uname -s)" = "Darwin" ]; then
  "$ROOT/scripts/dev/applevf-supervisor-build.sh" >/dev/null
  cp "$ROOT/supervisors/applevf/.build/release/microagent-applevf-supervisor" "$supervisor_path"
  # Ad-hoc + hardened runtime + library validation (see applevf-supervisor-build.sh).
  codesign -s - -f --options runtime,library --entitlements "$ROOT/supervisors/applevf/microagent-applevf-supervisor.entitlements" "$supervisor_path" >/dev/null
fi

if [ "$quiet" -eq 0 ]; then
  echo "CLI: $cli_path"
  echo "Version: $version"
  if [ -f "$supervisor_path" ]; then
    echo "VMM supervisor: $supervisor_path"
  fi
  if [ -f "$firecracker_supervisor_path" ]; then
    echo "VMM supervisor: $firecracker_supervisor_path"
  fi
  if [ -e "$firecracker_vmm_path" ]; then
    echo "Host VMM: $firecracker_vmm_path"
  elif [ "$(uname -s)" = "Linux" ]; then
    echo "Host VMM: not linked (run make install or set MICROAGENT_FIRECRACKER)"
  fi
  echo "Guest init: $guest_init_path"
  echo
  echo "Use this dev build from a shell:"
  echo "  export PATH=\"$output_dir:\$PATH\""
  echo
  echo "Use this dev build as an MCP stdio server:"
  echo "  command: $cli_path"
  echo "  args: [\"serve\", \"mcp\"]"
fi
