#!/usr/bin/env bash
# Builds the Android release APKs (64-bit and 32-bit) for upload in the admin
# panel (版本发布 → 发布 Android 版本, once per APK with the same build number).
#
#   scripts/build_android.sh [API_BASE_URLS]
#
# API_BASE_URLS: comma-separated API addresses baked into the app (default the
# production domain). Add a second domain here as a fallback if you have one:
# installed apps cannot learn new addresses later.
set -euo pipefail
cd "$(dirname "$0")/.."
urls="${1:-https://net-w.51aihelp.com}"
case "$urls" in http://10.*|http://127.*|http://localhost*) echo "refusing a local API address for a release build: $urls" >&2; exit 1;; esac
flutter build apk --release --split-per-abi --target-platform android-arm,android-arm64 \
  --obfuscate --split-debug-info=build/symbols --dart-define=API_BASE_URLS="$urls"
ls -la build/app/outputs/flutter-apk/*-release.apk
