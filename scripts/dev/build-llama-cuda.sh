#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"

CUDA_REPO_URL="${MICROAGENT_LLAMA_CUDA_REPO_URL:-https://developer.download.nvidia.com/compute/cuda/repos/ubuntu2404/x86_64}"
CUDA_VERSION_DOT="13.3"

DEFAULT_LLAMA_DIR="$(cd "$ROOT/.." && pwd -P)/llama.cpp"
llama_dir="${MICROAGENT_LLAMA_CPP_DIR:-$DEFAULT_LLAMA_DIR}"
build_dir="${MICROAGENT_LLAMA_CUDA_BUILD_DIR:-/tmp/llama.cpp-cuda13-ninja-build}"
cuda_root="${MICROAGENT_LLAMA_CUDA_ROOT:-/tmp/microagent-cuda13-root}"
cuda_home="${MICROAGENT_LLAMA_CUDA_HOME:-}"
debs_dir="${MICROAGENT_LLAMA_CUDA_DEBS_DIR:-/tmp/microagent-cuda13-debs}"
cuda_arch="${MICROAGENT_LLAMA_CUDA_ARCH:-86}"
jobs="${MICROAGENT_LLAMA_CUDA_JOBS:-}"
download_cuda=1
verify_build=1
with_ui=0
with_nccl=0
dry_run=0

CUDA_PACKAGES=(
  "cccl-13-3_13.3.3.3.1-1_amd64.deb|fead750e8a18f86acbed8557c1fe0d777b87289e1384bfbd3e8a69c2de1fe8e8"
  "cuda-cudart-13-3_13.3.29-1_amd64.deb|d3c825bfd40d6be5293854ea140cffdd66e66845be6595fc76e03c1240085ce8"
  "cuda-cudart-dev-13-3_13.3.29-1_amd64.deb|600e5cf3685d0afae85970ba02451358068b7b56c954999b9149900ec5d940d9"
  "cuda-culibos-dev-13-3_13.3.33-1_amd64.deb|db20e2e39cfa3a570581df1534de3a31810e9db5b8454b7605a333c07c677d90"
  "cuda-driver-dev-13-3_13.3.29-1_amd64.deb|9ff314357a66e7602d0fe9a354052f60f7571138e9d4e1e98f3dd06cd899e021"
  "cuda-crt-13-3_13.3.33-1_amd64.deb|d7a7893e49a84f7b6bfe4cdbbecbfecd4fe1f30e00cc33e2643352ea317f5be6"
  "cuda-nvcc-13-3_13.3.33-1_amd64.deb|8f51b83a216c10cdd463a42fd5294bddb32b605da6b765b210206d16c5a67352"
  "libnvvm-13-3_13.3.33-1_amd64.deb|88766feb10755f344f518651b82e8718acad660553550424d47e290cde985eac"
  "libnvptxcompiler-13-3_13.3.33-1_amd64.deb|df0d34023c4d78edad4eaf851bf570ad444da6a9775ce2d593f2162142b2eacb"
  "cuda-profiler-api-13-3_13.3.27-1_amd64.deb|626c5cd1c978e7151da2fa511f12a47cb15628db9e131c9a05e5a09bf44a9d41"
  "cuda-toolkit-13-3-config-common_13.3.29-1_all.deb|c71420541f81628f551cb26ab91ca4dfb2458f118d11c538ccf3dcc548e6abcf"
  "cuda-toolkit-13-config-common_13.3.29-1_all.deb|7177da13bf1d6e33ad591b7f79602665f2af3aaf1bc9306809b830c1650218a6"
  "cuda-toolkit-config-common_13.3.29-1_all.deb|a0ed2214ecf40c980a5a215fde943255da103c32695b004ad7efc7df622a5e30"
  "libcublas-13-3_13.5.1.27-1_amd64.deb|232ac7f5ab8e2fbadad6b7d5bac1e2bc11f382b1986747f8b62d270f0f669dde"
  "libcublas-dev-13-3_13.5.1.27-1_amd64.deb|017010f645a7eb182f206cf1d9ae1ff112c42dc82d4d99a2a885d3d3a17b4cea"
)

