#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

"$ROOT/scripts/dev/require-build-tools.sh"

usage() {
  cat >&2 <<'USAGE'
usage: scripts/install-from-source.sh [--prefix PATH] [--arch ARCH] [--firecracker PATH] [--download-firecracker] [--install-host-packages] [--install-kernel] [--quiet] [--no-check]

Build and install microagent from this checkout into a Unix prefix. The layout
matches packaged installs:

  PREFIX/bin/microagent
  PREFIX/bin/microagent-supervisor
  PREFIX/bin/microagent-firecracker-supervisor      # Linux
  PREFIX/libexec/microagent-guestinit-ARCH
  PREFIX/libexec/microagent-firecracker-supervisor  # Linux
  PREFIX/libexec/firecracker                        # Linux, with --download-firecracker or --firecracker

Options:
  --prefix PATH      install prefix (default: $HOME/.local)
  --arch ARCH        guest architecture (default: host arch mapped to arm64/amd64)
  --firecracker PATH copy an existing Firecracker binary into PREFIX/libexec
  --download-firecracker
                    download and verify the pinned upstream Firecracker VMM on Linux
  --no-download-firecracker
                    do not download Firecracker automatically
  --firecracker-version VERSION
                    Firecracker release to download (default: v1.16.0)
  --firecracker-sha256 SHA256
                    expected SHA-256 for the Firecracker release tarball
  --install-host-packages
                    install host packages such as passt/pasta and e2fsprogs when possible
  --no-install-host-packages
                    do not invoke a package manager
  --install-kernel   install the default kernel into PREFIX/libexec/kernels
  --quiet            suppress command output; write details to an install log
  --no-check         skip the final microagent doctor check
  -h, --help         show this help

This script builds and installs microagent artifacts. On Linux, it can also
download the upstream Firecracker VMM into PREFIX/libexec and install host
packages through the system package manager. It never installs or enables KVM;
that is a host virtualization capability.
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

host_backend() {
  case "$(uname -s)" in
    Linux)
      printf '%s\n' linux-kvm
      ;;
    Darwin)
      printf '%s\n' apple-vf
      ;;
    *)
      printf '%s\n' unsupported
      ;;
  esac
}

firecracker_release_arch() {
  case "$1" in
    amd64)
      printf '%s\n' x86_64
      ;;
    arm64)
      printf '%s\n' aarch64
      ;;
    *)
      return 1
      ;;
  esac
}

default_firecracker_sha256() {
  case "$1/$2" in
    v1.16.0/x86_64)
      printf '%s\n' bd04e26952d4e158085778c6230a0b383d2619c319182e27eaa9d61a212e92d6
      ;;
    v1.16.0/aarch64)
      printf '%s\n' 531c713cdbc37d4b8bc2533d851aabc0267096afa1768086a37672abb668efd7
      ;;
    *)
      return 1
      ;;
  esac
}

file_sha256() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1" | awk '{print $1}'
    return 0
  fi
  if command -v shasum >/dev/null 2>&1; then
    shasum -a 256 "$1" | awk '{print $1}'
    return 0
  fi
  echo "sha256sum or shasum is required to verify downloads" >&2
  return 1
}

init_log() {
  if [ "$quiet" -eq 1 ] && [ -z "$log_path" ]; then
    log_path="$(mktemp "${TMPDIR:-/tmp}/microagent-install-log.XXXXXX")"
  fi
}

run_cmd() {
  local status

  if [ "$quiet" -eq 0 ]; then
    "$@"
    return
  fi
  init_log
  {
    printf '+ '
    printf '%q ' "$@"
    printf '\n'
  } >>"$log_path"
  if "$@" >>"$log_path" 2>&1; then
    return 0
  fi
  status=$?
  echo "Command failed: $*" >&2
  echo "Install log: $log_path" >&2
  return "$status"
}

run_capture() {
  local out status

  out="$1"
  shift
  if [ "$quiet" -eq 0 ]; then
    "$@" >"$out"
    return
  fi
  init_log
  {
    printf '+ '
    printf '%q ' "$@"
    printf '\n'
  } >>"$log_path"
  if "$@" >"$out" 2>>"$log_path"; then
    return 0
  fi
  status=$?
  echo "Command failed: $*" >&2
  echo "Install log: $log_path" >&2
  return "$status"
}

run_doctor_capture() {
  local out

  out="$1"
  if [ "$quiet" -eq 0 ]; then
    "$prefix/bin/microagent" --json doctor >"$out"
    return
  fi
  init_log
  printf '+ %q %q %q\n' "$prefix/bin/microagent" --json doctor >>"$log_path"
  "$prefix/bin/microagent" --json doctor >"$out" 2>>"$log_path"
}

