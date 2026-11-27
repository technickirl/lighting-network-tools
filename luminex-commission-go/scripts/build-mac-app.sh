#!/usr/bin/env bash
# Build "Luminex Commission.app": one app for Apple silicon and Intel Macs, with its
# icon and a menu bar icon while it runs (app_darwin.go), zipped as
# dist/LuminexCommission-<version>-macos-app.zip. Needs a Mac with the Xcode command
# line tools, because the menu bar code is compiled with cgo against Cocoa.
# Run from anywhere:  ./scripts/build-mac-app.sh     (VERSION=x.y.z overrides the tag)
set -euo pipefail
cd "$(dirname "$0")/.."
export PATH="/opt/homebrew/bin:/usr/local/go/bin:$PATH"
if [ "$(uname)" != "Darwin" ]; then echo "The Mac app can only be built on a Mac."; exit 1; fi
VERSION="${VERSION:-$(git describe --tags --abbrev=0 2>/dev/null | sed 's/^v//')}"
VERSION="${VERSION:-dev}"
LDFLAGS="-s -w -X main.version=$VERSION"
NAME="Luminex Commission"
APP="dist/$NAME.app"
ZIP="dist/LuminexCommission-$VERSION-macos-app.zip"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

# The oldest macOS this Go release runs on, read from a plain Go binary, so the
# cgo parts and the Info.plist say the same as the Go parts.
CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build -o "$TMP/probe" .
MINOS="$(otool -l "$TMP/probe" | awk '/LC_BUILD_VERSION/{f=1} f&&/minos/{print $2; exit}')"
MINOS="${MINOS:-12.0}"
export MACOSX_DEPLOYMENT_TARGET="$MINOS"

for arch in arm64 amd64; do
  echo "darwin/$arch (cgo)"
  CGO_ENABLED=1 GOOS=darwin GOARCH=$arch go build -trimpath -ldflags "$LDFLAGS" -o "$TMP/$arch" .
done

rm -rf "$APP" "$ZIP"
mkdir -p "$APP/Contents/MacOS" "$APP/Contents/Resources"
lipo -create -output "$APP/Contents/MacOS/LuminexCommission" "$TMP/arm64" "$TMP/amd64"
cp icon/AppIcon.icns "$APP/Contents/Resources/AppIcon.icns"
# Info.plist wants numbers in CFBundleVersion; a build with no tag gets 0.
case "$VERSION" in [0-9]*) BUNDLE_VERSION="$VERSION" ;; *) BUNDLE_VERSION="0" ;; esac
cat > "$APP/Contents/Info.plist" <<EOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>CFBundleName</key><string>$NAME</string>
  <key>CFBundleDisplayName</key><string>$NAME</string>
  <key>CFBundleIdentifier</key><string>io.github.technickirl.luminex-commission</string>
  <key>CFBundleExecutable</key><string>LuminexCommission</string>
  <key>CFBundleIconFile</key><string>AppIcon</string>
  <key>CFBundlePackageType</key><string>APPL</string>
  <key>CFBundleInfoDictionaryVersion</key><string>6.0</string>
  <key>CFBundleShortVersionString</key><string>$VERSION</string>
  <key>CFBundleVersion</key><string>$BUNDLE_VERSION</string>
  <key>LSMinimumSystemVersion</key><string>$MINOS</string>
  <key>LSUIElement</key><true/>
  <key>NSHighResolutionCapable</key><true/>
  <key>NSLocalNetworkUsageDescription</key><string>Luminex Commission finds and configures Luminex nodes, switches and RDM fixtures on your network.</string>
  <key>NSBonjourServices</key><array><string>_luminex._tcp</string></array>
</dict>
</plist>
EOF
plutil -lint "$APP/Contents/Info.plist" >/dev/null
# Ad-hoc signed, not notarised: the first run needs Open Anyway or xattr.
codesign --force --sign - "$APP"
# ditto keeps the executable flag and the signature inside the zip.
ditto -c -k --keepParent "$APP" "$ZIP"
echo; lipo -info "$APP/Contents/MacOS/LuminexCommission"; ls -la "$ZIP"
