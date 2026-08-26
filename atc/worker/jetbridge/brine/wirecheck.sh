#!/bin/bash
# A step family that is defined but never registered does not fail loudly —
# its scenarios report `missing_step`, which reads like an authoring mistake
# in the feature file. This happened three times in one session while several
# agents edited steps/registry.go concurrently, so it is a check now.
set -euo pipefail
cd "$(dirname "$0")"
defined=$(grep -ohE '^func ([A-Z][A-Za-z]*Definitions)\(\)' steps/*.go \
          | sed 's/^func //; s/()//' | grep -v '^ResourceDefinitions$' | sort -u)
wired=$(grep -oE '[A-Z][A-Za-z]*Definitions\(\)' steps/registry.go \
        | sed 's/()//' | sort -u)
missing=$(comm -23 <(printf '%s\n' "$defined") <(printf '%s\n' "$wired") || true)
if [ -n "$missing" ]; then
  echo "UNWIRED STEP FAMILIES (defined in steps/ but never appended in registry.go):"
  printf '  %s\n' $missing
  exit 1
fi
printf 'all %d step families wired\n' "$(printf '%s\n' "$defined" | wc -l | tr -d ' ')"
