#!/usr/bin/env bash
#
# ci-check.sh -- run the real CI tasks against a ref, before offering it for merge.
#
# The local test tiers run on macOS. CI runs on Linux, in the
# registry.home/concourse-test-runner:v10 image, on the theborg cluster. Two
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
#   hack/ci-check.sh [--affected[=base]] [ref] [job ...]
#
#   --affected  the inner loop: run only unit-tests, and only over the
#               packages whose tests the change since `base` (default
#               origin/core, merge-base semantics) can reach -- see
#               hack/ci-affected. A change it cannot bound (go.mod, a file in
#               no Go package) runs the full unit tier. NOT a merge gate: run
#               the full check before offering the branch.
#
#   ref   git ref to test (default: HEAD)
#   job   pipeline jobs to run, in order (default: build-and-vet unit-tests);
#         on the battle station they run at once
#
# Environment:
#   FLY_TARGET   fly target to execute against (default: home)
#   KUBECONFIG   defaults to $HOME/.kube/config
#   PORT_FORWARD_NS / PORT_FORWARD_SVC / FLY_PORT  where to point the tunnel
#   BATTLE_STATION        auto (default) or off
#   BATTLE_STATION_LABEL  node label of the big CI node
#                         (default: jetbridge.dev/battle-station=true)
#   BATTLE_STATION_MIN_CPU  smallest per-job boost worth taking, millicores
#                         (default: 9000)
#
# Battle station. When the node carrying BATTLE_STATION_LABEL is Ready, not
# cordoned, has a ready artifact daemon and enough unrequested CPU, the jobs run
# in PARALLEL, each with its container_requests.cpu raised to an equal share of
# that free CPU (at least BATTLE_STATION_MIN_CPU, which is more than theborg
# can offer, so the boosted pods land on the battle station). Their output goes
# to a log per job; a failing job's tail is printed and its full log kept.
# Otherwise -- the desktop asleep, rebooting or cordoned for a game -- the jobs
# run one after another with the pipeline's own requests, exactly as before.
# Either way the summary reports each job's wall time and the total.
#
# The tunnel is opened only when the target's API is 127.0.0.1:$FLY_PORT (a
# port-forwarded target); `home` reaches concourse.home directly. Two runs at
# once share a tunnel: whichever one opened it takes it down on exit, and the
# other loses its log stream mid-build. The build itself survives on the
# cluster -- re-attach with `fly -t <target> watch -b <id>`.
#
# Jobs whose task config interpolates pipeline vars (`((name))`) need those
# values passed explicitly -- `fly execute` has no credential manager behind it.
# Export the var name uppercased with dashes as underscores (github-token ->
# GITHUB_TOKEN) and this script forwards it with `-v`. Missing ones are named
# and the run stops before it burns a build.

set -euo pipefail

FLY_TARGET="${FLY_TARGET:-home}"
FLY_PORT="${FLY_PORT:-18080}"
PORT_FORWARD_NS="${PORT_FORWARD_NS:-cicd}"
PORT_FORWARD_SVC="${PORT_FORWARD_SVC:-svc/concourse-web}"
export KUBECONFIG="${KUBECONFIG:-$HOME/.kube/config}"
BATTLE_STATION="${BATTLE_STATION:-auto}"
BATTLE_STATION_LABEL="${BATTLE_STATION_LABEL:-jetbridge.dev/battle-station=true}"
# Above theborg's unrequested CPU (~8.8 cores of 12), so a boosted one-off
# cannot fit there: it runs on the battle station or waits.
BATTLE_STATION_MIN_CPU="${BATTLE_STATION_MIN_CPU:-9000}"

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

AFFECTED_BASE=""
case "${1:-}" in
  --affected) AFFECTED_BASE="origin/core"; shift ;;
  --affected=*) AFFECTED_BASE="${1#--affected=}"; shift ;;
esac