usage() {
  cat >&2 <<'USAGE'
usage: scripts/dev/build-llama-cuda.sh [options]

Build a CUDA-enabled llama.cpp llama-server for microagent model-runner tests.
The default path reproduces the WSL2 RTX 3080 Ti build used during development:
it downloads pinned CUDA 13.3 Ubuntu 24.04 debs into /tmp, extracts them without
installing system packages, and builds llama.cpp out of tree with Ninja.

Options:
  --llama-dir PATH       llama.cpp checkout (default: ../llama.cpp)
  --build-dir PATH       CMake build dir (default: /tmp/llama.cpp-cuda13-ninja-build)
  --cuda-root PATH       extracted CUDA root for pinned debs (default: /tmp/microagent-cuda13-root)
  --cuda-home PATH       use an existing CUDA toolkit instead of pinned deb extraction
  --debs-dir PATH        CUDA deb cache dir (default: /tmp/microagent-cuda13-debs)
  --cuda-arch ARCH       CMAKE_CUDA_ARCHITECTURES value (default: 86 for RTX 3080 Ti)
  --jobs N               parallel build jobs (default: online CPU count)
  --repo-url URL         NVIDIA deb repository URL
  --no-download          require an existing --cuda-root/--cuda-home; do not download debs
  --no-verify           skip llama-server --list-devices after build
  --with-ui              build llama.cpp's embedded Web UI assets
  --with-nccl            enable GGML_CUDA_NCCL
  --dry-run              print the commands that would run
  -h, --help             show this help

Required host tools:
  bash, curl, dpkg-deb, sha256sum, cmake, ninja, g++, git

The script does not install dependencies. Install missing tools explicitly
before running it.
USAGE
}

die() {
  echo "error: $*" >&2
  exit 1
}

default_jobs() {
  if command -v getconf >/dev/null 2>&1; then
    getconf _NPROCESSORS_ONLN
    return
  fi
  printf '4\n'
}

quote_cmd() {
  local arg
  for arg in "$@"; do
    printf '%q ' "$arg"
  done
  printf '\n'
}

run() {
  if [ "$dry_run" -eq 1 ]; then
    printf '+ '
    quote_cmd "$@"
    return 0
  fi
  "$@"
}

need_cmd() {
  command -v "$1" >/dev/null 2>&1 || die "required command not found: $1"
}

abs_dir() {
  local path="$1"
  mkdir -p "$path"
  cd "$path" && pwd -P
}

verify_sha256() {
  local path="$1"
  local sha="$2"

  if [ "$dry_run" -eq 1 ]; then
    printf '+ verify sha256 %s %s\n' "$sha" "$path"
    return 0
  fi
  printf '%s  %s\n' "$sha" "$path" | sha256sum -c - >/dev/null
}

download_cuda_debs() {
  local entry file sha dest tmp url

  run mkdir -p "$debs_dir"
  for entry in "${CUDA_PACKAGES[@]}"; do
    file="${entry%%|*}"
    sha="${entry##*|}"
    dest="$debs_dir/$file"
    url="${CUDA_REPO_URL%/}/$file"

    if [ -f "$dest" ]; then
      verify_sha256 "$dest" "$sha" || die "checksum failed for existing $dest"
      continue
    fi

    tmp="$dest.tmp"
    run curl -fL --retry 3 -o "$tmp" "$url"
    run mv "$tmp" "$dest"
    verify_sha256 "$dest" "$sha" || die "checksum failed for downloaded $dest"
  done
}

