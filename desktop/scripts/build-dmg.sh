#!/usr/bin/env bash
# Builds FileSpaceDesktop in release mode, wraps it in a proper .app bundle
# (this package has no Xcode project, so there's no bundle infra otherwise),
# and packages that into a distributable .dmg with a drag-to-Applications
# layout.
#
# Usage: desktop/scripts/build-dmg.sh
# Output: desktop/dist/FileSpace.dmg
#
# Optional: drop an AppIcon.icns at desktop/Resources/AppIcon.icns before
# running to get a real app icon; otherwise the app uses the generic
# SwiftPM executable icon.

set -euo pipefail

DESKTOP_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$DESKTOP_DIR"

APP_NAME="FileSpace"
EXECUTABLE_NAME="FileSpaceDesktop"
BUILD_DIR="$DESKTOP_DIR/.build"
DIST_DIR="$DESKTOP_DIR/dist"
STAGING_DIR="$DIST_DIR/staging"
APP_BUNDLE="$STAGING_DIR/$APP_NAME.app"
DMG_PATH="$DIST_DIR/$APP_NAME.dmg"

echo "==> swift build -c release"
swift build -c release

RELEASE_BIN="$BUILD_DIR/release/$EXECUTABLE_NAME"
if [[ ! -x "$RELEASE_BIN" ]]; then
    echo "error: release binary not found at $RELEASE_BIN" >&2
    exit 1
fi

echo "==> assembling $APP_NAME.app"
rm -rf "$STAGING_DIR"
mkdir -p "$APP_BUNDLE/Contents/MacOS" "$APP_BUNDLE/Contents/Resources"

cp "$RELEASE_BIN" "$APP_BUNDLE/Contents/MacOS/$EXECUTABLE_NAME"
cp "$DESKTOP_DIR/Resources/Info.plist" "$APP_BUNDLE/Contents/Info.plist"
printf 'APPL????' > "$APP_BUNDLE/Contents/PkgInfo"

if [[ -f "$DESKTOP_DIR/Resources/AppIcon.icns" ]]; then
    cp "$DESKTOP_DIR/Resources/AppIcon.icns" "$APP_BUNDLE/Contents/Resources/AppIcon.icns"
else
    echo "note: no Resources/AppIcon.icns found, app will use the default icon"
fi

echo "==> ad-hoc code signing"
codesign --force --deep --sign - "$APP_BUNDLE"

echo "==> creating dmg"
ln -sf /Applications "$STAGING_DIR/Applications"
rm -f "$DMG_PATH"
hdiutil create -volname "$APP_NAME" -srcfolder "$STAGING_DIR" -ov -format UDZO "$DMG_PATH"

rm -rf "$STAGING_DIR"
echo "==> done: $DMG_PATH"
