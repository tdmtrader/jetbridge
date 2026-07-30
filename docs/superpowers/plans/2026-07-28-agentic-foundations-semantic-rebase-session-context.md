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
- A task gets at most **three review rounds total**:
  1. initial focused review;
  2. a focused re-review of blocking fixes;
  3. one final focused review when the second round finds a blocker.
- Stop early when a round passes. If a blocking finding remains or a new
  blocker appears after the third round, mark the task **Human Review
  Required**, record the exact evidence and proposed choices, and proceed to
  the next safe independent task.
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

Task 7:

- Implemented: governed review, merge-approval await, and repository-change
  publish consumers require an exact authoritative validation/v1 rev3 record.
- Exact candidate/base provenance, profile/config/image/toolchain/workflow
  identity, and passing conclusion are verified before the worker, wait, or
  publisher side effect. Selected-build metadata now propagates workflow
  version from the durable run association.
- A full DB suite rerun remains environment-blocked because port 5434 was
  already occupied before its BeforeSuite; source-level suites and DB
  compile-only verification passed.
- Review round 1 blocking fixes remove post-validation repository-change
  mutation from small-fix/version-upgrade and fail closed malformed nil
  validation authorities before base traversal.
- Status: **Accepted** in bounded review round 2. The re-review found no
  Critical, High, or acceptance-blocking issue. `DEPENDENCY-003` is resolved
  and Task 14 is unblocked.

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

Task 9:

- Implemented in `fb107b1db1`, with trust-boundary corrections
  `210eafcebf` and `9375fae0b8`.
- Status: **Accepted in review round 3**.
- ATC derives the builder's private authority from frozen typed execution
  facts; the fixed pinned Agent sidecar receives only exact typed ports, with
  input projections read-only. Runtime validation rejects malformed authority,
  incidental mounts, and both Kubernetes-name and physical HostPath/PVC
  backing aliases.
- Controller focused packages passed. Review rounds 1 and 2 found and corrected
  the malformed-spec projection and DaemonSet `dir`/`input-1` alias blockers;
  round 3 found no Critical, High, or acceptance-blocking issue.

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

- Implemented in `2d160f6c89`, corrected in `f9de627f40`, and accepted after
  the user-authorized opaque-boundary commit `875d604026`.
- Status: **Accepted** after reopened iteration 1 of at most 3.
- Admission provenance is now derived from selecting-build inputs, archive and
  create serialize on the owner row, and focused DB lifecycle/provenance specs
  pass.
- Binder-to-DB durable workflow launch now requires an opaque
  `workflow.ExecutionEnvelope`. Trusted private canonical/hash/source
  authority survives cloning; public config/hash mutation cannot mint or alter
  it; substitutions and zero/mismatched envelopes fail closed.
- Workflow/workflow-run suites, DB compile, and host PostgreSQL valid/rejection
  specs passed. Independent blocking-only review found no Critical, High, or
  blocking issue. `HUMAN-REVIEW-002` and `DEPENDENCY-002` are resolved.

Task 13:

- Implemented through `e08f37bc4a` with the scheduler-selection authority
  correction `d84dbaeb93`.
- Status: **Accepted in review round 2 of at most 3**.
- Promotion owns one revision-frozen standing Concourse admission pipeline;
  automatic and manual selections persist the exact build/version facts,
  capture them once into sealed Hangar-backed snapshots, and bind ready
  admissions through the mandatory opaque launch envelope. Runs, retries,
  replays, and experiment children reuse the exact admission.
- Public pipeline/job/build/resource-selection mutations fail closed.
  Workflow-run, experiment, ATC, full API, compile-only DB, and focused
  authority checks passed. Focused DB behavior remains unexecuted because of
  the documented external fixed-port collision; no infrastructure retry was
  made during the correction.
- Review round 1's valid lower-level mutation finding was corrected. Its
  migration observation was a false positive: `1773106148` already had the
  required down-migration constraint drop. Fresh round 2 returned PASS with no
  new blocking or deferred finding.

Task 14:

- Implemented through `7629e590d6`; credential-path correction
  `ce227c1abf`.
- Status: **Accepted in review round 2 of at most 3**.
- ATC owns exact policy and opaque destination credentials, materializes
  sealed changes into private Git scratch, and atomically updates the target
  plus idempotency marker after upstream rebase/validation.
- Helm mounts distinct policy and credential Secrets only into
  `concourse-web`; the runtime image supplies the fixed image-owned
  `/usr/bin/git`. The legacy gateway transport, service, flags, and operator
  topology are removed. One fail-closed Helm tombstone rejects the retired
  value rather than silently accepting it.
- Round 1 found inherited `PATH` could select a counterfeit Git executable
  that received askpass state. The correction pinned production to
  `/usr/bin/git`; fresh scoped round 2 found the issue addressed and no new
  blocking defect.

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

- Active. `DEPENDENCY-005` is resolved by accepted Tasks 13 and 14.
- Run the bounded migration, broad-suite, residue, documentation, and
  environment-gated live proof matrix once; do not repeat known
  infrastructure failures.

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

The recovery track and Tasks 6/7/9/12 are verified, but the branch is not
merge-ready. Do not report it as green until Tasks 13, 14, and 19 are
completed and the broad suites are rerun.

## Near-term sequence

1. Treat Task 6 as accepted; do not reopen it without new blocking evidence.
2. Treat Task 12 as accepted through `875d604026`; do not reopen it without
   new blocking evidence.
3. Treat Task 7 as accepted through `7571f5f846`; do not reopen it without new
   blocking evidence.
4. Treat Task 9 as accepted through `9375fae0b8`; do not reopen it without new
   blocking evidence.
5. Treat Task 13 as accepted through `d84dbaeb93`; do not reopen it without
   new blocking evidence.
6. Treat Task 15 as **Accepted**; its user-authorized final review found no
   blocking issue.
7. Treat Tasks 16, 17, and 18 as accepted; do not reopen their review cycles
   without new blocking evidence.
8. Treat Task 14 as accepted through `ce227c1abf`; do not reopen it without
   new blocking evidence.
9. Run Task 19 as the active final verification/residue track.
10. Treat every resumed feature group as a separate bounded track rather than
   one continuous "rebase."

## Session handoff requirement

Before ending a session:

- Update the local SDD `progress.md` with commits, focused test evidence, review
  round count, and accepted/human-review-required state.
- Update the deferred-item catalog for every intentionally deferred finding.
- Leave a precise dirty-worktree note if uncommitted work remains.
- Do not claim completion without current verification evidence.
