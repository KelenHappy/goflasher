#!/usr/bin/env bash
set -euo pipefail

if [[ $# -ne 5 ]]; then
  echo "usage: $0 LIBRARY OS ARCH EXPECTED_SHA256 SMOKE_WIM" >&2
  exit 64
fi

lib_arg=$1
os=$2
arch=$3
expected=$4
smoke_arg=$5

if [[ ! -f "$lib_arg" ]]; then
  echo "library not found: $lib_arg" >&2
  exit 66
fi

if [[ ! -f "$smoke_arg" ]]; then
  echo "smoke WIM not found: $smoke_arg" >&2
  exit 66
fi

lib_dir=$(cd "$(dirname "$lib_arg")" && pwd)
lib="$lib_dir/$(basename "$lib_arg")"

smoke_dir=$(cd "$(dirname "$smoke_arg")" && pwd)
smoke="$smoke_dir/$(basename "$smoke_arg")"

if [[ ! "$expected" =~ ^[0-9a-f]{64}$ ]]; then
  echo "invalid expected artifact SHA-256: $expected" >&2
  exit 65
fi

if command -v sha256sum >/dev/null 2>&1; then
  actual=$(sha256sum "$lib" | awk '{print $1}')
elif command -v shasum >/dev/null 2>&1; then
  actual=$(shasum -a 256 "$lib" | awk '{print $1}')
else
  echo "no SHA-256 utility available" >&2
  exit 69
fi

if [[ "$actual" != "$expected" ]]; then
  echo "artifact hash mismatch" >&2
  echo "expected: $expected" >&2
  echo "actual:   $actual" >&2
  exit 65
fi

echo "Artifact SHA-256 verified: $actual"

symbols='wimlib_global_init wimlib_get_version wimlib_get_version_string wimlib_open_wim wimlib_split wimlib_free wimlib_global_cleanup wimlib_get_error_string'

case "$os" in
  linux)
    case "$arch" in
      amd64)
        expected_arch="x86-64"
        ;;
      arm64)
        expected_arch="aarch64"
        ;;
      *)
        echo "unsupported Linux architecture: $arch" >&2
        exit 64
        ;;
    esac

    echo "Verifying Linux ELF architecture"

    file_output=$(file "$lib")
    echo "$file_output"

    if ! printf '%s\n' "$file_output" | grep -F "$expected_arch" >/dev/null; then
      echo "unexpected ELF architecture; expected $expected_arch" >&2
      exit 65
    fi

    echo "Verifying ELF SONAME"

    soname=$(
      readelf -d "$lib" |
        awk '/\(SONAME\)/ {
          if (match($0, /\[[^]]+\]/)) {
            print substr($0, RSTART + 1, RLENGTH - 2)
          }
        }'
    )

    if [[ "$soname" != "libwim.so.15" ]]; then
      echo "unexpected ELF SONAME: ${soname:-<missing>}" >&2
      echo "expected: libwim.so.15" >&2
      exit 65
    fi

    echo "ELF SONAME verified: $soname"

    echo "Verifying Linux runtime dependencies"

    ldd_output=$(ldd "$lib")
    printf '%s\n' "$ldd_output"

    if printf '%s\n' "$ldd_output" |
      grep -E 'not found|=> (\.|/tmp|/home)' >/dev/null; then
      echo "unsafe or unresolved Linux runtime dependency detected" >&2
      exit 65
    fi

    echo "Verifying required exported symbols"

    exported_symbols=$(nm -D --defined-only "$lib" | awk '{print $3}')

    for symbol in $symbols; do
      if ! printf '%s\n' "$exported_symbols" |
        grep -Fx "$symbol" >/dev/null; then
        echo "missing required libwim symbol: $symbol" >&2
        exit 65
      fi
    done
    ;;

  macos)
    case "$arch" in
      amd64)
        expected_arch="x86_64"
        ;;
      arm64)
        expected_arch="arm64"
        ;;
      *)
        echo "unsupported macOS architecture: $arch" >&2
        exit 64
        ;;
    esac

    echo "Verifying Mach-O architecture"

    architectures=$(lipo -archs "$lib")
    echo "Architectures: $architectures"

    if ! printf '%s\n' "$architectures" |
      tr ' ' '\n' |
      grep -Fx "$expected_arch" >/dev/null; then
      echo "unexpected Mach-O architecture; expected $expected_arch" >&2
      exit 65
    fi

    echo "Verifying Mach-O install name"

    install_names=$(otool -D "$lib")
    printf '%s\n' "$install_names"

    if ! printf '%s\n' "$install_names" |
      grep -Fx '@rpath/libwim.15.dylib' >/dev/null; then
      echo "unexpected or missing libwim dylib install name" >&2
      exit 65
    fi

    echo "Verifying macOS runtime dependencies"

    dependency_paths=$(
      otool -L "$lib" |
        tail -n +2 |
        awk '{print $1}'
    )

    printf '%s\n' "$dependency_paths"

    unexpected_dependencies=$(
      printf '%s\n' "$dependency_paths" |
        grep -Ev '^(@rpath/|@loader_path/|/usr/lib/|/System/Library/)' ||
        true
    )

    if [[ -n "$unexpected_dependencies" ]]; then
      echo "unexpected macOS runtime dependencies:" >&2
      printf '%s\n' "$unexpected_dependencies" >&2
      exit 65
    fi

    echo "Verifying required exported symbols"

    exported_symbols=$(nm -gU "$lib" | awk '{print $3}')

    for symbol in $symbols; do
      if ! printf '%s\n' "$exported_symbols" |
        grep -Fx "_$symbol" >/dev/null; then
        echo "missing required libwim symbol: $symbol" >&2
        exit 65
      fi
    done
    ;;

  *)
    echo "unsupported verification OS: $os" >&2
    exit 64
    ;;
esac

#
# Exercise the exact PureGo loader, version/ABI checks,
# WIM open path, and split call.
#

root=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)

work=$(mktemp -d)

cleanup() {
  rm -rf "$work"
}

trap cleanup EXIT INT TERM

echo "Building PureGo libwim smoke executable"

if [[ "$os" == "macos" ]]; then
  mkdir -p \
    "$work/GoFlasher.app/Contents/MacOS" \
    "$work/GoFlasher.app/Contents/Frameworks"

  cp \
    "$lib" \
    "$work/GoFlasher.app/Contents/Frameworks/libwim.15.dylib"

  (
    cd "$root"
    go build \
      -o "$work/GoFlasher.app/Contents/MacOS/wim-smoke" \
      ./cmd/wim-smoke
  )

  echo "Running macOS PureGo libwim smoke test"

  "$work/GoFlasher.app/Contents/MacOS/wim-smoke" "$smoke"

else
  mkdir -p "$work/app/lib/wimlib/1.14.5"

  cp \
    "$lib" \
    "$work/app/lib/wimlib/1.14.5/libwim.so.15"

  (
    cd "$root"
    go build \
      -o "$work/app/wim-smoke" \
      ./cmd/wim-smoke
  )

  echo "Running Linux PureGo libwim smoke test"

  "$work/app/wim-smoke" "$smoke"
fi

echo "Bundled libwim verification completed successfully."
