#!/usr/bin/env bash
# Builds the macOS release (universal: Apple silicon + Intel) as a DMG
# installer: opening it shows 环游.app next to an Applications shortcut to
# drag it onto. Upload the DMG in the admin panel (版本发布 → 发布 macOS 版本).
# Run from app/ on a Mac:
#
#   scripts/build_macos.sh [API_BASE_URLS]
#
# Signing (recommended, otherwise users must right-click → 打开 the first time):
#   SIGN_IDENTITY="Developer ID Application: Name (TEAMID)"  default: ad-hoc
#   NOTARY_PROFILE=<keychain profile>   also notarizes and staples; create the
#     profile once with: xcrun notarytool store-credentials <profile> ...
#
# Each architecture is built separately and the two apps are merged with lipo:
# Flutter's universal build calls `lipo <file> -verify_arch arm64 x86_64`,
# which Xcode 27's lipo rejects ("-verify_arch requires exactly one input file").
set -euo pipefail
cd "$(dirname "$0")/.."

CORE=macos/core/huanyou-core
[ -f "$CORE" ] || { echo "missing $CORE, run: ../mobile/build.sh macos" >&2; exit 1; }
(cd macos && ruby scripts/add_core_phase.rb >/dev/null)

defines=()
[ -n "${1:-}" ] && defines+=("--dart-define=API_BASE_URLS=$1")
IDENTITY="${SIGN_IDENTITY:--}"

ver=$(sed -n 's/^version:[[:space:]]*//p' pubspec.yaml)
name=${ver%%+*}
build=${ver##*+}
out=build/dist
mkdir -p "$out"
cfg="$(mktemp -d)"
trap 'rm -rf "$cfg"' EXIT
work="$cfg/apps" # outside build/: flutter build prunes unknown entries there
mkdir -p "$work"

for arch in arm64 x86_64; do
  echo "ARCHS = $arch" > "$cfg/$arch.xcconfig"
  XCODE_XCCONFIG_FILE="$cfg/$arch.xcconfig" flutter build macos --release \
    --obfuscate --split-debug-info="build/symbols/$arch" ${defines[@]+"${defines[@]}"}
  ditto build/macos/Build/Products/Release/Huanyou.app "$work/$arch.app"
done

app="$out/环游.app"
rm -rf "$app"
ditto "$work/arm64.app" "$app"
# Merge every Mach-O file of the two builds into a universal one.
(cd "$work/arm64.app" && find . -type f) | while read -r f; do
  a="$work/arm64.app/$f"; x="$work/x86_64.app/$f"
  [ -f "$x" ] || continue
  if file -b "$a" | grep -q "Mach-O"; then
    if lipo -archs "$a" | grep -qw x86_64; then continue; fi # already universal (e.g. the core)
    lipo -create "$a" "$x" -output "$app/$f"
  fi
done

# lipo invalidates the signatures: re-sign inside out.
if [ "$IDENTITY" = "-" ]; then
  # Ad-hoc: no hardened runtime. Its library validation rejects ad-hoc
  # frameworks (no Team ID), and it is only required for notarization.
  sign() { codesign --force --timestamp=none --sign - "$@"; }
else
  sign() { codesign --force --options runtime --timestamp --sign "$IDENTITY" "$@"; }
fi
find "$app/Contents/Frameworks" -maxdepth 1 -name "*.framework" -print0 | while IFS= read -r -d '' fw; do sign "$fw"; done
sign "$app/Contents/MacOS/huanyou-core"
sign --entitlements macos/Runner/Release.entitlements "$app"
codesign --verify --deep --strict "$app"
lipo -info "$app/Contents/MacOS/Huanyou"

# ---- DMG ----
dmg="$out/huanyou-macos-$name-$build.dmg"
stage="$cfg/dmg"
rm -rf "$stage" && mkdir -p "$stage"
ditto "$app" "$stage/环游.app"
ln -s /Applications "$stage/Applications"
rm -f "$dmg"
hdiutil create -volname "环游" -srcfolder "$stage" -fs HFS+ -format UDZO \
  -imagekey zlib-level=9 -ov "$dmg" >/dev/null
if [ "$IDENTITY" != "-" ]; then
  codesign --force --timestamp --sign "$IDENTITY" "$dmg"
  if [ -n "${NOTARY_PROFILE:-}" ]; then
    xcrun notarytool submit "$dmg" --keychain-profile "$NOTARY_PROFILE" --wait
    xcrun stapler staple "$dmg"
  fi
fi
hdiutil verify "$dmg" >/dev/null
echo "-> $dmg (build $build)"
