#!/usr/bin/env bash
set -euo pipefail

if [[ $# -ne 3 ]]; then
  echo "usage: $0 BINARY VERSION OUTPUT_DIR" >&2
  exit 2
fi

binary=$(realpath "$1")
version=${2#v}
rpm_version=${version//-/_}
output=$(realpath -m "$3")
root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
arch=${RPM_ARCH:-$(uname -m)}
topdir=$(mktemp -d)
stage="$topdir/stage"
trap 'rm -rf "$topdir"' EXIT

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
install -Dm644 "$root/LICENSE" "$stage/usr/share/doc/goflasher/copyright"

mkdir -p "$topdir/BUILD" "$topdir/BUILDROOT" "$topdir/RPMS" \
  "$topdir/SOURCES" "$topdir/SPECS" "$topdir/SRPMS" "$output"
cat >"$topdir/SPECS/goflasher.spec" <<EOF_SPEC
Name: goflasher
Version: $rpm_version
Release: 1%{?dist}
Summary: Safety-first USB image writer
License: GPL-3.0-or-later
URL: https://github.com/KelenHappy/goflasher
BuildArch: $arch
Requires: polkit
Requires: udisks2
Requires: dosfstools
Requires: xz
Requires: glibc
Requires: libX11
Requires: libglvnd-glx

%description
GoFlasher writes raw and compressed disk images to positively identified
removable USB flash media and can verify and eject the result.

%install
rm -rf %{buildroot}
cp -a $stage/* %{buildroot}/

%files
%license /usr/share/doc/goflasher/copyright
/usr/bin/goflasher
/usr/libexec/goflasher-helper
/usr/share/polkit-1/actions/org.goflasher.usbwriter.policy
/usr/share/applications/org.goflasher.usbwriter.desktop
/usr/share/icons/hicolor/scalable/apps/org.goflasher.usbwriter.svg
EOF_SPEC

rpmbuild --define "_topdir $topdir" --target "$arch" -bb "$topdir/SPECS/goflasher.spec"
cp "$topdir"/RPMS/*/goflasher-"$rpm_version"-1*.rpm "$output/"
