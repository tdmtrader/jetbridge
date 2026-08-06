# Full-State PR Monitor — Phase 2 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make the PR monitor capable of functioning at all. A live spike proved it bricks its own observer the first time it replies to a review comment; full-state observation is the fix, and it forces the removal of `ActionFor` in the same change.

**Architecture:** Stage A is atomic and is now a *repair*, not an improvement. Stages B–E are sequential and separately verifiable.

**Tech Stack:** Go 1.25, plain `testing` in `agent/*`, Ginkgo/Gomega in `atc/db`, PostgreSQL for DB suites.

**Spec:** [design](../specs/2026-08-05-full-state-pr-monitor-design.md) · **Audits:** [PR interface](2026-08-05-pr-interface-cleanup-audit.md), [snapshot duplication](2026-08-05-repository-snapshot-duplication-audit.md)

---

## The defect this plan repairs

Proven live against real GitHub (`tdmtrader/jetbridge#3`, since closed):

```
poll 1 (human's root review comment):            batches=1 threads=1   OK
poll 2 (advances to the platform's own reply):   FAILED
        github review reply has no root
```

A reply posted through `POST /pulls/{n}/comments/{id}/replies` is filed by GitHub under a **new review**, while its `in_reply_to_id` still names the root comment in the **previous** review. `normalizeReview` filters comments to one review (`observe.go:355-359`) and `normalizeThreads` builds its root index from only that subset (`:374-383`), so the reply is unresolvable and `Observe` fails at `:391` — permanently, since the cursor cannot advance past it.

Poll 1 succeeds only because `selectReview` processes one review at a time and happened to pick the human's. **The delta hides the defect until the platform acts.**

Three consequences that shape everything below:

1. **Full state is the repair.** Grouping threads across all comments in one pass is what makes a reply resolvable. Nothing else fixes this.
2. **The platform inflates its own batch count.** Every reply mints a review, so `ReviewBatches` grows on the platform's own actions — straight toward the 128 cliff. Batch windowing is load-bearing, not a nicety.
3. **The platform triggers itself.** A new review changes review state, which is in the digest, which mints a version and a build. The skip guard is load-bearing for the same reason.

---

## Prerequisites

**P1. `forge-pr in` cannot run at all.** `NoRepository: true` sets `GIT_DIR` (`directgit/runner.go:160-168`, `:589-590`) and `git init <dir>` honours it over the directory argument, so the repo lands in the ephemeral credential scratch and the fetch fails. Filed separately. **Blocks all end-to-end verification**, though Stage A's unit tests do not need it.

**P2. RESOLVED by the live spike.** Both questions answered: a reply *does* carry `pull_request_review_id` (so platform comments are observable), and a reply *does* mint a new review (so the platform self-triggers). No further provider investigation is needed.

**P3. Self-identity still does not exist.** `ghUser` decodes only `id` (`observe.go:54-56`); `githubUser` renders `"github-user-<id>"` (`:451`); login and the Bot flag are dropped. No identity field exists on `resource.Source` (`protocol.go:82-92`), `MonitorCheckState` (`protocol.go:30-40`, `pipeline.go:38-49`), or the rendered source map (`pipeline.go:284-294`). The only self-marking is `operationMarker`/`machineMarkerPattern` (`github/mutate.go:972-982`, `:28-31`), used only inside `mutate.go` — `observe.go` never inspects bodies. This is now a **code gap, not an unknown**, and Stage C must close it.

---

## Stage A — full state and server-side policy (atomic, and the repair)

Cannot be split. `triggers.go:77-81` branches on `len(ReviewBatches) > 0` and its own comment (`:78-80`) calls that slice *"an adapter-enforced unacknowledged delta"*. Full state makes it permanently non-empty, so `ActionReviewBatch` would fire forever and conflict/freshness would die.

### Five traps, all verified in source

1. **Windowing threads without pruning batch `ThreadIDs` bricks `Observe`.** `contracts.PullRequestReviewBatch.validate` (`contracts/pull_request.go:296-300`) errors on any thread ID absent from the observation; `Observation.Validate` calls it (`types.go:156`); `observe.go:140-142` makes it fatal. **The window must prune dropped thread IDs from every batch.**
2. **`maxReviewBatches = 128` is a separate cliff, and the platform drives toward it.** `reviews()` collects up to 1024 (`observe.go:271`) but `types.go:135-137` and `contracts/pull_request.go:182` reject >128. Each platform reply adds a review. **Batches need windowing too.**
3. **`pullrequest.Thread` is a type ALIAS** — `type Thread = contracts.PullRequestThread` (`types.go:51`). You cannot add an unexported field to it, and package `github` cannot set an unexported field owned by `contracts`. Carry ordering state in a local `map[string]time.Time` or wrapper inside `observe.go`.
4. **`anchorFor` returns two values** — `(*contracts.PullRequestAnchor, error)` (`observe.go:437`). A single-value call site will not compile.
5. **Cursor migration.** `decodeCursor` uses `DisallowUnknownFields()` (`:485`) and rejects a mismatched `Version` (`:486`). Removing `Watermark`/`BatchDigest` (`:70-75`) or bumping `cursorVersion` (`:21`) makes every stored `AcknowledgedCursor` undecodable — and those feed straight in at `check.go:34` and `in.go:80`. **Add a tolerant path (unrecognised cursor ⇒ treat as empty) or write a migration.** Not optional.

