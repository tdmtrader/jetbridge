# Task 15 report — durable checkpoint and execution-attempt models

## Behavior

Adds immutable checkpoint-head provenance, canonical Hangar checkpoint-object
identity, staged-to-committed generation CAS records, append-only events,
monotonic begun-to-committed effects, and fresh execution-attempt rows with
current-attempt and fence-token constraints. A begun external effect is
classified as manual-review-required; no provider resume, workspace restore,
retention orchestration, telemetry, or workflow-run terminalization is added.

## Files and migrations

- `agent/checkpoint`: identity, manifest, archive identity, effect/event,
  attempt, fence, and conservative recovery-policy contracts plus fakes/tests.
- `atc/db/agent_run_{checkpoints,attempts,events}_factory.go`: PostgreSQL
  persistence authority and focused coverage.
- `1773106144`: checkpoint heads/objects/generations/effects/events.
- `1773106145`: durable fresh execution attempts and fence-token ledger.
- Migration preflight and legacy-upgrade head pointers now equal `1773106145`.

## Coverage and verification

- Adapted foundations checkpoint, event, attempt, DB factory, and migration
  coverage to migrations 6144/6145 and the v3 workflow-run identity.
- Passed: `GOCACHE=/private/tmp/concourse-task15-gocache go test ./agent/checkpoint -count=1`.
- Passed: `GOCACHE=/private/tmp/concourse-task15-gocache go test ./agent/checkpoint ./atc/db -run '^$' -count=1`.
- Attempted once: focused serial `ginkgo --focus='AgentRun(Checkpoints|Attempts|Events)Factory|agent run (checkpoints|attempts)' ./atc/db ./atc/db/migration`.
  It ran zero specs because postgresrunner failed during initdb with sandbox-denied
  SysV shared memory (`shmget: Operation not permitted`).

## Self-review and concerns

The models intentionally retain copied v3 workflow-run identity without a
foreign-key delete action and do not call workflow-run finalization. Runtime
exact-node interruption plumbing and owner metadata are not expanded here to
avoid touching the already Human-Review-Required private-mount/Jetbridge seam;
the model accepts only the bounded interruption reasons. No external review
round has been performed. Implementation commit: `add954b863`.

## Pre-review verification fix

- Failure: a host-focused `AgentRunAttemptsFactory` run executed 26 specs and
  failed the typed-interruption replacement case at line 241 with `sql:
  expected 23 destination arguments in Scan, not 22`.
- Root cause hypothesis confirmed by complete column comparison: the
  replacement-attempt `INSERT ... RETURNING` list emitted its copied
  `workflow_run_id` placeholder twice, while `scanAgentRunAttempt` and the
  working initial/current-attempt SELECT paths have exactly one workflow-run
  column. That made the replacement row return 23 columns for 22 scan targets.
- Change: removed only the duplicate `$10::bigint` expression from the
  replacement `RETURNING` list.
- Commands: the exact `ginkgo --focus='allocates exactly one replacement per
  typed interruption without consuming retries' ./atc/db` rerun was attempted,
  but postgresrunner was sandbox-blocked before its one selected spec by SysV
  `shmget: Operation not permitted`. Non-PostgreSQL verification follows in
  the correction commit evidence.
- Correction commit: `a7e9eec0bc fix(agent): align recovery attempt scan columns`.

## Pre-review integration correction — workflow-run dual identity

- Failure/root cause: the rebase collapsed immutable
  `workflow_run_provenance_id` and nullable live `workflow_run_id` into one
  unfenced scalar. This broke the existing migration behavior: workflow-run
  deletion must null only the live FK while retaining recovery attribution.
- Change: restored the immutable copied provenance column, nullable live FK
  with `ON DELETE SET NULL`, equality constraint while live, and trigger that
  freezes provenance/build/plan/function but permits the FK nulling update.
  Factory head/attempt inserts, SELECT/RETURNING lists, and scanners now carry
  both values and construct public checkpoint identity from provenance only.
  The existing migration coverage again proves immutable attribution survives
  workflow-run deletion while the live link becomes null.
- Verification: `GOCACHE=/private/tmp/concourse-task15-gocache go test
  ./agent/checkpoint ./atc/db ./atc/db/migration -run '^$' -count=1` and
  `GOCACHE=/private/tmp/concourse-task15-gocache go test ./agent/checkpoint
  -count=1` passed; `git diff --check` passed. The serial PostgreSQL focus was
  not rerun locally because its prior exact attempt is sandbox-blocked at
  postgresrunner SysV shared-memory initialization; parent host verification is
  required.
- Correction commit: `d5d591b88b fix(agent): preserve checkpoint workflow provenance`.

