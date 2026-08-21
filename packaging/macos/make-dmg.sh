#!/bin/bash
set -euo pipefail
if [[ $# -ne 6 ]]; then
  echo "usage: $0 GUI HELPER VERSION ARCH SIGN_ID OUTPUT" >&2; exit 64
fi
GUI=$1 HELPER=$2 VERSION=$3 ARCH=$4 SIGN_ID=$5 OUTPUT=$6
ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
LIBWIM_DYLIB=${LIBWIM_DYLIB:-}
case "$ARCH" in
  amd64) PLIST_ARCH=x86_64 ;;
  arm64) PLIST_ARCH=arm64 ;;
  *) echo "unsupported architecture: $ARCH" >&2; exit 64 ;;
esac
[[ -f "$GUI" ]] || { echo "GUI executable not found: $GUI" >&2; exit 66; }
[[ -f "$HELPER" ]] || { echo "helper executable not found: $HELPER" >&2; exit 66; }
if [[ -n "$LIBWIM_DYLIB" && ! -f "$LIBWIM_DYLIB" ]]; then
  echo "libwim dylib not found: $LIBWIM_DYLIB" >&2; exit 66
fi

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
rm -rf "$APP"; mkdir -p "$CONTENTS/MacOS" "$CONTENTS/Frameworks" "$CONTENTS/Library/HelperTools" "$CONTENTS/Resources/legal/licenses"
install -m 755 "$GUI" "$CONTENTS/MacOS/GoFlasher"
install -m 755 "$HELPER" "$CONTENTS/Library/HelperTools/org.goflasher.helper"
install -m644 "$ROOT/docs/legal/THIRD_PARTY_NOTICES.md" "$CONTENTS/Resources/legal/"
install -m644 "$ROOT/docs/legal/THIRD_PARTY_NOTICES.zh-TW.md" "$CONTENTS/Resources/legal/"
purego_dir=$(cd "$ROOT" && go list -m -f '{{.Dir}}' github.com/ebitengine/purego)
xz_dir=$(cd "$ROOT" && go list -m -f '{{.Dir}}' github.com/ulikunitz/xz)
install -m644 "$purego_dir/LICENSE" "$CONTENTS/Resources/legal/licenses/github.com_ebitengine_purego_LICENSE"
install -m644 "$xz_dir/LICENSE" "$CONTENTS/Resources/legal/licenses/github.com_ulikunitz_xz_LICENSE"
install -m644 "$(go env GOROOT)/LICENSE" "$CONTENTS/Resources/legal/licenses/golang_LICENSE"
if [[ -n "$LIBWIM_DYLIB" ]]; then
  DYLIB="$CONTENTS/Frameworks/libwim.15.dylib"
  install -m 755 "$LIBWIM_DYLIB" "$DYLIB"
  WIMLIB_COMPLIANCE_RECORD=${WIMLIB_COMPLIANCE_RECORD:-} "$ROOT/packaging/legal/install-wimlib-record.sh" "$CONTENTS/Resources/legal/wimlib-1.14.5"
  DYLIB_ARCHS=$(lipo -archs "$DYLIB")
  if [[ " $DYLIB_ARCHS " != *" $PLIST_ARCH "* ]]; then
    echo "libwim does not contain required architecture $PLIST_ARCH: $DYLIB_ARCHS" >&2
    exit 65
  fi
  install_name_tool -id '@rpath/libwim.15.dylib' "$DYLIB"
  # The bundled dylib may only depend on itself, rpath-relative peers, or OS
  # libraries. Packaging fails rather than leaving an absolute Homebrew/local
  # dependency that dyld could resolve outside the signed bundle.
  while IFS= read -r dependency; do
    case "$dependency" in
      "$DYLIB"|'@rpath/'*|'@loader_path/'*|/usr/lib/*|/System/Library/*) ;;
      *) echo "unsafe libwim dependency: $dependency" >&2; exit 65 ;;
    esac
  done < <(otool -L "$DYLIB" | tail -n +2 | awk '{print $1}')
  if ! otool -l "$CONTENTS/MacOS/GoFlasher" | grep -Fq '@executable_path/../Frameworks'; then
    install_name_tool -add_rpath '@executable_path/../Frameworks' "$CONTENTS/MacOS/GoFlasher"
  fi
else
  echo "LIBWIM_DYLIB is empty; macOS Windows-installer building remains unavailable in this package" >&2
fi
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
WIMLIB_COMPLIANCE_RECORD=${WIMLIB_COMPLIANCE_RECORD:-} "$ROOT/packaging/legal/verify-release.sh" "$APP"
if [[ -n "$SIGN_ID" ]]; then
  test -n "${APPLE_ID:-}" && test -n "${APPLE_TEAM_ID:-}" && test -n "${APPLE_APP_PASSWORD:-}"
  # Nested code must be sealed from the inside out. Do not use --deep to sign:
  # each code object receives its own Hardened Runtime signature.
  if [[ -n "$LIBWIM_DYLIB" ]]; then
    codesign --force --timestamp --options runtime --sign "$SIGN_ID" "$DYLIB"
    codesign --verify --strict --verbose=2 "$DYLIB"
  fi
  codesign --force --timestamp --options runtime --sign "$SIGN_ID" "$CONTENTS/Library/HelperTools/org.goflasher.helper"
  codesign --verify --strict --verbose=2 "$CONTENTS/Library/HelperTools/org.goflasher.helper"
  codesign --force --timestamp --options runtime --sign "$SIGN_ID" "$APP"
  codesign --verify --deep --strict --verbose=2 "$APP"
else
  echo "SIGN_ID is empty; creating an unsigned DMG" >&2
fi
rm -rf "$OUTPUT/dmg-root"
mkdir -p "$OUTPUT/dmg-root"
cp -R "$APP" "$OUTPUT/dmg-root/"
rm -f "$OUTPUT/GoFlasher-${VERSION}-${ARCH}.dmg"
hdiutil create -quiet -volname GoFlasher -srcfolder "$OUTPUT/dmg-root" -format UDZO "$OUTPUT/GoFlasher-${VERSION}-${ARCH}.dmg"
DMG="$OUTPUT/GoFlasher-${VERSION}-${ARCH}.dmg"
if [[ -n "$SIGN_ID" ]]; then
  codesign --force --timestamp --sign "$SIGN_ID" "$DMG"
  codesign --verify --strict --verbose=2 "$DMG"
  xcrun notarytool submit "$DMG" --apple-id "$APPLE_ID" --team-id "$APPLE_TEAM_ID" --password "$APPLE_APP_PASSWORD" --wait
  xcrun stapler staple "$DMG"
  xcrun stapler validate "$DMG"
  spctl --assess --type open --context context:primary-signature --verbose=2 "$DMG"
fi
