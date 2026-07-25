#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
. "$ROOT/scripts/dev/e2e-lib.sh"

# Secret delivery over host<->guest vsock: materialized /run/secrets files,
# on-demand fetch through the guest API socket, and host audit records.
e2e_require_vm
# The ext4 lanes copy the guest probe into the stopped rootfs with debugfs.
e2e_require_cmd debugfs "debugfs (e2fsprogs) is required to copy the guest probe into the rootfs"
e2e_require_cmd mke2fs "mke2fs is required to build the workspace rootfs"

default_backend() {
  case "$(uname -s):$(uname -m)" in
    Linux:x86_64|Linux:amd64)
      printf '%s\n' linux-kvm
      ;;
    Darwin:arm64)
      printf '%s\n' applevf
      ;;
    *)
      printf '%s\n' unsupported
      ;;
  esac
}

BACKEND="${MICROAGENT_E2E_BACKEND:-$(default_backend)}"
STATE_DIR="$(mktemp -d "${TMPDIR:-/tmp}/microagent-e2e-secrets.XXXXXX")"
CLI="$STATE_DIR/microagent"
SUPERVISOR=""
WS="secrets-ok"
MATERIALIZED_VALUE="materialized-secret-value"
ON_DEMAND_VALUE="on-demand-secret-value"

cleanup() {
  status="$?"
  if [ -x "$CLI" ]; then
    "$CLI" kill "$WS" --state-dir "$STATE_DIR" --supervisor "$SUPERVISOR" >/dev/null 2>&1 || true
    if [ "$status" -eq 0 ] && [ "${MICROAGENT_KEEP_MICROAGENT_E2E_SECRETS:-0}" != "1" ]; then
      "$CLI" delete "$WS" --force --state-dir "$STATE_DIR" --supervisor "$SUPERVISOR" >/dev/null 2>&1 || true
    fi
  fi
  chmod -R u+w "$STATE_DIR" 2>/dev/null || true
  if [ "$status" -eq 0 ] && [ "${MICROAGENT_KEEP_MICROAGENT_E2E_SECRETS:-0}" != "1" ]; then
    rm -rf "$STATE_DIR"
  else
    echo "kept microagent E2E secrets state at $STATE_DIR" >&2
  fi
}
trap cleanup EXIT

cd "$ROOT"
export GOCACHE="${GOCACHE:-$STATE_DIR/gocache}"
export GOMODCACHE="${GOMODCACHE:-$STATE_DIR/gomodcache}"
if [ -z "${DOCKER_CONFIG:-}" ]; then
  mkdir -p "$STATE_DIR/docker-config"
  export DOCKER_CONFIG="$STATE_DIR/docker-config"
fi

case "$BACKEND" in
  linux-kvm)
    SUPERVISOR="$STATE_DIR/microagent-firecracker-supervisor"
    GUEST_INIT="$STATE_DIR/microagent-guestinit-amd64"
    IMAGE="${MICROAGENT_E2E_IMAGE:-docker.io/library/busybox:1.36}"
    GUEST_ARCH="amd64"
    e2e_build_firecracker_stack "$CLI" "$SUPERVISOR" "$GUEST_INIT"
    "$CLI" kernel install --backend linux-kvm --arch amd64 >"$STATE_DIR/kernel-install.json" 2>/dev/null || e2e_fail "kernel install"
    KERNEL="$(python3 -c 'import json,sys;print(json.load(open(sys.argv[1]))["path"])' "$STATE_DIR/kernel-install.json")"
    CREATE_FLAGS=(--kernel "$KERNEL" --guest-init "$GUEST_INIT" --supervisor "$SUPERVISOR" --state-dir "$STATE_DIR" --size-mib 128 --result-port 0)
    START_FLAGS=(--state-dir "$STATE_DIR" --supervisor "$SUPERVISOR")
    ;;
  applevf)
    case "$(uname -s):$(uname -m)" in
      Darwin:arm64)
        ;;
      *)
        e2e_skip "Apple VF secrets E2E requires macOS on Apple silicon"
        ;;
    esac
    SUPERVISOR="${MICROAGENT_APPLEVF_SUPERVISOR:-$ROOT/supervisors/applevf/.build/release/microagent-applevf-supervisor}"
    KERNEL="${MICROAGENT_APPLEVF_KERNEL:-$HOME/.microagent/kernels/apple-vf/arm64/Image}"
    if [ ! -r "$KERNEL" ] && [ -r "$HOME/.microagent/kernels/apple-vf/Image" ]; then
      KERNEL="$HOME/.microagent/kernels/apple-vf/Image"
    fi
    if [ ! -x "$SUPERVISOR" ]; then
      e2e_skip "supervisor is not executable at $SUPERVISOR; run scripts/dev/applevf-supervisor-build.sh"
    fi
    if [ ! -r "$KERNEL" ]; then
      e2e_skip "kernel is not readable at $KERNEL"
    fi
    GUEST_ARCH="${MICROAGENT_APPLEVF_BOOT_ARCH:-arm64}"
    IMAGE="${MICROAGENT_APPLEVF_BOOT_IMAGE:-docker.io/library/busybox@sha256:c4e5b27bf840ba1ebd5568b6b914f6926f3559b2ad4f505b1f37aae483b907d6}"
    GUEST_INIT="$STATE_DIR/microagent-guestinit"
    go build -buildvcs=false -o "$CLI" ./cmd/microagent
    GOOS=linux GOARCH="$GUEST_ARCH" CGO_ENABLED=0 go build -buildvcs=false -o "$GUEST_INIT" ./cmd/microagent-guestinit
    CREATE_FLAGS=(--backend apple-vf --kernel "$KERNEL" --guest-init "$GUEST_INIT" --supervisor "$SUPERVISOR" --state-dir "$STATE_DIR" --size-mib 128 --result-port 0)
    START_FLAGS=(--state-dir "$STATE_DIR" --supervisor "$SUPERVISOR")
    ;;
  *)
    e2e_skip "secrets E2E does not support backend lane: $BACKEND"
    ;;