print_doctor_summary() {
  local json="$1"
  python3 - "$json" <<'PY'
import json
import sys

path = sys.argv[1]
try:
    with open(path) as fh:
        data = json.load(fh)
except Exception as exc:
    print(f"Host check failed: could not parse doctor output: {exc}")
    raise SystemExit(0)

if data.get("ok"):
    print("Host check: ready")
else:
    print("Host check failed:")
    print(f"  {data.get('error') or 'host is not ready'}")
PY
}

print_install_summary() {
  echo "Install:"
  echo "  CLI: $prefix/bin/microagent"
  case "$backend" in
    linux-kvm)
      echo "  VMM supervisor: $prefix/bin/microagent-firecracker-supervisor"
      if [ -x "$prefix/libexec/firecracker" ]; then
        echo "  Host VMM: $prefix/libexec/firecracker"
      else
        echo "  Host VMM: missing"
      fi
      ;;
    apple-vf)
      echo "  VMM supervisor: $prefix/bin/microagent-applevf-supervisor"
      ;;
  esac
  echo "  Guest init: $prefix/libexec/microagent-guestinit-$arch"
  if [ -n "${kernel_installed_path:-}" ]; then
    echo "  Kernel: $kernel_installed_path"
  fi
  if [ -n "${host_tools_summary:-}" ]; then
    echo "  Host tools: $host_tools_summary"
  fi
}

install_host_packages() {
  if [ "$backend" = "linux-kvm" ]; then
    local missing_linux_packages=()
    if ! command -v pasta >/dev/null 2>&1; then
      missing_linux_packages+=("passt")
    fi
    if ! command -v mke2fs >/dev/null 2>&1; then
      missing_linux_packages+=("e2fsprogs")
    fi
    if [ "${#missing_linux_packages[@]}" -eq 0 ]; then
      validate_host_tools
      return 0
    fi
    echo "Host packages: installing ${missing_linux_packages[*]}"
    if command -v apt-get >/dev/null 2>&1; then
      run_privileged apt-get update
      run_privileged apt-get install -y "${missing_linux_packages[@]}"
      validate_host_tools
      return 0
    fi
    if command -v dnf >/dev/null 2>&1; then
      run_privileged dnf install -y "${missing_linux_packages[@]}"
      validate_host_tools
      return 0
    fi
    if command -v yum >/dev/null 2>&1; then
      run_privileged yum install -y "${missing_linux_packages[@]}"
      validate_host_tools
      return 0
    fi
    if command -v zypper >/dev/null 2>&1; then
      run_privileged zypper install -y "${missing_linux_packages[@]}"
      validate_host_tools
      return 0
    fi
    if command -v pacman >/dev/null 2>&1; then
      run_privileged pacman -Sy --needed --noconfirm "${missing_linux_packages[@]}"
      validate_host_tools
      return 0
    fi
    echo "Missing host packages: ${missing_linux_packages[*]}" >&2
    echo "Install passt/pasta and e2fsprogs with your system package manager, then rerun microagent doctor." >&2
    return 1
  fi

  if [ "$backend" = "apple-vf" ] && ! command -v mke2fs >/dev/null 2>&1 && [ ! -x /opt/homebrew/opt/e2fsprogs/sbin/mke2fs ]; then
    if command -v brew >/dev/null 2>&1; then
      echo "Host packages: installing e2fsprogs"
      run_cmd brew install e2fsprogs
    else
      echo "mke2fs is required for rootfs builds; install e2fsprogs with Homebrew." >&2
    fi
  fi
  validate_host_tools
}

run_privileged() {
  if [ "$(id -u)" -eq 0 ]; then
    run_cmd "$@"
    return
  fi
  if command -v sudo >/dev/null 2>&1; then
    if [ "$quiet" -eq 1 ]; then
      sudo -v
    fi
    run_cmd sudo "$@"
    return
  fi
  echo "sudo is required to install host packages: $*" >&2
  return 1
}

