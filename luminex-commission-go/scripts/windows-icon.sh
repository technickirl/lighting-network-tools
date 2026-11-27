#!/usr/bin/env bash
# Make rsrc_windows_amd64.syso beside main.go: the Windows icon (icon/windows.png) and
# the version and name Explorer shows under Properties. `go build` links any .syso it
# finds for its target, so run this before building the .exe. The file is not kept in
# git. Uses go-winres at a fixed version (fetched by `go run` the first time).
#   VERSION=x.y.z ./scripts/windows-icon.sh
set -euo pipefail
cd "$(dirname "$0")/.."
export PATH="/opt/homebrew/bin:/usr/local/go/bin:$PATH"
VERSION="${VERSION:-$(git describe --tags --abbrev=0 2>/dev/null | sed 's/^v//' || true)}"
ARGS=(--arch amd64 --out rsrc --manifest cli --icon icon/windows.png
      --product-name "Luminex Commission" --file-description "Luminex Commission")
# Windows wants numbers here; a build with no tag goes without.
case "$VERSION" in [0-9]*) ARGS+=(--product-version "$VERSION" --file-version "$VERSION") ;; esac
env -u GOOS -u GOARCH -u CGO_ENABLED go run github.com/tc-hib/go-winres@v0.3.3 simply "${ARGS[@]}"
