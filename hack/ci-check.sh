#!/usr/bin/env bash
#
# ci-check.sh -- run the real CI tasks against a ref, before offering it for merge.
#
# The local test tiers run on macOS. CI runs on Linux, in the
# registry.home/concourse-test-runner:v9 image, on the theborg cluster. Two
# whole classes of failure live in that gap and no amount of `make test-unit`
# will show them:
#
#   * process-environment assumptions -- signal dispositions inherited from the
#     parent, PATH, uid, /proc. A test that traps SIGHUP passes on a Mac and
#     fails under a CI shell that was started with SIGHUP ignored.
#   * anything that needs the cluster -- live-tagged suites are skipped on
#     darwin entirely, so a live test that misbehaves is invisible here.
#
# So rather than approximate CI, this runs CI: it lifts each job's inline task
# config straight out of deploy/concourse-pipeline.yml and hands it to
# `fly execute`, which schedules it on the same cluster, in the same image,
# running the same script. What it cannot reproduce is the pipeline's
# `attempts: 2` (a one-off build gets one attempt) and `timeout:` -- a hung
# task here hangs your terminal, not a serial group.
#
# Usage:
#   hack/ci-check.sh [ref] [job ...]
#
#   ref   git ref to test (default: HEAD)
#   job   pipeline jobs to run, in order (default: build-and-vet unit-tests)
#
# Environment:
#   FLY_TARGET   fly target to execute against (default: loupe-local)
#   KUBECONFIG   defaults to $HOME/.kube/config
#   PORT_FORWARD_NS / PORT_FORWARD_SVC / FLY_PORT  where to point the tunnel
#
# Two runs at once share the tunnel: whichever one opened it takes it down on
# exit, and the other loses its log stream mid-build. The build itself survives
# on the cluster -- re-attach with `fly -t <target> watch -b <id>`.
#
# Jobs whose task config interpolates pipeline vars (`((name))`) need those
# values passed explicitly -- `fly execute` has no credential manager behind it.
# Export the var name uppercased with dashes as underscores (github-token ->
# GITHUB_TOKEN) and this script forwards it with `-v`. Missing ones are named
# and the run stops before it burns a build.

set -euo pipefail

FLY_TARGET="${FLY_TARGET:-loupe-local}"
FLY_PORT="${FLY_PORT:-18080}"
PORT_FORWARD_NS="${PORT_FORWARD_NS:-cicd}"
PORT_FORWARD_SVC="${PORT_FORWARD_SVC:-svc/concourse-web}"
export KUBECONFIG="${KUBECONFIG:-$HOME/.kube/config}"

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

