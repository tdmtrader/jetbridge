# Judge + Evidence — plan 09 harvest-step remainder

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking. This is a REMAINDER plan: it amends the landed harvest v0.5 core (commits b6e8743a98, 0689163540, 96e96a8461, 59d3410745) — read "Landed state" before touching anything, and never rebuild what it lists. Where a task says "execute plan 09 Task N as written", open `09-harvest-step.md`, apply the delta notes here FIRST (they are corrections for code that landed after plan 09 was written), then run its checkbox steps.

**Date:** 2026-07-17
**Status:** draft for review (supersedes the earlier same-day draft at this path, which was written before the workflow-source-format slice landed and carries stale migration numbers)
**Depends-on:** plan 09 (`09-harvest-step.md`, the source of referenced tasks), `00-shared-contracts.md` (§1.10, §2.8.1, §5, §6.3, §6.4/§6.4.1, §8.2, §8.3, §11 log through the 2026-07-18 v0.5 entry), `CONVENTIONS.md` (C2, C3), landed slices: ticket-core, manual-dispatch, harvest v0/v0.5, agent-step ingestion (the pattern source), agentic-ui waves incl. E+F (0866d89fc9), workflow-source-format implementation (ac9347c9aa..187cad4926 — it moved the migration head, see below).

**Goal:** Complete the harvest step's trust story — rubric judge (advisory, platform-funded), flight-recorder evidence (`events.ndjson`/`results.json`/`manifest.json`/`review.json`), server-side ingestion into `agent_run_metrics`/`agent_reviews` (with new ticket/run linkage columns)/`agent_cost_ledger`, and build-page rendering for agent/harvest steps.

**Architecture:** The landed `harvest-runner` (in-pod, deterministic Go) gains a flight recorder and a single schema-constrained claude-CLI judge call slotted between gates and the push-by-sha; the landed `exec.HarvestStep` gains the platform-credential `SecretEnv` ref, the `flight` output artifact, and synchronous degradation-tolerant ingestion whose METRICS half (results.json/events.ndjson → `agent_run_metrics` + `agent_cost_ledger`) is copied from `exec.AgentStep.ingestFlightRecorder`, but whose EVIDENCE half (review.json → `agent_reviews`) is NEW server-side ingestion with NO exec precedent — `AgentStep` never touches reviews (they arrive via the principal-authenticated HTTP POST at `agent/api/reviews/handler.go`), so parsing a pod-written flight volume and upserting `agent_reviews` directly is a different trust model that Task 12 / decision D9 must own; migration 1773106080 adds nullable ticket/run linkage to `agent_reviews`/`agent_feedback` with COALESCE-preserving upserts. The judge refusal boundaries are relaxed in lockstep-safe order (runner first, then exec+render together).

**Tech stack:** Go (agent/harvest, agent/schema nested module, atc/exec, atc/db, agent/dispatch), PostgreSQL migration 1773106080, plain-Go tests in `agent/*` (the landed package style — NOT the Ginkgo skeletons plan 09 carries), Ginkgo in `atc/*`, Elm + Go public-plan for the build page, claude CLI (judge only), plain-Go `//go:build live` theborg test.

---

## Ground-state corrections (verified in-tree at planning time, HEAD 187cad4926 — they override plan 09, the 2026-07-17 shared ground state, AND the earlier draft of this file)

