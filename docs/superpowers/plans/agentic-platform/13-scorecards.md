# Workflow Scorecards and Run Metrics Surfaces Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Compare workflow-definition versions side-by-side on objective and subjective metrics (gate pass rate, cost/ticket, turns, findings/ticket, judge scores, human verdict distributions, merge outcomes), and answer "where did the turns go" for any run — as read-only rollups over existing tables, with counts shown alongside every rate.

**Architecture:** Scorecards writes **no domain tables** — it aggregates the rows five upstream workstreams already produce (`agent_run_metrics`, `agent_cost_ledger`, `agent_reviews`/`agent_feedback`, `agent_outcomes`, `agent_workflow_definitions`). A new `agent/api/scorecards` package holds the aggregate types and a `Store` interface; `atc/db.NewScorecardStore` implements it with squirrel/SQL over those tables keyed by `(workflow_name, workflow_version)`, LEFT-JOINing `agent_outcomes` so outcome columns render dark until delivery-outcomes' watcher fills them. One `GetAgentWorkflowScorecard` HTTP route feeds a new Elm scorecard-comparison page; a second Elm surface — the per-step "where did the turns go" metrics panel — is wired onto the existing ticket page over agent-step's already-registered `ListAgentRunMetrics` route. One index-only migration adds the `(workflow_version, day)` covering indexes the charter mandates from day one.

**Tech Stack:** Go (`agent/api/scorecards` plain-`testing` package matching `agent/api/reviews`; `atc/db` Ginkgo/Gomega factory over the template DB; `atc/wrappa` Ginkgo), PostgreSQL (index migration `1773106110`, aggregate SQL with `FILTER (WHERE …)` and `LEFT JOIN`), counterfeiter fakes, Elm 0.19 (`Scorecards/Scorecards.elm`, `Concourse/Scorecard.elm`, `Concourse/AgentMetrics.elm`, plus wiring in `Routes`/`SubPage`/`Message.*`/`Api.Endpoints`).

---

## Context

**Charter (workstreams.json id `scorecards`, size M, wave 4, depends_on: agent-step, harvest-step, workflow-store, credentials-and-budgets).** Scope-in → task mapping (every item maps to at least one task):

| scope_in item | Tasks |
|---|---|
| Scorecard rollup queries + API + Elm view over `agent_run_metrics`/`agent_cost_ledger`/judge scores/gate results keyed by workflow-definition version — gate pass rate, cost per ticket, turns, findings per ticket, judge scores, human verdict distributions, **with counts alongside rates** | 2 (types), 3 (metrics-side query), 4 (findings/judge/verdict query), 6 (API), 9 (Elm view) |
| Outcome-derived columns (merge rate, merged-untouched, sent-back, time-to-merge, human-touch delta) via NULLABLE joins onto `agent_outcomes`, filling as the same-wave watcher lands (schema agreed at wave start) | 1 (wave-start sign-off addendum), 5 (LEFT-JOIN outcome query), 9 (Elm — labels "lines") |
| Run/ticket page per-step metrics panel ("where did the turns go"), including gateway cross-provider call metering | 7 (Elm decoder over `event_counts`), 8 (Elm panel on ticket page) |
| ok/failed/error taxonomy enforced end-to-end in every rollup (agent-did-badly vs platform-broke) | 2 (types carry ok/failed/error counts), 3 (SQL `FILTER` by `status`), 9 (Elm renders the three-way split) |
| Indexes by (workflow_version, day) from day one | 1 (allocates the block in the contract), 10 (migration `1773106110`) |

**Scope OUT (do not implement):** metrics ingestion (agent-step owns `SubmitAgentRunMetrics`); outcome facts (delivery-outcomes writes `agent_outcomes`); experiments/benchmarks and automated analysis (process-intel-experiments); **promotion gates (none, ever — scorecards inform, humans decide)**. This plan never writes a domain table, never mutates a ticket, and adds no RunnableComponent.

