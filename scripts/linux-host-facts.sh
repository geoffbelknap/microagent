#!/usr/bin/env bash
set -euo pipefail

printf 'distro='
if [ -r /etc/os-release ]; then
  . /etc/os-release
  printf '%s\n' "${PRETTY_NAME:-unknown}"
else
  printf 'unknown\n'
fi

printf 'kernel='
uname -r

printf 'arch='
uname -m

printf 'dev_kvm='
if [ -e /dev/kvm ]; then
  ls -l /dev/kvm
else
  printf 'missing\n'
fi

printf 'dev_vhost_vsock='
if [ -e /dev/vhost-vsock ]; then
  ls -l /dev/vhost-vsock
else
  printf 'missing\n'
fi

printf 'firecracker='
if [ -n "${MICROAGENT_FIRECRACKER:-}" ] && [ -x "$MICROAGENT_FIRECRACKER" ]; then
  "$MICROAGENT_FIRECRACKER" --version
elif command -v firecracker >/dev/null 2>&1; then
  firecracker --version
elif command -v brew >/dev/null 2>&1; then
  formula_prefix="$(brew --prefix microagent-kit 2>/dev/null || true)"
  if [ -x "$formula_prefix/libexec/firecracker" ]; then
    "$formula_prefix/libexec/firecracker" --version
  else
    printf 'missing\n'
  fi
else
  printf 'missing\n'
fi
