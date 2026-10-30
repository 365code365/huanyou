#!/usr/bin/env bash
# Downloads the mainland-China IP list used by "智能分流" (smart routing).
# Source: https://github.com/Loyalsoldier/geoip (updated daily, ~140 KB).
# The API embeds it and compiles it into the profiles it sends to the app
# (server/internal/profile), so the app ships no geo file. Re-run before a
# server release to keep the list current, then rebuild the API.
set -euo pipefail
cd "$(dirname "$0")/../../server/internal/profile"
URL="https://github.com/Loyalsoldier/geoip/releases/latest/download/geoip-only-cn-private.dat"
curl -fL --retry 3 -o geoip-only-cn-private.dat.tmp "$URL"
curl -fL --retry 3 -o sha256.txt "$URL.sha256sum"
mv geoip-only-cn-private.dat.tmp geoip-only-cn-private.dat && shasum -a 256 -c sha256.txt && rm sha256.txt
ls -la geoip-only-cn-private.dat
