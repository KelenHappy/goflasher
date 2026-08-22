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

# libwim is optional: without it the package remains raw-writer-only and the
# Windows-installer builder fails closed. The release workflow passes it only
# when the MACOS_WINDOWS_BUILDER_READY gate is approved.
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

if [[ -n "$LIBWIM_DYLIB" ]]; then
  [[ -f "$LIBWIM_DYLIB" ]] || {
    echo "libwim dylib not found: $LIBWIM_DYLIB" >&2
    exit 66
  }

  [[ -n "$WIMLIB_COMPLIANCE_RECORD" && -d "$WIMLIB_COMPLIANCE_RECORD" ]] || {
    echo "approved WIMLIB_COMPLIANCE_RECORD is required when bundling libwim" >&2
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
# Bundle libwim when provided.
#

DYLIB="$CONTENTS/Frameworks/libwim.15.dylib"

if [[ -n "$LIBWIM_DYLIB" ]]; then
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
else
  echo \
    "LIBWIM_DYLIB is empty; macOS Windows-installer building remains unavailable in this package" \
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
  if [[ -n "$LIBWIM_DYLIB" ]]; then
    codesign --force --timestamp --options runtime --sign "$SIGN_ID" "$DYLIB"
    codesign --verify --strict --verbose=2 "$DYLIB"
  fi

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