extract_cuda_debs() {
  local entry file deb

  run mkdir -p "$cuda_root"
  for entry in "${CUDA_PACKAGES[@]}"; do
    file="${entry%%|*}"
    deb="$debs_dir/$file"
    if [ "$dry_run" -eq 0 ] && [ ! -f "$deb" ]; then
      die "missing CUDA deb: $deb"
    fi
    run dpkg-deb -x "$deb" "$cuda_root"
  done
}

resolve_cuda_home() {
  if [ -n "$cuda_home" ]; then
    cd "$cuda_home" && pwd -P
    return
  fi
  printf '%s/usr/local/cuda-%s\n' "$cuda_root" "$CUDA_VERSION_DOT"
}

resolve_cuda_lib_dir() {
  local home="$1"

  if [ -d "$home/targets/x86_64-linux/lib" ]; then
    cd "$home/targets/x86_64-linux/lib" && pwd -P
    return
  fi
  if [ -d "$home/lib64" ]; then
    cd "$home/lib64" && pwd -P
    return
  fi
  if [ "$dry_run" -eq 1 ]; then
    printf '%s/targets/x86_64-linux/lib\n' "$home"
    return
  fi
  die "could not find CUDA library directory under $home"
}

parse_args() {
  while [ "$#" -gt 0 ]; do
    case "$1" in
      --llama-dir)
        [ "$#" -ge 2 ] || die "--llama-dir requires a path"
        llama_dir="$2"
        shift
        ;;
      --build-dir)
        [ "$#" -ge 2 ] || die "--build-dir requires a path"
        build_dir="$2"
        shift
        ;;
      --cuda-root)
        [ "$#" -ge 2 ] || die "--cuda-root requires a path"
        cuda_root="$2"
        shift
        ;;
      --cuda-home)
        [ "$#" -ge 2 ] || die "--cuda-home requires a path"
        cuda_home="$2"
        download_cuda=0
        shift
        ;;
      --debs-dir)
        [ "$#" -ge 2 ] || die "--debs-dir requires a path"
        debs_dir="$2"
        shift
        ;;
      --cuda-arch)
        [ "$#" -ge 2 ] || die "--cuda-arch requires a value"
        cuda_arch="$2"
        shift
        ;;
      --jobs)
        [ "$#" -ge 2 ] || die "--jobs requires a value"
        jobs="$2"
        shift
        ;;
      --repo-url)
        [ "$#" -ge 2 ] || die "--repo-url requires a URL"
        CUDA_REPO_URL="$2"
        shift
        ;;
      --no-download)
        download_cuda=0
        ;;
      --no-verify)
        verify_build=0
        ;;
      --with-ui)
        with_ui=1
        ;;
      --with-nccl)
        with_nccl=1
        ;;
      --dry-run)
        dry_run=1
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
}

