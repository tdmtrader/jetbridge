# Agentic foundations semantic rebase — session operating context

Read this file at the start of every new root-agent or subagent session working
on this track.

## Why this context exists

This work was initially described as a rebase, but it became a semantic
reconstruction against the substantially changed Jetbridge v3 architecture.
The implementation plan contains 19 tasks and the current branch already spans
more than 160 files. Unbounded implementation/review cycles made the work much
slower and more token-intensive than intended.

The goal from this point forward is bounded, reviewable integration—not
unlimited hardening or adjacent platform design.

## Workspace and branch

- Work only in:
  `/Users/tdmtrader/concourse/concourse/.worktrees/agentic-platform-rebase`
- Branch: `codex/agentic-platform-rebase`
- Preserve the old foundations worktree as reference only.
- Do not push, merge, open a PR, or modify the main checkout unless the user
  explicitly requests it.
- Preserve unrelated/user changes. Never discard a dirty worktree to obtain a
  clean state.

## Model and agent policy

- Prefer `gpt-5.6-terra` for implementation, investigation, and review agents.
- Reserve `gpt-5.6-sol` for a genuinely complex blocker that Terra could not
  resolve, or when the user explicitly asks for Sol.
- Use one implementation agent per shared-worktree task.
- Use concise task briefs and limited context forks. Do not fork the full
  transcript when a small written brief is sufficient.
- Do not create parallel reviewers for the same delta.

## Scope discipline

- Implement only the current task's explicit acceptance criteria.
- Do not turn a rebase fix into a new general platform abstraction unless the
  task cannot be made correct without it.
- When an adjacent improvement is useful but not blocking correctness,
  security, migration safety, or the current acceptance test, record it in
  `2026-07-28-agentic-foundations-semantic-rebase-deferred-items.md` and
  continue.
- Do not expand a task merely to make every related package ideal.
- Known unrelated failures must be documented, not opportunistically repaired
  inside the current task.

## Review budget

- Fix only Critical, High, or otherwise genuinely blocking findings:
  correctness failures, security-boundary violations, data loss/corruption,
  migration hazards, or a required acceptance test that cannot pass.
- Record Medium, Minor, cleanup, optimization, speculative hardening, and
  additional-nice-to-have coverage in the deferred-item catalog.
- A task gets at most **two review rounds total**:
  1. initial focused review;
  2. one focused re-review of blocking fixes.
- If a blocking finding remains or a new blocker appears after the second
  round, mark the task **Human Review Required**, record the exact evidence and
  proposed choices, and proceed to the next safe independent task.
- Review only the relevant delta. Do not repeatedly re-audit already approved
  history without new evidence that it regressed.

### Task 6 exception at this checkpoint

Superseded by explicit user approval on 2026-07-29 to reopen Task 6 for at
most three additional iterations. Task 6 was accepted after two: the lifecycle
fix and one test-only compatibility correction. Do not reopen it again without
new blocking evidence.

## Test budget

- Treat this as a semantic rebase/integration, not greenfield construction.
  Existing or ported behavioral tests that fail because approved behavior is
  absent are sufficient implementation guidance; do not add a second
  artificial failing test merely to demonstrate RED.
- Reuse and adapt behavioral coverage from the foundations reference when it
  still expresses the approved contract on the current architecture.
- Add a new test only for a clear load-bearing behavioral gap that existing
  coverage does not exercise. Record nonblocking coverage gaps rather than
  expanding the current task.
- Use focused unit/integration tests while implementing.
- Run broad package suites once at the task checkpoint, not after every edit.
- Run the repository-wide acceptance suite only at a milestone or before a
  requested integration handoff.
- PostgreSQL-backed suites run serially because they share fixed ports.
- Do not spend repeated cycles diagnosing known sandbox-only infrastructure
  failures. Record them once with the narrower passing evidence.
- Borg may be used for resource-intensive cluster tests when reachable, but do
  not publish source/images or make unrelated external changes solely to make a
  test possible.

## Current checkpoint

Completed and independently approved:

