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

# The pipeline's split, sized for a laptop: the heavy Ginkgo suites under ONE
# ginkgo -p (suites in turn), everything else under one `go test`, both at once.
# Plain `ginkgo` over the whole selection took ~7m for 20 packages on a 10-core
# Mac; this took ~3m20s. The pipeline runs a ginkgo per suite instead, but a
# laptop is CPU-bound -- measured, that took 4m12s here, every suite slower --
# and jetbridge stays under `go test` because -p would rerun its plain tests in
# every process. Parallelism is capped because every test postgres holds a
# SysV segment and macOS allows 32 system-wide (kern.sysv.shmmni).
heavy=() rest=()
while IFS= read -r d; do
  case "$d" in
    ./atc/db | ./atc/api | ./atc/db/migration | ./atc/exec | ./atc/scheduler/algorithm | ./atc/engine | ./atc/runs) heavy+=("$d") ;;
    *) rest+=("$d") ;;
  esac
done <<<"$dirs"

ncpu="$(getconf _NPROCESSORS_ONLN)"
procs=$(( ncpu / 3 )); [ "$procs" -ge 2 ] || procs=2
pkgs=$(( ncpu / 2 )); [ "$pkgs" -ge 2 ] || pkgs=2

started=$(date +%s)
go_status=0 ginkgo_status=0 ginkgo_pid=""
log="$(mktemp "${TMPDIR:-/tmp}/test-affected-ginkgo.XXXXXX")"
if [ ${#heavy[@]} -gt 0 ]; then
  echo "==> ginkgo -p --procs=$procs: ${heavy[*]}" >&2
  ginkgo -p --procs="$procs" --keep-going --flake-attempts=1 "$@" "${heavy[@]}" >"$log" 2>&1 &
  ginkgo_pid=$!
fi
if [ ${#rest[@]} -gt 0 ]; then
  echo "==> go test -p $pkgs: ${#rest[@]} package(s)" >&2
  go test -count=1 -p "$pkgs" "${rest[@]}" || go_status=$?
fi
if [ -n "$ginkgo_pid" ]; then
  wait "$ginkgo_pid" || ginkgo_status=$?
  echo "==> ginkgo suites (exit $ginkgo_status):" >&2
  cat "$log"
fi
rm -f "$log"
echo "==> test-affected: $(( $(date +%s) - started ))s; go test exit $go_status; ginkgo exit $ginkgo_status" >&2
[ "$go_status" -eq 0 ] && [ "$ginkgo_status" -eq 0 ]
