#!/bin/bash
set -euo pipefail

if [[ $# -ne 6 ]]; then
  echo "usage: $0 GUI HELPER VERSION ARCH SIGN_ID OUTPUT" >&2
  exit 64
fi

GUI=$1
HELPER=$2
VERSION=$3
ARCH=$4
SIGN_ID=$5
OUTPUT=$6

ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)

# wimlib is optional: a GUI built without the wimlib tag is raw-writer-only and
# the Windows-installer builder fails closed. The release workflow links it in
# only when the MACOS_WINDOWS_BUILDER_READY gate is approved.
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

LINKED_WIMLIB=
if "$ROOT/packaging/legal/links-wimlib.sh" "$GUI"; then
  LINKED_WIMLIB=1

  [[ -n "$WIMLIB_COMPLIANCE_RECORD" && -d "$WIMLIB_COMPLIANCE_RECORD" ]] || {
    echo "approved WIMLIB_COMPLIANCE_RECORD is required when wimlib is linked in" >&2
    exit 65
  }
fi

if [[ -n "$SIGN_ID" ]]; then
  for required in APPLE_ID APPLE_TEAM_ID APPLE_APP_PASSWORD; do
    [[ -n "${!required:-}" ]] || {
      echo "$required is required for signed, notarized packaging" >&2
      exit 65
    }
  done
fi

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
  "$CONTENTS/Library/HelperTools" \
  "$CONTENTS/Resources/legal"

install -m755 \
  "$GUI" \
  "$CONTENTS/MacOS/GoFlasher"

install -m755 \
  "$HELPER" \
  "$CONTENTS/Library/HelperTools/org.goflasher.helper"

# Third-party notices and dependency licenses are embedded in the binary by
# internal/legal and shown under Settings. The project license stays a file so
# the bundle carries its own terms.
install -m644 \
  "$ROOT/LICENSE" \
  "$CONTENTS/Resources/legal/LICENSE"

#
# Ship wimlib's notices and compliance record when it is linked in.
#

if [[ -n "$LINKED_WIMLIB" ]]; then
  WIMLIB_COMPLIANCE_RECORD="$WIMLIB_COMPLIANCE_RECORD" \
    "$ROOT/packaging/legal/install-wimlib-record.sh" \
    "$CONTENTS/Resources/legal/wimlib-1.14.5"
else
  echo \
    "GUI is built without wimlib; macOS Windows-installer building remains unavailable in this package" \
    >&2
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
# Validate the final application payload before signing and creating the DMG.
#

WIMLIB_COMPLIANCE_RECORD="$WIMLIB_COMPLIANCE_RECORD" \
  "$ROOT/packaging/legal/verify-release.sh" \
  "$APP"

#
# Sign nested code from the inside out. Do not use --deep: each code object
# receives its own Hardened Runtime signature.
#

if [[ -n "$SIGN_ID" ]]; then
  codesign --force --timestamp --options runtime --sign "$SIGN_ID" \
    "$CONTENTS/Library/HelperTools/org.goflasher.helper"
  codesign --verify --strict --verbose=2 \
    "$CONTENTS/Library/HelperTools/org.goflasher.helper"

  codesign --force --timestamp --options runtime --sign "$SIGN_ID" "$APP"
  codesign --verify --deep --strict --verbose=2 "$APP"
else
  echo "SIGN_ID is empty; creating an unsigned DMG" >&2
fi

#
# Create DMG.
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

if [[ -z "$SIGN_ID" ]]; then
  echo "Created unsigned DMG:"
  echo "$DMG"
  exit 0
fi

#
# Sign, notarize, staple, and Gatekeeper-verify the DMG.
#

codesign --force --timestamp --sign "$SIGN_ID" "$DMG"
codesign --verify --strict --verbose=2 "$DMG"

xcrun notarytool submit "$DMG" \
  --apple-id "$APPLE_ID" \
  --team-id "$APPLE_TEAM_ID" \
  --password "$APPLE_APP_PASSWORD" \
  --wait

xcrun stapler staple "$DMG"
xcrun stapler validate "$DMG"

spctl --assess --type open --context context:primary-signature --verbose=2 "$DMG"

echo "Created signed and notarized DMG:"
echo "$DMG"
