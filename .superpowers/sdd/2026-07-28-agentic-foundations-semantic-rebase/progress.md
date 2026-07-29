# SDD ledger — plan: docs/superpowers/plans/2026-07-28-agentic-foundations-semantic-rebase.md

## Baseline

- Branch base: `296ef5dd86676fc48abd62bd1615ec4958da2ddd`
- Design commit: `1792f6d12e`
- Plan commit: `313cf2a484`
- Clean target verification passed:
  - `make test-unit`: 121/121 Ginkgo suites plus `agent/schema` in 13m24s.
  - `make test-dev-mcp`: both retained ci-agent packages passed.
  - `make test-fly-integration`: 675/675 specs passed.
  - `make test-integration`: 26/26 specs passed.
  - `helm lint deploy/chart`: passed with the chart's pre-existing informational
    output.
- PostgreSQL-backed suites run serially because they reserve ports 5434–5442.

## Tasks

- [x] Task 1 — direct snapshot team ownership
  - Commit: `179a57f30e feat(agent): make snapshots team-owned`
  - Verification: focused migration/legacy-upgrade, snapshot factory, feedback,
    and all affected DB consumer suites passed serially.
  - Review: approved; no Critical, Important, or Minor findings.
  - Infrastructure note: final rerun required removing only this session's two
    inactive Fly temp directories and one verified-dead PostgreSQL test
    cluster after the Data volume filled.
- [x] Task 2 — remove principals
  - Commit: `4269baf125 feat(agent): remove retired principal authority`
  - Verification: migration, focused authorization/API/ticket/client/ATC/Fly
    packages, focused Fly integration, and 30 Elm tests passed.
  - Review: approved; no Critical, Important, or Minor findings.
- [x] Task 3 — retained dev-capability core
  - Commits:
    - `20ea788325 feat(devcap): share retained execution core`
    - `b90f1e462c fix(devcap): bind outputs and verify logs`
  - Verification: focused `ci-agent` core, MCP, CLI, deploy-image contract,
    `make test-dev-mcp`, and diff checks passed.
  - Review: approved after fixing no-follow output binding and log
    write/close error propagation. One non-blocking cleanup note remains:
    failed multi-log staging can leave an unpublished hidden temporary
    directory in the task filesystem.
- [x] Task 4 — compiled validation authority
  - Commits:
    - `e54f5e1c6f feat(workflow): freeze dev validation authority`
    - `a61ddc33b3 fix(workflow): bind validation authority operationally`
  - Verification: workflow compilation/rendering, workflow-run admission,
    durable resume/template reuse, dispatch, API strict-parser callers,
    focused authority mutations, and diff checks passed.
  - Review: approved after preserving authority-aware identity through runtime
    image injection and durable reuse, enforcing aggregate asset limits and
    strict nested decoding, and proving hybrid interactive dev-MCP reuse does
    not leak runtime sidecar fields into validation authority.
- [x] Task 5 — validation/v1 rev3 provenance
  - Commits:
    - `e6dea50455 feat(snapshot): attest validation provenance at seal time`
    - `87b34ea1ed fix(agent): close validation provenance review gaps`
    - `fe492018f4 fix(db): preserve validation provenance on retry`
  - Verification: snapshot rev3, workflow-run binding, experiment propagation,
    authoritative-provenance migration upgrades, and both affected PostgreSQL
    factories passed. Retry identity covers nonempty mismatch and both
    legacy-empty crossings while preserving identical provenance.
  - Review: approved after enforcing authority before producer streams open,
    restricting it to produced `validation/v1`, wiring provenance through
    durable workflow/experiment identity, using canonical input-name grammar,
    and binding retries to the source run's exact provenance. Final fresh
    review reported no Critical, Important, or Minor findings.
- [x] Integration prerequisite — merge-preflight validation/v1 rev3
  compatibility
  - Commits:
    - `a2d5b939a4 fix(validation): seal merge preflight provenance`
    - `cd85882823 fix(workflow): bind merge preflight identity`
  - Verification:
    `go test ./atc ./atc/exec ./agent/functions/repositorymerge
    ./cmd/function-runner ./agent/workflow ./agent/workflowrun ./atc/builds
    -count=1` passed before review; the focused renderer identity regression
    passed after the blocking fix.
  - Review: accepted in round 2. Round 1 found that the trusted renderer did
    not replace compile-time zero workflow definition/version values. The fix
    now binds the exact persisted identity before fixed config/attestation/hash
    generation. Round 2 reported no blocking or nonblocking findings.