main() {
  local cuda_lib nvcc host_cxx server ui_flag prebuilt_ui_flag nccl_flag
  local llama_commit verify_ld_path

  parse_args "$@"
  [ -n "$jobs" ] || jobs="$(default_jobs)"

  case "$(uname -s):$(uname -m)" in
    Linux:x86_64|Linux:amd64)
      ;;
    *)
      die "CUDA llama-server dev build currently supports Linux x86_64 hosts only"
      ;;
  esac

  need_cmd cmake
  need_cmd ninja
  need_cmd git
  need_cmd g++
  if [ "$download_cuda" -eq 1 ]; then
    need_cmd curl
    need_cmd dpkg-deb
    need_cmd sha256sum
  fi

  host_cxx="$(command -v g++)"
  [ -d "$llama_dir" ] || die "llama.cpp checkout not found: $llama_dir"
  llama_dir="$(cd "$llama_dir" && pwd -P)"
  build_dir="$(abs_dir "$build_dir")"
  debs_dir="$(abs_dir "$debs_dir")"
  cuda_root="$(abs_dir "$cuda_root")"

  if [ "$download_cuda" -eq 1 ]; then
    echo "Downloading pinned CUDA $CUDA_VERSION_DOT debs into $debs_dir"
    download_cuda_debs
    echo "Extracting CUDA $CUDA_VERSION_DOT into $cuda_root"
    extract_cuda_debs
  else
    echo "Skipping CUDA deb download/extraction"
  fi

  cuda_home="$(resolve_cuda_home)"
  nvcc="$cuda_home/bin/nvcc"
  cuda_lib="$(resolve_cuda_lib_dir "$cuda_home")"
  [ "$dry_run" -eq 1 ] || [ -x "$nvcc" ] || die "nvcc not executable: $nvcc"

  ui_flag=OFF
  prebuilt_ui_flag=OFF
  if [ "$with_ui" -eq 1 ]; then
    ui_flag=ON
    prebuilt_ui_flag=ON
  fi

  nccl_flag=OFF
  if [ "$with_nccl" -eq 1 ]; then
    nccl_flag=ON
  fi

  llama_commit="$(git -C "$llama_dir" rev-parse --short=9 HEAD 2>/dev/null || printf 'unknown')"

  echo "Configuring llama.cpp $llama_commit with CUDA $CUDA_VERSION_DOT"
  run env \
    "PATH=$cuda_home/bin:$PATH" \
    "LD_LIBRARY_PATH=$cuda_lib${LD_LIBRARY_PATH:+:$LD_LIBRARY_PATH}" \
    cmake -G Ninja \
      -S "$llama_dir" \
      -B "$build_dir" \
      -DGGML_CUDA=ON \
      -DGGML_CUDA_NCCL="$nccl_flag" \
      -DCMAKE_CUDA_ARCHITECTURES="$cuda_arch" \
      -DCMAKE_CUDA_COMPILER="$nvcc" \
      -DCMAKE_CUDA_HOST_COMPILER="$host_cxx" \
      -DCUDAToolkit_ROOT="$cuda_home" \
      -DCMAKE_BUILD_TYPE=Release \
      -DLLAMA_BUILD_SERVER=ON \
      -DLLAMA_BUILD_UI="$ui_flag" \
      -DLLAMA_USE_PREBUILT_UI="$prebuilt_ui_flag" \
      "-DCMAKE_BUILD_RPATH=$cuda_lib;$build_dir/bin" \
      "-DCMAKE_EXE_LINKER_FLAGS=-Wl,-rpath,$cuda_lib,-rpath-link,$cuda_lib" \
      "-DCMAKE_SHARED_LINKER_FLAGS=-Wl,-rpath,$cuda_lib,-rpath-link,$cuda_lib"

  echo "Building llama-server"
  run cmake --build "$build_dir" --target llama-server --config Release -j "$jobs"

  server="$build_dir/bin/llama-server"
  if [ "$verify_build" -eq 1 ]; then
    verify_ld_path="$cuda_lib"
    if [ -d /usr/lib/wsl/lib ]; then
      verify_ld_path="$verify_ld_path:/usr/lib/wsl/lib"
    fi
    echo "Verifying llama-server device visibility"
    run env "LD_LIBRARY_PATH=$verify_ld_path${LD_LIBRARY_PATH:+:$LD_LIBRARY_PATH}" "$server" --list-devices
  fi

  cat <<EOF

llama-server: $server
CUDA toolkit: $cuda_home
CUDA libs:    $cuda_lib

Use with microagent:
  export MICROAGENT_LLAMA_SERVER="$server"
  export MICROAGENT_MODEL_RUNNER_ARGS='["-ngl","all","--no-ui"]'

Example:
  microagent model serve hf.co/Qwen/Qwen2.5-Coder-1.5B-Instruct-GGUF@main/qwen2.5-coder-1.5b-instruct-q4_k_m.gguf

Notes:
  - The default build disables llama.cpp's embedded UI assets to avoid npm/HF fetches.
  - Override --cuda-arch for GPUs other than the RTX 3080 Ti's compute capability 8.6.
  - WSL nvidia-smi may show aggregate memory/utilization without per-process rows.
EOF
}

main "$@"
