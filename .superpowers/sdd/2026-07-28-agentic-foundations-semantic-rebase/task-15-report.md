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
