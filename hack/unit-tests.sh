#!/bin/sh
#
# unit-tests.sh -- the unit tier: every package of the root module except the
# ones in EXCLUDED. Both entry points run this file, so there is one package
# list and one exclusion list:
#
#   make test-unit            a developer machine (no flag)
#   hack/unit-tests.sh --ci   the pipeline's unit-tests job, and so also
#                             hack/ci-check.sh, which lifts that job
#
# The two modes select the same packages and differ only in how they schedule
# them and what they check first. Local is one `ginkgo -r -p`, as it always
# was. --ci adds what only the CI pod needs: the fake-gcs reachability report,
# the PostgreSQL binary pre-check, UNIT_PACKAGES narrowing for
# `hack/ci-check.sh --affected`, and a heavy/rest split sized off nproc.
#
# POSIX sh: the pipeline runs it under the runner image's /bin/sh (dash).

set -e
cd "$(dirname "$0")/.."

# Packages outside the unit tier. TestOneUnitExclusionList (deploy/chart/tests)
# holds both entry points to this list. Before there was one list there were two, and they drifted silently: CI
# skipped six packages -- 52k lines of tests -- that every developer machine
# ran.
#
#   topgun                       the K8s tiers; they bring their own cluster
#   fly/integration              its own entry point, make test-fly-integration,
#                                which the unit-tests job runs after this file
#   testhelpers/otel             talks to tempo.home and loki.home
#   atc/worker/jetbridge/brine   a nested module: `go list ./...` never names
#                                it, but `ginkgo -r` walks directories, not
#                                modules, and would fail there on what only the
#                                brine job has (a brine binary, .build/busybox,
#                                otelcol). make test-brine-guards runs the part
#                                that needs nothing.
EXCLUDED="topgun fly/integration testhelpers/otel atc/worker/jetbridge/brine"

if [ "${1:-}" != --ci ]; then
  # Requires: PostgreSQL binaries on PATH (the suites start their own).
  exec ginkgo -r -p --keep-going --flake-attempts=1 \
    --skip-package="$(echo $EXCLUDED | tr ' ' ',')"
fi

set -x

# The Hangar output plane's tier-2 conformance suite reads
# HANGAR_FAKE_GCS_ENDPOINT, which the job's params set; why it is wired is
# written beside them in deploy/concourse-pipeline.yml. The assumption this
# wiring rests on, checked rather than assumed: that this task pod resolves
# the service in its own namespace. If it does not, the fully-qualified
# fake-gcs.cicd.svc.cluster.local:4443 is the next thing to try, and a
# NetworkPolicy exception after that -- dropping the wiring is not one of the
# options, because the suite would then report conformance it never ran.
echo "=== Hangar tier-2 substrate reachability ==="
echo "HANGAR_FAKE_GCS_ENDPOINT=${HANGAR_FAKE_GCS_ENDPOINT:-} HANGAR_CI=${HANGAR_CI:-}"
if wget -q -O- --timeout=5 "${HANGAR_FAKE_GCS_ENDPOINT:-}/storage/v1/b" >/dev/null 2>&1; then
  echo "reachable: the endpoint answered"
else
  echo "NOT reachable from this pod: the tier-2 conformance suite will fail with its"
  echo "named reason, which is the intended behaviour and not a flake."
fi
echo ""
echo "=== Running unit tests across the full codebase ==="
echo "Excluding: $EXCLUDED"
echo ""

# Most of the suites below run against a real postmaster that
# atc/postgresrunner starts itself (no sidecar, no service). It shells out to
# these three, and a missing one fails deep inside a BeforeSuite where the
# cause is not obvious -- so say it here.
for tool in initdb postgres psql; do
  command -v "$tool" >/dev/null 2>&1 || {
    echo "ERROR: '$tool' is not on PATH."
    echo "The test-runner image must carry the PostgreSQL server and"
    echo "client binaries; 24 packages start their own postmaster."
    exit 1
  }
done

# `ginkgo --skip-package` matches any part of the path; a fixed-string grep for
# "/<entry>" is the same test over import paths.
# One pattern per line: grep -F treats each line as a separate fixed string.
PACKAGES=$(go list ./... | grep -Fv "$(for p in $EXCLUDED; do echo "/$p"; done)")
echo "Testing packages:"
echo "$PACKAGES" | head -20
echo "... ($(echo "$PACKAGES" | wc -l | tr -d ' ') packages total)"
echo ""