- [ ] Task 6 — hermetic non-agent validation
  - Current blocking-fix commit: `a57a04f027 fix(validation): close private secret substitution race`.
  - Focused verification passed: private-mount lifecycle and adversarial Create
    tests; ownerless pre-bind reaper focus; real `dev-validate` opaque and
    repository-change chains; `agent/functions/devvalidate`; and `atc/exec`.
    `git diff --check` passed.
  - Full Jetbridge package remains sandbox-blocked by the pre-existing
    `httptest` IPv6 listener permission panic in
    `TestVT06_DaemonSetVolume_StreamOut_RetrySucceeds`; focused relevant tests
    pass.
  - Status: **Human Review Required**. Task 6 exhausted its review budget.
  - The single final blocking-only review confirmed that trusted immutable
    Secrets now exist before Pod visibility and mounted profile/config digests
    are checked before the validation runner launches, but found two remaining
    blocking lifecycle races:
    1. An ambiguous `Pods.Create` error may mean the API committed the Pod even
       though the client saw a timeout. The current error path immediately
       deletes its pre-created Secret and could therefore delete a Secret
       already mounted by the committed Pod. A human-approved fix should
       reconcile the exact Pod name/identity before deleting anything.
    2. Owner-bound orphan reaping deletes by Secret name without the observed
       Secret UID as a delete precondition. A replacement object can therefore
       be removed between the reaper's read and delete. The proposed fix is the
       same UID-preconditioned delete already used by the ownerless path.
  - Per the bounded-review policy, do not iterate further automatically.
  - Milestone verification exposed a third blocking issue:
    `make test-unit` ran all 121 suites and failed only Jetbridge. A focused
    host-network rerun of `go test -json ./atc/worker/jetbridge -count=1`
    reported 201/380 passed and 179/380 failed. All 179 failures terminate in
    `cannot bind incomplete private task mounts to pod`: the zero-mount path
    still requires a Pod UID, but the ordinary fake-client tests do not assign
    one. This is recorded under `HUMAN-REVIEW-001`; no automatic fix was made.
- [ ] Task 7 — exact validation gates
- [x] Task 8 — output-builder core
  - Commits:
    - `bb567a16c5 feat(agent): add managed output builder core`
    - `fc31f229d8 fix(agent): harden managed output builder boundaries`
  - Verification: `go test ./agent/outputbuilder ./cmd/agent-output
    ./agent/snapshot/... -count=1` passed. Focused raw-codec, CLI/MCP, command,
    and agent-runner Dockerfile suites also passed.
  - Review: accepted in round 2. Round 1 found five High blockers in protected
    authority loading, production limits, repository-change input reopening,
    mount TOCTOU, and staged publication. The single correction pass added
    fixed read-only authority, bounded streaming, exact canonical input
    opening, retained `os.Root` handles, and record-last rollback publication.
    Round 2 found all five addressed with no new blocker.
  - Deferred: `DEFERRED-003` tracks fsynced crash-recovery journaling for
    abrupt host loss; ordinary operation failures restore the prior candidate.
  - Fix round 1: addressed all five scoped High findings with fixed read-only
    authority loading, default snapshot limits, exact mounted-input reopening,
    descriptor-anchored output operations, and transactional record/content
    publication. Commit: `fc31f229d8`. Required checkpoint passed; pending
    final scoped review.
- [ ] Task 9 — output-builder execution wiring
  - Status: dependency-deferred; not started.
  - A bounded codebase audit found no safe independent server-owned authority
    file seam for a sidecar. Task 9 must extend Task 6's
    `runtime.PrivateFileMount`/Secret lifecycle, whose three current blockers
    directly affect output-builder authority availability and integrity.
  - Resume only after `HUMAN-REVIEW-001` is resolved and the full Jetbridge
    suite is green. See `DEPENDENCY-001`.
- [x] Task 10 — Hangar core
  - Commit: `29e5215b13 feat(hangar): add immutable GCS object storage`
  - Behavior: provider-neutral immutable object contract with canonical
    SHA-256 keys; bounded zstd streaming into generation-pinned, verified GCS
    objects; immutable create/idempotent identical puts; collision, truncation,
    metadata, digest, and byte-limit rejection; cancellation-safe scratch
    handling; and a concurrency-safe test fake.
  - Verification: `go test ./agent/hangar -count=1` and
    `go test ./agent/hangar/hangarfakes -count=1` passed. The tagged emulator
    harness cleanup contract compiled and passed. The exact
    `make test-hangar-integration` target then passed against a temporary
    fake-gcs-server deployment on Borg, including concurrent writers,
    truncated zstd, and complete local-scratch loss. The temporary namespace
    was deleted and its absence verified. `DEFERRED-004` is resolved.
  - Review: independent Terra blocking-only review round 1 found no blocking
    findings and accepted the task. No correction or round-2 review was
    required.
  - Status: Accepted.
- [ ] Task 11 — Hangar daemon/deployment integration
- [ ] Task 12 — resource-source grammar and persistence
- [ ] Task 13 — source capture/reuse runtime
- [ ] Task 14 — direct in-ATC publication
- [ ] Task 15 — checkpoint and attempt data models
- [ ] Task 16 — safe-boundary checkpoint capture
- [ ] Task 17 — fresh-attempt restore/provider resume
- [ ] Task 18 — recovery telemetry/retention
- [ ] Task 19 — full verification and residue audit

## Current milestone acceptance

- `make test-dev-mcp`: passed on the host-network rerun.
- `make test-fly-integration`: 666/666 specs passed on the host-network rerun.
- `make test-integration`: 24/24 specs passed.
- `helm lint deploy/chart`: passed with informational chart messages only.
- Merge-preflight revision-3 focused suites: passed.
- `make test-unit`: failed only in Jetbridge as described under Task 6; the
  other 120 Ginkgo suites completed without a reported failure.
- Status: checkpoint evidence is complete, but the branch is not merge-ready
  while Task 6 remains **Human Review Required**.
