#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"

usage() {
  cat >&2 <<'USAGE'
usage: scripts/dev/build-local.sh [--output PATH] [--arch ARCH]

Builds a local microagent CLI plus the Linux guest-init companion expected by
the CLI resolver. The default CLI path is .build/dev/microagent.

Options:
  --output PATH   CLI output path (default: .build/dev/microagent)
  --arch ARCH     guest architecture (default: host arch mapped to arm64/amd64)
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

output="${MICROAGENT_DEV_CLI:-$ROOT/.build/dev/microagent}"
arch="${MICROAGENT_DEV_ARCH:-$(default_arch)}"

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

(
  cd "$ROOT"
  go build -o "$cli_path" ./cmd/microagent
  GOOS=linux GOARCH="$arch" CGO_ENABLED=0 go build -o "$guest_init_path" ./cmd/microagent-guestinit
)

echo "CLI: $cli_path"
echo "Guest init: $guest_init_path"
