# The Bench — A1: Fixture Registry, Capture Hook, and Store

> **SUPERSEDED — historical implementation record (not executable).** The approved [Agentic Workflows as Functions over Snapshots design](../../../specs/2026-07-21-agentic-workflows-as-functions-design.md) and [Agentic Functions program plan](../../2026-07-21-agentic-functions-program.md) are authoritative. This document preserves the abandoned ticket-centric roadmap only. **Explicit superseded block:** every section below this banner, including migration reservations at `1773106100+`, `step_kind`, ticket/build/plan keys, restore runner/stub, and `primaryMetric` references, is historical and must not be implemented. **Keep:** fixtures, repetitions, evaluators, controls, and scorecards. **Supersede:** `step_kind`, ticket/build/plan keys, restore runner/stub, and the primary-metric switch.

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

- **Descends-from:** `docs/superpowers/specs/2026-07-19-agent-bench-design.md` (§1 Fixture capture, §2 Fixture registry, "Data model sketch", "Handoff briefs → A. bench-core" — the capture/registry/store slice); the FROZEN cross-track contract skeleton (A1 = `agent_step_fixtures`, migration **1773106100**). Supersedes plan 14's `agent_benchmark_cases` (M1) — benchmark cases import as `source: 'benchmark'` fixtures, so that table is **not built** (see Scope-out).
- **Tasks:** 15
- **Complexity:** M–L (one migration + one leaf domain package + a fire-and-forget hook in two hot ingestion paths + a host-path blob store + a retention reaper component). Mostly additive; the only edits to existing hot code are two guarded, non-fatal hook calls.
- **Risk:** Low–Medium. Capture is fire-and-forget and non-fatal (spec principle 2) — a bug degrades to "no fixture row", never a failed production step. The master-switch flag (`--agent-fixture-store-dir` empty) makes the whole slice a no-op on first deploy. The migration is a single additive table.
- **Migrations:** **1773106100** `agent_step_fixtures` (lands first and alone per spec Sequencing; no dependencies). On-disk head is `1773106091` (`create_agent_settings`); the `1773106100–102` block is the spec-reserved bench home (plan 14 M1 superseded → reclaimed). A2 takes `1773106101`, B takes `1773106102`; strict ascending merge order, never two in one push.

---

## Context

**Charter (spec "Handoff briefs → A. bench-core", the capture-first slice).** The spec's Sequencing is explicit: *"A's capture slice lands first and alone if needed — it is cheap, invisible, and every day it runs accrues the data everything else consumes."* This plan is exactly that slice. It owns the **FIXTURE contract** — the registry row shape, the `input_ref`/`output_ref` blob-ref shapes, content-hash splits, retention, the `source` taxonomy, and the non-replayable flags — that A2 (replay/harness), B (evaluators), C (scorecards), and D (14 disposition) all consume. It does **not** build replay, experiments, scores, routes, or fly verbs (those are A2/B; see Scope-out).

**Accrual honesty (2026-07-19 post-review):** v1 captures are non-replayable (`input-bundle-partial`) and expire in 30d; the replayable corpus begins accruing only when A2's Task 6a (overlay write path + full-bundle pinning) deploys — plan the first bake-off ≥ a week after A2, or pin a starter set.

**Guiding principles this plan is bound by (spec "Guiding principles"):**
- **Principle 2 — capture is free.** Capture is fire-and-forget, non-fatal, mirroring the existing gateway metering and `seedOutcome` pattern (`atc/exec/harvest_step.go:481`). A capture failure is logged and never fails or slows the production step.
- **Principle 3 — labels are JOINS.** The registry row holds **only join keys** (`ticket_id`, `build_id`, `plan_id`, `repo`) and content pins. It carries **no** verdict, score, merge-state, or human-touch column. Those are query-time joins into `agent_feedback`/`agent_reviews`/`agent_run_metrics`/`agent_outcomes` (owned by B's join task). **This plan builds no label tables and no label-sync machinery.**
- **House rule — no silent caps.** An over-bound overlay, a partial capture, or a parked (`ask_human`) step writes a registry row with `replayable=false` + a non-empty `skip_reason`. Never a silent drop.

**This plan PRODUCES (contract surface `fixture-contract`):**
- 00-shared-contracts.md **§1.15 `agent_step_fixtures`** (next free §1.x slot — §1.13 is Platform-credential-policy, §1.14 is `agent_run_step_state`) — the DDL, the `input_ref`/`output_ref`/`ground_truth` JSON shapes, the retention rule (`expires_at`), the split rule, the `source` taxonomy, the non-replayable rule, and the blob-store + inline-JSON bound decisions (Task 1 addendum resolves skeleton open Qs 1, 2, 5, 6). The **overlay** bound apparatus (materialization + `--agent-fixture-overlay-max-bytes` + `PutOverlay`) is A2's (its Task 6a) — A1 freezes only the `overlay_ref` shape + the 32 MiB cap **value** as a contract decision (Q2), and ships no overlay code (no producer exists until A2's restore path).
- 00-shared-contracts.md **§2.9 Fixture** — `agent/api/fixtures` domain types (`Fixture`, `Store` interface, `Source`, `Split`, `InputRef`, `OutputRef`, `FixtureFilter`, `Bundle`, plus the synthetic family `InjectedFault`/`Loc`/`SyntheticFixture`/`SyntheticFixtureStore` moved here from B — amended 2026-07-19 post-review), `atc/db.NewAgentStepFixturesFactory`, and the content-hash/split contract.
- The **capture hook** wired into both server-side ingestion paths, off by default.
- The **retention reaper** component (`agent_fixture_reaper`) and its web flags.

**Historical, unimplemented dependency assumptions (recorded as though already landed in `00-shared-contracts.md` and earlier §11 addenda):**
- **agent-step (wave 2):** `AgentStep.ingestFlightRecorder` @ `atc/exec/agent_step.go:684` (call site `:661`); the F4 detached-context (`context.WithoutCancel(ctx)` + 30s, `agent_step.go:708`); the `step.metricsStore == nil` early return (`:695`); the buffered raw read of `results.json` (`raw` at `:762`, `io.ReadAll`). **GOTCHA (verified):** `events.ndjson` is **NOT** buffered — it is streamed event-by-event through `schema.NewEventReader(rc)` at `:786` and the raw bytes are discarded; there is no `rawEvents` slice. To capture `events_ref` (a frozen `output_ref` field) the hook must **tee** the events `rc` into a `bytes.Buffer` during the existing read loop (one in-memory copy, still no extra artifact read) — see Task 7. Also consumes the metrics key `(build_id, plan_id)` and the join-key fields on `schema.RunMetrics` (`BuildID`, `PlanID`, `TicketID`, `PipelineRunID`, `WorkflowName/Version/Hash`); `WithAgentMetricsStore` option; the `--agent-step-image` value (`step.agentImage`).
- **harvest-step (wave 3):** `HarvestStep.ingestAndRecord` @ `atc/exec/harvest_step.go:538` (call site `:370`); F4 detached-context (`harvest_step.go:563`); the `step.metricsStore == nil` early return (`:545`); the buffered raw reads of `results.json` (`raw` at `:615`, into `parsedResults` `:552`) and `review.json` (`reviewRaw` at `:697`/`:606`). **GOTCHA (verified):** as on the agent path, `events.ndjson` is streamed through `schema.NewEventReader(rc)` at `:640` and **not** buffered — tee it if `events_ref` is wanted (Task 8). Also consumes `step.plan.Repo` (`HarvestPlan.Repo`, `atc/plan.go`); `schema.Results.Metadata` shas via `metaString(res.Metadata, "head_sha"/"base_sha")` (`harvest_step.go:494`, `metaString` @ `:513` — package-`exec` scope, reachable from `agent_step.go`); the harvest runner image `step.agentImage` (`harvest_step.go:127`, **not** a `harvestImage` field — none exists); the `seedOutcome` fire-and-forget precedent (`:481`).
- **agent/schema:** `schema.Results{Status, Summary, Metadata map[string]interface{}, Validate()}` (`agent/schema/results.go:34`); `schema.RunMetrics`.
- **delivery-outcomes (plan 12):** the `agent/api/outcomes` package shape (types.go + memory_store.go + fake + `atc/db` factory) is the **template** this plan's `agent/api/fixtures` package mirrors verbatim in structure. The `agent_outcomes` reaper-free lifecycle and the `--agent-outcome-git-dir` master-switch flag pattern are the models for the fixture reaper + `--agent-fixture-store-dir`.
- **atccmd wiring:** engine options applied at `atc/atccmd/command.go:2214` (`engine.WithAgentMetricsStore`) / `:2218` (`engine.WithAgentReviewsStore`); the `RunnableComponent` slice + `credentials.NewRunSecretReaper` @ `:1395–1407` (the reaper-component template, `Interval: time.Minute`); the host-path storage flag precedent `--kubernetes-artifact-daemon-host-path` (`:206`).

**Wave-mates (parallel, NOT landed — additive-merge coordination only):**
- **A2 replay/harness** — consumes `fixtures.Store`, `Fixture`, `InputRef`/`OutputRef`, `FixtureFilter`/`Bundle`, `split`, and the migration `1773106100` FK target. Admission POLICY (default sets, per-repo caps, recency) is A2's own, implemented in `agent/benchrunner` atop this plan's `List(FixtureFilter)` — honoring Q6; there is no `Admit` on any A1 surface (amended 2026-07-19 post-review). Migration `1773106101` (`agent_bench_cells.fixture_id REFERENCES agent_step_fixtures(id)`) lands **after** this. No shared files except additive merges in `atc/component.go`, `atc/atccmd/command.go`, and the `jetbridgeHeadMigration` const.
- **B evaluators** — consumes `ground_truth` (synthetic recall) and the join keys, and imports this plan's synthetic types (`InjectedFault`/`Loc`/`SyntheticFixture`/`SyntheticFixtureStore` live HERE — moved from B, amended 2026-07-19 post-review; B's "land before A1" constraint is deleted). B's corpus-builder registers `source:'synthetic'` fixtures through B's registration route, which calls this plan's native `RegisterSynthetic`.

**Anchor caveat:** line anchors here were read at HEAD on branch `jetbridge`. The wave-1..4 agentic work already sits above them, but the named symbols (`ingestFlightRecorder`, `ingestAndRecord`, `seedOutcome`, `WithAgentMetricsStore`, `NewRunSecretReaper`) are stable — treat every anchor as "the location of the quoted code" and search for the symbol, not the line number.

---

### Basic-experience guardrail

The spec's simplicity contract (spec "The basic experience") is: **"Capture requires nothing."** This plan must keep it that way. A1 introduces **zero** knobs on the hot path a user or operator has to think about to get fixtures:

- Capture fires automatically inside the ingestion path that already runs on every agent/harvest step. There is no capture verb, no per-step opt-in, no fixture config in the workflow YAML.
- **One control turns the slice on** — the master switch `--agent-fixture-store-dir`. **Empty (the default) = the entire slice is a no-op**: no capturer is wired, `ingestFlightRecorder`/`ingestAndRecord` skip the guarded call, no reaper runs. The remaining flags (`--agent-fixture-retention`, `--agent-fixture-inline-max-bytes`, `--agent-fixture-reaper-interval`) are **defaulted tuning knobs, revealed not required** — every one has a working default and none needs a thought to get fixtures. (There is no `--agent-fixture-overlay-max-bytes` in A1: overlay materialization has no producer until A2, so that knob ships with the overlay machinery in A2, not here — no dead operator control lands in the capture slice.)
- Everything sophisticated — inline-JSON spill, the holdout fraction, `source`/`split`/`pinned`/retention, `ground_truth` — has a working default and is **revealed only when you dig** (Task 1 addendum, drill-down queries in A2). None of it appears on the two-field `fly agent bench run` path or in the capture hook a step author sees. Complexity is revealed, never upfront (spec principle 6).

---

### Task 1: Wave-start contract addendum — §1.15 `agent_step_fixtures`, blob-store + retention decisions, migration-registry claim

Freeze the fixture contract in writing where A2/B/C/D read it, **before any code**. This task resolves the skeleton's A1-owned open questions (Q1 dedup scope, Q2 blob mechanics + bounds, Q5 pinning↔retention, Q6 holdout fraction) and claims migration `1773106100`.

**Section-number discipline (verified against the live doc):** the fixture registry goes to **§1.15** — the next free §1.x. `§1.13` is already **Platform credential policy** (owner: credentials-and-budgets) and `§1.14` is `agent_run_step_state` (owner: agent-step); inserting a second `§1.13` would clobber the credential policy and mis-point every A2/B/C/D reader. `§2.9` **is** free (highest existing 2.x is §2.8.1), so the Go-types section stays §2.9.

