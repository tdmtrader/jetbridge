#!/usr/bin/env bash
# Rebuild the embedded Elm bundle (web/public/elm.min.js) after Elm source
# changes. The served UI is elm.min.js — editing web/elm/src without running
# this leaves the browser on the OLD bundle (a known stale-bundle trap).
# Requires: elm 0.19.1 and uglify-js (npm i -g uglify-js).
set -euo pipefail
cd "$(dirname "$0")/../web/elm"
elm make --optimize --output ../public/elm.js src/Main.elm
uglifyjs ../public/elm.js \
  --compress "pure_funcs=[F2,F3,F4,F5,F6,F7,F8,F9,A2,A3,A4,A5,A6,A7,A8,A9],pure_getters,keep_fargs=false,unsafe_comps,unsafe" \
  | uglifyjs --mangle --output ../public/elm.min.js
rm -f ../public/elm.js
echo "built web/public/elm.min.js ($(wc -c < ../public/elm.min.js) bytes)"
