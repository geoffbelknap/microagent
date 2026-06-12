#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
. "$ROOT/scripts/dev/e2e-lib.sh"
STATE_DIR="$(mktemp -d "${TMPDIR:-/tmp}/microagent-e2e-help-usage.XXXXXX")"
CLI="$STATE_DIR/microagent"
KEEP_VAR="${MICROAGENT_KEEP_MICROAGENT_E2E_HELP_USAGE:-0}"

cleanup() {
  status="$?"
  chmod -R u+w "$STATE_DIR" 2>/dev/null || true
  if [ "$status" -eq 0 ] && [ "$KEEP_VAR" != "1" ]; then
    rm -rf "$STATE_DIR"
  else
    echo "kept microagent E2E help/usage state at $STATE_DIR" >&2
  fi
}
trap cleanup EXIT

for required in go grep; do
  if ! command -v "$required" >/dev/null 2>&1; then
    e2e_skip "$required is required for microagent help/usage E2E"
  fi
done

export GOCACHE="${GOCACHE:-$STATE_DIR/gocache}"
export GOMODCACHE="${GOMODCACHE:-$STATE_DIR/gomodcache}"
export GOFLAGS="${GOFLAGS:-} -modcacherw"

(cd "$ROOT" && go build -buildvcs=false -o "$CLI" ./cmd/microagent)

assert_stdout_contains() {
  name="$1"
  expected="$2"
  shift 2
  "$@" >"$STATE_DIR/${name}.out" 2>"$STATE_DIR/${name}.err"
  if ! grep -Eiq -- "$expected" "$STATE_DIR/${name}.out"; then
    echo "$name did not print expected output: $expected" >&2
    echo "--- stdout ---" >&2
    cat "$STATE_DIR/${name}.out" >&2
    echo "--- stderr ---" >&2
    cat "$STATE_DIR/${name}.err" >&2
    exit 1
  fi
}

assert_output_contains() {
  name="$1"
  expected="$2"
  shift 2
  set +e
  "$@" >"$STATE_DIR/${name}.out" 2>"$STATE_DIR/${name}.err"
  status="$?"
  set -e
  if ! grep -Eiq -- "$expected" "$STATE_DIR/${name}.out" "$STATE_DIR/${name}.err"; then
    echo "$name did not print expected output: $expected" >&2
    echo "exit status: $status" >&2
    echo "--- stdout ---" >&2
    cat "$STATE_DIR/${name}.out" >&2
    echo "--- stderr ---" >&2
    cat "$STATE_DIR/${name}.err" >&2
    exit 1
  fi
}

expect_failure_contains() {
  name="$1"
  expected="$2"
  shift 2
  if "$@" >"$STATE_DIR/${name}.out" 2>"$STATE_DIR/${name}.err"; then
    echo "$name unexpectedly succeeded" >&2
    exit 1
  fi
  if ! grep -Eiq -- "$expected" "$STATE_DIR/${name}.err"; then
    echo "$name failed without expected message: $expected" >&2
    echo "--- stdout ---" >&2
    cat "$STATE_DIR/${name}.out" >&2
    echo "--- stderr ---" >&2
    cat "$STATE_DIR/${name}.err" >&2
    exit 1
  fi
}

assert_stdout_contains top-help "Commands:" "$CLI" help
assert_stdout_contains top-help-rm-alias "rm[[:space:]]+Alias for delete" "$CLI" help
assert_stdout_contains top-help-inspect-alias "inspect[[:space:]]+Alias for status" "$CLI" help
assert_stdout_contains top-help-exec "exec[[:space:]]+Run a structured command" "$CLI" help
assert_stdout_contains default-help "rootfs build" "$CLI"
assert_stdout_contains create-help "Create a workspace from an image" "$CLI" create --help
assert_stdout_contains run-help "Run a command from an image" "$CLI" run --help
assert_stdout_contains run-help-env-alias "-e KEY=VALUE" "$CLI" run --help
assert_stdout_contains run-help-publish-alias "-p host:guest" "$CLI" run --help
assert_stdout_contains run-help-rm-alias "-rm" "$CLI" run --help
assert_stdout_contains run-help-volume-alias "-v SRC:DST" "$CLI" run --help
assert_stdout_contains run-help-container-examples "Container-style examples" "$CLI" run --help
assert_stdout_contains run-help-noncompat "container-engine APIs, compose projects, pods, privileged mode" "$CLI" run --help
assert_stdout_contains exec-help "Run a structured command in a running workspace" "$CLI" exec --help
assert_stdout_contains perf-help "Measure workspace performance" "$CLI" perf --help
assert_stdout_contains kernel-help "Advanced kernel commands" "$CLI" kernel --help
assert_stdout_contains rootfs-help "Build a rootfs from an OCI image" "$CLI" rootfs --help
assert_output_contains rootfs-build-help "Usage of rootfs build:" "$CLI" rootfs build --help
assert_stdout_contains linux-network-setup-help "--check" "$ROOT/scripts/dev/microagent-e2e-linux-network-setup.sh" --help
if [ "$(uname -s)" = "Linux" ]; then
  assert_stdout_contains linux-network-setup-check "microagent E2E Linux network setup check" "$ROOT/scripts/dev/microagent-e2e-linux-network-setup.sh" --check
