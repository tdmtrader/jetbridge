# The Bench — Two-Tier Scorecards (amends 13-scorecards) Implementation Plan

> **SUPERSEDED — historical implementation record (not executable).** The approved [Agentic Workflows as Functions over Snapshots design](../../../specs/2026-07-21-agentic-workflows-as-functions-design.md) and [Agentic Functions program plan](../../2026-07-21-agentic-functions-program.md) are authoritative. This document preserves the abandoned ticket-centric roadmap only. **Explicit superseded block:** every section below this banner, including migration reservations at `1773106100+`, `step_kind`, ticket/build/plan keys, restore runner/stub, and `primaryMetric` references, is historical and must not be implemented. **Keep:** fixtures, repetitions, evaluators, controls, and scorecards. **Supersede:** `step_kind`, ticket/build/plan keys, restore runner/stub, and the primary-metric switch.

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

- **Descends-from:** `13-scorecards.md` (its production-traffic rollup becomes the **production tier**, RETAINED verbatim including the applied **F8** fix — live flag + content hash read authoritatively from `agent_workflow_definitions`; this plan does NOT restate 13's retained tasks, it references them and adds on top).
- **Authority:** `docs/superpowers/specs/2026-07-19-agent-bench-design.md §7` (two-tier scorecards) + the FROZEN cross-track contract skeleton (§4 migration table: **C claims no bench migration**).
- **Consumes (wave-mate bench tables, NOT landed):** A2's `agent_bench_experiments` + `agent_bench_cells` (migration `1773106101`), B's `agent_bench_scores` (migration `1773106102`).
- **Tasks:** 11
- **Blocking prerequisite:** **plan 13 (`13-scorecards.md`) MUST land first.** C imports `agent/api/scorecards` (its `Scorecard`/`VersionColumn`/`ParseVersionsCSV`/`Store`), reuses `db.NewScorecardStore`, and registers a route beside `GetAgentWorkflowScorecard` — none of which exist at HEAD (see Anchor caveat). C cannot compile until 13 is on the branch. This gate is independent of, and in addition to, the bench-block `100 → 101 → 102` ascending merge order.
- **Complexity:** M · **Risk:** Low–Medium (read-only; new aggregate SQL over wave-mate bench tables; one additive HTTP route; one additive fly verb; zero domain writes).
- **Migrations:** **NONE.** The fixture-tier rollup reads `agent_bench_scores`/`agent_bench_cells`/`agent_bench_experiments`; its aggregation path rides B's `agent_bench_scores_cell` and A2's `agent_bench_cells_experiment` indexes. A single *conditional* covering index on `agent_bench_cells (variant, variant_version)` — needed only if the auto experiment-resolution query shows in slow-query logs — would use plan 13's own reserved **`1773106110–19`** block (`1773106111`, since `1773106110` is plan 13's Task 10 index), NOT a bench number (`100/101/102`). Default: no migration (Task 11).

---

## Context

**Charter (spec §7, handoff brief C).** The original wave-5 improvement loop measured workflows **only end-to-end, on production traffic** (plan 13). At this team's volume that starves: scorecards accrue ~1 row/ticket while workflow versions churn in days, so a production A/B between versions never reaches signal before the versions are obsolete (spec §Problem). The bench adds an **inner loop**: recorded step executions become replayable fixtures, step-variants compete on fixed fixtures under versioned evaluators, and their scores land in `agent_bench_scores`. This plan makes scorecards **two-tier**:

1. **Production tier** — plan 13's rollup, unchanged: honest, slow, confirming. Keyed by `(workflow_name, workflow_version)`, aggregated over `agent_run_metrics`/`agent_cost_ledger`/`agent_reviews`/`agent_feedback`/`agent_outcomes`. Carries F8 (authoritative `live`/`content_hash` from `agent_workflow_definitions`).
2. **Fixture tier** — NEW: the same "one column per requested version" shape, computed over `agent_bench_scores` with **paired-on-fixture deltas** (the statistical reason the inner loop exists — a within-fixture paired comparison, not the confounded cross-ticket delta production suffers) and **negative-control status** (spec principle 5: control failure flags the whole experiment `evaluator-suspect`, annotated on the column, never silently reported).
3. **Promotion view** — NEW: both tiers side-by-side. A candidate with **no production traffic** still renders a real fixture-tier column (its identity anchored by the F8 `agent_workflow_definitions` read; its body filled from bench scores). `set-live` stays a human decision informed by both tiers (spec principle 7).

Scope-in → task mapping (every item maps):

| scope_in item | Tasks |
|---|---|
| Production tier = plan 13's rollup, retained incl. F8 | 1 (records the retention + supersede-by-entry), Task-8 handler (reuses `scorecards.Store.Scorecard`) |
| Fixture tier: same scorecard shape over `agent_bench_scores` | 2 (types), 3 (store: resolution + identity + cell counts), 4 (metric aggregate) |
| Paired-on-fixture deltas | 5 |
| Negative-control status + `evaluator-suspect` annotation | 6 |
| Promotion view side-by-side; candidate-with-no-prod-traffic renders a real fixture column | 3 (identity-only column proof), 7 (promotion bundling), 9 (route), 10 (fly render) |
| API-first, principal-authenticable read verbs (spec §9 supervisor-readiness) | 9 (route on the team-less viewer tier), 10 (fly verb) |
| Reads only; writes no domain tables; no migration | whole plan; Task 11 (index deferral) |

**Scope OUT (do not implement):** capture (A1), replay/experiment harness (A2), evaluators + fault-injection corpus + `agent_bench_scores` writes (B), plan 14 disposition (D), gateway (E). Auto-promotion of any kind (spec §Out-of-scope — scorecards inform, humans decide; `set-live` is the existing `PromoteAgentWorkflowVersion` write, untouched here). Bench **web UI** — the two-tier promotion Elm page rides the **S-track** (spec §6/§Out-of-scope: "API + fly first"); this plan delivers API + fly and hands the Elm surface to S5 (web-loop-closure). Implementor-variance (a declared later slice, spec §4).

**Prior plan this DESCENDS FROM and RETAINS (13-scorecards — assumed landed exactly as its tasks + §11 amendments define; do NOT re-implement):**

- **`agent/api/scorecards`** (plan 13 Task 2): `Scorecard{WorkflowName, Columns []VersionColumn}`, `VersionColumn` (fixed production fields incl. `Version`, `Live`, `ContentHash`, `TicketCount`, ok/failed/error split, cost/turns per ticket, gate pass, findings, judge mean, `VerdictDistribution`, dark-until-filled outcome pointers), `VerdictDistribution`, `ParseVersionsCSV(csv string) ([]int, error)` (sorted, de-duped, errors on empty/non-integer), and `Store` interface with the single method `Scorecard(workflowName string, versions []int) (*Scorecard, error)`. Plain-`testing` package. **This plan imports and extends this package additively** (a new `fixture_types.go`, no edit to `types.go`).
- **`atc/db.NewScorecardStore(dbConn)`** (plan 13 Tasks 3–5) implementing `scorecards.Store` via unexported `*scorecardStore` with `perVersion` → `definition` (F8), `metricsAndCost`, `evidence`, `outcomes`. **F8** (plan 13 §11, 2026-07-09): `definition(name, version, col)` runs FIRST in `perVersion`, `SELECT content_hash, live FROM agent_workflow_definitions WHERE name=$1 AND version=$2`, setting `col.Live`/`col.ContentHash` authoritatively; `metricsAndCost`'s `MAX(workflow_hash)` is fallback-only. **This plan RE-USES the exact same `definition` read** to anchor fixture-tier columns (Task 3) — that is what makes a candidate with zero production traffic render a real column.
- **Route `GetAgentWorkflowScorecard`** `GET /api/v1/agent/workflows/:workflow_name/scorecard?versions=3,4` (plan 13 Task 6, authorized-viewer on `CheckAgentAuthorizationHandler`) + the Elm production-tier page `Scorecards/Scorecards.elm` (plan 13 Tasks 7–9) + index migration `1773106110` (plan 13 Task 10, the `(workflow_version, day)` covering indexes; block `1773106110–19` allocated to scorecards in plan 13 Task 1). **All retained unchanged.** This plan adds a sibling `promotion` route, not a second scorecard route.

**Wave-mate contracts this CONSUMES (from the FROZEN skeleton; NOT landed — coordinate, tolerate absence):**

- **A2 `agent_bench_experiments`** (migration `1773106101`): `id`, `name`, `step_kind ∈ {review,implement,plan,workflow}`, `spec JSONB`, `budget_usd`, `status ∈ {pending,running,complete,error,evaluator-suspect}`, `control_status ∈ {pending,pass,fail,none}` (spec §5 negative-control verdict), `created_by`, `created_at`, `completed_at`.
- **A2 `agent_bench_cells`** (same migration `1773106101`): `id`, `experiment_id` (FK, indexed `agent_bench_cells_experiment`), `fixture_id` (NOT NULL, FK, indexed `agent_bench_cells_fixture`), `variant TEXT` (= workflow-definition **name**), `variant_version INTEGER` (frozen workflow version), `control_role ∈ {'',baseline-clone,degraded}` (auto negative controls, spec §5), `repetition`, `pipeline_run_id` (step cells), `ticket_id` (workflow cells), `status ∈ {pending,running,ok,failed,error,skipped-budget}`, `skip_reason`, `env JSONB` (pinned + resolved image tags — A2's env-skew record, 2026-07-19 post-review; C does not read it), `created_at`, `UNIQUE (experiment_id, fixture_id, variant, variant_version, control_role, repetition)` (the 6-column key — `control_role` IS in the key, per A2's §1.12.3 amendment; consumed-contract updated 2026-07-19 post-review). **No index on `(variant, variant_version)`** — Task 11 pins the resolution path around this.
- **B `agent_bench_scores`** (migration `1773106102`): `id`, `cell_id` (FK, indexed `agent_bench_scores_cell`), `evaluator_name`, `evaluator_version` (indexed `agent_bench_scores_evaluator`), `metrics JSONB` (score envelope §2 — `{name: number}`, **values are numbers only**), `verdicts JSONB`, `rationale_ref`, `status ∈ {ok,error}` (evaluator failure ≠ low score), `cost_usd`, `created_at`, `UNIQUE (cell_id, evaluator_name, evaluator_version)`.
- **Score envelope** (spec §2, FROZEN): `{metrics:{k:number}, verdicts?:[...], rationale_ref?, pins:{evaluator_name, evaluator_version, fixture_id, variant, variant_version, rep}}` (pins incl. `variant_version` — amended 2026-07-19 post-review). C's fixture tier aggregates `metrics[k]` **paired on `pins.fixture_id`** across variants, annotated with `control_status`. Neither consumer reads any label column off a fixture row (principle 3 — labels are joins). **Envelope/DDL note (not a C bug — C keys off the DB columns, not the JSON):** the frozen envelope example renders `pins.variant` as `name@version` (`review-prompts@v5`), but A2's `agent_bench_cells.variant` column is **name-only** with the integer in `variant_version`. C's SQL pairs on `c.fixture_id` and matches `c.variant = $name` / `c.variant_version = $version` (the bare-name column, correct against the A2 DDL) — it never parses `pins.variant`. Flagged back to the skeleton owners so §2's `pins.variant` example is reconciled with A2's name-only column before the envelope freezes; a sibling track that parses `pins.variant` as `name@version` would diverge from C's pairing. **(Resolved 2026-07-19 post-review:** B's envelope `pins` now carries `variant` — the bare name — plus `variant_version`; the `name@version` example is gone. C's SQL keys off the DB columns either way.**)**

**Migration-registry note (skeleton §4).** Real on-disk head = `1773106091` (`create_agent_settings`). Bench claims `1773106100–102` (A1/A2/B). **C claims none.** Plan 13's own reserved `1773106110–19` block already spent `1773106110` (Task 10); the only migration C could ever add is the conditional `1773106111` index in Task 11, and the default is not to. Merge order across the bench block is strictly ascending (`100 → 101 → 102`), one per push (skeleton §4); C rides after `102` because it reads B's table.

**Anchor caveat.** Line anchors below (`atc/routes.go`, `atc/wrappa/api_auth_wrappa.go`, `atc/api/handler.go`, `fly/commands/agent.go`, `atc/db/pipeline_run_factory.go`) were verified on branch `jetbridge`. Plan 13, A1/A2/B, and sibling S-track edits will have shifted every one — treat each anchor as "the location of the quoted code" and place additions adjacent to the named existing symbol (search for it), not at a literal line.