REF="${1:-HEAD}"
if [ $# -gt 0 ]; then shift; fi
if [ $# -gt 0 ]; then
  JOBS=("$@")
elif [ -n "$AFFECTED_BASE" ]; then
  JOBS=(unit-tests)
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

target_is_tunnelled() {
  local api
  api="$(fly targets 2>/dev/null | awk -v t="$FLY_TARGET" '$1 == t { print $2 }')"
  case "$api" in
    http://127.0.0.1:"$FLY_PORT" | http://localhost:"$FLY_PORT") return 0 ;;
    *) return 1 ;;
  esac
}

ensure_tunnel() {
  if ! target_is_tunnelled; then
    return
  fi
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

AFFECTED_PKGS=""
if [ -n "$AFFECTED_BASE" ]; then
  if ! affected="$(cd "$REPO_ROOT" && go run ./hack/ci-affected -tree "$WORK/src" "$AFFECTED_BASE" "$SHA")"; then
    log "could not compute the affected packages"
    exit 1
  fi
  if [ "$affected" = ALL ]; then
    log "affected since $AFFECTED_BASE: every package (full unit tier)"
  elif [ -z "$affected" ]; then
    log "affected since $AFFECTED_BASE: no Go package; nothing to run"
    exit 0
  else
    AFFECTED_PKGS="$(tr '\n' ' ' <<<"$affected")"
    AFFECTED_PKGS="${AFFECTED_PKGS% }"
    log "affected since $AFFECTED_BASE: $(wc -l <<<"$affected" | tr -d ' ') package(s)"
    sed 's/^/      /' <<<"$affected" >&2
  fi
fi
log "jobs: ${JOBS[*]}"

# Prepare one job: its task config, the inputs to upload, whether the step is
# privileged, and its pipeline vars. Everything lands in $WORK/<job>.* so a run
# can happen later, possibly in the background. Prints a SKIPPED reason and
# returns 1 if the job cannot be run.
prepare_job() {
  local job="$1" cpu_request="$2" cfg="$WORK/$1.yml"
  local extract=(go run "$REPO_ROOT/hack/ci-extract-task")

  # The task config comes from the *ref*, not from the working tree: a branch
  # that changes its own CI task should be checked with the task it ships.
  if [ -n "$cpu_request" ]; then
    extract+=(-cpu-request "$cpu_request")
  fi
  if [ "$job" = unit-tests ] && [ -n "$AFFECTED_PKGS" ]; then
    extract+=(-param "UNIT_PACKAGES=$AFFECTED_PKGS")
  fi
  if ! "${extract[@]}" "$WORK/src/deploy/concourse-pipeline.yml" "$job" >"$cfg"; then
    echo "$job: SKIPPED (could not extract task config)"
    return 1
  fi

  : >"$WORK/$job.args"
  while IFS= read -r in_name; do
    [ -n "$in_name" ] || continue
    printf '%s\n' -i "$in_name=$WORK/src" >>"$WORK/$job.args"
  done < <(go run "$REPO_ROOT/hack/ci-extract-task" -inputs "$WORK/src/deploy/concourse-pipeline.yml" "$job")

  if [ ! -s "$WORK/$job.args" ]; then
    echo "$job: SKIPPED (task declares no inputs; nothing to upload)"
    return 1
  fi

  # `privileged: true` lives on the STEP, outside the config fly is given, and
  # fly execute only applies it as -p. Dropping it turned the brine job's
  # namespace launcher into an EPERM that looked like a branch failure.
  if [ "$(go run "$REPO_ROOT/hack/ci-extract-task" -privileged "$WORK/src/deploy/concourse-pipeline.yml" "$job")" = "true" ]; then
    printf '%s\n' -p >>"$WORK/$job.args"
    log "'$job' runs privileged"
  fi

  if ! vars_for "$cfg" "$job"; then
    echo "$job: SKIPPED (unresolved pipeline vars)"
    return 1
  fi
  if [ ${#FLY_VARS[@]} -gt 0 ]; then
    printf '%s\n' "${FLY_VARS[@]}" >>"$WORK/$job.args"
  fi
}

# Run one prepared job. Output goes to $WORK/<job>.out and, when $2 is "tee",
# to the terminal too. Writes the verdict to $WORK/<job>.verdict and the wall
# time to $WORK/<job>.secs; returns the job's status.
run_job() {
  local job="$1" mode="$2" status=0 started args=()
  started=$(date +%s)
  while IFS= read -r a; do args+=("$a"); done <"$WORK/$job.args"

  log "executing '$job' ($(basename "$WORK/$job.yml"))"

  # --include-ignored: the materialised tree is not a git repo, so fly's
  # `git ls-files` probe there is meaningless. Upload exactly what git archive
  # produced.
  if [ "$mode" = tee ]; then
    fly -t "$FLY_TARGET" execute -c "$WORK/$job.yml" --include-ignored "${args[@]}" 2>&1 \
      | tee "$WORK/$job.out" || status=1
  else
    fly -t "$FLY_TARGET" execute -c "$WORK/$job.yml" --include-ignored "${args[@]}" \
      >"$WORK/$job.out" 2>&1 || status=1
  fi

  local build_line build_id
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
  #
  # 300be0b841 fixed that in fly, so this fallback may become unnecessary --
  # but only once every operator's fly binary carries the fix, and those lag
  # core. Keep it until then; it costs one `watch` on an already-failed run.
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

  local secs=$(( $(date +%s) - started ))
  echo "$secs" >"$WORK/$job.secs"
  if [ "$status" -eq 0 ]; then
    echo "$job: PASS (build $build_id, $(fmt_secs "$secs"))" >"$WORK/$job.verdict"
  else
    echo "$job: FAIL (build $build_id, $(fmt_secs "$secs")) -- ${build_line#*at }" >"$WORK/$job.verdict"
  fi
  return "$status"
}

fmt_secs() { printf '%dm%02ds' $(( $1 / 60 )) $(( $1 % 60 )); }

# Millicores from a Kubernetes CPU quantity ("28", "1.5", "250m").
to_millicores() {
  awk '{ q = $1; if (q ~ /m$/) { sub(/m$/, "", q); print int(q) } else if (q != "") print int(q * 1000) }'
}

# The battle station is the big CI node (label $BATTLE_STATION_LABEL). It is a
# desktop that sleeps, reboots for updates, and is cordoned while its owner
# games, so "is it there" is asked every run, not assumed. Prints the per-job
# CPU request (millicores) to boost to when it is Ready, schedulable, carries a
# ready artifact daemon, and has room for every job at once at no less than
# $BATTLE_STATION_MIN_CPU each; prints nothing otherwise, and logs why.
battle_station_boost() {
  local njobs="$1" nodes name unsched ready cache alloc requested free per
  if [ "$BATTLE_STATION" = off ]; then
    log "battle station: off (BATTLE_STATION=off)"
    return
  fi
  if ! nodes="$(kubectl get nodes -l "$BATTLE_STATION_LABEL" --request-timeout=10s -o jsonpath='{range .items[*]}{.metadata.name}|{.spec.unschedulable}|{.status.conditions[?(@.type=="Ready")].status}|{.metadata.labels.concourse\.dev/artifact-cache}{"\n"}{end}' 2>/dev/null)"; then
    log "battle station: cannot read nodes with kubectl; running as usual"
    return
  fi
  if [ -z "$nodes" ]; then
    log "battle station: no node labelled $BATTLE_STATION_LABEL; running as usual"
    return
  fi
  IFS='|' read -r name unsched ready cache <<<"$(head -1 <<<"$nodes")"
  if [ "$ready" != True ]; then
    log "battle station: $name is not Ready (asleep or off?); running as usual"
    return
  fi
  if [ "$unsched" = true ]; then
    log "battle station: $name is cordoned; running as usual"
    return
  fi
  if [ "$cache" != ready ]; then
    log "battle station: $name has no ready artifact daemon; running as usual"
    return
  fi

  alloc="$(kubectl get node "$name" --request-timeout=10s -o jsonpath='{.status.allocatable.cpu}' | to_millicores)"
  requested="$(kubectl get pods -A --request-timeout=10s \
    --field-selector "spec.nodeName=$name,status.phase!=Succeeded,status.phase!=Failed" \
    -o jsonpath='{range .items[*]}{range .spec.containers[*]}{.resources.requests.cpu}{"\n"}{end}{end}' \
    | to_millicores | awk '{ s += $1 } END { print s + 0 }')"
  # Leave a core for whatever lands between this read and the submit.
  free=$(( alloc - requested - 1000 ))
  per=$(( free / njobs / 1000 * 1000 ))
  if [ "$per" -lt "$BATTLE_STATION_MIN_CPU" ]; then
    log "battle station: $name has ${free}m CPU free, under ${BATTLE_STATION_MIN_CPU}m for each of $njobs job(s); running as usual"
    return
  fi
  log "battle station: $name is live with ${free}m CPU free; running $njobs job(s) in parallel at ${per}m each"
  echo "$per"
}

declare -a VERDICTS=()
FAILED=0
RUN_STARTED=$(date +%s)
BOOST="$(battle_station_boost "${#JOBS[@]}" || true)"

if [ -n "$BOOST" ]; then
  # Prepare every job first: a SKIP should stop the run before any build burns.
  for job in "${JOBS[@]}"; do
    if ! skip="$(prepare_job "$job" "$BOOST")"; then
      VERDICTS+=("$skip")
      FAILED=1
      break
    fi
  done

  if [ "$FAILED" -eq 0 ]; then
    # One job has nothing to interleave with: stream it like the old path.
    mode=quiet
    [ "${#JOBS[@]}" -gt 1 ] || mode=tee
    declare -a PIDS=()
    for job in "${JOBS[@]}"; do
      run_job "$job" "$mode" &
      PIDS+=($!)
    done
    for i in "${!JOBS[@]}"; do
      job="${JOBS[$i]}"
      if wait "${PIDS[$i]}"; then
        log "$job finished: $(cat "$WORK/$job.verdict")"
      else
        FAILED=1
        log "$job FAILED -- last 40 lines:"
        tail -40 "$WORK/$job.out" >&2
        kept="${TMPDIR:-/tmp}/ci-check-${SHA:0:10}-$job.log"
        cp "$WORK/$job.out" "$kept"
        log "full log: $kept"
      fi
      VERDICTS+=("$(cat "$WORK/$job.verdict")")
    done
  fi
else
  for job in "${JOBS[@]}"; do
    if ! skip="$(prepare_job "$job" "")"; then
      VERDICTS+=("$skip")
      FAILED=1
      break
    fi
    status=0
    run_job "$job" tee || status=1
    VERDICTS+=("$(cat "$WORK/$job.verdict")")
    if [ "$status" -ne 0 ]; then
      FAILED=1
      break
    fi
  done
fi

echo >&2
log "ci-check $SHA on $FLY_TARGET in $(fmt_secs $(( $(date +%s) - RUN_STARTED )))${BOOST:+ (battle station, ${BOOST}m per job)}"
for v in "${VERDICTS[@]}"; do
  printf '    %s\n' "$v" >&2
done

exit "$FAILED"