### One decision to make explicitly

**`Truncated` cannot reach the agent as drafted.** `contracts.PullRequestBody` (`contracts/pull_request.go:50-66`) has no such field and `in.go:200` copies field-by-field. Either accept a sealed-schema revision (append-only, `rev3` → `rev4`, precedent exists) or convey truncation another way. Also note `Thread.Iteration` is currently the **review** id (`observe.go:411`), not the root-comment id — changing it is a semantic change to a sealed field, so decide rather than drift.

### Delete / rewrite / keep

| | symbols (current line numbers) |
|---|---|
| **delete** | `selectReview` (308-327), `afterWatermark` (329-341), `watermarkFor` (343-345), `digestBatch` (454-459), `reviewWatermark` (76-79), the `Watermark`/`BatchDigest` cursor fields (70-75), the cursor invariants (489-491), `parseCursorTime` (499-505) |
| **rewrite** | `normalizeReview` (347-371) — drop the per-review filter at 354-359; `normalizeThreads` (373-435) — build `byID` from **all** comments |
| **keep** | `reviews` (252-278), `comments` (280-306), `anchorFor` (437-449), `githubUser` (451), `commentID` (452), `digestState` (460-468), `encodeCursor` (506-515) |

### Consumers to repoint (all assume one batch)

`revision_executor.go:592` (positional `[0]`), `:693`/`:700`/`:705` (`len == 1` / `== 0`); `monitor_run_inspector.go:307-310` and `:499-505` (both `len == 1`, so full state makes every succeeded review-batch run classify `MonitorOutcomeAmbiguous`); `classifySucceededMonitorRun` `:257-263` (hard-coded publication counts); `ActionFor` callers `in.go:94` and `check.go:43`.

**NO HELPER EXISTS** to select a batch by ID — the only ID-keyed scan is inside `contracts.ValidatePullRequestResponseAgainst` (`pull_request_response.go:133-147`) and it returns only an error.

### The regression fixture — now specifiable exactly

The spike captured the real shape. A fixture reproducing it is the test that would have caught this defect, and it must exist before the fix:

```
review A: id 4870141034, state COMMENTED
review B: id 4870143605, state COMMENTED         <- minted by the platform's reply

comment root:  id 3725191076, pull_request_review_id 4870141034, in_reply_to_id null
comment reply: id 3725192857, pull_request_review_id 4870143605, in_reply_to_id 3725191076
```

Two testdata files are needed — `reviews_cross_review.json` and `review_comments_cross_review.json` — because **every existing fixture puts all comments under one `pull_request_review_id`**, which is precisely why the suite never caught this.

**Assert both directions:** the fix resolves the reply into the root's thread, *and* a test pinned to the pre-fix behaviour would fail. Do not merely assert `Observe` succeeds.

### Verified test helpers — use these, invent nothing

`agent/pullrequest/github` (internal package): `writeFixture` `observe_test.go:445` · `writeFixtureAt` `:455` · `tokenFunc` `:441` · `roundTripFunc` `:435` · `int64Pointer` `:433` · `sha(rune)` `:466` · `rotatingToken` `:17` · `writeJSON` `mutate_test.go:637`

`agent/pullrequest`: `monitorObservationBody` `monitor_run_inspector_test.go:548` · `newTestDurableMonitorRunInspector` `:537` · `monitorSucceededEvidence` `:593` · `monitorPublication` `:740` · `monitorSnapshotDigest` `:768`

`testdata/`: `pull_request_active.json`, `pull_request_closed.json`, `pull_request_merged.json`, `reviews_page_1.json`, `reviews_page_2.json`, `review_comments_page_1.json`.

### Scaffolding that must be built (NO HELPER EXISTS)

