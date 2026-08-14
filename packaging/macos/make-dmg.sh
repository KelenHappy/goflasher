#!/bin/bash
set -euo pipefail
if [[ $# -ne 6 ]]; then
  echo "usage: $0 GUI HELPER VERSION ARCH SIGN_ID OUTPUT" >&2; exit 64
fi
GUI=$1 HELPER=$2 VERSION=$3 ARCH=$4 SIGN_ID=$5 OUTPUT=$6
case "$ARCH" in amd64|arm64) ;; *) echo "unsupported architecture: $ARCH" >&2; exit 64;; esac
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
<key>CFBundleShortVersionString</key><string>${VERSION#v}</string>
<key>CFBundleVersion</key><string>${VERSION#v}</string>
<key>LSMinimumSystemVersion</key><string>13.0</string>
<key>LSArchitecturePriority</key><array><string>$ARCH</string></array>
</dict></plist>
PLIST
codesign --force --timestamp --options runtime --sign "$SIGN_ID" "$CONTENTS/Library/HelperTools/org.goflasher.helper"
codesign --force --timestamp --options runtime --sign "$SIGN_ID" "$APP"
codesign --verify --deep --strict --verbose=2 "$APP"
mkdir -p "$OUTPUT/dmg-root"; rm -rf "$OUTPUT/dmg-root/GoFlasher.app"; cp -R "$APP" "$OUTPUT/dmg-root/"
hdiutil create -quiet -volname GoFlasher -srcfolder "$OUTPUT/dmg-root" -format UDZO "$OUTPUT/GoFlasher-${VERSION}-${ARCH}.dmg"
