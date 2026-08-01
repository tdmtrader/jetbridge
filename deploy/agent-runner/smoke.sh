#!/bin/sh
set -eu

if version=$(claude --version 2>&1); then
  :
else
  status=$?
  printf 'ERROR: Claude version command failed (status %s)\n' "$status" >&2
  exit 1
fi
case "$version" in 2.1.212*) ;; *) echo "ERROR: expected Claude 2.1.212" >&2; exit 1;; esac

if help=$(claude --help 2>&1); then
  :
else
  status=$?
  printf 'ERROR: Claude help command failed (status %s)\n' "$status" >&2
  exit 1
fi
for flag in --max-budget-usd --mcp-config --strict-mcp-config --max-turns --append-system-prompt --output-format --verbose --dangerously-skip-permissions; do
  if ! printf '%s\n' "$help" | grep -F -- "$flag" >/dev/null; then
    printf 'ERROR: Claude help is missing required flag %s\n' "$flag" >&2
    exit 1
  fi
done
for binary in agent-runner function-runner agent-output; do
  if ! command -v "$binary" >/dev/null 2>&1; then
    printf 'ERROR: required Concourse binary missing: %s\n' "$binary" >&2
    exit 1
  fi
done
