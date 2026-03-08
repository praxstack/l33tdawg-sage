#!/bin/bash
# Build SAGE browser extension packages for Chrome Web Store and Firefox Add-ons.
# Usage: ./build.sh
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
SRC="$SCRIPT_DIR/chrome"
OUT="$SCRIPT_DIR/dist"

rm -rf "$OUT"
mkdir -p "$OUT/chrome" "$OUT/firefox"

echo "=== Generating PNG icons ==="
cd "$SRC/icons"
node generate-icons.js
cd "$SCRIPT_DIR"

echo "=== Building Chrome package ==="
# Copy all files except Firefox-specific ones
cp "$SRC/manifest.json" "$OUT/chrome/"
cp "$SRC"/*.js "$OUT/chrome/"
cp "$SRC"/*.html "$OUT/chrome/"
cp "$SRC"/*.css "$OUT/chrome/"
mkdir -p "$OUT/chrome/icons"
cp "$SRC/icons"/*.png "$OUT/chrome/icons/"

cd "$OUT/chrome"
zip -r "$OUT/sage-chrome-extension.zip" . -x "*.DS_Store"
echo "  -> $OUT/sage-chrome-extension.zip"

echo "=== Building Firefox package ==="
# Copy with Firefox manifest
cp "$SCRIPT_DIR/manifest.firefox.json" "$OUT/firefox/manifest.json"
cp "$SRC"/*.js "$OUT/firefox/"
cp "$SRC"/*.html "$OUT/firefox/"
cp "$SRC"/*.css "$OUT/firefox/"
mkdir -p "$OUT/firefox/icons"
cp "$SRC/icons"/*.png "$OUT/firefox/icons/"

cd "$OUT/firefox"
zip -r "$OUT/sage-firefox-extension.zip" . -x "*.DS_Store"
echo "  -> $OUT/sage-firefox-extension.zip"

echo ""
echo "=== Done ==="
echo "Chrome: $OUT/sage-chrome-extension.zip"
echo "Firefox: $OUT/sage-firefox-extension.zip"
echo ""
echo "Upload to:"
echo "  Chrome:  https://chrome.google.com/webstore/devconsole"
echo "  Firefox: https://addons.mozilla.org/developers/"
echo ""
echo "To sign the Firefox extension for self-distribution:"
echo "  cd $OUT/firefox"
echo "  web-ext sign --channel unlisted --api-key \$AMO_API_KEY --api-secret \$AMO_API_SECRET"
echo ""
echo "To sign for AMO listing:"
echo "  cd $OUT/firefox"
echo "  web-ext sign --channel listed --api-key \$AMO_API_KEY --api-secret \$AMO_API_SECRET"
echo ""
echo "Get API credentials at: https://addons.mozilla.org/developers/addon/api/key/"