**Migration-registry reconciliation (load-bearing — the skeleton forbids silent migration drift):** the §1.1 registry currently gives the **whole** `1773106100–109` block to `process-intel-experiments` (row: `agent_benchmark_cases, agent_experiments, agent_experiment_runs`), and §1.12 still shows `1773106100_create_agent_benchmark_cases` as live DDL. A1 reclaims `1773106100`, so this task must **also** retarget the process-intel row and add a SUPERSEDED banner to §1.12 — otherwise `1773106100` is allocated to two tables at once. (See the two extra edit steps below.)

**Files:**
- Modify: `docs/superpowers/plans/agentic-platform/00-shared-contracts.md`:
  - insert `### 1.15 agent_step_fixtures` **after §1.14** (`agent_run_step_state`), at the end of the §1 DDL block;
  - **edit the existing §1.1 registry row** `1773106100–109 | process-intel-experiments | …` → retarget it to `1773106103–109 | process-intel-experiments (retained: agent_reviews.defect_link/M2 only — M1 benchmark/experiment tables superseded by bench)` **and add a new bench row** `1773106100–102 | bench (A1 fixtures / A2 experiments+cells / B scores) | 1773106100 agent_step_fixtures — reclaims plan-14 M1 block`;
  - **add a SUPERSEDED banner atop §1.12** pointing to the bench sections (agent_benchmark_cases NOT built → `source='benchmark'` fixtures in §1.15; agent_experiments/agent_experiment_runs → A2's `agent_bench_experiments`/`agent_bench_cells` at `1773106101`) so no reader lands on a live `1773106100` reservation;
  - append the §11 amendment-log entry.

> **Note for the plan-writer's own boundary:** this task's *deliverable* is the addendum text below. The `00-shared-contracts.md` edit is the first execution step, exactly as plan 12 Task 1 and plan 13/14 Task 1 do it — this plan file does not itself touch the contracts doc.

**Steps:**

- [ ] Insert `### 1.15 agent_step_fixtures — owner: bench-A1 (fixture registry)` into `00-shared-contracts.md §1` (after §1.14), carrying the DDL exactly as Task 2 ships it (below), plus these frozen decisions:

  **DDL (migration `1773106100`)** — per captured production step execution, join-keys-only (principle 3):
  ```
  agent_step_fixtures(
    id, created_at,
    source      production|synthetic|benchmark   (CHECK),
    step_kind   review|implement|plan|workflow    (CHECK),
    repo,
    ticket_id NULL, build_id NULL, plan_id '',    -- join keys ONLY; labels are joins
    content_hash,                                  -- of the pinned input bundle; split + blob dedup
    split       open|holdout   (CHECK, default 'open'),
    tags TEXT[], pinned BOOL, replayable BOOL, skip_reason '',
    input_ref  JSONB,   -- {repo, base_sha, overlay_ref, ticket_snapshot_ref, config_ref,
                        --  env:{runner_image, sidecars:[...]}}
    output_ref JSONB,   -- {result_ref, results_json_ref, events_ref, review_json_ref?}
    ground_truth JSONB, -- synthetic only: [{class, location, description}]
    expires_at TIMESTAMPTZ
  )
  ```
  Indexes: non-unique `(content_hash)`; `(step_kind, split) WHERE replayable`; `(ticket_id) WHERE ticket_id IS NOT NULL`; `(build_id, plan_id) WHERE build_id IS NOT NULL`; `(expires_at) WHERE expires_at IS NOT NULL`.

  **Decision — Q1 dedup scope [DECIDED HERE]:** the registry is **one row per captured execution** (`content_hash` index is **NON-unique**). "Content-addressed" means **blob-store** dedup: two byte-identical input bundles share one on-disk blob (addressed by digest) but keep two registry rows, because principle-3 label joins are per-execution 1:1 with `(build_id, plan_id)`. There is no canonical-execution collapse.

  **Decision — Q2 blob mechanics + bounds [DECIDED HERE]:** blobs live in a **host-path / PVC-backed fixture store directory** on the web node, named by `--agent-fixture-store-dir` (the master switch — empty disables capture and the reaper both). This mirrors `--kubernetes-artifact-daemon-host-path` (`atc/atccmd/command.go:206`) and `--agent-outcome-git-dir` (plan 12 §1.11.1): the store survives artifact-fabric GC and web restarts and is mountable into replay pods (A2). Blobs are content-addressed files `fixtures/<sha256>[.tar]`. **Inline-JSON bound = 512 KiB** (`512 << 10`, the `maxSkillsRenderBytes` precedent, `agent/dispatch/render.go:330`; the L-3 evidence precedent): a `*_ref` JSON payload at or under the bound MAY be stored inline as a `inline:` ref; over the bound it spills to a `blob://` ref. **Overlay bound = 32 MiB (`33554432`) [contract value frozen HERE, machinery in A2]:** the `overlay_ref` field shape and the 32 MiB cap value are frozen now so A2 can build against them, but A1 ships **no** overlay code — there is no overlay producer until A2's restore/materialization path. When A2 lands overlay materialization it also lands `--agent-fixture-overlay-max-bytes` (default `33554432`) and the rule *an overlay over the bound writes the row `replayable=false, skip_reason='overlay-over-bound:<n>B'` and stores no overlay blob* (house rule: visible, filterable, never silently dropped). A1's `input_ref.overlay_ref` is reserved and always empty (v1 captures pin no overlay).

  **Decision — Q5 pinning ↔ retention [DECIDED HERE]:** `expires_at` is set **at capture**: `now() + retention` (default 30d, `--agent-fixture-retention`) **only when** `source='production' AND pinned=false AND split='open'`; **NULL (persist) otherwise** — so pinned, holdout, synthetic, and benchmark fixtures never expire (spec §2 "pinned, holdout, and synthetic fixtures persist"). The reaper deletes rows (and their blobs) `WHERE expires_at IS NOT NULL AND expires_at < now()` only. A2's experiment admission sets `pinned=true` (and NULLs `expires_at`), and `agent_bench_cells.fixture_id` is `ON DELETE RESTRICT` — so a pinned fixture a cell references can never be reaped; `SET NULL` is **rejected** (a cell must never outlive its fixture).

  **Decision — Q6 holdout fraction [DECIDED HERE]:** `split = holdout` iff `hash_mod5(content_hash) == 0` (20%); else `open`. Computed **once at capture**, stored in the `split` column — sticky and un-gameable by selection (spec §1 "Splits"). Default experiment fixture sets (A2) draw `source='production' AND split='open' AND replayable=true`; holdout is reserved for A2/C pre-promotion confirmation. Per-repo caps and the recency window are A2's harness policy, not a registry concern.

  **Non-replayable rule:** `replayable=false ⟺ skip_reason != ''`. A1 skip_reasons: `input-pin-unresolved` (no `base_sha`); `input-bundle-partial` (base_sha present but the full restore bundle — ticket-state snapshot, step config, non-git overlay — is not yet pinned, which is **every v1 agent/harvest production capture**, see below); `parked-ask-human` (a step that parked on `ask_human`). (`overlay-over-bound:<n>B` is added by A2 when overlay materialization lands.) **Load-bearing honesty decision [DECIDED HERE — finding, 2026-07-19]:** because A1's hooks pin only `(base_sha, env)` and pass **no** snapshot/config/overlay (A2's restore work refines input pinning), a v1 production fixture is **`replayable=false, skip_reason='input-bundle-partial'`** — NOT `replayable=true`. Marking these replayable would corrupt the cross-track `replayable` field A2/B/C all consume: A2's default sets (`ListReplayable`, the `(step_kind, split) WHERE replayable` index) would draw rows A2 provably cannot restore ("Replay restore failure → cell error"). A2 flips new captures to `replayable=true` when it pins the full bundle. The `Capturer` itself is general — given a complete bundle (`InputBundleComplete=true`, which A2 passes) it emits `replayable=true`; only A1's two hooks pass the partial signal. Non-replayable rows are **retained and filterable**, excluded from default sets — never deleted for being non-replayable (they still expire on the normal production+open+unpinned schedule). **Historical `input-bundle-partial` rows are explicitly excluded forever (2026-07-19 post-review, accepted handoff):** when A2's Task 6a lands the full-bundle write path, already-captured partial rows are NOT backfilled or re-pinned — fresh captures supersede them within days, and re-pinning is not worth building.

- [ ] Reconcile the §1.1 migration-registry table (do **both** edits — a bare add leaves `1773106100` double-allocated):
  - **Retarget** the existing row `1773106100–109 | process-intel-experiments | agent_benchmark_cases, agent_experiments, agent_experiment_runs` → `1773106103–109 | process-intel-experiments (retained: agent_reviews.defect_link/M2 only — M1 benchmark/experiment tables superseded by bench)`.
  - **Add** the bench row `1773106100–102 | bench (A1 fixtures / A2 experiments+cells / B scores) | 1773106100 agent_step_fixtures — reclaims plan-14 M1 block (agent_benchmark_cases NOT built)`.
  - Note the merge-order hazard: land `100 → 101 → 102` strictly ascending, never two in one push (cells FK fixtures, scores FK cells → ascending == referential order).

- [ ] Add a **SUPERSEDED banner** as the first line of §1.12 (Experiment substrate): `> **SUPERSEDED (2026-07-19, bench A) — do NOT build these tables at 1773106100–102.** agent_benchmark_cases → NOT built; imported end-to-end cases become source='benchmark' fixtures in §1.15 agent_step_fixtures (1773106100). agent_experiments / agent_experiment_runs → A2's agent_bench_experiments / agent_bench_cells (1773106101). The 1773106100 reservation below is reclaimed by the bench; §1.12's DDL is retained only as design reference.` This guarantees no reader lands on §1.12 and treats its `1773106100` reservation as live.

