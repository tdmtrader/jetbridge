# Full-State PR Monitor — Phase 2 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the consuming-delta observation and its resource-side action derivation with full-state observation plus a server-side policy — the smallest change that is internally consistent, because the two cannot coexist.

**Architecture:** Stage A is atomic: full-state `Observe`, deletion of `ActionFor`, a server-side policy, and repointing every consumer that assumed one review batch. Stages B–E are sequential and separately verifiable. Stage C is **not yet plannable** — see Prerequisites.

**Tech Stack:** Go 1.25, plain `testing` in `agent/*`, Ginkgo/Gomega in `atc/db`, PostgreSQL for DB suites.

**Spec:** [2026-08-05-full-state-pr-monitor-design.md](../specs/2026-08-05-full-state-pr-monitor-design.md) · **Audits:** [PR interface](2026-08-05-pr-interface-cleanup-audit.md), [snapshot duplication](2026-08-05-repository-snapshot-duplication-audit.md)

---

## Prerequisites — do not start Stage A without these

**P1. `forge-pr in` cannot run at all.** `NoRepository: true` sets `GIT_DIR` (`directgit/runner.go:160-168`, `:589-590`) and `git init <dir>` honours `GIT_DIR` over its argument, so the repository lands in the ephemeral credential scratch and the fetch fails. Filed separately. **Nothing in this plan can be verified end-to-end until it is fixed.**

**P2. A GitHub behaviour spike, before Stage C can be written.** Two facts are unverified and the skip guard's design depends on both:

- Does GitHub set `pull_request_review_id` on the platform's *own* reply comments? `normalizeReview` filters on `comment.ReviewID != nil && *comment.ReviewID == value.ID` (`observe.go:355-359`), so if replies arrive without a review id **the platform's own comments are silently absent from every observation** — and a guard that reasons about "items I published" cannot see them.
- Does a standalone reply mint a new review? If it does, the platform's own action changes review state, which feeds the digest.

**P3. Self-identity does not exist.** `ghUser` decodes only `id` (`observe.go:54-56`), and `githubUser` renders `"github-user-<id>"` (`:451`) — login and the Bot flag are dropped. No identity field exists on `resource.Source` (`protocol.go:82-92`), `MonitorCheckState` (`protocol.go:30-40`, `pipeline.go:38-49`), or the rendered source map (`pipeline.go:284-294`). Repo-wide greps for bot/self-login/app_slug return zero hits. The only self-marking mechanism is `operationMarker`/`appendMarker`/`machineMarkerPattern` (`github/mutate.go:972-982`, `:28-31`), used **only inside `mutate.go`** — `observe.go` never inspects comment bodies.

---

## Stage A — full state and server-side policy (atomic)

These cannot be separated. `triggers.go:77-81` branches on `len(ReviewBatches) > 0` and its own comment (`:78-80`) calls that slice *"an adapter-enforced unacknowledged delta"*. Full state makes it permanently non-empty, so `ActionReviewBatch` would fire forever and conflict/freshness would go dead.

### Four traps verified in the source

Any implementation must handle all four. Each was found by reading, not by reasoning:

1. **Windowing threads without pruning batch `ThreadIDs` bricks `Observe`.** `contracts.PullRequestReviewBatch.validate` (`contracts/pull_request.go:296-300`) errors `"thread id %q is not present in the observation"`, `Observation.Validate` calls it (`types.go:156`), and `observe.go:140-142` turns that into a hard error. **The window must prune every dropped thread ID from every batch.**
2. **`maxReviewBatches = 128` is a separate cliff.** `reviews()` collects up to 1024 (`observe.go:271`), but `types.go:135-137` and `contracts/pull_request.go:182` reject more than 128 batches. A PR with 129 submitted reviews fails permanently. **Batches need windowing too, not just threads.**
3. **`pullrequest.Thread` is a type ALIAS** — `type Thread = contracts.PullRequestThread` (`types.go:51`, `=` not a definition). You **cannot** add an unexported `lastActivity` field to it, and package `github` cannot set an unexported field declared in package `contracts`. Carry ordering state in a local `map[string]time.Time` or a local wrapper struct inside `observe.go`.
4. **`anchorFor` returns two values** — `func anchorFor(comment reviewComment) (*contracts.PullRequestAnchor, error)` (`observe.go:437`). Calling it as a single-value expression will not compile.