# The heavy Ginkgo suites run under the Ginkgo CLI with -p, beside a plain
# `go test` of everything else. -p runs the whole test binary in each process,
# so a suite's plain Go tests run once per process -- as they always have under
# `make test-unit`. Under `go test` alone each suite is one process, and
# atc/db (~1000 specs, 278s) was the whole job's critical path. Procs scale
# with the node (nproc sees the whole node: there is no CPU limit), so theborg
# gets fewer than the battle station.
MODULE=$(go list -m)
# hack/ci-check.sh --affected sets UNIT_PACKAGES to the packages a change can
# reach (hack/ci-affected). The exclusions above still apply; the pipeline
# itself never sets it.
if [ -n "${UNIT_PACKAGES:-}" ]; then
  PACKAGES=$(echo "$PACKAGES" | grep -Fx "$(echo "$UNIT_PACKAGES" | tr ' ' '\n')" || true)
  echo "UNIT_PACKAGES narrows the run to $(echo "$PACKAGES" | grep -c . || true) package(s)"
  if [ -z "$PACKAGES" ]; then
    echo "=== Nothing to test ==="
    exit 0
  fi
fi
HEAVY=""
for d in atc/db atc/api atc/db/migration atc/exec atc/scheduler/algorithm atc/engine atc/runs atc/worker/jetbridge; do
  if echo "$PACKAGES" | grep -Fxq "$MODULE/$d"; then HEAVY="$HEAVY $d"; fi
done
HEAVY_PKGS=$(for d in $HEAVY; do echo "$MODULE/$d"; done)
REST=$(echo "$PACKAGES" | grep -Fxv "$HEAVY_PKGS" || true)
# Per suite, and up to seven suites run at once beside go test.
PROCS=$(( $(nproc) / 6 ))
[ "$PROCS" -ge 2 ] || PROCS=2
[ "$PROCS" -le 6 ] || PROCS=6
# On a big node (the battle station, 28 cores) each suite gets its own Ginkgo
# process, all at once: a single `ginkgo` given several suites runs them one
# after another. A smaller node is CPU-bound and that fan-out only slows every
# suite down (measured on a 10-core laptop: 4m12s against 3m17s), so there one
# `ginkgo` takes them in turn.
GINKGO_PIDS=""
if [ -n "$HEAVY" ]; then
  go build -o /tmp/ginkgo github.com/onsi/ginkgo/v2/ginkgo
  if [ "$(nproc)" -ge 24 ]; then
    echo "Ginkgo -p (procs=$PROCS each, one process per suite):$HEAVY"
    for d in $HEAVY; do
      /tmp/ginkgo -p --procs="$PROCS" --keep-going --flake-attempts=1 \
        --timeout=20m "./$d" >"/tmp/ginkgo-$(echo "$d" | tr / -).log" 2>&1 &
      GINKGO_PIDS="$GINKGO_PIDS $!:$d"
    done
  else
    PROCS=$(( $(nproc) / 3 ))
    [ "$PROCS" -ge 2 ] || PROCS=2
    echo "Ginkgo -p (procs=$PROCS, suites in turn):$HEAVY"
    /tmp/ginkgo -p --procs="$PROCS" --keep-going --flake-attempts=1 \
      --timeout=20m $(for d in $HEAVY; do echo "./$d"; done) >/tmp/ginkgo-heavy.log 2>&1 &
    GINKGO_PIDS="$!:heavy"
  fi
fi
GO_TEST_STATUS=0
if [ -n "$REST" ]; then
  go test -count=1 -timeout 20m $REST || GO_TEST_STATUS=$?
fi
GINKGO_FAILED=""
for entry in $GINKGO_PIDS; do
  status=0
  wait "${entry%%:*}" || status=$?
  d="${entry#*:}"
  echo "=== ginkgo $d (exit $status) ==="
  cat "/tmp/ginkgo-$(echo "$d" | tr / -).log"
  [ "$status" -eq 0 ] || GINKGO_FAILED="$GINKGO_FAILED $d"
done
[ -z "$GINKGO_FAILED" ] || echo "=== FAILED Ginkgo suites:$GINKGO_FAILED ==="
# Not `[ ] && [ ]`: sh -e does not exit on a failure inside an && list.
if [ "$GO_TEST_STATUS" -ne 0 ] || [ -n "$GINKGO_FAILED" ]; then exit 1; fi
echo "=== All unit tests passed ==="
