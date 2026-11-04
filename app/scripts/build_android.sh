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
urls="${1:-https://rely-vpn-go.com,https://net-w.51aihelp.com}"
case "$urls" in http://10.*|http://127.*|http://localhost*) echo "refusing a local API address for a release build: $urls" >&2; exit 1;; esac
# Official signing key (android/key.properties). Its certificate hash is
# baked in: a copy re-signed by anyone else refuses to run, and the server
# can refuse it too (后台 → 系统设置 → 安全防护 → 官方签名证书).
props=android/key.properties
if [ ! -f "$props" ]; then
  echo "android/key.properties missing: a release must be signed with the official key (restore it from your backup)" >&2
  exit 1
fi
prop() { sed -n "s/^$1=//p" "$props"; }
signer=$(keytool -list -v -keystore "android/$(prop storeFile)" -storepass "$(prop storePassword)" -alias "$(prop keyAlias)" \
  | sed -n 's/.*SHA256: *//p' | tr -d ':' | tr 'A-F' 'a-f')
if [ ${#signer} -ne 64 ]; then echo "could not read the signing certificate" >&2; exit 1; fi
echo "signing certificate SHA-256: $signer"
flutter build apk --release --split-per-abi --target-platform android-arm,android-arm64 \
  --obfuscate --split-debug-info=build/symbols --dart-define=API_BASE_URLS="$urls" --dart-define=APP_SIGNER="$signer"
ls -la build/app/outputs/flutter-apk/*-release.apk