### Two hazards that need an explicit decision

- **Cursor migration.** `decodeCursor` uses `DisallowUnknownFields()` (`observe.go:485`) and rejects a mismatched `Version` (`:486`). Removing `Watermark`/`BatchDigest` from `githubCursor` (`:70-75`), or bumping `cursorVersion` (`:21`), makes every stored `AcknowledgedCursor` undecodable — and those are fed straight in at `check.go:34` and `in.go:80`. **Either add a tolerant path (unrecognised cursor ⇒ treat as empty) or write a migration.** Not optional.
- **`Truncated` cannot reach the agent as drafted.** `contracts.PullRequestBody` (`contracts/pull_request.go:50-66`) has no such field and `in.go:200` copies field-by-field. Adding one is a **sealed-schema revision**. Either accept the revision (append-only, `rev3` → `rev4`, precedent exists) or convey truncation another way.

### What to delete, rewrite, keep

| | symbols (with current line numbers) |
|---|---|
| **delete** | `selectReview` (308-327), `afterWatermark` (329-341), `watermarkFor` (343-345), `digestBatch` (454-459), `reviewWatermark` (76-79), the `Watermark`/`BatchDigest` cursor fields (70-75), the cursor invariants at 489-491, `parseCursorTime` (499-505) |
| **rewrite** | `normalizeReview` (347-371) — drop the per-review comment filter at 354-359; `normalizeThreads` (373-435) — build `byID` from **all** comments so a cross-review reply resolves |
| **keep** | `reviews` (252-278), `comments` (280-306), `anchorFor` (437-449), `githubUser` (451), `commentID` (452), `digestState` (460-468), `encodeCursor` (506-515) |

**Also note:** `Thread.Iteration` is currently the **review** id (`observe.go:411`), not the root-comment id. Changing it is a semantic change to a sealed field — decide deliberately, do not drift.

### Consumers to repoint (all assume one batch)

- `revision_executor.go:592` — publishes `ReviewBatches[0]` positionally
- `revision_executor.go:693`, `:700`, `:705` — `len == 1` for review_batch, `== 0` for conflict/freshness
- `monitor_run_inspector.go:307-310` (`exactMonitorObservation`), `:499-505` (`exactMonitorObservationBatch`) — both require `len == 1`; full state makes every succeeded review-batch run classify `MonitorOutcomeAmbiguous`
- `classifySucceededMonitorRun` (`:257-263`) — hard-codes publication counts (3 for review-batch, 2 otherwise)
- `ActionFor` callers: `in.go:94`, `check.go:43`

**NO HELPER EXISTS** to select a batch by ID. The only ID-keyed scan is inside `contracts.ValidatePullRequestResponseAgainst` (`pull_request_response.go:133-147`), which returns only an error. Each site needs its own lookup, or one shared helper added.

### Test helpers that exist (verified — use these, invent nothing)

`agent/pullrequest/github` (package `github`, internal):
`writeFixture(t, response, name)` `observe_test.go:445` · `writeFixtureAt(t, response, name, host)` `:455` · `tokenFunc` `:441` · `roundTripFunc` `:435` · `int64Pointer` `:433` · `sha(rune)` `:466` · `rotatingToken` `:17` · `writeJSON` `mutate_test.go:637`

`agent/pullrequest` (package `pullrequest`):
`monitorObservationBody(evidence)` `monitor_run_inspector_test.go:548` · `newTestDurableMonitorRunInspector` `:537` · `monitorSucceededEvidence(t)` `:593` · `monitorPublication(t, action, result)` `:740` · `monitorSnapshotDigest(byte)` `:768`

`testdata/`: `pull_request_active.json`, `pull_request_closed.json`, `pull_request_merged.json`, `reviews_page_1.json`, `reviews_page_2.json`, `review_comments_page_1.json`.

### Scaffolding that must be built first (NO HELPER EXISTS)

Each of these is a task in its own right — Phase 1 failed by assuming they existed:

