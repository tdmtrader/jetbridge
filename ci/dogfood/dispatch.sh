#!/bin/sh
# Dispatch a dogfood plan-slice run on the live Concourse (concourse.home).
#
#   usage: dispatch.sh <plan-file> <task-range> [base-branch]
#
#   plan-file    repo-relative path to a workstream plan, e.g.
#                docs/superpowers/plans/agentic-platform/03-pipeline-runs.md
#   task-range   inclusive plan-task range, e.g. "3-6" (or a single task, "4")
#   base-branch  branch to base the work on (default: jetbridge)
#
# Env:
#   FLY_TARGET    fly target to use (default: cicd — see memory
#                 reference_theborg_cicd_live_concourse.md; `home` also works)
#   DOGFOOD_FLAT  set to 1 to use a plain suffixed pipeline name instead of an
#                 instanced pipeline (fallback if the web node does not have
#                 pipeline instances enabled)
#   DOGFOOD_FORCE set to 1 to skip the settled-release-chain guard and
#                 dispatch even while jetbridge/* builds are in flight
set -eu

usage() {
  echo "usage: dispatch.sh <plan-file> <task-range> [base-branch]" >&2
  exit 2
}
[ $# -ge 2 ] && [ $# -le 3 ] || usage

PLAN_FILE=$1
TASK_RANGE=$2
BASE_BRANCH=${3:-jetbridge}
TARGET=${FLY_TARGET:-cicd}

REPO_ROOT=$(cd "$(dirname "$0")/../.." && pwd)
[ -f "$REPO_ROOT/$PLAN_FILE" ] || { echo "error: plan file not found: $PLAN_FILE" >&2; exit 2; }

# Derive slugs: plan slug from the filename, range slug sanitized for
# pipeline/branch names ("3-6" stays "3-6").
PLAN_SLUG=$(basename "$PLAN_FILE" .md)
RANGE_SLUG=$(printf '%s' "$TASK_RANGE" | tr -c 'a-zA-Z0-9' '-' | sed 's/-*$//;s/^-*//')
BRANCH_NAME="agent/dogfood-${PLAN_SLUG}-${RANGE_SLUG}"

CONFIG="$REPO_ROOT/deploy/dogfood-pipeline.yml"
WEB_URL="https://concourse.home"

# Wait-for-settled-chain guard (ci/dogfood/FINDINGS.md): a pending/started
# jetbridge/* build means a release chain is in flight, and its self-upgrade
# restarts web ~10-12 min after the push — a restart mid-run re-runs the
# implement step and double-spends the shared rate-limit window. `fly builds`
# name column is pipeline/job/build-name, status is the third column.
if [ "${DOGFOOD_FORCE:-0}" = "1" ]; then
  echo "warning: DOGFOOD_FORCE=1 — skipping the settled-release-chain guard" >&2
else
  RECENT_BUILDS=$(fly -t "$TARGET" builds --count 30) || {
    echo "error: 'fly -t $TARGET builds' failed; cannot verify the release chain is settled" >&2
    exit 3
  }
  IN_FLIGHT=$(printf '%s\n' "$RECENT_BUILDS" \
    | awk '$2 ~ /^jetbridge\// && ($3 == "pending" || $3 == "started")')
  if [ -n "$IN_FLIGHT" ]; then
    echo "error: jetbridge release chain in flight — dispatching now risks a mid-run web restart:" >&2
    printf '%s\n' "$IN_FLIGHT" >&2
    echo "wait for these to settle (fly -t $TARGET builds), or override with DOGFOOD_FORCE=1" >&2
    exit 3
  fi
fi

if [ "${DOGFOOD_FLAT:-0}" = "1" ]; then
  PIPELINE="dogfood-${PLAN_SLUG}-${RANGE_SLUG}"
  INSTANCE="$PIPELINE"
  fly -t "$TARGET" set-pipeline -n -p "$PIPELINE" -c "$CONFIG" \
    -v plan_file="$PLAN_FILE" \
    -v task_range="$TASK_RANGE" \
    -v base_branch="$BASE_BRANCH" \
    -v branch_name="$BRANCH_NAME"
else
  PIPELINE="dogfood"
  INSTANCE="${PIPELINE}/plan:${PLAN_SLUG},range:${RANGE_SLUG}"
  fly -t "$TARGET" set-pipeline -n -p "$PIPELINE" -c "$CONFIG" \
    --instance-var plan="$PLAN_SLUG" \
    --instance-var range="$RANGE_SLUG" \
    -v plan_file="$PLAN_FILE" \
    -v task_range="$TASK_RANGE" \
    -v base_branch="$BASE_BRANCH" \
    -v branch_name="$BRANCH_NAME"
fi

fly -t "$TARGET" unpause-pipeline -p "$INSTANCE"
fly -t "$TARGET" trigger-job -j "$INSTANCE/run"

echo ""
echo "Dispatched: $PLAN_FILE tasks $TASK_RANGE (base: $BASE_BRANCH)"
echo "  branch on success: $BRANCH_NAME"
echo ""
echo "Watch:"
echo "  fly -t $TARGET watch -j '$INSTANCE/run'"
echo "Web:"
echo "  $WEB_URL/teams/main/pipelines/$PIPELINE"
