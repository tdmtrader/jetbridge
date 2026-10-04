#!/usr/bin/env bash
#
# test-affected.sh -- the fastest inner loop: run, on this machine, only the
# unit-test packages a change can reach.
#
# The change is the working tree against `base` (default origin/core, merge-base
# semantics), uncommitted and untracked files included; hack/ci-affected turns
# it into packages. A change it cannot bound (go.mod, a file in no Go package)
# runs the whole tier with `make test-unit`.
#
# This is not a gate. It skips everything outside the selection and runs on
# macOS, so Linux-only behaviour is invisible to it -- before offering a branch,
# run hack/ci-check.sh (or `hack/ci-check.sh --affected` for a remote quick
# pass on the cluster).
#
# Usage:
#   hack/test-affected.sh [base] [-- extra ginkgo flags]

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$REPO_ROOT"

BASE=origin/core
if [ $# -gt 0 ] && [ "$1" != -- ]; then BASE="$1"; shift; fi
if [ "${1:-}" = -- ]; then shift; fi

affected="$(go run ./hack/ci-affected "$BASE" WORKTREE)"
if [ "$affected" = ALL ]; then
  echo "==> every package is affected; running make test-unit" >&2
  exec make test-unit
fi

# The same exclusions as `make test-unit` and the pipeline's unit-tests job.
dirs="$(sed "s#^$(go list -m)#.#" <<<"$affected" \
  | grep -Ev '/integration|/testflight|/topgun|/fly/integration|/testhelpers/otel|/atc/worker/jetbridge/brine' || true)"
if [ -z "$dirs" ]; then
  echo "==> nothing affected since $BASE" >&2
  exit 0
fi

echo "==> $(wc -l <<<"$dirs" | tr -d ' ') package(s) affected since $BASE:" >&2
sed 's/^/      /' <<<"$dirs" >&2
# shellcheck disable=SC2046
exec ginkgo -p --keep-going --flake-attempts=1 "$@" $(tr '\n' ' ' <<<"$dirs")