validate_host_tools() {
  local pasta_path mke2fs_path
  if [ "$backend" = "linux-kvm" ]; then
    if ! pasta_path="$(command -v pasta 2>/dev/null)"; then
      echo "passt installation did not provide the required pasta command on PATH" >&2
      echo "Install passt/pasta with your system package manager, then rerun microagent doctor." >&2
      return 1
    fi
    if ! "$pasta_path" --version >/dev/null 2>&1 && ! "$pasta_path" -h >/dev/null 2>&1; then
      echo "pasta was found at $pasta_path but did not execute successfully" >&2
      return 1
    fi
  fi

  if [ "$backend" = "linux-kvm" ] || [ "$backend" = "apple-vf" ]; then
    if mke2fs_path="$(command -v mke2fs 2>/dev/null)"; then
      :
    fi
    if [ -x /opt/homebrew/opt/e2fsprogs/sbin/mke2fs ]; then
      mke2fs_path="/opt/homebrew/opt/e2fsprogs/sbin/mke2fs"
    fi
    if [ -n "${mke2fs_path:-}" ]; then
      if [ "$backend" = "linux-kvm" ]; then
        host_tools_summary="pasta=$pasta_path, mke2fs=$mke2fs_path"
      else
        host_tools_summary="mke2fs=$mke2fs_path"
      fi
      return 0
    fi
    echo "e2fsprogs installation did not provide the required mke2fs command" >&2
    return 1
  fi
}

download_firecracker() {
  local actual archive archive_path cleanup_tmpdir expected extracted release_arch tmpdir url

  release_arch="$(firecracker_release_arch "$arch")"
  archive="firecracker-${firecracker_version}-${release_arch}.tgz"
  url="https://github.com/firecracker-microvm/firecracker/releases/download/${firecracker_version}/${archive}"
  expected="$firecracker_sha256"
  if [ -z "$expected" ]; then
    expected="$(default_firecracker_sha256 "$firecracker_version" "$release_arch" || true)"
  fi
  if [ -z "$expected" ]; then
    echo "no built-in SHA-256 for $archive; pass --firecracker-sha256" >&2
    exit 2
  fi
  if [ -x "$prefix/libexec/firecracker" ]; then
    return 0
  fi

  tmpdir="$(mktemp -d "${TMPDIR:-/tmp}/microagent-firecracker.XXXXXX")"
  cleanup_tmpdir=1
  trap 'if [ "${cleanup_tmpdir:-0}" -eq 1 ]; then rm -rf "$tmpdir"; fi' EXIT
  archive_path="$tmpdir/$archive"
  echo "Host VMM: downloading $firecracker_version for $release_arch"
  run_cmd curl -fsSL "$url" -o "$archive_path"
  actual="$(file_sha256 "$archive_path")"
  if [ "$actual" != "$expected" ]; then
    echo "Firecracker archive sha256 = $actual, want $expected" >&2
    exit 1
  fi
  run_cmd tar -xzf "$archive_path" -C "$tmpdir"
  extracted="$tmpdir/release-${firecracker_version}-${release_arch}/firecracker-${firecracker_version}-${release_arch}"
  if [ ! -x "$extracted" ]; then
    echo "downloaded Firecracker archive did not contain $extracted" >&2
    exit 1
  fi
  run_cmd install -m 0755 "$extracted" "$prefix/libexec/firecracker"
  cleanup_tmpdir=0
  rm -rf "$tmpdir"
}

install_firecracker_from_path() {
  local dest source source_real dest_real

  source="$1"
  dest="$prefix/libexec/firecracker"
  if [ -e "$dest" ] &&
    source_real="$(realpath "$source" 2>/dev/null)" &&
    dest_real="$(realpath "$dest" 2>/dev/null)" &&
    [ "$source_real" = "$dest_real" ]; then
    return 0
  fi
  run_cmd install -m 0755 "$source" "$dest"
}

prefix="${PREFIX:-$HOME/.local}"
arch="${MICROAGENT_DEV_ARCH:-$(default_arch)}"
firecracker=""
download_firecracker=0
firecracker_version="${FIRECRACKER_VERSION:-v1.16.0}"
firecracker_sha256="${FIRECRACKER_SHA256:-}"
install_host_packages=0
install_kernel=0
quiet=0
check=1
log_path="${MICROAGENT_INSTALL_LOG:-}"
host_tools_summary=""
kernel_installed_path=""

