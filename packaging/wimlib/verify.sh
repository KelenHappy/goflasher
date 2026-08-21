#!/usr/bin/env bash
set -euo pipefail
if [[ $# -ne 5 ]]; then echo "usage: $0 LIBRARY OS ARCH EXPECTED_SHA256 SMOKE_WIM" >&2; exit 64; fi
lib=$(realpath "$1"); os=$2; arch=$3; expected=$4; smoke=$(realpath "$5")
if command -v sha256sum >/dev/null; then actual=$(sha256sum "$lib" | awk '{print $1}'); else actual=$(shasum -a 256 "$lib" | awk '{print $1}'); fi
[[ "$actual" = "$expected" ]] || { echo "artifact hash mismatch" >&2; exit 65; }
symbols='wimlib_global_init wimlib_get_version wimlib_get_version_string wimlib_open_wim wimlib_split wimlib_free wimlib_global_cleanup wimlib_get_error_string'
case "$os" in
  linux)
    file "$lib" | grep -F "${arch/amd64/x86-64}"
    readelf -d "$lib" | grep -F 'SONAME.*libwim.so.15'
    ! ldd "$lib" | grep -E 'not found|=> (\.|/tmp|/home)'
    for symbol in $symbols; do nm -D --defined-only "$lib" | awk '{print $3}' | grep -Fx "$symbol"; done
    ;;
  macos)
    lipo -archs "$lib" | tr ' ' '\n' | grep -Fx "${arch/amd64/x86_64}"
    otool -D "$lib" | grep -Fx '@rpath/libwim.15.dylib'
    ! otool -L "$lib" | tail -n +2 | awk '{print $1}' | grep -Ev '^(@rpath/|@loader_path/|/usr/lib/|/System/Library/)'
    for symbol in $symbols; do nm -gU "$lib" | awk '{print $3}' | grep -Fx "_$symbol"; done
    ;;
  *) exit 64 ;;
esac

# Exercise the exact PureGo loader, version/ABI checks, open, and split call.
root=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
work=$(mktemp -d); trap 'rm -rf "$work"' EXIT
if [[ "$os" = macos ]]; then
  mkdir -p "$work/GoFlasher.app/Contents/MacOS" "$work/GoFlasher.app/Contents/Frameworks"
  cp "$lib" "$work/GoFlasher.app/Contents/Frameworks/libwim.15.dylib"
  go build -o "$work/GoFlasher.app/Contents/MacOS/wim-smoke" "$root/cmd/wim-smoke"
  "$work/GoFlasher.app/Contents/MacOS/wim-smoke" "$smoke"
else
  mkdir -p "$work/app/lib/wimlib/1.14.4"
  cp "$lib" "$work/app/lib/wimlib/1.14.4/libwim.so.15"
  go build -o "$work/app/wim-smoke" "$root/cmd/wim-smoke"
  "$work/app/wim-smoke" "$smoke"
fi
