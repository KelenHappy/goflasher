#!/usr/bin/env bash
set -euo pipefail

if [[ $# -ne 3 ]]; then
  echo "usage: $0 BINARY VERSION OUTPUT_DIR" >&2
  exit 2
fi

binary=$(realpath "$1")
version=${2#v}
pkgver=${version//-/_}
output=$(realpath -m "$3")
root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
xz_dir=$(cd "$root" && go list -m -f '{{.Dir}}' github.com/ulikunitz/xz)
arch=${ARCH_PKG_ARCH:-$(uname -m)}
stage=$(mktemp -d)
trap 'rm -rf "$stage"' EXIT

case "$arch" in
  amd64) arch=x86_64 ;;
  arm64) arch=aarch64 ;;
esac

install -Dm755 "$binary" "$stage/usr/bin/goflasher"
go build -trimpath -o "$stage/usr/libexec/goflasher-helper" "$root/cmd/goflasher-helper"
install -Dm644 "$root/packaging/org.goflasher.usbwriter.policy" \
  "$stage/usr/share/polkit-1/actions/org.goflasher.usbwriter.policy"
install -Dm644 "$root/packaging/org.goflasher.usbwriter.desktop" \
  "$stage/usr/share/applications/org.goflasher.usbwriter.desktop"
install -Dm644 "$root/packaging/org.goflasher.usbwriter.svg" \
  "$stage/usr/share/icons/hicolor/scalable/apps/org.goflasher.usbwriter.svg"
install -Dm644 "$root/LICENSE" "$stage/usr/share/licenses/goflasher/LICENSE"
install -Dm644 "$root/docs/legal/THIRD_PARTY_NOTICES.md" \
  "$stage/usr/share/doc/goflasher/THIRD_PARTY_NOTICES.md"
install -Dm644 "$xz_dir/LICENSE" \
  "$stage/usr/share/doc/goflasher/third-party/github.com_ulikunitz_xz_LICENSE"

size=$(du -sk "$stage" | cut -f1)
cat >"$stage/.PKGINFO" <<EOF
pkgname = goflasher
pkgbase = goflasher
pkgver = $pkgver-1
pkgdesc = Safety-first USB image writer
url = https://github.com/goflasher/goflasher
builddate = ${SOURCE_DATE_EPOCH:-$(date +%s)}
packager = GoFlasher contributors
size = $((size * 1024))
arch = $arch
license = GPL-3.0-or-later
depend = polkit
depend = udisks2
depend = glibc
depend = libx11
depend = libglvnd
depend = libxcursor
depend = libxrandr
depend = libxinerama
depend = libxi
depend = libxkbcommon
depend = wayland
EOF

mkdir -p "$output"
tar --zstd --sort=name --owner=0 --group=0 --numeric-owner \
  -cf "$output/goflasher-${pkgver}-1-${arch}.pkg.tar.zst" -C "$stage" .