fi

expect_failure_contains unknown-command "unknown command: definitely-not-a-command" "$CLI" definitely-not-a-command
expect_failure_contains linux-network-setup-unknown "unknown option: --definitely-not-an-option" "$ROOT/scripts/dev/microagent-e2e-linux-network-setup.sh" --definitely-not-an-option
expect_failure_contains run-missing-image "run requires --image" "$CLI" run --name missing-image --state-dir "$STATE_DIR"
expect_failure_contains run-exec-positional-conflict "both --exec and positional command" "$CLI" run --image example.com/acme/image:latest --exec true echo --state-dir "$STATE_DIR"
expect_failure_contains run-rm-keep-conflict "both --rm and --keep" "$CLI" run --rm --keep example.com/acme/image:latest true --state-dir "$STATE_DIR"
mkdir -p "$STATE_DIR/host-bind"
# Volume specs are parsed by the CLI itself; pass the host part in native
# form so Git Bash does not mangle the colon-separated spec on Windows.
expect_failure_contains run-volume-bind-reject "does not expose host bind mounts" "$CLI" run -v "$(e2e_host_path "$STATE_DIR/host-bind"):/workspace:rw" example.com/acme/image:latest true --state-dir "$STATE_DIR"
expect_failure_contains exec-missing-separator "usage: microagent exec" "$CLI" exec example.com/acme/image:latest true
expect_failure_contains inspect-usage "usage: microagent status" "$CLI" inspect --state-dir "$STATE_DIR"
expect_failure_contains compose-unsupported "compose-style multi-workspace projects are not supported" "$CLI" compose up
expect_failure_contains run-privileged-unsupported "microVM boundary" "$CLI" run --privileged example.com/acme/image:latest true
expect_failure_contains run-pod-unsupported "does not implement pods" "$CLI" run --pod new:demo example.com/acme/image:latest true
expect_failure_contains run-mount-bind-unsupported "does not expose host bind mounts" "$CLI" run --mount type=bind,source="$STATE_DIR/host-bind",target=/workspace example.com/acme/image:latest true
expect_failure_contains run-cap-unsupported "namespace, capability, device, or security-opt controls" "$CLI" run --cap-add NET_ADMIN example.com/acme/image:latest true
expect_failure_contains run-publish-alias-isolated "network.portForwards require user, nat, or bridged mode" "$CLI" run -p 127.0.0.1:18080:8080/tcp --network isolated example.com/acme/image:latest true --state-dir "$STATE_DIR"
expect_failure_contains cp-usage "usage: microagent cp" "$CLI" cp only-one-arg --state-dir "$STATE_DIR"
expect_failure_contains artifacts-usage "usage: microagent artifacts get" "$CLI" artifacts get only two --state-dir "$STATE_DIR"
expect_failure_contains images-rm-usage "usage: microagent images rm" "$CLI" images rm --state-dir "$STATE_DIR"
expect_failure_contains images-remove-usage "usage: microagent images rm" "$CLI" images remove --state-dir "$STATE_DIR"
expect_failure_contains images-rmi-usage "usage: microagent images rm" "$CLI" images rmi --state-dir "$STATE_DIR"
expect_failure_contains rootfs-unknown "unknown rootfs command: nope" "$CLI" rootfs nope
expect_failure_contains rootfs-missing-out "output_path is required" "$CLI" rootfs build --image docker.io/library/busybox@sha256:b7f3d86d6e84fc17718c48bcde1450807faa2d56704205c697b4bd5df7b9e29f --state-dir "$STATE_DIR"
expect_failure_contains perf-steady-interval "perf steady interval must be less than or equal to duration" "$CLI" perf steady workspace --duration 1 --interval 2 --state-dir "$STATE_DIR"

echo "microagent E2E help/usage passed"
