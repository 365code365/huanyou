#!/usr/bin/env bash
# Downloads the mainland-China IP list used by "智能分流" (smart routing).
# Source: https://github.com/Loyalsoldier/geoip (updated daily, ~140 KB).
# Re-run before each release to keep the list current.
set -euo pipefail
cd "$(dirname "$0")/.."
mkdir -p assets/geo
URL="https://github.com/Loyalsoldier/geoip/releases/latest/download/geoip-only-cn-private.dat"
curl -fL --retry 3 -o assets/geo/geoip-only-cn-private.dat.tmp "$URL"
curl -fL --retry 3 -o assets/geo/sha256.txt "$URL.sha256sum"
( cd assets/geo && mv geoip-only-cn-private.dat.tmp geoip-only-cn-private.dat && shasum -a 256 -c sha256.txt && rm sha256.txt )
ls -la assets/geo
