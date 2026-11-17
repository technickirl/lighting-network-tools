#!/usr/bin/env bash
# Cross-compile the Go edition for macOS (arm64, x64), Windows (x64) and a Raspberry
# Pi (linux-armv6 for 32-bit Pi OS on any Pi, linux-arm64 for 64-bit) into dist/.
# Needs Go 1.25+ (brew install go). Run from anywhere:  ./scripts/build.sh
set -euo pipefail
cd "$(dirname "$0")/.."
export PATH="/opt/homebrew/bin:/usr/local/go/bin:$PATH"
export CGO_ENABLED=0
mkdir -p dist
# Version = latest v* tag (VERSION=x.y.z overrides); it goes into the file names
# and into the binary (main.version) so the UI and the file always agree.
# Mac binaries end in .command: with dots in the name Finder would otherwise read
# ".6-macos-arm64" as the extension and refuse to open them; .command opens in Terminal.
VERSION="${VERSION:-$(git describe --tags --abbrev=0 2>/dev/null | sed 's/^v//')}"
VERSION="${VERSION:-dev}"
LDFLAGS="-s -w -X main.version=$VERSION"
echo "darwin/arm64";  GOOS=darwin  GOARCH=arm64 go build -trimpath -ldflags "$LDFLAGS" -o "dist/LuminexCommission-$VERSION-macos-arm64.command" .
echo "darwin/amd64";  GOOS=darwin  GOARCH=amd64 go build -trimpath -ldflags "$LDFLAGS" -o "dist/LuminexCommission-$VERSION-macos-x64.command" .
echo "windows/amd64"; GOOS=windows GOARCH=amd64 go build -trimpath -ldflags "$LDFLAGS" -o "dist/LuminexCommission-$VERSION-win-x64.exe" .
echo "linux/armv6";   GOOS=linux GOARCH=arm GOARM=6 go build -trimpath -ldflags "$LDFLAGS" -o "dist/LuminexCommission-$VERSION-linux-armv6" .
echo "linux/arm64";   GOOS=linux GOARCH=arm64       go build -trimpath -ldflags "$LDFLAGS" -o "dist/LuminexCommission-$VERSION-linux-arm64" .
# Ad-hoc sign the Mac binaries so they run locally without "damaged" warnings.
if command -v codesign >/dev/null; then
  for f in dist/LuminexCommission-*-macos-*; do codesign --force --sign - "$f" 2>/dev/null || true; done
fi
echo; ls -la dist
echo; echo "Mac users: not notarised. The first time, use System Settings → Privacy & Security → Open Anyway, or xattr -d com.apple.quarantine <file>."
