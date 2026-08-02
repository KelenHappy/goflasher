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
appdir=$(mktemp -d)
trap 'rm -rf "$appdir"' EXIT

mkdir -p "$output"

APPIMAGE_EXTRACT_AND_RUN=1 "$linuxdeploy" \
  --appdir "$appdir" \
  --executable "$binary" \
  --desktop-file "$root/packaging/org.goflasher.usbwriter.desktop" \
  --icon-file "$root/packaging/org.goflasher.usbwriter.svg"

ARCH=${APPIMAGE_ARCH:-x86_64} VERSION="$version" APPIMAGE_EXTRACT_AND_RUN=1 \
  "$appimagetool" "$appdir" "$output/GoFlasher-${version}-${APPIMAGE_ARCH:-x86_64}.AppImage"