- [ ] **C/D/E absorption (2026-07-19 review round — this task owns the deferred D/E §11 lines by name):**
  - (a) append to the §1.12 SUPERSEDED banner D's disposition pointer, verbatim from `bench/D-14-disposition.md` (its "Contract §1.12 / §1.1 / §11 addendum" subsection): *"Full per-task disposition: `bench/D-14-disposition.md`."*
  - (b) append Track E's null-disposition line **verbatim** as its **own** §11 entry, owner "bench-A1 on behalf of Track E" — copy the blockquote from `bench/E-gateway-disposition.md` Task 1 beginning *"2026-07-19 (bench wave; owner: bench-core; re: gateway): Track E adds **no** migration, **no** route, **no** fly verb, and **no** code to `10-gateway-mcp.md`…"*.
  - (c) post-land, add one confirmation line to D's and E's own §11 logs recording that A1 Task 1 carried their deferred lines (E Open decision 2 and D's §1.12-single-writer coordination item point at this bullet).

- [ ] Append the §11 amendment-log entry: `2026-07-19 (bench-A1 planning): added §1.15 agent_step_fixtures — per-execution fixture registry, join-keys-only (labels are joins, no label tables). Frozen: blob-store dedup + NON-unique content_hash index (Q1); host-path fixture store via --agent-fixture-store-dir master switch, 512KiB inline bound (Q2); output_ref gains an additive review_json_ref field (harvest-step captures buffer review.json) beyond the frozen {result_ref, results_json_ref, events_ref} shape — recorded here as a raised additive extension, not a silent drift, so A2's replay/restore consumer knows the field exists; overlay_ref shape + 32MiB cap VALUE frozen but overlay materialization + --agent-fixture-overlay-max-bytes deferred to A2 (no producer in A1); v1 production captures are replayable=false skip_reason='input-bundle-partial' (only base_sha+env pinned; A2 flips to replayable=true when it pins the full restore bundle); expires_at set only for production+open+unpinned, reaper deletes only expired, cells.fixture_id RESTRICT (Q5); holdout = hash mod 5 == 0 (Q6). RECONCILED §1.1: retargeted process-intel-experiments row to 1773106103–109 and added bench row 1773106100–102; added SUPERSEDED banner to §1.12. Supersedes plan-14 agent_benchmark_cases (benchmark cases import as source:'benchmark' fixtures). Claims migration 1773106100. AMENDED 2026-07-19 post-review (bench plan-set review): package identity is agent/api/fixtures (package fixtures, matching agent/api/outcomes — was agent/fixture); Store grows List(FixtureFilter{StepKind,Split,Tags,IDs,Limit}) / Tag(id, add, remove) / Bundle(id) -> Bundle{Repo, BaseSHA, Overlay, TicketSnapshot, Config []byte; Env EnvPins}, and Pin becomes Pin(id int, pinned bool); admission POLICY (default sets, caps, recency) is A2-owned in agent/benchrunner atop List — no Admit here (Q6 honored); the synthetic family (InjectedFault/Loc/SyntheticFixture/SyntheticFixtureStore) lives here (moved from B; B's land-before-A1 constraint deleted) and the db factory implements RegisterSynthetic natively; historical input-bundle-partial rows excluded forever (no backfill when A2 Task 6a lands the full-bundle write path); benchmark import rides B's registration route (no A1 importer). Affects: bench-A2, bench-B, bench-C, bench-D, process-intel-experiments.`

- [ ] Commit: `git add docs/superpowers/plans/agentic-platform/00-shared-contracts.md && git commit -m "docs(agentic): bench-A1 contract addendum - agent_step_fixtures registry, blob/retention/split decisions"`

---

### Task 2: Migration 1773106100 — `agent_step_fixtures`

**Files:**
- Create: `atc/db/migration/migrations/1773106100_create_agent_step_fixtures.up.sql`
- Create: `atc/db/migration/migrations/1773106100_create_agent_step_fixtures.down.sql`
- Modify: `atc/db/migration/legacy_upgrade_test.go:37` (`jetbridgeHeadMigration` const — currently `1773106090`; on-disk head is already `1773106091`, so this bumps it to `1773106100`, only-if-higher).

**Steps:**

- [ ] Write `1773106100_create_agent_step_fixtures.up.sql` (picked up automatically via `go:embed migrations`, no registration code):

```sql
CREATE TABLE agent_step_fixtures (
    id            SERIAL PRIMARY KEY,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    source        TEXT NOT NULL DEFAULT 'production'
                  CHECK (source IN ('production','synthetic','benchmark')),
    step_kind     TEXT NOT NULL
                  CHECK (step_kind IN ('review','implement','plan','workflow')),
    repo          TEXT NOT NULL,

    -- JOIN KEYS ONLY (principle 3). Labels live in agent_feedback / agent_reviews /
    -- agent_run_metrics / agent_outcomes and are joined at query time.
    ticket_id     INTEGER,                   -- agent_tickets.id (NULL for pure-CI steps)
    build_id      INTEGER,                   -- builds.id == agent_run_metrics.build_id
    plan_id       TEXT NOT NULL DEFAULT '',  -- (build_id, plan_id) is the metrics upsert key

    content_hash  TEXT NOT NULL,             -- of the pinned input bundle; split + blob dedup
    split         TEXT NOT NULL DEFAULT 'open'
                  CHECK (split IN ('open','holdout')),
    tags          TEXT[] NOT NULL DEFAULT '{}',
    pinned        BOOLEAN NOT NULL DEFAULT false,
    replayable    BOOLEAN NOT NULL DEFAULT true,
    skip_reason   TEXT NOT NULL DEFAULT '',   -- non-empty  <=>  replayable=false

    input_ref     JSONB NOT NULL,
    output_ref    JSONB,
    ground_truth  JSONB,                      -- synthetic only

    expires_at    TIMESTAMPTZ                 -- NULL = persist
);

CREATE INDEX agent_step_fixtures_content    ON agent_step_fixtures (content_hash);
CREATE INDEX agent_step_fixtures_kind_split ON agent_step_fixtures (step_kind, split) WHERE replayable;
CREATE INDEX agent_step_fixtures_ticket     ON agent_step_fixtures (ticket_id)  WHERE ticket_id IS NOT NULL;
CREATE INDEX agent_step_fixtures_build_plan ON agent_step_fixtures (build_id, plan_id) WHERE build_id IS NOT NULL;
CREATE INDEX agent_step_fixtures_expiry     ON agent_step_fixtures (expires_at) WHERE expires_at IS NOT NULL;
```

- [ ] Write `1773106100_create_agent_step_fixtures.down.sql`: `DROP TABLE agent_step_fixtures;`
- [ ] In `legacy_upgrade_test.go:37`, set `const jetbridgeHeadMigration = 1773106100` **only if the current value is lower** (never lower it; A2/B will bump to 101/102 in their own plans).
- [ ] Verify: `pg_isready && ginkgo --focus='Legacy Database Upgrade' ./atc/db/migration/` — green (walks empty + fixture DBs to HEAD; a SQL syntax error, a missing down file, or a stale head const fails here). **Note (finding, verified):** the walk migrates to the `jetbridgeHeadMigration` const. Bumping it from `1773106090` → `1773106100` pulls **`1773106091` (`create_agent_settings`)** into the walk **for the first time** — it was previously above the target-versioned walk and never exercised here. Confirm the walk applies `1773106091` **and** `1773106100` cleanly on the fixture DBs; a latent issue in `1773106091` would surface red here and must NOT be mis-attributed to the new `1773106100`.
- [ ] Commit: `git add atc/db/migration && git commit -m "feat(bench): migration 1773106100 - agent_step_fixtures registry table"`

---

### Task 3: `agent/api/fixtures` — domain types, `Store` interface, `MemoryStore`, fake

New leaf package (imports only stdlib + `agent/schema` for nothing yet — it stays leaf so `atc/exec` and `atc/db` can both import it without cycles, exactly as `agent/api/outcomes` does). Plain `testing`, matching `agent/api/outcomes`. **(Amended 2026-07-19 post-review:** package identity is `agent/api/fixtures`, package `fixtures` — the exact `agent/api/<domain>` template; the `Store` surface grows `List(FixtureFilter)`/`Tag`/`Bundle` and the two-arg `Pin(id, pinned)`; the synthetic family moved here from B lives in this file too.**)**

**Files:**
- Create: `agent/api/fixtures/types.go`
- Create: `agent/api/fixtures/memory_store.go`
- Test: `agent/api/fixtures/types_test.go`

**Steps:**

- [ ] Write the failing test `agent/api/fixtures/types_test.go` asserting: `ValidSource`/`ValidStepKind`/`ValidSplit` accept the CHECK values and reject others; `Fixture.Replayable == false` requires a non-empty `SkipReason` (a `Validate()` method returns an error on the mismatch, both directions); `MemoryStore.Insert` returns a positive id and `Get` round-trips the row including `InputRef`/`OutputRef`/`GroundTruth`; `ListReplayable(stepKind, split)` returns only `replayable && matching kind/split`; `ListExpired(now)` returns only rows with a non-NULL `ExpiresAt < now`; `Delete(id)` removes it; `Pin(id, true)` sets `pinned=true` and NULLs `ExpiresAt`, and `Pin(id, false)` clears the flag (amended 2026-07-19 post-review — two-arg form); `List(FixtureFilter{StepKind, Split, Tags, IDs, Limit})` filters accordingly, newest first (the general query surface A2's benchrunner builds its admission policy atop); `Tag(id, add, remove)` adds/removes tags idempotently; `Bundle(id)` hydrates `{Repo, BaseSHA, Overlay, TicketSnapshot, Config, Env}` from the row's refs via the blob store.

- [ ] Run `go test ./agent/api/fixtures/` — expect compile failure (package does not exist).

- [ ] Write `agent/api/fixtures/types.go`:

```go
// Package fixtures holds the bench fixture-registry domain types (shared-
// contracts §1.15/§2.9): a per-execution record of a captured step's pinned
// input bundle + output refs, plus the synthetic-fixture family (moved here
// from B, 2026-07-19 post-review). It carries ONLY join keys and content
// pins — no label columns (principle 3: labels are query-time joins). Leaf
// package: imported by atc/exec (capture hook) and atc/db (factory) with no
// cycle.
package fixtures

import (
	"errors"
	"time"
)

type Source string
type Split string

const (
	SourceProduction Source = "production"
	SourceSynthetic  Source = "synthetic"
	SourceBenchmark  Source = "benchmark"

	SplitOpen    Split = "open"
	SplitHoldout Split = "holdout"
)

// StepKinds and the source/split validators back the DB CHECK constraints so a
// bad value fails in Go before it reaches Postgres.
var stepKinds = map[string]bool{"review": true, "implement": true, "plan": true, "workflow": true}

func ValidSource(s Source) bool   { return s == SourceProduction || s == SourceSynthetic || s == SourceBenchmark }
func ValidStepKind(k string) bool { return stepKinds[k] }
func ValidSplit(s Split) bool     { return s == SplitOpen || s == SplitHoldout }

// InputRef pins the replayable input bundle (spec §1 workspace pinning).
// env pins are load-bearing: the A0-1 incident (silent runner-image skew) is
// why replays must record the environment they ran under.
type InputRef struct {
	Repo              string   `json:"repo"`
	BaseSHA           string   `json:"base_sha,omitempty"`
	OverlayRef        string   `json:"overlay_ref,omitempty"`         // inline:… or blob://…
	TicketSnapshotRef string   `json:"ticket_snapshot_ref,omitempty"` // inline:… or blob://…
	ConfigRef         string   `json:"config_ref,omitempty"`          // inline:… or blob://…
	Env               EnvPins  `json:"env"`
}

// EnvPins is the frozen environment the production step ran under.
type EnvPins struct {
	RunnerImage string   `json:"runner_image,omitempty"`
	Sidecars    []string `json:"sidecars,omitempty"` // resolved sidecar image tags
}

// OutputRef records the produced step output — a free baseline alongside any
// experiment's explicit baseline variant (spec §1 "Recorded output").
type OutputRef struct {
	ResultRef      string `json:"result_ref,omitempty"`       // result_sha or overlay ref
	ResultsJSONRef string `json:"results_json_ref,omitempty"` // inline:… or blob://…
	EventsRef      string `json:"events_ref,omitempty"`
	ReviewJSONRef  string `json:"review_json_ref,omitempty"`  // harvest steps only
}

// (GroundTruthFault deleted 2026-07-19 post-review — ONE ground-truth shape
// exists: InjectedFault below. Fixture.GroundTruth and the capturer use it
// directly, so RegisterSynthetic's manifest round-trips through Get without
// a second encoding.)

// FixtureFilter is the general query surface List exposes. A2's benchrunner
// builds its default-set ADMISSION POLICY (per-repo caps, recency window)
// atop it — policy is A2's, not a registry concern (Q6). (Added 2026-07-19
// post-review.)
type FixtureFilter struct {
	StepKind string   `json:"step_kind,omitempty"`
	Split    Split    `json:"split,omitempty"`
	Tags     []string `json:"tags,omitempty"`
	IDs      []int    `json:"ids,omitempty"`
	Limit    int      `json:"limit,omitempty"`
}

// Bundle is the hydrated restore bundle A2's replay renderer consumes: the
// row's *_refs resolved to bytes plus the pinned env. (Added 2026-07-19
// post-review; the snapshot→tickets.Ticket adapter is A2-side, in
// agent/benchrunner/replay — this package ships no SnapshotTicket.)
type Bundle struct {
	Repo           string
	BaseSHA        string
	Overlay        []byte
	TicketSnapshot []byte
	Config         []byte
	Env            EnvPins
}

// ——— synthetic family (moved here from B, 2026-07-19 post-review — one
// frozen ground-truth manifest shape shared by B's eval/corpus/benchcorpus,
// which import it from here) ———

// InjectedFault is one entry of a synthetic fixture's fault manifest (the
// frozen ground_truth shape the recall evaluator scores against).
type InjectedFault struct {
	Class       string `json:"class"`
	Location    Loc    `json:"location"`
	Description string `json:"description"`
}

type Loc struct {
	File string `json:"file"`
	Line int    `json:"line"`
}

// SyntheticFixture is the synthetic write surface: B's registration route
// (which accepts source ∈ {synthetic, benchmark}) hands one to the atc/db
// factory, which implements RegisterSynthetic natively.
type SyntheticFixture struct {
	Repo        string          `json:"repo"`
	StepKind    string          `json:"step_kind"`
	BaseSHA     string          `json:"base_sha"`
	OverlayRef  string          `json:"overlay_ref,omitempty"`
	ContentHash string          `json:"content_hash"`
	GroundTruth []InjectedFault `json:"ground_truth"`
	Tags        []string        `json:"tags,omitempty"`
}

// SyntheticFixtureStore is the write contract the atc/db fixtures factory
// implements: Insert with Source='synthetic', Pinned=true (persists),
// Split=AssignSplit(f.ContentHash), the fault manifest carried in
// ground_truth.
//
//counterfeiter:generate . SyntheticFixtureStore
type SyntheticFixtureStore interface {
	RegisterSynthetic(f SyntheticFixture) (int, error)
}

// Fixture mirrors agent_step_fixtures (§1.15). Timestamps are epoch seconds
// on the wire (matching every other agent API type); ExpiresAt is a pointer so
// NULL (persist) is distinct from the zero epoch.
type Fixture struct {
	ID          int                `json:"id"`
	CreatedAt   int64              `json:"created_at,omitempty"`
	Source      Source             `json:"source"`
	StepKind    string             `json:"step_kind"`
	Repo        string             `json:"repo"`
	TicketID    *int               `json:"ticket_id,omitempty"`
	BuildID     *int               `json:"build_id,omitempty"`
	PlanID      string             `json:"plan_id"`
	ContentHash string             `json:"content_hash"`
	Split       Split              `json:"split"`
	Tags        []string           `json:"tags,omitempty"`
	Pinned      bool               `json:"pinned"`
	Replayable  bool               `json:"replayable"`
	SkipReason  string             `json:"skip_reason,omitempty"`
	InputRef    InputRef           `json:"input_ref"`
	OutputRef   *OutputRef         `json:"output_ref,omitempty"`
	GroundTruth []InjectedFault `json:"ground_truth,omitempty"` // one shape everywhere (2026-07-19 post-review)
	ExpiresAt   *int64             `json:"expires_at,omitempty"`
}

// Validate enforces the house rule replayable=false <=> skip_reason != ""
// and the enum CHECKs, so a malformed fixture never reaches the DB.
func (f *Fixture) Validate() error {
	if !ValidSource(f.Source) {
		return errors.New("invalid fixture source")
	}
	if !ValidStepKind(f.StepKind) {
		return errors.New("invalid fixture step_kind")
	}
	if f.Split != "" && !ValidSplit(f.Split) {
		return errors.New("invalid fixture split")
	}
	if f.Replayable == (f.SkipReason != "") {
		return errors.New("replayable must be false iff skip_reason is set")
	}
	return nil
}

var ErrFixtureNotFound = errors.New("agent step fixture not found")

// Store is the persistence contract, implemented by
// atc/db.NewAgentStepFixturesFactory and MemoryStore. (Surface amended
// 2026-07-19 post-review: List/Tag/Bundle added, Pin is two-arg.)
//
//counterfeiter:generate . Store
type Store interface {
	// Insert persists a new fixture row (one per execution — never dedups on
	// content_hash) and returns its id. It never mutates an existing row.
	Insert(f *Fixture) (int, error)
	Get(id int) (*Fixture, bool, error)
	// List is the general query surface (newest first). A2's benchrunner
	// implements default-set admission policy atop it (per-repo caps,
	// recency window — policy is A2's, Q6).
	List(fil FixtureFilter) ([]Fixture, error)
	// ListReplayable returns replayable rows of the given kind+split, newest
	// first — the default-fixture-set primitive A2 filters further.
	ListReplayable(stepKind string, split Split) ([]Fixture, error)
	// ListExpired returns rows with a non-NULL expires_at < now (epoch secs),
	// the reaper's work-list.
	ListExpired(nowEpoch int64) ([]Fixture, error)
	// Pin(id, true) marks a fixture retention-exempt (experiment admission,
	// A2): sets pinned=true and clears expires_at. Pin(id, false) clears the
	// flag. Idempotent.
	Pin(id int, pinned bool) error
	// Tag adds then removes the given tags (curation surface, A2's routes).
	Tag(id int, add, remove []string) error
	// Bundle hydrates the row's *_refs into bytes via the blob store.
	Bundle(id int) (Bundle, error)
	Delete(id int) error
}

// helper for the store impls
func nowEpoch() int64 { return time.Now().Unix() }
```

- [ ] Write `agent/api/fixtures/memory_store.go` (in-memory `Store` for handler/reaper/capture tests; mirror `agent/api/outcomes/memory_store.go` structure — a mutex + `map[int]*Fixture` + autoincrement id; `Insert` deep-copies, `List` applies the `FixtureFilter` newest-first, `ListReplayable`/`ListExpired` filter+sort, `Pin(id, true)` sets pinned + nils ExpiresAt (false clears the flag), `Tag` mutates tags idempotently, `Bundle` hydrates refs via an injected `BlobStore` — a settable field; tests wire Task 5's `DirBlobStore` or seed `inline:` refs).
- [ ] Run `go test ./agent/api/fixtures/` — expect pass.
- [ ] Generate the fake: `cd agent/api/fixtures && go run github.com/maxbrunsfeld/counterfeiter/v6 -o fixturesfakes/fake_store.go . Store && cd ../../..` then `go build ./agent/...`.
- [ ] Commit: `git add agent/api/fixtures && git commit -m "feat(bench): agent/api/fixtures domain types, Store, MemoryStore, fake"`

---

### Task 4: `agent/api/fixtures` — content hash + deterministic split assignment

The un-gameable, sticky split is load-bearing (spec §1, principle "anti-Goodhart guardrails are structural"). Isolate it as a pure, exhaustively-tested function so it can never drift.

**Files:**
- Create: `agent/api/fixtures/hash.go`
- Test: `agent/api/fixtures/hash_test.go`

**Steps:**

- [ ] Write the failing test `agent/api/fixtures/hash_test.go`: `ContentHash(bundle)` is deterministic across field-order permutations of the same logical bundle (canonicalized), differs when any pin differs (`base_sha`, `overlay` digest, `ticket_snapshot` digest, `config` digest, each env pin), and is a lowercase hex sha256; `AssignSplit(hash)` returns `SplitHoldout` iff `hash_mod5 == 0` and is stable for a fixed hash across 1000 calls; over a large sample of random hashes the holdout fraction is ≈ 20% (assert within a wide band, e.g. 15–25%, to keep it non-flaky).

- [ ] Run `go test ./agent/api/fixtures/` — **expect FAIL** (`undefined: ContentHash`/`AssignSplit`). Observe RED before implementing (house TDD rule).

- [ ] Write `agent/api/fixtures/hash.go`:

```go
package fixtures

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strconv"
	"strings"
)

// HashBundle is the minimal canonical projection of the pinned input bundle
// that drives content addressing + split assignment. It is NOT the full
// InputRef — only the pins that define replay identity (blob digests, not the
// blobs). Callers pass digests of the overlay/snapshot/config (empty when the
// pin is absent) so ContentHash never has to read blob bytes.
type HashBundle struct {
	Repo          string
	BaseSHA       string
	OverlayDigest string
	SnapshotDigest string
	ConfigDigest  string
	RunnerImage   string
	Sidecars      []string // order-insensitive: sorted before hashing
}

// ContentHash is a stable lowercase-hex sha256 over a canonical, field-order-
// independent serialization of the bundle. Sidecars are sorted so a reordered
// (but equivalent) sidecar list hashes identically.
func ContentHash(b HashBundle) string {
	sidecars := append([]string(nil), b.Sidecars...)
	sort.Strings(sidecars) // stdlib; sidecar order must not change the hash
	// length-prefixed fields avoid "ab|c" vs "a|bc" collisions.
	var sb strings.Builder
	for _, f := range []string{
		b.Repo, b.BaseSHA, b.OverlayDigest, b.SnapshotDigest, b.ConfigDigest, b.RunnerImage,
	} {
		writeField(&sb, f)
	}
	for _, s := range sidecars {
		writeField(&sb, s)
	}
	sum := sha256.Sum256([]byte(sb.String()))
	return hex.EncodeToString(sum[:])
}

func writeField(sb *strings.Builder, s string) {
	sb.WriteString(strconv.Itoa(len(s)))
	sb.WriteByte(':')
	sb.WriteString(s)
	sb.WriteByte('|')
}

// AssignSplit is the deterministic, sticky split rule (§1.15 Q6): holdout iff
// the hash's low bytes mod 5 == 0 (~20%). Computed once at capture, then frozen
// in the split column — never recomputed, so it is un-gameable by selection.
func AssignSplit(contentHash string) Split {
	if hashMod5(contentHash) == 0 {
		return SplitHoldout
	}
	return SplitOpen
}

func hashMod5(contentHash string) int {
	// interpret the first 8 hex chars as an unsigned int; mod 5.
	if len(contentHash) < 8 {
		return 1 // never-holdout for a degenerate hash
	}
	v, err := strconv.ParseUint(contentHash[:8], 16, 32)
	if err != nil {
		return 1
	}
	return int(v % 5)
}
```

- [ ] Run `go test ./agent/api/fixtures/` — expect pass.
- [ ] Commit: `git add agent/api/fixtures/hash.go agent/api/fixtures/hash_test.go && git commit -m "feat(bench): fixture content-hash + deterministic sticky split (hash mod 5)"`

---

### Task 5: `agent/api/fixtures` — host-path blob store with inline-JSON bound

The `*_ref` mechanics (Q2): small JSON inline, large blobs content-addressed on a host path. (Overlay materialization — `PutOverlay` + `ErrOverCap` + the over-bound `skip_reason` — is **A2's**, landing with the overlay producer; A1 ships only `PutInlineOrBlob`, which the captured output-JSON refs genuinely exercise. See Scope-out.)

**Files:**
- Create: `agent/api/fixtures/blobstore.go`
- Test: `agent/api/fixtures/blobstore_test.go`

**Steps:**

- [ ] Write the failing spec `agent/api/fixtures/blobstore_test.go` (plain `testing`, `t.TempDir()` as the store dir): a `DirBlobStore` rooted at a temp dir; `PutInlineOrBlob(bytes, bound)` returns an `inline:<base64>` ref for payloads ≤ bound and a `blob://<sha256>` ref (writing `<dir>/<sha256>`) for payloads over bound; two identical payloads produce the **same** `blob://` ref and one on-disk file (dedup); `Get(ref)` round-trips both inline and blob refs byte-for-byte; `Delete(ref)` removes a blob file and is a no-op for an inline ref and for an already-absent blob; a blob file **survives** reopening the store at the same dir (the web-restart-persistence property).

- [ ] Run `go test ./agent/api/fixtures/` — **expect FAIL** (`undefined: DirBlobStore`/`NewDirBlobStore`). Observe RED before implementing (house TDD rule).

- [ ] Write `agent/api/fixtures/blobstore.go`:

```go
package fixtures

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// BlobStore stores fixture payloads addressed by ref. Refs are opaque strings:
// "inline:<base64>" (payload embedded) or "blob://<sha256>" (content-addressed
// file). The A0-1/L-3 bound keeps small JSON inline in the DB and spills only
// large blobs to the host path.
//
// (A2 extends this interface with PutOverlay(r io.Reader, max int64) + ErrOverCap
// when the overlay producer lands; A1 has no overlay producer, so it is omitted
// here — shipping a dead method + a dead error is the exact over-build this slice
// avoids.)
//
//counterfeiter:generate . BlobStore
type BlobStore interface {
	// PutInlineOrBlob returns inline:… when len(b) <= inlineBound, else writes a
	// content-addressed blob and returns blob://<sha256>.
	PutInlineOrBlob(b []byte, inlineBound int) (string, error)
	Get(ref string) ([]byte, error)
	Delete(ref string) error
}

const (
	inlinePrefix = "inline:"
	blobPrefix   = "blob://"
)

// DirBlobStore is the host-path / PVC-backed implementation (--agent-fixture-
// store-dir). The dir survives artifact-fabric GC and web restarts and is
// mountable into replay pods (A2).
type DirBlobStore struct{ dir string }

func NewDirBlobStore(dir string) (*DirBlobStore, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	return &DirBlobStore{dir: dir}, nil
}

func (s *DirBlobStore) PutInlineOrBlob(b []byte, inlineBound int) (string, error) {
	if len(b) <= inlineBound {
		return inlinePrefix + base64.StdEncoding.EncodeToString(b), nil
	}
	sum := sha256.Sum256(b)
	digest := hex.EncodeToString(sum[:])
	if err := s.writeOnce(digest, b); err != nil {
		return "", err
	}
	return blobPrefix + digest, nil
}

func (s *DirBlobStore) Get(ref string) ([]byte, error) {
	switch {
	case strings.HasPrefix(ref, inlinePrefix):
		return base64.StdEncoding.DecodeString(strings.TrimPrefix(ref, inlinePrefix))
	case strings.HasPrefix(ref, blobPrefix):
		return os.ReadFile(s.path(strings.TrimPrefix(ref, blobPrefix)))
	default:
		return nil, errors.New("unknown fixture ref scheme")
	}
}

func (s *DirBlobStore) Delete(ref string) error {
	if !strings.HasPrefix(ref, blobPrefix) {
		return nil // inline refs have no file; nothing to delete
	}
	err := os.Remove(s.path(strings.TrimPrefix(ref, blobPrefix)))
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

// writeOnce is content-addressed: an existing identical blob is left as-is
// (dedup). Writes via a temp file + rename so a crashed write never leaves a
// partial blob under its final digest name.
func (s *DirBlobStore) writeOnce(digest string, b []byte) error {
	final := s.path(digest)
	if _, err := os.Stat(final); err == nil {
		return nil
	}
	tmp, err := os.CreateTemp(s.dir, ".tmp-"+digest+"-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), final)
}

func (s *DirBlobStore) path(digest string) string { return filepath.Join(s.dir, "fixtures-"+digest) }
```

- [ ] Run `go test ./agent/api/fixtures/` — expect pass. Generate the `BlobStore` fake: `cd agent/api/fixtures && go run github.com/maxbrunsfeld/counterfeiter/v6 -o fixturesfakes/fake_blob_store.go . BlobStore && cd ../../..`.
- [ ] Commit: `git add agent/api/fixtures && git commit -m "feat(bench): fixture host-path blob store with inline/overlay bounds"`

---

### Task 6: `agent/api/fixtures` — the `Capturer` (assemble → hash → split → bound → persist), non-fatal

The capture facade the hooks call. It composes `Store` + `BlobStore` + bounds into one fire-and-forget entry point that assembles a `Fixture` from what the ingestion path already has in memory, computes hash+split, enforces bounds (→ `replayable=false`+`skip_reason`), sets `expires_at`, and inserts. Pure-ish: the only side effects are `blobs.*` and `store.Insert`, both injected, so it is unit-testable with the fakes/memory stores.

**Files:**
- Create: `agent/api/fixtures/capture.go`
- Test: `agent/api/fixtures/capture_test.go`

**Steps:**

- [ ] Write the failing spec `agent/api/fixtures/capture_test.go` (`MemoryStore` + `DirBlobStore` on `t.TempDir()`) covering:
  - **complete bundle (replayable):** a `CaptureInput` with `base_sha`, small `results.json`/`events.ndjson` bytes, env pins, **and `InputBundleComplete: true`** (the signal A2 passes) → one **replayable** row; `content_hash` matches `ContentHash(bundle)`; `split` matches `AssignSplit`; `output_ref.results_json_ref` round-trips via `blobs.Get`; small JSON is `inline:`; `expires_at` ≈ `now + retention`.
  - **v1 partial bundle (the default A1 hooks pass):** `base_sha` present but `InputBundleComplete` **false** → `replayable=false`, `skip_reason='input-bundle-partial'`, row **still inserted**, output refs still captured (this is the shape every v1 production capture takes — finding, 2026-07-19).
  - **holdout (complete bundle):** a complete bundle whose hash mod 5 == 0 → `split=holdout` **and** `expires_at == nil` (holdout persists).
  - **unresolved pin (no base_sha):** `replayable=false`, `skip_reason='input-pin-unresolved'` (takes precedence over `input-bundle-partial`).
  - **parked step:** `Parked=true` → `replayable=false`, `skip_reason='parked-ask-human'` (highest precedence).
  - **synthetic:** `Source=synthetic` with `GroundTruth` (and `InputBundleComplete: true`) → row carries the manifest and `expires_at == nil` (synthetic persists).
  - **non-fatal:** a `BlobStore` fake returning an error from `PutInlineOrBlob` makes `Capture` return an error (for the caller to log) but the test asserts the caller contract is "returns err, callers ignore it" — verified structurally in Tasks 7/8.

- [ ] Run `go test ./agent/api/fixtures/` — **expect FAIL** (`undefined: NewCapturer`/`Capturer`/`CaptureInput`). Observe RED before implementing (house TDD rule).

- [ ] Write `agent/api/fixtures/capture.go`:

```go
package fixtures

// Config is the operator-resolved capture policy (from atccmd flags).
// (OverlayMax is NOT here — overlay materialization lands in A2 with its
// producer; A1 pins no overlay.)
type Config struct {
	InlineBound   int   // --agent-fixture-inline-max-bytes (default 512<<10)
	RetentionSecs int64 // --agent-fixture-retention (default 30d)
}

// CaptureInput is what the ingestion path has at the hook point plus the pins
// assembled at the call site. results.json/review.json byte slices are the raw
// io.ReadAll'd flight reads (no extra artifact read); EventsND is the events
// stream tee-captured into a bytes.Buffer during the existing NewEventReader
// loop (one in-memory copy — events.ndjson is NOT otherwise buffered).
type CaptureInput struct {
	Source   Source
	StepKind string
	Repo     string
	TicketID *int
	BuildID  *int
	PlanID   string

	// input pins
	BaseSHA    string
	Snapshot   []byte // ticket-state snapshot JSON (A2 populates; nil in A1 v1)
	ConfigJSON []byte // frozen step config (A2 populates; nil in A1 v1)
	Env        EnvPins

	// InputBundleComplete is the honesty signal: true only when the FULL
	// restore bundle (snapshot + config + any non-git overlay) is pinned, so
	// the fixture can actually be replayed. A1's two hooks leave it false (they
	// pin only base_sha+env → skip_reason='input-bundle-partial'); A2 sets it
	// true when it pins the whole bundle. (No overlay []byte field in A1 —
	// overlay materialization is A2's.)
	InputBundleComplete bool

	// output (free baseline)
	ResultsJSON []byte
	EventsND    []byte // tee-captured; empty if the tee/read yielded nothing
	ReviewJSON  []byte // harvest steps only
	ResultSHA   string

	GroundTruth []InjectedFault // synthetic only; one shape everywhere (2026-07-19 post-review)
	Parked      bool               // step parked on ask_human -> non-replayable
}

// Capturer is the fire-and-forget capture facade. Its receiver is nil-safe at
// the call site: hooks guard `if capturer == nil` (master switch off).
type Capturer struct {
	store Store
	blobs BlobStore
	cfg   Config
}

func NewCapturer(store Store, blobs BlobStore, cfg Config) *Capturer {
	return &Capturer{store: store, blobs: blobs, cfg: cfg}
}

// Capture assembles, bounds, hashes, splits, and inserts one fixture. It never
// panics and returns an error only for the caller to LOG — the caller must not
// fail the production step on it (principle 2). Returns the new id on success.
func (c *Capturer) Capture(in CaptureInput) (int, error) {
	f := &Fixture{
		Source:   in.Source,
		StepKind: in.StepKind,
		Repo:     in.Repo,
		TicketID: in.TicketID,
		BuildID:  in.BuildID,
		PlanID:   in.PlanID,
		Tags:     []string{},
	}

	// --- replayable / skip_reason precedence (house rule: flag, never drop) ---
	// parked > no-base_sha > partial-bundle. (overlay-over-bound is added by A2
	// when overlay materialization lands.) input-bundle-partial is the v1
	// production shape: base_sha present but the full restore bundle is not yet
	// pinned, so the row is captured but NOT drawn into A2's default sets.
	var skip string
	switch {
	case in.Parked:
		skip = "parked-ask-human"
	case in.BaseSHA == "":
		skip = "input-pin-unresolved"
	case !in.InputBundleComplete:
		skip = "input-bundle-partial"
	}

	snapRef, snapDigest, err := c.putRef(in.Snapshot)
	if err != nil {
		return 0, err
	}
	cfgRef, cfgDigest, err := c.putRef(in.ConfigJSON)
	if err != nil {
		return 0, err
	}

	f.InputRef = InputRef{
		Repo: in.Repo, BaseSHA: in.BaseSHA, // OverlayRef reserved (A2); empty in A1
		TicketSnapshotRef: snapRef, ConfigRef: cfgRef, Env: in.Env,
	}

	// --- content hash + sticky split (digests, not bytes) ---
	// OverlayDigest is left empty in A1 (no overlay pinned); A2 fills it when it
	// materializes overlays, which changes only NEW captures' hashes (existing
	// rows keep their frozen split column — Open decision 5).
	f.ContentHash = ContentHash(HashBundle{
		Repo: in.Repo, BaseSHA: in.BaseSHA,
		SnapshotDigest: snapDigest, ConfigDigest: cfgDigest,
		RunnerImage: in.Env.RunnerImage, Sidecars: in.Env.Sidecars,
	})
	f.Split = AssignSplit(f.ContentHash)

	// --- output refs (free baseline) ---
	out := &OutputRef{ResultRef: in.ResultSHA}
	if out.ResultsJSONRef, _, err = c.putRef(in.ResultsJSON); err != nil {
		return 0, err
	}
	if out.EventsRef, _, err = c.putRef(in.EventsND); err != nil {
		return 0, err
	}
	if out.ReviewJSONRef, _, err = c.putRef(in.ReviewJSON); err != nil {
		return 0, err
	}
	f.OutputRef = out

	if in.Source == SourceSynthetic {
		f.GroundTruth = in.GroundTruth
	}

	// --- replayable flag + retention (§1.15 Q5) ---
	f.Replayable = skip == ""
	f.SkipReason = skip
	if f.Source == SourceProduction && !f.Pinned && f.Split == SplitOpen {
		exp := nowEpoch() + c.cfg.RetentionSecs
		f.ExpiresAt = &exp
	} // holdout / synthetic / benchmark / pinned => ExpiresAt nil (persist)

	if err := f.Validate(); err != nil {
		return 0, err
	}
	return c.store.Insert(f)
}

// putRef stores a payload (empty => empty ref) and returns (ref, digest).
func (c *Capturer) putRef(b []byte) (string, string, error) {
	if len(b) == 0 {
		return "", "", nil
	}
	ref, err := c.blobs.PutInlineOrBlob(b, c.cfg.InlineBound)
	if err != nil {
		return "", "", err
	}
	return ref, digestOf(b), nil
}
```

  (Add the tiny helper `digestOf` — a hex sha256 used only for the hash bundle, distinct from the blob-store's internal digest. No `bytesReader`/`itoa` needed — the overlay branch that used them is A2's.)

- [ ] Run `go test ./agent/api/fixtures/` — expect pass.
- [ ] Commit: `git add agent/api/fixtures && git commit -m "feat(bench): fixture Capturer - assemble, hash, split, bound, persist (non-fatal)"`

---

### Task 7: Capture hook option on `AgentStep` + wire into `ingestFlightRecorder`

Add the capturer as an optional field and fire it inside `ingestFlightRecorder`, under the same detached context, guarded and non-fatal — mirroring the `metricsStore == nil` early-return contract. **Three scoping realities (verified at HEAD) the implementer must handle before the call compiles:** (1) `results.json`'s bytes (`raw`) and the parsed `results` are **block-scoped** inside `if flightArtifact != nil` at `:762`/`:765` — they must be hoisted to function scope to be visible at the end of the function; (2) `events.ndjson` is **not buffered at all** — it is streamed through `schema.NewEventReader(rc)` at `:786`, so there is **no `rawEvents`** slice; to populate `events_ref` the events `rc` must be wrapped in an `io.TeeReader(rc, &eventsBuf)` (a function-scope `bytes.Buffer`) so the loop's reads fill the buffer; (3) there is **no `resolvedSidecarImages`** variable — `resolveSidecarImages` mutates the slice in place and the resolved tags live in `containerSpec.Sidecars[i].Image`, so the sidecar image list must be built from there and threaded into `ingestFlightRecorder` as a new parameter.

**Files:**
- Modify: `atc/exec/agent_step.go` (new `WithAgentFixtureCapturer` option + `fixtureCapturer *fixtures.Capturer` field; capture call inside `ingestFlightRecorder`).
- Test: `atc/exec/agent_step_test.go` (extend the ingestion suite).

**Steps:**

- [ ] Write a failing Ginkgo spec in `atc/exec/agent_step_test.go`: with a `fixtures.MemoryStore`-backed capturer wired via `exec.WithAgentFixtureCapturer(...)`, running an agent step whose flight `results.json`/`events.ndjson` fakes return known bytes inserts **exactly one** fixture row with `source=production`, `step_kind` from the plan, `build_id`/`plan_id`/`ticket_id` matching the metrics row, `output_ref.results_json_ref` round-tripping the results bytes, `output_ref.events_ref` round-tripping the tee-captured events bytes, and — because v1 pins no full bundle — `replayable=false` with `skip_reason='input-bundle-partial'`. A second spec: a capturer whose `BlobStore` errors does **not** change the step result (still succeeds/`Finished`) — capture is non-fatal. A third: **no** capturer wired (nil) leaves behavior identical to today (no panic, metrics row still written, **and NO events buffering occurs — the tee is gated on the capturer, so a nil-capturer run allocates no `eventsBuf` copy** (2026-07-19 post-review hardening)). A fourth (`agentStepKind`): a `planEnv` whose `AGENT_WORKFLOW_NAME` contains `review` → `step_kind='review'`; otherwise `'implement'`.

- [ ] Add to `agent_step.go`:
  - import `"github.com/concourse/concourse/agent/api/fixtures"`, `"bytes"`, `"io"` (io if not already imported).
  - option + field (next to `WithAgentMetricsStore`/`metricsStore`, `agent_step.go:79/120`):
    ```go
    func WithAgentFixtureCapturer(c *fixtures.Capturer) AgentStepOption {
        return func(s *AgentStep) { s.fixtureCapturer = c }
    }
    ```
  - **Sub-step (a) — hoist results to function scope.** At the top of `ingestFlightRecorder`, declare `var rawResults []byte` and `var resultsMeta map[string]interface{}`. Inside the `flightArtifact != nil` block, after the successful `results.json` read/unmarshal at `:762`/`:765`, assign `rawResults = raw` and `resultsMeta = results.Metadata` (so the end-of-function capture call can read them; today `raw`/`results` die at the block's close).
  - **Sub-step (b) — tee the events stream, GATED on the capturer (2026-07-19 post-review — capture-is-free hardening).** Declare `var eventsBuf bytes.Buffer` at function scope. At the events read (`:786`), tee ONLY when capture is wired: `if step.fixtureCapturer != nil { reader = schema.NewEventReader(io.TeeReader(rc, &eventsBuf)) } else { reader = schema.NewEventReader(rc) }` — a capture-off deploy buffers nothing. The existing loop already drains `rc` to EOF, so with capture on `eventsBuf` ends holding the full raw `events.ndjson` (best-effort: on a torn read it holds what was consumed). This is the one extra in-memory copy the CONSUMES note calls out — there is no pre-existing buffer to reuse; no separate size cap is needed since the buffer is bounded by the existing events stream size.
  - **Sub-step (c) — thread the resolved sidecar images.** In `run()`, after `resolveSidecarImages` + `containerSpec.Sidecars = sidecars`, build `sidecarImages := make([]string, 0, len(containerSpec.Sidecars))` from each `containerSpec.Sidecars[i].Image`, and add a `sidecarImages []string` parameter to `ingestFlightRecorder` (the call site at `:661` has `containerSpec` in scope — pass it there).
  - **Sub-step (d) — the `agentStepKind` helper** (new, package-`exec`): `func agentStepKind(planEnv map[string]string) string { if strings.Contains(strings.ToLower(planEnv["AGENT_WORKFLOW_NAME"]), "review") { return "review" }; return "implement" }`. Concrete signal = `AGENT_WORKFLOW_NAME` (set on `rm.WorkflowName` at `:727`). Covered by the fourth unit spec above. (Provisional — Open decision 1: if the workflow grammar later carries an explicit per-step kind, thread it and drop the helper.)
  - **Sub-step (e) — the guarded, non-fatal capture call at the end of the function:**
    ```go
    if step.fixtureCapturer != nil {
        _, capErr := step.fixtureCapturer.Capture(fixtures.CaptureInput{
            Source:      fixtures.SourceProduction,
            StepKind:    agentStepKind(planEnv),
            Repo:        planEnv["AGENT_REPO"], // may be "" for pure-CI (a missing base_sha, not repo, is what flags input-pin-unresolved)
            TicketID:    rm.TicketID,
            BuildID:     &rm.BuildID,
            PlanID:      rm.PlanID,
            BaseSHA:     metaString(resultsMeta, "base_sha"), // metaString is package-exec (harvest_step.go:513)
            ResultsJSON: rawResults,        // hoisted in sub-step (a)
            EventsND:    eventsBuf.Bytes(), // tee-captured in sub-step (b)
            ResultSHA:   metaString(resultsMeta, "head_sha"),
            Env:         fixtures.EnvPins{RunnerImage: step.agentImage, Sidecars: sidecarImages},
            Parked:      rm.Status == schema.RunStatusParked,
            // InputBundleComplete deliberately left false: v1 pins only base_sha+env
            // (no snapshot/config/overlay) → replayable=false, skip_reason='input-bundle-partial'.
            // This is the honest early-capture posture; A2 sets it true when it pins the full bundle.
        })
        if capErr != nil {
            logger.Error("failed-to-capture-fixture", capErr) // non-fatal (principle 2)
        }
    }
    ```

- [ ] Run `ginkgo ./atc/exec/ --focus="fixture"` — expect pass. Run `go build ./atc/...`.
- [ ] Commit: `git add atc/exec/agent_step.go atc/exec/agent_step_test.go && git commit -m "feat(bench): agent-step fixture capture hook (fire-and-forget, non-fatal)"`

---

### Task 8: Capture hook option on `HarvestStep` + wire into `ingestAndRecord`

Same shape for the harvest step, which additionally has `step.plan.Repo` (verified `:246`), the `base_sha`/`head_sha` from `results.Metadata` (verified `harvest_step.go:494`), and a third output artifact `review.json` (verified `:697`). `step_kind` here is `"workflow"` for the terminal harvest of a full ticket run. Two scoping notes (verified): `parsedResults` (`:552`) and `reviewRaw` (`:606`) are already **function-scoped** (no hoist needed), but `events.ndjson` is streamed through `schema.NewEventReader(rc)` at `:640` and **not** buffered — tee it exactly as on the agent path if `events_ref` is wanted. The harvest runner image is **`step.agentImage`** (`:127`) — there is **no `harvestImage` field**.

**Files:**
- Modify: `atc/exec/harvest_step.go` (`WithHarvestFixtureCapturer` option + field; capture call inside `ingestAndRecord`).
- Test: `atc/exec/harvest_step_test.go`.

**Steps:**

- [ ] Write a failing Ginkgo spec in `atc/exec/harvest_step_test.go`: a `HarvestStep` with a `fixtures.MemoryStore` capturer inserts one fixture with `source=production`, `step_kind=workflow`, `repo=step.plan.Repo`, `base_sha` from results metadata, `output_ref.review_json_ref` round-tripping the review.json bytes, and `replayable=false`/`skip_reason='input-bundle-partial'` (v1 partial bundle). Assert non-fatal (a failing blob store does not change the step result) and nil-capturer parity.

- [ ] Add to `harvest_step.go` (mirror `seedOutcome`'s guarded, non-fatal, metadata-reading shape at `:481`):
  - import `agent/api/fixtures` (+ `bytes`/`io` for the events tee); option `WithHarvestFixtureCapturer(*fixtures.Capturer)` + `fixtureCapturer` field next to `metricsStore` (`harvest_step.go:134`).
  - Tee the events stream into a function-scope `bytes.Buffer` at the `:640` read, **gated on the capturer exactly as in Task 7 sub-step (b)** (`if step.fixtureCapturer != nil { … io.TeeReader(rc, &eventsBuf) … } else { … rc … }` — 2026-07-19 post-review hardening; a nil-capturer spec asserts no buffering here too).
  - Inside `ingestAndRecord`, after the review.json read (`:697`) and metrics upsert, capture with the buffered `results.json` (`raw`/`parsedResults`) and `review.json` (`reviewRaw`) slices + the tee-captured events, `Repo: step.plan.Repo`, `BaseSHA: metaString(res.Metadata,"base_sha")`, `ResultSHA: metaString(res.Metadata,"head_sha")`, `Env: fixtures.EnvPins{RunnerImage: step.agentImage, Sidecars: <resolved dev-mcp image tags from containerSpec.Sidecars>}`, `StepKind: "workflow"`, join keys from `rm`. Leave `InputBundleComplete` false (v1 partial → `input-bundle-partial`). Log-only on error.

- [ ] Run `ginkgo ./atc/exec/ --focus="harvest.*fixture"` — expect pass. `go build ./atc/...`.
- [ ] Commit: `git add atc/exec/harvest_step.go atc/exec/harvest_step_test.go && git commit -m "feat(bench): harvest-step fixture capture hook (review.json + repo pin)"`

---

### Task 9: `atc/db` `AgentStepFixturesFactory` implementing `fixtures.Store` + `fixtures.SyntheticFixtureStore`

The DB backing (squirrel `psql`, JSONB marshal, epoch scan) following the `agent_run_metrics_factory.go` / `agent_outcomes_factory.go` recipe. `Insert` is a plain INSERT (one row per execution — **no** ON CONFLICT, per Q1). This is what capture and the reaper use in production. **(Amended 2026-07-19 post-review:** the factory also implements `fixtures.SyntheticFixtureStore` natively — B's registration route calls `RegisterSynthetic` here — and the constructor gains a `BlobStore` for `Bundle` hydration.**)**

**Files:**
- Create: `atc/db/agent_step_fixtures_factory.go`
- Create: `atc/db/dbfakes/fake_agent_step_fixtures_factory.go` (generated)
- Test: `atc/db/agent_step_fixtures_factory_test.go`

**Steps:**

- [ ] Write the failing Ginkgo spec `atc/db/agent_step_fixtures_factory_test.go` (matching `agent_run_metrics_factory_test.go`; `DELETE FROM agent_step_fixtures` in `BeforeEach`): `Insert` returns a positive id and `Get` round-trips every field incl. `InputRef`/`OutputRef`/`GroundTruth`/`Tags`/`ExpiresAt` (pointer nil-vs-set distinction preserved); two `Insert`s of a byte-identical bundle produce **two** rows with the same `content_hash` (per-execution, non-unique — Q1); `ListReplayable("implement","open")` returns only replayable open implement rows newest-first and excludes `replayable=false` and `holdout` rows; `List(FixtureFilter{StepKind, Split, Tags, IDs, Limit})` filters accordingly newest-first; `ListExpired(now)` returns only rows with a non-NULL `expires_at < now`; `Pin(id, true)` sets `pinned=true` and NULLs `expires_at` (and a subsequent `ListExpired` excludes it), `Pin(id, false)` clears the flag; `Tag(id, add, remove)` round-trips through the `TEXT[]` column; `Bundle(id)` returns `{Repo, BaseSHA, Overlay, TicketSnapshot, Config, Env}` with refs hydrated via the blob store; `RegisterSynthetic(f)` inserts `source='synthetic', pinned=true, split=AssignSplit(f.ContentHash)` with the `InjectedFault` manifest carried in `ground_truth` and NULL `expires_at`; `Delete(id)` removes it; `Get` of a missing id returns `found=false, nil`.

- [ ] Write `atc/db/agent_step_fixtures_factory.go`:
  - `//counterfeiter:generate . AgentStepFixturesFactory`; `type AgentStepFixturesFactory interface { fixtures.Store; fixtures.SyntheticFixtureStore }`; `NewAgentStepFixturesFactory(conn DbConn, blobs fixtures.BlobStore)` (the blob store hydrates `Bundle` — amended 2026-07-19 post-review).
  - `Insert`: marshal `InputRef`/`OutputRef`/`GroundTruth` to JSONB (`json.Marshal` → `any`, nil for absent `OutputRef`/`GroundTruth`), insert with `RETURNING id`. **`Tags` (`TEXT[]`) follows the house precedent `agent_principals.scopes` — NOT `pq.Array`** (no atc/db factory imports `github.com/lib/pq`; `lib/pq` is used only by `atc/db/listener.go`). Write the `[]string` **directly as a query arg** (the pgx-backed driver encodes `TEXT[]`) exactly like `agent_principals_factory.go:60` writes `spec.Scopes`, and scan it back via `pgtype.NewMap().SQLScanner(&f.Tags)` exactly like `agent_principals_factory.go:152`. Convert `*int` ticket/build to `sql.NullInt64`; `*int64` `ExpiresAt` to `to_timestamp($n)` or NULL.
  - `Get`/`ListReplayable`/`ListExpired`: `SELECT` with `EXTRACT(EPOCH FROM created_at)::bigint` and `EXTRACT(EPOCH FROM expires_at)::bigint` (scanned into `sql.NullInt64` → `*int64`), JSONB columns unmarshaled back into the structs. `ListReplayable` = `WHERE replayable AND step_kind=$1 AND split=$2 ORDER BY id DESC`; `ListExpired` = `WHERE expires_at IS NOT NULL AND expires_at < to_timestamp($1) ORDER BY id ASC`.
  - `Pin(id, pinned)`: `UPDATE … SET pinned=$2, expires_at = CASE WHEN $2 THEN NULL ELSE expires_at END WHERE id=$1`; zero rows → `fixtures.ErrFixtureNotFound` (amended 2026-07-19 post-review — two-arg form).
  - `List(fil)`: composed `WHERE` from the non-zero `FixtureFilter` fields (`step_kind`, `split`, tag containment on the `TEXT[]`, `id = ANY($n)`, `LIMIT`), `ORDER BY id DESC`.
  - `Tag(id, add, remove)`: read-modify-write the `TEXT[]` (add then remove, dedup'd) in one UPDATE; zero rows → `fixtures.ErrFixtureNotFound`.
  - `Bundle(id)`: load the row, then hydrate `input_ref`'s `overlay_ref`/`ticket_snapshot_ref`/`config_ref` via `blobs.Get` (empty ref → nil bytes) into `fixtures.Bundle{Repo, BaseSHA, Overlay, TicketSnapshot, Config, Env}`.
  - `RegisterSynthetic(f)`: build a `Fixture{Source: SourceSynthetic, StepKind: f.StepKind, Repo: f.Repo, ContentHash: f.ContentHash, Split: AssignSplit(f.ContentHash), Pinned: true, Tags: f.Tags, InputRef: {Repo, BaseSHA, OverlayRef}}`, marshal the `[]InjectedFault` manifest into the `ground_truth` JSONB, and `Insert` (NULL `expires_at` — synthetic persists). (Added 2026-07-19 post-review — DEC-5; B's route is the caller.)
  - `Delete`: `DELETE … WHERE id=$1`.
  - Reuse a `scannable` interface if the package already declares one (the compiler flags a duplicate — drop the local decl and reuse).

- [ ] Run to verify failure then pass: `ginkgo --focus="AgentStepFixturesFactory" ./atc/db/` (fails: `undefined: db.NewAgentStepFixturesFactory`), implement, then generate the fake `cd atc/db && go run github.com/maxbrunsfeld/counterfeiter/v6 -o dbfakes/fake_agent_step_fixtures_factory.go . AgentStepFixturesFactory && cd ../..`, then `ginkgo --focus="AgentStepFixturesFactory" ./atc/db/ && go build ./atc/db/...` — green.
- [ ] Commit: `git add atc/db && git commit -m "feat(bench): AgentStepFixturesFactory + fake (per-execution insert, JSONB refs)"`

---

### Task 10: `agent/api/fixtures` retention reaper

A `Reaper` that each tick lists expired rows, deletes their blobs (`input_ref`/`output_ref` blob refs), then deletes the rows — deleting only `expires_at IS NOT NULL AND expires_at < now` (i.e. unpinned production open past 30d). Pinned/holdout/synthetic/benchmark rows (NULL `expires_at`) are never touched. Implements `component.Runnable`. Non-fatal per row.

**Files:**
- Create: `agent/api/fixtures/reaper.go`
- Test: `agent/api/fixtures/reaper_test.go`

**Steps:**

- [ ] Write the failing spec `agent/api/fixtures/reaper_test.go` (`MemoryStore` + `DirBlobStore`): seed a mix — an expired production/open/unpinned row (with blob refs), a pinned row (NULL expiry), a holdout row (NULL expiry), a synthetic row (NULL expiry), and a future-expiry production row. After `Reaper.Run(ctx)`: only the expired row is gone (`Get` → not found) and its blob files are removed from disk; the other four remain and their blobs are intact. A blob-delete error on one row does not stop the reaper from reaping the rest (non-fatal), and a row whose blob was already missing still gets its DB row deleted.

- [ ] Run `go test ./agent/api/fixtures/` — **expect FAIL** (`undefined: NewReaper`/`Reaper`). Observe RED before implementing (house TDD rule).

- [ ] Write `agent/api/fixtures/reaper.go`:

```go
package fixtures

import (
	"context"

	"code.cloudfoundry.org/lager/v3"
)

// Reaper deletes expired unpinned production-open fixtures and their blobs on
// a polling interval (never notify-only — house rule). It is a RunnableComponent
// wired in atccmd only when --agent-fixture-store-dir is set.
type Reaper struct {
	logger lager.Logger
	store  Store
	blobs  BlobStore
}

func NewReaper(logger lager.Logger, store Store, blobs BlobStore) *Reaper {
	return &Reaper{logger: logger, store: store, blobs: blobs}
}

// Run implements component.Runnable.
func (r *Reaper) Run(ctx context.Context) error {
	expired, err := r.store.ListExpired(nowEpoch())
	if err != nil {
		return err
	}
	for _, f := range expired {
		for _, ref := range blobRefs(f) {
			if err := r.blobs.Delete(ref); err != nil {
				// non-fatal: a leaked blob is far better than a wedged reaper.
				r.logger.Error("failed-to-delete-fixture-blob", err, lager.Data{"fixture-id": f.ID, "ref": ref})
			}
		}
		if err := r.store.Delete(f.ID); err != nil {
			r.logger.Error("failed-to-delete-fixture-row", err, lager.Data{"fixture-id": f.ID})
		}
	}
	if n := len(expired); n > 0 {
		r.logger.Info("reaped-expired-fixtures", lager.Data{"count": n})
	}
	return nil
}

// blobRefs collects the blob:// refs a fixture owns (inline refs need no delete).
func blobRefs(f Fixture) []string {
	refs := []string{f.InputRef.OverlayRef, f.InputRef.TicketSnapshotRef, f.InputRef.ConfigRef}
	if f.OutputRef != nil {
		refs = append(refs, f.OutputRef.ResultsJSONRef, f.OutputRef.EventsRef, f.OutputRef.ReviewJSONRef)
	}
	return refs
}
```

- [ ] Run `go test ./agent/api/fixtures/` — expect pass.
- [ ] Commit: `git add agent/api/fixtures/reaper.go agent/api/fixtures/reaper_test.go && git commit -m "feat(bench): fixture retention reaper (deletes only expired unpinned rows + blobs)"`

---

### Task 11: atccmd wiring — master-switch flags, store construction, engine options, reaper component

Wire the whole slice off-by-default. The master switch is `--agent-fixture-store-dir`: empty → no capturer is passed to the engine and no reaper is registered (identical to today). Follows the `--agent-outcome-git-dir` master-switch pattern (plan 12) and the `NewRunSecretReaper` component-registration template (`atc/atccmd/command.go:1395`).

**Files:**
- Create: `atc/component.go` addition — `ComponentAgentFixtureReaper = "agent_fixture_reaper"`.
- Modify: **`atc/engine/step_factory.go`** (NOT `atc/exec/engine.go` — that file does not exist; the engine-layer options live here). Add an `agentFixtureCapturer *fixtures.Capturer` field + a `WithAgentFixtureCapturer(c *fixtures.Capturer) CoreStepFactoryOption` **exactly mirroring `WithAgentMetricsStore`** (`step_factory.go:73`, which is one engine option that feeds BOTH steps). In the step construction, when the field is non-nil, append `exec.WithAgentFixtureCapturer(f.agentFixtureCapturer)` to `agentOpts` (near `:272`, beside `exec.WithAgentMetricsStore`) and `exec.WithHarvestFixtureCapturer(f.agentFixtureCapturer)` to `harvestOpts` (near `:328`, beside `exec.WithHarvestMetricsStore`). (The two-layer `engine.With*` → `exec.With*` option pattern, with the same base name in both packages, is precisely what `WithAgentMetricsStore` already does.)
- Modify: `atc/atccmd/command.go` — flags, store construction, the single engine option, reaper registration.

**Steps:**

- [ ] Add the flags to the ATC command's Kubernetes/agent flag group (near `--agent-run-timeout`, `command.go:255`; the outcome-watcher group is the layout model). **Four flags — no `--agent-fixture-overlay-max-bytes`** (that knob ships with A2's overlay materialization; a flag with no producer is exactly the dead operator control the basic-experience guardrail forbids):
  ```
  --agent-fixture-store-dir            (default "")     master switch: host-path fixture blob store; empty disables capture + reaper
  --agent-fixture-retention            (default 720h)   unpinned production-open fixture TTL
  --agent-fixture-inline-max-bytes     (default 524288)    512 KiB inline-JSON bound (else blob)
  --agent-fixture-reaper-interval      (default 1h)     reaper polling interval
  ```
- [ ] Write a failing Ginkgo spec in `atc/atccmd` (or the existing command wiring test) — or, if that suite is thin, an assertion in the engine-options test — that: with `--agent-fixture-store-dir` **empty**, `constructComponents` does **not** include `ComponentAgentFixtureReaper` and the engine receives a **nil** fixture capturer; with it **set** (to a `t.TempDir()`), the reaper component is present and a capturer is wired. (Keep this light — the real coverage is the package-level tests; this guards the master switch.)
- [ ] Implement in `command.go`:
  - When `cmd.AgentFixtureStoreDir != ""`: `blobs, err := fixtures.NewDirBlobStore(dir)` — **the error is HANDLED (2026-07-19 post-review hardening): on error, log loudly (`agent-fixture-store-unavailable`) and leave the ENTIRE slice unwired (no capturer passed to the engine, no reaper registered)** — a store-dir config error degrades to visible no-capture; it never fails web startup and never touches production steps. On success: `store := db.NewAgentStepFixturesFactory(dbConn, blobs)`; `capturer := fixtures.NewCapturer(store, blobs, fixtures.Config{InlineBound: cmd.AgentFixtureInlineMaxBytes, RetentionSecs: int64(cmd.AgentFixtureRetention.Seconds())})` (no `OverlayMax` — not in A1's `Config`; A2 Task 6a adds it).
  - Pass the **single** engine option `engine.WithAgentFixtureCapturer(capturer)` alongside `engine.WithAgentMetricsStore(...)` at `command.go:2214` (it feeds both agent + harvest steps, exactly like `WithAgentMetricsStore`). When the dir is empty, pass nothing (steps get a nil capturer → hooks skip).
  - Register the reaper in the `components` slice (guard on the dir being set, exactly like the k8s-only reapers guard on the k8s block):
    ```go
    components = append(components, RunnableComponent{
        Component: atc.Component{Name: atc.ComponentAgentFixtureReaper},
        Runnable:  fixtures.NewReaper(logger.Session(atc.ComponentAgentFixtureReaper), store, blobs),
        Interval:  cmd.AgentFixtureReaperInterval,
    })
    ```
- [ ] Verify: `go build ./...` and `ginkgo ./atc/atccmd/ ./atc/exec/ ./atc/engine/` green.
- [ ] Commit: `git add atc/component.go atc/engine atc/exec atc/atccmd && git commit -m "feat(bench): wire fixture capture + reaper behind --agent-fixture-store-dir master switch"`

---

### Task 12: Source taxonomy + non-replayable + retention-exemption matrix (cross-cutting DB proof)

One focused DB-backed spec that pins the **whole** contract matrix end-to-end through the real factory + reaper, so a future refactor can't quietly break a corner (this is the contract A2/B/C/D depend on).

**Files:**
- Test: `atc/db/agent_step_fixtures_matrix_test.go`

**Steps:**

- [ ] Write a Ginkgo spec (real `dbConn` + a `DirBlobStore` on `GinkgoT().TempDir()` + `fixtures.NewReaper`) asserting, in one table-driven pass, that a `Reaper.Run` deletes **exactly** the `source='production' AND pinned=false AND split='open' AND expires_at<now` rows and **retains**: every `pinned` row, every `holdout` row, every `source IN ('synthetic','benchmark')` row, every future-expiry production row, and every `replayable=false` production-open row **that has not yet expired** (non-replayable is orthogonal to retention — it is filtered from default sets, not reaped early). Also assert `ListReplayable` never returns `holdout` or `replayable=false` rows, and that a `synthetic` row round-trips its `ground_truth` manifest. This is the executable form of the §1.15 decisions.
- [ ] Run `ginkgo --focus="fixtures matrix" ./atc/db/` — green.
- [ ] Commit: `git add atc/db/agent_step_fixtures_matrix_test.go && git commit -m "test(bench): fixture source/retention/non-replayable matrix proof"`

---

### Task 13: Round-trip echo canary self-test (capture → stored bytes byte-stable)

The spec's named canary suite (spec "Testing approach → Round-trip self-test"): a deterministic echo-step fixture whose captured output bytes reproduce byte-stable. For A1 (capture-only; replay is A2) the canary is: given known `results.json`/`events.ndjson` bytes, `Capturer.Capture` → `Store.Get` → `BlobStore.Get(output_ref.results_json_ref)` returns the **identical** bytes, and the `content_hash` is stable across repeated captures of the same bundle. This is the regression fence the daily capture stream rides on.

**Files:**
- Test: `agent/api/fixtures/roundtrip_test.go`

**Steps:**

- [ ] Write `agent/api/fixtures/roundtrip_test.go`: build a fixed `CaptureInput` (small deterministic bytes, fixed `base_sha`, fixed env pins); capture twice; assert both rows carry the **same** `content_hash` and `split`; assert `BlobStore.Get(row.OutputRef.ResultsJSONRef)` equals the input bytes exactly; assert a one-byte change to any input pin changes the `content_hash` (and can flip the split). Keep it hermetic (no DB — `MemoryStore` + `DirBlobStore` on `t.TempDir()`), so it runs under `make test-quick` as the bench's own smoke.
- [ ] Note: unlike Tasks 4/5/6/10 this canary runs **after** the code exists, so there is no undefined-symbol RED. Guard against a vacuous test instead: confirm the negative assertion actually bites — temporarily corrupt one output byte (or perturb one pin) and observe the round-trip/`content_hash` assertion **fail**, then revert. Only then is the green meaningful.
- [ ] Run `go test ./agent/api/fixtures/` — green.
- [ ] Commit: `git add agent/api/fixtures/roundtrip_test.go && git commit -m "test(bench): fixture capture round-trip canary (byte-stable, stable hash)"`

---

### Task 14: Live theborg capture + blob-persistence verification (throwaway namespace)

The mandatory live proof (spec "Testing approach"; MEMORY.md live-test rules). The property A2 depends on: a real agent/harvest step under a live web node with `--agent-fixture-store-dir` set writes a fixture **row** and a **blob**, and the blob **survives** a web restart (the host-path persistence guarantee). Replay itself is A2 — this test stops at "the fixture and its blob are durably present and queryable."

**Files:**
- Create: `atc/worker/jetbridge/live_fixture_capture_test.go` (plain Go, `//go:build live`, matching the existing `live_*_test.go` gating in MEMORY.md).

**Steps:**

- [ ] Write a `//go:build live` test that (a) confirms the fixture store dir is a host path with disk headroom, (b) after a known agent step runs against a **throwaway namespace** (never `cicd`/`concourse`), queries `agent_step_fixtures` for the step's `(build_id, plan_id)` and asserts one row with a non-empty `output_ref.results_json_ref`, (c) reads that ref out of the store dir on the node and confirms the bytes are present, and (d) after a web-pod restart (or by re-reading the persisted host-path file directly) confirms the blob is still there. Gate credentials/host per the `kubeClient(t)` + `K8S_TEST_NAMESPACE` convention; `t.Cleanup` deletes the throwaway namespace.
- [ ] Document the run command in the Execution notes (below). Do **not** run it in `make test-quick`.
- [ ] Commit: `git add atc/worker/jetbridge/live_fixture_capture_test.go && git commit -m "test(bench): live theborg fixture-capture + blob-persistence probe (throwaway ns)"`

---

### Task 15: Verification sweep + reconciliation vs plan 14 M1

Close-out: prove the slice is green end-to-end and record the plan-14 supersession explicitly (the handoff brief requires "Verify vs plan 14 M1 and mark superseded pieces explicitly").

**Files:** none (verification + a note appended to this plan's Execution notes / the §11 log written in Task 1).

**Steps:**

- [ ] Run the full A1 suite (Execution notes below) and confirm green: `agent/api/fixtures`, `atc/db` (fixtures factory + matrix + migration walk), `atc/exec` (both hooks), `atc/atccmd` (master-switch wiring), `go build ./...`.
- [ ] Confirm **no label table and no label-sync** was introduced (grep the diff: the only new table is `agent_step_fixtures`; no `verdict`/`score`/`merge_state`/`human_*` column exists on it — principle 3).
- [ ] Reconcile plan 14 M1: confirm `agent_benchmark_cases` is **not** created by this plan and that its role (imported end-to-end cases) is served by `source='benchmark'` fixtures; the §11 amendment (Task 1) already records the supersession. Confirm the migration number `1773106100` matches plan 14's now-reclaimed slot and that A2/B will take `101`/`102`.
- [ ] Confirm the master switch: with `--agent-fixture-store-dir` unset, a full `make test-quick` shows zero behavioral change to agent/harvest steps (capturer nil, reaper absent).

---

## Execution notes

**Running this workstream's suite (all green before close-out):**

```bash
pg_isready                                                        # PostgreSQL for atc/db + migration specs
go test ./agent/api/fixtures/                                          # types, hash/split, blob store, capturer, reaper, round-trip (plain testing)
ginkgo --focus="AgentStepFixturesFactory" ./atc/db/              # DB factory
ginkgo --focus="fixtures matrix" ./atc/db/                       # source/retention/non-replayable matrix
ginkgo --focus='Legacy Database Upgrade' ./atc/db/migration/     # migration 1773106100 walk
ginkgo ./atc/exec/ --focus="fixture"                             # both capture hooks (agent + harvest)
ginkgo ./atc/atccmd/                                             # master-switch wiring guard
go build ./...                                                   # nothing else breaks from the option/atccmd changes
```

Per CLAUDE.md: unit tests run in parallel with `-p`; **never** pass `--race` (parallel-compilation failures). Do not lower the `atc/db` template-DB timeouts. `agent/api/fixtures` is hermetic (stdlib + `t.TempDir()`, no DB, no network) and safe under `make test-quick`.

**Live-test requirements (theborg pattern, MEMORY.md):**
- Task 14 is a `//go:build live` test: `KUBECONFIG=~/.kube/config K8S_TEST_NAMESPACE=<throwaway-ns> go test -tags live -run '^TestLiveFixtureCapture$' -v -count=1 -timeout 10m ./atc/worker/jetbridge/`. Create a THROWAWAY namespace (never `cicd`/`concourse`); `t.Cleanup` deletes it. It needs a live web node deployed with `--agent-fixture-store-dir=/var/lib/concourse/fixtures` (the master switch) on a node with disk headroom.
- The fake clientset cannot exercise host-path blob persistence across a restart — this genuinely needs the live cluster (house lesson: SC-11 sidecar-transport precedent).

**Sequencing & merge order (house spine rule — one migration per push):**
- A1 lands **first and alone** (spec Sequencing). Its migration `1773106100` must merge before A2's `1773106101` (which FKs `agent_step_fixtures`) and B's `1773106102`. Never push two of `100/101/102` in one push; ascending applied order == referential-dependency order.
- On-disk head at planning was `1773106091` (`create_agent_settings`); the `legacy_upgrade_test.go` const read `1773106090` (a pre-existing one-behind lag) — Task 2 bumps it to `1773106100` (only-if-higher), and the migrator applies `1773106091` and `1773106100` on the walk.

**Rollback notes:**
- **Migration 1773106100** is additive (one table + five indexes); `DROP TABLE agent_step_fixtures` fully reverses it. No existing table is touched.
- **Master switch off = no-op:** shipping the code without `--agent-fixture-store-dir` wires no capturer and no reaper — the safe first-deploy default. Turn it on per-node once the store dir has disk headroom.
- **Capture is non-fatal by construction:** every hook call is guarded (`if capturer == nil`) and log-only on error, under the F4 detached context — a capture bug degrades to "no fixture row", never a failed or slowed production step (principle 2). This ordering (capture strictly after the metrics upsert, never before) is load-bearing: capture must never precede or block the authoritative ingestion.

**Plan-14 reconciliation (handoff-brief requirement):** `agent_benchmark_cases` (plan 14 M1, `1773106100_create_agent_benchmark_cases`) is **superseded and not built** — its purpose (imported end-to-end cases the runner enumerates without cloning) is served by `source='benchmark'` fixtures through this plan's `Store`. Plan 14's `agent_experiments`/`agent_experiment_runs` are A2's `agent_bench_experiments`/`agent_bench_cells`; plan 14's retained `agent_reviews.defect_link` keeps its own plan-14-owned migration (not a bench allocation). This plan touches none of those.

---

## Scope-out (explicitly NOT in A1)

- **Replay** (one-step template pipeline render, stub-ATC sidecar, restore/evaluate steps, write-absorption) — **A2**. A1 stops at capture + registry + store; `output_ref` is a free recorded baseline, not a replay.
- **Experiments, cells, scores** (`agent_bench_experiments`/`agent_bench_cells`/`agent_bench_scores`, the runner component, budget envelope) — **A2/B** (migrations `1773106101`/`1773106102`).
- **Fixture API routes + fly verbs** (`/api/v1/agent/bench/fixtures` list/tag/pin, `fly agent bench fixtures`) — **A2's harness** (six-touchpoint pattern). A1 is backend-only; `Store.Pin` exists for A2's admission to call in-process, but exposes no HTTP surface here.
- **Label tables / label-sync** — never built (principle 3). Six-verdict/judge/outcome labels are query-time joins, pinned by **B's** join task (skeleton Q4: `agent_feedback` needs a per-finding key — B's problem, not A1's).
- **The fault-injection corpus builder** and `ground_truth` *production* — **B**. A1 provides the `source='synthetic'` + `ground_truth JSONB` **contract, the synthetic types (`InjectedFault`/`Loc`/`SyntheticFixture`/`SyntheticFixtureStore` — moved here from B, amended 2026-07-19 post-review), and the native `RegisterSynthetic` Store path**; it does not build the corpus workflow.
- **Benchmark-case import** — rides **B's** registration route, which accepts `source ∈ {synthetic, benchmark}` (2026-07-19 post-review); there is **no A1 importer**.
- **Full input-bundle overlay materialization** for agent steps (clone-at-sha + non-git delta tarball) **and all its machinery** — `BlobStore.PutOverlay` + `ErrOverCap`, the `--agent-fixture-overlay-max-bytes` flag, the capturer's overlay branch, and the `overlay-over-bound:<n>B` skip_reason — **all land in A2** with the overlay producer (its **Task 6a**, the accepted handoff of this bullet). A1 captures only the reliably-present pins (`base_sha` + output refs), leaves `input_ref.overlay_ref` empty, and marks every v1 production capture `replayable=false, skip_reason='input-bundle-partial'` (honestly excluded from A2's default sets until the full restore bundle is pinned). A1 freezes only the `overlay_ref` shape + the 32 MiB cap value (Task 1 contract) so A2 builds against a stable contract. Shipping the overlay code + operator flag in A1 — ahead of the only thing that feeds them — is exactly the over-build this capture-first slice avoids; this is the honest early-capture posture, not a gap.
- **Bench naming** — `agent_step_fixtures`, `--agent-fixture-*`, `agent/api/fixtures` are **provisional** pending the S-8 naming-spine review (spec open item 9); a rename is a coordinated pre-freeze amendment.

---

## Open decisions (surfaced, not silently resolved)

1. **Agent-step `step_kind` derivation.** The concrete v1 signal is **`AGENT_WORKFLOW_NAME`** (already surfaced on `rm.WorkflowName`, `agent_step.go:727`): `agentStepKind` returns `'review'` when the lower-cased name contains `review`, else `'implement'` (Task 7 sub-step (d), covered by a unit spec). This is a heuristic, not a contract: if a review workflow is named without the substring it mislabels as `'implement'`, and A2/B's `ListReplayable('review','open')` would miss it. The durable fix is an explicit per-step kind in the workflow grammar threaded into `CaptureInput.StepKind` (drop the helper then). (Harvest is unambiguously `'workflow'`.)
2. **Agent-step `repo`/`base_sha` source.** Harvest has `step.plan.Repo` + results-metadata shas (verified). Agent steps have no `Repo` on `AgentPlan`; v1 reads `planEnv["AGENT_REPO"]` (may be absent) and results-metadata `base_sha` (present only if the runner writes it), flagging `replayable=false` otherwise. If early capture shows too many pin-unresolved rows, the cheapest fix is the agent-runner emitting `base_sha`/`repo` into `results.json` metadata (a runner change, out of A1 scope) — the capture path already reads it.
3. **Reaper interval vs global cap.** Default `--agent-fixture-reaper-interval=1h`. If the store dir fills faster than hourly reaping under heavy capture, shorten it or add a size-pressure trigger (deferred — retention is time-based v1, matching the spec's 30d default).
4. **Inline-vs-blob threshold tuning.** 512 KiB inline bound (L-3 precedent) keeps `results.json`/`review.json` inline in the common case and spills only large event streams. If DB row bloat shows up, lower the inline bound — it is a pure `--agent-fixture-inline-max-bytes` knob with no schema change.
5. **`content_hash` bundle fields.** v1 hashes `{repo, base_sha, overlay/snapshot/config digests, runner_image, sorted sidecars}`. If A2 replay finds a pin that materially changes behavior but is absent from the hash (e.g. model), add it to `HashBundle` — but note this changes split assignment for new captures only (existing rows keep their frozen split, which is correct: the split column is authoritative, the function is only used at capture).