esac

cat >"$STATE_DIR/secret-probe.go" <<'GO'
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"os"
)

type workloadResponse struct {
	OK    bool   `json:"ok"`
	Value []byte `json:"value,omitempty"`
	Error string `json:"error,omitempty"`
}

func main() {
	if len(os.Args) != 3 {
		fmt.Fprintln(os.Stderr, "usage: secret-probe MATERIALIZED_NAME ON_DEMAND_NAME")
		os.Exit(2)
	}
	materialized, err := os.ReadFile("/run/secrets/" + os.Args[1])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	sock := os.Getenv("MICROAGENT_SECRETS_SOCK")
	if sock == "" {
		sock = "/run/secrets-api.sock"
	}
	conn, err := net.Dial("unix", sock)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer conn.Close()
	if _, err := fmt.Fprintf(conn, "GET %s\n", os.Args[2]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	line, err := bufio.NewReader(conn).ReadBytes('\n')
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	var resp workloadResponse
	if err := json.Unmarshal(line, &resp); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if !resp.OK {
		fmt.Fprintln(os.Stderr, resp.Error)
		os.Exit(1)
	}
	value := resp.Value
	fmt.Printf("materialized=%s\non_demand=%s\n", string(materialized), string(value))
}
GO

GOOS=linux GOARCH="$GUEST_ARCH" CGO_ENABLED=0 go build -buildvcs=false -o "$STATE_DIR/secret-probe" "$STATE_DIR/secret-probe.go"

e2e_step "create workspace with materialized and on-demand secrets"
MICROAGENT_E2E_SECRET_MATERIALIZED="$MATERIALIZED_VALUE" MICROAGENT_E2E_SECRET_ON_DEMAND="$ON_DEMAND_VALUE" \
  "$CLI" create --name "$WS" --image "$IMAGE" --network isolated --service-command "chmod +x /secret-probe && /secret-probe API DB > /secret-output 2>&1; sleep 120" \
  --secret API=env:MICROAGENT_E2E_SECRET_MATERIALIZED --secret-on-demand DB=env:MICROAGENT_E2E_SECRET_ON_DEMAND --secrets-audit \
  "${CREATE_FLAGS[@]}" >"$STATE_DIR/create.json" 2>&1 || { cat "$STATE_DIR/create.json"; e2e_fail "create workspace with secrets"; }

e2e_step "copy guest secret probe into the stopped rootfs"
"$CLI" cp "$(e2e_host_path "$STATE_DIR/secret-probe")" "$WS:/secret-probe" --state-dir "$STATE_DIR" >/dev/null 2>&1 || e2e_fail "copy secret probe"

e2e_step "start workspace and wait for exec readiness"
MICROAGENT_E2E_SECRET_MATERIALIZED="$MATERIALIZED_VALUE" MICROAGENT_E2E_SECRET_ON_DEMAND="$ON_DEMAND_VALUE" \
  "$CLI" start "$WS" "${START_FLAGS[@]}" >/dev/null 2>&1 || e2e_fail "start workspace"
e2e_wait_exec_ready "$CLI" "$STATE_DIR" "$WS" || e2e_fail "exec service never became ready"

e2e_step "guest reads materialized secret and fetches on-demand secret"
for _ in $(seq 1 30); do
  "$CLI" exec "$WS" --state-dir "$STATE_DIR" -- sh -c "cat /secret-output" >"$STATE_DIR/secret-output.txt" 2>/dev/null || true
  if grep -q "materialized=$MATERIALIZED_VALUE" "$STATE_DIR/secret-output.txt" && grep -q "on_demand=$ON_DEMAND_VALUE" "$STATE_DIR/secret-output.txt"; then
    break
  fi
  sleep 1
done
grep -q "materialized=$MATERIALIZED_VALUE" "$STATE_DIR/secret-output.txt" || { cat "$STATE_DIR/secret-output.txt"; e2e_fail "materialized secret missing"; }
grep -q "on_demand=$ON_DEMAND_VALUE" "$STATE_DIR/secret-output.txt" || { cat "$STATE_DIR/secret-output.txt"; e2e_fail "on-demand secret missing"; }

e2e_step "secret audit records materialized and on-demand access without values"
"$CLI" secret audit "$WS" --state-dir "$STATE_DIR" >"$STATE_DIR/secret-audit.txt" 2>&1 || { cat "$STATE_DIR/secret-audit.txt"; e2e_fail "secret audit"; }
grep -q "API" "$STATE_DIR/secret-audit.txt" || e2e_fail "audit missing materialized secret"
grep -q "DB" "$STATE_DIR/secret-audit.txt" || e2e_fail "audit missing on-demand secret"
if grep -q "$MATERIALIZED_VALUE\\|$ON_DEMAND_VALUE" "$STATE_DIR/secret-audit.txt"; then
  e2e_fail "audit leaked a secret value"
fi

"$CLI" kill "$WS" "${START_FLAGS[@]}" >/dev/null 2>&1 || true
e2e_log "secrets scenario passed"
