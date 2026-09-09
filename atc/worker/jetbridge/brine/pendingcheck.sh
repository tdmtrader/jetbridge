#!/bin/bash
# `brine check` over features/pending/, which the .brine glob deliberately does
# not match.
#
# Those scenarios are never executed, so nothing else would notice a chain that
# does not type-check: a step whose input state is not what the step before it
# produced is a scenario that can never be moved up, and the longer it sits the
# more expensive it is to find. This runs the same static walk the running
# corpus gets.
#
# Verify this script can FAIL before trusting it: add a line to any pending
# feature whose input state is wrong (`And the capture settles` straight after a
# Given, say) and this must report invalid and exit 1.
set -euo pipefail
cd "$(dirname "$0")"
export PATH="$HOME/brine-private/target/debug:$PATH"

if [ ! -d features/pending ] || [ -z "$(ls -A features/pending/*.feature 2>/dev/null)" ]; then
  echo "no pending features — the family has fully landed"
  exit 0
fi

# The manifest's glob is the thing under test elsewhere, so it is restored on
# every exit path rather than edited in place and hoped over.
backup=$(mktemp)
cp .brine "$backup"
trap 'cp "$backup" .brine; rm -f "$backup"' EXIT

sed -i.tmp 's|^features: "features/\*.feature"$|features: "features/pending/*.feature"|' .brine
rm -f .brine.tmp

output=$(brine check 2>&1 || true)
echo "$output" | grep '"type":"check_end"'
if echo "$output" | grep -q '"invalid":0'; then
  exit 0
fi
echo "$output" | grep '"status":"invalid"' || true
exit 1
