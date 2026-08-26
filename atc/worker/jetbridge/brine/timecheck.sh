#!/bin/bash
# A scenario that waits on a Kubernetes deadline does not FAIL — it HANGS.
# PodStartupTimeout defaults to 5 minutes and PodSchedulingTimeout to 15, so a
# scenario written over the wrong chain blocks instead of going red, and the
# whole suite blows its timeout with no indication which scenario did it.
#
# That happened once (RF-07) and cost a ten-minute suite timeout to diagnose.
# The "confirm your feature runs under 60 seconds" rule was a convention with
# nothing enforcing it. This enforces it per feature file.
#
# macOS has no GNU `timeout`, so the deadline is a polled wait on a background
# job. Verify this script can FAIL before trusting it:
#   BRINE_FEATURE_TIMEOUT=2 ./timecheck.sh   # must report TOO SLOW and exit 1
set -uo pipefail
cd "$(dirname "$0")"
export PATH="$HOME/brine-private/target/debug:$PATH"
LIMIT="${BRINE_FEATURE_TIMEOUT:-120}"
status=0

for f in features/*.feature; do
  start=$(date +%s)
  brine run "$f" --mode sync >/dev/null 2>&1 &
  pid=$!
  waited=0
  while kill -0 "$pid" 2>/dev/null && [ "$waited" -lt "$LIMIT" ]; do
    sleep 1
    waited=$((waited + 1))
  done
  if kill -0 "$pid" 2>/dev/null; then
    kill -9 "$pid" 2>/dev/null
    wait "$pid" 2>/dev/null
    echo "HANGING (>${LIMIT}s): $(basename "$f") — a scenario is waiting on a deadline, not failing"
    status=1
    continue
  fi
  wait "$pid" 2>/dev/null
  took=$(( $(date +%s) - start ))
  if [ "$took" -ge "$LIMIT" ]; then
    echo "TOO SLOW (${took}s >= ${LIMIT}s): $(basename "$f")"
    status=1
  else
    printf '%4ss  %s\n' "$took" "$(basename "$f")"
  fi
done
exit $status