**Prior waves (assumed LANDED exactly as 00-shared-contracts.md + the earlier plans' §11 addenda define — do NOT re-implement):**

- **agent-step** (wave 2): `agent/schema` nested module — `schema.RunMetrics` (§2.4: fields `TicketID *int`, `PipelineRunID *int`, `BuildID int`, `PlanID`, `StepName`, `WorkflowName`, `WorkflowVersion *int`, `WorkflowHash`, `Status string` = `ok|failed|error`, `Summary`, `Model`, `Usage schema.Usage`, `Turns int`, `WallTimeSeconds int`, `CostUSD float64`, `Results json.RawMessage`, `EventsArtifact string`, `EventCounts map[string]int`, `CreatedAt int64`), constants `schema.RunStatusOK/RunStatusFailed/RunStatusError`; `agent/api/metrics.Store` with `ListByTicket(ticketID int) ([]schema.RunMetrics, error)` + `GetByBuild`; `atc/db.NewAgentRunMetricsFactory(dbConn)` over table `agent_run_metrics` (§1.8; indexes `agent_run_metrics_build_plan`, `agent_run_metrics_ticket`, `agent_run_metrics_workflow`); the **already-registered** `ListAgentRunMetrics` route `GET /api/v1/agent/tickets/:ticket_id/metrics` (ViewerRole, on `CheckAgentAuthorizationHandler`) returning a JSON array of `schema.RunMetrics` (agent-step plan Task 9). **The UI consuming it is unbuilt — this plan builds it.**
- **harvest-step** (wave 3): `agent_reviews`/`agent_feedback` ticket linkage columns (`agent_reviews.ticket_id`, `agent_reviews.pipeline_run_id`, `agent_feedback.ticket_id`; migration `1773106080`, §1.10); judge findings written into `agent_reviews.review` as `observations` with `category: "judge"`, gate failures as `proven_issues` with `category: "gate"`; feedback on judge findings submitted with `finding_type: "judge"` (§6.4.1); the six-verdict feedback taxonomy in `agent_feedback.verdict` (`accurate, false_positive, noisy, overly_strict, partially_correct, missed_context`). `agent_reviews` denormalized columns `score`, `max_score`, `pass`, `proven_count`, `observation_count` (existing, `atc/db/agent_reviews_factory.go`). The judge total is the `agent_reviews.score`/`max_score` for `ticket_id`-linked rows (§6.4.1 evidence payload `score.value` = judge total when the judge ran).
- **workflow-store** (wave 1): `agent_workflow_definitions` (§1.6; columns `name`, `version`, `content_hash`, `live`, `description`, `promoted_at`, unique `(name, version)`); `agent/workflow.Store` + `atc/db.NewAgentWorkflowDefinitionsFactory`; the `Live(name)` / `Versions(name)` reads. Scorecards uses the workflow name/version as its comparison key; it does not parse definition YAML.
- **credentials-and-budgets** (wave 1): `agent_cost_ledger` (§1.4; columns incl. `ticket_id`, `pipeline_run_id`, `source`, `cost_usd`, `turns`, `metadata JSONB`); `budget.RollupRow{Key, Entries, InputTokens, OutputTokens, Turns, CostUSD}`; the `GetAgentCostRollup` route `GET /api/v1/agent/costs?group_by=user|ticket|day|workflow`. **Workflow attribution rides `agent_cost_ledger.metadata->>'workflow'` formatted `"<name>@<version>"`** (credentials-and-budgets addendum, 2026-07-08) — writers that know their workflow (agent-step ingest, gateway metering) set it. Scorecards reads cost per ticket from `agent_cost_ledger` joined by `ticket_id` rather than by that metadata key (see Task 3 rationale).
- **agent-identity** (wave 1): `auth.CheckAgentAuthorizationHandler(handler, rejector)` case group giving team-less `/api/v1/agent/*` routes real main-team viewer/member authorization (contracts decision 21); `accessor/roles.go`'s `DefaultRoles` map is effective for these routes. Scorecards' one route rides this tier.
- **delivery-outcomes** (wave-mate, parallel — see below): `agent_outcomes` (§1.11 + §1.11.1 additive deltas) keyed uniquely on `ticket_id`, columns `merge_state ∈ {open,merged,merged_with_fixes,closed_unmerged}`, `merged_at`, `human_commit_count`, `human_lines_added`, `human_lines_deleted`, `disposition ∈ {'',sent_back,abandoned}`, `base_sha`. **Delta unit is LINES** (numstat of non-`concourse-agent[bot]` first-parent commits, §1.11.1).

**Wave-mates (parallel, NOT landed — coordinate, do not depend on files):**
- **delivery-outcomes** produces `agent_outcomes`. Its Task 1 writes contract §1.11.1 — the "agreed at wave start" join contract (LEFT JOIN on `ticket_id`, delta = lines). Scorecards' Task 1 records the reciprocal sign-off so the agreement is bilateral in the amendment log, and scorecards' outcome query (Task 5) is written to tolerate the table being absent at deploy time (returns dark columns) so neither workstream blocks the other. **Migration ordering hazard:** `agent_outcomes` is `1773106090` (delivery-outcomes) and scorecards' index migration is `1773106110`. The scorecard index migration touches only `agent_run_metrics` and `agent_reviews` (never `agent_outcomes`), so the two never collide; but the scorecard **query** referencing `agent_outcomes` will fail at runtime if deployed before `1773106090`. Task 5 guards this (see step: existence check + `--agent-scorecard-outcomes` implicit tolerance).
- **dispatch** shares only additive edits in `atc/routes.go` / `atc/wrappa/api_auth_wrappa.go` / `atc/api/accessor/roles.go` / `atc/api/handler.go` / `atc/atccmd/command.go` — higher migration number wins on merge; route/handler edits are append-only. No shared logic.

**This plan PRODUCES (contract surface `scorecard-rollup-api`, §9 index → §4.2 `GetAgentWorkflowScorecard`, over §1.8/§1.4/§1.10/§1.11 rows):**
- `agent/api/scorecards` — `Scorecard`, `VersionColumn`, `VerdictDistribution`, `Store` interface (Task 2).
- `atc/db.NewScorecardStore(dbConn)` implementing the aggregate SQL (Tasks 3–5).
- Route `GetAgentWorkflowScorecard` `GET /api/v1/agent/workflows/:workflow_name/scorecard?versions=3,4` (authorized viewer) (Task 6).
- Index migration `1773106110` — `(workflow_version, day)` covering indexes on `agent_run_metrics` and `agent_reviews` (Task 10, block allocated in Task 1).
- Elm `Scorecards/Scorecards.elm` scorecard-comparison page + the per-step metrics panel on the ticket page (Tasks 7–9).

**This plan CONSUMES:**
- §1.8 / §2.4 (agent-step) — `agent_run_metrics` rows + `schema.RunMetrics` shape + `ListAgentRunMetrics` route.
- §1.10 / §6.4.1 (harvest-step) — `agent_reviews` denormalized score/pass/counts + `agent_feedback` verdicts, ticket linkage, judge/gate `category`.
- §1.6 (workflow-store) — `agent_workflow_definitions` name/version key.
- §1.4 (credentials-and-budgets) — `agent_cost_ledger` cost/turns by ticket.
- §1.11 / §1.11.1 (delivery-outcomes) — `agent_outcomes` merge/delta columns via LEFT JOIN.
- §4.1 / decision 21 (agent-identity) — `CheckAgentAuthorizationHandler` for the scorecard route.

**Anchor caveat:** `Modify:` line anchors were verified on branch `jetbridge` at head `fb1c54fac2` (pre-wave-1). Three prior waves and two wave-mates will have shifted every anchor in `atc/routes.go`, `atc/wrappa/api_auth_wrappa.go`, `atc/api/accessor/roles.go`, `atc/api/handler.go`, `atc/atccmd/command.go`, and the Elm `Message.*`/`Api.Endpoints`/`Routes`/`SubPage` files — treat each anchor as "the location of the quoted code" and place additions adjacent to the named existing agent-route/agent-page code, not at a literal line.

---

### Task 1: Wave-start contract addendum — scorecard migration block allocation + reciprocal outcomes sign-off

The charter mandates "Indexes by (workflow_version, day) from day one" but §1.1's migration-block table (1773106010–1773106109) allocates **no block to scorecards** — a genuine gap, since scorecards was expected to add no tables. It needs one index-only migration number. It also must record the reciprocal half of the "agreed at wave start" `agent_outcomes` join contract that delivery-outcomes' Task 1 writes as §1.11.1. Both go in the §11 amendment log now, before any code, where the delivery-outcomes and process-intel-experiments planners read.

**Files:**
- Modify: `docs/superpowers/plans/agentic-platform/00-shared-contracts.md` (append a row to the §1.1 block table at :42 region; append to the §11 Amendment log at end of file)

**Steps:**

- [ ] **Step 1: Add the scorecards migration block to the §1.1 table.** In `docs/superpowers/plans/agentic-platform/00-shared-contracts.md`, after the `process-intel-experiments` row of the "Wave/number allocation" table (the `1773106100–109` row), add:

```markdown
| 1773106110–19 | scorecards | index-only: `(workflow_version, day)` covering indexes on `agent_run_metrics` + `agent_reviews` |
```

- [ ] **Step 2: Append the amendment-log entry.** At the end of `## 11. Amendment log`, append:

```markdown
- 2026-07-08 (scorecards planning): allocated migration block 1773106110–19 to scorecards (previously unallocated — §1.1 gave it no block because it adds no domain tables; it needs one index-only migration `1773106110` for the charter-mandated `(workflow_version, day)` covering indexes on `agent_run_metrics` and `agent_reviews`, additive-only, no schema change). Recorded the reciprocal sign-off on delivery-outcomes' §1.11.1 `agent_outcomes` wave-start join contract: scorecards joins `agent_outcomes` LEFT (unique `ticket_id`), reads `merge_state`/`merged_at`/`human_commit_count`/`human_lines_added`/`human_lines_deleted`/`disposition` as written, treats every one as nullable (dark until the same-wave watcher fills them), labels the delta columns "lines", and never writes or requires the table (queries degrade to dark outcome columns when `agent_outcomes` is absent at deploy time). Human verdict distributions are read from `agent_feedback.verdict` over the six-verdict taxonomy; findings-per-ticket counts `agent_reviews.proven_count + observation_count` for `ticket_id`-linked rows. No promotion gate is ever derived from these numbers (spec §8 / charter scope-out). Affects: delivery-outcomes, process-intel-experiments.
```

- [ ] **Step 3: Verify the additions parse.** Run `grep -n "1773106110–19 | scorecards\|scorecards planning" docs/superpowers/plans/agentic-platform/00-shared-contracts.md` — expect two matching lines (the table row and the log entry).

- [ ] **Step 4: Commit.**

```bash
git add docs/superpowers/plans/agentic-platform/00-shared-contracts.md
git commit -m "docs(agentic): scorecards contract addendum - migration block 1773106110-19, reciprocal outcomes sign-off"
```

---

### Task 2: `agent/api/scorecards` — aggregate domain types + Store interface

The API/DB contract types. Plain `testing`, matching `agent/api/reviews`/`agent/api/metrics`. A `Scorecard` is one workflow name plus a `VersionColumn` per requested version; every rate carries its denominator count (small-team sample honesty). Outcome fields are pointers so JSON omits them when dark.

**Files:**
- Create: `agent/api/scorecards/types.go`
- Test: `agent/api/scorecards/types_test.go`

**Steps:**

- [ ] **Step 1: Write the failing test** `agent/api/scorecards/types_test.go`:

```go
package scorecards_test

import (
	"encoding/json"
	"testing"

	"github.com/concourse/concourse/agent/api/scorecards"
)

func TestParseVersionsCSV(t *testing.T) {
	vs, err := scorecards.ParseVersionsCSV("3,4")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(vs) != 2 || vs[0] != 3 || vs[1] != 4 {
		t.Fatalf("expected [3 4], got %v", vs)
	}
	if _, err := scorecards.ParseVersionsCSV(""); err == nil {
		t.Fatalf("expected error for empty versions")
	}
	if _, err := scorecards.ParseVersionsCSV("3,x"); err == nil {
		t.Fatalf("expected error for non-integer version")
	}
	if _, err := scorecards.ParseVersionsCSV("3,3,4"); err != nil {
		t.Fatalf("dedupe should not error, got %v", err)
	}
}

func TestVersionColumnJSONOmitsDarkOutcomes(t *testing.T) {
	// Outcome pointers nil = "watcher has not filled them yet" -> omitted.
	col := scorecards.VersionColumn{
		Version:     3,
		ContentHash: "abc123",
		Live:        true,
		TicketCount: 5,
	}
	b, _ := json.Marshal(col)
	s := string(b)
	if contains(s, "merge_rate") {
		t.Fatalf("nil outcome fields must be omitted, got %s", s)
	}
	rate := 0.6
	col.MergeRate = &rate
	col.MergedCount = intptr(3)
	b, _ = json.Marshal(col)
	if !contains(string(b), "merge_rate") {
		t.Fatalf("non-nil outcome field must serialize, got %s", string(b))
	}
}

func intptr(i int) *int { return &i }
func contains(s, sub string) bool {
	return len(s) >= len(sub) && (func() bool {
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				return true
			}
		}
		return false
	})()
}
```

- [ ] **Step 2: Run test to verify it fails.** Run: `go test ./agent/api/scorecards/` — Expected: FAIL (`package .../agent/api/scorecards is not in std` / build error, package does not exist).

- [ ] **Step 3: Write `agent/api/scorecards/types.go`:**

```go
package scorecards

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
)

//go:generate go run github.com/maxbrunsfeld/counterfeiter/v6 -generate

// Scorecard compares one workflow definition's versions side-by-side.
// It is a pure read-model over agent_run_metrics / agent_cost_ledger /
// agent_reviews / agent_feedback / agent_outcomes (shared-contracts
// §1.8/§1.4/§1.10/§1.11). No promotion gate is ever derived from it.
type Scorecard struct {
	WorkflowName string          `json:"workflow_name"`
	Columns      []VersionColumn `json:"columns"`
}

// VersionColumn is one workflow version's aggregated metrics. Every rate
// carries its denominator count (small-team samples are noisy; the UI
// shows both). Objective outcome fields are pointers: nil = the
// same-wave outcome watcher has not populated agent_outcomes yet ("dark"
// column), rendered as "—" rather than a misleading 0%.
type VersionColumn struct {
	Version     int    `json:"version"`
	ContentHash string `json:"content_hash"`
	Live        bool   `json:"live"`

	// Volume denominators.
	TicketCount int `json:"ticket_count"` // distinct ticket_id in agent_run_metrics for this version
	StepCount   int `json:"step_count"`   // agent_run_metrics rows for this version

	// ok/failed/error taxonomy (spec §8: agent-did-badly vs platform-broke),
	// counted from agent_run_metrics.status. Enforced end-to-end.
	StepOK     int `json:"step_ok"`
	StepFailed int `json:"step_failed"`
	StepError  int `json:"step_error"`

	// Objective — cost/turns from agent_cost_ledger, gate pass from
	// harvest agent_reviews evidence, findings from agent_reviews counts.
	CostUSDTotal      float64  `json:"cost_usd_total"`
	CostUSDPerTicket  *float64 `json:"cost_usd_per_ticket,omitempty"` // nil when TicketCount == 0
	TurnsTotal        int64    `json:"turns_total"`
	TurnsPerTicket    *float64 `json:"turns_per_ticket,omitempty"`
	GateReviewCount   int      `json:"gate_review_count"` // harvest reviews for this version's tickets
	GatePassCount     int      `json:"gate_pass_count"`   // of those, agent_reviews.pass = true
	GatePassRate      *float64 `json:"gate_pass_rate,omitempty"`
	FindingsTotal     int      `json:"findings_total"`     // proven_count + observation_count summed
	FindingsPerTicket *float64 `json:"findings_per_ticket,omitempty"`

	// Subjective — judge rubric scores (agent_reviews.score/max_score for
	// ticket-linked harvest rows) and human verdict distribution.
	JudgeScoredCount   int                 `json:"judge_scored_count"`
	JudgeScoreMeanPct  *float64            `json:"judge_score_mean_pct,omitempty"` // mean(score/max_score) * 100
	VerdictDistribution VerdictDistribution `json:"verdict_distribution"`

	// Outcome-derived (LEFT JOIN agent_outcomes; all nil until the
	// same-wave watcher lands — §1.11.1). MergeState denominators use
	// OutcomeCount (tickets that reached needs_review with an outcome row).
	OutcomeCount        *int     `json:"outcome_count,omitempty"`
	MergedCount         *int     `json:"merged_count,omitempty"`
	MergeRate           *float64 `json:"merge_rate,omitempty"`
	MergedUntouchedCount *int    `json:"merged_untouched_count,omitempty"` // merge_state='merged' (no human commits)
	MergedUntouchedRate *float64 `json:"merged_untouched_rate,omitempty"`
	SentBackCount       *int     `json:"sent_back_count,omitempty"`        // disposition='sent_back'
	SentBackRate        *float64 `json:"sent_back_rate,omitempty"`
	HumanLinesTotal     *int     `json:"human_lines_total,omitempty"`      // added+deleted, labeled "lines"
	TimeToMergeMedianHrs *float64 `json:"time_to_merge_median_hrs,omitempty"`
}

// VerdictDistribution counts human verdicts on this version's harvest
// findings, over the six-verdict feedback taxonomy (§6.4.1). A zero-value
// distribution (no feedback yet) marshals to all-zero counts, which the
// UI renders as "no verdicts recorded".
type VerdictDistribution struct {
	Accurate         int `json:"accurate"`
	FalsePositive    int `json:"false_positive"`
	Noisy            int `json:"noisy"`
	OverlyStrict     int `json:"overly_strict"`
	PartiallyCorrect int `json:"partially_correct"`
	MissedContext    int `json:"missed_context"`
}

// ParseVersionsCSV parses the ?versions=3,4 query param into a sorted,
// de-duplicated slice. Errors on empty input or a non-integer element.
func ParseVersionsCSV(csv string) ([]int, error) {
	if strings.TrimSpace(csv) == "" {
		return nil, fmt.Errorf("versions is required (e.g. ?versions=3,4)")
	}
	seen := map[int]bool{}
	var out []int
	for _, part := range strings.Split(csv, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		v, err := strconv.Atoi(part)
		if err != nil {
			return nil, fmt.Errorf("invalid version %q: not an integer", part)
		}
		if !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("versions is required (e.g. ?versions=3,4)")
	}
	sort.Ints(out)
	return out, nil
}

// Store computes scorecards. Implemented by atc/db.ScorecardStore over
// the upstream tables; a counterfeiter fake backs the handler test.
//
//counterfeiter:generate . Store
type Store interface {
	// Scorecard aggregates the given workflow's versions. Versions with
	// no rows still appear (all-zero column) so the UI shows "no runs yet"
	// rather than dropping the column.
	Scorecard(workflowName string, versions []int) (*Scorecard, error)
}
```

- [ ] **Step 4: Run test to verify it passes.** Run: `go test ./agent/api/scorecards/` — Expected: PASS.

- [ ] **Step 5: Generate the counterfeiter fake** (used by the handler test in Task 6): `go generate ./agent/api/scorecards/...` — produces `agent/api/scorecards/scorecardsfakes/fake_store.go`. Then `go build ./agent/api/scorecards/...`.

- [ ] **Step 6: Commit.**

```bash
git add agent/api/scorecards
git commit -m "feat(scorecards): aggregate domain types, versions CSV parse, Store interface"
```

---

### Task 3: `atc/db` ScorecardStore — metrics-side aggregate (volume, taxonomy, cost, turns)

The first half of the SQL: everything computable from `agent_run_metrics` + `agent_cost_ledger` alone (volume denominators, the ok/failed/error split, cost/turns per ticket). Findings/judge/verdict (Task 4) and outcomes (Task 5) extend the same store. Follows the `agent_reviews_factory.go` recipe (squirrel `psql`, `f.conn.Query`, epoch-seconds scan).

**Files:**
- Create: `atc/db/scorecard_store.go`
- Test: `atc/db/scorecard_store_test.go`

**Steps:**

- [ ] **Step 1: Write the failing Ginkgo spec** `atc/db/scorecard_store_test.go` (in the existing `db_test` suite, which migrates the template DB; inserts fixture rows directly with `dbConn.Exec` so this suite does not depend on wave-mate factories):

```go
package db_test

import (
	"github.com/concourse/concourse/agent/api/scorecards"
	"github.com/concourse/concourse/atc/db"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("ScorecardStore", func() {
	var store scorecards.Store

	BeforeEach(func() {
		store = db.NewScorecardStore(dbConn)

		// Two versions of workflow "standard-dev". Insert agent_run_metrics
		// rows: v3 has 2 tickets (ticket 1: 2 steps ok; ticket 2: 1 step failed),
		// v4 has 1 ticket (ticket 3: 1 step error). agent_cost_ledger carries
		// per-ticket cost/turns.
		_, err := dbConn.Exec(`
			INSERT INTO agent_run_metrics
			  (ticket_id, build_id, plan_id, step_name, workflow_name, workflow_version, workflow_hash, status, turns, cost_usd)
			VALUES
			  (1, 100, 'a', 'spec',      'standard-dev', 3, 'h3', 'ok',     10, 1.00),
			  (1, 100, 'b', 'implement', 'standard-dev', 3, 'h3', 'ok',     20, 2.00),
			  (2, 101, 'c', 'implement', 'standard-dev', 3, 'h3', 'failed',  5, 0.50),
			  (3, 102, 'd', 'implement', 'standard-dev', 4, 'h4', 'error',   1, 0.10)`)
		Expect(err).ToNot(HaveOccurred())

		_, err = dbConn.Exec(`
			INSERT INTO agent_cost_ledger (ticket_id, source, cost_usd, turns)
			VALUES
			  (1, 'agent_step', 3.00, 30),
			  (2, 'agent_step', 0.50, 5),
			  (3, 'agent_step', 0.10, 1)`)
		Expect(err).ToNot(HaveOccurred())
	})

	It("aggregates volume, ok/failed/error taxonomy, cost, and turns per version", func() {
		sc, err := store.Scorecard("standard-dev", []int{3, 4})
		Expect(err).ToNot(HaveOccurred())
		Expect(sc.WorkflowName).To(Equal("standard-dev"))
		Expect(sc.Columns).To(HaveLen(2))

		v3 := sc.Columns[0]
		Expect(v3.Version).To(Equal(3))
		Expect(v3.TicketCount).To(Equal(2))
		Expect(v3.StepCount).To(Equal(3))
		Expect(v3.StepOK).To(Equal(2))
		Expect(v3.StepFailed).To(Equal(1))
		Expect(v3.StepError).To(Equal(0))
		Expect(v3.CostUSDTotal).To(BeNumerically("~", 3.50, 1e-9)) // ledger: 3.00 + 0.50
		Expect(v3.TurnsTotal).To(Equal(int64(35)))                 // ledger: 30 + 5
		Expect(*v3.CostUSDPerTicket).To(BeNumerically("~", 1.75, 1e-9))
		Expect(*v3.TurnsPerTicket).To(BeNumerically("~", 17.5, 1e-9))

		v4 := sc.Columns[1]
		Expect(v4.Version).To(Equal(4))
		Expect(v4.TicketCount).To(Equal(1))
		Expect(v4.StepError).To(Equal(1))
		Expect(*v4.CostUSDPerTicket).To(BeNumerically("~", 0.10, 1e-9))
	})

	It("returns an all-zero column for a version with no rows (nil per-ticket rates)", func() {
		sc, err := store.Scorecard("standard-dev", []int{99})
		Expect(err).ToNot(HaveOccurred())
		Expect(sc.Columns).To(HaveLen(1))
		Expect(sc.Columns[0].Version).To(Equal(99))
		Expect(sc.Columns[0].TicketCount).To(Equal(0))
		Expect(sc.Columns[0].CostUSDPerTicket).To(BeNil())
	})
})
```

- [ ] **Step 2: Run test to verify it fails.** Run: `pg_isready && ginkgo --focus="ScorecardStore" ./atc/db/` — Expected: FAIL (`undefined: db.NewScorecardStore`). (If `database "testdb_template" already exists` appears, another test run is live — wait for it, per CLAUDE.md.)

- [ ] **Step 3: Write `atc/db/scorecard_store.go`** (metrics + cost only; findings/judge/verdict/outcomes added in Tasks 4–5 into the same `perVersion` method):

```go
package db

import (
	"database/sql"

	"github.com/concourse/concourse/agent/api/scorecards"
)

//counterfeiter:generate . ScorecardStore
type ScorecardStore interface {
	scorecards.Store
}

func NewScorecardStore(conn DbConn) ScorecardStore {
	return &scorecardStore{conn: conn}
}

type scorecardStore struct {
	conn DbConn
}

func (s *scorecardStore) Scorecard(workflowName string, versions []int) (*scorecards.Scorecard, error) {
	out := &scorecards.Scorecard{WorkflowName: workflowName}
	for _, v := range versions {
		col, err := s.perVersion(workflowName, v)
		if err != nil {
			return nil, err
		}
		out.Columns = append(out.Columns, *col)
	}
	return out, nil
}

// perVersion aggregates one (workflowName, version). Each concern is its
// own query so an empty upstream table (e.g. agent_outcomes before the
// watcher lands) degrades that concern to zero/dark without failing the
// whole scorecard.
func (s *scorecardStore) perVersion(name string, version int) (*scorecards.VersionColumn, error) {
	col := &scorecards.VersionColumn{Version: version}

	if err := s.metricsAndCost(name, version, col); err != nil {
		return nil, err
	}
	return col, nil
}

// metricsAndCost fills volume/taxonomy from agent_run_metrics and
// cost/turns from agent_cost_ledger (per §1.8/§1.4). Cost joins the ledger
// by ticket_id (authoritative per-ticket spend, incl. gateway rows) over
// the DISTINCT tickets this version ran — not by metadata->>'workflow',
// which is a display-attribution convenience that a hand-dispatched run
// may omit.
func (s *scorecardStore) metricsAndCost(name string, version int, col *scorecards.VersionColumn) error {
	var stepCount, ticketCount, ok, failed, errored int
	var workflowHash sql.NullString
	err := s.conn.QueryRow(`
		SELECT
		  COUNT(*),
		  COUNT(DISTINCT ticket_id) FILTER (WHERE ticket_id IS NOT NULL),
		  COUNT(*) FILTER (WHERE status = 'ok'),
		  COUNT(*) FILTER (WHERE status = 'failed'),
		  COUNT(*) FILTER (WHERE status = 'error'),
		  MAX(workflow_hash)
		FROM agent_run_metrics
		WHERE workflow_name = $1 AND workflow_version = $2`,
		name, version,
	).Scan(&stepCount, &ticketCount, &ok, &failed, &errored, &workflowHash)
	if err != nil {
		return err
	}
	col.StepCount = stepCount
	col.TicketCount = ticketCount
	col.StepOK = ok
	col.StepFailed = failed
	col.StepError = errored
	if workflowHash.Valid {
		col.ContentHash = workflowHash.String
	}

	// Cost/turns over the ledger rows for this version's tickets.
	var costTotal sql.NullFloat64
	var turnsTotal sql.NullInt64
	err = s.conn.QueryRow(`
		SELECT COALESCE(SUM(cost_usd), 0), COALESCE(SUM(turns), 0)
		FROM agent_cost_ledger
		WHERE ticket_id IN (
		  SELECT DISTINCT ticket_id FROM agent_run_metrics
		  WHERE workflow_name = $1 AND workflow_version = $2 AND ticket_id IS NOT NULL
		)`,
		name, version,
	).Scan(&costTotal, &turnsTotal)
	if err != nil {
		return err
	}
	col.CostUSDTotal = costTotal.Float64
	col.TurnsTotal = turnsTotal.Int64

	if ticketCount > 0 {
		cpt := col.CostUSDTotal / float64(ticketCount)
		tpt := float64(col.TurnsTotal) / float64(ticketCount)
		col.CostUSDPerTicket = &cpt
		col.TurnsPerTicket = &tpt
	}
	return nil
}
```

- [ ] **Step 4: Run test to verify it passes.** Run: `ginkgo --focus="ScorecardStore" ./atc/db/` — Expected: PASS (both specs).

- [ ] **Step 5: Generate the DB counterfeiter fake** (dispatch/experiments may consume it later; the API test uses the `agent/api/scorecards` fake): `cd atc/db && go run github.com/maxbrunsfeld/counterfeiter/v6 -o dbfakes/fake_scorecard_store.go . ScorecardStore && cd ../..` — then `go build ./atc/db/...`.

- [ ] **Step 6: Commit.**

```bash
git add atc/db/scorecard_store.go atc/db/scorecard_store_test.go atc/db/dbfakes
git commit -m "feat(scorecards): db ScorecardStore metrics+cost aggregate (volume, ok/failed/error, cost/turns per ticket)"
```

---

### Task 4: ScorecardStore — findings, judge scores, and human verdict distribution

Extend `perVersion` with the harvest-evidence side: gate pass rate + findings from `agent_reviews` (ticket-linked, §1.10), judge mean from `agent_reviews.score/max_score` (§6.4.1: `score.value` = judge total for harvest rows), and the six-verdict human distribution from `agent_feedback.verdict` over this version's tickets.

**Files:**
- Modify: `atc/db/scorecard_store.go` (add `evidence` method + call from `perVersion`)
- Modify: `atc/db/scorecard_store_test.go` (add a spec)

**Steps:**

- [ ] **Step 1: Add the failing spec** to `atc/db/scorecard_store_test.go` inside the existing `Describe("ScorecardStore", ...)`. Extend the `BeforeEach` fixture with harvest reviews + feedback (append these `dbConn.Exec` blocks after the existing inserts):

```go
		// Harvest evidence for v3's tickets: ticket 1 passed (judge 8/10),
		// ticket 2 failed gates (judge 4/10). proven_count/observation_count
		// feed findings-per-ticket. commit_sha differs per ticket (upsert key).
		_, err = dbConn.Exec(`
			INSERT INTO agent_reviews
			  (build_id, build_name, team_name, pipeline_name, job_name, repo, commit_sha, branch,
			   score, max_score, pass, proven_count, observation_count, summary, agent_model, duration_seconds,
			   review, ticket_id)
			VALUES
			  (100, '1', 'main', 'p', 'j', 'o/r', 'sha1', 'agent/ticket-1',
			   8, 10, true, 0, 2, 's', 'claude', 5, '{}', 1),
			  (101, '2', 'main', 'p', 'j', 'o/r', 'sha2', 'agent/ticket-2',
			   4, 10, false, 1, 3, 's', 'claude', 5, '{}', 2)`)
		Expect(err).ToNot(HaveOccurred())

		// Human verdicts on v3 findings (agent_feedback has finding_id, repo,
		// commit_sha, verdict, reviewer; ticket_id added by harvest §1.10).
		_, err = dbConn.Exec(`
			INSERT INTO agent_feedback (finding_id, repo, commit_sha, verdict, reviewer, ticket_id)
			VALUES
			  ('f1', 'o/r', 'sha1', 'accurate',       'alice', 1),
			  ('f2', 'o/r', 'sha1', 'false_positive', 'alice', 1),
			  ('f3', 'o/r', 'sha2', 'accurate',       'bob',   2)`)
		Expect(err).ToNot(HaveOccurred())
```

Then add the spec (still inside the `Describe`):

```go
	It("aggregates gate pass rate, findings-per-ticket, judge mean, and verdict distribution", func() {
		sc, err := store.Scorecard("standard-dev", []int{3})
		Expect(err).ToNot(HaveOccurred())
		v3 := sc.Columns[0]

		Expect(v3.GateReviewCount).To(Equal(2))
		Expect(v3.GatePassCount).To(Equal(1))
		Expect(*v3.GatePassRate).To(BeNumerically("~", 0.5, 1e-9))

		Expect(v3.FindingsTotal).To(Equal(6)) // (0+2) + (1+3)
		Expect(*v3.FindingsPerTicket).To(BeNumerically("~", 3.0, 1e-9)) // 6 / 2 tickets

		Expect(v3.JudgeScoredCount).To(Equal(2))
		Expect(*v3.JudgeScoreMeanPct).To(BeNumerically("~", 60.0, 1e-9)) // mean(80%, 40%)

		Expect(v3.VerdictDistribution.Accurate).To(Equal(2))
		Expect(v3.VerdictDistribution.FalsePositive).To(Equal(1))
		Expect(v3.VerdictDistribution.Noisy).To(Equal(0))
	})
```

- [ ] **Step 2: Run test to verify it fails.** Run: `ginkgo --focus="ScorecardStore" ./atc/db/` — Expected: FAIL (`GateReviewCount` is 0 / `GatePassRate` is nil — `evidence` not yet wired).

- [ ] **Step 3: Add the `evidence` method and call it from `perVersion`.** In `atc/db/scorecard_store.go`, add the call inside `perVersion` after `metricsAndCost`:

```go
	if err := s.evidence(name, version, col); err != nil {
		return nil, err
	}
```

Then add the method:

```go
// evidence fills the harvest side: gate pass rate + findings from
// agent_reviews (ticket-linked, §1.10), judge mean from score/max_score
// (§6.4.1), and the six-verdict human distribution from agent_feedback —
// all scoped to the tickets this version ran. Empty result sets leave the
// fields zero/dark (nil rates), never error.
func (s *scorecardStore) evidence(name string, version int, col *scorecards.VersionColumn) error {
	// The DISTINCT tickets this version ran; reused by every evidence query.
	const ticketsCTE = `WITH v_tickets AS (
	  SELECT DISTINCT ticket_id FROM agent_run_metrics
	  WHERE workflow_name = $1 AND workflow_version = $2 AND ticket_id IS NOT NULL
	)`

	var reviewCount, passCount, findingsTotal, judgeScored int
	var judgeMeanPct sql.NullFloat64
	err := s.conn.QueryRow(ticketsCTE+`
		SELECT
		  COUNT(*),
		  COUNT(*) FILTER (WHERE r.pass),
		  COALESCE(SUM(r.proven_count + r.observation_count), 0),
		  COUNT(*) FILTER (WHERE r.max_score > 0),
		  AVG(CASE WHEN r.max_score > 0 THEN (r.score / r.max_score) * 100 END)
		FROM agent_reviews r
		WHERE r.ticket_id IN (SELECT ticket_id FROM v_tickets)`,
		name, version,
	).Scan(&reviewCount, &passCount, &findingsTotal, &judgeScored, &judgeMeanPct)
	if err != nil {
		return err
	}
	col.GateReviewCount = reviewCount
	col.GatePassCount = passCount
	col.FindingsTotal = findingsTotal
	col.JudgeScoredCount = judgeScored
	if reviewCount > 0 {
		rate := float64(passCount) / float64(reviewCount)
		col.GatePassRate = &rate
	}
	if col.TicketCount > 0 {
		fpt := float64(findingsTotal) / float64(col.TicketCount)
		col.FindingsPerTicket = &fpt
	}
	if judgeMeanPct.Valid {
		m := judgeMeanPct.Float64
		col.JudgeScoreMeanPct = &m
	}

	// Six-verdict human distribution over this version's tickets.
	rows, err := s.conn.Query(ticketsCTE+`
		SELECT fb.verdict, COUNT(*)
		FROM agent_feedback fb
		WHERE fb.ticket_id IN (SELECT ticket_id FROM v_tickets)
		GROUP BY fb.verdict`,
		name, version,
	)
	if err != nil {
		return err
	}
	defer Close(rows)
	for rows.Next() {
		var verdict string
		var n int
		if err := rows.Scan(&verdict, &n); err != nil {
			return err
		}
		switch verdict {
		case "accurate":
			col.VerdictDistribution.Accurate = n
		case "false_positive":
			col.VerdictDistribution.FalsePositive = n
		case "noisy":
			col.VerdictDistribution.Noisy = n
		case "overly_strict":
			col.VerdictDistribution.OverlyStrict = n
		case "partially_correct":
			col.VerdictDistribution.PartiallyCorrect = n
		case "missed_context":
			col.VerdictDistribution.MissedContext = n
		}
	}
	return rows.Err()
}
```

Note: `Close(rows)` is the `atc/db` package helper for closing a `*sql.Rows` (used throughout `agent_reviews_factory.go`); confirm the exact name with `grep -n "func Close(" atc/db/open.go` and match it.

- [ ] **Step 4: Run test to verify it passes.** Run: `ginkgo --focus="ScorecardStore" ./atc/db/` — Expected: PASS (all three specs).

- [ ] **Step 5: Commit.**

```bash
git add atc/db/scorecard_store.go atc/db/scorecard_store_test.go
git commit -m "feat(scorecards): gate pass rate, findings/ticket, judge mean, six-verdict distribution"
```

---

### Task 5: ScorecardStore — outcome columns via NULLABLE LEFT JOIN onto agent_outcomes

The dark-until-filled outcome columns (merge rate, merged-untouched, sent-back, human-touch lines, time-to-merge). Because `agent_outcomes` is a wave-mate table (`1773106090`), the query first checks the table exists; when it does not, the outcome pointers stay nil (dark) and the scorecard still returns. When it exists, LEFT JOIN on unique `ticket_id` per §1.11.1.

**Files:**
- Modify: `atc/db/scorecard_store.go` (add `outcomes` method + call from `perVersion`)
- Modify: `atc/db/scorecard_store_test.go` (add two specs: with and without outcome rows)

**Steps:**

- [ ] **Step 1: Add the failing specs** to `atc/db/scorecard_store_test.go` inside the `Describe`. The `db_test` suite migrates the template DB to HEAD, so `agent_outcomes` (migration `1773106090`) is present once delivery-outcomes' migration is on the branch; this plan's index migration (`1773106110`, Task 10) is higher, so at execution time both exist. Insert outcome fixtures in a dedicated `Context`:

```go
	Context("with agent_outcomes rows (watcher landed)", func() {
		BeforeEach(func() {
			// ticket 1 merged untouched; ticket 2 merged with 12 human lines.
			// merged_at timestamps give a time-to-merge signal.
			_, err := dbConn.Exec(`
				INSERT INTO agent_outcomes
				  (ticket_id, repo, branch, pushed_sha, base_sha, merge_state, merged_sha, merged_at,
				   human_commit_count, human_lines_added, human_lines_deleted, disposition)
				VALUES
				  (1, 'o/r', 'agent/ticket-1', 's1', 'b1', 'merged',            'm1', now() - interval '2 hours',
				   0, 0, 0, ''),
				  (2, 'o/r', 'agent/ticket-2', 's2', 'b2', 'merged_with_fixes', 'm2', now() - interval '6 hours',
				   1, 8, 4, '')`)
			Expect(err).ToNot(HaveOccurred())
		})

		It("fills outcome columns via LEFT JOIN on ticket_id", func() {
			sc, err := store.Scorecard("standard-dev", []int{3})
			Expect(err).ToNot(HaveOccurred())
			v3 := sc.Columns[0]

			Expect(v3.OutcomeCount).ToNot(BeNil())
			Expect(*v3.OutcomeCount).To(Equal(2))
			Expect(*v3.MergedCount).To(Equal(2)) // both merged* states count as merged
			Expect(*v3.MergeRate).To(BeNumerically("~", 1.0, 1e-9))
			Expect(*v3.MergedUntouchedCount).To(Equal(1)) // only merge_state='merged'
			Expect(*v3.MergedUntouchedRate).To(BeNumerically("~", 0.5, 1e-9))
			Expect(*v3.SentBackCount).To(Equal(0))
			Expect(*v3.HumanLinesTotal).To(Equal(12)) // 8 + 4
		})
	})
```

And, to prove the dark path without the table, a spec that does NOT depend on `agent_outcomes` existing is already covered by Task 3's "all-zero column" spec (version 99 has no outcome row → nil). Add one explicit assertion to the base (no-outcome-fixture) test in Task 3's first spec is unnecessary; instead add here a spec asserting dark columns when a version has metrics but no outcome rows:

```go
	It("leaves outcome columns dark when no outcome rows exist for the version", func() {
		sc, err := store.Scorecard("standard-dev", []int{4}) // v4 ticket 3 has no agent_outcomes row
		Expect(err).ToNot(HaveOccurred())
		Expect(sc.Columns[0].OutcomeCount).ToNot(BeNil()) // table exists -> 0, not nil
		Expect(*sc.Columns[0].OutcomeCount).To(Equal(0))
		Expect(sc.Columns[0].MergeRate).To(BeNil()) // no rows -> rate undefined -> nil
	})
```

- [ ] **Step 2: Run test to verify it fails.** Run: `ginkgo --focus="ScorecardStore" ./atc/db/` — Expected: FAIL (`v3.OutcomeCount` is nil — `outcomes` not yet wired).

- [ ] **Step 3: Add the `outcomes` method and call it from `perVersion`.** In `atc/db/scorecard_store.go`, add after the `evidence` call in `perVersion`:

```go
	if err := s.outcomes(name, version, col); err != nil {
		return nil, err
	}
```

Then add the method:

```go
// outcomes fills the LEFT-JOIN outcome columns from agent_outcomes
// (§1.11 + §1.11.1). agent_outcomes is a same-wave (delivery-outcomes)
// table; if it is absent at deploy time the columns stay nil (dark) and
// the scorecard still returns — neither workstream blocks the other. When
// the table exists, OutcomeCount is set (0 when no rows), and each rate is
// nil only when its denominator is 0. Delta columns are LINES (§1.11.1).
func (s *scorecardStore) outcomes(name string, version int, col *scorecards.VersionColumn) error {
	var exists bool
	if err := s.conn.QueryRow(
		`SELECT to_regclass('agent_outcomes') IS NOT NULL`,
	).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		return nil // dark columns; watcher/table not deployed yet
	}

	const ticketsCTE = `WITH v_tickets AS (
	  SELECT DISTINCT ticket_id FROM agent_run_metrics
	  WHERE workflow_name = $1 AND workflow_version = $2 AND ticket_id IS NOT NULL
	)`

	var outcomeCount, mergedCount, mergedUntouched, sentBack, humanLines int
	var ttmMedianHrs sql.NullFloat64
	err := s.conn.QueryRow(ticketsCTE+`
		SELECT
		  COUNT(*),
		  COUNT(*) FILTER (WHERE o.merge_state IN ('merged','merged_with_fixes')),
		  COUNT(*) FILTER (WHERE o.merge_state = 'merged'),
		  COUNT(*) FILTER (WHERE o.disposition = 'sent_back'),
		  COALESCE(SUM(o.human_lines_added + o.human_lines_deleted), 0),
		  PERCENTILE_CONT(0.5) WITHIN GROUP (
		    ORDER BY EXTRACT(EPOCH FROM (o.merged_at - o.created_at)) / 3600.0
		  ) FILTER (WHERE o.merged_at IS NOT NULL)
		FROM agent_outcomes o
		WHERE o.ticket_id IN (SELECT ticket_id FROM v_tickets)`,
		name, version,
	).Scan(&outcomeCount, &mergedCount, &mergedUntouched, &sentBack, &humanLines, &ttmMedianHrs)
	if err != nil {
		return err
	}

	col.OutcomeCount = &outcomeCount
	col.MergedCount = &mergedCount
	col.MergedUntouchedCount = &mergedUntouched
	col.SentBackCount = &sentBack
	col.HumanLinesTotal = &humanLines
	if outcomeCount > 0 {
		mr := float64(mergedCount) / float64(outcomeCount)
		mur := float64(mergedUntouched) / float64(outcomeCount)
		sbr := float64(sentBack) / float64(outcomeCount)
		col.MergeRate = &mr
		col.MergedUntouchedRate = &mur
		col.SentBackRate = &sbr
	}
	if ttmMedianHrs.Valid {
		t := ttmMedianHrs.Float64
		col.TimeToMergeMedianHrs = &t
	}
	return nil
}
```

- [ ] **Step 4: Run test to verify it passes.** Run: `ginkgo --focus="ScorecardStore" ./atc/db/` — Expected: PASS (all specs incl. the two new outcome specs).

- [ ] **Step 5: Commit.**

```bash
git add atc/db/scorecard_store.go atc/db/scorecard_store_test.go
git commit -m "feat(scorecards): outcome columns via nullable LEFT JOIN on agent_outcomes (dark until watcher lands)"
```

---

### Task 6: HTTP handler + route registration for `GetAgentWorkflowScorecard`

The one route: `GET /api/v1/agent/workflows/:workflow_name/scorecard?versions=3,4`, authorized viewer (team-less → the wave-1 `CheckAgentAuthorizationHandler` group). Handler parses versions, calls `Store.Scorecard`, writes JSON. Follows the `agent/api/reviews` handler idiom (`r.FormValue(":workflow_name")` for the rata path param; `r.URL.Query().Get("versions")` for the query).

**Files:**
- Create: `agent/api/scorecards/handler.go`
- Test: `agent/api/scorecards/handler_test.go`
- Modify: `atc/routes.go` (name constant + route entry, near the other `Agent` routes ~:127/:260)
- Modify: `atc/api/handler.go` (new `scorecardStore` param ~:91, construct handler ~:122, map entry ~:277)
- Modify: `atc/wrappa/api_auth_wrappa.go` (add `GetAgentWorkflowScorecard` to the `CheckAgentAuthorizationHandler` case group, near the feedback routes ~:169)
- Modify: `atc/api/accessor/roles.go` (`atc.GetAgentWorkflowScorecard: ViewerRole`, near ~:108)
- Modify: `atc/atccmd/command.go` (construct `db.NewScorecardStore` and pass to `api.NewHandler`, near the other agent-store wiring)
- Modify: `atc/wrappa/api_auth_wrappa_test.go` (add the route to exhaustive expectations)

**Steps:**

- [ ] **Step 1: Write the failing handler test** `agent/api/scorecards/handler_test.go` (httptest against the handler with the counterfeiter fake, mirroring `agent/api/metrics/handler_test.go`):

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

func TestScorecardHandlerParsesVersionsAndReturnsJSON(t *testing.T) {
	fake := new(scorecardsfakes.FakeStore)
	fake.ScorecardReturns(&scorecards.Scorecard{
		WorkflowName: "standard-dev",
		Columns:      []scorecards.VersionColumn{{Version: 3, TicketCount: 5}},
	}, nil)
	h := scorecards.NewHandler(fake)

	// rata puts path params in the query as ":name"; emulate that here.
	req := httptest.NewRequest("GET", "/api/v1/agent/workflows/standard-dev/scorecard?:workflow_name=standard-dev&versions=3,4", nil)
	rec := httptest.NewRecorder()
	h.GetScorecard(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	name, versions := fake.ScorecardArgsForCall(0)
	if name != "standard-dev" {
		t.Fatalf("expected workflow name standard-dev, got %q", name)
	}
	if len(versions) != 2 || versions[0] != 3 || versions[1] != 4 {
		t.Fatalf("expected versions [3 4], got %v", versions)
	}
	var sc scorecards.Scorecard
	if err := json.Unmarshal(rec.Body.Bytes(), &sc); err != nil {
		t.Fatalf("body not a Scorecard: %v", err)
	}
	if sc.WorkflowName != "standard-dev" || len(sc.Columns) != 1 {
		t.Fatalf("unexpected body: %+v", sc)
	}
}

func TestScorecardHandlerRejectsMissingVersions(t *testing.T) {
	h := scorecards.NewHandler(new(scorecardsfakes.FakeStore))
	req := httptest.NewRequest("GET", "/api/v1/agent/workflows/x/scorecard?:workflow_name=x", nil)
	rec := httptest.NewRecorder()
	h.GetScorecard(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for missing versions, got %d", rec.Code)
	}
}
```

- [ ] **Step 2: Run test to verify it fails.** Run: `go test ./agent/api/scorecards/` — Expected: FAIL (`undefined: scorecards.NewHandler`).

- [ ] **Step 3: Write `agent/api/scorecards/handler.go`:**

```go
package scorecards

import (
	"encoding/json"
	"net/http"
)

// Handler serves the workflow-scorecard read API.
type Handler struct {
	store Store
}

func NewHandler(store Store) *Handler {
	return &Handler{store: store}
}

// GetScorecard handles
// GET /api/v1/agent/workflows/:workflow_name/scorecard?versions=3,4.
func (h *Handler) GetScorecard(w http.ResponseWriter, r *http.Request) {
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
	sc, err := h.store.Scorecard(name, versions)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(sc)
}
```

- [ ] **Step 4: Run test to verify it passes.** Run: `go test ./agent/api/scorecards/` — Expected: PASS.

- [ ] **Step 5: Register the route name + path in `atc/routes.go`.** Next to the existing agent route name constants (the `SubmitAgentReview`/`GetBuildAgentReviews` block, ~:127), add:

```go
	GetAgentWorkflowScorecard = "GetAgentWorkflowScorecard"
```

and in the routes table, adjacent to the other `/api/v1/agent/*` entries (after the agent-reviews block ~:262), add:

```go
	{Path: "/api/v1/agent/workflows/:workflow_name/scorecard", Method: "GET", Name: GetAgentWorkflowScorecard},
```

- [ ] **Step 6: Run the wrappa exhaustiveness test to see it fail.** Run: `ginkgo ./atc/wrappa/` — Expected: FAIL, the exhaustive auth switch panics `you missed a spot: "GetAgentWorkflowScorecard"` (this is the failing test driving the wrappa change).

- [ ] **Step 7: Add the route to the `CheckAgentAuthorizationHandler` case group in `atc/wrappa/api_auth_wrappa.go`.** Find the case group agent-identity landed for team-less agent-authorized routes (grep `CheckAgentAuthorizationHandler`; the five feedback routes + `ListAgentRunMetrics` sit there). Add `atc.GetAgentWorkflowScorecard,` to that `case` list.

- [ ] **Step 8: Add the role in `atc/api/accessor/roles.go`.** In the `DefaultRoles` map, near the other agent entries (~:108), add:

```go
	atc.GetAgentWorkflowScorecard: ViewerRole,
```

- [ ] **Step 9: Wire the handler in `atc/api/handler.go`.** Import `scorecardsapi "github.com/concourse/concourse/agent/api/scorecards"`; add param `scorecardStore scorecardsapi.Store,` after the existing agent-store params (~:91, near `reviewsStore`/`metricsStore`); construct `scorecardsServer := scorecardsapi.NewHandler(scorecardStore)` next to `reviewsServer` (~:122); add to the handlers map (~:277):

```go
		atc.GetAgentWorkflowScorecard: http.HandlerFunc(scorecardsServer.GetScorecard),
```

- [ ] **Step 10: Construct the store in `atc/atccmd/command.go`.** Where the other agent stores are built and passed into `api.NewHandler` (grep `NewAgentRunMetricsFactory` / `NewAgentReviewsFactory` for the block), build `db.NewScorecardStore(dbConn)` and pass it as the new `scorecardStore` argument. Match the exact `dbConn` variable name in scope there.

- [ ] **Step 11: Add the route to the wrappa test's expectations** `atc/wrappa/api_auth_wrappa_test.go`. Copy the assertion style used for `ListAgentRunMetrics` / the feedback routes (they are on the same `CheckAgentAuthorizationHandler` tier) and add `GetAgentWorkflowScorecard`.

- [ ] **Step 12: Run the affected suites.** Run: `go test ./agent/api/scorecards/ && ginkgo ./atc/wrappa/ && go build ./...` — Expected: all PASS/build clean.

- [ ] **Step 13: Commit.**

```bash
git add agent/api/scorecards atc/routes.go atc/api/handler.go atc/wrappa atc/api/accessor/roles.go atc/atccmd/command.go
git commit -m "feat(api): GetAgentWorkflowScorecard route (authorized viewer) + handler wiring"
```

---

### Task 7: Elm — `Concourse/AgentMetrics.elm` and `Concourse/Scorecard.elm` decoders

Two decoder modules mirroring `Concourse/AgentReview.elm`: `AgentMetrics` decodes one `schema.RunMetrics` row (for the "where did the turns go" panel — Task 8), and `Scorecard` decodes the `GetAgentWorkflowScorecard` response (for the comparison view — Task 9). Elm's decoders FAIL on unexpected shapes, so the fields must match the Go JSON tags exactly, and pointer fields (`*float64`) decode as `Maybe`.

**Files:**
- Create: `web/elm/src/Concourse/AgentMetrics.elm`
- Create: `web/elm/src/Concourse/Scorecard.elm`
- Test: `web/elm/tests/Concourse/ScorecardTests.elm`

**Steps:**

- [ ] **Step 1: Write the failing elm-test** `web/elm/tests/Concourse/ScorecardTests.elm`:

```elm
module Concourse.ScorecardTests exposing (all)

import Concourse.AgentMetrics as AgentMetrics
import Concourse.Scorecard as Scorecard
import Expect
import Json.Decode as Decode
import Test exposing (Test, describe, test)


all : Test
all =
    describe "Scorecard and AgentMetrics decoders"
        [ test "decodes a scorecard with a dark and a filled outcome column" <|
            \_ ->
                let
                    json =
                        """
                        {"workflow_name":"standard-dev","columns":[
                          {"version":3,"content_hash":"h3","live":true,
                           "ticket_count":2,"step_count":3,"step_ok":2,"step_failed":1,"step_error":0,
                           "cost_usd_total":3.5,"cost_usd_per_ticket":1.75,
                           "turns_total":35,"turns_per_ticket":17.5,
                           "gate_review_count":2,"gate_pass_count":1,"gate_pass_rate":0.5,
                           "findings_total":6,"findings_per_ticket":3.0,
                           "judge_scored_count":2,"judge_score_mean_pct":60.0,
                           "verdict_distribution":{"accurate":2,"false_positive":1,"noisy":0,"overly_strict":0,"partially_correct":0,"missed_context":0},
                           "outcome_count":2,"merged_count":2,"merge_rate":1.0,
                           "merged_untouched_count":1,"merged_untouched_rate":0.5,
                           "sent_back_count":0,"sent_back_rate":0.0,"human_lines_total":12},
                          {"version":4,"content_hash":"h4","live":false,
                           "ticket_count":1,"step_count":1,"step_ok":0,"step_failed":0,"step_error":1,
                           "cost_usd_total":0.1,"turns_total":1,
                           "gate_review_count":0,"findings_total":0,"judge_scored_count":0,
                           "verdict_distribution":{"accurate":0,"false_positive":0,"noisy":0,"overly_strict":0,"partially_correct":0,"missed_context":0}}
                        ]}
                        """
                in
                case Decode.decodeString Scorecard.decode json of
                    Ok sc ->
                        Expect.all
                            [ \s -> Expect.equal "standard-dev" s.workflowName
                            , \s -> Expect.equal 2 (List.length s.columns)
                            , \s ->
                                case List.head s.columns of
                                    Just c ->
                                        Expect.equal (Just 1.0) c.mergeRate

                                    Nothing ->
                                        Expect.fail "no columns"
                            , \s ->
                                case List.drop 1 s.columns |> List.head of
                                    Just c ->
                                        Expect.equal Nothing c.mergeRate

                                    Nothing ->
                                        Expect.fail "no v4 column"
                            ]
                            sc

                    Err e ->
                        Expect.fail (Decode.errorToString e)
        , test "decodes an agent run-metrics row with event counts" <|
            \_ ->
                let
                    json =
                        """
                        {"build_id":9,"plan_id":"p","step_name":"implement","status":"ok",
                         "turns":20,"wall_time_seconds":61,"cost_usd":2.0,"model":"claude",
                         "event_counts":{"tool.call":87,"subagent.call":3}}
                        """
                in
                case Decode.decodeString AgentMetrics.decode json of
                    Ok m ->
                        Expect.all
                            [ \x -> Expect.equal "implement" x.stepName
                            , \x -> Expect.equal 20 x.turns
                            , \x -> Expect.equal (Just 87) (List.filter (\( k, _ ) -> k == "tool.call") x.eventCounts |> List.head |> Maybe.map Tuple.second)
                            ]
                            m

                    Err e ->
                        Expect.fail (Decode.errorToString e)
        ]
```

- [ ] **Step 2: Run test to verify it fails.** Run: `cd web/elm && npx elm-test tests/Concourse/ScorecardTests.elm; cd ../..` — Expected: FAIL (modules `Concourse.Scorecard` / `Concourse.AgentMetrics` do not exist).

- [ ] **Step 3: Write `web/elm/src/Concourse/AgentMetrics.elm`:**

```elm
module Concourse.AgentMetrics exposing
    ( RunMetrics
    , decode
    )

import Json.Decode
import Json.Decode.Extra exposing (andMap)


type alias RunMetrics =
    { buildId : Int
    , planId : String
    , stepName : String
    , status : String
    , model : String
    , turns : Int
    , wallTimeSeconds : Int
    , costUsd : Float
    , summary : String
    , eventCounts : List ( String, Int )
    }


decode : Json.Decode.Decoder RunMetrics
decode =
    Json.Decode.succeed RunMetrics
        |> andMap (Json.Decode.field "build_id" Json.Decode.int)
        |> andMap (Json.Decode.field "plan_id" Json.Decode.string)
        |> andMap (Json.Decode.field "step_name" Json.Decode.string)
        |> andMap (Json.Decode.field "status" Json.Decode.string)
        |> andMap (optionalString "model")
        |> andMap (optionalInt "turns")
        |> andMap (optionalInt "wall_time_seconds")
        |> andMap (optionalFloat "cost_usd")
        |> andMap (optionalString "summary")
        |> andMap (optionalPairs "event_counts")


optionalString : String -> Json.Decode.Decoder String
optionalString key =
    Json.Decode.maybe (Json.Decode.field key Json.Decode.string)
        |> Json.Decode.map (Maybe.withDefault "")


optionalInt : String -> Json.Decode.Decoder Int
optionalInt key =
    Json.Decode.maybe (Json.Decode.field key Json.Decode.int)
        |> Json.Decode.map (Maybe.withDefault 0)


optionalFloat : String -> Json.Decode.Decoder Float
optionalFloat key =
    Json.Decode.maybe (Json.Decode.field key Json.Decode.float)
        |> Json.Decode.map (Maybe.withDefault 0.0)


optionalPairs : String -> Json.Decode.Decoder (List ( String, Int ))
optionalPairs key =
    Json.Decode.maybe (Json.Decode.field key (Json.Decode.keyValuePairs Json.Decode.int))
        |> Json.Decode.map (Maybe.withDefault [])
```

- [ ] **Step 4: Write `web/elm/src/Concourse/Scorecard.elm`:**

```elm
module Concourse.Scorecard exposing
    ( Scorecard
    , VerdictDistribution
    , VersionColumn
    , decode
    )

import Json.Decode
import Json.Decode.Extra exposing (andMap)


type alias Scorecard =
    { workflowName : String
    , columns : List VersionColumn
    }


type alias VerdictDistribution =
    { accurate : Int
    , falsePositive : Int
    , noisy : Int
    , overlyStrict : Int
    , partiallyCorrect : Int
    , missedContext : Int
    }


type alias VersionColumn =
    { version : Int
    , contentHash : String
    , live : Bool
    , ticketCount : Int
    , stepCount : Int
    , stepOk : Int
    , stepFailed : Int
    , stepError : Int
    , costUsdTotal : Float
    , costUsdPerTicket : Maybe Float
    , turnsTotal : Int
    , turnsPerTicket : Maybe Float
    , gateReviewCount : Int
    , gatePassCount : Int
    , gatePassRate : Maybe Float
    , findingsTotal : Int
    , findingsPerTicket : Maybe Float
    , judgeScoredCount : Int
    , judgeScoreMeanPct : Maybe Float
    , verdictDistribution : VerdictDistribution
    , outcomeCount : Maybe Int
    , mergedCount : Maybe Int
    , mergeRate : Maybe Float
    , mergedUntouchedCount : Maybe Int
    , mergedUntouchedRate : Maybe Float
    , sentBackCount : Maybe Int
    , sentBackRate : Maybe Float
    , humanLinesTotal : Maybe Int
    , timeToMergeMedianHrs : Maybe Float
    }


decode : Json.Decode.Decoder Scorecard
decode =
    Json.Decode.succeed Scorecard
        |> andMap (Json.Decode.field "workflow_name" Json.Decode.string)
        |> andMap (Json.Decode.field "columns" (Json.Decode.list decodeColumn))


decodeVerdicts : Json.Decode.Decoder VerdictDistribution
decodeVerdicts =
    Json.Decode.succeed VerdictDistribution
        |> andMap (Json.Decode.field "accurate" Json.Decode.int)
        |> andMap (Json.Decode.field "false_positive" Json.Decode.int)
        |> andMap (Json.Decode.field "noisy" Json.Decode.int)
        |> andMap (Json.Decode.field "overly_strict" Json.Decode.int)
        |> andMap (Json.Decode.field "partially_correct" Json.Decode.int)
        |> andMap (Json.Decode.field "missed_context" Json.Decode.int)


decodeColumn : Json.Decode.Decoder VersionColumn
decodeColumn =
    Json.Decode.succeed VersionColumn
        |> andMap (Json.Decode.field "version" Json.Decode.int)
        |> andMap (optString "content_hash")
        |> andMap (optBool "live")
        |> andMap (optInt "ticket_count")
        |> andMap (optInt "step_count")
        |> andMap (optInt "step_ok")
        |> andMap (optInt "step_failed")
        |> andMap (optInt "step_error")
        |> andMap (optFloat "cost_usd_total")
        |> andMap (maybeFloat "cost_usd_per_ticket")
        |> andMap (optIntFromMaybe "turns_total")
        |> andMap (maybeFloat "turns_per_ticket")
        |> andMap (optInt "gate_review_count")
        |> andMap (optInt "gate_pass_count")
        |> andMap (maybeFloat "gate_pass_rate")
        |> andMap (optInt "findings_total")
        |> andMap (maybeFloat "findings_per_ticket")
        |> andMap (optInt "judge_scored_count")
        |> andMap (maybeFloat "judge_score_mean_pct")
        |> andMap (Json.Decode.field "verdict_distribution" decodeVerdicts)
        |> andMap (maybeInt "outcome_count")
        |> andMap (maybeInt "merged_count")
        |> andMap (maybeFloat "merge_rate")
        |> andMap (maybeInt "merged_untouched_count")
        |> andMap (maybeFloat "merged_untouched_rate")
        |> andMap (maybeInt "sent_back_count")
        |> andMap (maybeFloat "sent_back_rate")
        |> andMap (maybeInt "human_lines_total")
        |> andMap (maybeFloat "time_to_merge_median_hrs")


optString : String -> Json.Decode.Decoder String
optString k =
    Json.Decode.maybe (Json.Decode.field k Json.Decode.string) |> Json.Decode.map (Maybe.withDefault "")


optBool : String -> Json.Decode.Decoder Bool
optBool k =
    Json.Decode.maybe (Json.Decode.field k Json.Decode.bool) |> Json.Decode.map (Maybe.withDefault False)


optInt : String -> Json.Decode.Decoder Int
optInt k =
    Json.Decode.maybe (Json.Decode.field k Json.Decode.int) |> Json.Decode.map (Maybe.withDefault 0)


optIntFromMaybe : String -> Json.Decode.Decoder Int
optIntFromMaybe =
    optInt


optFloat : String -> Json.Decode.Decoder Float
optFloat k =
    Json.Decode.maybe (Json.Decode.field k Json.Decode.float) |> Json.Decode.map (Maybe.withDefault 0.0)


maybeFloat : String -> Json.Decode.Decoder (Maybe Float)
maybeFloat k =
    Json.Decode.maybe (Json.Decode.field k Json.Decode.float)


maybeInt : String -> Json.Decode.Decoder (Maybe Int)
maybeInt k =
    Json.Decode.maybe (Json.Decode.field k Json.Decode.int)
```

- [ ] **Step 5: Run test to verify it passes.** Run: `cd web/elm && npx elm-test tests/Concourse/ScorecardTests.elm; cd ../..` — Expected: PASS.

- [ ] **Step 6: Commit.**

```bash
git add web/elm/src/Concourse/AgentMetrics.elm web/elm/src/Concourse/Scorecard.elm web/elm/tests/Concourse/ScorecardTests.elm
git commit -m "feat(web): Scorecard and AgentMetrics Elm decoders"
```

---

### Task 8: Elm — "where did the turns go" per-step metrics panel on the ticket page

The run/ticket-page per-step panel. It reuses the **existing** `ListAgentRunMetrics` route (`GET /api/v1/agent/tickets/:ticket_id/metrics`, registered by agent-step) — this plan only adds the Elm endpoint/effect/callback and a render function. Rather than fork the ticket-core page, add a self-contained view module the ticket page (ticket-core / delivery-outcomes) calls; wire the fetch through the standard Effect/Callback pipeline. Gateway cross-provider metering shows up automatically as `subagent.call` / `subagent.result` keys in `event_counts` (§5), so no gateway-specific code is needed — the panel renders whatever event types the row carries.

**Files:**
- Create: `web/elm/src/AgentMetricsPanel/AgentMetricsPanel.elm` (pure view: `view : List Concourse.AgentMetrics.RunMetrics -> Html msg`)
- Modify: `web/elm/src/Api/Endpoints.elm` (add `TicketAgentMetrics Int` endpoint + path)
- Modify: `web/elm/src/Message/Effects.elm` (add `FetchTicketAgentMetrics Int` + run block)
- Modify: `web/elm/src/Message/Callback.elm` (add `TicketAgentMetricsFetched (Fetched (List Concourse.AgentMetrics.RunMetrics))`)
- Test: `web/elm/tests/AgentMetricsPanel/AgentMetricsPanelTests.elm`

**Steps:**

- [ ] **Step 1: Write the failing elm-test** `web/elm/tests/AgentMetricsPanel/AgentMetricsPanelTests.elm` (uses `Test.Html.Query` as the reviews page tests do):

```elm
module AgentMetricsPanel.AgentMetricsPanelTests exposing (all)

import AgentMetricsPanel.AgentMetricsPanel as Panel
import Concourse.AgentMetrics exposing (RunMetrics)
import Test exposing (Test, describe, test)
import Test.Html.Query as Query
import Test.Html.Selector exposing (text)


sample : List RunMetrics
sample =
    [ { buildId = 9
      , planId = "p"
      , stepName = "implement"
      , status = "ok"
      , model = "claude"
      , turns = 20
      , wallTimeSeconds = 61
      , costUsd = 2.0
      , summary = ""
      , eventCounts = [ ( "tool.call", 87 ), ( "subagent.call", 3 ) ]
      }
    ]


all : Test
all =
    describe "AgentMetricsPanel"
        [ test "renders each step's turns, cost, and top event counts" <|
            \_ ->
                Panel.view sample
                    |> Query.fromHtml
                    |> Query.has [ text "implement", text "20", text "tool.call" ]
        , test "renders an empty-state when there are no metrics rows" <|
            \_ ->
                Panel.view []
                    |> Query.fromHtml
                    |> Query.has [ text "No agent steps recorded" ]
        ]
```

- [ ] **Step 2: Run test to verify it fails.** Run: `cd web/elm && npx elm-test tests/AgentMetricsPanel/AgentMetricsPanelTests.elm; cd ../..` — Expected: FAIL (module `AgentMetricsPanel.AgentMetricsPanel` does not exist).

- [ ] **Step 3: Write `web/elm/src/AgentMetricsPanel/AgentMetricsPanel.elm`:**

```elm
module AgentMetricsPanel.AgentMetricsPanel exposing (view)

import Concourse.AgentMetrics exposing (RunMetrics)
import Html exposing (Html, div, span, table, tbody, td, text, th, thead, tr)
import Html.Attributes exposing (class, style)


view : List RunMetrics -> Html msg
view metrics =
    if List.isEmpty metrics then
        div [ class "agent-metrics-empty" ] [ text "No agent steps recorded for this ticket yet." ]

    else
        table [ class "agent-metrics-panel", style "width" "100%" ]
            [ thead []
                [ tr []
                    [ th [] [ text "Step" ]
                    , th [] [ text "Status" ]
                    , th [] [ text "Turns" ]
                    , th [] [ text "Wall (s)" ]
                    , th [] [ text "Cost (USD)" ]
                    , th [] [ text "Where the turns went" ]
                    ]
                ]
            , tbody [] (List.map row metrics)
            ]


row : RunMetrics -> Html msg
row m =
    tr []
        [ td [] [ text m.stepName ]
        , td [ class ("status-" ++ m.status) ] [ text m.status ]
        , td [] [ text (String.fromInt m.turns) ]
        , td [] [ text (String.fromInt m.wallTimeSeconds) ]
        , td [] [ text (formatUsd m.costUsd) ]
        , td [] [ eventBreakdown m.eventCounts ]
        ]


eventBreakdown : List ( String, Int ) -> Html msg
eventBreakdown counts =
    let
        sorted =
            List.sortBy (\( _, n ) -> negate n) counts
                |> List.take 5
    in
    if List.isEmpty sorted then
        text "—"

    else
        span []
            (List.map
                (\( k, n ) ->
                    span [ class "event-chip", style "margin-right" "8px" ]
                        [ text (k ++ " ×" ++ String.fromInt n) ]
                )
                sorted
            )


formatUsd : Float -> String
formatUsd v =
    "$" ++ String.fromFloat (toFloat (round (v * 10000)) / 10000)
```

- [ ] **Step 4: Run test to verify it passes.** Run: `cd web/elm && npx elm-test tests/AgentMetricsPanel/AgentMetricsPanelTests.elm; cd ../..` — Expected: PASS.

- [ ] **Step 5: Add the endpoint** in `web/elm/src/Api/Endpoints.elm`. In the `Endpoint` union near `TeamAgentReviews`/`AgentFeedback`, add `| TicketAgentMetrics Int`; and in the path-builder `case` (near `BuildAgentReviews`), add:

```elm
        TicketAgentMetrics ticketId ->
            base |> appendPath [ "agent", "tickets", String.fromInt ticketId, "metrics" ]
```

- [ ] **Step 6: Add the callback** in `web/elm/src/Message/Callback.elm`, next to `TeamAgentReviewsFetched`:

```elm
    | TicketAgentMetricsFetched (Fetched (List Concourse.AgentMetrics.RunMetrics))
```

Add `import Concourse.AgentMetrics` if not already present.

- [ ] **Step 7: Add the effect** in `web/elm/src/Message/Effects.elm`. In the `Effect` union near `FetchTeamAgentReviews`, add `| FetchTicketAgentMetrics Int`; and in the `run` case (near `FetchTeamAgentReviews`), add:

```elm
        FetchTicketAgentMetrics ticketId ->
            Api.get (Endpoints.TicketAgentMetrics ticketId)
                |> Api.expectJson (Json.Decode.list Concourse.AgentMetrics.decode)
                |> Api.request
                |> Task.attempt TicketAgentMetricsFetched
```

Add `import Concourse.AgentMetrics` if not already present.

- [ ] **Step 8: Verify the whole Elm app still compiles** (union additions force exhaustiveness in any `case` over `Callback`/`Effect`; the standard build catches gaps). Run: `cd web/elm && npx elm make src/Main.elm --output=/dev/null; cd ../..` — Expected: compile succeeds. If a `Callback` `case` in a page module now warns as non-exhaustive, add a `TicketAgentMetricsFetched _ -> ( model, effects )` no-op branch there (the ticket page consumes it in the integration step below; other pages ignore it).

- [ ] **Step 9: Consume the panel on the ticket page.** In the ticket page module (`web/elm/src/AgentTickets/AgentTicket.elm`, landed by ticket-core), add: on `init` (or when the ticket id is known) emit `FetchTicketAgentMetrics model.ticketId`; store the result in a `metrics : List Concourse.AgentMetrics.RunMetrics` model field on `TicketAgentMetricsFetched (Ok rows)`; and render `AgentMetricsPanel.view model.metrics` in the page body under a "Run metrics" heading. Because the ticket page's exact model/update shape is landed by ticket-core (not this plan), match its existing pattern: find where it stores `tasks` from its 5s poll and add `metrics` alongside with the same wiring. Re-fetch metrics on the same poll tick as the task list.

- [ ] **Step 10: Run the full Elm test + build.** Run: `cd web/elm && npx elm-test && npx elm make src/Main.elm --output=/dev/null; cd ../..` — Expected: all PASS, build clean.

- [ ] **Step 11: Commit.**

```bash
git add web/elm/src/AgentMetricsPanel web/elm/src/Api/Endpoints.elm web/elm/src/Message/Effects.elm web/elm/src/Message/Callback.elm web/elm/src/AgentTickets/AgentTicket.elm web/elm/tests/AgentMetricsPanel
git commit -m "feat(web): per-step 'where did the turns go' metrics panel on the ticket page"
```

---

### Task 9: Elm — `Scorecards/Scorecards.elm` version-comparison page + routing

The side-by-side scorecard page: one column per requested version, every rate shown with its count in parentheses (small-team honesty), the ok/failed/error split visible, dark outcome cells rendered "—". Patterned on `AgentReviews/AgentReviews.elm` (a `Login.Model` extension with `init`/`view`/`handleCallback`/`subscriptions`). Route: `/workflows/:name/scorecard?versions=3,4`.

**Files:**
- Create: `web/elm/src/Scorecards/Scorecards.elm`
- Modify: `web/elm/src/Api/Endpoints.elm` (add `WorkflowScorecard { name : String, versions : String }`)
- Modify: `web/elm/src/Message/Effects.elm` (add `FetchWorkflowScorecard { name, versions }` + run block)
- Modify: `web/elm/src/Message/Callback.elm` (add `WorkflowScorecardFetched (Fetched Concourse.Scorecard.Scorecard)`)
- Modify: `web/elm/src/Routes.elm` (add the `Scorecards` route + parser + toString + page title/section handling — mirror the four `AgentReviews` clauses)
- Modify: `web/elm/src/SubPage/SubPage.elm` (add `ScorecardsModel`, `init` dispatch, `handleCallback`, `view`, `subscriptions` — mirror the `AgentReviews` clauses)
- Test: `web/elm/tests/Scorecards/ScorecardsPageTests.elm`

**Steps:**

- [ ] **Step 1: Write the failing elm-test** `web/elm/tests/Scorecards/ScorecardsPageTests.elm`. This drives the **whole application** through the same `Common.init` / `Application.handleCallback` / `Common.queryView` harness the existing `AgentReviewsPageTests.elm` uses (no `Session` stub — the app builds its own) — so it simultaneously exercises the Routes parser, the SubPage dispatch, and the page view added in this task:

```elm
module Scorecards.ScorecardsPageTests exposing (all)

import Application.Application as Application
import Common
import Concourse.Scorecard exposing (Scorecard, VerdictDistribution, VersionColumn)
import Data
import Message.Callback as Callback
import Message.Effects as Effects
import Test exposing (Test, describe, test)
import Test.Html.Query as Query
import Test.Html.Selector exposing (containing, text)
import Url


emptyVerdicts : VerdictDistribution
emptyVerdicts =
    VerdictDistribution 0 0 0 0 0 0


col : Int -> VersionColumn
col v =
    { version = v
    , contentHash = "h"
    , live = v == 3
    , ticketCount = 2
    , stepCount = 3
    , stepOk = 2
    , stepFailed = 1
    , stepError = 0
    , costUsdTotal = 3.5
    , costUsdPerTicket = Just 1.75
    , turnsTotal = 35
    , turnsPerTicket = Just 17.5
    , gateReviewCount = 2
    , gatePassCount = 1
    , gatePassRate = Just 0.5
    , findingsTotal = 6
    , findingsPerTicket = Just 3.0
    , judgeScoredCount = 2
    , judgeScoreMeanPct = Just 60.0
    , verdictDistribution = emptyVerdicts
    , outcomeCount = Nothing
    , mergedCount = Nothing
    , mergeRate = Nothing
    , mergedUntouchedCount = Nothing
    , mergedUntouchedRate = Nothing
    , sentBackCount = Nothing
    , sentBackRate = Nothing
    , humanLinesTotal = Nothing
    , timeToMergeMedianHrs = Nothing
    }


sampleScorecard : Scorecard
sampleScorecard =
    Scorecard "standard-dev" [ col 3, col 4 ]


all : Test
all =
    describe "scorecards page"
        [ test "fetches the scorecard on load with the requested versions" <|
            \_ ->
                Application.init Data.flags
                    { protocol = Url.Http
                    , host = ""
                    , port_ = Nothing
                    , path = "/workflows/standard-dev/scorecard"
                    , query = Just "versions=3,4"
                    , fragment = Nothing
                    }
                    |> Tuple.second
                    |> Common.contains
                        (Effects.FetchWorkflowScorecard { name = "standard-dev", versions = "3,4" })
        , test "shows gate pass rate with its count denominator" <|
            \_ ->
                Common.init "/workflows/standard-dev/scorecard?versions=3,4"
                    |> Application.handleCallback
                        (Callback.WorkflowScorecardFetched (Ok sampleScorecard))
                    |> Tuple.first
                    |> Common.queryView
                    |> Query.has
                        [ containing [ text "50%" ]
                        , containing [ text "(1/2)" ]
                        ]
        , test "renders a dark cell for unfilled outcome columns" <|
            \_ ->
                Common.init "/workflows/standard-dev/scorecard?versions=3,4"
                    |> Application.handleCallback
                        (Callback.WorkflowScorecardFetched (Ok sampleScorecard))
                    |> Tuple.first
                    |> Common.queryView
                    |> Query.has [ containing [ text "—" ] ]
        , test "renders an error notice when the scorecard fails to load" <|
            \_ ->
                Common.init "/workflows/standard-dev/scorecard?versions=3,4"
                    |> Application.handleCallback
                        (Callback.WorkflowScorecardFetched Data.httpUnauthorized)
                    |> Tuple.first
                    |> Common.queryView
                    |> Query.has [ containing [ text "Failed to load scorecard." ] ]
        ]
```

This is the exact harness `AgentReviewsPageTests.elm` uses: the first test asserts the load effect fires (via `Common.contains`, matching that file's `Effects.FetchTeamAgentReviews "main"` assertion), and the render tests drive the callback through `Application.handleCallback` then inspect `Common.queryView`. No page-internal `Session` fixture is constructed — the `Application` builds its own from `Data.flags`.

- [ ] **Step 2: Run test to verify it fails.** Run: `cd web/elm && npx elm-test tests/Scorecards/ScorecardsPageTests.elm; cd ../..` — Expected: FAIL (module `Scorecards.Scorecards` does not exist, and the `/workflows/.../scorecard` route is unknown to `Routes`/`SubPage`).

- [ ] **Step 3: Write `web/elm/src/Scorecards/Scorecards.elm`** (structure mirrors `AgentReviews/AgentReviews.elm`; `view` returns a `Browser.Document`-style record `{ title, body }` as that page does — match its exact return type):

```elm
module Scorecards.Scorecards exposing
    ( Model
    , documentTitle
    , handleCallback
    , init
    , subscriptions
    , tooltip
    , update
    , view
    )

import Application.Models exposing (Session)
import Concourse.Scorecard as Scorecard exposing (Scorecard, VersionColumn)
import EffectTransformer exposing (ET)
import Html exposing (Html, div, table, tbody, td, text, th, thead, tr)
import Html.Attributes exposing (class, style)
import Login.Login as Login
import Message.Callback exposing (Callback(..))
import Message.Effects exposing (Effect(..))
import Message.Message exposing (Message)
import Message.Subscription exposing (Subscription)
import Tooltip


type alias Model =
    Login.Model
        { workflowName : String
        , versions : String
        , scorecard : Maybe Scorecard
        , loaded : Bool
        , loadError : Bool
        }


init : { workflowName : String, versions : String } -> ( Model, List Effect )
init { workflowName, versions } =
    ( { workflowName = workflowName
      , versions = versions
      , scorecard = Nothing
      , loaded = False
      , loadError = False
      , isUserMenuExpanded = False
      }
    , [ FetchWorkflowScorecard { name = workflowName, versions = versions } ]
    )


documentTitle : String
documentTitle =
    "Workflow scorecard"


handleCallback : Callback -> ET Model
handleCallback callback ( model, effects ) =
    case callback of
        WorkflowScorecardFetched (Ok sc) ->
            ( { model | scorecard = Just sc, loaded = True }, effects )

        WorkflowScorecardFetched (Err _) ->
            ( { model | loaded = True, loadError = True }, effects )

        _ ->
            ( model, effects )


update : Message -> ET Model
update _ ( model, effects ) =
    ( model, effects )


tooltip : Model -> a -> Maybe Tooltip.Tooltip
tooltip _ _ =
    Nothing


subscriptions : List Subscription
subscriptions =
    []


view : Session -> Model -> { title : String, body : List (Html Message) }
view _ model =
    { title = documentTitle
    , body =
        [ div [ class "scorecard-page", style "padding" "20px" ]
            [ Html.h1 [] [ text ("Scorecard — " ++ model.workflowName) ]
            , case model.scorecard of
                Just sc ->
                    scorecardTable sc

                Nothing ->
                    if model.loadError then
                        div [] [ text "Failed to load scorecard." ]

                    else
                        div [] [ text "Loading…" ]
            ]
        ]
    }


scorecardTable : Scorecard -> Html Message
scorecardTable sc =
    let
        cols =
            sc.columns

        headerRow =
            tr [] (th [] [ text "Metric" ] :: List.map versionHeader cols)

        rows =
            [ metricRow "Tickets" (\c -> String.fromInt c.ticketCount) cols
            , metricRow "Steps ok / failed / error"
                (\c -> String.fromInt c.stepOk ++ " / " ++ String.fromInt c.stepFailed ++ " / " ++ String.fromInt c.stepError)
                cols
            , metricRow "Cost / ticket (USD)" (\c -> maybeMoney c.costUsdPerTicket) cols
            , metricRow "Turns / ticket" (\c -> maybeNum c.turnsPerTicket) cols
            , metricRow "Gate pass rate"
                (\c -> rateWithCount c.gatePassRate c.gatePassCount c.gateReviewCount)
                cols
            , metricRow "Findings / ticket" (\c -> maybeNum c.findingsPerTicket) cols
            , metricRow "Judge score mean"
                (\c -> maybePct c.judgeScoreMeanPct ++ " " ++ countParen c.judgeScoredCount)
                cols
            , metricRow "Verdicts (acc/fp/noisy/strict/partial/missed)" verdictCell cols
            , metricRow "Merge rate"
                (\c -> maybeRateWithCount c.mergeRate c.mergedCount c.outcomeCount)
                cols
            , metricRow "Merged untouched"
                (\c -> maybeRateWithCount c.mergedUntouchedRate c.mergedUntouchedCount c.outcomeCount)
                cols
            , metricRow "Sent back"
                (\c -> maybeRateWithCount c.sentBackRate c.sentBackCount c.outcomeCount)
                cols
            , metricRow "Human-touch (lines)" (\c -> maybeIntText c.humanLinesTotal) cols
            , metricRow "Time to merge (median hrs)" (\c -> maybeNum c.timeToMergeMedianHrs) cols
            ]
    in
    table [ class "scorecard-table" ] [ thead [] [ headerRow ], tbody [] rows ]


versionHeader : VersionColumn -> Html Message
versionHeader c =
    th []
        [ text ("v" ++ String.fromInt c.version)
        , if c.live then
            Html.span [ class "live-badge", style "margin-left" "6px" ] [ text "live" ]

          else
            text ""
        ]


metricRow : String -> (VersionColumn -> String) -> List VersionColumn -> Html Message
metricRow label cell cols =
    tr [] (td [ class "metric-label" ] [ text label ] :: List.map (\c -> td [] [ text (cell c) ]) cols)


verdictCell : VersionColumn -> String
verdictCell c =
    let
        d =
            c.verdictDistribution
    in
    if d.accurate + d.falsePositive + d.noisy + d.overlyStrict + d.partiallyCorrect + d.missedContext == 0 then
        "—"

    else
        String.join "/"
            (List.map String.fromInt
                [ d.accurate, d.falsePositive, d.noisy, d.overlyStrict, d.partiallyCorrect, d.missedContext ]
            )



-- Formatting: rates ALWAYS carry their count denominator (small-team honesty).


rateWithCount : Maybe Float -> Int -> Int -> String
rateWithCount rate num denom =
    case rate of
        Just r ->
            pct r ++ " (" ++ String.fromInt num ++ "/" ++ String.fromInt denom ++ ")"

        Nothing ->
            "—"


maybeRateWithCount : Maybe Float -> Maybe Int -> Maybe Int -> String
maybeRateWithCount rate num denom =
    case ( rate, num, denom ) of
        ( Just r, Just n, Just d ) ->
            pct r ++ " (" ++ String.fromInt n ++ "/" ++ String.fromInt d ++ ")"

        _ ->
            "—"


countParen : Int -> String
countParen n =
    "(" ++ String.fromInt n ++ ")"


pct : Float -> String
pct r =
    String.fromInt (round (r * 100)) ++ "%"


maybePct : Maybe Float -> String
maybePct m =
    case m of
        Just v ->
            String.fromInt (round v) ++ "%"

        Nothing ->
            "—"


maybeNum : Maybe Float -> String
maybeNum m =
    case m of
        Just v ->
            String.fromFloat (toFloat (round (v * 10)) / 10)

        Nothing ->
            "—"


maybeMoney : Maybe Float -> String
maybeMoney m =
    case m of
        Just v ->
            "$" ++ String.fromFloat (toFloat (round (v * 100)) / 100)

        Nothing ->
            "—"


maybeIntText : Maybe Int -> String
maybeIntText m =
    Maybe.map String.fromInt m |> Maybe.withDefault "—"
```

- [ ] **Step 4: Add the endpoint** in `web/elm/src/Api/Endpoints.elm`. Add `| WorkflowScorecard { name : String, versions : String }` to the `Endpoint` union (near `TeamAgentReviews`/`AgentFeedback`). **Note:** `Api.get` passes `query = []` to `Endpoints.toString`, so a `GET` cannot carry a query param through the call site — the `versions` query must be baked into the endpoint clause's own `RouteBuilder` via `appendQuery` + `Url.Builder.string` (the `appendQuery` helper is already imported from `RouteBuilder`; `Url.Builder` is imported as `Url.Builder` — confirm the alias with `grep "import Url.Builder" web/elm/src/Api/Endpoints.elm`). Add the clause:

```elm
        WorkflowScorecard { name, versions } ->
            base
                |> appendPath [ "agent", "workflows", name, "scorecard" ]
                |> appendQuery [ Url.Builder.string "versions" versions ]
```

- [ ] **Step 5: Add the callback** in `web/elm/src/Message/Callback.elm`, near `TeamAgentReviewsFetched`:

```elm
    | WorkflowScorecardFetched (Fetched Concourse.Scorecard.Scorecard)
```

Add `import Concourse.Scorecard`.

- [ ] **Step 6: Add the effect** in `web/elm/src/Message/Effects.elm`. Add `| FetchWorkflowScorecard { name : String, versions : String }` to the union, and in `run`:

```elm
        FetchWorkflowScorecard params ->
            Api.get (Endpoints.WorkflowScorecard params)
                |> Api.expectJson Concourse.Scorecard.decode
                |> Api.request
                |> Task.attempt WorkflowScorecardFetched
```

Add `import Concourse.Scorecard`.

- [ ] **Step 7: Add the route** in `web/elm/src/Routes.elm`. Four edits, parallel to the existing `AgentReviews` clauses (which use `RouteBuilder.build`, `s`/`string`, and — for other routes — `<?> Query.string` with `Query.map (Maybe.withDefault "")` for optional string queries; verified idioms at :197–:200, :276, :595, :315):

  1. Add the variant to the `Route` union (near `| AgentReviews { teamName : String }` at :61):

```elm
    | Scorecards { workflowName : String, versions : String }
```

  2. Add the parser (near `agentReviews` at :315). `versions` is a required-but-string query param; read it with `Query.string` defaulted to `""` (the page's `init` re-validates it, and the handler 400s on empty):

```elm
scorecards : Parser ((b -> Route) -> a) a
scorecards =
    map (\name versions -> always <| Scorecards { workflowName = name, versions = versions })
        (s "workflows" </> string </> s "scorecard" <?> (Query.string "versions" |> Query.map (Maybe.withDefault "")))
```

  Then add `scorecards` to the top-level `oneOf` parser list where `agentReviews` is registered (grep `agentReviews` in the `oneOf`).

  3. Add the `toString` clause (near `AgentReviews { teamName } ->` at :597). The `RouteBuilder` tuple's second element is `List Url.Builder.QueryParameter` (not `(String,String)` pairs); add the query via `appendQuery` + `Builder.string`, exactly as the `Pipeline`/`Dashboard` clauses do (`Builder` is the `Url.Builder` import already in the file):

```elm
        Scorecards { workflowName, versions } ->
            ( [ "workflows", workflowName, "scorecard" ], [] )
                |> appendQuery [ Builder.string "versions" versions ]
                |> RouteBuilder.build
```

  4. Add the two remaining `AgentReviews`-parallel clauses at the later `case` sites (:708 and :742 — one returns the page's section/breadcrumb, the other its top-bar handling). Copy exactly what the `AgentReviews _ ->` clause returns at each site:

```elm
        Scorecards _ ->
            -- copy the body of the adjacent `AgentReviews _ ->` clause verbatim
```

- [ ] **Step 8: Wire the SubPage** in `web/elm/src/SubPage/SubPage.elm`. Mirror every `AgentReviews` clause: add `import Scorecards.Scorecards as Scorecards`; add `| ScorecardsModel Scorecards.Model` to the page model union; add the `Routes.Scorecards params -> Scorecards.init params |> Tuple.mapFirst ScorecardsModel` init clause; add the `handleCallback`, `view`, `subscriptions`, `update`, and `tooltip` delegation clauses for `ScorecardsModel` (copy the `AgentReviewsModel` clauses exactly, swapping the module name).

- [ ] **Step 9: Compile + test.** Run: `cd web/elm && npx elm make src/Main.elm --output=/dev/null && npx elm-test tests/Scorecards/ScorecardsPageTests.elm; cd ../..` — Expected: compile succeeds, page test PASS. Fix any non-exhaustive `case` warnings the two new union variants surface (each is a mechanical `ScorecardsModel ... -> ...` or `Scorecards _ -> ...` branch parallel to the `AgentReviews` one).

- [ ] **Step 10: Run the full Elm test suite + build.** Run: `cd web/elm && npx elm-test && npx elm make src/Main.elm --output=/dev/null; cd ../..` — Expected: all PASS, build clean.

- [ ] **Step 11: Commit.**

```bash
git add web/elm/src/Scorecards web/elm/src/Api/Endpoints.elm web/elm/src/Message/Effects.elm web/elm/src/Message/Callback.elm web/elm/src/Routes.elm web/elm/src/SubPage/SubPage.elm web/elm/tests/Scorecards
git commit -m "feat(web): workflow scorecard side-by-side comparison page (counts alongside rates, dark outcome cells)"
```

---

### Task 10: Migration `1773106110` — `(workflow_version, day)` covering indexes

The charter's "indexes by (workflow_version, day) from day one". Additive index-only migration (no schema change) over the two tables the scorecard rollups scan by workflow version and time: `agent_run_metrics` (already has `agent_run_metrics_workflow` on `(workflow_name, workflow_version)` but nothing time-keyed) and `agent_reviews` (harvest rows). Block allocated in Task 1. Uses `date(created_at)` expression indexes so day-window rollups (and process-intel-experiments' time-series) hit an index.

**Files:**
- Create: `atc/db/migration/migrations/1773106110_add_scorecard_indexes.up.sql`
- Create: `atc/db/migration/migrations/1773106110_add_scorecard_indexes.down.sql`
- Modify: `atc/db/migration/legacy_upgrade_test.go:37` (`jetbridgeHeadMigration` const)
- Test: `ginkgo --focus='Legacy Database Upgrade' ./atc/db/migration/`

**Steps:**

- [ ] **Step 1: Write `1773106110_add_scorecard_indexes.up.sql`:**

```sql
-- Scorecard rollups scan agent_run_metrics by (workflow_name, workflow_version)
-- and window by day; add a covering index that keys workflow version with the
-- day bucket so version-over-time rollups (scorecards + process intelligence)
-- hit an index rather than a seq scan. agent_run_metrics_workflow already
-- exists on (workflow_name, workflow_version) — this adds the time dimension.
CREATE INDEX agent_run_metrics_workflow_day
    ON agent_run_metrics (workflow_name, workflow_version, (date(created_at)));

-- Harvest evidence rows are read per ticket for gate/judge rollups; index the
-- day bucket so time-windowed scorecard queries over reviews are indexed.
CREATE INDEX agent_reviews_ticket_day
    ON agent_reviews (ticket_id, (date(created_at)))
    WHERE ticket_id IS NOT NULL;
```

- [ ] **Step 2: Write `1773106110_add_scorecard_indexes.down.sql`:**

```sql
DROP INDEX agent_reviews_ticket_day;
DROP INDEX agent_run_metrics_workflow_day;
```

- [ ] **Step 3: Bump the head constant.** In `atc/db/migration/legacy_upgrade_test.go:37`, set `jetbridgeHeadMigration = 1773106110` **only if the current value is lower**. Wave-mates delivery-outcomes (`1773106090`) and dispatch may have set it to their number first; scorecards' `1773106110` is the highest in wave 4, so it will win — but never lower it if a later value is already present.

- [ ] **Step 4: Run the legacy-upgrade suite.** Run: `pg_isready && ginkgo --focus='Legacy Database Upgrade' ./atc/db/migration/` — Expected: PASS (the suite migrates an empty and a fixture DB to HEAD; a SQL error in the index DDL or a stale head constant fails here). Also re-run the scorecard store suite to confirm the indexes don't change results: `ginkgo --focus="ScorecardStore" ./atc/db/` — Expected: PASS.

- [ ] **Step 5: Commit.**

```bash
git add atc/db/migration
git commit -m "feat(db): scorecard (workflow_version, day) covering indexes (migration 1773106110)"
```

---

## Execution notes

**Running this workstream's test suite** (PostgreSQL must be running — `pg_isready`):

- Go domain + handler: `go test ./agent/api/scorecards/`
- DB aggregate store + migration: `ginkgo --focus="ScorecardStore" ./atc/db/` and `ginkgo --focus='Legacy Database Upgrade' ./atc/db/migration/`
- Auth wiring: `ginkgo ./atc/wrappa/`
- Whole-build sanity after the route wiring (Task 6): `go build ./...`
- Elm: `cd web/elm && npx elm-test && npx elm make src/Main.elm --output=/dev/null` (elm-test compiles every module; the whole-app `elm make` catches non-exhaustive `case` gaps the new `Callback`/`Effect`/`Route`/SubPage union variants introduce).
- Full unit tier (per CLAUDE.md): `make test-unit` — do NOT pass `--race` (parallel-compilation failures). The `atc/db` suite uses the `testdb_template` template DB; if you see `database "testdb_template" already exists`, another test process is live — wait for it.

**Live-test requirements:** none. Scorecards is read-only over existing tables + Elm; it needs no jetbridge pod, no cluster, no theborg run. Validation against real data on theborg is manual/observational after the wave lands (open the scorecard page for a workflow with ≥2 versions), not an automated live test in this plan.

**Rollback notes for the risky diffs:**
- The **route + handler wiring** (Task 6) touches five shared files (`atc/routes.go`, `atc/api/handler.go`, `atc/wrappa/api_auth_wrappa.go`, `atc/api/accessor/roles.go`, `atc/atccmd/command.go`) that dispatch and delivery-outcomes also append to. All edits are **append-only** (one route constant, one route row, one case-list entry, one role entry, one handler param + map entry, one store construction) — a merge conflict is a mechanical re-add, never a semantic change. To back out: revert the Task 6 commit; the `agent/api/scorecards` package and DB store remain compilable and simply unrouted.
- The **migration** (Task 10) is index-only and fully reversible via its `.down.sql`; dropping the two indexes changes performance only, never correctness. It is the highest wave-4 number (`1773106110`); if it merges/deploys before a lower wave-4 migration, the version-pointer migrator will still apply the lower ones on the next deploy only if they land first — so merge wave-4 branches in migration-number order (`…090` delivery-outcomes → dispatch's block → `…110` scorecards) before any theborg deploy, per the wave-1 migration-ordering addendum.
- The **`agent_outcomes` LEFT JOIN** (Task 5) is guarded by `to_regclass('agent_outcomes') IS NOT NULL` — if delivery-outcomes' `1773106090` is not yet deployed, outcome columns render dark and the scorecard still returns. No rollback needed for the ordering; the guard is the safety.
- The **Elm ticket-page integration** (Task 8, Step 9) is the only edit to a file this plan does not own (`AgentTickets/AgentTicket.elm`, ticket-core's). It adds a model field, a fetch on the existing poll, and a panel render — additive. To back out: remove the `metrics` field, the `FetchTicketAgentMetrics` emit, and the `AgentMetricsPanel.view` call; the panel module and its route remain unused but compilable.
