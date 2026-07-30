# Task 8b authority sealing and wiring report

## Decisions

- The broker-to-ATC seal DTO now carries only an execution ID and JSON candidate
  body. ATC derives the result type from the durable tool, subjects from the
  immutable scoped input references, and static-review/tests-not-run provenance
  from the tool; profile and provider/model claims are not accepted from the
  sidecar.
- `OrdinaryResultSealer` uses `contracts.NormalizeRawRecordBody`,
  `contracts.NewRecord`, and the shared `snapshot.SnapshotCreator`. It emits a
  one-file canonical tar (`record.json`) through the ordinary sealing path and
  binds the returned snapshot before transitioning the execution to success.
- The read-only inspection route is team-scoped and omits input digests,
  prompts, native transcript details, credentials, capabilities, and provider
  responses.
- Global routes are registered for the capability-authenticated internal
  authority surface and normal team-authorized inspection. When enabled, ATC
  reads a raw 32-byte HMAC key, composes matching signer/verifier instances and
  the ordinary sealer, and runs bounded expired-lease reconciliation.

## Focused evidence

Passed on 2026-07-30:

```
go test ./agent/broker ./agent/broker/transport ./atc/api/agentchildexecutions -count=1
go test ./atc -run AgentChildExecutionRoutes -count=1
go test ./atc/api/agentchildexecutions -run 'Inspection|OrdinaryResultSealer' -count=1
go test ./atc/wrappa ./atc/atccmd ./atc/api ./atc -run 'AgentChild|Broker' -count=1
git diff --check
```

## Replay repair

The unshipped child ledger now persists the sealed snapshot type/digest and
result body. A duplicate terminal admission returns the durable success result
or the same safe terminal failure before attachment, credential, or harness
work. Nonterminal duplicates are rejected as busy rather than receiving a new
execution capability. Seal replay returns the exact persisted reference only
when the candidate body matches.

The snapshot creation and child-execution binding are separate durable systems;
the service remains seal-before-success. The ordinary sealer uses the stable
parent attempt, node plan, and idempotency key as the snapshot occurrence, so a
retry after a binding failure asks the ordinary creator to recover the same
seal before retrying the DB success transition. A future operator repair pass
could make that recovery observable without changing the authority boundary.

## Final review corrections

- The capability-signed child scope now includes a positive workflow
  definition ID paired with the run ID. Verification compares every sealing
  authority field: team and build identity, snapshot actor, workflow
  definition/run, node/attempt/broker/lease, and each exact immutable input
  reference. HMAC tests reject definition, team-name, build, and input drift.
- `OrdinaryResultSealer` passes the exact server-owned definition/run pointers
  to `snapshot.SealRequest`; its test double invokes the real request
  validation before accepting the call.
- Migration coverage now checks the durable replay columns and their terminal
  constraints on upgrade, as well as the down-migration's snapshot constraint
  shape. The factory coverage drives a real child through success and reads
  back its exact snapshot reference, normalized body, observed usage, and
  duration; the factory rejects contradictory result snapshot IDs before SQL.

Focused non-PostgreSQL evidence on 2026-07-30:

```text
go test ./atc/api/agentchildexecutions -count=1
go test ./atc/db ./atc/db/migration -run '^$' -count=1
git diff --check
```

The single allowed serial PostgreSQL attempt was infrastructure-blocked before
any specs ran: `initdb` failed with `could not create shared memory segment:
Operation not permitted` (`shmget(..., size=56, 03600)`). It was not retried.
