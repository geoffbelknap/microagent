#!/usr/bin/env bash
set -euo pipefail

usage() {
  cat >&2 <<'USAGE'
usage: scripts/dev/cleanup-temp.sh [--yes] [--all] [--min-age-hours HOURS] [--root PATH]

Finds temporary microagent development artifacts in temp directories.
Dry-run is the default; pass --yes to delete matching paths.

Options:
  --yes                 delete candidates instead of printing a dry-run
  --all                 ignore the minimum-age filter
  --min-age-hours N     only include candidates older than N hours (default: 24)
  --root PATH           scan PATH instead of the default temp roots; repeatable
  -h, --help            show this help
USAGE
}

confirm=0
all=0
min_age_hours=24
roots=()

while [ "$#" -gt 0 ]; do
  case "$1" in
    --yes)
      confirm=1
      ;;
    --all)
      all=1
      ;;
    --min-age-hours)
      if [ "$#" -lt 2 ]; then
        usage
        exit 2
      fi
      min_age_hours="$2"
      shift
      ;;
    --root)
      if [ "$#" -lt 2 ]; then
        usage
        exit 2
      fi
      roots+=("$2")
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

case "$min_age_hours" in
  ''|*[!0-9]*)
    echo "--min-age-hours must be a non-negative integer" >&2
    exit 2
    ;;
esac

if [ "${#roots[@]}" -eq 0 ]; then
  roots=(/tmp /private/tmp)
  if [ -n "${TMPDIR:-}" ]; then
    roots+=("${TMPDIR%/}")
  fi
fi

canonical_root() {
  local root="$1"
  (cd "$root" 2>/dev/null && pwd -P) || return 1
}

is_allowed_root() {
  case "$1" in
    /|''|/private|/tmp/*|/private/tmp/*)
      return 1
      ;;
    /tmp|/private/tmp|/var/folders/*/T|/private/var/folders/*/T)
      return 0
      ;;
    *)
      return 1
      ;;
  esac
}

seen_roots=""
unique_roots=()
for root in "${roots[@]}"; do
  if [ ! -d "$root" ]; then
    continue
  fi
  canon="$(canonical_root "$root")" || continue
  if ! is_allowed_root "$canon"; then
    echo "Refusing to scan unsafe root: $root" >&2
    exit 2
  fi
  case "
$seen_roots
" in
    *"
$canon
"*)
      ;;
    *)
      seen_roots="${seen_roots}${canon}
"
      unique_roots+=("$canon")
      ;;
  esac
done

if [ "${#unique_roots[@]}" -eq 0 ]; then
  echo "No temp roots found to scan."
  exit 0
fi

find_candidates() {
  local root="$1"
  if [ "$all" -eq 1 ]; then
    find "$root" -maxdepth 1 \
      \( -name 'microagent-*' -o -name 'microvm-rootfs-smoke*' \) \
      -print
  else
    find "$root" -maxdepth 1 \
      \( -name 'microagent-*' -o -name 'microvm-rootfs-smoke*' \) \
      -mmin "+$((min_age_hours * 60))" \
      -print
  fi
}

candidate_file="$(mktemp -t makit-cleanup.XXXXXX)"
trap 'rm -f "$candidate_file"' EXIT

for root in "${unique_roots[@]}"; do
  find_candidates "$root" >>"$candidate_file"
done

sort -u -o "$candidate_file" "$candidate_file"

count="$(wc -l <"$candidate_file" | tr -d ' ')"
if [ "$count" -eq 0 ]; then
  echo "No matching temporary microagent artifacts found."
  exit 0
fi

if [ "$confirm" -eq 1 ]; then
  failures=0
  while IFS= read -r path; do
    echo "delete $path"
    if [ -d "$path" ] && [ ! -L "$path" ]; then
      chmod -R u+w "$path" 2>/dev/null || true
    fi
    if ! rm -rf -- "$path"; then
      echo "failed to delete $path" >&2
      failures=$((failures + 1))
    fi
  done <"$candidate_file"
  if [ "$failures" -gt 0 ]; then
    echo "Deleted with $failures failure(s)." >&2
    exit 1
  fi
  echo "Deleted $count temporary microagent artifact(s)."
else
  while IFS= read -r path; do
    echo "would delete $path"
  done <"$candidate_file"
  if [ "$all" -eq 1 ]; then
    echo "Dry run: $count candidate(s). Pass --yes to delete them."
  else
    echo "Dry run: $count candidate(s) older than ${min_age_hours}h. Pass --yes to delete them, or --all to ignore age."
  fi
fi
