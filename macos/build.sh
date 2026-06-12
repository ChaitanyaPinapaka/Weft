#!/usr/bin/env bash
# Build the Weft macOS app and assemble a runnable .app bundle.
# A bundle (with a stable bundle id) + an ad-hoc signature is what lets
# UNUserNotificationCenter work; running the bare binary skips notifications.
set -euo pipefail

cd "$(dirname "$0")"

CONFIG="${1:-release}"
APP="build/Weft.app"
# Fresh id: the old "app.tryweft.mac" is stuck in a macOS notification denial
# from its ad-hoc days, and that record (in a SIP-protected db) can't be reset.
# An unseen id, properly signed, starts at notDetermined → a clean auth prompt.
BUNDLE_ID="app.tryweft.weft"
VERSION="0.1.0"

echo "→ swift build -c $CONFIG"
swift build -c "$CONFIG"
BIN="$(swift build -c "$CONFIG" --show-bin-path)/Weft"

echo "→ assembling $APP"
rm -rf "$APP"
mkdir -p "$APP/Contents/MacOS" "$APP/Contents/Resources"
cp "$BIN" "$APP/Contents/MacOS/Weft"
cp Resources/AppIcon.icns "$APP/Contents/Resources/AppIcon.icns"
# Menu-bar glyph as a loose template image (the "Template" suffix makes AppKit
# tint it for light/dark + selection). NSImage(named:) pairs @1x/@2x by name.
cp Resources/weftTemplate.png "$APP/Contents/Resources/weftTemplate.png"
cp Resources/weftTemplate@2x.png "$APP/Contents/Resources/weftTemplate@2x.png"

# "Set up this Mac" payloads. The weft CLI ships inside the app so the panel
# can install it onto PATH without a separate download.
if [ ! -x ../bin/weft ]; then
  echo "error: ../bin/weft not found — run 'make build-ort' at the repo root first" >&2
  exit 1
fi
mkdir -p "$APP/Contents/Resources/bin"
cp ../bin/weft "$APP/Contents/Resources/bin/weft"
# Chrome extension, unpacked, minus dotfiles — setup copies it to App Support.
mkdir -p "$APP/Contents/Resources/extension"
rsync -a --exclude='.*' ../ext/ "$APP/Contents/Resources/extension/"
# Claude memory templates (CLAUDE.md marker block + slash commands).
cp -R Resources/claude "$APP/Contents/Resources/claude"

cat > "$APP/Contents/Info.plist" <<PLIST
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>CFBundleName</key>            <string>Weft</string>
  <key>CFBundleDisplayName</key>     <string>Weft</string>
  <key>CFBundleIdentifier</key>      <string>$BUNDLE_ID</string>
  <key>CFBundleExecutable</key>      <string>Weft</string>
  <key>CFBundleIconFile</key>        <string>AppIcon</string>
  <key>CFBundlePackageType</key>     <string>APPL</string>
  <key>CFBundleShortVersionString</key> <string>$VERSION</string>
  <key>CFBundleVersion</key>         <string>1</string>
  <key>LSMinimumSystemVersion</key>  <string>14.0</string>
  <key>NSPrincipalClass</key>        <string>NSApplication</string>
  <key>NSHighResolutionCapable</key> <true/>
  <key>LSUIElement</key>             <false/>
  <key>NSSupportsAutomaticTermination</key> <false/>
</dict>
</plist>
PLIST

# Prefer a real signing identity — macOS only grants notification authorization
# to properly-signed apps; ad-hoc is denied. Override with WEFT_SIGN_ID, else
# auto-pick the first usable identity, else fall back to ad-hoc.
SIGN_ID="${WEFT_SIGN_ID:-}"
if [ -z "$SIGN_ID" ]; then
  SIGN_ID=$(security find-identity -v -p codesigning 2>/dev/null \
    | grep -oE '"(Apple Development|Developer ID Application|Apple Distribution|Mac Developer)[^"]*"' \
    | head -1 | tr -d '"')
fi
if [ -n "$SIGN_ID" ]; then
  echo "→ codesign as: $SIGN_ID"
  codesign --force --deep --sign "$SIGN_ID" "$APP" && echo "  signed ✓"
else
  echo "→ no signing identity — ad-hoc (OS banner notifications will be blocked by macOS)"
  echo "  create one: Xcode ▸ Settings ▸ Accounts ▸ your team ▸ Manage Certificates ▸ + ▸ Apple Development"
  codesign --force --deep --sign - "$APP" >/dev/null 2>&1 || true
fi

echo "✓ $APP"
echo "  open $APP   # or: ./macos/run.sh"
