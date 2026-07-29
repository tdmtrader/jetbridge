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
round has been performed. Commit: pending.
