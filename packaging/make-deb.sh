#!/usr/bin/env bash
set -euo pipefail

if [[ $# -ne 3 ]]; then
  echo "usage: $0 BINARY VERSION OUTPUT_DIR" >&2
  exit 2
fi

binary=$(realpath "$1")
version=${2#v}
# Git permits underscores in tag names, but Debian versions do not. Preserve
# the tag's components while converting that separator to a Debian-safe one.
version=${version//_/.}
output=$(realpath -m "$3")
root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
xz_dir=$(cd "$root" && go list -m -f '{{.Dir}}' github.com/ulikunitz/xz)
arch=${DEB_ARCH:-$(dpkg --print-architecture)}
stage=$(mktemp -d)
trap 'rm -rf "$stage"' EXIT

install -Dm755 "$binary" "$stage/usr/bin/goflasher"
go build -trimpath -o "$stage/usr/libexec/goflasher-helper" "$root/cmd/goflasher-helper"
install -Dm644 "$root/packaging/org.goflasher.usbwriter.policy" \
  "$stage/usr/share/polkit-1/actions/org.goflasher.usbwriter.policy"
install -Dm644 "$root/packaging/org.goflasher.usbwriter.desktop" \
  "$stage/usr/share/applications/org.goflasher.usbwriter.desktop"
install -Dm644 "$root/packaging/org.goflasher.usbwriter.svg" \
  "$stage/usr/share/icons/hicolor/scalable/apps/org.goflasher.usbwriter.svg"
install -Dm644 "$root/LICENSE" "$stage/usr/share/doc/goflasher/copyright"
install -Dm644 "$root/docs/legal/THIRD_PARTY_NOTICES.md" \
  "$stage/usr/share/doc/goflasher/THIRD_PARTY_NOTICES.md"
install -Dm644 "$xz_dir/LICENSE" \
  "$stage/usr/share/doc/goflasher/third-party/github.com_ulikunitz_xz_LICENSE"
mkdir -p "$stage/DEBIAN" "$output"
cat >"$stage/DEBIAN/control" <<EOF
Package: goflasher
Version: $version
Section: utils
Priority: optional
Architecture: $arch
Depends: policykit-1, udisks2, libc6, libgl1, libx11-6
Maintainer: GoFlasher contributors
Description: Safety-first USB image writer
 GoFlasher writes raw and compressed disk images to positively identified
 removable USB flash media and can verify and eject the result.
EOF

dpkg-deb --root-owner-group --build "$stage" "$output/goflasher_${version}_${arch}.deb"
