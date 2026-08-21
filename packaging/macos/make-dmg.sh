#!/bin/bash
set -euo pipefail

if [[ $# -ne 5 ]]; then
  echo "usage: $0 GUI HELPER VERSION ARCH OUTPUT" >&2
  exit 64
fi

GUI=$1
HELPER=$2
VERSION=$3
ARCH=$4
OUTPUT=$5

ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)

LIBWIM_DYLIB=${LIBWIM_DYLIB:-}
WIMLIB_COMPLIANCE_RECORD=${WIMLIB_COMPLIANCE_RECORD:-}

case "$ARCH" in
  amd64)
    PLIST_ARCH=x86_64
    ;;
  arm64)
    PLIST_ARCH=arm64
    ;;
  *)
    echo "unsupported architecture: $ARCH" >&2
    exit 64
    ;;
esac

[[ -f "$GUI" ]] || {
  echo "GUI executable not found: $GUI" >&2
  exit 66
}

[[ -f "$HELPER" ]] || {
  echo "helper executable not found: $HELPER" >&2
  exit 66
}

# libwim is the bundled macOS WIM-splitting backend.
[[ -n "$LIBWIM_DYLIB" ]] || {
  echo "LIBWIM_DYLIB is required for release packaging" >&2
  exit 65
}

[[ -f "$LIBWIM_DYLIB" ]] || {
  echo "libwim dylib not found: $LIBWIM_DYLIB" >&2
  exit 66
}

[[ -n "$WIMLIB_COMPLIANCE_RECORD" && -d "$WIMLIB_COMPLIANCE_RECORD" ]] || {
  echo "approved WIMLIB_COMPLIANCE_RECORD is required" >&2
  exit 65
}

# Apple bundle versions must remain numeric even when the public artifact
# version contains a prerelease suffix, e.g. v0.1.0-alpha.
BUNDLE_VERSION=${VERSION#v}
BUNDLE_VERSION=${BUNDLE_VERSION%%-*}

if [[ ! "$BUNDLE_VERSION" =~ ^[0-9]+([.][0-9]+){0,2}$ ]]; then
  echo "version does not contain a valid numeric bundle version: $VERSION" >&2
  exit 64
fi

mkdir -p "$OUTPUT"

APP="$OUTPUT/GoFlasher.app"
CONTENTS="$APP/Contents"

rm -rf "$APP"

mkdir -p \
  "$CONTENTS/MacOS" \
  "$CONTENTS/Frameworks" \
  "$CONTENTS/Library/HelperTools" \
  "$CONTENTS/Resources/legal/licenses"

install -m755 \
  "$GUI" \
  "$CONTENTS/MacOS/GoFlasher"

install -m755 \
  "$HELPER" \
  "$CONTENTS/Library/HelperTools/org.goflasher.helper"

install -m644 \
  "$ROOT/LICENSE" \
  "$CONTENTS/Resources/legal/LICENSE"

install -m644 \
  "$ROOT/docs/legal/THIRD_PARTY_NOTICES.md" \
  "$CONTENTS/Resources/legal/"

install -m644 \
  "$ROOT/docs/legal/THIRD_PARTY_NOTICES.zh-TW.md" \
  "$CONTENTS/Resources/legal/"

purego_dir=$(
  cd "$ROOT"
  go list -m -f '{{.Dir}}' github.com/ebitengine/purego
)

xz_dir=$(
  cd "$ROOT"
  go list -m -f '{{.Dir}}' github.com/ulikunitz/xz
)

install -m644 \
  "$purego_dir/LICENSE" \
  "$CONTENTS/Resources/legal/licenses/github.com_ebitengine_purego_LICENSE"

install -m644 \
  "$xz_dir/LICENSE" \
  "$CONTENTS/Resources/legal/licenses/github.com_ulikunitz_xz_LICENSE"

install -m644 \
  "$(go env GOROOT)/LICENSE" \
  "$CONTENTS/Resources/legal/licenses/golang_LICENSE"

#
# Bundle libwim.
#

DYLIB="$CONTENTS/Frameworks/libwim.15.dylib"

install -m755 \
  "$LIBWIM_DYLIB" \
  "$DYLIB"

WIMLIB_COMPLIANCE_RECORD="$WIMLIB_COMPLIANCE_RECORD" \
  "$ROOT/packaging/legal/install-wimlib-record.sh" \
  "$CONTENTS/Resources/legal/wimlib-1.14.5"

#
# Validate architecture.
#

DYLIB_ARCHS=$(lipo -archs "$DYLIB")

if [[ " $DYLIB_ARCHS " != *" $PLIST_ARCH "* ]]; then
  echo \
    "libwim does not contain required architecture $PLIST_ARCH: $DYLIB_ARCHS" \
    >&2
  exit 65
fi

#
# Ensure a private bundle-relative install name.
#

install_name_tool \
  -id '@rpath/libwim.15.dylib' \
  "$DYLIB"

#
# Reject dependencies that could escape the application bundle.
#

while IFS= read -r dependency; do
  case "$dependency" in
    '@rpath/'* | '@loader_path/'* | /usr/lib/* | /System/Library/*)
      ;;
    *)
      echo "unsafe libwim dependency: $dependency" >&2
      exit 65
      ;;
  esac
done < <(
  otool -L "$DYLIB" |
    tail -n +2 |
    awk '{print $1}'
)

#
# The executable must know where its bundled dylib lives.
#

if ! otool -l "$CONTENTS/MacOS/GoFlasher" |
  grep -Fq '@executable_path/../Frameworks'; then

  install_name_tool \
    -add_rpath '@executable_path/../Frameworks' \
    "$CONTENTS/MacOS/GoFlasher"
fi

#
# Application metadata.
#

cat >"$CONTENTS/Info.plist" <<PLIST
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>CFBundleExecutable</key>
  <string>GoFlasher</string>

  <key>CFBundleIdentifier</key>
  <string>org.goflasher.usbwriter</string>

  <key>CFBundleName</key>
  <string>GoFlasher</string>

  <key>CFBundlePackageType</key>
  <string>APPL</string>

  <key>CFBundleShortVersionString</key>
  <string>$BUNDLE_VERSION</string>

  <key>CFBundleVersion</key>
  <string>$BUNDLE_VERSION</string>

  <key>CFBundleGetInfoString</key>
  <string>GoFlasher ${VERSION#v}</string>

  <key>LSMinimumSystemVersion</key>
  <string>13.0</string>

  <key>LSArchitecturePriority</key>
  <array>
    <string>$PLIST_ARCH</string>
  </array>
</dict>
</plist>
PLIST

plutil -lint "$CONTENTS/Info.plist"

#
# Validate the final application payload before creating the DMG.
#

WIMLIB_COMPLIANCE_RECORD="$WIMLIB_COMPLIANCE_RECORD" \
  "$ROOT/packaging/legal/verify-release.sh" \
  "$APP"

#
# Create unsigned DMG.
#

DMG_ROOT=$(mktemp -d "$OUTPUT/dmg-root.XXXXXX")

cleanup() {
  rm -rf "$DMG_ROOT"
}
trap cleanup EXIT INT TERM

cp -R "$APP" "$DMG_ROOT/"

DMG="$OUTPUT/GoFlasher-${VERSION}-${ARCH}.dmg"

rm -f "$DMG"

hdiutil create \
  -quiet \
  -volname GoFlasher \
  -srcfolder "$DMG_ROOT" \
  -format UDZO \
  "$DMG"

#
# Verify the DMG container itself is structurally readable.
#

hdiutil verify "$DMG"

echo "Created unsigned DMG:"
echo "$DMG"
