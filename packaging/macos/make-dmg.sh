#!/bin/bash
set -euo pipefail
if [[ $# -ne 6 ]]; then
  echo "usage: $0 GUI HELPER VERSION ARCH SIGN_ID OUTPUT" >&2; exit 64
fi
GUI=$1 HELPER=$2 VERSION=$3 ARCH=$4 SIGN_ID=$5 OUTPUT=$6
case "$ARCH" in
  amd64) PLIST_ARCH=x86_64 ;;
  arm64) PLIST_ARCH=arm64 ;;
  *) echo "unsupported architecture: $ARCH" >&2; exit 64 ;;
esac
[[ -f "$GUI" ]] || { echo "GUI executable not found: $GUI" >&2; exit 66; }
[[ -f "$HELPER" ]] || { echo "helper executable not found: $HELPER" >&2; exit 66; }

# Apple bundle versions must be numeric even when the artifact version has a
# prerelease suffix (for example, v0.1.0-alpha).
BUNDLE_VERSION=${VERSION#v}
BUNDLE_VERSION=${BUNDLE_VERSION%%-*}
if [[ ! "$BUNDLE_VERSION" =~ ^[0-9]+([.][0-9]+){0,2}$ ]]; then
  echo "version does not contain a valid numeric bundle version: $VERSION" >&2
  exit 64
fi
APP="$OUTPUT/GoFlasher.app"
CONTENTS="$APP/Contents"
rm -rf "$APP"; mkdir -p "$CONTENTS/MacOS" "$CONTENTS/Library/HelperTools"
install -m 755 "$GUI" "$CONTENTS/MacOS/GoFlasher"
install -m 755 "$HELPER" "$CONTENTS/Library/HelperTools/org.goflasher.helper"
cat > "$CONTENTS/Info.plist" <<PLIST
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
<key>CFBundleExecutable</key><string>GoFlasher</string>
<key>CFBundleIdentifier</key><string>org.goflasher.usbwriter</string>
<key>CFBundleName</key><string>GoFlasher</string>
<key>CFBundlePackageType</key><string>APPL</string>
<key>CFBundleShortVersionString</key><string>$BUNDLE_VERSION</string>
<key>CFBundleVersion</key><string>$BUNDLE_VERSION</string>
<key>CFBundleGetInfoString</key><string>GoFlasher ${VERSION#v}</string>
<key>LSMinimumSystemVersion</key><string>13.0</string>
<key>LSArchitecturePriority</key><array><string>$PLIST_ARCH</string></array>
</dict></plist>
PLIST
plutil -lint "$CONTENTS/Info.plist"
if [[ -n "$SIGN_ID" ]]; then
  codesign --force --timestamp --options runtime --sign "$SIGN_ID" "$CONTENTS/Library/HelperTools/org.goflasher.helper"
  codesign --force --timestamp --options runtime --sign "$SIGN_ID" "$APP"
  codesign --verify --deep --strict --verbose=2 "$APP"
else
  echo "SIGN_ID is empty; creating an unsigned hardware-test DMG" >&2
fi
rm -rf "$OUTPUT/dmg-root"
mkdir -p "$OUTPUT/dmg-root"
cp -R "$APP" "$OUTPUT/dmg-root/"
rm -f "$OUTPUT/GoFlasher-${VERSION}-${ARCH}.dmg"
hdiutil create -quiet -volname GoFlasher -srcfolder "$OUTPUT/dmg-root" -format UDZO "$OUTPUT/GoFlasher-${VERSION}-${ARCH}.dmg"
