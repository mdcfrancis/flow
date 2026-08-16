#!/bin/bash
# Builds the flow menu-bar helper into a minimal .app bundle.
#
#   ./menubar/build.sh          # build
#   open menubar/build/FlowMenuBar.app
#
# A bundle (rather than a bare executable) is required: NSStatusItem needs a real
# app bundle with LSUIElement set, or macOS gives the process a Dock tile.
set -euo pipefail

cd "$(dirname "$0")"
APP="build/FlowMenuBar.app"

rm -rf "$APP"
mkdir -p "$APP/Contents/MacOS"

swiftc -O FlowMenuBar.swift -o "$APP/Contents/MacOS/FlowMenuBar"

cat > "$APP/Contents/Info.plist" <<'PLIST'
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>CFBundleName</key><string>FlowMenuBar</string>
  <key>CFBundleDisplayName</key><string>flow</string>
  <key>CFBundleIdentifier</key><string>com.mdcfrancis.flow.menubar</string>
  <key>CFBundleVersion</key><string>1.0</string>
  <key>CFBundleShortVersionString</key><string>1.0</string>
  <key>CFBundlePackageType</key><string>APPL</string>
  <key>CFBundleExecutable</key><string>FlowMenuBar</string>
  <!-- Menu-bar only: no Dock tile, no app switcher entry. -->
  <key>LSUIElement</key><true/>
  <key>LSMinimumSystemVersion</key><string>13.0</string>
</dict>
</plist>
PLIST

echo "built $APP"
echo "run:  open $(cd "$(dirname "$APP")" && pwd)/$(basename "$APP")"