- Task 1 — direct snapshot team ownership
- Task 2 — remove principals
- Task 3 — retained dev-capability core
- Task 4 — compiled validation authority
- Task 5 — validation/v1 revision 3 provenance

Completed branch-wide compatibility repair:

- Merge-preflight revision-3 provenance and server seal authority were repaired
  in `a2d5b939a4` and its workflow-identity correction `cd85882823`.
- The bounded review accepted the repair in round 2 with no remaining findings.

Task 6:

- Accepted through `a57a04f027`, lifecycle correction `5f87d2671c`, and
  test-only compatibility correction `477c4d554a`.
- Ambiguous Pod Create outcomes retain authority unless a fresh exact-name read
  proves absence; owner-bound reaping uses the observed Secret UID; zero mounts
  do not require a fake Pod UID.
- The unmasked typed-interruption assertions now check
  `runtime.InterruptionError`/`runtime.InterruptionEvicted`.
- Focused regressions and a fresh host-access full Jetbridge package passed
  381/381. Two blocking-only reviews passed. `HUMAN-REVIEW-001` and
  `DEPENDENCY-001` are resolved.

Task 8:

- Completed in `bb567a16c5` with the blocking correction
  `fc31f229d8`.
- Status: **Accepted** in review round 2.
- Fresh checkpoint passed:
  `go test ./agent/outputbuilder ./cmd/agent-output ./agent/snapshot/...
  -count=1`, plus the agent-runner Dockerfile contract.
- The builder consumes only fixed read-only authority, uses bounded streaming
  and retained filesystem roots, reopens exact canonical inputs, publishes
  `record.json` last with ordinary-error rollback, and never seals.
- `DEFERRED-003` records durable fsynced crash-recovery journaling; it is
  nonblocking for the current authoring/preflight boundary.

Task 7 remains unstarted but is no longer blocked by Task 6.

Task 9:

- Not started; `DEPENDENCY-001` is resolved.
- The current runtime has no safe independent server-owned file seam for a
  managed sidecar. Task 9 must extend Task 6's protected private-Secret mount,
  now accepted and green, without exposing the authority to arbitrary
  sidecars.

Task 10:

- Completed in `29e5215b13` with evidence commits `705dcf1d37` and
  `3e1e2c6cfc`.
- Status: **Accepted** in independent blocking-only review round 1.
- Focused package tests passed, and the exact
  `make test-hangar-integration` target passed against a temporary Borg
  fake-gcs-server deployment. The namespace was deleted and its absence
  verified.
- The core provides provider-neutral immutable storage, canonical SHA-256
  identity over uncompressed bytes, bounded zstd representation, GCS
  create-only/idempotent semantics, and generation-pinned verification.
- `DEFERRED-004` is resolved.

Task 11:

- Completed in `685d09104e` with cache-root recovery correction
  `979ca6490a`.
- Status: **Accepted** in blocking-only review round 2.
- The daemon durably commits verified bytes before successful PUT, restores
  from generation-pinned Hangar content after complete cache-root loss, keeps
  one canonical `hangar-v1` location, safely handles legacy cache locations,
  and does not proactively peer-mirror agentic snapshots.
- Focused daemon/Jetbridge/ATC/chart suites and Helm lint passed. Task 10's
  accepted Borg emulator run remains the production GCS-adapter evidence.
- `DEFERRED-005` records unreachable-object retention after a narrow
  post-Hangar/pre-location failure window. `DEFERRED-006` records that narrow
  Hangar egress is enforced only when the optional daemon egress policy is
  enabled.

Task 12:

- Implemented in `2d160f6c89` with the single correction
  `f9de627f40`.
- Status: **Human Review Required** after review round 2.
- Admission provenance is now derived from selecting-build inputs, archive and
  create serialize on the owner row, and focused DB lifecycle/provenance specs
  pass.
- Residual High: `RenderedFunction.Config` remains directly available and
  `BindExecutionParams` is optional, so no launch seam requires the exact
  validated source-reference envelope. See `HUMAN-REVIEW-002`.

Task 13:

- Not started; dependency-deferred under `DEPENDENCY-002` because its runtime
  capture/reuse path would build directly on Task 12's unresolved launch
  boundary.

