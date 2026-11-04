#!/usr/bin/env bash
# Builds the iOS app for App Store / TestFlight (an .ipa, obfuscated Dart).
#
#   scripts/build_ios.sh [API_BASE_URLS]
#
# Signing uses the team / profiles set in Xcode (Runner + PacketTunnel).
# Dart symbols for crash stacks go to build/symbols-ios (keep them per release).
set -euo pipefail
cd "$(dirname "$0")/.."
urls="${1:-https://rely-vpn-go.com,https://net-w.51aihelp.com}"
case "$urls" in http://*) echo "refusing a plain-http API address for a release build: $urls" >&2; exit 1;; esac
flutter build ipa --release --obfuscate --split-debug-info=build/symbols-ios --dart-define=API_BASE_URLS="$urls"
ls -la build/ios/ipa/*.ipa