**Plan-13 dependency surface (does NOT exist at HEAD — the blocking prerequisite above).** Everything this plan "imports and extends," "re-uses," or "descends from" in plan 13 is UNVERIFIABLE at HEAD and only appears once plan 13 lands: the **`agent/api/scorecards`** package (its `Scorecard` / `VersionColumn` / `ParseVersionsCSV` / `Store` symbols and `scorecardsfakes`), **`db.NewScorecardStore`**, route **`GetAgentWorkflowScorecard`** (absent from `atc/routes.go` — verified: grep returns nothing), the **F8 `definition` read** this plan claims to reuse verbatim, plan 13's index migration **`1773106110`**, and the Elm page **`web/elm/.../Scorecards/Scorecards.elm`**. The bench wave-mate tables (`agent_bench_experiments`/`_cells`/`_scores`, migrations `1773106101`/`1773106102`) are likewise NOT on disk (HEAD head = `1773106091`). **One mitigating fact IS verified at HEAD:** `agent_workflow_definitions` (migration `1773106040`) and its `content_hash`/`live`/`name`/`version` columns are real, so the F8 identity read is implementable on top of a real table independent of plan 13's store code.

---

### Task 1: Superseding contract addendum — two-tier scorecard supersede-by-entry, fixture-tier read contract, promotion route

This is a **plan-prose** task (like plan 12/13 Task 1): it *describes* the `00-shared-contracts.md` §11 addendum this track lands **as its own contract commit at execution time**; it does not edit the contract file from within this plan doc, and it does NOT rewrite plan 13's retained tasks. It records (a) the supersede-by-entry that makes plan 13's rollup the *production tier* of a two-tier scorecard, (b) the fixture-tier read contract over `agent_bench_scores`, (c) the one additive route, (d) the reciprocal sign-off with A2/B on the tables C reads.

**Files (at execution time, its own commit — NOT edited by this plan doc):**
- Modify: `docs/superpowers/plans/agentic-platform/00-shared-contracts.md` (append one row to the §4.2 route table after `GetAgentWorkflowScorecard`; append a §11 supersede-by-entry — append-only, supersede by entry not by edit, per the doc's own rule).

**Addendum content to write (verbatim intent):**

- [ ] **§11 supersede-by-entry** (append-only): "**2026-07-19 (bench two-tier scorecards — SUPERSEDES the single-tier reading of §7/plan 13):** plan 13's production-traffic rollup is hereby the **production tier** of a two-tier scorecard; it is RETAINED verbatim including F8 (authoritative `live`/`content_hash` from `agent_workflow_definitions`). A **fixture tier** is added — the same column-per-version shape computed over `agent_bench_scores` (B, `1773106102`) joined through `agent_bench_cells` (A2, `1773106101`), scoped to one **source experiment**, with **paired-on-fixture deltas** (candidate − baseline averaged over the fixtures both ran) and `control_status` (A2 experiment column) surfaced as `evaluator_suspect`. Labels are joins (principle 3) — the fixture tier reads no label columns off fixtures; it reads the evaluator-produced `metrics` map only. Reads are **unscored-tolerant**: when no experiment has scored the requested versions, fixture columns are identity-only (F8 read of `agent_workflow_definitions`) with empty metrics and `bench_available=false`; the production tier and the whole scorecard still return. (The bench tables A2/B are a HARD same-wave dependency C merges strictly after `102`, and Concourse migrates before it serves, so their absence is not a runtime case — C uses **no `to_regclass` table-existence guard**; unlike plan 13's `to_regclass('agent_outcomes')`, which guards a *different, optional* track's table.) **C writes no domain table and claims no bench migration** (`100/101/102` are A1/A2/B). A conditional covering index on `agent_bench_cells (variant, variant_version)` — only if auto experiment-resolution shows in slow-query logs — indexes **A2's frozen table**, so it is NOT a unilateral C decision: it requires **A2's explicit sign-off**, and the preferred home is A2's own migration `1773106101` (add the index there). Only if A2 declines and the index is still needed does C add it as plan 13's reserved `1773106111` (never a bench number `100/101/102`), recorded here with A2's acknowledgement. **Cross-track coordination — auto-control cells vs the A2 UNIQUE key (Resolved by A2, §1.12.3 — amended 2026-07-19 post-review):** the shipped key INCLUDES `control_role` — `UNIQUE (experiment_id, fixture_id, variant, variant_version, control_role, repetition)` — so an auto negative-control cell (`baseline-clone`/`degraded`, spec §5) coexists with the real cell at the same rep; C reads via the `control_role=''` filter. C's Task 6 test fixtures keep controls at `repetition` ≥ 2 (rep value arbitrary — the 6-col key permits any rep). Affects: bench-core (A2), evaluators (B)."
- [ ] **§4.2 route row** (append after `GetAgentWorkflowScorecard`):

  ```markdown
  | `GetAgentWorkflowPromotion` | GET | `/api/v1/agent/workflows/:workflow_name/promotion` (`?versions=&experiment=`) | authorized viewer | bench-C (two-tier scorecards) |
  ```

  `GetAgentWorkflowPromotion` returns `{workflow_name, production: Scorecard, fixture: FixtureScorecard}` — the production tier is exactly `GetAgentWorkflowScorecard`'s body (reused `scorecards.Store.Scorecard`), the fixture tier is the new rollup. It is a **read** (viewer tier), the sibling that *informs* the existing `PromoteAgentWorkflowVersion` (set-live) **write** — never triggers it. `?versions=` is required (reuses `ParseVersionsCSV`); `?experiment=` is optional (0 ⇒ auto-resolve the newest experiment scoring the requested versions).

- [ ] **Verify + commit** (at execution time): `grep -n "GetAgentWorkflowPromotion\|bench two-tier scorecards" docs/superpowers/plans/agentic-platform/00-shared-contracts.md` → two matches. Commit: `docs(agentic): bench two-tier scorecards contract addendum — production/fixture tiers, promotion route, no migration`.

**Why prose-only here:** the same house rule plan 13 Task 1 follows — the contract file is edited in its own commit at execution start so the A2/B planners read the sign-off before code; this plan document only specifies what that commit says.

---

### Task 2: `agent/api/scorecards` — fixture-tier domain types + `PromotionView`

Additive to plan 13's package (a NEW `fixture_types.go`; `types.go` untouched). A `FixtureScorecard` mirrors the production `Scorecard`'s "one column per requested version" shape (spec §7 "same scorecard shape"), but because the evaluator metric vocabulary is per-step-kind (spec §4 evaluability ladder), a column's body is a **dynamic metric list**, not the fixed `VersionColumn` fields. Deltas are pointers (nil = baseline column, or no shared fixtures — dark like plan 13's outcome pointers). Plain `testing`, matching plan 13.

**Files:**
- Create: `agent/api/scorecards/fixture_types.go`
- Test: `agent/api/scorecards/fixture_types_test.go`

**Steps:**

- [ ] **Step 1: Write the failing test** `agent/api/scorecards/fixture_types_test.go`:

```go
package scorecards_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/concourse/concourse/agent/api/scorecards"
)

func TestFixtureMetricOmitsDarkDelta(t *testing.T) {
	// A baseline column (or a candidate sharing zero fixtures with the
	// baseline) has no paired delta -> pointer nil -> JSON omits it,
	// exactly like plan 13's dark outcome pointers.
	m := scorecards.FixtureMetric{Name: "precision", Mean: 0.83, Min: 0.7, Max: 0.9, SampleCount: 24}
	b, _ := json.Marshal(m)
	if strings.Contains(string(b), "paired_delta") {
		t.Fatalf("nil paired delta must be omitted: %s", b)
	}
	d := 0.12
	m.PairedDelta = &d
	m.PairedCount = intp(18)
	b, _ = json.Marshal(m)
	if !strings.Contains(string(b), "paired_delta") || !strings.Contains(string(b), "18") {
		t.Fatalf("non-nil paired delta+count must serialize: %s", b)
	}
}

func TestFixtureScorecardControlStatusAndAvailability(t *testing.T) {
	// evaluator_suspect is a plain bool derived by the store from
	// control_status='fail' (spec principle 5) — annotated, never dropped.
	sc := scorecards.FixtureScorecard{
		WorkflowName:  "review-prompts",
		StepKind:      "review",
		BenchAvailable: true,
		ControlStatus: "fail",
		EvaluatorSuspect: true,
	}
	b, _ := json.Marshal(sc)
	for _, want := range []string{`"control_status":"fail"`, `"evaluator_suspect":true`, `"bench_available":true`} {
		if !strings.Contains(string(b), want) {
			t.Fatalf("missing %s in %s", want, b)
		}
	}
}

func TestPromotionViewBundlesBothTiers(t *testing.T) {
	// The promotion view holds the production Scorecard (plan 13, RETAINED,
	// carries F8) beside the fixture tier. Fixture omitted when nil (bench
	// tables absent at deploy) — production still present.
	pv := scorecards.PromotionView{
		WorkflowName: "review-prompts",
		Production:   &scorecards.Scorecard{WorkflowName: "review-prompts"},
	}
	b, _ := json.Marshal(pv)
	if strings.Contains(string(b), `"fixture"`) {
		t.Fatalf("nil fixture must be omitted: %s", b)
	}
	pv.Fixture = &scorecards.FixtureScorecard{WorkflowName: "review-prompts"}
	b, _ = json.Marshal(pv)
	if !strings.Contains(string(b), `"fixture"`) || !strings.Contains(string(b), `"production"`) {
		t.Fatalf("both tiers must serialize: %s", b)
	}
}

func intp(i int) *int { return &i }
```

- [ ] **Step 2: Run test to verify it fails.** `go test ./agent/api/scorecards/` — Expected: build error (`FixtureScorecard`/`FixtureMetric`/`PromotionView` undefined).

- [ ] **Step 3: Write `agent/api/scorecards/fixture_types.go`:**

```go
package scorecards

//go:generate go run github.com/maxbrunsfeld/counterfeiter/v6 -generate

// Control status values (mirror agent_bench_experiments.control_status,
// spec §5). The bench harness (A2) writes them; C only reads.
const (
	ControlPending = "pending"
	ControlPass    = "pass"
	ControlFail    = "fail"
	ControlNone    = "none"
)

// FixtureScorecard is the fixture-tier rollup for one workflow (variant):
// the same "one column per requested version" shape as the production
// Scorecard (spec §7), computed over agent_bench_scores instead of
// production traffic. Its columns' bodies are a dynamic metric list
// (the score envelope's metrics{} map, §2) because the evaluable metric
// vocabulary is per step_kind (§4). Deltas are PAIRED on fixture_id
// within one source experiment (the statistical reason the inner loop
// exists). control_status carries the negative-control verdict; an
// evaluator-suspect experiment is annotated on every column, never dropped
// (principle 5).
type FixtureScorecard struct {
	WorkflowName string `json:"workflow_name"`
	StepKind     string `json:"step_kind,omitempty"` // review|implement|plan|workflow (from the source experiment)

	// BenchAvailable is false when NO experiment has scored the requested
	// versions yet (the candidate is defined but not yet benched): columns are
	// then identity-only (F8 definition read) with empty metrics — honest,
	// never a hard failure. (The bench tables themselves are a hard dependency
	// C merges after, so their absence is not a runtime case; see Task 3.)
	BenchAvailable bool `json:"bench_available"`

	// Source experiment (0 when none scored these versions). The whole
	// column set is scoped to ONE experiment so paired deltas are
	// well-defined (candidate + baseline ran the same fixtures).
	ExperimentID    int    `json:"experiment_id"`
	BaselineVersion int    `json:"baseline_version"` // the version deltas are paired against (live, else lowest requested)
	ControlStatus   string `json:"control_status,omitempty"`
	EvaluatorSuspect bool  `json:"evaluator_suspect"` // control_status='fail' OR experiment status='evaluator-suspect'

	// Evaluator pin: the SINGLE (name, version) whose ok scores the reported
	// metrics/deltas are computed over. v1 assumes one evaluator per experiment
	// (spec §6 default: evaluator = the live evaluator for the step_kind). If
	// >1 evaluator produced scores, the store pins the DOMINANT one (most ok
	// scores; deterministic tie-break by name,version) and sets
	// MultipleEvaluators=true — the numbers still correspond to exactly this
	// one evaluator (metric/delta queries filter by it), never a silent average
	// across evaluators. Per-evaluator split is a later slice.
	EvaluatorName      string `json:"evaluator_name,omitempty"`
	EvaluatorVersion   int    `json:"evaluator_version,omitempty"`
	MultipleEvaluators bool   `json:"multiple_evaluators,omitempty"` // >1 evaluator scored; reported numbers are the pinned one only

	Columns []FixtureColumn `json:"columns"`
}

// FixtureColumn is one workflow version's fixture-tier body. Its identity
// (Version/Live/ContentHash) is read AUTHORITATIVELY from
// agent_workflow_definitions (the F8 read, plan 13 §11) so a candidate with
// zero production traffic AND zero bench scores still renders a real column.
type FixtureColumn struct {
	Version     int    `json:"version"`
	Live        bool   `json:"live"`         // authoritative (F8 carried)
	ContentHash string `json:"content_hash"` // authoritative (F8 carried)
	IsBaseline  bool   `json:"is_baseline"`

	// Cell denominators for THIS experiment (counts alongside numbers —
	// plan 13's small-team honesty). status taxonomy is the cell's
	// {ok,failed,error,skipped-budget}; scores never carry 'failed' (§2).
	FixtureCount      int `json:"fixture_count"` // distinct fixtures with an ok score
	CellOK            int `json:"cell_ok"`
	CellFailed        int `json:"cell_failed"`
	CellError         int `json:"cell_error"`
	CellSkippedBudget int `json:"cell_skipped_budget"`

	Metrics []FixtureMetric `json:"metrics"`
}

// FixtureMetric is one metric name aggregated across a column's ok scores.
// Reps report a distribution (mean/min/max, spec §3 nondeterminism); the
// paired delta is the candidate−baseline difference averaged over the
// fixtures BOTH variants ran (nil for the baseline column, or when the pair
// share no fixtures). Polarity (higher-vs-lower better) is NOT baked in:
// the store reports a signed delta; the renderer/human reads the sign per
// metric semantics (precision↑ vs cost_usd↓).
type FixtureMetric struct {
	Name        string   `json:"name"`
	Mean        float64  `json:"mean"`
	Min         float64  `json:"min"`
	Max         float64  `json:"max"`
	SampleCount int      `json:"sample_count"`         // ok score rows (fixture × rep) contributing
	PairedDelta *float64 `json:"paired_delta,omitempty"`
	PairedCount *int     `json:"paired_count,omitempty"` // fixtures shared with the baseline
}

// PromotionView is the two-tier side-by-side surface (spec §7). Production
// (plan 13's Scorecard, RETAINED, carries F8) is honest+slow and may be
// dark for a candidate with no traffic; Fixture discovers, is paired, and
// is real even at zero production traffic. set-live stays a human decision
// informed by both (principle 7); this view never triggers it.
type PromotionView struct {
	WorkflowName string            `json:"workflow_name"`
	Production   *Scorecard        `json:"production"`
	Fixture      *FixtureScorecard `json:"fixture,omitempty"`
}

// FixtureStore computes the fixture tier. Implemented by
// atc/db.NewFixtureScorecardStore over the bench tables; a counterfeiter
// fake backs the promotion handler test.
//
//counterfeiter:generate . FixtureStore
type FixtureStore interface {
	// FixtureScorecard aggregates agent_bench_scores for the workflow's
	// versions, paired on fixture within a single source experiment.
	// experimentID > 0 pins that experiment; 0 auto-resolves the newest
	// experiment that scored the requested versions. Never errors when no
	// experiment has scored the requested versions — returns identity-only
	// columns (F8) with BenchAvailable=false / empty metrics.
	FixtureScorecard(workflowName string, versions []int, experimentID int) (*FixtureScorecard, error)
}
```

