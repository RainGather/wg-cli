#!/bin/sh
set -eu
cd "$(dirname "$0")/.."
mkdir -p dist
# Pure Go, no NDK, libc, WireGuard tools or shared libraries at runtime.
CGO_ENABLED=0 GOOS=android GOARCH=arm64 go build -trimpath -ldflags='-s -w' -o dist/wg-cli-android-arm64 .
cp LICENSE THIRD_PARTY_NOTICES dist/
(cd dist && shasum -a 256 wg-cli-android-arm64) > dist/SHA256SUMS