REF="${1:-HEAD}"
if [ $# -gt 0 ]; then shift; fi
if [ $# -gt 0 ]; then
  JOBS=("$@")
else
  JOBS=(build-and-vet unit-tests)
fi

WORK="$(mktemp -d "${TMPDIR:-/tmp}/ci-check.XXXXXX")"
PF_PID=""

cleanup() {
  if [ -n "$PF_PID" ]; then
    kill "$PF_PID" 2>/dev/null || true
    wait "$PF_PID" 2>/dev/null || true
  fi
  rm -rf "$WORK"
}
trap cleanup EXIT

log() { printf '==> %s\n' "$*" >&2; }

port_is_open() {
  # No `nc` guarantee on every box; bash's /dev/tcp is always there.
  (exec 3<>"/dev/tcp/127.0.0.1/$FLY_PORT") 2>/dev/null
}

ensure_tunnel() {
  if port_is_open; then
    log "port $FLY_PORT already open; leaving it alone"
    return
  fi

  log "opening port-forward $PORT_FORWARD_NS/$PORT_FORWARD_SVC $FLY_PORT:8080"
  kubectl -n "$PORT_FORWARD_NS" port-forward "$PORT_FORWARD_SVC" "$FLY_PORT:8080" \
    >"$WORK/port-forward.log" 2>&1 &
  PF_PID=$!

  for _ in $(seq 1 50); do
    if port_is_open; then
      log "port-forward up (pid $PF_PID)"
      return
    fi
    if ! kill -0 "$PF_PID" 2>/dev/null; then
      log "port-forward died:"
      cat "$WORK/port-forward.log" >&2
      exit 1
    fi
    sleep 0.2
  done

  log "port-forward never came up:"
  cat "$WORK/port-forward.log" >&2
  exit 1
}

# The working tree is not the ref. Other sessions leave uncommitted files
# around, and `fly execute` uploads the whole input directory -- so materialise
# the committed tree and nothing else.
materialise() {
  local sha
  sha="$(git -C "$REPO_ROOT" rev-parse --verify "$REF^{commit}")"
  mkdir -p "$WORK/src"
  git -C "$REPO_ROOT" archive --format=tar "$sha" | tar -x -C "$WORK/src"
  echo "$sha"
}

vars_for() {
  # `((name))` in a task config is a pipeline var. fly execute cannot resolve
  # one, so require it from the environment and pass it through.
  local cfg="$1" job="$2" missing=() name env_name
  FLY_VARS=()

  while IFS= read -r name; do
    [ -n "$name" ] || continue
    env_name="$(printf '%s' "$name" | tr '[:lower:]' '[:upper:]' | tr -c '[:alnum:]' '_')"
    if [ -z "${!env_name:-}" ]; then
      missing+=("$name (export $env_name)")
    else
      FLY_VARS+=(-v "$name=${!env_name}")
    fi
  # A task script may legitimately contain `$((...))`. Concourse's own var
  # syntax never follows a `$`, so require the preceding character not to be
  # one -- and pad each line so a var at column 1 still has one.
  done < <(sed 's/^/ /' "$cfg" \
    | grep -oE '[^$]\(\([A-Za-z0-9_.:/-]+\)\)' \
    | sed 's/^.*((//; s/))$//' \
    | sort -u)

  if [ ${#missing[@]} -gt 0 ]; then
    log "job '$job' needs pipeline vars this script cannot resolve:"
    printf '    %s\n' "${missing[@]}" >&2
    return 1
  fi
}

ensure_tunnel

if ! fly -t "$FLY_TARGET" status >/dev/null 2>&1; then
  log "fly target '$FLY_TARGET' is not logged in; run: fly -t $FLY_TARGET login"
  exit 1
fi

SHA="$(materialise)"
log "ref $REF -> $SHA"
log "jobs: ${JOBS[*]}"

declare -a VERDICTS=()
FAILED=0

for job in "${JOBS[@]}"; do
  cfg="$WORK/$job.yml"

  # The task config comes from the *ref*, not from the working tree: a branch
  # that changes its own CI task should be checked with the task it ships.
  if ! go run "$REPO_ROOT/hack/ci-extract-task" "$WORK/src/deploy/concourse-pipeline.yml" "$job" >"$cfg"; then
    VERDICTS+=("$job: SKIPPED (could not extract task config)")
    FAILED=1
    break
  fi

  inputs=()
  while IFS= read -r in_name; do
    [ -n "$in_name" ] || continue
    inputs+=(-i "$in_name=$WORK/src")
  done < <(go run "$REPO_ROOT/hack/ci-extract-task" -inputs "$WORK/src/deploy/concourse-pipeline.yml" "$job")

  if [ ${#inputs[@]} -eq 0 ]; then
    VERDICTS+=("$job: SKIPPED (task declares no inputs; nothing to upload)")
    FAILED=1
    break
  fi

  if ! vars_for "$cfg" "$job"; then
    VERDICTS+=("$job: SKIPPED (unresolved pipeline vars)")
    FAILED=1
    break
  fi

  log "executing '$job' ($(basename "$cfg"))"

  status=0
  # --include-ignored: the materialised tree is not a git repo, so fly's
  # `git ls-files` probe there is meaningless. Upload exactly what git archive
  # produced.
  fly -t "$FLY_TARGET" execute \
    -c "$cfg" \
    --include-ignored \
    "${inputs[@]}" \
    ${FLY_VARS+"${FLY_VARS[@]}"} 2>&1 | tee "$WORK/$job.out" || status=1

  build_line="$(grep -m1 '^executing build ' "$WORK/$job.out" || true)"
  build_id="${build_line#executing build }"
  build_id="${build_id%% *}"
  [ -n "$build_id" ] || build_id="?"

  # `fly execute` is not a trustworthy verdict on its own. After the log
  # stream ends it calls ListBuildArtifacts unconditionally -- even for a task
  # with no outputs -- and returns that call's error instead of the build's
  # exit code. One EOF on the port-forward there turns a build that printed
  # "succeeded" into a non-zero fly. So when fly is unhappy but a build exists,
  # ask the build.
  if [ "$status" -ne 0 ] && [ "$build_id" != "?" ]; then
    for _ in 1 2 3; do
      if fly -t "$FLY_TARGET" watch -b "$build_id" >/dev/null 2>&1; then
        log "fly exited non-zero but build $build_id succeeded; taking the build's word"
        status=0
        break
      fi
      sleep 2
    done
  fi

  if [ "$status" -eq 0 ]; then
    VERDICTS+=("$job: PASS (build $build_id)")
  else
    VERDICTS+=("$job: FAIL (build $build_id) -- ${build_line#*at }")
    FAILED=1
    break
  fi
done

echo >&2
log "ci-check $SHA on $FLY_TARGET"
for v in "${VERDICTS[@]}"; do
  printf '    %s\n' "$v" >&2
done

exit "$FAILED"
