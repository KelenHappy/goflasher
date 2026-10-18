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

#
# Verify pinned source hash.
#

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

#
# Verify build host/toolchain.
#

host_os=$(uname -s)

# Git Bash reports e.g. MINGW64_NT-10.0-26100.
case "$host_os" in
  MINGW64_NT-*) host_os=Windows ;;
esac

case "$host_os" in
  Linux)
    if [[ ! -f /etc/os-release ]]; then
      echo "/etc/os-release not found" >&2
      exit 65
    fi

    linux_version=$(
      # shellcheck source=/dev/null
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

  Windows)
    # libwim.a and the GUI must share one C runtime, so this is the exact
    # WinLibs UCRT release CI installs (WINDOWS_TOOLCHAIN in BUILD.lock).
    for tool in gcc mingw32-make pkg-config; do
      if ! command -v "$tool" >/dev/null 2>&1; then
        echo "$tool not found" >&2
        exit 69
      fi
    done

    gcc_version=$(gcc --version | head -1)

    echo "Detected compiler: $gcc_version"

    case "$gcc_version" in
      *"(MinGW-W64 x86_64-ucrt-posix-seh, built by Brecht Sanders, r2) 16.2.0")
        ;;
      *)
        echo "unsupported gcc: $gcc_version" >&2
        echo "expected WinLibs GCC 16.2.0 UCRT r2" >&2
        exit 65
        ;;
    esac
    ;;

  *)
    echo "unsupported build host: $host_os" >&2
    exit 65
    ;;
esac

#
# Prepare temporary source tree.
#

work=$(mktemp -d)

cleanup() {
  rm -rf "$work"
}

trap cleanup EXIT INT TERM

case "$host_os" in
  Linux)
    tar --no-same-owner -xf "$source_tar" -C "$work"
    ;;

  Darwin | Windows)
    tar -xf "$source_tar" -C "$work"
    ;;
esac

src="$work/wimlib-$WIMLIB_VERSION"

if [[ ! -d "$src" ]]; then
  echo "expected source directory not found: $src" >&2
  exit 65
fi

#
# Configure and build wimlib.
#
# Only the static archive is built: GoFlasher links it into the GUI executable
# through cgo (internal/wim/native, wimlib build tag). The source prefix map
# keeps the random temporary build directory out of the archive, and
# ZERO_AR_DATE makes Apple's archiver omit member timestamps, so the archive
# hash depends on the source, toolchain, and flags only.
#
# On Windows the compiler sees native paths, so the prefix map uses those.
# MinGW's make expands $(SHELL) to the unquoted "C:/Program Files/Git/..."
# path, which breaks automake's $(LIBTOOL), so libtool runs through the sh on
# PATH instead. WinLibs' binutils do not default to deterministic archives,
# so ar and ranlib get D to zero the member timestamps.
#

case "$host_os" in
  Windows)
    cc=gcc
    compiler_src=$(cygpath -m "$src")
    make_cmd=(mingw32-make "LIBTOOL=sh ./libtool")
    export AR_FLAGS=crD RANLIB="ranlib -D"
    exe=.exe
    ;;
  *)
    cc=clang
    compiler_src=$src
    make_cmd=(make)
    exe=
    ;;
esac

echo "Configuring wimlib $WIMLIB_VERSION"

(
  cd "$src"

  CC=$cc \
  MAKE=${make_cmd[0]} \
  CFLAGS="-g -O2 -ffile-prefix-map=$compiler_src=wimlib-$WIMLIB_VERSION" \
  ./configure \
    --prefix=/usr \
    "${WIMLIB_CONFIGURE_FLAGS[@]}"

  echo "Building wimlib $WIMLIB_VERSION"
  ZERO_AR_DATE=1 "${make_cmd[@]}" -j2
)

#
# Copy the static archive and its header into the output directory, laid out
# for CGO_CFLAGS=-I<output>/include and CGO_LDFLAGS=-L<output>/lib.
#

archive_source="$src/.libs/libwim.a"
archive_output="$output/lib/libwim.a"

if [[ ! -f "$archive_source" ]]; then
  echo "expected libwim archive not found: $archive_source" >&2
  exit 65
fi

install -d "$output/lib" "$output/include"
install -m644 "$archive_source" "$archive_output"
install -m644 "$src/include/wimlib.h" "$output/include/wimlib.h"

#
# Build a small smoke-test WIM using the newly built wimlib-imagex.
#

smoke_root="$work/smoke-root"
mkdir -p "$smoke_root"

printf '%s\n' \
  'GoFlasher libwim smoke fixture' \
  >"$smoke_root/file.txt"

wimlib_imagex="$src/wimlib-imagex$exe"

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

#
# Generate artifact SHA-256 manifest.
#

if command -v sha256sum >/dev/null 2>&1; then
  (
    cd "$output/lib"
    sha256sum libwim.a >"$output/SHA256SUMS"
  )
elif command -v shasum >/dev/null 2>&1; then
  (
    cd "$output/lib"
    shasum -a 256 libwim.a >"$output/SHA256SUMS"
  )
else
  echo "no SHA-256 implementation available" >&2
  exit 69
fi

#
# Final diagnostics.
#

echo
echo "Built artifact:"
ls -l "$archive_output"

echo
echo "Artifact SHA-256:"
cat "$output/SHA256SUMS"

echo
echo "Smoke WIM:"
ls -l "$output/smoke.wim"

echo
echo "Bundled wimlib build completed successfully."