1. A GitHub test-server helper. Every existing test inlines `httptest.NewServer` with its own path switch; there is no shared builder.
2. A synthetic review/comment JSON generator. `testdata/` holds at most 3 reviews and 2 comments; a window-truncation test needs N > 150 generated in-test.
3. A cross-review-reply fixture. Both comments in `review_comments_page_1.json` carry `pull_request_review_id: 10`, so the currently-broken case has no fixture.
4. A full-state observation builder. `reviewObservation` (`triggers_test.go:137`) and `activeObservation` (`resource/check_test.go:137`) both build single-batch deltas.
5. An active-state `PullRequestBody` builder for `MonitorObservationInspector`. `monitorDirectTerminalBody` (`monitor_test.go:853`) emits only completed/abandoned with zero batches.

**Warning:** `monitorDirectTerminalDigest` (`monitor_test.go:842-851`) hard-codes literal sha256 strings. They break silently if the `actionDigest` identity struct (`triggers.go:145-154`) or its domain prefix (`:159`) changes.

---

## Stage B — generic source instances

`agent_pr_bindings` → `agent_workflow_source_instances`; `pr_binding_id` → nullable `source_instance_id`; collapse the binding-scoped store twins.

Independent of Stage A and separately verifiable. **NO COUNTERFEITER FAKE EXISTS** for `WorkflowResourceSourceAdmissionStore`, `WorkflowResourceSourceBindingAdmissionStore`, `WorkflowResourceSourceBuildStore`, `WorkflowResourceSourceBindingBuildStore`, `WorkflowResourceSourcePipelinesFactory` or `pullrequest.BindingStore` — no `//counterfeiter:generate` directive and nothing in `dbfakes/`. New unit tests must hand-roll stubs in the style of `source_build_reconciler_test.go`.

Migration constraints: head is `1773106159`, two head constants move in lockstep (`legacy_upgrade_test.go:37`, `migrate-preflight.sh:82`), and `1773106154` hard-fails the upgrade if any `agent_pr_bindings` row exists — which is also the guarantee that no data migration is needed.

---

## Stage C — skip guard — **NOT PLANNABLE YET**

Blocked on P2 and P3. The design assumes the platform can recognise its own comments; today it cannot, and it is unverified whether its replies even appear in observations. Additionally **NO durable per-batch state exists**: `pullrequest.Binding` (`store.go:58-94`) carries only scalars — `AcknowledgedCursor` (`:76`), `LastObservationSnapshotID` (`:77`), `LastAcknowledgedActionDigest` (`:78`) — and `AcknowledgeAction` advances one cursor plus one digest. A cursor-free "which batches have I answered" needs new durable state that does not exist.

Run the P2 spike, then plan this stage.

---

## Stage D — server-side re-launch

Re-launch a failed or freshness-due run from the durable captured snapshot, with no new build or version. Removes the need for `binding_revision` in the version and the freshness-via-manual-build path (which is structurally closed and a cluster-wide poison pill — see the spec).

---

## Stage E — `resource_sources:` declaration and mixed inputs

`pr-monitor-v3` declares the observation as a source; `BindReadySourceAdmission` gains an `Inputs` map; `LaunchMonitor` is deleted. Carries three known blockers: the per-instance vs definition-scoped config-hash mismatch, `origin_kind='pr-monitor'` hard-coded in five SQL statements, and `BindPRMonitorAuthority` becoming unreachable (fails closed, but every run fails).

---

## Verification

Per stage: `go build ./...`, `go vet ./...`, the affected package tests, and for Stage B the DB suites. **Drain ports 5434–5442 with SIGTERM first** — `postgresrunner` binds `5433 + GinkgoParallelProcess()`, other worktrees collide, and `kill -9` leaks SysV shared memory until `initdb` fails.

**What green tests do not prove.** Most of this subsystem has never executed. Passing unit and integration tests mean the code is internally consistent, not that the feature works. The only evidence that it works is the live GitHub run on theborg — which needs P1 fixed, Stage A–E landed, and the four authority-spine gaps closed. `PR_PUBLISH_LIVE_PROOF.md` still reads "Current result: not run."

## Provenance

Written against a ground-truth survey of five areas, each required to cite a declaration site for every symbol it named, followed by an independent pass that grepped for each claimed helper. All five helper sets verified. This process exists because the Phase 1 plan invented four test fixtures and two compile errors (`Thread` alias, `anchorFor` arity) that reached the executing agent.
