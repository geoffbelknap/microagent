#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
STATE_DIR="$(mktemp -d "${TMPDIR:-/tmp}/microagent-e2e-registry-auth.XXXXXX")"
KEEP_VAR="${MICROAGENT_KEEP_MICROAGENT_E2E_REGISTRY_AUTH:-0}"

cleanup() {
  status="$?"
  chmod -R u+w "$STATE_DIR" 2>/dev/null || true
  if [ "$status" -eq 0 ] && [ "$KEEP_VAR" != "1" ]; then
    rm -rf "$STATE_DIR"
  else
    echo "kept microagent E2E registry-auth state at $STATE_DIR" >&2
  fi
}
trap cleanup EXIT

for required in go mke2fs; do
  if ! command -v "$required" >/dev/null 2>&1; then
    echo "$required is required for microagent registry-auth E2E" >&2
    exit 2
  fi
done

export GOCACHE="${GOCACHE:-$STATE_DIR/gocache}"
export GOMODCACHE="${GOMODCACHE:-$STATE_DIR/gomodcache}"
export GOFLAGS="${GOFLAGS:-} -modcacherw"
export MICROAGENT_ROOTFS_REGISTRY_AUTH_E2E=1
export MICROAGENT_ROOTFS_BASE_CACHE_DIR=

(
  cd "$ROOT"
  go test ./pkg/rootfs \
    -run 'TestBuilder(PullsFromPrivateRegistryUsingDockerConfig|RejectsPrivateRegistryWithoutDockerCredentials)$' \
    -count=1
)

echo "microagent E2E registry-auth passed"
