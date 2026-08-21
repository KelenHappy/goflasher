#!/usr/bin/env bash
set -euo pipefail
root=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
# shellcheck disable=SC1091
source "$root/packaging/wimlib/BUILD.lock"
if [[ $# -ne 2 ]]; then echo "usage: $0 SOURCE_TARBALL OUTPUT_DIR" >&2; exit 64; fi
source_tar=$(realpath "$1"); output=$(realpath -m "$2")
[[ "$WIMLIB_SOURCE_SHA256" =~ ^[0-9a-f]{64}$ ]] || { echo "unreviewed WIMLIB_SOURCE_SHA256" >&2; exit 65; }
if command -v sha256sum >/dev/null; then actual=$(sha256sum "$source_tar" | awk '{print $1}'); else actual=$(shasum -a 256 "$source_tar" | awk '{print $1}'); fi
[[ "$actual" = "$WIMLIB_SOURCE_SHA256" ]] || { echo "source hash mismatch" >&2; exit 65; }
case "$(uname -s)" in
  Linux) test "$(. /etc/os-release; echo "$VERSION_ID")" = 24.04; test "$(clang --version | head -1)" = *"version 18."* ;;
  Darwin) test "$(xcodebuild -version | tr '\n' ' ')" = "Xcode 16.4 Build version "* ;;
  *) echo "unsupported build host" >&2; exit 65 ;;
esac
work=$(mktemp -d); trap 'rm -rf "$work"' EXIT
tar --no-same-owner -xf "$source_tar" -C "$work"
src="$work/wimlib-$WIMLIB_VERSION"; test -d "$src"
(cd "$src" && CC=clang ./configure --prefix=/usr $CONFIGURE_FLAGS && make -j2)
mkdir -p "$output"
case "$(uname -s)" in
  Linux) install -m755 "$src/.libs/libwim.so.15" "$output/libwim.so.15" ;;
  Darwin)
    install -m755 "$src/.libs/libwim.15.dylib" "$output/libwim.15.dylib"
    install_name_tool -id '@rpath/libwim.15.dylib' "$output/libwim.15.dylib"
    ;;
esac
mkdir "$work/smoke-root"
printf 'GoFlasher libwim smoke fixture\n' >"$work/smoke-root/file.txt"
"$src/wimlib-imagex" capture "$work/smoke-root" "$output/smoke.wim" --compress=none
if command -v sha256sum >/dev/null; then sha256sum "$output"/libwim* >"$output/SHA256SUMS"; else shasum -a 256 "$output"/libwim* >"$output/SHA256SUMS"; fi
