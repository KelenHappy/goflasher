#!/usr/bin/env bash
set -euo pipefail

root=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)

# shellcheck disable=SC1091
source "$root/packaging/wimlib/lock.sh"
load_wimlib_lock "$root/packaging/wimlib/BUILD.lock"

if [[ $# -ne 2 ]]; then
  echo "usage: $0 SOURCE_TARBALL OUTPUT_DIR" >&2
  exit 64
fi

source_arg=$1
output_arg=$2

if [[ ! -f "$source_arg" ]]; then
  echo "source tarball not found: $source_arg" >&2
  exit 66
fi

source_dir=$(cd "$(dirname "$source_arg")" && pwd)
source_tar="$source_dir/$(basename "$source_arg")"

mkdir -p "$output_arg"
output=$(cd "$output_arg" && pwd)

if [[ ! "$WIMLIB_SOURCE_SHA256" =~ ^[0-9a-f]{64}$ ]]; then
  echo "invalid or unreviewed WIMLIB_SOURCE_SHA256" >&2
  exit 65
fi

if command -v sha256sum >/dev/null 2>&1; then
  actual_sha256=$(sha256sum "$source_tar" | awk '{print $1}')
elif command -v shasum >/dev/null 2>&1; then
  actual_sha256=$(shasum -a 256 "$source_tar" | awk '{print $1}')
else
  echo "no SHA-256 implementation available" >&2
  exit 69
fi

if [[ "$actual_sha256" != "$WIMLIB_SOURCE_SHA256" ]]; then
  echo "source hash mismatch" >&2
  echo "expected: $WIMLIB_SOURCE_SHA256" >&2
  echo "actual:   $actual_sha256" >&2
  exit 65
fi

host_os=$(uname -s)

case "$host_os" in
  Linux)
    if [[ ! -f /etc/os-release ]]; then
      echo "/etc/os-release not found" >&2
      exit 65
    fi

    linux_version=$(
      . /etc/os-release
      printf '%s' "${VERSION_ID:-}"
    )

    if [[ "$linux_version" != "24.04" ]]; then
      echo "unsupported Linux version: $linux_version" >&2
      echo "expected Ubuntu 24.04" >&2
      exit 65
    fi

    if ! command -v clang >/dev/null 2>&1; then
      echo "clang not found" >&2
      exit 69
    fi

    clang_version=$(clang --version | head -1)

    echo "Detected Linux: $linux_version"
    echo "Detected compiler: $clang_version"

    case "$clang_version" in
      *"version 18."*)
        ;;
      *)
        echo "unsupported clang version: $clang_version" >&2
        echo "expected Clang 18.x" >&2
        exit 65
        ;;
    esac
    ;;

  Darwin)
    if ! command -v xcodebuild >/dev/null 2>&1; then
      echo "xcodebuild not found" >&2
      exit 69
    fi

    if ! command -v clang >/dev/null 2>&1; then
      echo "clang not found" >&2
      exit 69
    fi

    xcode_name=$(xcodebuild -version | sed -n '1p')
    xcode_build=$(xcodebuild -version | sed -n '2p')
    clang_version=$(clang --version | head -1)

    echo "Detected Xcode: $xcode_name"
    echo "Detected Xcode build: $xcode_build"
    echo "Detected compiler: $clang_version"

    if [[ "$xcode_name" != "Xcode 16.4" ]]; then
      echo "unsupported Xcode version: $xcode_name" >&2
      echo "expected Xcode 16.4" >&2
      exit 65
    fi
    ;;

  *)
    echo "unsupported build host: $host_os" >&2
    exit 65
    ;;
esac

work=$(mktemp -d)

cleanup() {
  rm -rf "$work"
}

trap cleanup EXIT INT TERM

case "$host_os" in
  Linux)
    tar --no-same-owner -xf "$source_tar" -C "$work"
    ;;
  Darwin)
    tar -xf "$source_tar" -C "$work"
    ;;
esac

src="$work/wimlib-$WIMLIB_VERSION"

if [[ ! -d "$src" ]]; then
  echo "expected source directory not found: $src" >&2
  exit 65
fi

echo "Configuring wimlib $WIMLIB_VERSION"

(
  cd "$src"

  CC=clang ./configure \
    --prefix=/usr \
    "${WIMLIB_CONFIGURE_FLAGS[@]}"

  echo "Building wimlib $WIMLIB_VERSION"
  make -j2
)

case "$host_os" in
  Linux)
    library_source="$src/.libs/libwim.so.15"
    library_output="$output/libwim.so.15"

    if [[ ! -f "$library_source" ]]; then
      echo "expected Linux libwim artifact not found: $library_source" >&2
      exit 65
    fi

    install -m755 "$library_source" "$library_output"
    ;;

  Darwin)
    library_source="$src/.libs/libwim.15.dylib"
    library_output="$output/libwim.15.dylib"

    if [[ ! -f "$library_source" ]]; then
      echo "expected macOS libwim artifact not found: $library_source" >&2
      exit 65
    fi

    install -m755 "$library_source" "$library_output"

    if ! command -v install_name_tool >/dev/null 2>&1; then
      echo "install_name_tool not found" >&2
      exit 69
    fi

    install_name_tool \
      -id '@rpath/libwim.15.dylib' \
      "$library_output"
    ;;
esac

smoke_root="$work/smoke-root"

mkdir -p "$smoke_root"

printf '%s\n' \
  'GoFlasher libwim smoke fixture' \
  >"$smoke_root/file.txt"

wimlib_imagex="$src/wimlib-imagex"

if [[ ! -x "$wimlib_imagex" ]]; then
  echo "wimlib-imagex was not built: $wimlib_imagex" >&2
  exit 65
fi

echo "Creating smoke WIM"

"$wimlib_imagex" capture \
  "$smoke_root" \
  "$output/smoke.wim" \
  --compress=none

if [[ ! -f "$output/smoke.wim" ]]; then
  echo "smoke WIM was not created" >&2
  exit 65
fi

case "$host_os" in
  Linux)
    (
      cd "$output"

      if ! command -v sha256sum >/dev/null 2>&1; then
        echo "sha256sum not available on Linux" >&2
        exit 69
      fi

      sha256sum libwim.so.15 >SHA256SUMS
    )
    ;;

  Darwin)
    (
      cd "$output"

      if ! command -v shasum >/dev/null 2>&1; then
        echo "shasum not available on macOS" >&2
        exit 69
      fi

      shasum -a 256 libwim.15.dylib >SHA256SUMS
    )
    ;;
esac

echo "Built artifact:"
ls -l "$library_output"

echo "Artifact SHA-256:"
cat "$output/SHA256SUMS"

echo "Smoke WIM:"
ls -l "$output/smoke.wim"

echo "Bundled wimlib build completed successfully."
