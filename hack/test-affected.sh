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

# The same split as the pipeline's unit-tests job: the heavy Ginkgo suites
# under ginkgo -p, everything else under one `go test`, both at once. Plain
# `ginkgo` runs suites one after another, which on a 10-core Mac took ~7m for a
# 20-package selection. Parallelism is capped because every test postgres
# holds a SysV segment and macOS allows 32 system-wide (kern.sysv.shmmni).
heavy=() rest=()
while IFS= read -r d; do
  case "$d" in
    ./atc/db | ./atc/api | ./atc/db/migration | ./atc/exec | ./atc/scheduler/algorithm | ./atc/engine | ./atc/runs | ./atc/worker/jetbridge) heavy+=("$d") ;;
    *) rest+=("$d") ;;
  esac
done <<<"$dirs"

ncpu="$(getconf _NPROCESSORS_ONLN)"
# Per suite; up to seven suites run at once beside go test.
procs=2
pkgs=$(( ncpu / 2 )); [ "$pkgs" -ge 2 ] || pkgs=2

started=$(date +%s)
go_status=0 failed=()
logdir="$(mktemp -d "${TMPDIR:-/tmp}/test-affected.XXXXXX")"
pids=()
for d in "${heavy[@]+"${heavy[@]}"}"; do
  ginkgo -p --procs="$procs" --keep-going --flake-attempts=1 "$@" "$d" \
    >"$logdir/$(tr / - <<<"${d#./}").log" 2>&1 &
  pids+=("$!")
done
[ ${#heavy[@]} -eq 0 ] || echo "==> ginkgo -p --procs=$procs, one process per suite: ${heavy[*]}" >&2
if [ ${#rest[@]} -gt 0 ]; then
  echo "==> go test -p $pkgs: ${#rest[@]} package(s)" >&2
  go test -count=1 -p "$pkgs" "${rest[@]}" || go_status=$?
fi
for i in "${!pids[@]}"; do
  d="${heavy[$i]}"
  if ! wait "${pids[$i]}"; then failed+=("$d"); fi
  echo "==> ginkgo $d:" >&2
  cat "$logdir/$(tr / - <<<"${d#./}").log"
done
rm -rf "$logdir"
echo "==> test-affected: $(( $(date +%s) - started ))s; go test exit $go_status; failed ginkgo suites: ${failed[*]:-none}" >&2
[ "$go_status" -eq 0 ] && [ ${#failed[@]} -eq 0 ]