- [ ] **Step 4: Run test to verify it passes.** `go test ./agent/api/scorecards/` — Expected: PASS.
- [ ] **Step 5: Generate the fake** (handler test in Task 8 uses it): `go generate ./agent/api/scorecards/...` → `agent/api/scorecards/scorecardsfakes/fake_fixture_store.go` (alongside plan 13's `fake_store.go`). Then `go build ./agent/api/scorecards/...`.
- [ ] **Step 6: Commit.** `git add agent/api/scorecards && git commit -m "feat(scorecards): fixture-tier domain types (FixtureScorecard/Column/Metric), PromotionView, FixtureStore"`

---

### Task 3: `atc/db` FixtureScorecardStore — resolution (F8 reuse), identity columns, cell-status counts, unscored path

The store scaffold. `NewFixtureScorecardStore` implements `scorecards.FixtureStore`. It (a) resolves the **baseline** version (the F8 `definition` read tells us which requested version is `live`; else the lowest) and the **source experiment** (explicit or newest scoring the versions), (b) when no experiment has scored the requested versions (`expID == 0`), returns identity-only columns (F8) with empty metrics and `BenchAvailable=false` — never a 500 (the reachable "candidate defined but not yet benched" case), (c) fills each scored column's identity from `agent_workflow_definitions` — the **same read plan 13's F8 fix uses** — so a candidate with zero bench scores still renders, and (d) counts cell status. **No `to_regclass` table-existence guard:** the bench tables are a hard same-wave dependency C merges strictly after (`102`), and the `atc/db` suite migrates the template DB to HEAD, so they always exist when this code runs — a guard would be untestable (the pooled test `dbConn` defeats a `SET search_path` probe) and would defend an unreachable state (unlike plan 13's `to_regclass('agent_outcomes')`, which guards a *different, optional* track's table). Metrics (Task 4), deltas (Task 5), controls (Task 6) extend the same store. Ginkgo over the template DB, inserting bench fixtures directly with `dbConn.Exec` (no dependency on A2/B factories).

**Files:**
- Create: `atc/db/fixture_scorecard_store.go`
- Test: `atc/db/fixture_scorecard_store_test.go`

**Steps:**

- [ ] **Step 1: Write the failing Ginkgo spec** `atc/db/fixture_scorecard_store_test.go` (in the existing `db_test` suite, which migrates the template DB to HEAD; at execution time A2's `1773106101` and B's `1773106102` are on the branch so the bench tables exist):

```go
package db_test

import (
	"github.com/concourse/concourse/agent/api/scorecards"
	"github.com/concourse/concourse/atc/db"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("FixtureScorecardStore", func() {
	var store scorecards.FixtureStore

	BeforeEach(func() {
		store = db.NewFixtureScorecardStore(dbConn)
		for _, tbl := range []string{"agent_bench_scores", "agent_bench_cells", "agent_bench_experiments", "agent_workflow_definitions"} {
			_, err := dbConn.Exec("DELETE FROM " + tbl)
			Expect(err).ToNot(HaveOccurred())
		}

		// Authoritative definitions (F8 source of truth): review-prompts v4
		// is live, v5 is a candidate. v5 has NO production traffic and (in
		// this task) no scores yet — it must still render a real column.
		_, err := dbConn.Exec(`
			INSERT INTO agent_workflow_definitions (name, version, content_hash, live, definition, description, created_by)
			VALUES ('review-prompts', 4, 'hash-v4', true,  '{}', '', 'seed'),
			       ('review-prompts', 5, 'hash-v5', false, '{}', '', 'seed')`)
		Expect(err).ToNot(HaveOccurred())

		// One experiment, review step: variants v4 (baseline) and v5,
		// two fixtures each (fixture ids 101,102), one rep. Cell status mix
		// proves the ok/failed/error/skipped-budget split.
		_, err = dbConn.Exec(`
			INSERT INTO agent_bench_experiments (id, name, step_kind, spec, status, control_status, created_by)
			VALUES (7, 'v5 bake-off', 'review', '{}', 'complete', 'pass', 'tdmtrader')`)
		Expect(err).ToNot(HaveOccurred())
		_, err = dbConn.Exec(`
			INSERT INTO agent_bench_cells (id, experiment_id, fixture_id, variant, variant_version, control_role, repetition, status)
			VALUES
			  (1, 7, 101, 'review-prompts', 4, '', 1, 'ok'),
			  (2, 7, 102, 'review-prompts', 4, '', 1, 'ok'),
			  (3, 7, 101, 'review-prompts', 5, '', 1, 'ok'),
			  (4, 7, 102, 'review-prompts', 5, '', 1, 'failed')`)
		Expect(err).ToNot(HaveOccurred())
	})

	It("resolves baseline+experiment and fills authoritative identity (F8) + cell counts", func() {
		sc, err := store.FixtureScorecard("review-prompts", []int{4, 5}, 0)
		Expect(err).ToNot(HaveOccurred())
		Expect(sc.BenchAvailable).To(BeTrue())
		Expect(sc.ExperimentID).To(Equal(7))
		Expect(sc.StepKind).To(Equal("review"))
		Expect(sc.BaselineVersion).To(Equal(4)) // v4 is live
		Expect(sc.Columns).To(HaveLen(2))

		v4 := sc.Columns[0]
		Expect(v4.Version).To(Equal(4))
		Expect(v4.Live).To(BeTrue())                    // authoritative (F8)
		Expect(v4.ContentHash).To(Equal("hash-v4"))     // authoritative (F8)
		Expect(v4.IsBaseline).To(BeTrue())
		Expect(v4.CellOK).To(Equal(2))
		Expect(v4.FixtureCount).To(Equal(2))

		v5 := sc.Columns[1]
		Expect(v5.Version).To(Equal(5))
		Expect(v5.Live).To(BeFalse())
		Expect(v5.ContentHash).To(Equal("hash-v5"))
		Expect(v5.IsBaseline).To(BeFalse())
		Expect(v5.CellOK).To(Equal(1))
		Expect(v5.CellFailed).To(Equal(1))
		Expect(v5.FixtureCount).To(Equal(1)) // only fixture 101 scored ok
	})

	It("renders an identity-only column for a candidate with NO bench scores (F8 anchor)", func() {
		// v5 has a definition row but no cells in any experiment: no experiment
		// scored it, so BenchAvailable=false, yet it is still a real column —
		// live=false, real content hash, zero metrics (the "never a 500 /
		// identity-only" guarantee, for the case that can actually happen).
		_, err := dbConn.Exec("DELETE FROM agent_bench_cells WHERE variant_version = 5")
		Expect(err).ToNot(HaveOccurred())
		sc, err := store.FixtureScorecard("review-prompts", []int{5}, 0)
		Expect(err).ToNot(HaveOccurred())
		Expect(sc.BenchAvailable).To(BeFalse())
		Expect(sc.Columns).To(HaveLen(1))
		Expect(sc.Columns[0].Version).To(Equal(5))
		Expect(sc.Columns[0].ContentHash).To(Equal("hash-v5"))
		Expect(sc.Columns[0].Metrics).To(BeEmpty())
	})
})
```

- [ ] **Step 2: Run test to verify it fails.** `pg_isready && ginkgo --focus="FixtureScorecardStore" ./atc/db/` — Expected: FAIL (`undefined: db.NewFixtureScorecardStore`). (If `database "testdb_template" already exists`, another test run is live — wait, per CLAUDE.md.)

- [ ] **Step 3: Write `atc/db/fixture_scorecard_store.go`** (resolution + identity + counts; metrics/deltas/controls added in Tasks 4–6):

```go
package db

import (
	"database/sql"

	"github.com/concourse/concourse/agent/api/scorecards"
	"github.com/lib/pq"
)

//counterfeiter:generate . FixtureScorecardStore
type FixtureScorecardStore interface {
	scorecards.FixtureStore
}

func NewFixtureScorecardStore(conn DbConn) FixtureScorecardStore {
	return &fixtureScorecardStore{conn: conn}
}

type fixtureScorecardStore struct {
	conn DbConn
}

func (s *fixtureScorecardStore) FixtureScorecard(name string, versions []int, experimentID int) (*scorecards.FixtureScorecard, error) {
	sc := &scorecards.FixtureScorecard{WorkflowName: name}

	// Baseline = the live version among the requested set (F8 read); else
	// the lowest requested version. Deltas (Task 5) pair against it.
	sc.BaselineVersion = s.resolveBaseline(name, versions)

	// Source experiment: explicit, else the newest that scored these versions.
	// The bench tables (A2 `1773106101` / B `1773106102`) are a HARD, same-wave
	// dependency: C merges STRICTLY after them (skeleton §4 ascending order) and
	// Concourse migrates before it serves, so agent_bench_scores/_cells/
	// _experiments always exist when this code runs. No table-existence guard is
	// needed (unlike plan 13's `to_regclass('agent_outcomes')`, which guards a
	// DIFFERENT, optional track's table). The reachable "nothing scored these
	// versions yet" case is handled below (BenchAvailable=false, identity-only).
	expID, stepKind, controlStatus, expStatus, err := s.resolveExperiment(name, versions, experimentID)
	if err != nil {
		return nil, err
	}
	if expID == 0 {
		// No experiment has scored these versions yet: identity-only columns
		// (F8 read of agent_workflow_definitions), empty metrics,
		// BenchAvailable=false — the scorecard still returns, never a 500. This
		// is the case a candidate hits before any bake-off has run for it.
		return s.identityOnly(name, versions, sc), nil
	}
	sc.BenchAvailable = true
	sc.ExperimentID = expID
	sc.StepKind = stepKind
	sc.ControlStatus = controlStatus
	sc.EvaluatorSuspect = controlStatus == scorecards.ControlFail || expStatus == "evaluator-suspect" // asserted in Task 6

	for _, v := range versions {
		col := scorecards.FixtureColumn{Version: v, IsBaseline: v == sc.BaselineVersion}
		if err := s.definition(name, v, &col); err != nil { // F8 reuse
			return nil, err
		}
		if err := s.cellCounts(expID, name, v, &col); err != nil {
			return nil, err
		}
		// Task 4 resolves the evaluator pin (BEFORE this loop) and adds
		// s.metrics(...); Task 5 adds s.pairedDeltas(...). Both filter by the
		// pinned evaluator so the reported numbers correspond to exactly one
		// evaluator. Task 6 surfaces control status + adds the branch specs.
		sc.Columns = append(sc.Columns, col)
	}
	return sc, nil
}

// resolveBaseline returns the live version among versions (F8), else min.
func (s *fixtureScorecardStore) resolveBaseline(name string, versions []int) int {
	baseline := 0
	for _, v := range versions {
		if baseline == 0 || v < baseline {
			baseline = v
		}
	}
	var live sql.NullInt64
	err := s.conn.QueryRow(`
		SELECT version FROM agent_workflow_definitions
		WHERE name = $1 AND live = true AND version = ANY($2)`,
		name, pq.Array(versions),
	).Scan(&live)
	if err == nil && live.Valid {
		return int(live.Int64)
	}
	return baseline
}

// resolveExperiment returns (id, step_kind, control_status, status). id=0
// when none scored these versions. Explicit experimentID short-circuits.
// NOTE: the auto path filters cells by variant (no index yet — Task 11
// pins this); at current scale (dozens of experiments) it is an EXISTS over
// the experiment-indexed cell set.
func (s *fixtureScorecardStore) resolveExperiment(name string, versions []int, explicit int) (int, string, string, string, error) {
	q := `
		SELECT e.id, e.step_kind, e.control_status, e.status
		FROM agent_bench_experiments e
		WHERE ($3 = 0 OR e.id = $3)
		  AND EXISTS (
		    SELECT 1 FROM agent_bench_cells c
		    WHERE c.experiment_id = e.id AND c.variant = $1
		      AND c.variant_version = ANY($2) AND c.control_role = ''
		  )
		ORDER BY e.created_at DESC, e.id DESC
		LIMIT 1`
	var id int
	var stepKind, controlStatus, status string
	err := s.conn.QueryRow(q, name, pq.Array(versions), explicit).Scan(&id, &stepKind, &controlStatus, &status)
	if err == sql.ErrNoRows {
		return 0, "", "", "", nil
	}
	if err != nil {
		return 0, "", "", "", err
	}
	return id, stepKind, controlStatus, status, nil
}

// identityOnly fills identity columns only (F8 read), no metrics — used when
// no experiment has scored the requested versions (BenchAvailable stays false).
// The candidate is real (its identity is anchored by the F8 definition read)
// but has no bench data yet.
func (s *fixtureScorecardStore) identityOnly(name string, versions []int, sc *scorecards.FixtureScorecard) *scorecards.FixtureScorecard {
	for _, v := range versions {
		col := scorecards.FixtureColumn{Version: v, IsBaseline: v == sc.BaselineVersion}
		_ = s.definition(name, v, &col)
		sc.Columns = append(sc.Columns, col)
	}
	return sc
}

// definition is the F8 read, IDENTICAL to plan 13's scorecard_store.go:
// authoritative live flag + content hash from agent_workflow_definitions
// (workflow-store §1.6, unique (name, version)). This is what anchors a
// candidate with zero production traffic and zero bench scores as a real
// column. Missing row leaves Live=false / ContentHash unset.
func (s *fixtureScorecardStore) definition(name string, version int, col *scorecards.FixtureColumn) error {
	var contentHash sql.NullString
	var live sql.NullBool
	err := s.conn.QueryRow(`
		SELECT content_hash, live FROM agent_workflow_definitions
		WHERE name = $1 AND version = $2`, name, version,
	).Scan(&contentHash, &live)
	if err == sql.ErrNoRows {
		return nil
	}
	if err != nil {
		return err
	}
	if contentHash.Valid {
		col.ContentHash = contentHash.String
	}
	col.Live = live.Bool
	return nil
}

// cellCounts fills the cell-status denominators for one (experiment,
// variant, version), control_role='' (real candidate cells; auto controls
// are counted separately in Task 6). Rides agent_bench_cells_experiment.
func (s *fixtureScorecardStore) cellCounts(expID int, name string, version int, col *scorecards.FixtureColumn) error {
	return s.conn.QueryRow(`
		SELECT
		  COUNT(*) FILTER (WHERE status = 'ok'),
		  COUNT(*) FILTER (WHERE status = 'failed'),
		  COUNT(*) FILTER (WHERE status = 'error'),
		  COUNT(*) FILTER (WHERE status = 'skipped-budget'),
		  COUNT(DISTINCT fixture_id) FILTER (WHERE status = 'ok')
		FROM agent_bench_cells
		WHERE experiment_id = $1 AND variant = $2 AND variant_version = $3 AND control_role = ''`,
		expID, name, version,
	).Scan(&col.CellOK, &col.CellFailed, &col.CellError, &col.CellSkippedBudget, &col.FixtureCount)
}
```

> `pq.Array` is already a dependency across `atc/db` (grep `lib/pq` in the package). If a package-level array helper is preferred, match the existing idiom.

- [ ] **Step 4: Run test to verify it passes.** `ginkgo --focus="FixtureScorecardStore" ./atc/db/` — Expected: PASS (both specs).
- [ ] **Step 5: Generate the DB fake.** `cd atc/db && go run github.com/maxbrunsfeld/counterfeiter/v6 -o dbfakes/fake_fixture_scorecard_store.go . FixtureScorecardStore && cd ../..` then `go build ./atc/db/...`.
- [ ] **Step 6: Commit.** `git add atc/db/fixture_scorecard_store.go atc/db/fixture_scorecard_store_test.go atc/db/dbfakes && git commit -m "feat(scorecards): fixture-tier store — resolution, F8 identity anchor, cell counts, dark path"`

---

### Task 4: FixtureScorecardStore — evaluator pin + per-metric mean/min/max aggregation from the score envelope

Two coupled pieces, because the numbers are only well-defined once the evaluator is pinned. First **resolve the single evaluator** (spec §6 default: one evaluator per experiment) the reported numbers are computed over — the dominant `(name, version)` when more than one scored, plus a `MultipleEvaluators` flag — so the aggregation corresponds to exactly one evaluator, never a silent average across evaluators. Then fill each column's `Metrics` from `agent_bench_scores.metrics` (the score envelope `{name:number}` map, §2), **filtered to that pin**. Values are numbers only (§2), so unpack via `jsonb_each_text` filtered to numerics and aggregate mean/min/max + a sample count per metric name across the column's ok scores. Reps report a distribution (spec §3). Extends `FixtureScorecard` (a pre-loop pin resolution) and its per-version loop.

**Files:**
- Modify: `atc/db/fixture_scorecard_store.go` (add `resolveEvaluatorPin` before the loop + `metrics` method filtered to the pin + call it in the loop)
- Modify: `atc/db/fixture_scorecard_store_test.go` (extend `BeforeEach` with scores; add a spec)

**Steps:**

- [ ] **Step 1: Add the failing spec.** Extend `BeforeEach` with score rows (append after the cell inserts), then add a spec:

```go
		// Score envelope metrics per cell (numbers only, §2). v4 baseline:
		// precision 0.80/0.90 across its two fixtures; v5: 0.90 on fixture
		// 101 (the only ok cell). One evaluator (review-judge v3).
		_, err = dbConn.Exec(`
			INSERT INTO agent_bench_scores (cell_id, evaluator_name, evaluator_version, metrics, status, cost_usd)
			VALUES
			  (1, 'review-judge', 3, '{"precision":0.80,"recall":0.60,"cost_usd":0.40}', 'ok', 0.40),
			  (2, 'review-judge', 3, '{"precision":0.90,"recall":0.70,"cost_usd":0.42}', 'ok', 0.42),
			  (3, 'review-judge', 3, '{"precision":0.90,"recall":0.75,"cost_usd":0.30}', 'ok', 0.30)`)
		Expect(err).ToNot(HaveOccurred())
```

```go
	It("aggregates per-metric mean/min/max/sample across a column's ok scores", func() {
		sc, err := store.FixtureScorecard("review-prompts", []int{4, 5}, 0)
		Expect(err).ToNot(HaveOccurred())

		v4 := sc.Columns[0]
		prec := metricByName(v4.Metrics, "precision")
		Expect(prec).ToNot(BeNil())
		Expect(prec.Mean).To(BeNumerically("~", 0.85, 1e-9)) // (0.80+0.90)/2
		Expect(prec.Min).To(BeNumerically("~", 0.80, 1e-9))
		Expect(prec.Max).To(BeNumerically("~", 0.90, 1e-9))
		Expect(prec.SampleCount).To(Equal(2))

		v5 := sc.Columns[1]
		p5 := metricByName(v5.Metrics, "precision")
		Expect(p5.Mean).To(BeNumerically("~", 0.90, 1e-9)) // only fixture 101 ok
		Expect(p5.SampleCount).To(Equal(1))

		// The single evaluator is pinned; metrics are filtered to it.
		Expect(sc.EvaluatorName).To(Equal("review-judge"))
		Expect(sc.EvaluatorVersion).To(Equal(3))
	})
```

Add the helper to the test file:

```go
func metricByName(ms []scorecards.FixtureMetric, name string) *scorecards.FixtureMetric {
	for i := range ms {
		if ms[i].Name == name {
			return &ms[i]
		}
	}
	return nil
}
```

- [ ] **Step 2: Run test to verify it fails.** `ginkgo --focus="FixtureScorecardStore" ./atc/db/` — Expected: FAIL (`v4.Metrics` empty; `sc.EvaluatorName` empty — `resolveEvaluatorPin`/`metrics` not wired).

- [ ] **Step 3: Resolve the evaluator pin (before the loop) + add the `metrics` method & call.** First, in `FixtureScorecard`, **after the `expID == 0` early-return and the `sc.ExperimentID`/`sc.EvaluatorSuspect` assignments (which need `expID`), before the `for _, v := range versions` loop**, resolve the single evaluator the reported numbers are computed over — so metrics (this task) and deltas (Task 5) can filter by it:

```go
	// Pin the SINGLE evaluator the reported metrics/deltas are computed over.
	// v1 assumes one evaluator per experiment (spec §6 default); if more than
	// one scored, pin the dominant (most ok scores) and flag it — the numbers
	// stay a single evaluator's, never a silent average across evaluators.
	if err := s.resolveEvaluatorPin(expID, name, versions, sc); err != nil {
		return nil, err
	}
```

Then, in the loop after `cellCounts`, fill metrics filtered to that pin:

```go
		if err := s.metrics(expID, name, v, sc.EvaluatorName, sc.EvaluatorVersion, &col); err != nil {
			return nil, err
		}
```

Methods:

```go
// resolveEvaluatorPin sets sc.EvaluatorName/Version to the SINGLE evaluator
// whose ok scores back the reported numbers: the DOMINANT one (most ok scores;
// deterministic tie-break by name,version). None -> unset (no scores yet).
// This is what makes the pinned metrics/deltas well-defined — every metric and
// delta query filters by (sc.EvaluatorName, sc.EvaluatorVersion), so the
// numbers correspond to exactly this evaluator and are never averaged across
// evaluators. Task 6 adds the MultipleEvaluators flag (a distinct-count read)
// for the >1-evaluator honesty case.
func (s *fixtureScorecardStore) resolveEvaluatorPin(expID int, name string, versions []int, sc *scorecards.FixtureScorecard) error {
	err := s.conn.QueryRow(`
		SELECT s.evaluator_name, s.evaluator_version
		FROM agent_bench_scores s
		JOIN agent_bench_cells c ON c.id = s.cell_id
		WHERE c.experiment_id = $1 AND c.variant = $2
		  AND c.variant_version = ANY($3) AND c.control_role = '' AND s.status = 'ok'
		GROUP BY s.evaluator_name, s.evaluator_version
		ORDER BY COUNT(*) DESC, s.evaluator_name, s.evaluator_version
		LIMIT 1`, expID, name, pq.Array(versions),
	).Scan(&sc.EvaluatorName, &sc.EvaluatorVersion)
	if err == sql.ErrNoRows {
		return nil // no scores yet -> pin unset
	}
	return err
}
```

```go
// numericMetricSQL unpacks the score envelope's metrics{} map to
// (key, numeric value) rows — numbers only per §2 (the regex drops any
// non-numeric metric a future envelope might carry).
const numericMetricSQL = `
	SELECT key, value::numeric AS val
	FROM jsonb_each_text(s.metrics)
	WHERE value ~ '^-?[0-9]+(\.[0-9]+)?$'`

// metrics fills col.Metrics with per-name mean/min/max + sample count over
// the ok scores of this (experiment, variant, version)'s real cells, FILTERED
// to the pinned evaluator so reported numbers correspond to exactly one
// evaluator (never averaged across evaluators). When the pin is unset (no ok
// scores) the evaluator filter matches nothing and col.Metrics stays empty.
func (s *fixtureScorecardStore) metrics(expID int, name string, version int, evalName string, evalVer int, col *scorecards.FixtureColumn) error {
	rows, err := s.conn.Query(`
		SELECT m.key, AVG(m.val), MIN(m.val), MAX(m.val), COUNT(*)
		FROM agent_bench_scores s
		JOIN agent_bench_cells c ON c.id = s.cell_id
		CROSS JOIN LATERAL (`+numericMetricSQL+`) m
		WHERE c.experiment_id = $1 AND c.variant = $2 AND c.variant_version = $3
		  AND c.control_role = '' AND s.status = 'ok'
		  AND s.evaluator_name = $4 AND s.evaluator_version = $5
		GROUP BY m.key
		ORDER BY m.key`,
		expID, name, version, evalName, evalVer,
	)
	if err != nil {
		return err
	}
	defer Close(rows)
	for rows.Next() {
		var m scorecards.FixtureMetric
		if err := rows.Scan(&m.Name, &m.Mean, &m.Min, &m.Max, &m.SampleCount); err != nil {
			return err
		}
		col.Metrics = append(col.Metrics, m)
	}
	return rows.Err()
}
```

> `Close(rows)` is the `atc/db` helper (used throughout `agent_reviews_factory.go`); confirm the name with `grep -n "func Close(" atc/db/open.go` and match it (plan 13 Task 4 uses the same helper).

- [ ] **Step 4: Run test to verify it passes.** `ginkgo --focus="FixtureScorecardStore" ./atc/db/` — Expected: PASS.
- [ ] **Step 5: Commit.** `git add atc/db/fixture_scorecard_store.go atc/db/fixture_scorecard_store_test.go && git commit -m "feat(scorecards): fixture-tier per-metric mean/min/max/sample from the score envelope"`

---

### Task 5: FixtureScorecardStore — paired-on-fixture deltas vs baseline

The load-bearing statistical piece (spec §7): for each candidate column and each metric, the delta is `candidate − baseline` averaged over the fixtures **both variants ran** (a within-fixture paired comparison, immune to the difficulty confound production suffers). Per-fixture value = mean over reps. `PairedDelta`/`PairedCount` are nil for the baseline column and when the pair share no fixtures (dark, honest). Extends the loop.

**Files:**
- Modify: `atc/db/fixture_scorecard_store.go` (add `pairedDeltas` + call)
- Modify: `atc/db/fixture_scorecard_store_test.go` (add a spec)

**Steps:**

- [ ] **Step 1: Add the failing spec:**

```go
	It("computes paired-on-fixture deltas vs the baseline (shared fixtures only)", func() {
		sc, err := store.FixtureScorecard("review-prompts", []int{4, 5}, 0)
		Expect(err).ToNot(HaveOccurred())

		// Baseline v4 has no paired delta (it IS the baseline).
		Expect(metricByName(sc.Columns[0].Metrics, "precision").PairedDelta).To(BeNil())

		// v5 vs v4: only fixture 101 is shared (v5's fixture 102 cell failed
		// -> no ok score). On 101: v5 precision 0.90 − v4 precision 0.80 =
		// +0.10, over 1 shared fixture.
		p5 := metricByName(sc.Columns[1].Metrics, "precision")
		Expect(p5.PairedDelta).ToNot(BeNil())
		Expect(*p5.PairedDelta).To(BeNumerically("~", 0.10, 1e-9))
		Expect(*p5.PairedCount).To(Equal(1))
	})
```

- [ ] **Step 2: Run test to verify it fails.** `ginkgo --focus="FixtureScorecardStore" ./atc/db/` — Expected: FAIL (`PairedDelta` nil for v5).

- [ ] **Step 3: Add the `pairedDeltas` method + call.** In the loop, after `metrics`, only for non-baseline columns:

```go
		if v != sc.BaselineVersion {
			if err := s.pairedDeltas(expID, name, sc.BaselineVersion, v, sc.EvaluatorName, sc.EvaluatorVersion, &col); err != nil {
				return nil, err
			}
		}
```

Method:

```go
// pairedDeltas fills PairedDelta/PairedCount per metric: candidate − baseline
// averaged over the fixtures BOTH variants scored ok in this experiment
// (per-fixture value = mean over reps). Metrics not shared by the pair, and
// the baseline column itself, stay nil (dark). Signed — polarity is the
// reader's (precision↑ vs cost_usd↓).
func (s *fixtureScorecardStore) pairedDeltas(expID int, name string, baseline, candidate int, evalName string, evalVer int, col *scorecards.FixtureColumn) error {
	perFixture := `
		SELECT c.fixture_id, m.key, AVG(m.val) AS v
		FROM agent_bench_scores s
		JOIN agent_bench_cells c ON c.id = s.cell_id
		CROSS JOIN LATERAL (` + numericMetricSQL + `) m
		WHERE c.experiment_id = $1 AND c.variant = $2 AND c.variant_version = $3
		  AND c.control_role = '' AND s.status = 'ok'
		  AND s.evaluator_name = $5 AND s.evaluator_version = $6
		GROUP BY c.fixture_id, m.key`

	rows, err := s.conn.Query(`
		WITH cand AS (`+perFixture+`),
		     base AS (`+
		// reuse the same shape with the baseline version bound to $4, same
		// pinned evaluator ($5,$6) — pairing must compare like evaluator to like
		`SELECT c.fixture_id, m.key, AVG(m.val) AS v
		 FROM agent_bench_scores s
		 JOIN agent_bench_cells c ON c.id = s.cell_id
		 CROSS JOIN LATERAL (`+numericMetricSQL+`) m
		 WHERE c.experiment_id = $1 AND c.variant = $2 AND c.variant_version = $4
		   AND c.control_role = '' AND s.status = 'ok'
		   AND s.evaluator_name = $5 AND s.evaluator_version = $6
		 GROUP BY c.fixture_id, m.key)
		SELECT cand.key, AVG(cand.v - base.v), COUNT(*)
		FROM cand JOIN base ON base.fixture_id = cand.fixture_id AND base.key = cand.key
		GROUP BY cand.key`,
		expID, name, candidate, baseline, evalName, evalVer,
	)
	if err != nil {
		return err
	}
	defer Close(rows)
	deltas := map[string]struct {
		d float64
		n int
	}{}
	for rows.Next() {
		var key string
		var d float64
		var n int
		if err := rows.Scan(&key, &d, &n); err != nil {
			return err
		}
		deltas[key] = struct {
			d float64
			n int
		}{d, n}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for i := range col.Metrics {
		if hit, ok := deltas[col.Metrics[i].Name]; ok {
			d := hit.d
			n := hit.n
			col.Metrics[i].PairedDelta = &d
			col.Metrics[i].PairedCount = &n
		}
	}
	return nil
}
```

- [ ] **Step 4: Run test to verify it passes.** `ginkgo --focus="FixtureScorecardStore" ./atc/db/` — Expected: PASS.
- [ ] **Step 5: Commit.** `git add atc/db/fixture_scorecard_store.go atc/db/fixture_scorecard_store_test.go && git commit -m "feat(scorecards): fixture-tier paired-on-fixture deltas vs baseline"`

---

### Task 6: FixtureScorecardStore — negative-control status, `MultipleEvaluators` flag, control-cell exclusion

Surface the negative-control verdict (spec principle 5) and harden the evaluator pin's honesty case. `control_status`/`EvaluatorSuspect` are already set in Task 3; the dominant evaluator pin (name+version) in Task 4. This task adds the ONE remaining store behavior — the `MultipleEvaluators` flag (a distinct-evaluator count) — and locks in two cross-cutting invariants with specs: (a) `EvaluatorSuspect` flips true when `control_status='fail'` or the experiment `status='evaluator-suspect'`, and (b) control-role cells (`baseline-clone`/`degraded`) never leak into the real candidate columns' counts/metrics (every store query filters `control_role=''`). The `MultipleEvaluators` spec is the fail-first anchor; the control specs are regression locks. The 0-evaluator branch (pin unset) is already covered by Task 3's no-scores spec.

**Files:**
- Modify: `atc/db/fixture_scorecard_store.go` (extend `resolveEvaluatorPin` (Task 4) with the distinct-evaluator count → `MultipleEvaluators`; no other store change)
- Modify: `atc/db/fixture_scorecard_store_test.go` (add two specs + frozen-key-conformant control-cell fixtures)

**Steps:**

- [ ] **Step 1: Add the specs.** First the control-status/exclusion lock-in (control cells at rep ≥ 2 — rep value arbitrary, A2's 6-col key permits any rep):

```go
	It("surfaces control status/evaluator-suspect and excludes control-role cells", func() {
		// Flip the experiment to a failed control (a degraded variant that
		// did NOT lose -> evaluator blind, spec §5) and add auto-control cells
		// that must NOT leak into the real v4 column's counts/metrics.
		_, err := dbConn.Exec(`UPDATE agent_bench_experiments SET control_status='fail', status='evaluator-suspect' WHERE id=7`)
		Expect(err).ToNot(HaveOccurred())
		// Auto-control cells clone the real (fixture 101, v4) cell at rep >= 2
		// (rep value arbitrary — A2's 6-col UNIQUE includes control_role, so
		// any rep is permitted). C only READS these cells.
		_, err = dbConn.Exec(`
			INSERT INTO agent_bench_cells (id, experiment_id, fixture_id, variant, variant_version, control_role, repetition, status)
			VALUES
			  (5, 7, 101, 'review-prompts', 4, 'baseline-clone', 2, 'ok'),
			  (6, 7, 101, 'review-prompts', 4, 'degraded',       3, 'ok')`)
		Expect(err).ToNot(HaveOccurred())
		_, err = dbConn.Exec(`
			INSERT INTO agent_bench_scores (cell_id, evaluator_name, evaluator_version, metrics, status, cost_usd)
			VALUES
			  (5, 'review-judge', 3, '{"precision":0.80}', 'ok', 0.40),
			  (6, 'review-judge', 3, '{"precision":0.10}', 'ok', 0.40)`)
		Expect(err).ToNot(HaveOccurred())

		sc, err := store.FixtureScorecard("review-prompts", []int{4, 5}, 0)
		Expect(err).ToNot(HaveOccurred())
		Expect(sc.ControlStatus).To(Equal("fail"))
		Expect(sc.EvaluatorSuspect).To(BeTrue())
		Expect(sc.EvaluatorName).To(Equal("review-judge"))
		Expect(sc.EvaluatorVersion).To(Equal(3))
		Expect(sc.MultipleEvaluators).To(BeFalse()) // still one evaluator

		// v4's real (control_role='') cell count + metric sample are unchanged
		// by the two control-role cells added above.
		Expect(sc.Columns[0].CellOK).To(Equal(2))
		Expect(metricByName(sc.Columns[0].Metrics, "precision").SampleCount).To(Equal(2))
	})
```

Then the multi-evaluator branch (the fail-first anchor — `MultipleEvaluators` is not set until Step 3):

```go
	It("flags MultipleEvaluators, pins the dominant, and reports only its numbers", func() {
		// A second evaluator (review-judge v4) scores ONE real cell. v3 stays
		// dominant (3 ok scores > 1). MultipleEvaluators flips true; the v4
		// column's metrics stay v3's only — v4's 0.20 must NOT drag the mean.
		_, err := dbConn.Exec(`
			INSERT INTO agent_bench_scores (cell_id, evaluator_name, evaluator_version, metrics, status, cost_usd)
			VALUES (1, 'review-judge', 4, '{"precision":0.20}', 'ok', 0.40)`)
		Expect(err).ToNot(HaveOccurred())

		sc, err := store.FixtureScorecard("review-prompts", []int{4, 5}, 0)
		Expect(err).ToNot(HaveOccurred())
		Expect(sc.MultipleEvaluators).To(BeTrue())
		Expect(sc.EvaluatorName).To(Equal("review-judge"))
		Expect(sc.EvaluatorVersion).To(Equal(3)) // dominant: 3 ok scores vs 1

		// v4 column precision is still (0.80+0.90)/2 = 0.85 over 2 samples —
		// the v4-evaluator's 0.20 on cell 1 is excluded by the pin filter.
		prec := metricByName(sc.Columns[0].Metrics, "precision")
		Expect(prec.Mean).To(BeNumerically("~", 0.85, 1e-9))
		Expect(prec.SampleCount).To(Equal(2))
	})
```

- [ ] **Step 2: Run to verify it fails.** `ginkgo --focus="FixtureScorecardStore" ./atc/db/` — Expected: the multi-evaluator spec FAILS (`MultipleEvaluators` is never set — stays false). The control spec already PASSES (`ControlStatus`/`EvaluatorSuspect` from Task 3; pin from Task 4; the `control_role=''` filter from Tasks 3–5 already excludes control cells — this spec is a regression lock).

- [ ] **Step 3: Extend `resolveEvaluatorPin` (Task 4) to set `MultipleEvaluators`.** Restructure the dominant-pin `Scan`'s trailing `return err` into an explicit `if err == sql.ErrNoRows { return nil }; if err != nil { return err }` (so a missing pin short-circuits), then count distinct evaluators over the real candidate cells:

```go
	// ... dominant-pin Scan above; on ErrNoRows return nil (pin unset) ...
	var distinct int
	if err := s.conn.QueryRow(`
		SELECT COUNT(*) FROM (
			SELECT DISTINCT s.evaluator_name, s.evaluator_version
			FROM agent_bench_scores s
			JOIN agent_bench_cells c ON c.id = s.cell_id
			WHERE c.experiment_id = $1 AND c.variant = $2
			  AND c.variant_version = ANY($3) AND c.control_role = '' AND s.status = 'ok'
		) d`, expID, name, pq.Array(versions)).Scan(&distinct); err != nil {
		return err
	}
	sc.MultipleEvaluators = distinct > 1
	return nil
```

The reported metrics/deltas remain the single pinned evaluator's (Tasks 4–5 filter by `sc.EvaluatorName`/`Version`); `MultipleEvaluators` is honesty-only — it never changes which numbers are shown, only signals that a per-evaluator split (a later slice) would refine them.

- [ ] **Step 4: Run test to verify it passes.** `ginkgo --focus="FixtureScorecardStore" ./atc/db/` — Expected: PASS (all specs).
- [ ] **Step 5: Commit.** `git add atc/db/fixture_scorecard_store.go atc/db/fixture_scorecard_store_test.go && git commit -m "feat(scorecards): fixture-tier control status, evaluator-suspect, MultipleEvaluators flag, control-cell exclusion"`

---

### Task 7: `agent/api/scorecards` — promotion assembler (production RETAINED + fixture)

A thin composition unit that bundles the two tiers into a `PromotionView`. It calls plan 13's retained `scorecards.Store.Scorecard` (production tier, carries F8) and this plan's `FixtureStore.FixtureScorecard` (fixture tier). No DB access of its own; both stores are injected — so the assembler is unit-tested with the two counterfeiter fakes, proving the candidate-with-no-production-traffic case at the composition layer. Kept in the domain package (no `atc/api/accessor` import — the import-cycle rule plan 13/12 follow).

**Files:**
- Create: `agent/api/scorecards/promotion.go`
- Test: `agent/api/scorecards/promotion_test.go`

**Steps:**

- [ ] **Step 1: Write the failing test** `agent/api/scorecards/promotion_test.go` (plain `testing`, uses both fakes):

```go
package scorecards_test

import (
	"testing"

	"github.com/concourse/concourse/agent/api/scorecards"
	"github.com/concourse/concourse/agent/api/scorecards/scorecardsfakes"
)

func TestAssemblePromotionBundlesBothTiers(t *testing.T) {
	prod := new(scorecardsfakes.FakeStore)
	fix := new(scorecardsfakes.FakeFixtureStore)

	// Candidate v5 has NO production traffic: production column is dark
	// (all-zero), but the fixture tier still returns a real column.
	prod.ScorecardReturns(&scorecards.Scorecard{
		WorkflowName: "review-prompts",
		Columns:      []scorecards.VersionColumn{{Version: 4, Live: true}, {Version: 5}},
	}, nil)
	fix.FixtureScorecardReturns(&scorecards.FixtureScorecard{
		WorkflowName: "review-prompts", BenchAvailable: true, ExperimentID: 7,
		Columns: []scorecards.FixtureColumn{{Version: 4, Live: true}, {Version: 5, ContentHash: "hash-v5"}},
	}, nil)

	a := scorecards.NewPromotionAssembler(prod, fix)
	pv, err := a.Promotion("review-prompts", []int{4, 5}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if pv.Production == nil || len(pv.Production.Columns) != 2 {
		t.Fatalf("production tier missing: %+v", pv.Production)
	}
	if pv.Fixture == nil || len(pv.Fixture.Columns) != 2 {
		t.Fatalf("fixture tier missing: %+v", pv.Fixture)
	}
	// candidate v5 renders a real fixture column despite no prod traffic
	if pv.Fixture.Columns[1].ContentHash != "hash-v5" {
		t.Fatalf("candidate v5 fixture column not real: %+v", pv.Fixture.Columns[1])
	}
	// both stores called with the same versions
	_, gotVersions := prod.ScorecardArgsForCall(0)
	if len(gotVersions) != 2 {
		t.Fatalf("production called with %v", gotVersions)
	}
}

func TestAssembleProductionErrorPropagates(t *testing.T) {
	prod := new(scorecardsfakes.FakeStore)
	fix := new(scorecardsfakes.FakeFixtureStore)
	prod.ScorecardReturns(nil, errBoom)
	a := scorecards.NewPromotionAssembler(prod, fix)
	if _, err := a.Promotion("x", []int{1}, 0); err == nil {
		t.Fatal("production error must propagate")
	}
}

var errBoom = &boomErr{}

type boomErr struct{}

func (*boomErr) Error() string { return "boom" }
```

- [ ] **Step 2: Run test to verify it fails.** `go test ./agent/api/scorecards/` — Expected: build error (`NewPromotionAssembler` undefined).

- [ ] **Step 3: Write `agent/api/scorecards/promotion.go`:**

```go
package scorecards

// PromotionAssembler bundles the production tier (plan 13's retained Store,
// carrying F8) and the fixture tier into a PromotionView. It owns no DB —
// both stores are injected, so a candidate with no production traffic still
// gets a real fixture column because FixtureStore anchors it on the F8
// definition read, independent of Store.Scorecard's dark column.
type PromotionAssembler struct {
	production Store
	fixture    FixtureStore
}

func NewPromotionAssembler(production Store, fixture FixtureStore) *PromotionAssembler {
	return &PromotionAssembler{production: production, fixture: fixture}
}

// Promotion computes both tiers for the workflow's versions. The production
// error is fatal (it is the honest baseline); a fixture error is fatal too
// — the store itself absorbs the unscored case (BenchAvailable=false,
// identity-only columns), so any error here is a real DB fault.
func (a *PromotionAssembler) Promotion(workflowName string, versions []int, experimentID int) (*PromotionView, error) {
	prod, err := a.production.Scorecard(workflowName, versions)
	if err != nil {
		return nil, err
	}
	fix, err := a.fixture.FixtureScorecard(workflowName, versions, experimentID)
	if err != nil {
		return nil, err
	}
	return &PromotionView{WorkflowName: workflowName, Production: prod, Fixture: fix}, nil
}
```

- [ ] **Step 4: Run test to verify it passes.** `go test ./agent/api/scorecards/` — Expected: PASS.
- [ ] **Step 5: Commit.** `git add agent/api/scorecards/promotion.go agent/api/scorecards/promotion_test.go && git commit -m "feat(scorecards): promotion assembler bundling production (retained) + fixture tiers"`

---

### Task 8: HTTP handler for `GetAgentWorkflowPromotion`

The handler for `GET /api/v1/agent/workflows/:workflow_name/promotion?versions=4,5&experiment=7`. Parses the path param + `versions` (reuses `ParseVersionsCSV`) + optional `experiment`, calls the assembler, writes JSON. Follows the `agent/api/reviews`/plan-13 scorecard-handler idiom (no `atc/api/accessor` import; reads are viewer-tier so no `UserNameFunc` needed). Route registration is Task 9.

**Files:**
- Create: `agent/api/scorecards/promotion_handler.go`
- Test: `agent/api/scorecards/promotion_handler_test.go`

**Steps:**

- [ ] **Step 1: Write the failing test** `agent/api/scorecards/promotion_handler_test.go`:

```go
package scorecards_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/concourse/concourse/agent/api/scorecards"
	"github.com/concourse/concourse/agent/api/scorecards/scorecardsfakes"
)

func newPromotionHandler() (*scorecards.PromotionHandler, *scorecardsfakes.FakeStore, *scorecardsfakes.FakeFixtureStore) {
	prod := new(scorecardsfakes.FakeStore)
	fix := new(scorecardsfakes.FakeFixtureStore)
	prod.ScorecardReturns(&scorecards.Scorecard{WorkflowName: "review-prompts"}, nil)
	fix.FixtureScorecardReturns(&scorecards.FixtureScorecard{WorkflowName: "review-prompts", BenchAvailable: true}, nil)
	return scorecards.NewPromotionHandler(scorecards.NewPromotionAssembler(prod, fix)), prod, fix
}

func TestPromotionHandlerOK(t *testing.T) {
	h, _, fix := newPromotionHandler()
	req := httptest.NewRequest("GET", "/api/v1/agent/workflows/review-prompts/promotion?versions=4,5&experiment=7", nil)
	req.Form = map[string][]string{":workflow_name": {"review-prompts"}}
	rec := httptest.NewRecorder()
	h.GetPromotion(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body)
	}
	var pv scorecards.PromotionView
	if err := json.Unmarshal(rec.Body.Bytes(), &pv); err != nil {
		t.Fatal(err)
	}
	if pv.WorkflowName != "review-prompts" || pv.Production == nil || pv.Fixture == nil {
		t.Fatalf("bad body: %+v", pv)
	}
	// experiment=7 threaded to the fixture store
	_, _, gotExp := fix.FixtureScorecardArgsForCall(0)
	if gotExp != 7 {
		t.Fatalf("experiment not threaded: %d", gotExp)
	}
}

func TestPromotionHandlerRejectsMissingVersions(t *testing.T) {
	h, _, _ := newPromotionHandler()
	req := httptest.NewRequest("GET", "/x", nil) // no ?versions
	req.Form = map[string][]string{":workflow_name": {"review-prompts"}}
	rec := httptest.NewRecorder()
	h.GetPromotion(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("missing versions must 400, got %d", rec.Code)
	}
}

func TestPromotionHandlerDefaultsExperimentToZero(t *testing.T) {
	h, _, fix := newPromotionHandler()
	req := httptest.NewRequest("GET", "/x?versions=4,5", nil) // no ?experiment
	req.Form = map[string][]string{":workflow_name": {"review-prompts"}}
	h.GetPromotion(httptest.NewRecorder(), req)
	if _, _, gotExp := fix.FixtureScorecardArgsForCall(0); gotExp != 0 {
		t.Fatalf("absent experiment must default to 0 (auto-resolve), got %d", gotExp)
	}
}
```

- [ ] **Step 2: Run test to verify it fails.** `go test ./agent/api/scorecards/` — Expected: build error (`NewPromotionHandler`/`GetPromotion` undefined).

- [ ] **Step 3: Write `agent/api/scorecards/promotion_handler.go`:**

```go
package scorecards

import (
	"encoding/json"
	"net/http"
	"strconv"
)

// PromotionHandler serves GetAgentWorkflowPromotion. Auth is enforced by
// the wrappa tier (authorized viewer, §4.2); this handler only reads.
type PromotionHandler struct {
	assembler *PromotionAssembler
}

func NewPromotionHandler(a *PromotionAssembler) *PromotionHandler {
	return &PromotionHandler{assembler: a}
}

// GetPromotion handles GET /api/v1/agent/workflows/:workflow_name/promotion.
func (h *PromotionHandler) GetPromotion(w http.ResponseWriter, r *http.Request) {
	name := r.FormValue(":workflow_name")
	if name == "" {
		http.Error(w, "workflow_name is required", http.StatusBadRequest)
		return
	}
	versions, err := ParseVersionsCSV(r.URL.Query().Get("versions"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	experimentID := 0
	if raw := r.URL.Query().Get("experiment"); raw != "" {
		if experimentID, err = strconv.Atoi(raw); err != nil || experimentID < 0 {
			http.Error(w, "experiment must be a non-negative integer", http.StatusBadRequest)
			return
		}
	}
	pv, err := h.assembler.Promotion(name, versions, experimentID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(pv)
}
```

- [ ] **Step 4: Run test to verify it passes.** `go test ./agent/api/scorecards/` — Expected: PASS.
- [ ] **Step 5: Commit.** `git add agent/api/scorecards/promotion_handler.go agent/api/scorecards/promotion_handler_test.go && git commit -m "feat(scorecards): GetAgentWorkflowPromotion HTTP handler"`

---

### Task 9: Route registration — the four server-side touchpoints

Wire `GetAgentWorkflowPromotion` through the four **server-side** touchpoints of the #36 pattern (route name+entry, auth tier, handler map, role). All edits are **append-only** adjacent to the existing agent-workflow routes and plan 13's `GetAgentWorkflowScorecard` (search for those symbols — anchors shifted). The auth-wrappa `default: panic` at the end of the switch fails startup if the new route name is skipped, so this touchpoint is not optional. **Note:** the #36 pattern's fifth touchpoint (a `go-concourse` client method) does **not** apply here — the `fly agent workflows` family reaches the ATC via `agentAPIRequest`, not go-concourse (Task 10 idiom note), so there is no client method to add for this route. The sixth touchpoint (the fly verb) is Task 10.

**Files:**
- Modify: `atc/routes.go` (name constant near `GetAgentWorkflowVersion` @ :161–165; `Routes` entry near :337–341)
- Modify: `atc/wrappa/api_auth_wrappa.go` (add the name to the `CheckAgentAuthorizationHandler` team-less viewer case group @ :215–246, beside `GetAgentWorkflowScorecard`)
- Modify: `atc/api/handler.go` (add a `scorecardStore`/assembler construction + a route→handler map entry near the other agent handlers @ :366)
- Modify: `atc/api/accessor/roles.go` (`atc.GetAgentWorkflowPromotion: ViewerRole`, beside the other agent-workflow read roles)

**Steps:**

- [ ] **Step 1 (touchpoint 1 — route name + entry).** In `atc/routes.go`, add the constant beside `GetAgentWorkflowVersion`:

```go
	GetAgentWorkflowPromotion = "GetAgentWorkflowPromotion"
```

and the `Routes` slice entry beside the existing workflow routes (and plan 13's scorecard route):

```go
	{Path: "/api/v1/agent/workflows/:workflow_name/promotion", Method: "GET", Name: GetAgentWorkflowPromotion},
```

- [ ] **Step 2 (touchpoint 2 — auth tier).** In `atc/wrappa/api_auth_wrappa.go`, add `atc.GetAgentWorkflowPromotion` to the same `case` list as `atc.GetAgentWorkflowScorecard` inside the `CheckAgentAuthorizationHandler` group (team-less `/api/v1/agent/*` viewer tier, decision 21). Skipping it hits the `default: panic("you missed a spot: …")` at startup.

- [ ] **Step 3 (touchpoint 3 — handler wiring).** In `atc/api/handler.go`:
  - The `NewScorecardStore(dbConn)` construction **exists once plan 13 has landed** (plan 13 Task 6) — it is NOT on HEAD (see the Blocking prerequisite). Add the fixture store + assembler + handler beside it:

    ```go
    fixtureScorecardStore := db.NewFixtureScorecardStore(dbConn)
    promotionAssembler := scorecards.NewPromotionAssembler(scorecardStore, fixtureScorecardStore)
    promotionHandler := scorecards.NewPromotionHandler(promotionAssembler)
    ```

    (`scorecardStore` is plan 13's already-constructed `db.NewScorecardStore(dbConn)`; reuse it — do not construct a second one.)
  - Add the map entry beside `GetAgentWorkflowScorecard`:

    ```go
    atc.GetAgentWorkflowPromotion: http.HandlerFunc(promotionHandler.GetPromotion),
    ```

- [ ] **Step 4 (touchpoint 4 — role).** In `atc/api/accessor/roles.go`, add beside the other agent-workflow reads:

```go
	atc.GetAgentWorkflowPromotion: ViewerRole,
```

- [ ] **Step 5: Verify wiring.** `go build ./...` (a missed auth-switch case panics only at runtime, but the build must be clean first) and `ginkgo ./atc/wrappa/` (the wrappa suite constructs the wrappa and would panic on a missing case). Then the API suite: `go test ./atc/api/ 2>&1 | tail -5` (if `atc/api/api_suite_test.go` enumerates routes, it fails on an unrouted name — confirm green).
- [ ] **Step 6: Commit.** `git add atc/routes.go atc/wrappa/api_auth_wrappa.go atc/api/handler.go atc/api/accessor/roles.go && git commit -m "feat(scorecards): register GetAgentWorkflowPromotion route (viewer tier, four server-side touchpoints)"`

---

### Task 10: fly verb `fly agent workflows promotion` + two-tier table rendering

Append a `promotion` subcommand to the existing `AgentWorkflowsCommand` (`fly/commands/agent_workflows.go`, beside `list`/`show`/`import`/`set-live`) — the read sibling that *informs* the `set-live` write. It renders **both tiers** as `fly/ui.Table`s: the production tier (dark `—` cells for a candidate with no traffic) and the fixture tier (per-metric rows with `mean` and the paired `Δ (n=k)` against the baseline, plus a `control: PASS|FAIL(evaluator-suspect)` banner). The **basic experience** is one arg — when `--versions` is omitted the command fills `{live, latest}`, so `fly agent workflows promotion review-prompts` answers the promotion question with zero flags.

> **Idiom (verified at HEAD — no go-concourse touchpoint for this family).** Every `fly agent workflows` subcommand (`list`/`show`/`import`/`set-live`) reaches the ATC through the file-local `loadAgentTarget()` + `agentAPIRequest(target, method, path, body)` + `decodeOrError(resp, &out)` helpers (`fly/commands/agent_workflows.go:39,67`), **NOT** a `go-concourse` `Client` method — there is **no** `go-concourse/concourse/agent_workflows.go` and no `AgentWorkflow*` client method exists (`grep AgentWorkflow go-concourse/` is empty). So this promotion verb follows the SAME idiom: it hits `GET /api/v1/agent/workflows/<name>/promotion?...` via `agentAPIRequest`, and resolves the `{live, latest}` default by reusing the `.../versions` fetch `show` already uses (`agent_workflows.go:140`, decoding `[]workflow.Definition`). **There is no touchpoint-5 (go-concourse client) for this route** — the six-touchpoint pattern's client method belongs to the `fly agent bench`/`tickets`/`costs` families that DO use go-concourse, not to the `workflows` family. The route name (`GetAgentWorkflowPromotion`) exists after Task 9; there is no corresponding client binding, and none is needed.

**Files:**
- Modify: `fly/commands/agent_workflows.go` (add `Promotion WorkflowsPromotionCommand` field to `AgentWorkflowsCommand` @ :22–27; add the command struct + `Execute` + the pure `defaultPromotionVersions`/`renderProductionTier`/`renderFixtureTier` helpers; add imports `strings` + `agent/api/scorecards`)
- Test: `fly/commands/agent_workflows_promotion_test.go` (plain unit test for `defaultPromotionVersions`)
- Test: `fly/integration/agent_workflows_promotion_test.go` (mock-ATC integration idiom, `make test-fly-integration`)

**Steps:**

- [ ] **Step 1: Write the failing integration test** `fly/integration/agent_workflows_promotion_test.go`, mirroring `fly/integration/agent_workflows_test.go` exactly (globals `atcServer`, `flyPath`, `targetName`; `ghttp` + `gexec` + `gbytes`). Concrete body:

```go
package integration_test

import (
	"net/http"
	"os/exec"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/concourse/concourse/agent/api/scorecards"
	"github.com/onsi/gomega/gbytes"
	"github.com/onsi/gomega/gexec"
	"github.com/onsi/gomega/ghttp"
)

var _ = Describe("fly agent workflows promotion", func() {
	fp := func(f float64) *float64 { return &f }
	ip := func(i int) *int { return &i }

	BeforeEach(func() {
		atcServer.AppendHandlers(
			ghttp.CombineHandlers(
				// explicit --versions 4,5 -> no default /versions fetch; one call
				ghttp.VerifyRequest("GET", "/api/v1/agent/workflows/review-prompts/promotion", "versions=4,5"),
				ghttp.RespondWithJSONEncoded(http.StatusOK, scorecards.PromotionView{
					WorkflowName: "review-prompts",
					Production: &scorecards.Scorecard{ // v5 is dark (no production traffic) — fields per plan 13
						WorkflowName: "review-prompts",
						Columns:      []scorecards.VersionColumn{{Version: 4, Live: true}, {Version: 5}},
					},
					Fixture: &scorecards.FixtureScorecard{
						WorkflowName: "review-prompts", BenchAvailable: true, ExperimentID: 7,
						BaselineVersion: 4, ControlStatus: "pass",
						EvaluatorName: "review-judge", EvaluatorVersion: 3,
						Columns: []scorecards.FixtureColumn{
							{Version: 4, Live: true, ContentHash: "hash-v4", IsBaseline: true,
								Metrics: []scorecards.FixtureMetric{{Name: "precision", Mean: 0.80, SampleCount: 24}}},
							{Version: 5, ContentHash: "hash-v5",
								Metrics: []scorecards.FixtureMetric{{Name: "precision", Mean: 0.90, SampleCount: 18,
									PairedDelta: fp(0.10), PairedCount: ip(18)}}},
						},
					},
				}),
			),
		)
	})

	It("renders both tiers (production dark cell + fixture paired delta + control banner)", func() {
		flyCmd := exec.Command(flyPath, "-t", targetName, "agent", "workflows", "promotion", "review-prompts", "--versions", "4,5")
		sess, err := gexec.Start(flyCmd, GinkgoWriter, GinkgoWriter)
		Expect(err).NotTo(HaveOccurred())
		<-sess.Exited
		Expect(sess.ExitCode()).To(Equal(0))
		Expect(sess.Out).To(gbytes.Say("precision"))
		Expect(sess.Out).To(gbytes.Say(`\+0.10`))
		Expect(sess.Out).To(gbytes.Say(`n=18`))
		Expect(sess.Out).To(gbytes.Say("—"))          // dark production cell for v5
		Expect(sess.Out).To(gbytes.Say(`control: PASS`))
	})
})
```

  Also write the unit test `fly/commands/agent_workflows_promotion_test.go` for the default helper (a plain table test): `defaultPromotionVersions([]workflow.Definition{{Version:4,Live:true},{Version:5}})` → `"4,5"`; all-non-live `{{Version:5}}` → `"5"`; live==latest → just that one version (deduped).

- [ ] **Step 2: Run to verify it fails.** `make test-fly-integration` (or `ginkgo ./fly/integration/ --focus="promotion"`) — Expected: FAIL (unknown command `promotion`).

- [ ] **Step 3: Add the command** to `fly/commands/agent_workflows.go` — using the **family idiom** (`loadAgentTarget` + `agentAPIRequest` + `decodeOrError`), NOT a go-concourse client method (see the idiom note above):

```go
type AgentWorkflowsCommand struct {
	List      WorkflowsListCommand      `command:"list" ...`
	Show      WorkflowsShowCommand      `command:"show" ...`
	Import    WorkflowsImportCommand    `command:"import" ...`
	SetLive   WorkflowsSetLiveCommand   `command:"set-live" ...`
	Promotion WorkflowsPromotionCommand `command:"promotion" description:"Two-tier scorecard (production + fixture) to inform set-live"`
}

type WorkflowsPromotionCommand struct {
	Versions   string `long:"versions" description:"Comma-separated versions (default: live + latest)"`
	Experiment int    `long:"experiment" description:"Pin the source experiment (default: newest scoring these versions)"`
	Args       struct {
		Workflow string `positional-arg-name:"workflow" required:"true"`
	} `positional-args:"yes"`
}

func (c *WorkflowsPromotionCommand) Execute([]string) error {
	target, err := loadAgentTarget()
	if err != nil {
		return err
	}
	name := url.PathEscape(c.Args.Workflow)

	versions := c.Versions
	if versions == "" {
		// Basic experience: default to {live, latest}. Reuse the SAME
		// .../versions fetch `show` uses (agent_workflows.go:140) and its
		// latest-unless-live selection — no new route, no client method.
		resp, err := agentAPIRequest(target, "GET", "/api/v1/agent/workflows/"+name+"/versions", nil)
		if err != nil {
			return err
		}
		var defs []workflow.Definition
		if err := decodeOrError(resp, &defs); err != nil {
			return err
		}
		versions = defaultPromotionVersions(defs) // "live,latest" helper (unit-tested)
	}

	path := "/api/v1/agent/workflows/" + name + "/promotion?versions=" + url.QueryEscape(versions)
	if c.Experiment > 0 {
		path += "&experiment=" + strconv.Itoa(c.Experiment)
	}
	resp, err := agentAPIRequest(target, "GET", path, nil)
	if err != nil {
		return err
	}
	var pv scorecards.PromotionView
	if err := decodeOrError(resp, &pv); err != nil {
		return err
	}
	renderProductionTier(pv.Production) // fly/ui.Table, dark "—" cells (reuse plan 13's renderer if exported)
	if pv.Fixture != nil {              // omitted only on a real server fault; store returns identity-only otherwise
		renderFixtureTier(pv.Fixture)   // per-metric mean + "Δ +0.10 (n=18)", control banner
	}
	return nil
}

// defaultPromotionVersions returns the {live, latest} default as a
// comma-separated, de-duped, ascending version list — mirroring `show`'s
// latest-unless-live selection (agent_workflows.go:150-163). "latest" alone
// when none is live; "live,latest" otherwise.
func defaultPromotionVersions(defs []workflow.Definition) string {
	latest, live := 0, 0
	for _, d := range defs {
		if d.Version > latest {
			latest = d.Version
		}
		if d.Live {
			live = d.Version
		}
	}
	set := []int{}
	if live > 0 && live != latest {
		set = append(set, live)
	}
	if latest > 0 {
		set = append(set, latest)
	}
	sort.Ints(set)
	parts := make([]string, len(set))
	for i, v := range set {
		parts[i] = strconv.Itoa(v)
	}
	return strings.Join(parts, ",")
}
```

  Keep `renderProductionTier`/`renderFixtureTier` small and pure (`fly/ui.Table`). Reuse plan 13's production-tier table renderer if it is exported; otherwise render production from `PromotionView.Production` with the same `—`-for-nil convention. Rendering is covered by the integration test; `defaultPromotionVersions` by the unit test.

- [ ] **Step 4: Run to verify it passes.** `make test-fly-integration` — Expected: PASS. The mock ATC version must match `versions.go` (CLAUDE.md note).
- [ ] **Step 5: Commit.** `git add fly/commands fly/integration && git commit -m "feat(fly): agent workflows promotion — two-tier scorecard render (basic one-arg path)"`

---

### Task 11: Covering-index decision + whole-suite verification pass

Two things: (a) decide the `agent_bench_cells (variant, variant_version)` index (default: **do not add one** — keep C at zero migrations; and if added, it needs A2's sign-off per the Task 1 addendum since it indexes A2's frozen table), and (b) run the full per-merge gate.

**Files:**
- (Conditional, default NOT created) `atc/db/migration/migrations/1773106111_add_bench_cells_variant_index.{up,down}.sql`
- Modify (only if the index is added): `atc/db/migration/legacy_upgrade_test.go` (`jetbridgeHeadMigration`)

> **`jetbridgeHeadMigration` caveat (finding).** At HEAD the const is `jetbridgeHeadMigration = 1773106090` (`legacy_upgrade_test.go:37`) while the real on-disk head is already `1773106091` (`create_agent_settings`) — the const **lags head by one** (a pre-existing repo inconsistency, not C's). If the conditional `1773106111` index is ever added, do NOT merely `+1` the stale const: set it to the **true on-disk head at execution time** (after plan 13's `1773106110`, the bench block `100–102`, and this `1773106111` have all landed), and reconcile the legacy-upgrade expectation with the `1773106091` slot the const never reflected.

**Steps:**

- [ ] **Step 1: Decide the index.** The only unindexed access is `resolveExperiment`'s `EXISTS (… c.variant = $1 AND c.variant_version = ANY($2) …)` (Task 3). At current scale (dozens of experiments, hundreds of cells) this is an EXISTS over the experiment-indexed cell set and is cheap. **Default: add no index** — this keeps the skeleton's "C claims no bench migration" true and avoids touching A2's table. Add the index **only** if theborg slow-query logs show `resolveExperiment` hot, and then as `1773106111` (plan 13's reserved `1773106110–19` block — NOT a bench number `100/101/102`):

  ```sql
  -- up: CREATE INDEX agent_bench_cells_variant ON agent_bench_cells (variant, variant_version);
  -- down: DROP INDEX agent_bench_cells_variant;
  ```

  Prefer routing the explicit-`experiment_id` path (already indexed by `agent_bench_cells_experiment`) from the fly verb and web S-track, which sidesteps the variant scan entirely — the promotion view for a specific bake-off always knows its experiment id.

- [ ] **Step 2: Run the merge gate.** `pg_isready && make test-quick` (unit + ci-agent, PostgreSQL) — Expected: green. **Never pass `--race`** (parallel-compilation failures, CLAUDE.md). If the index was added, also `ginkgo --focus='Legacy Database Upgrade' ./atc/db/migration/`.
- [ ] **Step 3: Whole-build + fly integration.** `go build ./...` and `make test-fly-integration` — Expected: green.
- [ ] **Step 4: Elm sanity (no-op check).** This plan ships **no Elm** (bench web UI is S-track, spec §6). Confirm the tree still compiles if any shared Elm file was touched incidentally: `cd web/elm && npx elm make src/Main.elm --output=/dev/null; cd ../..` — Expected: green (it will be, since no Elm file is edited).
- [ ] **Step 5: Commit** (only if the conditional index was added): `git add atc/db/migration && git commit -m "perf(db): agent_bench_cells (variant, variant_version) covering index (migration 1773106111, plan-13 reserved block)"`.

---

## Execution notes

**Test tiers (PostgreSQL must be running — `pg_isready`):**
- Domain + assembler + handler: `go test ./agent/api/scorecards/` (plain `testing`, matching plan 13).
- Fixture-tier DB store: `ginkgo --focus="FixtureScorecardStore" ./atc/db/` (template DB, migrated to HEAD — so A2's `1773106101` + B's `1773106102` bench tables exist for every spec; the store assumes they exist and has no `to_regclass` guard). The "no experiment scored these versions" identity-only path (`expID == 0`, `BenchAvailable=false`) is exercised by its own spec (Task 3) against the real tables — there is **no** untestable "tables absent" branch.
- Auth wiring: `ginkgo ./atc/wrappa/` (the `default: panic` guard).
- fly: `make test-fly-integration` (mock ATC; version must match `versions.go`). No go-concourse client suite — this route is driven by `agentAPIRequest`, not a `Client` method (Task 10).
- Per-merge gate: `make test-quick` (unit + ci-agent). **Never `--race`.** The `atc/db` suite uses `testdb_template`; if you see `database "testdb_template" already exists`, another process is live — wait, per CLAUDE.md.

**Merge order (two gates).** (1) **plan 13 MUST land first** — C imports `agent/api/scorecards`, reuses `db.NewScorecardStore`, and registers a route beside `GetAgentWorkflowScorecard`, none of which exist at HEAD; C does not compile until 13 is on the branch (see the Blocking prerequisite + Anchor caveat). (2) **After the bench block** — C reads B's `agent_bench_scores` (`1773106102`), so it merges after the bench block lands (`100 → 101 → 102`, ascending, one migration per push — skeleton §4). C adds **no migration** (default), so it introduces no ordering hazard of its own; the only cross-branch collision surface is the append-only route/handler edits (Task 9), which are mechanical re-adds on conflict.

**Unscored versions are tolerated by construction (but the bench tables are NOT optional).** The bench tables are a HARD same-wave dependency C merges strictly after (`102`), and Concourse migrates before it serves — so `agent_bench_scores`/`_cells`/`_experiments` always exist when C's code runs. C therefore uses **no `to_regclass` guard** (unlike plan 13's `to_regclass('agent_outcomes')`, which guards a *different, optional* track's table — a genuine cross-track runtime dependency, not a same-wave merge-ordered one). What C *does* tolerate is the reachable case where **no experiment has scored the requested versions yet**: the store returns identity-only columns (F8) with `BenchAvailable=false` and empty metrics, so the promotion view renders production-tier + an identity-only fixture panel — honest, never a 500 — and lights up automatically once a bake-off scores the candidate. (This is why the earlier "C can land before A2/B deploy" framing was dropped: the merge order makes the tables present, so that state is unreachable; the honest tolerance is unscored-versions, which IS tested — Task 3.)

**Live-test requirements: none.** C is read-only over existing + bench tables + API/fly. No jetbridge pod, no cluster, no theborg run. Validation against real bench data is manual/observational after A2/B accrue scores (run `fly agent workflows promotion <workflow>` for a workflow with a completed experiment). The bench's own round-trip self-test and negative-control calibration (spec §Testing) are A/B's canaries, not C's.

**Rollback.** The route wiring (Task 9) is a handful of append-only shared-file edits across four files — revert that one commit and `agent/api/scorecards`'s new files + `atc/db` fixture store remain compilable and simply unrouted. The fly verb (Task 10) is one appended subcommand field — remove it and the workflows command is unchanged. No migration to roll back (default).

---

## Scope-out (explicit)

- **Bench web UI / two-tier Elm promotion page** — S-track (spec §6/§Out-of-scope: "API + fly first"). This plan delivers the API `PromotionView` + `fly agent workflows promotion` and hands the Elm surface to **S5 (web-loop-closure)**. Plan 13's production-tier Elm page (`Scorecards/Scorecards.elm`, its Tasks 7–9) is RETAINED unchanged; C adds no Elm.
- **`agent_bench_scores` writes / evaluators / fault-injection corpus** — B. C reads scores; it never writes one.
- **Capture, replay, experiment harness** — A1/A2. C reads `agent_bench_experiments`/`agent_bench_cells`; it creates no experiment and no cell.
- **Auto-promotion / any promotion gate** — never (spec §Out-of-scope). The promotion view *informs* the existing `PromoteAgentWorkflowVersion` set-live write; it never triggers it, and no threshold is derived from either tier (plan 13 charter scope-out carried forward).
- **Multi-evaluator per-column split** — a later slice. v1 pins the single dominant evaluator per experiment and reports its numbers only (metric/delta queries filter by the pin); a `MultipleEvaluators` boolean flags the >1 case honestly without ever averaging across evaluators (Task 6).
- **Implementor-variance plan evaluator** — a declared later slice (spec §4); its scores would land in `agent_bench_scores` like any other and flow through this rollup unchanged.

## Open decisions

1. **Metric polarity.** The store reports **signed** paired deltas (candidate − baseline); the reader interprets sign per metric (precision↑ vs cost_usd↓). A per-metric polarity hint (so the UI can color +/− as good/bad) is deferred — it would ride the evaluator/step-kind registry, not C. *Recommended: keep signed-only in v1; revisit when the Elm page (S5) needs coloring.*
2. **Source-experiment selection.** C auto-resolves the **newest** experiment scoring the requested versions, or takes an explicit `?experiment=`. Alternative: aggregate across **all** experiments that scored a version (more samples, but breaks the single-experiment pairing guarantee). *Recommended: newest-single-experiment (pairing is the whole point); revisit if fixtures fragment across many small experiments.*
3. **`agent_bench_cells (variant, variant_version)` index** (Task 11) — default none; add `1773106111` in plan 13's reserved block only if `resolveExperiment` shows hot. *Open until theborg slow-query data exists.*
4. **Naming (S-8) + route/verb duplication.** `GetAgentWorkflowPromotion` / `fly agent workflows promotion` / `/api/v1/agent/workflows/:workflow_name/promotion` are **provisional** — they sit next to plan 13's `scorecard` route because C amends 13, but S-8 may relocate the whole two-tier surface under a `/bench/` namespace. Additionally flag for S-8: the promotion route/verb is a **strict superset** of plan 13's retained (production-only) `GetAgentWorkflowScorecard` route/verb — both read endpoints and both fly verbs coexist, with `promotion` returning everything `scorecard` does plus the fixture tier. S-8 should decide whether both surfaces persist or whether the existing `scorecard` route/verb should instead gain an optional fixture tier (e.g. always-both, or a `?tier=` flag) so there is **one** scorecard surface that reveals the fixture tier, rather than a parallel route + verb (+ future Elm page) to maintain. The `PromotionView` wrapper type itself is spec-sanctioned (§7 "both tiers side-by-side"); only the duplicated route+verb surface is the open cost. All names here are subject to the coordinated pre-freeze rename (spec open item 9 / skeleton Q11).
5. **`evaluator-suspect` presentation.** C annotates the column set (`EvaluatorSuspect=true`) and still returns every number (principle 5 — never suppress). Whether the fly renderer should additionally **grey out** the paired deltas of a suspect experiment (vs just banner it) is a rendering choice deferred to Task 10's renderer / S5. *Recommended: banner + keep deltas visible; suppression hides evidence.*
6. **Holdout-confirmation surface (added 2026-07-19 post-review).** The pre-promotion confirmation recipe is a `fixtures: {split: holdout}` full-form experiment (A2's execution notes). The PromotionView should eventually surface, beside the two tiers, "the latest holdout-split experiment for these versions — or `no holdout confirmation run`" so a promoter sees at a glance whether the candidate was confirmed on unseen fixtures. Deferred as a follow-up (recorded in the bench README's still-open list); v1 renders the two tiers only.

## Basic-experience guardrail

The spec's simplicity contract (§6, principle 6: *revealed, not upfront, complexity*) survives intact. The common case is **one argument**:

```
$ fly agent workflows promotion review-prompts
  → versions default to {live, latest}; experiment auto-resolves to the newest bake-off;
    two tables print — production tier (dark "—" where the candidate has no traffic)
    and fixture tier (precision/recall/cost/turns, each with its paired Δ and control: PASS).
```

No version list, no experiment id, no metric selection is required — the store resolves the baseline (the live version, via the F8 read), resolves the source experiment (newest scoring the versions), and computes every metric's distribution and paired delta. The heavier machinery — explicit `--versions`, pinning `--experiment`, per-metric drill-down, the negative-control detail, multi-evaluator splits — is all **present behind flags and the response body**, never in the way of the one-arg question "should I promote this candidate?" The two-tier answer is computed for free on that one call; the user reveals the rest only by asking.

---

## §11. Amendments

*(append-only; supersede by entry, not by edit — mirrors plan 13's convention)*