A GitHub test-server helper (every test inlines `httptest.NewServer`); a synthetic review/comment generator (a window test needs N > 150, testdata holds ≤ 3 reviews); the cross-review fixture above; a full-state observation builder (`reviewObservation` `triggers_test.go:137` and `activeObservation` `resource/check_test.go:137` both build single-batch deltas); an active-state `PullRequestBody` builder (`monitorDirectTerminalBody` `monitor_test.go:853` emits only terminal states).

**Warning:** `monitorDirectTerminalDigest` (`monitor_test.go:842-851`) hard-codes literal sha256 strings that break silently if `actionDigest`'s identity struct (`triggers.go:145-154`) or domain prefix (`:159`) changes.

---

## Stage B — generic source instances

`agent_pr_bindings` → `agent_workflow_source_instances`; `pr_binding_id` → nullable `source_instance_id`; collapse the binding-scoped store twins.

**NO COUNTERFEITER FAKE EXISTS** for `WorkflowResourceSourceAdmissionStore`, `WorkflowResourceSourceBindingAdmissionStore`, `WorkflowResourceSourceBuildStore`, `WorkflowResourceSourceBindingBuildStore`, `WorkflowResourceSourcePipelinesFactory` or `pullrequest.BindingStore` — no `//counterfeiter:generate` directive, nothing in `dbfakes/`. Hand-roll stubs in the style of `source_build_reconciler_test.go`.

Migration head is `1773106159`; two head constants move in lockstep (`legacy_upgrade_test.go:37`, `migrate-preflight.sh:82`); `1773106154` hard-fails if any `agent_pr_bindings` row exists — which is also the guarantee that no data migration is needed.

---

## Stage C — skip guard (now plannable, with two code gaps to close)

The spike unblocked the provider question: platform replies **are** observable. Two gaps remain, both in our code:

1. **Self-identity (P3).** Decide the attribution mechanism: extend `ghUser` to decode `login` and the Bot flag, or scan bodies for `machineMarkerPattern` in `observe.go`. The marker already exists and is already written; it is simply never read on the observe side.
2. **No durable per-batch state.** `pullrequest.Binding` (`store.go:58-94`) carries only scalars — `AcknowledgedCursor` `:76`, `LastObservationSnapshotID` `:77`, `LastAcknowledgedActionDigest` `:78` — and `AcknowledgeAction` advances one cursor plus one digest. "Which batches have I already answered" needs new durable state.

Design constraints carried from the spec: the digest must be **server-derived** at acknowledge time (never workflow-supplied, or a buggy agent could silence its own checks); `GET /issues/{n}/comments` must be added to the observer (the platform writes there at `mutate.go:453` and never reads); and the guard must **fail toward re-running**.

New from the spike: since each platform reply mints a review, the guard must treat *the platform's own review* as self-caused, or every reply re-triggers a run.

---

## Stage D — server-side re-launch

Re-launch a failed or freshness-due run from the durable captured snapshot, with no new build or version. Removes `binding_revision` from the version and the freshness-via-manual-build path — which is structurally closed at five layers and a cluster-wide poison pill (see the spec).

## Stage E — `resource_sources:` declaration and mixed inputs

`pr-monitor-v3` declares the observation as a source; `BindReadySourceAdmission` gains an `Inputs` map; `LaunchMonitor` is deleted. Three known blockers: the per-instance vs definition-scoped config-hash mismatch, `origin_kind='pr-monitor'` hard-coded in five SQL statements, and `BindPRMonitorAuthority` becoming unreachable (fails closed, but every run fails).

---

## Verification

Per stage: `go build ./...`, `go vet ./...`, affected package tests, plus DB suites for Stage B. **Drain ports 5434–5442 with SIGTERM first** — `postgresrunner` binds `5433 + GinkgoParallelProcess()`, other worktrees collide, and `kill -9` leaks SysV shared memory until `initdb` fails.

**Add a live smoke test.** The spike proved that a whole class of defect is unreachable from fixtures, because every fixture encodes an assumption the real provider violates. An environment-gated test against a throwaway PR — post a root comment, reply, poll twice — would have caught this in seconds. It belongs in the suite, skipped without a token.

**What green tests still do not prove.** Most of this subsystem has never executed. Passing tests mean internal consistency; only the live GitHub run on theborg proves the feature works, and that needs P1 fixed, Stages A–E landed, and the four authority-spine gaps closed. `PR_PUBLISH_LIVE_PROOF.md` reads "Current result: not run."

## Provenance

Written against a five-area ground-truth survey (every symbol cited to a declaration site, every claimed helper independently grep-verified) and a live GitHub spike. That process exists because the Phase 1 plan invented four test fixtures and two compile errors — `Thread` being an alias, `anchorFor`'s arity — that reached the executing agent, and because no amount of fixture-based reasoning could have found the cross-review defect.
