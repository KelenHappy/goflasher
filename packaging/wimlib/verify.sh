#!/usr/bin/env bash
set -euo pipefail

if [[ $# -ne 5 ]]; then
  echo "usage: $0 BUILD_OUTPUT OS ARCH EXPECTED_SHA256 SMOKE_WIM" >&2
  exit 64
fi

output_arg=$1
os=$2
arch=$3
expected=$4
smoke_arg=$5

if [[ ! -d "$output_arg" ]]; then
  echo "build output not found: $output_arg" >&2
  exit 66
fi

if [[ ! -f "$smoke_arg" ]]; then
  echo "smoke WIM not found: $smoke_arg" >&2
  exit 66
fi

output=$(cd "$output_arg" && pwd)
archive="$output/lib/libwim.a"

smoke_dir=$(cd "$(dirname "$smoke_arg")" && pwd)
smoke="$smoke_dir/$(basename "$smoke_arg")"

for required in "$archive" "$output/include/wimlib.h"; do
  if [[ ! -f "$required" ]]; then
    echo "build output is incomplete: $required" >&2
    exit 66
  fi
done

if [[ ! "$expected" =~ ^[0-9a-f]{64}$ ]]; then
  echo "invalid expected artifact SHA-256: $expected" >&2
  exit 65
fi

if command -v sha256sum >/dev/null 2>&1; then
  actual=$(sha256sum "$archive" | awk '{print $1}')
elif command -v shasum >/dev/null 2>&1; then
  actual=$(shasum -a 256 "$archive" | awk '{print $1}')
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

symbols='wimlib_global_init wimlib_get_version wimlib_get_version_string wimlib_open_wim wimlib_split wimlib_register_progress_function wimlib_free wimlib_global_cleanup wimlib_get_error_string'

case "$os" in
  linux)
    symbol_prefix=
    case "$arch" in
      amd64) expected_arch="x86-64" ;;
      arm64) expected_arch="aarch64" ;;
      *)
        echo "unsupported Linux architecture: $arch" >&2
        exit 64
        ;;
    esac
    ;;

  macos)
    symbol_prefix=_
    case "$arch" in
      amd64) expected_arch="x86_64" ;;
      arm64) expected_arch="arm64" ;;
      *)
        echo "unsupported macOS architecture: $arch" >&2
        exit 64
        ;;
    esac
    ;;

  windows)
    symbol_prefix=
    case "$arch" in
      amd64) expected_arch="x86-64" ;;
      *)
        echo "unsupported Windows architecture: $arch" >&2
        exit 64
        ;;
    esac
    ;;

  *)
    echo "unsupported verification OS: $os" >&2
    exit 64
    ;;
esac

echo "Verifying required archive symbols"

defined_symbols=$(nm -g "$archive" 2>/dev/null | awk '$2 == "T" {print $3}')

for symbol in $symbols; do
  if ! printf '%s\n' "$defined_symbols" |
    grep -Fx "$symbol_prefix$symbol" >/dev/null; then
    echo "missing required libwim symbol: $symbol" >&2
    exit 65
  fi
done

#
# Link the archive into the wimlib-tagged smoke executable and the native
# package tests, and exercise the version check, WIM open, and split calls the
# application uses.
#

root=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)

work=$(mktemp -d)

cleanup() {
  rm -rf "$work"
}

trap cleanup EXIT INT TERM

# Under Git Bash, cgo's native gcc needs Windows paths.
if [[ "$os" == "windows" ]]; then
  cgo_output=$(cygpath -m "$output")
  smoke_exe="$work/wim-smoke.exe"
else
  cgo_output=$output
  smoke_exe="$work/wim-smoke"
fi

export CGO_ENABLED=1
export CGO_CFLAGS="-I$cgo_output/include"
export CGO_LDFLAGS="-L$cgo_output/lib"

echo "Running wimlib-tagged WIM package tests"

(
  cd "$root"
  go test -tags wimlib ./internal/wim/...
)

echo "Building statically linked libwim smoke executable"

(
  cd "$root"
  go build -tags wimlib -o "$smoke_exe" ./cmd/wim-smoke
)

file_output=$(file "$smoke_exe")
echo "$file_output"

if ! printf '%s\n' "$file_output" | grep -F "$expected_arch" >/dev/null; then
  echo "unexpected smoke executable architecture; expected $expected_arch" >&2
  exit 65
fi

echo "Verifying that libwim is not loaded dynamically"

case "$os" in
  macos) dependencies=$(otool -L "$smoke_exe") ;;
  windows) dependencies=$(objdump -p "$smoke_exe" | grep -F 'DLL Name') ;;
  *) dependencies=$(ldd "$smoke_exe") ;;
esac

printf '%s\n' "$dependencies"

if printf '%s\n' "$dependencies" | grep -F libwim >/dev/null; then
  echo "smoke executable depends on a shared libwim" >&2
  exit 65
fi

if ! "$root/packaging/legal/links-wimlib.sh" "$smoke_exe"; then
  echo "smoke executable lacks the linked-wimlib marker" >&2
  exit 65
fi

echo "Running libwim smoke test"

"$smoke_exe" "$smoke"

echo "Bundled libwim verification completed successfully."