Task 14:

- Not started; dependency-deferred under `DEPENDENCY-003` because the design
  requires accepted exact validation gates before replacing the publisher
  gateway, while Tasks 6–7 remain Human Review Required/dependent.

Task 15:

- Implemented through `aafd924521`.
- Status: **Accepted** after the user-authorized stale-time correction and one
  final scoped Terra review.
- Host serial verification passed 28/28 checkpoint/attempt DB specs and 2/2
  migration specs. Recursive checkpoint/fake packages, runtime interruption
  checks, AgentStep interruption behavior, and affected-package compilation
  also passed.
- The correction durably binds staged checkpoints/effects to exact fence
  tokens, verifies and relationally pins same-head recovery checkpoints, and
  repairs the checkpoint fake.
- `HUMAN-REVIEW-003` is resolved. Fence authority now samples PostgreSQL
  `clock_timestamp()` after the current-attempt row lock is acquired. A
  PID-observed concurrency regression proved the old code incorrectly
  authorized a transaction that waited past expiry and that the corrected code
  returns `ErrStaleFence`.

Tasks 16–18:

- Task 16 is accepted through `91d8e47fb0`: bounded safe-boundary capture,
  exact-node daemon/Hangar commit, lifecycle integration, and completion
  capture passed focused verification and two bounded reviews.
- Task 17 is accepted through `1f6e9f67d0`: exact retained source restore into
  a fresh durable attempt, capability-gated native resume, workspace-only and
  checkpoint-zero fallback, reconstruction guidance, and fail-closed manual
  review passed focused verification and one Terra blocking-only review.
- Task 18 is accepted at `986f1e591d`: exact-attempt metrics/transcripts,
  atomic aggregate/ledger deltas, active-source retention, terminal cleanup,
  bounded recovery telemetry/alerts, migrations 1773106146-47, and the
  recovery operations guide passed affected DB/migration/package/chart
  verification and one Terra blocking-only review.

Task 19:

- Not started; dependency-deferred under `DEPENDENCY-005`.
- Final end-to-end and residue proof cannot complete while Tasks 6 and 12
  remain Human Review Required, their dependent tasks remain unstarted, and
  the branch is knowingly incomplete. Tasks 16–18 are no longer blockers.

## Milestone verification

Fresh verification at the current checkpoint:

- `make test-dev-mcp`: passed.
- `make test-fly-integration`: 666/666 passed.
- `make test-integration`: 24/24 passed.
- `helm lint deploy/chart`: passed with informational image-value and icon
  messages only.
- Focused merge-preflight revision-3 suites passed.
- The earlier `make test-unit` failure was isolated to the Task 6 zero-mount
  regression. After correction, a fresh host-access
  `go test ./atc/worker/jetbridge -count=1` passed all 381 specs. The complete
  repository-wide `make test-unit` target has not yet been rerun.

The recovery track is verified, but the branch is not merge-ready. Do not
report it as green until Task 12 and the remaining dependent tasks are
implemented and the broad suites are rerun.

## Near-term sequence

1. Treat Task 6 as accepted; do not reopen it without new blocking evidence.
2. Tasks 7 and 9 are unblocked but remain unstarted.
3. Resolve Task 12's mandatory source-binding launch boundary next.
4. Leave Tasks 13–14 at their documented dependency boundaries until their
   prerequisites are accepted.
5. Treat Task 15 as **Accepted**; its user-authorized final review found no
   blocking issue.
6. Treat Tasks 16, 17, and 18 as accepted; do not reopen their review cycles
   without new blocking evidence.
7. Leave Task 19 dependency-deferred until the Human Review Required boundaries
   and dependent tasks are resolved.
8. Treat every resumed feature group as a separate bounded track rather than
   one continuous "rebase."

## Session handoff requirement

Before ending a session:

- Update the local SDD `progress.md` with commits, focused test evidence, review
  round count, and accepted/human-review-required state.
- Update the deferred-item catalog for every intentionally deferred finding.
- Leave a precise dirty-worktree note if uncommitted work remains.
- Do not claim completion without current verification evidence.
