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
purego_dir=$(cd "$root" && go list -m -f '{{.Dir}}' github.com/ebitengine/purego)
arch=${DEB_ARCH:-$(dpkg --print-architecture)}
stage=$(mktemp -d)
trap 'rm -rf "$stage"' EXIT

install -Dm755 "$binary" "$stage/usr/bin/goflasher"
if [[ -n "${LIBWIM_LIBRARY:-}" ]]; then
  install -Dm755 "$LIBWIM_LIBRARY" "$stage/usr/lib/goflasher/lib/wimlib/1.14.5/libwim.so.15"
  "$root/packaging/legal/install-wimlib-record.sh" "$stage/usr/share/doc/goflasher/third-party/wimlib-1.14.5"
fi
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
install -Dm644 "$root/docs/legal/THIRD_PARTY_NOTICES.zh-TW.md" \
  "$stage/usr/share/doc/goflasher/THIRD_PARTY_NOTICES.zh-TW.md"
install -Dm644 "$xz_dir/LICENSE" \
  "$stage/usr/share/doc/goflasher/third-party/github.com_ulikunitz_xz_LICENSE"
install -Dm644 "$purego_dir/LICENSE" "$stage/usr/share/doc/goflasher/third-party/github.com_ebitengine_purego_LICENSE"
install -Dm644 "$(go env GOROOT)/LICENSE" "$stage/usr/share/doc/goflasher/third-party/golang_LICENSE"
WIMLIB_COMPLIANCE_RECORD=${WIMLIB_COMPLIANCE_RECORD:-} "$root/packaging/legal/verify-release.sh" "$stage"
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
