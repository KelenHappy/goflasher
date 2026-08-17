#!/usr/bin/env bash
set -euo pipefail

if [[ $# -ne 5 ]]; then
  echo "usage: $0 BINARY VERSION OUTPUT_DIR LINUXDEPLOY APPIMAGETOOL" >&2
  exit 2
fi

binary=$(realpath "$1")
version=${2#v}
output=$(realpath -m "$3")
linuxdeploy=$(realpath "$4")
appimagetool=$(realpath "$5")
root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
xz_dir=$(cd "$root" && go list -m -f '{{.Dir}}' github.com/ulikunitz/xz)
appdir=$(mktemp -d)
trap 'rm -rf "$appdir"' EXIT

mkdir -p "$output"

APPIMAGE_EXTRACT_AND_RUN=1 "$linuxdeploy" \
  --appdir "$appdir" \
  --executable "$binary" \
  --desktop-file "$root/packaging/org.goflasher.usbwriter.desktop" \
  --icon-file "$root/packaging/org.goflasher.usbwriter.svg"

# Keep the separately built helper in the standard AppDir libexec location. The
# GUI discovers it through APPDIR, so an AppImage can perform privileged work
# without requiring users to extract and install a second executable first.
go build -trimpath -o "$appdir/usr/libexec/goflasher-helper" "$root/cmd/goflasher-helper"
install -Dm644 "$root/packaging/org.goflasher.usbwriter.policy" \
  "$appdir/usr/share/goflasher/org.goflasher.usbwriter.policy"
install -Dm644 "$root/docs/legal/THIRD_PARTY_NOTICES.md" \
  "$appdir/usr/share/doc/goflasher/THIRD_PARTY_NOTICES.md"
install -Dm644 "$xz_dir/LICENSE" \
  "$appdir/usr/share/doc/goflasher/third-party/github.com_ulikunitz_xz_LICENSE"

ARCH=${APPIMAGE_ARCH:-x86_64} VERSION="$version" APPIMAGE_EXTRACT_AND_RUN=1 \
  "$appimagetool" "$appdir" "$output/GoFlasher-${version}-${APPIMAGE_ARCH:-x86_64}.AppImage"
