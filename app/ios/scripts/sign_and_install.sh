#!/usr/bin/env bash
# Signs an unsigned `flutter build ios --release --no-codesign` build with a
# given distribution / ad-hoc profile and installs it on a connected iPhone.
#
#   ios/scripts/sign_and_install.sh <app.mobileprovision> [extension.mobileprovision] [device-udid]
#
# The extension (PacketTunnel.appex) needs its own profile for <bundle-id>.PacketTunnel.
# Without one it is removed from the app: the app runs, but "connect" cannot start the VPN.
set -euo pipefail
cd "$(dirname "$0")/../.."

APP_PROFILE=${1:?usage: sign_and_install.sh <app.mobileprovision> [extension.mobileprovision] [udid]}
EXT_PROFILE=${2:-}
DEVICE=${3:-$(xcrun devicectl list devices 2>/dev/null | awk '/connected/ && /physical/ {for(i=1;i<=NF;i++) if ($i ~ /^[0-9A-F]{8}-[0-9A-F]{16}$/) print $i; exit}')}
SRC=build/ios/iphoneos/Runner.app
WORK=build/ios/signed
APP=$WORK/Runner.app

[ -d "$SRC" ] || { echo "build first: flutter build ios --release --no-codesign"; exit 1; }
rm -rf "$WORK" && mkdir -p "$WORK" && cp -R "$SRC" "$APP"

decode() { security cms -D -i "$1"; }

# Signing identity: the certificate embedded in the profile.
identity_of() {
  decode "$1" > "$WORK/p.plist"
  /usr/libexec/PlistBuddy -c 'Print :DeveloperCertificates:0' "$WORK/p.plist" 2>/dev/null \
    | openssl x509 -inform DER -noout -fingerprint -sha1 | sed 's/.*=//; s/://g'
}

# Entitlements for a bundle = the profile's entitlements, with
# application-identifier set to TEAM.<bundle-id> and NE narrowed to packet tunnels.
entitlements_for() { # profile bundle-id out
  decode "$1" > "$WORK/p.plist"
  local team; team=$(/usr/libexec/PlistBuddy -c 'Print :TeamIdentifier:0' "$WORK/p.plist")
  /usr/libexec/PlistBuddy -x -c 'Print :Entitlements' "$WORK/p.plist" > "$3"
  /usr/libexec/PlistBuddy -c "Set :application-identifier $team.$2" "$3"
  /usr/libexec/PlistBuddy -c 'Delete :com.apple.developer.networking.networkextension' "$3" 2>/dev/null || true
  /usr/libexec/PlistBuddy -c 'Add :com.apple.developer.networking.networkextension array' "$3"
  /usr/libexec/PlistBuddy -c 'Add :com.apple.developer.networking.networkextension:0 string packet-tunnel-provider' "$3"
  # Unused capabilities the profile happens to allow; leaving them in is harmless
  # but noisy, so drop the ones the app doesn't use.
  for k in aps-environment com.apple.developer.applesignin inter-app-audio com.apple.developer.siri \
           com.apple.developer.healthkit com.apple.developer.homekit com.apple.developer.icloud-services \
           com.apple.developer.icloud-container-identifiers com.apple.developer.ubiquity-kvstore-identifier \
           com.apple.developer.associated-domains com.apple.developer.nfc.readersession.formats \
           com.apple.developer.pass-type-identifiers com.apple.developer.in-app-payments \
           com.apple.developer.healthkit.access com.apple.developer.ClassKit-environment \
           com.apple.developer.authentication-services.autofill-credential-provider \
           com.apple.developer.networking.multipath com.apple.developer.networking.HotspotConfiguration \
           com.apple.developer.networking.wifi-info com.apple.developer.networking.vpn.api \
           com.apple.developer.usernotifications.time-sensitive com.apple.developer.default-data-protection \
           com.apple.developer.user-fonts com.apple.developer.kernel.increased-memory-limit \
           com.apple.developer.kernel.extended-virtual-addressing com.apple.external-accessory.wireless-configuration \
           com.apple.developer.associated-appclip-app-identifiers com.apple.developer.parent-application-identifiers \
           com.apple.developer.on-demand-install-capable com.apple.developer.group-session \
           com.apple.developer.coremedia.hls.low-latency com.apple.developer.devicecheck.appattest-environment \
           com.apple.developer.family-controls com.apple.developer.shared-with-you \
           com.apple.developer.weatherkit com.apple.developer.matter.allow-setup-payload \
           com.apple.developer.sustained-execution com.apple.developer.fileprovider.testing-mode \
           com.apple.security.application-groups com.apple.developer.team-identifier.unused; do
    /usr/libexec/PlistBuddy -c "Delete :$k" "$3" 2>/dev/null || true
  done
}

ID=$(identity_of "$APP_PROFILE")
BUNDLE_ID=$(/usr/libexec/PlistBuddy -c 'Print :CFBundleIdentifier' "$APP/Info.plist")
echo "bundle id: $BUNDLE_ID   identity: $ID   device: ${DEVICE:-none}"

# --- extension ---
EXT=$APP/PlugIns/PacketTunnel.appex
if [ -d "$EXT" ]; then
  if [ -n "$EXT_PROFILE" ]; then
    cp "$EXT_PROFILE" "$EXT/embedded.mobileprovision"
    entitlements_for "$EXT_PROFILE" "$BUNDLE_ID.PacketTunnel" "$WORK/ext.entitlements"
    codesign -f -s "$ID" --entitlements "$WORK/ext.entitlements" --generate-entitlement-der "$EXT"
  else
    echo "!! no extension profile: removing PacketTunnel.appex (VPN connect will not work)"
    rm -rf "$EXT"
  fi
fi

# --- embedded frameworks ---
for f in "$APP"/Frameworks/*; do codesign -f -s "$ID" --generate-entitlement-der "$f"; done

# --- app ---
cp "$APP_PROFILE" "$APP/embedded.mobileprovision"
entitlements_for "$APP_PROFILE" "$BUNDLE_ID" "$WORK/app.entitlements"
codesign -f -s "$ID" --entitlements "$WORK/app.entitlements" --generate-entitlement-der "$APP"
codesign --verify --deep --strict "$APP" && echo "signature OK"

if [ -n "${DEVICE:-}" ]; then
  xcrun devicectl device install app --device "$DEVICE" "$APP"
  xcrun devicectl device process launch --device "$DEVICE" "$BUNDLE_ID" || true
fi
