#!/usr/bin/env bash
set -euo pipefail

usage() {
  cat <<'USAGE'
build-firecracker-kernel.sh

Build a Linux kernel for Microagent's Firecracker backend.

Options:
  --arch <amd64|arm64>       Target architecture. Defaults to the host.
  --version <version>        Linux version. Defaults to 6.1.155.
  --work-dir <dir>           Build work directory. Defaults to .kernel-build.
  --out-dir <dir>            Output directory. Defaults to dist/kernels.
  --jobs <n>                 Parallel build jobs. Defaults to host CPU count.
  --cross-prefix <prefix>    Optional CROSS_COMPILE prefix for cross builds.
  --help                     Show help.
USAGE
}

host_arch() {
  case "$(uname -m)" in
    x86_64|amd64) echo "amd64" ;;
    arm64|aarch64) echo "arm64" ;;
    *) echo "unsupported" ;;
  esac
}

cpu_count() {
  if command -v nproc >/dev/null 2>&1; then
    nproc
    return
  fi
  getconf _NPROCESSORS_ONLN 2>/dev/null || echo 2
}

arch="$(host_arch)"
version="6.1.155"
work_dir=".kernel-build"
out_dir="dist/kernels"
jobs="$(cpu_count)"
cross_prefix=""

while [ "$#" -gt 0 ]; do
  case "$1" in
    --arch)
      arch="${2:-}"
      shift 2
      ;;
    --version)
      version="${2:-}"
      shift 2
      ;;
    --work-dir)
      work_dir="${2:-}"
      shift 2
      ;;
    --out-dir)
      out_dir="${2:-}"
      shift 2
      ;;
    --jobs)
      jobs="${2:-}"
      shift 2
      ;;
    --cross-prefix)
      cross_prefix="${2:-}"
      shift 2
      ;;
    --help|-h)
      usage
      exit 0
      ;;
    *)
      echo "unknown argument: $1" >&2
      usage >&2
      exit 2
      ;;
  esac
done

case "$arch" in
  amd64)
    kernel_arch="x86"
    defconfig="x86_64_defconfig"
    kernel_image="vmlinux"
    ;;
  arm64)
    kernel_arch="arm64"
    defconfig="defconfig"
    kernel_image="arch/arm64/boot/Image"
    ;;
  *)
    echo "unsupported architecture: $arch" >&2
    exit 2
    ;;
esac

for tool in curl tar make sha256sum; do
  if ! command -v "$tool" >/dev/null 2>&1; then
    echo "missing required tool: $tool" >&2
    exit 1
  fi
done

mkdir -p "$work_dir" "$out_dir"

major="${version%%.*}"
archive="linux-$version.tar.xz"
source_dir="$work_dir/linux-$version"
archive_path="$work_dir/$archive"
url="https://cdn.kernel.org/pub/linux/kernel/v$major.x/$archive"

if [ ! -f "$archive_path" ]; then
  curl -fL -o "$archive_path" "$url"
fi

if [ ! -d "$source_dir" ]; then
  tar -C "$work_dir" -xf "$archive_path"
fi

make_args=(
  "ARCH=$kernel_arch"
)
if [ -n "$cross_prefix" ]; then
  make_args+=("CROSS_COMPILE=$cross_prefix")
fi

make -C "$source_dir" "${make_args[@]}" "$defconfig"

config="$source_dir/scripts/config"
"$config" --enable BLK_DEV_INITRD
"$config" --enable DEVTMPFS
"$config" --enable DEVTMPFS_MOUNT
"$config" --enable EXT4_FS
"$config" --enable NET
"$config" --enable INET
"$config" --enable UNIX
"$config" --enable VIRTIO
"$config" --enable VIRTIO_BLK
"$config" --enable VIRTIO_NET
"$config" --enable VIRTIO_MMIO
"$config" --enable VIRTIO_MMIO_CMDLINE_DEVICES
"$config" --enable VSOCKETS
"$config" --enable VIRTIO_VSOCKETS
"$config" --enable SERIAL_8250
"$config" --enable SERIAL_8250_CONSOLE
"$config" --disable DEBUG_INFO

if [ "$arch" = "amd64" ]; then
  "$config" --enable PCI
  "$config" --enable VIRTIO_PCI
  "$config" --enable VIRTIO_PCI_LEGACY
fi

make -C "$source_dir" "${make_args[@]}" olddefconfig
make -C "$source_dir" "${make_args[@]}" "-j$jobs" "$kernel_image"

artifact="$out_dir/microagent-kernel-$version-firecracker-$arch"
cp "$source_dir/$kernel_image" "$artifact"
sha256sum "$artifact" >"$artifact.sha256"

echo "$artifact"
cat "$artifact.sha256"
