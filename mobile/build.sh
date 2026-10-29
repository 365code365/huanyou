#!/usr/bin/env bash
# Builds the vpncore library for the Flutter app.
#   ./build.sh android   -> app/android/app/libs/vpncore.aar
#   ./build.sh ios       -> app/ios/Frameworks/Vpncore.xcframework
#   ./build.sh           -> both
# Requires: Go >= 1.26 (GOTOOLCHAIN downloads it), Android NDK (ANDROID_HOME), Xcode for iOS.
set -euo pipefail
cd "$(dirname "$0")"

export GOTOOLCHAIN="${GOTOOLCHAIN:-go1.26.1}"
export PATH="$(go env GOPATH)/bin:$PATH"
APP="../app"
LDFLAGS="-s -w -buildid="

if ! command -v gomobile >/dev/null; then
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
  gomobile bind -v -target=android/arm64,android/arm,android/amd64 -androidapi 24 \
    -trimpath -ldflags="$LDFLAGS" -javapkg=com.vpnplatform \
    -o "$APP/android/app/libs/vpncore.aar" ./vpncore
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

case "${1:-all}" in
  android) build_android ;;
  ios) build_ios ;;
  all) build_android; build_ios ;;
  *) echo "usage: $0 [android|ios|all]"; exit 1 ;;
esac