## Runtime interruption classification and attempt container ownership

The remaining Task 15 runtime/owner acceptance area is now implemented in
`0b37ddd6a4 feat(agent): classify runtime pod interruptions`.

### Scope

- `runtime.InterruptionError` carries one bounded reason
  (`pod_deleted`, `evicted`, `node_lost`, or `preempted`), preserves its cause,
  and remains runtime-retryable without authorizing agent replay.
- Jetbridge classifies only authoritative Kubernetes state: a Deleted event or
  API NotFound; terminal `PodFailed` reasons `Evicted`, `NodeLost`, or
  `Shutdown`; and terminal `DisruptionTarget` condition reasons. The explicit
  `concourse-ci.org/preemption-notice=true` annotation is preemption evidence
  only when the pod was actually deleted. Free-form messages, a mere notice,
  API timeouts, SPDY/transport errors, command exits, cancellation, and step
  timeouts do not become interruption classifications. Active exec transport,
  stream-input, and output-upload failures re-fetch pod state and classify
  only a terminal structured status or API NotFound; caller cancellation wins
  over a concurrent typed interruption.
- `db.NewAgentAttemptContainerOwner` preserves the legacy build/plan/team
  owner columns for attempt one. Later attempts retain those columns and use a
  deterministic SHA-256-derived `agent-attempt-` handle; attempt-one lookup
  excludes those later deterministic handles, so lifecycle GC still follows
  build ownership.
- No automatic retry/restart, checkpoint capture/restore, provider resume,
  protected-mount change, or log-stream refactor was added. The sole
  `atc/exec/agent_step.go` exception is required fail-closed translation:
  after normal flight-recorder ingestion, a typed interruption emits the
  bounded `manual_review_required` delegate event and returns `(false, nil)`
  so `RetryErrorStep` cannot convert the runtime-retryable error into an
  unsafe replacement run. The four-reason regression proves it.

### RED / GREEN evidence

- RED: `GOCACHE=/private/tmp/concourse-task15-runtime-gocache go test
  ./atc/runtime -run TestInterruptionErrorPreservesCauseAndIsRetryable
  -count=1` failed because `NewInterruptionError` and
  `InterruptionNodeLost` were undefined.
- RED: `GOCACHE=/private/tmp/concourse-task15-runtime-gocache go test
  ./atc/worker/jetbridge -run
  'TestInterruptionReasonForPodUsesOnlyTerminalStructuredKubernetesState|TestPreferContextCancellationOverInterruption'
  -count=1` failed because the interruption types and classifier/cancellation
  helpers were undefined.
- RED: `GOCACHE=/private/tmp/concourse-task15-runtime-gocache go test
  ./atc/db -run '^$' -count=1` failed because
  `db.NewAgentAttemptContainerOwner` was undefined.
- RED: `GOCACHE=/private/tmp/concourse-task15-runtime-gocache go test
  ./atc/worker/jetbridge -run
  TestInterruptionErrorForPodFailureRequiresAuthoritativePodLoss -count=1`
  failed because `interruptionErrorForPodFailure` was undefined.
- GREEN: `GOCACHE=/private/tmp/concourse-task15-runtime-gocache go test
  ./atc/worker/jetbridge -run
  'TestInterruptionReasonForPodUsesOnlyTerminalStructuredKubernetesState|TestPreferContextCancellationOverInterruption|TestInterruptionErrorForPodFailureRequiresAuthoritativePodLoss'
  -count=1` passed.
- GREEN: `GOCACHE=/private/tmp/concourse-task15-runtime-gocache go test
  ./atc/runtime ./atc/db ./atc/worker/jetbridge -run '^$' -count=1` passed.
- RED: `GOCACHE=/private/tmp/concourse-task15-runtime-gocache ginkgo
  --focus='when process.Wait reports an interruption' ./atc/exec` ran four
  specs and failed all four because each typed interruption escaped as the
  retryable runtime error.
- GREEN: the same focused `ginkgo` command passed all four
  (`pod_deleted`, `evicted`, `node_lost`, and `preempted`); the fail-closed
  translation runs after flight-recorder ingestion and before the retry
  wrapper can observe a retryable error.
- GREEN: `git diff --check` passed.
- Not run: full Jetbridge Ginkgo suite remains intentionally out of scope
  because the known Task 6 zero-private-mount regression fails unrelated specs.
  The new DB lifecycle/GC focus could not run locally because `pg_isready`
  returned `/tmp:5432 - no response`. Parent host verification subsequently
  passed: `ginkgo --procs=1 --focus='AgentAttemptContainerOwner' ./atc/db`
  ran 2/2 focused specs green.