while [ "$#" -gt 0 ]; do
  case "$1" in
    --prefix)
      if [ "$#" -lt 2 ]; then
        usage
        exit 2
      fi
      prefix="$2"
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
    --firecracker)
      if [ "$#" -lt 2 ]; then
        usage
        exit 2
      fi
      firecracker="$2"
      shift
      ;;
    --download-firecracker)
      download_firecracker=1
      ;;
    --no-download-firecracker)
      download_firecracker=0
      ;;
    --firecracker-version)
      if [ "$#" -lt 2 ]; then
        usage
        exit 2
      fi
      firecracker_version="$2"
      shift
      ;;
    --firecracker-sha256)
      if [ "$#" -lt 2 ]; then
        usage
        exit 2
      fi
      firecracker_sha256="$2"
      shift
      ;;
    --install-host-packages)
      install_host_packages=1
      ;;
    --no-install-host-packages)
      install_host_packages=0
      ;;
    --install-kernel)
      install_kernel=1
      ;;
    --quiet)
      quiet=1
      ;;
    --no-check)
      check=0
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

backend="$(host_backend)"
case "$backend" in
  linux-kvm|apple-vf)
    ;;
  *)
    echo "source install is not supported on $(uname -s)" >&2
    exit 2
    ;;
esac

if [ -n "$firecracker" ] && [ ! -x "$firecracker" ]; then
  echo "firecracker binary is not executable: $firecracker" >&2
  exit 2
fi
if [ -n "$firecracker" ] && [ "$download_firecracker" -eq 1 ]; then
  echo "--firecracker and --download-firecracker are mutually exclusive" >&2
  exit 2
fi

stage="$ROOT/.build/source-install"
cli="$stage/bin/microagent"
rm -rf "$stage"
mkdir -p "$stage/bin"

"$ROOT/scripts/dev/build-local.sh" --output "$cli" --arch "$arch" >/dev/null

if ! install -d "$prefix" "$prefix/bin" "$prefix/libexec"; then
  echo "failed to create install prefix at $prefix; choose a writable PREFIX or rerun with appropriate privileges" >&2
  exit 1
fi
install -m 0755 "$cli" "$prefix/bin/microagent"
install -m 0755 "$stage/bin/microagent-guestinit-$arch" "$prefix/libexec/microagent-guestinit-$arch"

case "$backend" in
  linux-kvm)
    install -m 0755 "$stage/bin/microagent-firecracker-supervisor" "$prefix/libexec/microagent-firecracker-supervisor"
    ln -sf ../libexec/microagent-firecracker-supervisor "$prefix/bin/microagent-firecracker-supervisor"
    ln -sf microagent-firecracker-supervisor "$prefix/bin/microagent-supervisor"
    if [ -n "$firecracker" ]; then
      install_firecracker_from_path "$firecracker"
    elif [ "$download_firecracker" -eq 1 ]; then
      download_firecracker
    fi
    ;;
  apple-vf)
    install -m 0755 "$stage/bin/microagent-applevf-supervisor" "$prefix/libexec/microagent-applevf-supervisor"
    ln -sf ../libexec/microagent-applevf-supervisor "$prefix/bin/microagent-applevf-supervisor"
    ln -sf microagent-applevf-supervisor "$prefix/bin/microagent-supervisor"
    ;;
esac

if [ "$install_host_packages" -eq 1 ]; then
  install_host_packages
fi

if [ "$install_kernel" -eq 1 ]; then
  kernel_json="$(mktemp "${TMPDIR:-/tmp}/microagent-kernel-install-json.XXXXXX")"
  kernel_path="$prefix/libexec/kernels/$backend/$arch/Image"
  run_capture "$kernel_json" "$prefix/bin/microagent" kernel install --backend "$backend" --arch "$arch" --out "$kernel_path"
  kernel_installed_path="$(python3 - "$kernel_json" "$kernel_path" <<'PY'
import json
import sys

try:
    with open(sys.argv[1]) as fh:
        data = json.load(fh)
except Exception:
    print(sys.argv[2])
else:
    print(data.get("path") or sys.argv[2])
PY
)"
  rm -f "$kernel_json"
fi

print_install_summary

if [ "$backend" = "linux-kvm" ]; then
  if [ -z "$firecracker" ] && ! command -v firecracker >/dev/null 2>&1 && [ ! -x "$prefix/libexec/firecracker" ]; then
    echo
    echo "Firecracker is still required: install it on PATH, set MICROAGENT_FIRECRACKER, or rerun with --firecracker PATH."
  fi
  if ! command -v pasta >/dev/null 2>&1; then
    echo "pasta is still required for default user-mode networking; install the passt package."
  fi
fi

if [ "$check" -eq 1 ]; then
  doctor_json="$(mktemp "${TMPDIR:-/tmp}/microagent-install-doctor-json.XXXXXX")"
  set +e
  run_doctor_capture "$doctor_json"
  doctor_status=$?
  set -e
  print_doctor_summary "$doctor_json"
  rm -f "$doctor_json"
  exit "$doctor_status"
fi
