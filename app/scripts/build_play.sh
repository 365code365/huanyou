#!/usr/bin/env bash
# Builds the Google Play release bundle (AAB) for upload to Play Console.
#
#   scripts/build_play.sh [API_BASE_URLS]
#
# This is the `play` distribution channel (--dart-define=CHANNEL=play): it uses
# Google Play Billing only, has no in-app self-update, no external download
# links or recommended apps, and shows the VpnService disclosure before
# connecting — everything Google Play policy requires. The官网 APK (the `site`
# channel) is built with scripts/build_android.sh instead.
#
# Google Play upload / signing notes:
#   - Play requires an app bundle (.aab), not an APK.
#   - Use Play App Signing. To keep the installed app's signing certificate
#     (and therefore ANDROID_ID, the no-login device identity) the same as the
#     官网 APK, import the existing android/keystore key as the Play *app
#     signing key* when you enable Play App Signing. This AAB is signed with
#     that key as the upload key too.
#   - In Play Console submit the VpnService declaration (VPN is the core
#     function), the prominent-disclosure video, and the Data safety form
#     (device id, usage analytics, crash logs, connection IP).
set -euo pipefail
cd "$(dirname "$0")/.."
urls="${1:-https://rely-vpn-go.com,https://net-w.51aihelp.com}"
case "$urls" in http://10.*|http://127.*|http://localhost*) echo "refusing a local API address for a release build: $urls" >&2; exit 1;; esac

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

# versionCode = 3000 + build number. The官网 arm64 APK is 2000 + build
# (--split-per-abi), so a phone that sideloaded the APK can be updated by Play
# (same applicationId and signing key). The app strips the prefix (build % 1000).
build=$(sed -n 's/^version: *[^+]*+\([0-9]*\).*/\1/p' pubspec.yaml)
if [ -z "$build" ] || [ "$build" -ge 1000 ]; then echo "pubspec build number must be 1..999: '$build'" >&2; exit 1; fi
flutter build appbundle --release --build-number=$((3000 + build)) \
  --obfuscate --split-debug-info=build/symbols \
  --dart-define=API_BASE_URLS="$urls" \
  --dart-define=CHANNEL=play \
  --dart-define=APP_SIGNER="$signer"
ls -la build/app/outputs/bundle/release/*.aab
