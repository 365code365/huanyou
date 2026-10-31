#!/usr/bin/env bash
# Builds the vpncore library for the Flutter app.
#   ./build.sh android   -> app/android/app/libs/vpncore.aar
#   ./build.sh ios       -> app/ios/Frameworks/Vpncore.xcframework
#   ./build.sh windows   -> app/windows/core/huanyou-core.exe (pure Go, builds on any OS)
#   ./build.sh macos     -> app/macos/core/huanyou-core (universal arm64 + x86_64, needs a Mac)
#   ./build.sh           -> android + ios
# Requires: Go >= 1.26 (GOTOOLCHAIN downloads it), Android NDK (ANDROID_HOME), Xcode for iOS.
set -euo pipefail
cd "$(dirname "$0")"

export GOTOOLCHAIN="${GOTOOLCHAIN:-go1.26.1}"
export PATH="$(go env GOPATH)/bin:$PATH"
APP="../app"
LDFLAGS="-s -w -buildid="

if [ "${1:-all}" != windows ] && [ "${1:-all}" != macos ] && ! command -v gomobile >/dev/null; then
  go install golang.org/x/mobile/cmd/gomobile@latest golang.org/x/mobile/cmd/gobind@latest
  gomobile init
fi

build_android() {
  : "${ANDROID_HOME:=$HOME/Library/Android/sdk}"
  export ANDROID_HOME
  if [ -z "${ANDROID_NDK_HOME:-}" ]; then
    ANDROID_NDK_HOME="$(ls -d "$ANDROID_HOME"/ndk/* | sort -V | tail -1)"
    export ANDROID_NDK_HOME
  fi
  mkdir -p "$APP/android/app/libs"
  gomobile bind -v -target=android/arm64,android/arm -androidapi 24 \
    -trimpath -ldflags="$LDFLAGS" -javapkg=com.vpnplatform \
    -o "$APP/android/app/libs/vpncore.aar" ./vpncore
  # gomobile's consumer rule keeps all of com.vpnplatform.**, which would
  # also stop R8 from obfuscating the app's own classes (com.vpnplatform.
  # vpn_app). The app keeps the core's packages itself (proguard-rules.pro).
  zip -q -d "$APP/android/app/libs/vpncore.aar" proguard.txt
  echo "-> $APP/android/app/libs/vpncore.aar"
}

build_ios() {
  mkdir -p "$APP/ios/Frameworks"
  rm -rf "$APP/ios/Frameworks/Vpncore.xcframework"
  gomobile bind -v -target=ios,iossimulator -iosversion 15.0 \
    -trimpath -ldflags="$LDFLAGS" \
    -o "$APP/ios/Frameworks/Vpncore.xcframework" ./vpncore
  echo "-> $APP/ios/Frameworks/Vpncore.xcframework"
}

# Windows desktop core: a GUI-subsystem exe (no console window) the app runs
# as a child process; see cmd/huanyou-core.
build_windows() {
  mkdir -p "$APP/windows/core"
  GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -trimpath \
    -ldflags="$LDFLAGS -H=windowsgui" \
    -o "$APP/windows/core/huanyou-core.exe" ./cmd/huanyou-core
  echo "-> $APP/windows/core/huanyou-core.exe"
}

# macOS desktop core, same program as Windows. The Xcode build phase added by
# app/macos/scripts/add_core_phase.rb copies it into the .app and signs it.
build_macos() {
  mkdir -p "$APP/macos/core"
  local tmp; tmp="$(mktemp -d)"
  for arch in arm64 amd64; do
    GOOS=darwin GOARCH=$arch CGO_ENABLED=1 MACOSX_DEPLOYMENT_TARGET=12.0 \
      CGO_CFLAGS="-mmacosx-version-min=12.0" CGO_LDFLAGS="-mmacosx-version-min=12.0" \
      go build -trimpath -ldflags="$LDFLAGS" -o "$tmp/core-$arch" ./cmd/huanyou-core
  done
  lipo -create -output "$APP/macos/core/huanyou-core" "$tmp/core-arm64" "$tmp/core-amd64"
  rm -rf "$tmp"
  echo "-> $APP/macos/core/huanyou-core"
}

case "${1:-all}" in
  android) build_android ;;
  ios) build_ios ;;
  windows) build_windows ;;
  macos) build_macos ;;
  all) build_android; build_ios ;;
  *) echo "usage: $0 [android|ios|windows|macos|all]"; exit 1 ;;
esac
