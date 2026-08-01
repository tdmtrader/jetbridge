#!/bin/sh
set -eu

version=$(claude --version 2>&1)
case "$version" in 2.1.212*) ;; *) echo "ERROR: expected Claude 2.1.212, got $version" >&2; exit 1;; esac
help=$(claude --help 2>&1)
for flag in --max-budget-usd --mcp-config --strict-mcp-config --max-turns --append-system-prompt --output-format --verbose --dangerously-skip-permissions; do
  printf '%s\n' "$help" | grep -F -- "$flag" >/dev/null
done
command -v agent-runner >/dev/null
command -v function-runner >/dev/null
command -v agent-output >/dev/null