1. **Deployed head migration is 1773106066, not 1773106064.** The workflow-source-format slice LANDED (commit ac9347c9aa: `1773106066_add_agent_workflow_source_manifest`); `atc/db/migration/legacy_upgrade_test.go:37` reads `jetbridgeHeadMigration = 1773106066` and `docs/migration/migrate-preflight.sh:38` reads `JETBRIDGE_VERSION=1773106066`. Consequences: (a) any "renumber down to 1773106066" option is DEAD — that number is taken; next-free is 1773106067; (b) PARK-V2's reserved `1773106065` is now BELOW head and will be forced into a renumber at ITS land time (ticket-core precedent). This plan's `1773106080` remains safely above head — see "Migration allocations".
2. **`agent/harvest` is plain-Go tested** (`runner_test.go`, `gates_test.go`, `policy_test.go` — `func TestXxx(t *testing.T)`, no Ginkgo). Every plan-09 Ginkgo skeleton for this package is re-issued below in the package's real style. `agent/dispatch` is also plain-Go. `atc/exec`/`atc/db` remain Ginkgo.
3. **`agent/devmcp.WaitHealthy` does not exist.** The healthz wait lives unexported in `agent/runner/runner.go:368-391` (`waitForSidecars`/`waitHealthy`). Plan 09's F34 text assumed a shared helper; the dev-mcp swap that needs it is SCOPED OUT of this plan (see Scope).
4. **`atc/exec/harvest_step_test.go` does not exist.** The landed exec step shipped with zero unit specs; Tasks 10 and 12 create the file from scratch (budgeted).
5. **No Elm `BuildStepAgent` precedent exists** (grep-verified at HEAD 187cad4926, i.e. AFTER the agentic-ui waves: no `agent`/`harvest` decoder in `web/elm/src/Concourse.elm`'s `decodeBuildPlan` oneOf), and `atc/public_plan.go`'s `Public()` struct has an `Agent` field but NO `Harvest` field — `HarvestPlan.Public()` at `atc/public_plan.go:328` is dead code. Task 13 covers BOTH step types plus the Go touchpoint.
6. **`RunGates` landed with a different signature and outcome shape than plan 09 Task 8 designed** (`RunGates(policy, workspaceDir) ([]GateOutcome, error)`; `GateOutcome{Gate,Scope,Status,Attempt,Flaky,DurationSeconds,Detail}` — `Attempt` not `Attempts`, `Detail` not `Summary`/`OutputTail`, no `Component`). The JSON shape of `GateOutcome` is a live contract (`Results.Metadata.Gates`, in-cluster since v0.2.163) and MUST NOT change; the Go signature may grow an events parameter (in-repo callers only).
7. **`harvest.Run` landed as `Run(cfg, workspaceDir, credsDir string, out io.Writer) int`** with inline git closures — plan 09 Task 10's `RunDeps`/`ExitGatesPassed` orchestration never existed. Task 9 extends the landed function.
8. **`StoredReview` grew `EvaluatedCount` and `SubmittedBy`** (audit-attribution convention, §1.2/§11 2026-07-09 entry; `agent_reviews.submitted_by` landed via migration 1773106011). Plan 09 Tasks 3/4 predate both; harvest evidence writes set `submitted_by = 'harvest'`.

---

## Landed state (do NOT rebuild)

| What | Where | Commit |
|---|---|---|
| `atc.HarvestStep`/`HarvestPlan` + all visitors, parse key `harvest` before `run`; full §2.8.1 field set incl. `TargetBranch`/`Env`/`DevMCP`/`Judge`/`Timeout` | `atc/steps.go:433`, `atc/plan.go` | b6e8743a98 |
| `agent/harvest` policy wire types (`GatePolicy`/`Gate` incl. `Retries`, `JudgeConfig`/`RubricDimension`, `Config`, `GitCredSecretName`) | `agent/harvest/policy.go` | b6e8743a98 |
| harvest-runner v0: git-repo check, F33 dirty=fail (no auto-discard), push-by-sha `--force-with-lease` (F32), GIT_ASKPASS cred delivery (token never on argv), exit taxonomy 0/1/2, results JSON to stdout | `agent/harvest/runner.go`, `cmd/harvest-runner/main.go`, `deploy/agent-runner/Dockerfile` | 0689163540, b12e77ff32 |
| Gates v0.5: `RunGates` fixed command map (build=`go build ./...`, test=`go test ./...`, lint=`go vet ./...`), scope-full-only, per-gate timeout default 30m, §6.3 flake stance exact, first non-ok stops, outcomes in `Results.Metadata.Gates` | `agent/harvest/gates.go` | 59d3410745 (ticket #14, built BY the loop) |
| `exec.HarvestStep`: pod spec, HARVEST_CONFIG env, static-env fail-closed + F30 run-id fallback, v0.5 admission (full-scope gates in; judge/dev_mcp/pushless-branch refused loudly), `SecretMounts` git-cred (push only), guarded ticket transition (`RunBelongsToPipeline`+`TicketBelongsToRun`) incl. timeout/process-failure paths (ticket #12 live finding) | `atc/exec/harvest_step.go` | 96e96a8461, 1aef877c49 |
| `runtime.ContainerSpec.SecretMounts` + jetbridge main-container-only mounting (sidecars spec-pinned to never receive them); `applySecretRefs` APPEND semantics for SecretEnv-only keys (F20 FIXED, live-tested via `live_secret_env_test.go`); sidecar WorkingDir inherit (F21) | `atc/runtime/types.go:139-148`, `atc/worker/jetbridge/container.go:459-470,781-819` | 96e96a8461 + agent-step 11B |
| Engine/atccmd wiring: `db.ContainerTypeHarvest`, builder dispatch, `CoreStepFactory.HarvestStep` with tickets-store/run-verifier opts; `--agent-platform-token-secret` flag feeding `factory.agentPlatformToken` | `atc/engine/builder.go`, `atc/engine/step_factory.go:281-303`, `atc/atccmd/command.go:236,2082-2087` | 96e96a8461 |
| Renderer emits terminal harvest for every ticketed run (workspace="workspace", branch `agent/ticket-<id>`, push:true, §8.1 identity env) + v0.5-relaxed full-scope `gate_policy` conversion; judge/hitl/affected-scopes/source-format still refused at render | `agent/dispatch/render.go:141-170,207-234,294-309` | 2dbb9dc3fe, b4965aa126, 59d3410745, 2db630c3e9 |
| §5 flight-recorder building blocks: event constants (`step.start/step.end/gate.start/gate.result/judge.score/cost.record/push.done`), payload structs, `EventWriter`/`EventReader` (`Event{ts,event,data}`, auto-timestamp on Write), `ReviewOutput`, `Results` (metadata map) | `agent/schema/` (nested stdlib-only module) | agent-step wave |
| dev-mcp Go client + fakes (for the OUT-of-scope executor swap) | `agent/devmcp/` | dev-mcp wave |
| Judge funding plumbing: `budget.SourceHarvestJudge`, `harvest_judge` in the ledger CHECK, excluded from ticket-budget spend | `agent/budget/budget.go:28,36,115`, migration 1773106021 | credentials wave |
| Ticket-page review embed (fetches build-scoped reviews via `builds/:id/agent-reviews` — works without linkage; linkage enables `ListByTicket` for delivery-outcomes later) | `web/elm/src/AgentTickets/AgentTicket.elm` | 66c3eb45ba |
| Fixture-workspace gate coverage at unit level: pass/fail/vet-fail/unknown-gate/non-full-scope/retry-flaky/retries-exhausted/timeout at `RunGates` level; pass-pushes/fail-blocks/error-exits at `Run()` level; dirty/re-push/askpass | `agent/harvest/gates_test.go`, `agent/harvest/runner_test.go` | 59d3410745 |
| Workflow-source-format slice: manifest imports, `source_manifest` column (migration **1773106066**), fly import, render refusals for source-format surfaces | `agent/workflow/`, `atc/db/migration/migrations/1773106066_*` | ac9347c9aa..187cad4926 |

---

## Scope

**IN:**
1. Contract addendum body text (plan 09 Task 1, amended) — merged §2.8.1, §6.3 `Retries` body text, §6.4.1, results.json shape pin, §11 entries.
2. Migration 1773106080 + reviews/feedback linkage (Tasks 2–4, referenced with deltas).
3. Judge engine: git-context helpers, `JudgeConfig.Validate()`/`RubricHash()`, `RunJudge` (plain-Go tests).
4. Flight recorder in harvest-runner + judge execution in `Run()` + additive `agent/schema` keys.
5. Exec/renderer judge admission + platform-credential `SecretEnv` + `flight` output artifact.
6. Exec server-side ingestion (metrics upsert, evidence upsert with linkage + `submitted_by='harvest'`, judge ledger record).
7. Elm build-page rendering for agent AND harvest steps + the `public_plan.go` fix.
8. Fixture e2e remainder, live theborg credential-isolation test, close-out.

**OUT (stays deferred, named home):**
- **dev-mcp gate-executor swap** (plan 09 Task 8 + the Task 12 dev-mcp-sidecar subset + F34 readiness + `affected`/`affected_then_full` scope relaxation + the shared `WaitHealthy` extraction + the fixed-map-fallback decision + the `Retries>2` clamp and `on_gate_failure` validation for hand-authored steps). Lives in a follow-on remainder plan (`remainders/` — "devmcp-gates", not yet written). Until it lands: the three refusal boundaries KEEP refusing non-full scopes and `dev_mcp` blocks, and the fixed command map remains the only executor. Task 9 here wires `gate.start`/`gate.result` event emission into the in-pod engine, which the swap inherits.
- **Diff/PR rendering on the ticket page, merge detection, outcome watcher** — delivery-outcomes (plan 12; its `1773106090` block is untouched; this plan only writes findings in the pinned shape). **Judge-finding DISPLAY is not a new UI in any plan** (retiring the earlier pointer to a delivery-outcomes "judge-finding feedback UI"): judge findings (category `judge`, added by Task 8) surface via (a) THIS plan's build-page step render + the `agent-reviews` evidence panel (Task 13), and (b) the ALREADY-SHIPPED generic six-verdict feedback on the ticket page (`SubmitAgentReviewVerdict`, waves E+F) — it is category-agnostic and works on `judge` findings with no new surface. delivery-outcomes D2 explicitly drops any judge-specific panel; there is no `finding_type: "judge"` verdict UI to build, so do not point at delivery-outcomes for one.
- **Budget pre-metering for the judge** — impossible for a single CLI call; `BudgetUSD` stays post-hoc (§6.4.1).
- **Ticket-evidence API routes** (`/agent-tickets/:id/evidence` etc.) — delivery-outcomes owns them. This plan adds NO routes (C1 does not trigger; evidence reads ride the existing `builds/:id/agent-reviews`).
- **PARK-V2 `agent_run_step_state`** — its 1773106065 reservation is now below head; renumbering happens at ITS land time, not here.

---

## Slices

Each slice is independently shippable, in order. Verification story per slice is split three ways: **gate-verifiable** (pure-Go `go build ./...` + `go test ./...` + `go vet ./...` — what the loop's harvest gates run in-pod), **local-verify** (postgres-backed or toolchain-bound; a human runs it locally pre-merge), **live-verify** (theborg cluster smoke).

### Slice A — Contract addendum (Task 1)
Doc-only, normative. **Gate-verifiable:** none (no code). **Local-verify:** proofread against the landed code cited inline; `git diff --stat` shows only `00-shared-contracts.md`. **Live-verify:** none.

### Slice B — Linkage data layer (Tasks 2–4)
Migration 1773106080, `agent/api/reviews` fields + `ListByTicket`, `atc/db` factories. **Gate-verifiable:** `agent/api/reviews` compiles and its suite runs without postgres — but the loop's fixed `test` gate runs `go test ./...` at repo root, which DOES reach `atc/db` suites; whether those skip cleanly without postgres is UNVERIFIED (open decision D7). Treat this slice as NOT loop-gate-verifiable. **Local-verify (mandatory pre-merge):** `pg_isready && ginkgo --focus='Legacy Database Upgrade' ./atc/db/migration/ && ginkgo ./atc/db/` (~90s). **Live-verify:** none (migration applies on next deploy; the preflight-script constant is the guard).

### Slice C — Judge engine + git context (Tasks 5–7)
`workspace.go` helpers, policy methods, `judge.go`. Pure Go, real-git + stub-CLI fixtures. **Gate-verifiable:** fully (`go test ./agent/harvest/...` — git is in the agent-runner image, proven by tickets #13/#14). **Local-verify:** `go test ./agent/harvest/ -count=1`. **Live-verify:** none.

### Slice D — Flight recorder + judge in the runner (Tasks 8–9)
`agent/schema` additive keys; `Run()` gains flightDir, events, manifest, evidence, judge execution (runner-boundary relaxation — safe alone: exec/render still refuse judge, so no judge config reaches a pod until Slice E). **Gate-verifiable:** `agent/harvest` fully; **`agent/schema` is NOT covered by the root `go test ./...`** (nested module) — the ticket spec must state `cd agent/schema && go test ./...` explicitly and a human re-runs it pre-merge. **Local-verify:** `cd agent/schema && go test ./... && cd ../.. && go test ./agent/harvest/ -count=1 && go build ./...`. **Live-verify:** none yet (Slice H smokes the built image).

### Slice E — Exec + renderer judge admission (Tasks 10–11)
Platform-credential SecretEnv, flight output, HARVEST_CONFIG judge, renderer emission; relaxes the remaining two boundaries together (lockstep complete). **Gate-verifiable:** `agent/dispatch` plain-Go; `atc/exec` Ginkgo runs without postgres. **Local-verify:** `ginkgo ./atc/exec/ ./atc/engine/ && go test ./agent/dispatch/`. **Live-verify:** deferred to Slice H (end-to-end judged harvest).

### Slice F — Exec ingestion (Task 12)
Metrics/evidence/ledger recording in `exec.HarvestStep`, all exit paths. **Gate-verifiable:** `atc/exec` Ginkgo (fake-driven, no postgres). **Local-verify:** `ginkgo ./atc/exec/`; then `ginkgo ./atc/db/` once (Slice B interplay). **Live-verify:** Slice H.

### Slice G — Elm + public plan (Task 13)
`public_plan.go` Harvest dispatch + Elm union/decoders/StepTree + bundle rebuild. **Gate-verifiable:** only the Go touchpoint (`ginkgo ./atc/`). **Local-verify:** Elm compile is the test (`cd web && yarn && yarn build`; exhaustiveness errors are the failing-test step), plus a manual look at a local build page. **Live-verify:** open a live agentic build page on concourse.home after deploy; step tree renders.

### Slice H — Verification close-out (Tasks 14–16)
Fixture e2e remainder, live credential isolation, full-suite close-out. **Gate-verifiable:** fixture e2e (pure Go). **Local-verify:** `make test-quick`. **Live-verify:** `TestLiveHarvestCredentialIsolation` on a throwaway theborg namespace + one real judged harvest on a scratch ticket.

---

## Tasks

### Task 1 (Slice A): Contract addendum — execute plan 09 Task 1 as written, with the deltas below

Execute `09-harvest-step.md` Task 1 (the fenced §2.8.1 / §6.3-retries / §6.4.1 / §11 texts are still the normative content to insert). Deltas accumulated since it was written:

- [ ] **Anchors moved.** §2.8's closing "Render-time-resolution rule" paragraph is now at `00-shared-contracts.md:857`; the standalone `### 2.8.1 Harvest push addendum (2026-07-09, F32)` block is at `:859` — fold it per Task 1's own instruction (insert the merged subsection ABOVE it, delete the standalone heading line, keep its body as the closing "Push pin" paragraph). §6.3's fenced `Gate` struct is at `:1535-1545` (add `Retries` after `Timeout`); §6.4 body at `:1550-1552`; insert §6.4.1 before `## 7.` at `:1554`; append §11 entries after the v0.5 entry at `:1931`.
- [ ] **Date the new §11 entries 2026-07-17** (not 2026-07-08/09 — those drafts predate landing; the log is append-only and the existing 2026-07-17/-18 harvest entries stay untouched). Note in the entry that the §2.8.1 body-merge is a RECONCILIATION: the code landed first (v0/v0.5 entries), the body text now catches up.
- [ ] **F34 sentence correction.** Task 1's §2.8.1 text says the sidecar wait uses "the shared `agent/devmcp.WaitHealthy` helper" — that helper does not exist (`waitForSidecars`/`waitHealthy` are unexported in `agent/runner/runner.go:368-391`). Rewrite the F34 sentence to: "harvest-runner performs the same 2s-interval/60s-bound `GET /healthz` wait agent-runner uses (extraction into a shared `agent/devmcp` helper lands with the dev-mcp executor swap); a never-healthy sidecar is exit 2." Mark the whole dev-mcp leg of §2.8.1 (the `DevMCP` field, `DEV_MCP_URL` env) as "declared, refused until the dev-mcp executor swap".
- [ ] **Pin the results.json shape [DECISION D3 — recommended: shared schema form].** Add to §2.8.1's flight-dir bullet: "`results.json` is `agent/schema.Results` (`schema_version: "1.0"`, `status: pass|fail|error`, `summary` non-empty, `artifacts: []`, `metadata` map carrying `pushed_branch`, `head_sha`, `base_sha`, `detail`, `gates` (GateOutcome array, the landed v0.5 JSON shape: `gate/scope/status/attempt/flaky/duration_seconds/detail`), `judge`, `judge_error`). STDOUT mirrors the identical document — the v0.5 local `{status, metadata{...}}` stdout shape is superseded; no machine consumer reads stdout (verified: the exec keys off the exit code only), so this is a build-log-only change."
- [ ] **§6.4.1 additions beyond the plan-09 text:** (a) `agent/schema` gains additive `Category` constants `gate` and `judge` (§5 producers-may-add rule — additive vocabulary keeping the shared §5 finding-category set complete for any future consumer that validates through `schema.Category`; NOT an enforcement gate on harvest's own path, which uses a bare-string `EvidenceIssue.Category` and never calls `Category.Validate()` — see Task 8's rationale); (b) harvest evidence upserts record `submitted_by = 'harvest'` (ground-state correction #8); (c) `GateOutcome`'s landed field spellings (`attempt`, `detail`) are the evidence-payload `gates` shape — plan 09's `attempts`/`summary`/`output_tail` sketch is superseded.
- [ ] Commit: `git add docs/superpowers/plans/agentic-platform/00-shared-contracts.md && git commit -m "docs(agentic): harvest contract body reconciliation - merged 2.8.1, 6.3 retries, 6.4.1 judge conventions"`

---

### Task 2 (Slice B): Migration 1773106080 — execute plan 09 Task 2 as written, with the deltas below

Execute `09-harvest-step.md` Task 2 (SQL verbatim from §1.10 at `00-shared-contracts.md:394-403`; up + down files exactly as fenced there). Deltas:

- [ ] **Dual-constant rule (C2) — plan 09 Task 2 predates it and names only the test constant.** Bump BOTH in the same commit: `atc/db/migration/legacy_upgrade_test.go:37` `jetbridgeHeadMigration` **1773106066 → 1773106080**, and `docs/migration/migrate-preflight.sh:38` `JETBRIDGE_VERSION=1773106066` → `1773106080`.
- [ ] **Current head is 1773106066** (ground-state correction #1 — NOT 1773106064, and NOT "maybe 1773106070" as plan 09 hedged). platform-mcp-hitl's 1773106070–79 block has NOT landed; landing 1773106080 moves the version pointer past it (see "Migration allocations" for the accepted consequence and the alternative).
- [ ] Run `pg_isready && ginkgo --focus='Legacy Database Upgrade' ./atc/db/migration/` — expect green.
- [ ] Commit: `git add atc/db/migration docs/migration && git commit -m "feat(db): agent_reviews/agent_feedback ticket linkage columns (migration 1773106080, C2 dual bump)"`

---

### Task 3 (Slice B): `agent/api/reviews` linkage fields + `ListByTicket` — execute plan 09 Task 3 as written, with the deltas below

Execute `09-harvest-step.md` Task 3 (test text and implementation stand). Deltas:

- [ ] **`StoredReview` grew since planning** (`agent/api/reviews/types.go:69-96`): `EvaluatedCount` and `SubmittedBy` now trail the struct. Insert `TicketID`/`PipelineRunID` (with the plan's comment) after `Review json.RawMessage` at `:86`, BEFORE `CreatedAt` — do not disturb the two newer fields (C3).
- [ ] The `Store` interface is at `:128-139` (not `:122`); add `ListByTicket` after `ListByTeam` as planned.
- [ ] **`MemoryStore` shape** (`agent/api/reviews/memory_store.go`): records live in `records []*StoredReview` guarded by `mu sync.Mutex` on receiver `m`, insertion-ordered with `CreatedAt` stamped at upsert. Implement `ListByTicket` with the plan's sort-by-BuildID body adapted to receiver `m` and the `[]*StoredReview` slice (dereference into the result copy, as `GetByBuild` does).
- [ ] **TWO `go build ./...` breaks, not one** (plan 09 predates the counterfeiter fake). Adding `ListByTicket` to `reviews.Store` breaks BOTH: (a) `atc/db/agent_reviews_factory.go`'s `agentReviewsFactory` struct (missing the method — Task 4 implements it); AND (b) the package-level compile-time assertion `var _ db.AgentReviewsFactory = new(FakeAgentReviewsFactory)` at `atc/db/dbfakes/fake_agent_reviews_factory.go:266` — a NON-test file that `go build ./...` compiles regardless of any test importing it, so the fake missing `ListByTicket` fails the whole build. Both close in Task 4 (its new regen step). Proceed straight into Task 4 before expecting a green `go build ./...`, or commit `agent/` alone as the plan sanctions.

---

### Task 4 (Slice B): `atc/db` factories — execute plan 09 Task 4 as written, with the deltas below

Execute `09-harvest-step.md` Task 4 (spec text, COALESCE upsert clauses, `ListByTicket` query, feedback backfill subselect all stand). Deltas:

- [ ] **Line anchors are stale.** `agent_reviews_factory.go` has since gained the feedback-count subselect and the `submitted_by` column; re-locate `Upsert`'s `Columns(...)`/`Values(...)`, `reviewColumns`, `scanReviewRows`, and `ListByTeam` by symbol, not line. Same for `agent_feedback_factory.go` `Save`.
- [ ] **C3 hard mode:** the upsert edit varies EVERY `ON CONFLICT ... DO UPDATE SET` column between insert payload and conflict payload — after editing, diff and confirm no pre-existing column (`submitted_by`, the feedback-count machinery, `updated_at = now()`) was dropped or reordered out of the SET list.
- [ ] The evidence writer (Task 12) will pass `SubmittedBy: "harvest"` — no factory change needed for that (the column and its upsert clause already exist); just confirm `submitted_by` flows through `Upsert` for a server-side (non-HTTP) caller.
- [ ] **Regenerate the counterfeiter fake** (break (b) from Task 3): after `agentReviewsFactory` implements `ListByTicket`, run `go generate ./atc/db/...` (the `//go:generate go run github.com/maxbrunsfeld/counterfeiter/v6 -generate` + `//counterfeiter:generate . AgentReviewsFactory` directives at `agent_reviews_factory.go:11-13`; counterfeiter v6.12.1 is in `go.mod`) so `FakeAgentReviewsFactory` gains the `ListByTicket` stub + call-tracking. Then confirm `go build ./...` succeeds repo-wide (the assertion at `dbfakes/fake_agent_reviews_factory.go:266` now holds). If `go generate` is unavailable in the executor's environment, hand-add the `ListByTicketStub`/`listByTicketMutex`/`...ArgsForCall` fields + method by mirroring the sibling `ListByTeam` block already in that file.
- [ ] Full close: `go build ./...` green repo-wide, `ginkgo ./atc/db/` green, then commit as planned.

---

### Task 5 (Slice C): `agent/harvest` git-context helpers — NEW (plan 09 Task 7 subset, plain-Go)

The judge needs `target_branch..HEAD` context (base sha + truncated diff) and the flight recorder needs the patch manifest. Plan 09 Task 7's `Push`/`Porcelain` helpers are NOT taken — push and cleanliness landed inline in `runner.go` and stay there (do not duplicate; a second push path is a hazard).

**Files:**
- Create: `agent/harvest/workspace.go`
- Test: `agent/harvest/workspace_test.go`

**Steps:**

- [ ] Write the failing test `agent/harvest/workspace_test.go` (reuses `git(t, ...)` and `workspaceWithRemote(t)` from `runner_test.go` — same package `harvest_test`):

```go
package harvest_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/concourse/concourse/agent/harvest"
)

// workspaceWithHistory: workspaceWithRemote plus a second commit, so the
// manifest has two entries and base..HEAD is non-trivial.
func workspaceWithHistory(t *testing.T) (workspace, remote string) {
	t.Helper()
	workspace, remote = workspaceWithRemote(t)
	os.WriteFile(filepath.Join(workspace, "more.md"), []byte("more work\n"), 0644)
	git(t, workspace, "add", ".")
	git(t, workspace, "commit", "-m", "second agent commit")
	return workspace, remote
}

func TestHeadAndBaseSHA(t *testing.T) {
	ws, _ := workspaceWithHistory(t)

	head, err := harvest.HeadSHA(ws)
	if err != nil || len(head) != 40 {
		t.Fatalf("HeadSHA: %q, %v", head, err)
	}
	base, err := harvest.BaseSHA(ws, "main")
	if err != nil {
		t.Fatalf("BaseSHA: %v", err)
	}
	if base == head {
		t.Fatal("base must differ from head (two commits on top of main)")
	}
	if got := git(t, ws, "rev-parse", "origin/main"); got != base {
		t.Fatalf("base %s != origin/main %s", base, got)
	}
}

func TestBaseSHADefaultsToMain(t *testing.T) {
	ws, _ := workspaceWithHistory(t)
	a, err1 := harvest.BaseSHA(ws, "")
	b, err2 := harvest.BaseSHA(ws, "main")
	if err1 != nil || err2 != nil || a != b {
		t.Fatalf("empty target must default to main: %q/%v vs %q/%v", a, err1, b, err2)
	}
}

func TestChangedPathsAndDiff(t *testing.T) {
	ws, _ := workspaceWithHistory(t)
	base, _ := harvest.BaseSHA(ws, "main")

	paths, err := harvest.ChangedPaths(ws, base)
	if err != nil {
		t.Fatalf("ChangedPaths: %v", err)
	}
	want := map[string]bool{"report.md": true, "more.md": true}
	if len(paths) != 2 || !want[paths[0]] || !want[paths[1]] {
		t.Fatalf("ChangedPaths = %v", paths)
	}

	diff, err := harvest.Diff(ws, base, 1<<20)
	if err != nil || !strings.Contains(diff, "report.md") {
		t.Fatalf("Diff: %v\n%s", err, diff)
	}

	tiny, err := harvest.Diff(ws, base, 10)
	if err != nil {
		t.Fatalf("Diff truncated: %v", err)
	}
	if !strings.HasSuffix(tiny, harvest.DiffTruncatedMarker) {
		t.Fatalf("truncated diff must end with the marker: %q", tiny)
	}
	if len(tiny) > 10+len(harvest.DiffTruncatedMarker) {
		t.Fatalf("truncated diff too long: %d", len(tiny))
	}
}

func TestBuildManifest(t *testing.T) {
	ws, _ := workspaceWithHistory(t)
	head, _ := harvest.HeadSHA(ws)
	base, _ := harvest.BaseSHA(ws, "main")

	m, err := harvest.BuildManifest(ws, base, head, "tdmtrader/concourse", "agent/ticket-42")
	if err != nil {
		t.Fatalf("BuildManifest: %v", err)
	}
	if m.Repo != "tdmtrader/concourse" || m.Branch != "agent/ticket-42" ||
		m.BaseSHA != base || m.HeadSHA != head {
		t.Fatalf("manifest header wrong: %+v", m)
	}
	if len(m.Commits) != 2 {
		t.Fatalf("want 2 commits oldest-first, got %+v", m.Commits)
	}
	if m.Commits[0].Subject != "agent work for ticket 42" {
		t.Fatalf("oldest-first violated: %+v", m.Commits)
	}
	if len(m.Files) != 2 {
		t.Fatalf("want 2 files, got %+v", m.Files)
	}
	for _, f := range m.Files {
		if f.Added < 1 {
			t.Fatalf("numstat not parsed: %+v", f)
		}
	}
}
```

- [ ] Run `go test ./agent/harvest/ -run 'TestHead|TestBase|TestChanged|TestBuildManifest' -count=1` — expect compile failure.
- [ ] Write `agent/harvest/workspace.go` — plan 09 Task 7's implementations for `runGit`, `DiffTruncatedMarker`, `HeadSHA`, `BaseSHA`, `ChangedPaths`, `Diff`, `Manifest`/`ManifestCommit`/`ManifestFile`, `BuildManifest` VERBATIM (`09-harvest-step.md:1265-1405`), MINUS `Porcelain` and `Push` (and their imports: drop `net/url`, `os`, `path/filepath` — keep `context`, `fmt`, `os/exec`, `strconv`, `strings`).
- [ ] Run the focused tests — expect pass. Run `go vet ./agent/harvest/`.
- [ ] Commit: `git add agent/harvest && git commit -m "feat(harvest): git context helpers - head/base/changed-paths/diff/manifest (plan 09 Task 7 subset)"`

---

### Task 6 (Slice C): `JudgeConfig.Validate` + `RubricHash` — NEW (policy.go additions)

`RunJudge` (Task 7) calls both; neither exists (`agent/harvest/policy.go` has no methods on `JudgeConfig`). Stdlib-only additions, keeping policy.go importable by `atc` without weight.

**Files:**
- Modify: `agent/harvest/policy.go`
- Test: `agent/harvest/policy_test.go` (append)

**Steps:**

- [ ] Append failing tests to `agent/harvest/policy_test.go`:

```go
func TestJudgeConfigValidate(t *testing.T) {
	valid := harvest.JudgeConfig{
		Rubric:        []harvest.RubricDimension{{Name: "correctness", Weight: 3, Guidance: "works"}},
		PassThreshold: 6.5,
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid config rejected: %v", err)
	}

	cases := map[string]harvest.JudgeConfig{
		"empty rubric":    {PassThreshold: 5},
		"unnamed dim":     {Rubric: []harvest.RubricDimension{{Weight: 1}}, PassThreshold: 5},
		"duplicate dim":   {Rubric: []harvest.RubricDimension{{Name: "a", Weight: 1}, {Name: "a", Weight: 1}}, PassThreshold: 5},
		"non-positive wt": {Rubric: []harvest.RubricDimension{{Name: "a", Weight: 0}}, PassThreshold: 5},
		"threshold > 10":  {Rubric: []harvest.RubricDimension{{Name: "a", Weight: 1}}, PassThreshold: 11},
		"threshold < 0":   {Rubric: []harvest.RubricDimension{{Name: "a", Weight: 1}}, PassThreshold: -1},
	}
	for name, cfg := range cases {
		if err := cfg.Validate(); err == nil {
			t.Errorf("%s: expected validation error", name)
		}
	}
}

func TestRubricHashDeterministicAndOrderSensitive(t *testing.T) {
	a := harvest.JudgeConfig{Rubric: []harvest.RubricDimension{
		{Name: "x", Weight: 1, Guidance: "g"}, {Name: "y", Weight: 2, Guidance: "h"},
	}}
	b := harvest.JudgeConfig{Rubric: []harvest.RubricDimension{
		{Name: "y", Weight: 2, Guidance: "h"}, {Name: "x", Weight: 1, Guidance: "g"},
	}}
	if a.RubricHash() == "" || len(a.RubricHash()) != 64 {
		t.Fatalf("hash must be sha256 hex: %q", a.RubricHash())
	}
	if a.RubricHash() != a.RubricHash() {
		t.Fatal("hash must be deterministic")
	}
	if a.RubricHash() == b.RubricHash() {
		t.Fatal("dimension order is semantic (prompt order) - hash must differ")
	}
}
```

- [ ] Run `go test ./agent/harvest/ -run 'TestJudgeConfig|TestRubricHash' -count=1` — expect compile failure.
- [ ] Add to `agent/harvest/policy.go` (imports grow by `crypto/sha256`, `encoding/hex`, `encoding/json` — still stdlib-only):

```go
// Validate rejects a judge config the runner could not execute
// faithfully (§6.4.1). Mirrors agent/workflow's import-time checks so a
// hand-authored harvest step gets the same fail-closed treatment.
func (j JudgeConfig) Validate() error {
	if len(j.Rubric) == 0 {
		return fmt.Errorf("judge: rubric must have at least one dimension")
	}
	seen := map[string]bool{}
	for _, d := range j.Rubric {
		if d.Name == "" {
			return fmt.Errorf("judge: rubric dimension name is required")
		}
		if seen[d.Name] {
			return fmt.Errorf("judge: duplicate rubric dimension %q", d.Name)
		}
		seen[d.Name] = true
		if d.Weight <= 0 {
			return fmt.Errorf("judge: rubric dimension %q weight must be > 0", d.Name)
		}
	}
	if j.PassThreshold < 0 || j.PassThreshold > 10 {
		return fmt.Errorf("judge: pass_threshold must be within 0-10, got %g", j.PassThreshold)
	}
	return nil
}

// RubricHash is the judge.score correlation key (§6.4.1): sha256 hex of
// the rubric's canonical JSON. Dimension order is semantic (it is the
// prompt order), so reordering changes the hash.
func (j JudgeConfig) RubricHash() string {
	payload, _ := json.Marshal(j.Rubric)
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}
```

- [ ] Run the focused tests — expect pass. Commit: `git add agent/harvest && git commit -m "feat(harvest): JudgeConfig.Validate and RubricHash (contracts 6.4.1)"`

---

### Task 7 (Slice C): `RunJudge` — plan 09 Task 9's implementation, plain-Go tests

The `agent/harvest/judge.go` implementation in `09-harvest-step.md:1904-2120` is still valid VERBATIM (envelope parity with ci-agent, `total_cost_usd` fallback, fence-unwrapping, per-dimension validation by name, 0–10 clamping, weighted total, advisory posture) — use it unchanged. Only the test file is re-issued (ground-state correction #2: this package is plain-Go).

**Files:**
- Create: `agent/harvest/judge.go` (from plan 09 Task 9, verbatim)
- Test: `agent/harvest/judge_test.go` (below, replacing the plan's Ginkgo skeleton)

**Steps:**

- [ ] Write the failing test `agent/harvest/judge_test.go`:

```go
package harvest_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/concourse/concourse/agent/harvest"
)

// stubClaude writes an executable that emits the given CLI envelope.
func stubClaude(t *testing.T, envelope string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "claude")
	script := "#!/bin/sh\necho '" + envelope + "'\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

var judgeCfg = harvest.JudgeConfig{
	Rubric: []harvest.RubricDimension{
		{Name: "correctness", Weight: 3, Guidance: "does it work"},
		{Name: "tests", Weight: 1, Guidance: "are behaviors covered"},
	},
	PassThreshold: 6.5,
	Model:         "claude-sonnet-4-5",
}

func TestRunJudgeScoresWeightsAndIssues(t *testing.T) {
	// correctness 8 (weight 3), tests 4 (weight 1) -> (24+4)/4 = 7.0
	envelope := `{"type":"result","subtype":"success","result":"{\"dimensions\":[{\"name\":\"correctness\",\"score\":8,\"rationale\":\"solid\",\"issues\":[]},{\"name\":\"tests\",\"score\":4,\"rationale\":\"thin\",\"issues\":[{\"title\":\"missing edge case\",\"description\":\"no nil test\",\"file\":\"x.go\",\"line\":10}]}]}","model":"claude-sonnet-4-5","total_cost_usd":0.31,"num_turns":1,"is_error":false,"usage":{"input_tokens":900,"output_tokens":120}}`

	res, err := harvest.RunJudge(context.Background(), judgeCfg, harvest.JudgeOpts{
		ClaudePath: stubClaude(t, envelope),
		WorkDir:    t.TempDir(),
		Diff:       "diff --git a/x.go b/x.go",
	})
	if err != nil {
		t.Fatalf("RunJudge: %v", err)
	}
	if res.Total < 6.999 || res.Total > 7.001 {
		t.Fatalf("weighted total = %v, want 7.0", res.Total)
	}
	if res.MaxTotal != 10 || !res.Pass {
		t.Fatalf("MaxTotal/Pass wrong: %+v", res)
	}
	if res.RubricHash != judgeCfg.RubricHash() {
		t.Fatal("rubric hash mismatch")
	}
	if len(res.Dimensions) != 2 || res.Dimensions[0].Name != "correctness" || res.Dimensions[0].Score != 8 {
		t.Fatalf("dimensions wrong: %+v", res.Dimensions)
	}
	if len(res.Issues) != 1 || res.Issues[0].Dimension != "tests" || res.Issues[0].Title != "missing edge case" {
		t.Fatalf("issues wrong: %+v", res.Issues)
	}
	if res.CostUSD < 0.309 || res.CostUSD > 0.311 || res.Model != "claude-sonnet-4-5" {
		t.Fatalf("cost/model wrong: %+v", res)
	}
}

func TestRunJudgeMissingDimensionErrors(t *testing.T) {
	envelope := `{"type":"result","subtype":"success","result":"{\"dimensions\":[{\"name\":\"correctness\",\"score\":8,\"rationale\":\"r\",\"issues\":[]}]}","model":"m","cost_usd":0.1,"num_turns":1,"is_error":false,"usage":{}}`
	_, err := harvest.RunJudge(context.Background(), judgeCfg, harvest.JudgeOpts{
		ClaudePath: stubClaude(t, envelope), WorkDir: t.TempDir(),
	})
	if err == nil || !strings.Contains(err.Error(), `missing dimension "tests"`) {
		t.Fatalf("want missing-dimension error, got %v", err)
	}
}

func TestRunJudgeCLIErrorEnvelope(t *testing.T) {
	envelope := `{"type":"result","subtype":"error_during_execution","result":"\"\"","is_error":true,"usage":{}}`
	_, err := harvest.RunJudge(context.Background(), judgeCfg, harvest.JudgeOpts{
		ClaudePath: stubClaude(t, envelope), WorkDir: t.TempDir(),
	})
	if err == nil || !strings.Contains(err.Error(), "judge CLI reported error") {
		t.Fatalf("want CLI-error, got %v", err)
	}
}

func TestRunJudgeScoresAreClamped(t *testing.T) {
	envelope := `{"type":"result","subtype":"success","result":"{\"dimensions\":[{\"name\":\"correctness\",\"score\":15,\"rationale\":\"r\",\"issues\":[]},{\"name\":\"tests\",\"score\":-3,\"rationale\":\"r\",\"issues\":[]}]}","model":"m","cost_usd":0.1,"num_turns":1,"is_error":false,"usage":{}}`
	res, err := harvest.RunJudge(context.Background(), judgeCfg, harvest.JudgeOpts{
		ClaudePath: stubClaude(t, envelope), WorkDir: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("RunJudge: %v", err)
	}
	// clamped: (10*3 + 0*1)/4 = 7.5
	if res.Total < 7.499 || res.Total > 7.501 {
		t.Fatalf("clamping failed: total = %v", res.Total)
	}
}

func TestRunJudgeInvalidConfigRefused(t *testing.T) {
	_, err := harvest.RunJudge(context.Background(), harvest.JudgeConfig{}, harvest.JudgeOpts{
		ClaudePath: "claude-not-called", WorkDir: t.TempDir(),
	})
	if err == nil {
		t.Fatal("empty rubric must be refused before any CLI call")
	}
}
```

- [ ] Run `go test ./agent/harvest/ -run TestRunJudge -count=1` — expect compile failure.
- [ ] Create `agent/harvest/judge.go` with plan 09 Task 9's implementation verbatim (`09-harvest-step.md:1904-2120`). It imports `agent/schema` for `JudgeScoreDimension` — the main module already requires the nested module (agent-step precedent), so this compiles.
- [ ] Run the focused tests — expect pass; `go vet ./agent/harvest/`.
- [ ] Commit: `git add agent/harvest && git commit -m "feat(harvest): schema-constrained rubric judge (contracts 6.4.1, plan 09 Task 9)"`

---

### Task 8 (Slice D): `agent/schema` additive keys — `GateResultData.attempt/flaky`, categories `gate`/`judge`

Nested stdlib-only module; the §5 producers-may-add rule sanctions both. NOT covered by the repo-root `go test ./...` — run its suite explicitly.

**Files:**
- Modify: `agent/schema/event_payloads.go` (`GateResultData`)
- Modify: `agent/schema/review.go` (`Category` constants + `validCategories` + error message)
- Test: `agent/schema/event_payloads_test.go`, `agent/schema/review_test.go` (append; both are plain-Go)

**Steps:**

- [ ] Append failing tests. To `agent/schema/event_payloads_test.go`:

```go
func TestGateResultDataAttemptFlakyRoundTrip(t *testing.T) {
	in := GateResultData{Gate: "test", Scope: "full", Status: "ok",
		DurationSeconds: 2.5, Summary: "passed on retry", Attempt: 2, Flaky: true}
	raw, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"attempt":2`) || !strings.Contains(string(raw), `"flaky":true`) {
		t.Fatalf("additive keys missing: %s", raw)
	}
	var out GateResultData
	if err := json.Unmarshal(raw, &out); err != nil || out.Attempt != 2 || !out.Flaky {
		t.Fatalf("round-trip failed: %+v, %v", out, err)
	}
	// first-attempt passes omit both keys (omitempty) so old consumers
	// see byte-identical events
	clean, _ := json.Marshal(GateResultData{Gate: "test", Scope: "full", Status: "ok"})
	if strings.Contains(string(clean), "attempt") || strings.Contains(string(clean), "flaky") {
		t.Fatalf("omitempty violated: %s", clean)
	}
}
```

  (add `"strings"` to that file's imports if absent). To `agent/schema/review_test.go`:

```go
func TestGateAndJudgeCategoriesAreValid(t *testing.T) {
	if err := CategoryGate.Validate(); err != nil {
		t.Fatalf("gate category: %v", err)
	}
	if err := CategoryJudge.Validate(); err != nil {
		t.Fatalf("judge category: %v", err)
	}
}
```

- [ ] Run `cd agent/schema && go test ./...` — expect compile failure.
- [ ] In `event_payloads.go`, extend `GateResultData` (ADD after `LogArtifact`, C3):

```go
	// Attempt/Flaky surface the §6.3 flake stance (2026-07-17 harvest
	// addendum): a pass on attempt N>1 is ok + flaky:true — flakiness is
	// surfaced, never hidden. Omitted on first-attempt results.
	Attempt int  `json:"attempt,omitempty"`
	Flaky   bool `json:"flaky,omitempty"`
```

- [ ] In `review.go`, ADD constants + `validCategories` entries (C3 — alongside, never replacing) and extend the `Validate` error message to name the two new values. **Rationale (do NOT claim this gates harvest's path):** nothing in harvest's actual evidence pipeline calls `schema.Category.Validate()` — Task 9's `EvidenceIssue.Category` is a bare `string` and the exec ingestion parses `reviews.ReviewPayload`'s raw-JSON findings; even in `review.go` `Observation.Validate()` only checks `Category != ""` and `ProvenIssue.Validate()` never checks its `Category` at all — neither calls `Category.Validate()`, so the closed set is unenforced everywhere but the category's own unit test. This addition is additive shared-§5 vocabulary so the enum stays complete for any FUTURE consumer that does validate (and so `TestGateAndJudgeCategoriesAreValid` passes) — it is not an enforcement mechanism this plan relies on. All three edits (the constant block, the map entries, the error string):

```go
	// constant block (ADD after CategoryTesting):
	CategoryGate  Category = "gate"  // objectively-proven gate failure (§6.4.1)
	CategoryJudge Category = "judge" // judge-cited advisory finding (§6.4.1)
```

```go
	// validCategories map (ADD after CategoryTesting: true):
	CategoryGate:  true,
	CategoryJudge: true,
```

```go
	// Validate error string — name the two new values:
	return fmt.Errorf("invalid category %q: must be one of security, correctness, performance, maintainability, testing, gate, judge", c)
```

- [ ] Run `cd agent/schema && go test ./...` — expect pass. Then `cd ../.. && go build ./...` (main module unaffected but confirm).
- [ ] Commit: `git add agent/schema && git commit -m "feat(schema): GateResultData attempt/flaky + gate/judge finding categories (contracts 5/6.4.1 additive)"`

---

### Task 9 (Slice D): harvest-runner flight recorder + judge execution — MATERIALLY AMENDED (supersedes plan 09 Task 10's orchestration; extends the landed `Run`)

The centerpiece. `Run` gains a `flightDir` parameter (empty = the deployed pre-flight exec; all flight outputs skipped — backward compatible), emits §5 events, writes `manifest.json`/`review.json`/`results.json`, converges the results shape onto `agent/schema.Results` (Decision D3, pinned in Task 1), and replaces the judge refusal with execution AFTER head-sha capture (push-by-sha means judge workspace mutation cannot alter the push). `RunGates` gains a nil-tolerant events parameter so `gate.start`/`gate.result` are emitted live per attempt — the `GateOutcome` JSON shape is untouched (ground-state correction #6).

**Boundary-lockstep note:** this slice moves ONLY the runner boundary from refuse→execute. Exec and render still refuse judge configs, so no judge reaches a pod until Slice E — relaxing the innermost boundary first is the safe direction (a declared policy block still cannot be silently skipped anywhere).

**Files:**
- Create: `agent/harvest/flight.go`
- Create: `agent/harvest/evidence.go`
- Modify: `agent/harvest/runner.go` (full replacement of `Run`; `Results`/`ResultsMetadata` types deleted in favor of `schema.Results`)
- Modify: `agent/harvest/gates.go` (`RunGates`/`runGate` events parameter)
- Modify: `cmd/harvest-runner/main.go` (pass `AGENT_FLIGHT_DIR`)
- Test: `agent/harvest/flight_test.go` (new), `agent/harvest/runner_test.go` + `gates_test.go` (mechanical call-site updates)

**Steps:**

- [ ] Write the failing test `agent/harvest/flight_test.go`:

```go
package harvest_test

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/concourse/concourse/agent/harvest"
	schema "github.com/concourse/concourse/agent/schema"
)

func readResults(t *testing.T, flight string) schema.Results {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(flight, "results.json"))
	if err != nil {
		t.Fatalf("results.json: %v", err)
	}
	var res schema.Results
	if err := json.Unmarshal(raw, &res); err != nil {
		t.Fatalf("results.json unmarshal: %v", err)
	}
	if err := res.Validate(); err != nil {
		t.Fatalf("results.json invalid: %v", err)
	}
	return res
}

func eventTypes(t *testing.T, flight string) []string {
	t.Helper()
	f, err := os.Open(filepath.Join(flight, "events.ndjson"))
	if err != nil {
		t.Fatalf("events.ndjson: %v", err)
	}
	defer f.Close()
	var types []string
	r := schema.NewEventReader(f)
	for {
		e, err := r.Read()
		if err != nil {
			break
		}
		types = append(types, string(e.Type))
	}
	return types
}

func contains(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}

func TestRunFlightOutputsOnPass(t *testing.T) {
	ws, remote := workspaceWithRemote(t)
	flight := t.TempDir()
	cfg := harvest.Config{StepName: "harvest", Workspace: "workspace",
		Repo: "tdmtrader/scratch", TargetBranch: "main",
		TicketID: 42, Branch: "agent/ticket-42", Push: true}

	var out bytes.Buffer
	exit := harvest.Run(cfg, ws, "", flight, &out)
	if exit != 0 {
		t.Fatalf("exit = %d: %s", exit, out.String())
	}

	res := readResults(t, flight)
	if res.Status != schema.StatusPass || res.SchemaVersion != "1.0" {
		t.Fatalf("results: %+v", res)
	}
	if res.Metadata["pushed_branch"] != "agent/ticket-42" {
		t.Fatalf("metadata: %+v", res.Metadata)
	}
	head := git(t, ws, "rev-parse", "HEAD")
	if res.Metadata["head_sha"] != head {
		t.Fatalf("head_sha: %+v", res.Metadata)
	}
	if got := git(t, remote, "rev-parse", "refs/heads/agent/ticket-42"); got != head {
		t.Fatalf("pushed %s, want %s", got, head)
	}

	// stdout mirrors the identical document
	var mirror schema.Results
	if err := json.Unmarshal(out.Bytes(), &mirror); err != nil || mirror.Status != schema.StatusPass {
		t.Fatalf("stdout mirror: %v / %+v", err, mirror)
	}

	types := eventTypes(t, flight)
	for _, want := range []string{"step.start", "push.done", "step.end"} {
		if !contains(types, want) {
			t.Fatalf("missing event %s in %v", want, types)
		}
	}

	// manifest: base resolvable (clone has origin/main) -> written
	var m harvest.Manifest
	raw, err := os.ReadFile(filepath.Join(flight, "manifest.json"))
	if err != nil {
		t.Fatalf("manifest.json: %v", err)
	}
	if err := json.Unmarshal(raw, &m); err != nil || m.HeadSHA != head || len(m.Commits) != 1 {
		t.Fatalf("manifest: %+v, %v", m, err)
	}

	// evidence: schema_version harvest/1, pass, commit = head
	var ev harvest.Evidence
	raw, err = os.ReadFile(filepath.Join(flight, "review.json"))
	if err != nil {
		t.Fatalf("review.json: %v", err)
	}
	if err := json.Unmarshal(raw, &ev); err != nil {
		t.Fatalf("evidence unmarshal: %v", err)
	}
	if ev.SchemaVersion != "harvest/1" || !ev.Score.Pass || ev.Metadata.Commit != head {
		t.Fatalf("evidence: %+v", ev)
	}
}

func TestRunFlightGateFailureEvidence(t *testing.T) {
	ws, remote := workspaceWithRemote(t)
	seedGoModule(t, ws, map[string]string{
		"go.mod":    gateFixtureGoMod,
		"main.go":   gateFixtureMain,
		"f_test.go": "package p\n\nimport \"testing\"\n\nfunc TestF(t *testing.T) {\n\tt.Fatal(\"always fails\")\n}\n",
	})
	flight := t.TempDir()
	cfg := harvest.Config{StepName: "harvest", Repo: "r", TargetBranch: "main",
		TicketID: 42, Branch: "agent/ticket-42", Push: true,
		GatePolicy: harvest.GatePolicy{Gates: []harvest.Gate{{Gate: "test", Scope: "full"}}}}

	var out bytes.Buffer
	exit := harvest.Run(cfg, ws, "", flight, &out)
	if exit != 1 {
		t.Fatalf("exit = %d", exit)
	}
	if res := readResults(t, flight); res.Status != schema.StatusFail {
		t.Fatalf("results: %+v", res)
	}
	if got := git(t, remote, "for-each-ref", "refs/heads/agent"); got != "" {
		t.Fatalf("nothing may be pushed on gate failure: %q", got)
	}
	types := eventTypes(t, flight)
	if !contains(types, "gate.start") || !contains(types, "gate.result") {
		t.Fatalf("gate events missing: %v", types)
	}
	raw, _ := os.ReadFile(filepath.Join(flight, "review.json"))
	if !strings.Contains(string(raw), `"gate-test"`) || !strings.Contains(string(raw), `"category":"gate"`) {
		t.Fatalf("gate proven issue missing: %s", raw)
	}
}

func TestRunFlightDirtyEvidence(t *testing.T) {
	ws, _ := workspaceWithRemote(t)
	os.WriteFile(filepath.Join(ws, "wip.txt"), []byte("uncommitted"), 0644)
	flight := t.TempDir()

	var out bytes.Buffer
	exit := harvest.Run(harvest.Config{StepName: "harvest", Repo: "r", Branch: "b", Push: true}, ws, "", flight, &out)
	if exit != 1 {
		t.Fatalf("exit = %d", exit)
	}
	raw, _ := os.ReadFile(filepath.Join(flight, "review.json"))
	if !strings.Contains(string(raw), `"workspace-dirty"`) {
		t.Fatalf("workspace-dirty evidence missing: %s", raw)
	}
	if _, err := os.Stat(filepath.Join(ws, "wip.txt")); err != nil {
		t.Fatal("no auto-discard: uncommitted work must survive (F33)")
	}
}

func TestRunJudgePassRecordedAndPushed(t *testing.T) {
	ws, remote := workspaceWithRemote(t)
	flight := t.TempDir()
	envelope := `{"type":"result","subtype":"success","result":"{\"dimensions\":[{\"name\":\"correctness\",\"score\":9,\"rationale\":\"good\",\"issues\":[{\"title\":\"nit\",\"description\":\"d\",\"file\":\"report.md\",\"line\":1}]}]}","model":"m1","total_cost_usd":0.2,"num_turns":1,"is_error":false,"usage":{"input_tokens":10,"output_tokens":5}}`
	t.Setenv("HARVEST_JUDGE_CLI", stubClaude(t, envelope))

	cfg := harvest.Config{StepName: "harvest", Repo: "r", TargetBranch: "main",
		TicketID: 42, Branch: "agent/ticket-42", Push: true,
		Judge: &harvest.JudgeConfig{
			Rubric:        []harvest.RubricDimension{{Name: "correctness", Weight: 1, Guidance: "g"}},
			PassThreshold: 6.5,
		}}

	var out bytes.Buffer
	exit := harvest.Run(cfg, ws, "", flight, &out)
	if exit != 0 {
		t.Fatalf("exit = %d: %s", exit, out.String())
	}
	head := git(t, ws, "rev-parse", "HEAD")
	if got := git(t, remote, "rev-parse", "refs/heads/agent/ticket-42"); got != head {
		t.Fatal("judge pass must still push")
	}
	types := eventTypes(t, flight)
	if !contains(types, "judge.score") || !contains(types, "cost.record") {
		t.Fatalf("judge events missing: %v", types)
	}
	raw, _ := os.ReadFile(filepath.Join(flight, "review.json"))
	if !strings.Contains(string(raw), `"judge-correctness-1"`) || !strings.Contains(string(raw), `"category":"judge"`) {
		t.Fatalf("judge observation missing: %s", raw)
	}
}

func TestRunJudgeErrorIsAdvisory(t *testing.T) {
	ws, remote := workspaceWithRemote(t)
	flight := t.TempDir()
	// stub CLI that exits 1: judge errors, push must proceed (§6.4)
	dir := t.TempDir()
	bad := filepath.Join(dir, "claude")
	os.WriteFile(bad, []byte("#!/bin/sh\nexit 1\n"), 0o755)
	t.Setenv("HARVEST_JUDGE_CLI", bad)

	cfg := harvest.Config{StepName: "harvest", Repo: "r", TargetBranch: "main",
		Branch: "agent/ticket-9", Push: true,
		Judge: &harvest.JudgeConfig{
			Rubric:        []harvest.RubricDimension{{Name: "c", Weight: 1}},
			PassThreshold: 6.5,
		}}

	var out bytes.Buffer
	exit := harvest.Run(cfg, ws, "", flight, &out)
	if exit != 0 {
		t.Fatalf("judge error must not block: exit = %d: %s", exit, out.String())
	}
	head := git(t, ws, "rev-parse", "HEAD")
	if got := git(t, remote, "rev-parse", "refs/heads/agent/ticket-9"); got != head {
		t.Fatal("push must have happened")
	}
	res := readResults(t, flight)
	if res.Metadata["judge_error"] == nil || res.Metadata["judge_error"] == "" {
		t.Fatalf("judge_error must be recorded: %+v", res.Metadata)
	}
}

func TestRunNoFlightDirStillWorks(t *testing.T) {
	ws, _ := workspaceWithRemote(t)
	var out bytes.Buffer
	exit := harvest.Run(harvest.Config{StepName: "h", Repo: "r", Branch: "b", Push: true}, ws, "", "", &out)
	if exit != 0 {
		t.Fatalf("flightless run (deployed v0.5 exec) must keep working: %d", exit)
	}
	var res schema.Results
	if err := json.Unmarshal(out.Bytes(), &res); err != nil || res.Status != schema.StatusPass {
		t.Fatalf("stdout results: %v / %+v", err, res)
	}
}
```

  (Helpers are same-package (`harvest_test`), reuse — do not redefine: `seedGoModule` is the 3-arg helper at `runner_test.go:176` that writes files into an EXISTING `ws` and `git add`+commits (keeping the tree clean so F33 does not fail the run before gates execute) — do NOT reach for `gates_test.go`'s `writeFixtureModule`, which is 2-arg, allocates a fresh `t.TempDir()`, and never commits; `stubClaude` is created by Task 7's `judge_test.go`; the `gateFixtureGoMod`/`gateFixtureMain` constants also live in `runner_test.go`. There is no `writeGoModule` in the repo — earlier drafts named a helper that never existed.)
- [ ] Run `go test ./agent/harvest/ -run 'TestRunFlight|TestRunJudge|TestRunNoFlight' -count=1` — expect compile failure.
- [ ] Write `agent/harvest/flight.go`:

```go
package harvest

import (
	"encoding/json"
	"os"
	"path/filepath"

	schema "github.com/concourse/concourse/agent/schema"
)

// flightRecorder owns the §2.8.1 flight-dir outputs (events.ndjson,
// results.json, manifest.json, review.json). A nil recorder (no
// AGENT_FLIGHT_DIR — the pre-flight-recorder exec) is a no-op on every
// method: the runner must keep working under the deployed v0.5 exec.
// Recorder failures never break harvest control flow — evidence is
// best-effort, the exit code is the contract.
type flightRecorder struct {
	dir     string
	events  *schema.EventWriter
	eventsF *os.File
}

func newFlightRecorder(dir string) (*flightRecorder, error) {
	if dir == "" {
		return nil, nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	f, err := os.Create(filepath.Join(dir, "events.ndjson"))
	if err != nil {
		return nil, err
	}
	return &flightRecorder{dir: dir, events: schema.NewEventWriter(f), eventsF: f}, nil
}

// eventWriter exposes the writer for RunGates' live gate events; nil
// when there is no flight dir.
func (r *flightRecorder) eventWriter() *schema.EventWriter {
	if r == nil {
		return nil
	}
	return r.events
}

// emit writes one event; the timestamp is set by EventWriter.Write.
func (r *flightRecorder) emit(t schema.EventType, payload any) {
	if r == nil {
		return
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return
	}
	_ = r.events.Write(schema.Event{Type: t, Data: data})
}

// writeJSON writes one flight file, best-effort.
func (r *flightRecorder) writeJSON(name string, v any) {
	if r == nil {
		return
	}
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return
	}
	_ = os.WriteFile(filepath.Join(r.dir, name), append(data, '\n'), 0o644)
}

func (r *flightRecorder) close() {
	if r == nil {
		return
	}
	_ = r.eventsF.Close()
}
```

- [ ] Write `agent/harvest/evidence.go`:

```go
package harvest

// Evidence is the review.json payload (§6.4.1): the existing
// ReviewPayload shape plus additive gates/judge/judge_error keys —
// consumers (atc reviews ingestion, the Elm review embed) ignore
// unknown keys and only count proven_issues/observations.
type Evidence struct {
	SchemaVersion string           `json:"schema_version"` // "harvest/1"
	Metadata      EvidenceMetadata `json:"metadata"`
	Score         EvidenceScore    `json:"score"`
	ProvenIssues  []EvidenceIssue  `json:"proven_issues"`
	Observations  []EvidenceIssue  `json:"observations"`
	Summary       string           `json:"summary"`
	Gates         []GateOutcome    `json:"gates"`
	Judge         *EvidenceJudge   `json:"judge,omitempty"`
	JudgeError    string           `json:"judge_error,omitempty"`
}

type EvidenceMetadata struct {
	Repo        string `json:"repo"`
	Commit      string `json:"commit"`
	Branch      string `json:"branch"`
	AgentModel  string `json:"agent_model"`
	DurationSec int    `json:"duration_seconds"`
}

type EvidenceScore struct {
	Value float64 `json:"value"`
	Max   float64 `json:"max"`
	Pass  bool    `json:"pass"`
}

// EvidenceIssue matches the findings shape the reviews consumers parse
// (id/severity/title/description/file/line/category).
type EvidenceIssue struct {
	ID          string `json:"id"`
	Severity    string `json:"severity,omitempty"`
	Title       string `json:"title"`
	Description string `json:"description,omitempty"`
	File        string `json:"file,omitempty"`
	Line        int    `json:"line,omitempty"`
	Category    string `json:"category"`
}

type EvidenceJudge struct {
	RubricHash     string              `json:"rubric_hash"`
	Dimensions     []EvidenceDimension `json:"dimensions"`
	Total          float64             `json:"total"`
	MaxTotal       float64             `json:"max_total"`
	Pass           bool                `json:"pass"`
	Model          string              `json:"model,omitempty"`
	CostUSD        float64             `json:"cost_usd,omitempty"`
	BudgetExceeded bool                `json:"budget_exceeded,omitempty"`
}

type EvidenceDimension struct {
	Name      string  `json:"name"`
	Score     float64 `json:"score"`
	Max       float64 `json:"max"`
	Rationale string  `json:"rationale,omitempty"`
}
```

- [ ] In `agent/harvest/gates.go`: change `RunGates(policy GatePolicy, workspaceDir string)` → `RunGates(policy GatePolicy, workspaceDir string, events *schema.EventWriter)` and `runGate(gate, workspaceDir)` → `runGate(gate, workspaceDir, events)`; inside the attempt loop emit `schema.EventGateStart` (`GateStartData{Gate: gate.Gate, Scope: gate.Scope}`) immediately before `execGate` and `schema.EventGateResult` (`GateResultData{Gate, Scope, Status, DurationSeconds, Summary: truncate(detail, 4096), Attempt: attempt, Flaky: <this attempt's flaky>}`) immediately after each attempt's outcome is known — every attempt gets a result event (§6.3: flakiness surfaced). Add a nil-tolerant `emitEvent(events, t, payload)` helper (marshal, `events.Write(schema.Event{Type: t, Data: data})`, ignore errors — the recorder must never break control flow) and `truncate(s string, n int) string`. The `GateOutcome` struct, refusal/timeout/retry semantics, and the fixed command map are UNTOUCHED. Callers update: `runner.go` passes the recorder's writer; every `gates_test.go` call site passes `nil` (mechanical).
- [ ] Replace `agent/harvest/runner.go`'s `Run` (and delete the local `Results`/`ResultsMetadata` types) with:

```go
// Run executes the harvest flow against workspaceDir (§2.8.1): refuse
// invalid config (exit 2), verify the committed clean git tree (F33:
// dirty ⇒ fail, exit 1, nothing pushed, nothing auto-discarded),
// resolve head/base + patch manifest, run the §6.3 gate policy, run the
// judge (ADVISORY, §6.4: a judge error is recorded as judge_error and
// never blocks the push), then — only when every gate passed —
// push-by-sha --force-with-lease to the stable ticket branch.
//
// flightDir empty (the pre-flight-recorder exec) skips every flight
// output; stdout always carries the schema.Results document. credsDir
// semantics are unchanged from v0.5.
func Run(cfg Config, workspaceDir, credsDir, flightDir string, out io.Writer) int {
	started := time.Now()

	rec, recErr := newFlightRecorder(flightDir)
	if recErr != nil {
		// a broken flight dir is a platform fault: evidence could not be
		// recorded, so nothing may proceed to a push
		res := buildResults(schema.StatusError, "flight dir: "+recErr.Error(), nil)
		json.NewEncoder(out).Encode(res)
		return 2
	}
	defer rec.close()

	rec.emit(schema.EventStepStart, schema.StepStartData{
		StepName: cfg.StepName,
		BuildID:  envInt("BUILD_ID"),
		PlanID:   os.Getenv("AGENT_PLAN_ID"),
		TicketID: optionalInt(cfg.TicketID),
	})

	facts := &runFacts{}

	finish := func(status schema.Status, detail string) int {
		meta := facts.metadata(detail)
		res := buildResults(status, detail, meta)
		rec.writeJSON("review.json", assembleEvidence(cfg, status, detail, facts, int(time.Since(started).Seconds())))
		rec.writeJSON("results.json", res)
		rec.emit(schema.EventStepEnd, schema.StepEndData{
			StepName: cfg.StepName, Status: stepEndStatus(status),
			Summary: detail, WallTimeSeconds: int(time.Since(started).Seconds()),
			CostUSD: facts.judgeCost(),
		})
		json.NewEncoder(out).Encode(res)
		switch status {
		case schema.StatusPass:
			return 0
		case schema.StatusFail:
			return 1
		default:
			return 2
		}
	}

	// -- admission (the runner-side boundary; exec/render mirror it) --
	if cfg.Judge != nil {
		if err := cfg.Judge.Validate(); err != nil {
			return finish(schema.StatusError, "judge config invalid: "+err.Error())
		}
	}
	if cfg.Push && cfg.Branch == "" {
		return finish(schema.StatusError, "push requires a branch")
	}

	git := func(args ...string) (string, error) {
		cmd := exec.Command("git", args...)
		cmd.Dir = workspaceDir
		var buf strings.Builder
		cmd.Stdout = &buf
		cmd.Stderr = &buf
		err := cmd.Run()
		return strings.TrimSpace(buf.String()), err
	}

	// -- committed clean tree (F33, unchanged semantics) --
	if _, err := git("rev-parse", "--git-dir"); err != nil {
		return finish(schema.StatusFail, "workspace is not a git repository — the agent must commit its work into the workspace checkout")
	}
	status, err := git("status", "--porcelain")
	if err != nil {
		return finish(schema.StatusError, "git status: "+status)
	}
	if status != "" {
		facts.Dirty = status
		return finish(schema.StatusFail, "workspace-dirty: uncommitted changes present (F33) — commit or clean up; nothing was pushed:\n"+status)
	}
	head, err := git("rev-parse", "HEAD")
	if err != nil {
		return finish(schema.StatusFail, "workspace has no commits: "+head)
	}
	facts.HeadSHA = head

	// -- base + manifest (best-effort context: absence degrades the
	//    judge diff and skips manifest.json, never fails the harvest) --
	if base, err := BaseSHA(workspaceDir, cfg.TargetBranch); err == nil {
		facts.BaseSHA = base
		if m, err := BuildManifest(workspaceDir, base, head, cfg.Repo, cfg.Branch); err == nil {
			rec.writeJSON("manifest.json", m)
			facts.ManifestWritten = true
		}
	}

	// -- gates (between cleanliness and push, §6.3; unchanged engine) --
	if len(cfg.GatePolicy.Gates) > 0 {
		outcomes, gatesErr := RunGates(cfg.GatePolicy, workspaceDir, rec.eventWriter())
		facts.Gates = outcomes
		if gatesErr != nil {
			return finish(schema.StatusError, "gate engine failure: "+gatesErr.Error())
		}
		for _, o := range outcomes {
			switch o.Status {
			case "ok":
				continue
			case "failed":
				return finish(schema.StatusFail, fmt.Sprintf("gate %q failed — nothing pushed:\n%s", o.Gate, o.Detail))
			default: // "error"
				return finish(schema.StatusError, fmt.Sprintf("gate %q errored: %s", o.Gate, o.Detail))
			}
		}
	}

	// -- judge (ADVISORY; after head-sha capture so judge-process
	//    workspace mutation can never alter what is pushed — §2.8.1) --
	if cfg.Judge != nil {
		diff := ""
		if facts.BaseSHA != "" {
			if d, err := Diff(workspaceDir, facts.BaseSHA, judgeDiffMaxBytes); err == nil {
				diff = d
			}
		}
		jr, jerr := RunJudge(context.Background(), *cfg.Judge, JudgeOpts{
			ClaudePath: os.Getenv("HARVEST_JUDGE_CLI"), // test seam; "" = "claude"
			WorkDir:    workspaceDir,
			Diff:       diff,
		})
		if jerr != nil {
			facts.JudgeErr = jerr.Error()
		} else {
			facts.Judge = jr
			rec.emit(schema.EventJudgeScore, schema.JudgeScoreData{
				RubricHash: jr.RubricHash, Dimensions: jr.Dimensions,
				Total: jr.Total, MaxTotal: jr.MaxTotal,
				Model: jr.Model, CostUSD: jr.CostUSD,
			})
			rec.emit(schema.EventCostRecord, schema.CostRecordData{
				Source: "harvest_judge", Provider: "anthropic", Model: jr.Model,
				InputTokens: jr.InputTokens, OutputTokens: jr.OutputTokens,
				Turns: jr.Turns, CostUSD: jr.CostUSD,
			})
		}
	}

	if !cfg.Push {
		return finish(schema.StatusPass, passSummary(facts))
	}

	// -- push-by-sha (unchanged v0.5 mechanics: lease, askpass) --
	if _, err := git("remote", "get-url", "origin"); err != nil {
		return finish(schema.StatusFail, "workspace has no origin remote to push to")
	}
	pushArgs := []string{
		"push",
		"--force-with-lease=refs/heads/" + cfg.Branch,
		"origin",
		head + ":refs/heads/" + cfg.Branch,
	}
	cmd := exec.Command("git", pushArgs...)
	cmd.Dir = workspaceDir
	cmd.Env = os.Environ()
	if credsDir != "" {
		askpass, cleanup, err := writeAskpass(credsDir)
		if err != nil {
			return finish(schema.StatusError, "git credentials: "+err.Error())
		}
		defer cleanup()
		cmd.Env = append(cmd.Env, "GIT_ASKPASS="+askpass, "GIT_TERMINAL_PROMPT=0")
	}
	var pushOut strings.Builder
	cmd.Stdout = &pushOut
	cmd.Stderr = &pushOut
	if err := cmd.Run(); err != nil {
		// Auth/network/lease failures are platform faults (the lease only
		// trips on a concurrent harvest, which correctly errors).
		return finish(schema.StatusError, "git push failed: "+pushOut.String())
	}
	facts.PushedBranch = cfg.Branch
	rec.emit(schema.EventPushDone, schema.PushDoneData{
		Branch: cfg.Branch, Sha: head, ManifestArtifact: manifestArtifactName(facts),
	})

	return finish(schema.StatusPass, passSummary(facts))
}
```

  with the supporting pieces in the same file:

```go
const judgeDiffMaxBytes = 256 << 10

// runFacts accumulates everything the finish path folds into results
// metadata, evidence, and step.end.
type runFacts struct {
	HeadSHA, BaseSHA string
	Dirty            string
	Gates            []GateOutcome
	Judge            *JudgeResult
	JudgeErr         string
	PushedBranch     string
	ManifestWritten  bool
}

func (f *runFacts) metadata(detail string) map[string]interface{} {
	m := map[string]interface{}{}
	if detail != "" {
		m["detail"] = detail
	}
	if f.HeadSHA != "" {
		m["head_sha"] = f.HeadSHA
	}
	if f.BaseSHA != "" {
		m["base_sha"] = f.BaseSHA
	}
	if f.PushedBranch != "" {
		m["pushed_branch"] = f.PushedBranch
	}
	if len(f.Gates) > 0 {
		m["gates"] = f.Gates
	}
	if f.Judge != nil {
		m["judge"] = map[string]interface{}{
			"rubric_hash": f.Judge.RubricHash, "total": f.Judge.Total,
			"max_total": f.Judge.MaxTotal, "pass": f.Judge.Pass,
		}
	}
	if f.JudgeErr != "" {
		m["judge_error"] = f.JudgeErr
	}
	return m
}

func (f *runFacts) judgeCost() float64 {
	if f.Judge == nil {
		return 0
	}
	return f.Judge.CostUSD
}

func buildResults(status schema.Status, detail string, meta map[string]interface{}) schema.Results {
	summary := detail
	if summary == "" {
		summary = string(status)
	}
	return schema.Results{
		SchemaVersion: "1.0",
		Status:        status,
		Confidence:    1,
		Summary:       summary,
		Artifacts:     []schema.Artifact{},
		Metadata:      meta,
	}
}

func stepEndStatus(s schema.Status) string {
	switch s {
	case schema.StatusPass:
		return "ok"
	case schema.StatusFail:
		return "failed"
	default:
		return "error"
	}
}

func manifestArtifactName(f *runFacts) string {
	if f.ManifestWritten {
		return "manifest.json"
	}
	return ""
}

func passSummary(f *runFacts) string {
	s := "verified"
	if len(f.Gates) > 0 {
		s = fmt.Sprintf("%d gate(s) ok", len(f.Gates))
	}
	if f.Judge != nil {
		verdict := "fail"
		if f.Judge.Pass {
			verdict = "pass"
		}
		s += fmt.Sprintf("; judge %.1f/10 (%s)", f.Judge.Total, verdict)
	}
	if f.JudgeErr != "" {
		s += "; judge errored (advisory)"
	}
	if f.PushedBranch != "" {
		s += "; pushed " + f.PushedBranch
	}
	return s
}

func envInt(key string) int {
	n, _ := strconv.Atoi(os.Getenv(key))
	return n
}

func optionalInt(v int) *int {
	if v <= 0 {
		return nil
	}
	return &v
}

// assembleEvidence maps runFacts onto the §6.4.1 evidence payload:
// failed gates → proven_issues gate-<gate> (category gate), a dirty
// tree → proven issue workspace-dirty, judge citations → observations
// judge-<dim>-<n> (category judge). score.value = judge total when the
// judge ran, else 10/0 for pass/fail; score.pass = status pass AND (no
// judge OR judge pass OR judge errored).
func assembleEvidence(cfg Config, status schema.Status, detail string, f *runFacts, durationSec int) *Evidence {
	ev := &Evidence{
		SchemaVersion: "harvest/1",
		Metadata: EvidenceMetadata{
			Repo: cfg.Repo, Commit: f.HeadSHA, Branch: cfg.Branch,
			DurationSec: durationSec,
		},
		ProvenIssues: []EvidenceIssue{},
		Observations: []EvidenceIssue{},
		Summary:      detail,
		Gates:        f.Gates,
		JudgeError:   f.JudgeErr,
	}
	if ev.Gates == nil {
		ev.Gates = []GateOutcome{}
	}
	if ev.Summary == "" {
		ev.Summary = string(status)
	}

	if f.Dirty != "" {
		ev.ProvenIssues = append(ev.ProvenIssues, EvidenceIssue{
			ID: "workspace-dirty", Severity: "high",
			Title:       "uncommitted changes in the workspace (F33)",
			Description: f.Dirty, Category: "correctness",
		})
	}
	for _, g := range f.Gates {
		if g.Status == "failed" {
			ev.ProvenIssues = append(ev.ProvenIssues, EvidenceIssue{
				ID: "gate-" + g.Gate, Severity: "high",
				Title:       fmt.Sprintf("gate %q failed", g.Gate),
				Description: truncate(g.Detail, 8192), Category: "gate",
			})
		}
	}
	if f.Judge != nil {
		ev.Metadata.AgentModel = f.Judge.Model
		perDim := map[string]int{}
		for _, iss := range f.Judge.Issues {
			perDim[iss.Dimension]++
			ev.Observations = append(ev.Observations, EvidenceIssue{
				ID:    fmt.Sprintf("judge-%s-%d", iss.Dimension, perDim[iss.Dimension]),
				Title: iss.Title, Description: iss.Description,
				File: iss.File, Line: iss.Line, Category: "judge",
			})
		}
		dims := make([]EvidenceDimension, len(f.Judge.Dimensions))
		for i, d := range f.Judge.Dimensions {
			dims[i] = EvidenceDimension{Name: d.Name, Score: d.Score, Max: d.Max, Rationale: d.Rationale}
		}
		ev.Judge = &EvidenceJudge{
			RubricHash: f.Judge.RubricHash, Dimensions: dims,
			Total: f.Judge.Total, MaxTotal: f.Judge.MaxTotal, Pass: f.Judge.Pass,
			Model: f.Judge.Model, CostUSD: f.Judge.CostUSD,
			BudgetExceeded: cfg.Judge != nil && cfg.Judge.BudgetUSD > 0 && f.Judge.CostUSD > cfg.Judge.BudgetUSD,
		}
	}

	gatesOK := status == schema.StatusPass
	switch {
	case f.Judge != nil:
		ev.Score = EvidenceScore{Value: f.Judge.Total, Max: 10, Pass: gatesOK && f.Judge.Pass}
	case gatesOK:
		ev.Score = EvidenceScore{Value: 10, Max: 10, Pass: true}
	default:
		ev.Score = EvidenceScore{Value: 0, Max: 10, Pass: false}
	}
	if f.JudgeErr != "" {
		// §6.4: an errored judge never blocks — score falls back to gates
		ev.Score = EvidenceScore{Value: 10, Max: 10, Pass: gatesOK}
		if !gatesOK {
			ev.Score.Value = 0
		}
	}
	return ev
}
```

  (imports for runner.go grow by `context`, `strconv`, `time`, and `schema "github.com/concourse/concourse/agent/schema"`; `truncate` lives in gates.go from the events step. `JudgeResult.Dimensions` is already `[]schema.JudgeScoreDimension`, so the `judge.score` emission passes it directly.)
- [ ] Update `cmd/harvest-runner/main.go`: `os.Exit(harvest.Run(cfg, workspaceDir, credsDir, os.Getenv("AGENT_FLIGHT_DIR"), os.Stdout))`.
- [ ] Mechanically update the existing `runner_test.go` call sites: `harvest.Run(cfg, ws, creds, "", &out)` and change stdout decoding from the deleted `harvest.Results` to `schema.Results` (metadata keys move to the map: `res.Metadata["pushed_branch"]`, `res.Metadata["head_sha"]`; gates assertions decode `res.Metadata["gates"]` via re-marshal into `[]harvest.GateOutcome`). Update `gates_test.go` call sites with the `nil` events arg. Do NOT weaken any assertion — the semantics under test are unchanged.
- [ ] Run `go test ./agent/harvest/ -count=1` — full package green. `go build ./... && go vet ./agent/harvest/`.
- [ ] Commit: `git add agent/harvest cmd/harvest-runner && git commit -m "feat(harvest): flight recorder + advisory judge in the runner (contracts 2.8.1/5/6.4.1)"`

---

### Task 10 (Slice E): exec judge admission + platform credential + flight artifact — MATERIALLY AMENDED (plan 09 Task 12 subset against the landed exec)

Relaxes the exec boundary (renderer follows in Task 11 — same slice, lockstep complete). The F20 append semantics this depends on are LANDED (`applySecretRefs`, `container.go:781-819`, live-tested) — plan 09's workaround prose is obsolete; rely on the seam.

**Files:**
- Modify: `atc/exec/harvest_step.go`
- Modify: `atc/engine/step_factory.go` (`HarvestStep` harvestOpts)
- Test: `atc/exec/harvest_step_test.go` (CREATE — none exists; ground-state correction #4)

**Steps:**

- [ ] Create `atc/exec/harvest_step_test.go` by copying the fixture scaffolding from `atc/exec/agent_step_test.go` (fakePool / chosenWorker / chosenContainer / fakeStreamer / fake delegate factory / `exec.NewRunState`; register a `workspace` artifact in the repository as the agent missing-inputs spec does). Baseline fixture plan:

```go
	plan := atc.HarvestPlan{
		Name: "harvest", Workspace: "workspace",
		Repo: "tdmtrader/concourse", TargetBranch: "main",
		TicketID: 42, PipelineRunID: 7, Branch: "agent/ticket-42", Push: true,
		Judge: &harvest.JudgeConfig{
			Rubric:        []harvest.RubricDimension{{Name: "correctness", Weight: 1, Guidance: "g"}},
			PassThreshold: 6.5,
		},
	}
```

  constructed via the LANDED signature `exec.NewHarvestStep(planID, plan, stepMetadata, containerMetadata, fakePool, fakeDelegateFactory, 0, "registry.home/agent-runner:v1", opts...)`. Specs (Ginkgo, the atc/exec house style):

```go
	It("admits a judge and carries it in HARVEST_CONFIG", func() {
		step := newStep(plan, exec.WithHarvestPlatformTokenSecret("agent-platform-credential"))
		_, err := step.Run(ctx, state)
		Expect(err).ToNot(HaveOccurred())
		_, _, spec, _ := fakePool.FindOrSelectWorkerArgsForCall(0)
		var cfg harvest.Config
		for _, e := range spec.Env {
			if strings.HasPrefix(e, "HARVEST_CONFIG=") {
				Expect(json.Unmarshal([]byte(strings.TrimPrefix(e, "HARVEST_CONFIG=")), &cfg)).To(Succeed())
			}
		}
		Expect(cfg.Judge).ToNot(BeNil())
		Expect(cfg.Judge.PassThreshold).To(Equal(6.5))
	})

	It("wires the PLATFORM token via SecretEnv only when a judge is declared", func() {
		step := newStep(plan, exec.WithHarvestPlatformTokenSecret("agent-platform-credential"))
		step.Run(ctx, state)
		_, _, spec, _ := fakePool.FindOrSelectWorkerArgsForCall(0)
		Expect(spec.SecretEnv).To(HaveKeyWithValue("CLAUDE_CODE_OAUTH_TOKEN", vars.SecretRef{
			Name: "agent-platform-credential", Key: "anthropic-token",
		}))
	})

	It("omits the token for judgeless plans", func() {
		judgeless := plan
		judgeless.Judge = nil
		step := newStep(judgeless, exec.WithHarvestPlatformTokenSecret("agent-platform-credential"))
		step.Run(ctx, state)
		_, _, spec, _ := fakePool.FindOrSelectWorkerArgsForCall(0)
		Expect(spec.SecretEnv).ToNot(HaveKey("CLAUDE_CODE_OAUTH_TOKEN"))
	})

	It("fails closed when a judge is declared but no platform secret is configured", func() {
		step := newStep(plan) // no WithHarvestPlatformTokenSecret
		_, err := step.Run(ctx, state)
		Expect(err).To(MatchError(ContainSubstring("--agent-platform-token-secret")))
	})

	It("fails closed on an invalid judge config", func() {
		bad := plan
		bad.Judge = &harvest.JudgeConfig{PassThreshold: 5} // empty rubric
		step := newStep(bad, exec.WithHarvestPlatformTokenSecret("agent-platform-credential"))
		_, err := step.Run(ctx, state)
		Expect(err).To(MatchError(ContainSubstring("judge config invalid")))
	})

	It("declares the flight output and AGENT_FLIGHT_DIR", func() {
		step := newStep(plan, exec.WithHarvestPlatformTokenSecret("agent-platform-credential"))
		step.Run(ctx, state)
		_, _, spec, _ := fakePool.FindOrSelectWorkerArgsForCall(0)
		Expect(spec.Outputs).To(HaveKey("flight"))
		Expect(spec.Outputs).To(HaveLen(1))
		Expect(spec.Env).To(ContainElement(HavePrefix("AGENT_FLIGHT_DIR=")))
	})

	It("still refuses dev_mcp and non-full gate scopes (unchanged boundaries)", func() {
		withDev := plan
		withDev.DevMCP = &atc.SidecarSource{}
		step := newStep(withDev, exec.WithHarvestPlatformTokenSecret("agent-platform-credential"))
		_, err := step.Run(ctx, state)
		Expect(err).To(MatchError(ContainSubstring("dev_mcp")))
	})
```

  While the file exists, ALSO pin the landed v0.5 behavior with basic specs (image-flag error, gates-full admission, SecretMounts on push, workspace missing-input error, exit-0 → needs_review + branch through fake tickets store) — the exec shipped untested; this file is the harness Task 12 reuses.
- [ ] Run `ginkgo --focus="Harvest" ./atc/exec/` — expect compile failure/red.
- [ ] Edit `atc/exec/harvest_step.go`:
  - Add field `platformTokenSecret string` + option:

```go
// WithHarvestPlatformTokenSecret names the long-lived §8.2 platform
// credential secret (key anthropic-token) funding the judge. The judge
// NEVER uses the per-run agent-run-<id> user token — precedence is the
// OPPOSITE of agent steps (§2.8.1/§8.3).
func WithHarvestPlatformTokenSecret(name string) HarvestStepOption {
	return func(h *HarvestStep) { h.platformTokenSecret = name }
}
```

  - REPLACE the judge refusal block (`harvest_step.go:129-131`) with:

```go
	if step.plan.Judge != nil {
		if err := step.plan.Judge.Validate(); err != nil {
			return false, fmt.Errorf("harvest judge config invalid: %w", err)
		}
		if step.platformTokenSecret == "" {
			return false, errors.New("harvest judge requires the web node to be started with --agent-platform-token-secret (the §8.2 platform credential; the judge never uses per-run user tokens)")
		}
	}
```

    The `DevMCP` refusal and the gates scope check stay EXACTLY as landed (those boundaries move with the dev-mcp swap plan, not here).
  - Add `Judge: step.plan.Judge` to the `harvest.Config` literal.
  - After the env assembly add `env = append(env, "AGENT_FLIGHT_DIR="+artifactPath(workdir, harvestFlightArtifact, ""))` and on the containerSpec: `containerSpec.Outputs = runtime.OutputPaths{harvestFlightArtifact: ensureTrailingSlash(artifactPath(workdir, harvestFlightArtifact, ""))}` with `const harvestFlightArtifact = "flight"`. Also add `env = append(env, "AGENT_PLAN_ID="+string(step.planID))` (the runner's `step.start` correlation key, §8.1 agent-step precedent).
  - After the SecretMounts block add:

```go
	if step.plan.Judge != nil {
		// §8.2/§8.3: PLATFORM credential only, secretKeyRef-only —
		// jetbridge applySecretRefs APPENDS the EnvVar (F20, landed).
		containerSpec.SecretEnv = map[string]vars.SecretRef{
			"CLAUDE_CODE_OAUTH_TOKEN": {Name: step.platformTokenSecret, Key: "anthropic-token"},
		}
	}
```

  - Change `container, _, err := chosenWorker.FindOrCreateContainer(...)` to capture `volumeMounts` (used by Task 12; harmless now).
- [ ] In `atc/engine/step_factory.go` `HarvestStep()` (:290 region), append (C3 — alongside the existing two):

```go
	if factory.agentPlatformToken != "" {
		harvestOpts = append(harvestOpts, exec.WithHarvestPlatformTokenSecret(factory.agentPlatformToken))
	}
```

  No atccmd change: `--agent-platform-token-secret` already flows into `CoreStepFactory` via `engine.WithAgentPlatformTokenSecret` (`command.go:2083`). [DECISION D2 — recommended: reuse the flag; optionally update its help text at `command.go:236` to mention the harvest judge.]
- [ ] Run `ginkgo --focus="Harvest" ./atc/exec/` then `ginkgo ./atc/exec/ ./atc/engine/` — green.
- [ ] Commit: `git add atc/exec atc/engine && git commit -m "feat(exec): harvest judge admission - platform token SecretEnv, flight output, HARVEST_CONFIG judge"`

---

### Task 11 (Slice E): renderer judge emission — NEW (relaxes the last boundary)

**Files:**
- Modify: `agent/dispatch/render.go`
- Test: `agent/dispatch/render_test.go` (append; plain-Go)

**Steps:**

- [ ] Append failing tests to `agent/dispatch/render_test.go`. The existing fixture builder is `renderInput()` (zero-arg, returns a `dispatch.RenderInput` value with a files-delivery ticketed input — `render_test.go:15`); mutate its `.Workflow` fields in place as the file's other tests do (`in := renderInput(); in.Workflow.X = ...; dispatch.Render(in)`). Confirm the `Defaults.Model` / `Budget.JudgeUSD` / `Judge` field spellings against `workflow.Config` before relying on them:

```go
func TestRenderEmitsJudgeOntoHarvest(t *testing.T) {
	in := renderInput()
	in.Workflow.Judge = &workflow.Judge{
		Rubric:        []workflow.RubricDimension{{Name: "correctness", Weight: 2, Guidance: "works"}},
		PassThreshold: 7,
	}
	in.Workflow.Defaults.Model = "claude-sonnet-4-5"
	in.Workflow.Budget.JudgeUSD = 1.5

	cfg, err := dispatch.Render(in)
	if err != nil {
		t.Fatalf("judge workflows must now render: %v", err)
	}
	steps := cfg.Jobs[0].PlanSequence
	last, ok := steps[len(steps)-1].Config.(*atc.HarvestStep)
	if !ok {
		t.Fatalf("terminal step is not harvest: %T", steps[len(steps)-1].Config)
	}
	if last.Judge == nil {
		t.Fatal("judge not emitted onto the harvest step")
	}
	if last.Judge.PassThreshold != 7 || len(last.Judge.Rubric) != 1 ||
		last.Judge.Rubric[0].Name != "correctness" || last.Judge.Rubric[0].Weight != 2 {
		t.Fatalf("judge mis-converted: %+v", last.Judge)
	}
	if last.Judge.Model != "claude-sonnet-4-5" || last.Judge.BudgetUSD != 1.5 {
		t.Fatalf("model/budget defaults not applied: %+v", last.Judge)
	}
}

func TestRenderJudgelessWorkflowEmitsNoJudge(t *testing.T) {
	in := renderInput()
	cfg, err := dispatch.Render(in)
	if err != nil {
		t.Fatal(err)
	}
	steps := cfg.Jobs[0].PlanSequence
	last := steps[len(steps)-1].Config.(*atc.HarvestStep)
	if last.Judge != nil {
		t.Fatal("no judge block, no judge emission")
	}
}
```

- [ ] Run `go test ./agent/dispatch/ -run TestRenderEmitsJudge -count=1` — expect failure (Render refuses judge).
- [ ] In `render.go`: DELETE the judge refusal (`render.go:160-162`), and in the terminal-harvest literal add `Judge: harvestJudge(in.Workflow.Judge, in.Workflow.Defaults.Model, in.Workflow.Budget.JudgeUSD),` plus:

```go
// harvestJudge converts the workflow judge block (validated at import)
// into the executable §6.4 shape. Model defaults to the workflow's
// default model; the budget cap comes from budget.judge_usd (§6) — both
// resolved at render time per the §2.8 render-time-resolution rule.
func harvestJudge(j *workflow.Judge, defaultModel string, budgetUSD float64) *harvest.JudgeConfig {
	if j == nil {
		return nil
	}
	rubric := make([]harvest.RubricDimension, len(j.Rubric))
	for i, d := range j.Rubric {
		rubric[i] = harvest.RubricDimension{Name: d.Name, Weight: d.Weight, Guidance: d.Guidance}
	}
	return &harvest.JudgeConfig{
		Rubric: rubric, PassThreshold: j.PassThreshold,
		Model: defaultModel, BudgetUSD: budgetUSD,
	}
}
```

  Update BOTH stale comments (judge now renders — harvest executes it — while `hitl`, non-full gate scopes, and source-format surfaces remain refused with the existing wording): (1) the boundary comment above the gate/hitl refusals near `render.go:153-160`; AND (2) the comment immediately preceding the terminal `atc.HarvestStep{...}` literal at `render.go:213-214`, which today reads "`judge/dev_mcp are never emitted — those workflows are refused above`" — after this task judge IS emitted (only `dev_mcp` is still refused), so reword it to say exactly that.
- [ ] Run `go test ./agent/dispatch/ -count=1`. The judge refusal is NOT a standalone test — it is the `{"judge", ...}` sub-case inside the table-driven `TestRenderRefusesDeclaredButUnenforcedPolicyBlocks` (`render_test.go:302-334`), whose `cases` table ALSO holds `"gate_policy_affected_scope"` and `"hitl"` sub-cases that MUST keep refusing. Precise surgery: remove ONLY the `{"judge", ...}` entry from that `cases` slice; leave the `gate_policy_affected_scope` and `hitl` entries and the shared loop untouched; update the test's now-stale doc comment (which currently says "affected-scope gates, hitl, and judge ... still refuse") to drop judge; and add the judge-emission behavior as the NEW separate top-level functions above (`TestRenderEmitsJudgeOntoHarvest` / `TestRenderJudgelessWorkflowEmitsNoJudge`). Do NOT delete or rewrite the whole table function.
- [ ] Commit: `git add agent/dispatch && git commit -m "feat(dispatch): render the judge onto the terminal harvest step (boundary lockstep complete)"`

---

### Task 12 (Slice F): exec server-side ingestion — MATERIALLY AMENDED (plan 09 Task 13 against the landed exec)

Two landed divergences the amendment respects: (1) there is NO `ingestAndRecord` stub — the ticket transition landed separately in `transitionTicket` with a RICHER guard than plan 09 specified (`RunBelongsToPipeline` + `TicketBelongsToRun`); KEEP it and its exit-code-driven call sites exactly as landed — ingestion is purely ADDITIVE recording, do not regress transitions to plan 09's results.json-driven version; (2) the timeout/process-failure paths already transition the ticket (`harvest_step.go:262-275`) — ingestion must run on those paths too, mirroring `AgentStep`'s placement (`agent_step.go:638` is `result, runErr := process.Wait(ctx)`; the `ingestFlightRecorder(...)` call is at `:648`, immediately after — before exit handling, with `context.WithoutCancel` + 30s bound inside; the F4 lesson).

**Trust boundary — the reviews half is NEW, not a copy (D9).** `AgentStep.ingestFlightRecorder` reads results.json/events.ndjson ONLY; it never touches `agent_reviews` (agent reviews reach the DB through the principal-authenticated / `--agent-review-publish-token` HTTP path at `agent/api/reviews/handler.go`'s `ParseSubmission`, verified: no `reviews.Store`/`agent/api/reviews` import anywhere in `atc/exec`). HarvestStep instead PULLS `review.json` off the pod-written `flight` volume and upserts `agent_reviews` server-side, bypassing `ParseSubmission`'s validation and the principal-auth boundary. That asymmetry is deliberate (the harvest evidence is judged, not self-reported) but it makes the flight volume attacker-writable input to a privileged DB write — so this task hard-pins every trust-bearing column from the SERVER side (`Repo` from `step.plan`, `TicketID`/`PipelineRunID` from the `verifiedIDs` helper, `SubmittedBy: "harvest"`) and treats the payload's own repo/ticket/score fields as advisory display data only. **D9 records this as an owner decision: confirm this bypass is acceptable and what security review the pod→`agent_reviews` write path needs before merge.**

**Files:**
- Modify: `atc/exec/harvest_step.go`
- Modify: `atc/engine/step_factory.go`, the engine-level option pass-through (mirror `WithAgentTicketsStore`'s definition chain — locate by grep), and `atc/atccmd/command.go` (one line)
- Test: `atc/exec/harvest_step_test.go` (extend the Task 10 harness)

**Steps:**

- [ ] Add options + fields to `HarvestStep` (imports: `agent/api/metrics`, `agent/api/reviews`, `agent/budget`; `Streamer` is the existing atc/exec seam the agent step uses — match its spelling):

```go
func WithHarvestStreamer(s Streamer) HarvestStepOption {
	return func(h *HarvestStep) { h.streamer = s }
}
func WithHarvestMetricsStore(m metrics.Store) HarvestStepOption {
	return func(h *HarvestStep) { h.metricsStore = m }
}
func WithHarvestReviewsStore(r reviews.Store) HarvestStepOption {
	return func(h *HarvestStep) { h.reviewsStore = r }
}
func WithHarvestBudgetRecorder(c budget.Checker) HarvestStepOption {
	return func(h *HarvestStep) { h.budgetChecker = c }
}

// WithHarvestPlatformUserResolver supplies the §1.13 platform-user
// lookup (UserBySub("agent-platform")) so the harvest_judge ledger row
// carries platform attribution. Only wired if D8 resolves exec-side; a
// nil resolver logs-and-skips attribution, never fails the step.
func WithHarvestPlatformUserResolver(r PlatformUserResolver) HarvestStepOption {
	return func(h *HarvestStep) { h.platformUserResolver = r }
}

// PlatformUserResolver is the UserBySub subset the ledger attribution
// needs (matches agent/credentials.Backend / agent_user_credentials).
type PlatformUserResolver interface {
	UserBySub(sub string) (userID int, userName string, found bool, err error)
}
```

- [ ] Extract the verification from `transitionTicket` into a helper both it and ingestion use, so linkage NEVER comes from raw plan env (the agent-step 2026-07-11 review lesson — a raw claim reaching `LedgerEntry.TicketID` lets any pipeline drain a victim ticket's budget):

```go
// verifiedIDs returns (ticketID, runID) only when the server-side
// verifier confirms the plan-carried claims; (0, 0) otherwise.
// transitionTicket and ingestAndRecord both consume it.
func (step *HarvestStep) verifiedIDs(logger lager.Logger) (int, int) { /* the landed
	transitionTicket verification body, returning ids instead of acting */ }
```

  `transitionTicket` keeps its exact landed behavior, now calling the helper.
- [ ] Add ingestion specs to the Task 10 harness. `fakeStreamer.StreamFileStub` returns fixture readers keyed on the requested path — fixtures in the TASK 9 shapes (NOT plan 09's):
  - `results.json` → `{"schema_version":"1.0","status":"pass","confidence":1,"summary":"1 gate(s) ok; judge 7.5/10 (pass); pushed agent/ticket-42","artifacts":[],"metadata":{"pushed_branch":"agent/ticket-42","head_sha":"abc123"}}`
  - `events.ndjson` → NDJSON lines for `step.start`, `gate.result` (`{"gate":"test","scope":"full","status":"ok","duration_seconds":3}`), `judge.score`, `cost.record` (`{"source":"harvest_judge","provider":"anthropic","model":"m1","input_tokens":10,"output_tokens":5,"turns":1,"cost_usd":0.2}`), `push.done`, `step.end` (`{"step_name":"harvest","status":"ok","summary":"done","wall_time_seconds":42,"cost_usd":0.2,"turns":1}`)
  - `review.json` → a Task 9 `Evidence` document (`schema_version: "harvest/1"`, metadata commit `abc123` branch `agent/ticket-42` agent_model `m1`, score 7.5/10 pass, one `judge-correctness-1` observation, gates `[]`, judge block)

  Specs (adapted from plan 09 Task 13's list — transition assertions DROP, transitions are already covered by the landed exit-code-driven specs from Task 10):
  1. upserts a `RunMetrics` row before Run returns **via `metrics.Store.UpsertReturningInserted`** (NOT plain `Upsert` — the F3 ledger-dedup discriminator, `metrics/types.go:21-40`): status `ok`, BuildID/PlanID/StepName from metadata, `*TicketID == 42` and `*PipelineRunID == 7` (verifier fake returns true/true), `CostUSD ~ 0.2` (only `harvest_judge`-source cost.records are rolled up), `EventCounts["gate.result"] == 1` and `["judge.score"] == 1`, `WallTimeSeconds == 42` (step.end wins);
  2. upserts the evidence row into a `reviews.MemoryStore`: BuildID from metadata, `Repo` from the PLAN (never the pod-written payload — the pod is attacker-writable), CommitSha `abc123`, Branch `agent/ticket-42`, Score 7.5 / Pass true / counts from the payload, `Review` = raw bytes, `*TicketID == 42`, `*PipelineRunID == 7`, `SubmittedBy == "harvest"`;
  3. records the `harvest_judge` ledger entry fire-and-forget on a **fresh insert**: `fakeChecker.RecordCallCount() == 1`, `entry.Source == "harvest_judge"`, `entry.CostUSD ~ 0.2` (`ledgerCost`, computed the agent-step way — see the implementation bullet), `*entry.TicketID == 42`, and **`entry.UserID`/`entry.UserName` carry the §1.13 platform service user** (`*entry.UserID == platformUserID`, `entry.UserName == "platform"`; the fake platform-user resolver returns a fixed id) — a `harvest_judge` row that omits platform attribution violates the FROZEN §1.13 clause; and NO ledger record when the events carry no harvest_judge cost;
  3a. **F3 regression (two-pass idempotency):** run ingestion twice over the SAME `(build_id, plan_id)` (simulating a web-restart resume through `attachOrRun` + a second `process.Wait`) with identical flight fixtures — assert `fakeChecker.RecordCallCount() == 1` total (the second pass is an `UpsertReturningInserted` UPDATE with `prev.CostUSD == rm.CostUSD`, so `ledgerCost == 0` and the append is skipped). Then a resume whose FIRST pass inserted a zero-cost row (cost.record absent) and whose SECOND pass carries the real `harvest_judge` cost must Record exactly the DELTA once;
  4. degrades to `status=error` / "flight recorder output missing" metrics row when every stream errors (evidence upsert skipped, no ledger entry, exit-code-driven transition untouched);
  5. runs ingestion on the DeadlineExceeded path (`fakeMetricsStore.UpsertCallCount() == 1` even when `process.Wait` returns `context.DeadlineExceeded`);
  6. never fails the step when any recording write errors (`UpsertReturns(errors.New(...))` etc. — Run still returns the exit-status result);
  7. strips linkage (nil TicketID/PipelineRunID on metrics AND evidence) when the verifier rejects the claim, while STILL writing both rows.
- [ ] Implement `ingestAndRecord(ctx, logger, wkr, volumeMounts, wallTime)` following `AgentStep.ingestFlightRecorder` (`agent_step.go:671-943`) structurally FOR THE METRICS + LEDGER HALVES (the review.json half has no AgentStep precedent — see D9): locate the `flight` mount → `wkr.ArtifactFromVolume` (+ record the volume handle as `EventsArtifact`); `context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)`; stream `results.json` (5 MiB limit, `schema.Results` + `Validate` + `ThreeWayStatus`), `events.ndjson` (counts + judge-only cost rollup filtered on `CostRecordData.Source == "harvest_judge"` + `step.end` wall time), `review.json` (5 MiB limit → `reviews.ReviewPayload` parse for denormalized columns; `StoredReview{..., Review: raw, TicketID/PipelineRunID: verified-or-nil, SubmittedBy: "harvest"}` upsert with `Repo: step.plan.Repo` — NEVER the pod-written payload's repo).
- [ ] **Metrics + ledger dedup discipline (F3 — mirror `agent_step.go:902-943` EXACTLY, do NOT gate on raw `judgeCost > 0`):** call `inserted, prev, err := step.metricsStore.UpsertReturningInserted(&rm)`; compute `ledgerCost` = `rm.CostUSD` on a fresh insert, `rm.CostUSD - prev.CostUSD` on an update, and 0 (skip) when `inserted == false && prev == nil` (indeterminate — the F3 lesson: a pure first-insert gate double-charges a web-restart resume, because a severed exec inserts a zero-cost row and the resume's update carries the real spend as its delta). Build the `budget.LedgerEntry` (`Source: budget.SourceHarvestJudge`, `Provider: "anthropic"`, verified `TicketID`/`PipelineRunID`, `CostUSD: ledgerCost`, token counts as deltas off `prev`) and Record it only when `step.budgetChecker != nil && ledgerCost > 0`.
- [ ] **§1.13 platform-user attribution (FROZEN clause — the harvest_judge row MUST carry the platform service user):** set `entry.UserID` / `entry.UserName` to the §1.13 platform user (`users` row `sub='agent-platform'`, `user_name='platform'`) on the harvest_judge ledger append. Resolve it the way `agent/dispatch/dispatch.go:241` does — `resolver.UserBySub(credentials.PlatformUserSub)` (`credentials.PlatformUserSub == "agent-platform"`) — threaded into `HarvestStep` via a new nil-guarded `WithHarvestPlatformUserResolver` option (mirroring `WithHarvestBudgetRecorder`'s wiring; skip attribution + log when unset, never fail the step). See D8 for which side resolves. Every write logged-never-returned. Call `ingestAndRecord` in `run()` immediately after `result, runErr := process.Wait(ctx)`, BEFORE the runErr/timeout handling and before the exit-taxonomy switch.
- [ ] Wire the engine: in `step_factory.go` `HarvestStep()` append `exec.WithHarvestStreamer(factory.streamer)` unconditionally, plus nil-guarded `WithHarvestMetricsStore(factory.agentMetricsStore)` / `WithHarvestBudgetRecorder(factory.agentBudgetChecker)` / `WithHarvestReviewsStore(factory.agentReviewsStore)` — the last needs a NEW `agentReviewsStore reviews.Store` field + `WithAgentReviewsStore` CoreStepFactoryOption + the engine-level pass-through mirroring `WithAgentTicketsStore`'s chain, then in `atc/atccmd/command.go` (the `:2082-2087` options block) append `engine.WithAgentReviewsStore(db.NewAgentReviewsFactory(dbConn)),` (C3 — alongside the existing options). If D8 lands exec-side, also thread `WithHarvestPlatformUserResolver` down the same chain from a `UserBySub` backend (the `agent_user_credentials` factory already exposes `UserBySub`, `atc/db/agent_user_credentials_factory.go:24`).
- [ ] Run `ginkgo ./atc/exec/ ./atc/engine/` then `go build ./...` — green.
- [ ] Commit: `git add atc/exec atc/engine atc/atccmd && git commit -m "feat(exec): harvest ingestion - metrics, evidence with linkage + submitted_by, judge ledger (contracts 2.8.1/1.10)"`

---

### Task 13 (Slice G): Elm build-page rendering + public-plan fix — MATERIALLY AMENDED (plan 09 Task 16's premise is false; scope doubled)

Ground-state correction #5: no `BuildStepAgent` exists anywhere in `web/elm` (verified AFTER the agentic-ui waves landed), and `plan.Harvest` is silently dropped by `Public()`. Any build containing an agent step yields plan JSON the strict `decodeBuildPlan` oneOf rejects, and `Build.elm` swallows the decode error — no step tree renders. **Before writing code, verify live behavior** (open an agentic build page on concourse.home; expect a missing/empty step tree — if the page shows steps via some event-driven path this task's Elm scope shrinks; record what you observed in the commit message). [DECISION D6.]

**Files:**
- Modify: `atc/public_plan.go` (Harvest field + dispatch)
- Modify: `web/elm/src/Concourse.elm` (`BuildStep` union at :495, `decodeBuildPlan` oneOf at :668 region, two new decoders)
- Modify: `web/elm/src/Build/StepTree/StepTree.elm` (every case site the compiler flags — treat both new steps like `BuildStepTask`: leaf step with name; labels `"agent:"` / `"harvest:"`)
- Test: `ginkgo ./atc/` (public-plan spec below); Elm compile exhaustiveness + `cd web && yarn && yarn build` (bundle-regen precedent: commit 46db7b9735)

**Steps:**

- [ ] Go side first (gate-verifiable). Failing spec in the existing public-plan spec file (locate `Public` specs under `atc/`): a plan with `Harvest: &atc.HarvestPlan{Name: "harvest"}` must surface a `"harvest"` key in `Public()` output (today it vanishes). Then in `public_plan.go` ADD (C3) `Harvest *json.RawMessage \`json:"harvest,omitempty"\`` to the anonymous struct after `Agent`, and the dispatch arm after the Agent arm:

```go
	if plan.Harvest != nil {
		public.Harvest = plan.Harvest.Public()
	}
```

  (`HarvestPlan.Public()` at :328 already exists — this wires the dead code in.) Run `ginkgo ./atc/` — green. Commit the Go half separately: `git commit -m "fix(atc): public plan no longer drops harvest steps"`.
- [ ] Elm: add BOTH union members next to each other:

```elm
    | BuildStepAgent StepName
    | BuildStepHarvest StepName
```

  decoders (after the `run` field entry in the oneOf):

```elm
        , Json.Decode.field "agent" <| lazy (\_ -> decodeBuildStepAgent)
        , Json.Decode.field "harvest" <| lazy (\_ -> decodeBuildStepHarvest)
```

```elm
decodeBuildStepAgent : Json.Decode.Decoder BuildStep
decodeBuildStepAgent =
    Json.Decode.succeed BuildStepAgent
        |> andMap (Json.Decode.field "name" Json.Decode.string)


decodeBuildStepHarvest : Json.Decode.Decoder BuildStep
decodeBuildStepHarvest =
    Json.Decode.succeed BuildStepHarvest
        |> andMap (Json.Decode.field "name" Json.Decode.string)
```

  (Confirm the public JSON field for the step name against `atc/plan.go`'s json tags for `AgentPlan`/`HarvestPlan` — match the decoder to the actual key.) Elm exhaustiveness is the failing test: compile, then satisfy every flagged `case` in `StepTree.elm` (and any other flagged module) the way `BuildStepTask name` is handled — leaf init with the step name, view labels `"agent:"` / `"harvest:"`. The agentic-ui wave's spine-serialization rule applies to any data-layer additions — these are pure decoder/view additions, no ports/spine changes.
- [ ] `cd web && yarn && yarn build` (+ `npx elm-test` if configured) — clean. Rebuild the embedded bundle the way commit 46db7b9735 did.
- [ ] Local look: open a build containing an agent step and a harvest step; both render as steps with logs.
- [ ] Commit: `git add web atc && git commit -m "feat(web-ui): render agent and harvest steps in the build plan view"`

---

### Task 14 (Slice H): fixture e2e remainder — judged-run postures over the full flight contract

Plan 09 Task 17's gates pass/fail/flaky triad ALREADY EXISTS at unit level (`gates_test.go`, `runner_test.go`) and Task 9 added flight/judge coverage. The remainder is the locked-together POSTURE suite plan 09 wanted: one test per end-state over a real fixture workspace asserting the FULL flight contract each time (results.json valid + review.json + events.ndjson + push state), now including judge postures. Dev-mcp-backed variants are OUT (follow-on plan).

**Files:**
- Create: `agent/harvest/fixture_e2e_test.go`
- Test: `go test ./agent/harvest/ -run TestPosture -count=1`

**Steps:**

- [ ] Write `agent/harvest/fixture_e2e_test.go` — plain-Go translation of plan 09 Task 17's posture matrix (`09-harvest-step.md:3889-4026`) minus dev-mcp, with these bindings: exits are literal `0`/`1`/`2` (no `ExitGatesPassed` constants exist); gates use the in-pod fixed map (fixture Go module via `seedGoModule` — the 3-arg `runner_test.go` helper that commits into the workspace, NOT `writeFixtureModule`); judge via the `HARVEST_JUDGE_CLI` stub; shared `flightContract(t, flight, wantStatus)` helper built from Task 9's `readResults`/`eventTypes`. Postures:
  1. `TestPostureGreenJudgePass` — passing gate + judge pass → exit 0, branch delivered, evidence score pass, `judge.score` + `push.done` events present;
  2. `TestPostureGateFail` — red test gate → exit 1, nothing pushed (`for-each-ref refs/heads/agent` empty), `gate-test` proven issue with the `--- FAIL` detail in evidence;
  3. `TestPostureDirty` — F33 → exit 1, no gate events (gates never ran), no push, `workspace-dirty` evidence, wip file survives;
  4. `TestPostureFlaky` — `Retries: 1` + the landed marker-file flaky fixture (`gates_test.go:185-231` — reuse the trick) → exit 0, `"flaky":true` in evidence gates AND a `gate.result` event decoding to `schema.GateResultData{Attempt: 2, Flaky: true}`;
  5. `TestPostureJudgeFailStillPushes` — judge verdict below threshold → exit 0 (gates are the only hard gate, §6.4), pushed, evidence `Score.Pass == false`;
  6. `TestPostureJudgeErrorAdvisory` — posture-level restatement of Task 9's invariant: exit 0 + `judge_error` in results metadata + pushed.
- [ ] Run `go test ./agent/harvest/ -count=1` — full package green (any failure is a real semantics bug in Tasks 5–9; fix the implementation, not the fixtures).
- [ ] Commit: `git add agent/harvest && git commit -m "test(harvest): posture suite - green/gate-fail/dirty/flaky/judge-fail/judge-error with full flight contract"`

---

### Task 15 (Slice H): live theborg credential isolation — execute plan 09 Task 18 as written, with the deltas below

Execute `09-harvest-step.md` Task 18 (`atc/worker/jetbridge/live_harvest_credentials_test.go`, `//go:build live`, scaffolding from `live_secret_env_test.go`). Deltas:

- [ ] **Scope narrows to the ISOLATION property** — push functionality is proven in-cluster (tickets #12/#13/#14 pushed via the real secret). Assert: (a) the git-cred secret volume is mounted on the harvest pod's MAIN container only (spec-level, and a live `cat /var/run/agent/git/token` succeeds in main); (b) an AGENT pod built by the same worker carries neither the git-cred mount nor the platform-credential SecretEnv ref; (c) with a judge configured, the harvest main container's `CLAUDE_CODE_OAUTH_TOKEN` EnvVar is a `secretKeyRef` to `agent-platform-credential`/`anthropic-token` (never a literal, never `agent-run-<id>`).
- [ ] Pattern per CLAUDE.md: throwaway namespace (`kubectl --context theborg create ns harvest-live-<date>`), NEVER `cicd`/`concourse`; `t.Cleanup` pods then namespace; `KUBECONFIG=~/.kube/config K8S_TEST_NAMESPACE=harvest-live-<date> go test -tags live -run '^TestLiveHarvestCredentialIsolation$' -v -count=1 -timeout 5m ./atc/worker/jetbridge/`.
- [ ] The per-repo secret `agent-harvest-git-<slug>` is admin-created manually (§8.3; no API writes it) — for this test create a throwaway secret in the throwaway namespace; no real PAT needed (isolation, not push).

---

### Task 16 (Slice H): full verification + close-out — execute plan 09 Task 19 as written, with the deltas below

- [ ] Amended suite list (replaces plan 09's Execution-notes block):

```bash
go test ./agent/harvest/ ./agent/dispatch/ -count=1        # plain-Go packages
(cd agent/schema && go test ./...)                          # nested module (NOT covered by root go test)
ginkgo ./agent/api/reviews/
ginkgo ./atc/ ./atc/exec/ ./atc/engine/ ./atc/worker/jetbridge/
ginkgo --focus='Legacy Database Upgrade' ./atc/db/migration/
ginkgo ./atc/db/                                            # ~90s, needs postgres
make test-quick                                             # full tier before handing off
```

- [ ] End-to-end live proof (the Slice E/F payoff): on theborg, import a workflow with a small judge rubric onto a SCRATCH ticket (scratch repo, never a live one; the `agent-platform-credential` secret exists in-cluster per the agent-review-native precedent), dispatch, and verify: build page shows the harvest step (Slice G), branch pushed, ticket → needs_review, `agent_reviews` row has `ticket_id`/`pipeline_run_id`/`submitted_by='harvest'`, ticket page's review embed shows the judge evidence, `agent_cost_ledger` has EXACTLY one `harvest_judge` row (F3 — re-run/resume must not add a second) and that row carries the §1.13 platform `user_id` (the `sub='agent-platform'` service user, `user_name='platform'`). Respect the dispatch-timing rule (push→settle→dispatch; self-upgrade restarts web and double-spends agents).
- [ ] Update `09-harvest-step.md`'s amendment log: one entry stating this remainder plan executed Tasks 1–4 / 7-subset / 9 / 10-subset / 12-subset / 13 / 16–19 as amended here, and that Task 8 + the dev-mcp legs moved to the follow-on plan.
- [ ] Confirm the dual constants read `1773106080` and the theborg DB migrates cleanly on the next image bump (ArgoCD gotchas per the harvest-v0 memory: image-bump + target-branch).

---

## Migration allocations

- **This plan lands exactly ONE migration: `1773106080_add_ticket_linkage_to_agent_reviews.{up,down}.sql`** — the reserved §1.1 harvest-step block number (1773106080–89), SQL pinned in §1.10.
- **Current deployed head = 1773106066** (`add_agent_workflow_source_manifest`, commit ac9347c9aa; both C2 constants verified at planning time — this CORRECTS the 2026-07-17 shared ground state's 1773106064 AND the earlier draft of this file). 1773106080 is ABOVE head: it lands at its reserved number, no renumbering (unlike ticket-core).
- **C2:** the same commit bumps `atc/db/migration/legacy_upgrade_test.go:37` `jetbridgeHeadMigration` AND `docs/migration/migrate-preflight.sh:38` `JETBRIDGE_VERSION` from 1773106066 → **1773106080**.
- **Ordering trap (flag, accepted):** platform-mcp-hitl's reserved 1773106070–79 block (e.g. `agent_run_questions` 1773106072) has NOT landed. Landing 1773106080 advances the version pointer past that whole block, forcing platform-mcp-hitl into a renumber-above-head at ITS land time — exactly the ticket-core precedent the §11 2026-07-12/-17 entries sanction. The alternative (renumber this migration down to next-free **1773106067** — NOT 1773106066, which is now taken — keeping the pointer low for wave-mates, at the cost of vacating the 1773106080–89 block under the never-reuse rule) is **DECISION D1** for the owner. Default if unanswered: land at 1773106080 as reserved (precedent-sanctioned; §1.10's SQL filename is already normative text).
- Noted in passing (no action here): PARK-V2's 1773106065 reservation is now BELOW head 1773106066 and will renumber at its own land time; delivery-outcomes' 1773106090 stays safely above everything this plan does.
- Migrations merge lowest-version-first; if another branch lands a migration below 1773106080 while this plan is in flight, merge THAT first (the version-pointer migrator silently skips lower numbers merged late).

---

## Risks & open decisions

**Owner decisions needed (defaults stated; the plan proceeds on defaults if unanswered):**
- **D1 — migration number:** 1773106080 as reserved (default) vs renumber-down to 1773106067. See Migration allocations.
- **D2 — judge token flag:** reuse `--agent-platform-token-secret` (default; zero new wiring — `step_factory.agentPlatformToken` already exists) vs a harvest-specific flag. §8.2 pins the SECRET name (`agent-platform-credential`); the flag is just the operator seam. If reused, optionally update the help text at `command.go:236`.
- **D3 — results.json shape:** converge on `agent/schema.Results` for BOTH flight file and stdout (default; Task 1 pins it, Task 9 implements it). The stdout shape change is build-log-only — verified no machine consumer parses stdout (the exec keys off exit status). The alternative (keep the local v0.5 stdout shape, write the schema form only to flight) doubles the maintained shapes for zero consumer benefit.
- **D4 — fixed-command-map fate:** untouched by this plan (the map stays the only executor). The keep-as-fallback vs require-dev_mcp question moves to the follow-on dev-mcp swap plan, together with the renderer-emission contract change it forces.
- **D5 — judge model/budget at render:** `Model` = workflow `defaults.model`, `BudgetUSD` = `budget.judge_usd` (Task 11 implements this; §6's judge block has no per-judge model field). If the owner wants a judge-specific model slot it is a workflow-store schema_version bump, not this plan.
- **D6 — Elm live behavior:** confirm on concourse.home what an agentic build page shows TODAY before executing Task 13 (code says: no step tree; the F1–F11 waves may have partially compensated). If something already renders, re-scope Task 13 before writing Elm.
- **D7 — loop test-gate vs atc/db:** tickets #13/#14 passed full-scope gates, implying `go test ./...` at repo root somehow tolerates missing postgres in the agent pod. UNVERIFIED for the `atc/db` suites Slice B touches. Before dispatching ANY slice to the loop with a `test` gate, confirm whether `atc/db` specs skip cleanly without postgres; if they don't, Slice B is native-only (as recommended below) and other slices are unaffected (they don't add postgres-only specs to gate-covered packages).
- **D8 — platform-user attribution side (§1.13, FROZEN):** the `harvest_judge` ledger row MUST carry the platform service user (`sub='agent-platform'`, `user_name='platform'`). REVIEW.md left "which side backfills" open. Default (recommended): resolve exec-side in `ingestAndRecord` via a threaded `WithHarvestPlatformUserResolver` (`UserBySub(credentials.PlatformUserSub)`, mirroring `dispatch.go:241`) — self-contained, no schema change, testable with a fake resolver. Alternative: a DB-side default/trigger that stamps `user_id` on `source='harvest_judge'` rows (keeps exec dependency-free but hides attribution in a migration). Either way Task 12's spec 3 asserts `entry.UserID == platformUserID` / `entry.UserName == "platform"`.
- **D9 — reviews-ingestion trust bypass:** HarvestStep pulls `review.json` off the pod-written flight volume and upserts `agent_reviews` server-side, bypassing the principal-authenticated `ParseSubmission` HTTP path AgentStep reviews use (no exec precedent). Default: proceed, with every trust-bearing column pinned server-side (`Repo` from plan, verified `TicketID`/`PipelineRunID`, `SubmittedBy: "harvest"`) and the payload's own repo/ticket/score treated as advisory display data. Owner must confirm this bypass is acceptable and name the security review the pod→`agent_reviews` write path needs before merge (see Task 12's trust-boundary note).

**Risks:**
- **Stdout shape change (Task 9)** alters what humans see in harvest build logs in-cluster. Mitigation: the new document is a superset (schema_version/summary/metadata) and ships in the same release as the exec that reads the flight file.
- **`Run`/`RunGates` signature changes** break only in-repo callers (grep-verified: `cmd/harvest-runner` + tests). The `GateOutcome`/`Results.Metadata.Gates` JSON contracts are preserved byte-compatible for first-attempt results.
- **Evidence category values** (`gate`/`judge`) are outside `ReviewPayload`'s parsed subset (raw-message arrays) — Elm renders whatever the raw payload carries; verify the ticket-page embed doesn't choke on unknown categories during Task 16's live proof.
- **Boundary lockstep:** the judge relaxation is split runner-first (Slice D) then exec+render together (Slice E). Never ship Slice E's render half without its exec half — a rendered judge hitting a refusing exec errors every ticketed run of judge workflows. Tasks 10 and 11 are one slice, one merge.
- **Ledger idempotency (F3):** `agent_reviews` upsert is idempotent on `(build_id, repo, commit_sha)` and metrics on `(build_id, plan_id)`; the append-only `agent_cost_ledger` has NO dedup key of its own. A first-insert gate is NOT enough (harvest steps resume via `attachOrRun` + a second `process.Wait` on web restart, exactly like agent steps). Mitigation is the landed agent-step discipline (`agent_step.go:902-943`): drive the ledger off `metrics.Store.UpsertReturningInserted` and charge the full cost on a fresh insert, only the DELTA (`rm.CostUSD - prev.CostUSD`) on a resume/update, and skip when `inserted==false && prev==nil` — reusing the metrics `(build_id, plan_id)` key as the single dedup authority. The judge-only `source == harvest_judge` cost rollup (the non-monotonic-upsert double-count lesson from the wave-2 review) filters WHICH cost.records roll into `rm.CostUSD`; the insert/delta gate is what stops the double-append. Task 12 spec 3a is the two-pass regression proof.
- **Live judge spend:** funded by the platform credential; `BudgetUSD` is post-hoc. Config-only rollback: remove the `judge:` block from the workflow (Task 16's live proof uses a tiny rubric on a scratch ticket).
- **Parallel-plan collision:** sibling remainder plans (platform-mcp-hitl, delivery-outcomes, dispatcher, workflow-source-format) touch neighboring surfaces. Hard collisions:
  - (1) migration ordering (covered above);
  - (2) `00-shared-contracts.md` §11 (append-only — re-read the current log tail and append after whatever entry is now last, NOT after a pinned line; all five plans append here, coordinate merges);
  - (3) **`agent/dispatch/render.go` + `render_test.go`, shared with TWO other plans.** This plan's Task 11 deletes the judge refusal (`render.go:158-162`) and removes the `"judge"` sub-case from `TestRenderRefusesDeclaredButUnenforcedPolicyBlocks`. **platform-mcp-hitl's Task 25** deletes the ADJACENT hitl refusal (`render.go:157-159`), removes the `"hitl"` sub-case, and adds `Sidecars` to the `RenderAgentStep` return literal. **workflow-source-format's Task 3b/5** narrows then removes the source-format refusal at the IMMEDIATELY-FOLLOWING `render.go:163-170` (right below this plan's judge refusal) and adds `SystemPrompt`/`Context`/`Skills` to the same return literal. Each plan is written against the others' stale line numbers/state (platform-mcp-hitl even asserts "judge ... stays byte-identical" — false once this plan lands). These three Tasks MUST land sequentially, never in parallel; suggested order: workflow-source slice-b → this plan's Task 11 → platform-mcp-hitl Task 25. Whoever lands second/third re-greps the whole `render.go:125-170` refusal chain and the `render_test.go` table at current HEAD, and preserves the others' already-applied removals/additions (removed sub-cases, added `Sidecars`/`SystemPrompt`/`Context`/`Skills` fields, narrowed source-format refusal) rather than patching cited lines. The terminal-harvest comment block at `render.go:213-214` is the same shared surface.
  - (4) **harvest results shape + exec surface, shared with delivery-outcomes.** This plan's Task 9 does the FULL `Run` rewrite that DELETES the typed `harvest.Results`/`ResultsMetadata` and converges runner stdout + flight `results.json` on `agent/schema.Results` (metadata a `map[string]interface{}` carrying `head_sha`/`base_sha`, Decision D3). delivery-outcomes Task B4 also touches `agent/harvest/runner.go` (base_sha), CREATES `atc/exec/harvest_step_test.go` (this plan's Task 10 creates it too), modifies `atc/exec/harvest_step.go` (this plan: SecretEnv/flight/ingestion + `verifiedIDs` helper; delivery-outcomes: stdout tee/`seedOutcome` + a verifier helper), and appends to `atc/engine/step_factory.go` `harvestOpts`. **This plan's Task 9 OWNS the results shape and lands FIRST**; delivery-outcomes reads `res.Metadata["head_sha"]`/`["base_sha"]` off `schema.Results` (map keys — NOT a typed `ResultsMetadata.BaseSHA`, which no longer exists), MERGES into the existing `harvest_step.go`/`harvest_step_test.go`, and reuses the single `verifiedIDs` helper instead of adding a second. See delivery-outcomes Task B4's cross-plan header.

---

## Complexity, risk, and recommended execution level

**Honest sizing:** 16 tasks across 8 slices. The heavy code is concentrated in Task 9 (runner + gates events + evidence, ~700 lines with tests — at the top of, but comparable to, ticket #14's proven 8-file/+682 envelope), Task 12 (ingestion + engine threading), and Task 13 (Elm + Go, toolchain-bound). Slices A–C are small-to-medium and fully specified with complete code. Everything except Slices B, G, H is verifiable by the loop's in-pod gates; B needs postgres, G needs Elm, H needs theborg.

**Recommendation: split** — per-slice mapping:

| Slice | Level | Rationale |
|---|---|---|
| A (contracts) | **native-opus** | Doc-only, normative; judgment reconciling body text with landed code, no tests to gate on; cheap on-machine. |
| B (migration + db) | **native-opus** | Postgres-backed specs cannot be gate-verified (D7 unresolved); the migration carries the C2/lowest-first hazards — keep the human in the merge path. Small, referenced-task work. |
| C (judge engine) | **loop-opus** | Pure Go, full code + tests in-plan, gate-verifiable, one ticket comfortably inside the #14 envelope. Sonnet would likely survive the mechanical parts, but the CLI-envelope parsing subtleties justify opus. |
| D (flight recorder) | **loop-fable** | Largest single ticket; full code is given but it reshapes a live stdout contract, touches the nested module (whose tests the gates do NOT run — the ticket spec must order `cd agent/schema && go test ./...` and a human re-runs it pre-merge), and coordinates gates.go/runner.go/main.go/test updates. Fable margin is worth it; loop-opus acceptable if budget-tight. |
| E (exec + render admission) | **native-fable** | Security-boundary work (platform-credential wiring, refusal relaxation) plus creating the exec test harness from an unread fixture file — judgment-heavy against live wiring; the lockstep rule makes a botched half-merge expensive. Keep it native. |
| F (ingestion) | **loop-opus** | Fake-driven Ginkgo with a landed pattern to copy (`agent_step.go` ingestion) and the harness already created in E; no postgres. Mandatory human read of the verifiedIDs/linkage leg at review (budget-security surface). |
| G (Elm + public plan) | **native-fable** | No gate coverage for Elm; requires the live-behavior check (D6) first, bundle regen, and spine-serialization discipline. Heuristic: Elm ⇒ native. |
| H (e2e + live + close-out) | **native-fable** | theborg cluster, throwaway namespaces, live judged-harvest proof, fly/kubectl — native by definition. Task 14 alone (fixture postures, pure Go) could be split off to loop-opus if desired. |

**Budget note:** the three loop slices (C, D, F) are sequential dependencies of E/H — dispatch them one at a time (push→settle→dispatch; parallel fan-out double-spends the shared rate window), interleaving native A/B up front (C depends on nothing from B; E depends on B+C+D; F depends on B+E).
