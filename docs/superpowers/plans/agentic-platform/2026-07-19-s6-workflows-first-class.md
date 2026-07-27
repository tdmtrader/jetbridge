# S-6 Workflows as First-Class Objects Implementation Plan

> **SUPERSEDED — historical implementation record (not executable).** The approved [Agentic Workflows as Functions over Snapshots design](../../specs/2026-07-21-agentic-workflows-as-functions-design.md) and [Agentic Functions program plan](../2026-07-21-agentic-functions-program.md) are authoritative. This workflow-detail-page proposal targeted ticket-composed workflow definitions; workflows are now snapshot-keyed functions — see the 2026-07-21 design.

> For agentic workers: REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox syntax.

**Goal:** Give every agent workflow a first-class detail page — step DAG, prompt text, gate policy, budget, version history with structural diffs, live-version promotion, and per-workflow run statistics — plus the server aggregation and the describe/annotate + deprecate/hide lifecycle verbs the API currently lacks.

**Architecture:** A new per-version stats aggregation over `agent_run_metrics` (grouped by `workflow_version`, joined to `builds` for outcome truth) is exposed as `GET /api/v1/agent/workflows/:workflow_name/stats`. Two lifecycle verbs — annotate (operator note) and hide/deprecate — are added as `PUT /api/v1/agent/workflows/:workflow_name`, backed by a new name-keyed `agent_workflow_lifecycle` table (migration slot FLAGGED, not numbered — see Open Decisions). The Elm client gains a `WorkflowDetail` route/page that composes the existing `versions` + `version` reads with the new `stats` read, renders the DAG/prompts/gates/budget from the definition `Config`, computes a structural diff against the predecessor version, and drives promotion + lifecycle mutations. The compiled `elm.min.js` is rebuilt and committed.

**Tech Stack:** Go (atc/db squirrel factory, agent/api handlers, atc/wrappa auth, atc/routes rata routes), fly CLI (raw `agentAPIRequest` HTTP, no go-concourse client method — workflows bypass go-concourse), Elm 0.19 (web/elm), Ginkgo (wrappa specs) + plain Go `testing` (handler/factory/fly-integration).

---

## File Structure

| File | Create/Modify | Responsibility |
|------|---------------|----------------|
| `agent/schema/workflow_stats.go` | Create | `WorkflowVersionStats` wire/row struct + `WithDerived()` computing success rate / avg cost / avg turns |
| `agent/schema/workflow_stats_test.go` | Create | Unit-tests the derived-field math |
| `agent/api/metrics/types.go` | Modify | Add `WorkflowStats(name string)` to the `Store` interface |
| `atc/db/agent_run_metrics_factory.go` | Modify | Implement `WorkflowStats` (grouped aggregation query) |
| `atc/db/agent_run_metrics_factory_test.go` | Modify (file already exists) | Append the WorkflowStats aggregation Describe + the `createBuildWithStatus` helper |
| `agent/workflow/definition.go` | Modify | Add `Hidden`/`Annotation` to `Definition`; add `Annotate`/`SetHidden` to `Store` |
| `agent/workflow/memory_store.go` | Modify | Implement `Annotate`/`SetHidden`; surface hidden/annotation in List/Versions/Get |
| `agent/workflow/memory_store_test.go` | Modify | Unit-test the lifecycle memory-store behavior |
| `atc/db/migration/migrations/<SLOT>_create_agent_workflow_lifecycle.up.sql` | Create | New name-keyed lifecycle table (SLOT flagged, not numbered) |
| `atc/db/migration/migrations/<SLOT>_create_agent_workflow_lifecycle.down.sql` | Create | Drop the lifecycle table |
| `atc/db/agent_workflows_factory.go` | Modify | Implement `Annotate`/`SetHidden`; LEFT JOIN lifecycle in read paths |
| `atc/db/agent_workflows_factory_test.go` | Create/Modify | Integration test for lifecycle persistence + list join |
| `agent/api/workflows/handler.go` | Modify | Add `Stats` + `Update` handlers; add `hidden`/`annotation` to `WorkflowSummary`; add `StatsProvider` dep |
| `agent/api/workflows/handler_test.go` | Modify | Handler tests for stats + lifecycle update |
| `agent/api/workflows/route_registration_test.go` | Modify | Assert the two new routes are registered |
| `atc/routes.go` | Modify | Add `GetAgentWorkflowStats` + `UpdateAgentWorkflow` consts + route entries |
| `atc/wrappa/api_auth_wrappa.go` | Modify | Add stats to the team-less viewer block; update = human-only viewer block |
| `atc/wrappa/api_auth_wrappa_test.go` | Modify | Tier-pinning specs incl. "REJECTS bare tickets:read principal" for the update route |
| `atc/api/handler.go` | Modify | Wire `workflowsServer` with the metrics store; register the two new handlers |
| `atc/atccmd/command.go` | Modify | Pass the metrics factory into the workflows handler construction if needed |
| `fly/commands/agent_workflows.go` | Modify | Add `stats`, `annotate`, `deprecate`, `restore` subcommands |
| `fly/integration/agent_workflows_test.go` | Modify | Integration specs for the four new subcommands |
| `web/elm/src/Routes.elm` | Modify | Add `AgentWorkflow { name }` route (parse/build/title/page-map) |
| `web/elm/src/Api/Endpoints.elm` | Modify | Add version/versions/stats/lifecycle/promote endpoints |
| `web/elm/src/Concourse/Agent.elm` | Modify | Add `WorkflowDefinition`/`WorkflowVersionStats`/`Config` decoders |
| `web/elm/src/Message/Effects.elm` | Modify | Add fetch/mutate effects for the detail page |
| `web/elm/src/Message/Callback.elm` | Modify | Add the corresponding callbacks |
| `web/elm/src/Message/Message.elm` | Modify | Add page-local messages (version select, promote, annotate, hide) |
| `web/elm/src/AgentWorkflow/AgentWorkflow.elm` | Create | The workflow detail page module |
| `web/elm/src/SubPage/SubPage.elm` | Modify | Wire the new page into init/update/callback/delivery/view/tooltip/subscriptions |
| `web/elm/src/Agent/Agent.elm` | Modify | Link each workflow row name to its detail route |
| `web/public/elm.min.js` | Modify (regenerated) | Rebuilt compiled bundle — MUST be committed |

---

## Coordination Preconditions (read before Task 1)

- **Migration slot:** head is `1773106090` (`ls atc/db/migration/migrations/ | grep '^17731' | sort | tail`). judge-evidence (`…080`) and delivery-outcomes (`…090`) have ALREADY LANDED — they are not in-flight and do not reserve future slots. Slots between the last shipped agentic migration (`1773106066`) and the head are FREE: `1773106067` is currently unused and is the intended slot for this plan's one migration (the lifecycle table). platform-mcp (`…070-072`) is the only remaining in-flight reservation to avoid. **Do NOT hard-code a number.** Before Task 1.1, re-grep `atc/db/migration/migrations/` for the current lowest-unused slot at or above `1773106067`, FLAG it in the remainders README registry row, and confirm no other in-flight track has taken it. Substitute it for `<SLOT>` everywhere in Task 1.1.
- **No `render.go` edits.** This track never touches `agent/dispatch/render.go`.
- **Additive wire only.** Every new JSON field is `omitempty` / defaulted so older clients and servers interoperate.
- **Elm bundle.** There is NO local elm-build gate today (WF-2 adds one). The final task MUST rebuild and commit `web/public/elm.min.js`, or the deployed web serves a stale bundle.

---

## Phase 0 — Per-version stats aggregation (no migration)

### Task 0.1 — `WorkflowVersionStats` schema + derived math

**Files:**
- Create `agent/schema/workflow_stats.go`
- Test `agent/schema/workflow_stats_test.go`

Steps:

- [ ] Write the failing test `agent/schema/workflow_stats_test.go`:

```go
package schema

import (
	"math"
	"testing"
)

func TestWorkflowVersionStatsWithDerived(t *testing.T) {
	v := 3
	s := WorkflowVersionStats{
		Version:       &v,
		Runs:          4,
		Tickets:       3,
		SucceededRuns: 3,
		TotalCostUSD:  8.00,
		TotalTurns:    40,
	}.WithDerived()

	if math.Abs(s.SuccessRate-0.75) > 1e-9 {
		t.Errorf("SuccessRate = %v, want 0.75", s.SuccessRate)
	}
	if math.Abs(s.AvgCostUSD-2.00) > 1e-9 {
		t.Errorf("AvgCostUSD = %v, want 2.00", s.AvgCostUSD)
	}
	if math.Abs(s.AvgTurns-10.0) > 1e-9 {
		t.Errorf("AvgTurns = %v, want 10.0", s.AvgTurns)
	}
}

func TestWorkflowVersionStatsZeroRunsIsSafe(t *testing.T) {
	s := WorkflowVersionStats{Runs: 0, SucceededRuns: 0, TotalCostUSD: 0, TotalTurns: 0}.WithDerived()
	if s.SuccessRate != 0 || s.AvgCostUSD != 0 || s.AvgTurns != 0 {
		t.Errorf("zero-run stats must derive to 0, got %+v", s)
	}
}
```

- [ ] Run it, expect FAIL: `cd /Users/tdmtrader/concourse/concourse && go test ./agent/schema/ -run TestWorkflowVersionStats` → `undefined: WorkflowVersionStats`.

- [ ] Minimal implementation `agent/schema/workflow_stats.go`:

```go
package schema

// WorkflowVersionStats is one row of the per-workflow, per-version run
// aggregation over agent_run_metrics (S-6). The "run" unit is a distinct
// build_id: a dispatched ticket run is one build with many agent-step rows,
// so cost/turns are summed across the build's steps and averaged per build,
// and success is the build's own terminal status (joined from builds), never
// a single green step. Version is a pointer because pre-workflow / ad-hoc CI
// rows carry a NULL workflow_version and aggregate into their own bucket.
type WorkflowVersionStats struct {
	Version       *int    `json:"version"`
	Runs          int     `json:"runs"`
	Tickets       int     `json:"tickets"`
	SucceededRuns int     `json:"succeeded_runs"`
	TotalCostUSD  float64 `json:"total_cost_usd"`
	TotalTurns    int     `json:"total_turns"`

	// Derived, filled by WithDerived (0 when Runs == 0).
	SuccessRate float64 `json:"success_rate"`
	AvgCostUSD  float64 `json:"avg_cost_usd"`
	AvgTurns    float64 `json:"avg_turns"`
}

// WithDerived returns a copy with SuccessRate/AvgCostUSD/AvgTurns computed
// from the raw counters. Zero Runs derives every ratio to 0 (no divide-by-zero,
// no NaN on the wire).
func (s WorkflowVersionStats) WithDerived() WorkflowVersionStats {
	if s.Runs > 0 {
		s.SuccessRate = float64(s.SucceededRuns) / float64(s.Runs)
		s.AvgCostUSD = s.TotalCostUSD / float64(s.Runs)
		s.AvgTurns = float64(s.TotalTurns) / float64(s.Runs)
	}
	return s
}
```

- [ ] Run it, expect PASS: `go test ./agent/schema/ -run TestWorkflowVersionStats` → `ok`.

- [ ] Commit:

```bash
git add agent/schema/workflow_stats.go agent/schema/workflow_stats_test.go
git commit -m "feat(agent): WorkflowVersionStats schema + derived stat math"
```

### Task 0.2 — `WorkflowStats` store method + aggregation query

**Files:**
- Modify `agent/api/metrics/types.go` (interface)
- Modify `atc/db/agent_run_metrics_factory.go` (implementation)
- Modify `atc/db/agent_run_metrics_factory_test.go` (integration; file already exists — append the WorkflowStats block + `createBuildWithStatus` helper)

Steps:

- [ ] Add the method to the `Store` interface in `agent/api/metrics/types.go`, immediately after the `ListRecent` doc block (before the closing brace of `type Store interface`):

```go
	// WorkflowStats returns one aggregation row per distinct workflow_version
	// for the named workflow, newest version first (NULL version last). The
	// "run" unit is a distinct build_id; success is counted from the joined
	// builds.status = 'succeeded'. Rows carry only the raw counters — callers
	// call schema.WorkflowVersionStats.WithDerived for the ratios.
	WorkflowStats(workflowName string) ([]schema.WorkflowVersionStats, error)
```

- [ ] Append the failing integration test to the existing `atc/db/agent_run_metrics_factory_test.go` (do NOT create a new file — it already holds the `AgentRunMetricsFactory` specs). It follows the existing Ginkgo db-suite convention (`dbConn`, `defaultTeam`); the build fixture reuses the `defaultTeam.CreateOneOffBuild()` + `Finish` path the specs already in this file use:

```go
package db_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/concourse/concourse/atc/db"
	agentschema "github.com/concourse/concourse/agent/schema"
)

var _ = Describe("AgentRunMetricsFactory WorkflowStats", func() {
	var factory db.AgentRunMetricsFactory

	BeforeEach(func() {
		factory = db.NewAgentRunMetricsFactory(dbConn)
	})

	insert := func(buildID int, plan string, ver int, ticket int, status string, cost float64, turns int) {
		v := ver
		tk := ticket
		Expect(factory.Upsert(&agentschema.RunMetrics{
			BuildID: buildID, PlanID: plan, StepName: "s", TicketID: &tk,
			WorkflowName: "wf-stats", WorkflowVersion: &v,
			Status: status, CostUSD: cost, Turns: turns,
		})).To(Succeed())
	}

	It("aggregates per version by distinct build, joining build status for success", func() {
		// Two builds on v3: one whose build row is 'succeeded', one 'failed'.
		b1 := createBuildWithStatus(db.BuildStatusSucceeded) // helper added to this file (below)
		b2 := createBuildWithStatus(db.BuildStatusFailed)
		insert(b1, "p1", 3, 100, "ok", 1.50, 5)
		insert(b1, "p2", 3, 100, "ok", 0.50, 3) // 2 steps, same build → 1 run
		insert(b2, "p1", 3, 101, "failed", 2.00, 8)

		stats, err := factory.WorkflowStats("wf-stats")
		Expect(err).NotTo(HaveOccurred())
		Expect(stats).To(HaveLen(1))

		s := stats[0]
		Expect(*s.Version).To(Equal(3))
		Expect(s.Runs).To(Equal(2))          // distinct builds
		Expect(s.Tickets).To(Equal(2))       // distinct tickets
		Expect(s.SucceededRuns).To(Equal(1)) // only b1 succeeded
		Expect(s.TotalCostUSD).To(BeNumerically("~", 4.00, 1e-6))
		Expect(s.TotalTurns).To(Equal(16))
	})
})
```

> NOTE: `atc/db/agent_run_metrics_factory_test.go` ALREADY EXISTS in the suite (it holds the existing `AgentRunMetricsFactory` upsert/outcome specs). This task APPENDS a new `Describe("AgentRunMetricsFactory WorkflowStats", …)` block to that file rather than creating it — merge the imports (`agentschema "github.com/concourse/concourse/agent/schema"` is already imported there as `schema`; reuse the existing alias instead of adding a second one).

> The `createBuildWithStatus` helper does NOT exist in the suite today — it MUST be authored before the aggregation test above will compile. Do NOT model it on `agent_ticket_attribution_test.go` (that test fakes a `BuildID: 424242` with no real `builds` row, which the LEFT JOIN to `builds` would never match). Instead model it on the real build-with-status path ALREADY used in this same file (the "joins the pipeline build status" / "derives the outcome across the succeeded/open build matrix" specs): `defaultTeam.CreateOneOffBuild()` then `build.Finish(status)`. Add this helper to the file (package-level, so both the existing specs and the new block can share it):

```go
// createBuildWithStatus creates a real one-off build under the suite's
// defaultTeam and finishes it with the given terminal status, returning its
// id. WorkflowStats LEFT JOINs builds for success truth, so the fixture needs
// a REAL builds row (a fabricated BuildID like the attribution test's 424242
// would never match the join). Mirrors the CreateOneOffBuild + Finish path the
// existing specs in this file already use.
func createBuildWithStatus(status db.BuildStatus) int {
	build, err := defaultTeam.CreateOneOffBuild()
	Expect(err).ToNot(HaveOccurred())
	Expect(build.Finish(status)).To(Succeed())
	return build.ID()
}
```

> Then change the two call sites in the aggregation test to pass the typed status: `createBuildWithStatus(db.BuildStatusSucceeded)` and `createBuildWithStatus(db.BuildStatusFailed)` (not the bare strings `"succeeded"`/`"failed"`).

- [ ] Run it, expect FAIL: `ginkgo --focus="WorkflowStats" ./atc/db/` → compile error `factory.WorkflowStats undefined`.

- [ ] Implement `WorkflowStats` in `atc/db/agent_run_metrics_factory.go` (append after `ListRecent`):

```go
// WorkflowStats aggregates agent_run_metrics per workflow_version for one
// workflow. The run unit is a distinct build_id; cost/turns are summed across
// the build's step rows (the LEFT JOIN to builds is 1:1 so there is no
// fan-out) and success is counted from the joined build's terminal status.
// NULL workflow_version rows (ad-hoc CI) aggregate into their own bucket and
// sort last.
func (f *agentRunMetricsFactory) WorkflowStats(workflowName string) ([]agentschema.WorkflowVersionStats, error) {
	rows, err := f.conn.Query(`
		SELECT
			m.workflow_version,
			COUNT(DISTINCT m.build_id)                                        AS runs,
			COUNT(DISTINCT m.ticket_id) FILTER (WHERE m.ticket_id IS NOT NULL) AS tickets,
			COUNT(DISTINCT m.build_id) FILTER (WHERE b.status = 'succeeded')  AS succeeded_runs,
			COALESCE(SUM(m.cost_usd), 0)                                      AS total_cost_usd,
			COALESCE(SUM(m.turns), 0)                                         AS total_turns
		FROM agent_run_metrics m
		LEFT JOIN builds b ON b.id = m.build_id
		WHERE m.workflow_name = $1
		GROUP BY m.workflow_version
		ORDER BY m.workflow_version DESC NULLS LAST`, workflowName)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []agentschema.WorkflowVersionStats{}
	for rows.Next() {
		var s agentschema.WorkflowVersionStats
		var version sql.NullInt64
		if err := rows.Scan(&version, &s.Runs, &s.Tickets, &s.SucceededRuns, &s.TotalCostUSD, &s.TotalTurns); err != nil {
			return nil, err
		}
		if version.Valid {
			v := int(version.Int64)
			s.Version = &v
		}
		out = append(out, s)
	}
	return out, rows.Err()
}
```

- [ ] Regenerate the metrics-store counterfeiter fake so the new interface method is on the fake (the fake backs handler tests elsewhere):

```bash
cd /Users/tdmtrader/concourse/concourse && go generate ./agent/api/metrics/...
```

- [ ] Run it, expect PASS: `ginkgo --focus="WorkflowStats" ./atc/db/` → `ok`.

- [ ] Commit:

```bash
git add agent/api/metrics/types.go agent/api/metrics/metricsfakes/ atc/db/agent_run_metrics_factory.go atc/db/agent_run_metrics_factory_test.go
git commit -m "feat(agent)+feat(db): WorkflowStats per-version aggregation over agent_run_metrics"
```

### Task 0.3 — `GetAgentWorkflowStats` route + handler + auth

**Files:**
- Modify `atc/routes.go`
- Modify `agent/api/workflows/handler.go`
- Modify `agent/api/workflows/handler_test.go`
- Modify `agent/api/workflows/route_registration_test.go`
- Modify `atc/wrappa/api_auth_wrappa.go`
- Modify `atc/api/handler.go`

Steps:

- [ ] Add the route const + entry in `atc/routes.go`. Add the const to the workflow const block (after `PromoteAgentWorkflowVersion`):

```go
	GetAgentWorkflowStats       = "GetAgentWorkflowStats"
```

and the route entry after the `…/versions/:version/live` PUT entry:

```go
	{Path: "/api/v1/agent/workflows/:workflow_name/stats", Method: "GET", Name: GetAgentWorkflowStats},
```

- [ ] Add a `StatsProvider` dependency + `Stats` handler in `agent/api/workflows/handler.go`. Define the narrow interface (kept local to the workflows package so the handler test can pass a tiny fake, and the DB `AgentRunMetricsFactory` satisfies it in production):

```go
// StatsProvider is the read the Stats handler needs — a subset of
// agent/api/metrics.Store, satisfied in production by
// atc/db.AgentRunMetricsFactory.
type StatsProvider interface {
	WorkflowStats(workflowName string) ([]schema.WorkflowVersionStats, error)
}
```

Add `stats StatsProvider` to `Handler`, extend `NewHandler`:

```go
func NewHandler(store workflow.Store, stats StatsProvider) *Handler {
	return &Handler{store: store, stats: stats}
}
```

Add the handler (import `schema "github.com/concourse/concourse/agent/schema"`):

```go
// Stats handles GET /api/v1/agent/workflows/:workflow_name/stats. Returns the
// per-version aggregation with the derived ratios filled in. A workflow with
// no runs returns [] (200), not 404 — the workflow may exist with zero runs.
func (h *Handler) Stats(w http.ResponseWriter, r *http.Request) {
	name := r.FormValue(":workflow_name")
	rows, err := h.stats.WorkflowStats(name)
	if err != nil {
		http.Error(w, "failed to load workflow stats", http.StatusInternalServerError)
		return
	}
	out := make([]schema.WorkflowVersionStats, len(rows))
	for i, row := range rows {
		out[i] = row.WithDerived()
	}
	writeJSON(w, http.StatusOK, out)
}
```

- [ ] Update the two existing `NewHandler` call sites' expectations. In `agent/api/workflows/handler_test.go`, change `newHandler` to construct a stats fake:

```go
type fakeStats struct {
	rows []schema.WorkflowVersionStats
	err  error
}

func (f fakeStats) WorkflowStats(string) ([]schema.WorkflowVersionStats, error) {
	return f.rows, f.err
}

func newHandler(t *testing.T) (*workflows.Handler, *workflow.MemoryStore) {
	t.Helper()
	store := workflow.NewMemoryStore()
	return workflows.NewHandler(store, fakeStats{}), store
}
```

Add a stats handler test:

```go
func TestStatsReturnsDerivedRows(t *testing.T) {
	store := workflow.NewMemoryStore()
	v := 2
	h := workflows.NewHandler(store, fakeStats{rows: []schema.WorkflowVersionStats{
		{Version: &v, Runs: 4, SucceededRuns: 3, Tickets: 3, TotalCostUSD: 8, TotalTurns: 40},
	}})

	w := httptest.NewRecorder()
	h.Stats(w, request("GET", "/api/v1/agent/workflows/wf/stats",
		url.Values{":workflow_name": {"wf"}}, ""))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	var got []schema.WorkflowVersionStats
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].SuccessRate != 0.75 || got[0].AvgTurns != 10 {
		t.Errorf("derived rows = %+v", got)
	}
}
```

(Add `schema "github.com/concourse/concourse/agent/schema"` to the test imports.)

- [ ] Add the route to `route_registration_test.go`'s `required` slice:

```go
		{atc.GetAgentWorkflowStats, "GET", "/api/v1/agent/workflows/:workflow_name/stats"},
```

- [ ] Add `atc.GetAgentWorkflowStats` to the team-less viewer block in `atc/wrappa/api_auth_wrappa.go` — put it in the same `case` list as `ListAgentWorkflows` (the `CheckAgentAuthorizationHandler` block, lines ~209-236), right after `PromoteAgentWorkflowVersion`:

```go
			atc.PromoteAgentWorkflowVersion,
			atc.GetAgentWorkflowStats,
```

- [ ] Wire the handler in `atc/api/handler.go`. Change the `workflowsServer` construction (line ~177) to pass the metrics store (`metricsStore` is already in scope from `metricsServer := metricsapi.NewHandler(metricsStore)` at line 169):

```go
	workflowsServer := workflowsapi.NewHandler(workflowStore, metricsStore)
```

and register the handler in the handlers map (after `PromoteAgentWorkflowVersion`):

```go
			atc.GetAgentWorkflowStats:        http.HandlerFunc(workflowsServer.Stats),
```

> Confirm `metricsStore`'s concrete type satisfies `workflows.StatsProvider` — it is `atc/db.AgentRunMetricsFactory`, which now has `WorkflowStats`. If `metricsStore` is a narrower interface at that call site, widen the local variable's type or pass `db.NewAgentRunMetricsFactory(dbConn)` directly. Grep for where `metricsStore` is declared in `handler.go` and confirm before editing.

- [ ] Run the unit tests, expect PASS:

```bash
go test ./agent/api/workflows/... && go build ./atc/... && ginkgo --focus="Stats route" ./atc/wrappa/
```

- [ ] Commit:

```bash
git add atc/routes.go agent/api/workflows/ atc/wrappa/api_auth_wrappa.go atc/api/handler.go
git commit -m "feat(agent): GET workflow stats route, handler, viewer-tier auth"
```

---

## Phase 1 — Lifecycle verbs: annotate + hide/deprecate (needs migration)

### Task 1.1 — Lifecycle migration (FLAG the slot first)

**Files:**
- Create `atc/db/migration/migrations/<SLOT>_create_agent_workflow_lifecycle.up.sql`
- Create `atc/db/migration/migrations/<SLOT>_create_agent_workflow_lifecycle.down.sql`

Steps:

- [ ] Determine the slot: `ls atc/db/migration/migrations/ | grep '^17731' | sort | tail -5`. As of this writing the head is `1773106090` (judge-evidence `…080` and delivery-outcomes `…090` have LANDED), and `1773106067` is unused — pick it, or the lowest unused number at or above `1773106067` that no still-in-flight track (platform-mcp 070-072) has claimed. **FLAG it** in `docs/superpowers/plans/agentic-platform/remainders/README.md` (add a registry row: `<SLOT> — S-6 agent_workflow_lifecycle`). Substitute the chosen number for `<SLOT>` below. **STOP and surface the chosen slot in the Open Decisions hand-off if any ambiguity remains.**

- [ ] Create the up migration `<SLOT>_create_agent_workflow_lifecycle.up.sql`:

```sql
-- S-6: workflow-name-level lifecycle metadata. Distinct from
-- agent_workflow_definitions (which is per-version and whose `description`
-- is derived from the version's YAML): annotation is a human operator note,
-- hidden deprecates a workflow from default listings without deleting its
-- versions. Keyed by name; a row is created lazily on first annotate/hide.
CREATE TABLE agent_workflow_lifecycle (
    name       TEXT PRIMARY KEY,
    hidden     BOOLEAN NOT NULL DEFAULT false,
    annotation TEXT NOT NULL DEFAULT '',
    updated_by TEXT NOT NULL DEFAULT '',
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
```

- [ ] Create the down migration `<SLOT>_create_agent_workflow_lifecycle.down.sql`:

```sql
DROP TABLE agent_workflow_lifecycle;
```

- [ ] Verify the migration parses/applies by running the db migration suite once:

```bash
ginkgo --focus="migration" ./atc/db/migration/ 2>&1 | tail -20
```

(Expected: existing migration tests still pass; the new files are picked up by the embedded FS.)

- [ ] Commit:

```bash
git add atc/db/migration/migrations/<SLOT>_create_agent_workflow_lifecycle.up.sql atc/db/migration/migrations/<SLOT>_create_agent_workflow_lifecycle.down.sql docs/superpowers/plans/agentic-platform/remainders/README.md
git commit -m "feat(db): agent_workflow_lifecycle table (name-keyed hidden/annotation)"
```

### Task 1.2 — Definition fields + Store interface + MemoryStore

**Files:**
- Modify `agent/workflow/definition.go`
- Modify `agent/workflow/memory_store.go`
- Modify `agent/workflow/memory_store_test.go`

Steps:

- [ ] Write the failing memory-store test in `agent/workflow/memory_store_test.go`:

```go
func TestMemoryStoreAnnotateAndHide(t *testing.T) {
	m := NewMemoryStore()
	if _, err := m.Import("wf", []byte(validMemYAML), "importer"); err != nil {
		t.Fatal(err)
	}

	if err := m.Annotate("wf", "prefer for hotfixes", "alice"); err != nil {
		t.Fatal(err)
	}
	if err := m.SetHidden("wf", true, "alice"); err != nil {
		t.Fatal(err)
	}

	defs, err := m.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(defs) != 1 || defs[0].Annotation != "prefer for hotfixes" || !defs[0].Hidden {
		t.Fatalf("list did not surface lifecycle: %+v", defs)
	}

	if err := m.Annotate("nope", "x", "alice"); err != ErrVersionNotFound {
		t.Errorf("Annotate on unknown workflow = %v, want ErrVersionNotFound", err)
	}
}
```

> Reuse the existing `validMemYAML`/equivalent const already in `memory_store_test.go`; if none, add one mirroring `validYAML` from `handler_test.go`.

- [ ] Run it, expect FAIL: `go test ./agent/workflow/ -run TestMemoryStoreAnnotateAndHide` → `m.Annotate undefined` / `defs[0].Annotation undefined`.

- [ ] Add fields to `Definition` in `agent/workflow/definition.go` (after `CreatedAt`):

```go
	// Hidden/Annotation are workflow-NAME-level lifecycle metadata (S-6),
	// stored in agent_workflow_lifecycle and joined onto every version row on
	// read. Hidden deprecates a workflow from default listings; Annotation is
	// a human operator note distinct from the per-version YAML Description.
	Hidden     bool   `json:"hidden"`
	Annotation string `json:"annotation,omitempty"`
```

Add to the `Store` interface:

```go
	// Annotate sets the workflow's operator note (name-level). Returns
	// ErrVersionNotFound if no version of name exists.
	Annotate(name, annotation, updatedBy string) error
	// SetHidden deprecates (hidden=true) or restores (false) a workflow from
	// default listings. Returns ErrVersionNotFound if no version exists.
	SetHidden(name string, hidden bool, updatedBy string) error
```

- [ ] Implement in `agent/workflow/memory_store.go`. Add a lifecycle map to the struct:

```go
type MemoryStore struct {
	mu        sync.Mutex
	nextID    int
	defs      []*Definition
	lifecycle map[string]struct {
		hidden     bool
		annotation string
	}
}
```

Add the methods and surface lifecycle in `List`/`Versions`/`Get`/`Latest`/`Live` by decorating the returned copies:

```go
func (m *MemoryStore) exists(name string) bool {
	for _, d := range m.defs {
		if d.Name == name {
			return true
		}
	}
	return false
}

func (m *MemoryStore) Annotate(name, annotation, updatedBy string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.exists(name) {
		return ErrVersionNotFound
	}
	if m.lifecycle == nil {
		m.lifecycle = map[string]struct {
			hidden     bool
			annotation string
		}{}
	}
	e := m.lifecycle[name]
	e.annotation = annotation
	m.lifecycle[name] = e
	_ = updatedBy
	return nil
}

func (m *MemoryStore) SetHidden(name string, hidden bool, updatedBy string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.exists(name) {
		return ErrVersionNotFound
	}
	if m.lifecycle == nil {
		m.lifecycle = map[string]struct {
			hidden     bool
			annotation string
		}{}
	}
	e := m.lifecycle[name]
	e.hidden = hidden
	m.lifecycle[name] = e
	_ = updatedBy
	return nil
}

func (m *MemoryStore) decorate(d Definition) Definition {
	if e, ok := m.lifecycle[d.Name]; ok {
		d.Hidden = e.hidden
		d.Annotation = e.annotation
	}
	return d
}
```

Then in each read method that returns `cp := *d`, replace the return with `m.decorate(cp)` (List loop, Versions loop, Get, Latest, Live). For example in `List`, change `out = append(out, cp)` to `out = append(out, m.decorate(cp))`.

- [ ] Run it, expect PASS: `go test ./agent/workflow/ -run TestMemoryStoreAnnotateAndHide` → `ok`.

- [ ] Regenerate the workflow.Store counterfeiter fake:

```bash
go generate ./agent/workflow/... && go generate ./atc/db/...
```

- [ ] Run the whole workflow package + build the db package:

```bash
go test ./agent/workflow/... && go build ./atc/db/...
```

(Building `atc/db` will FAIL until Task 1.3 implements the new methods on `agentWorkflowsFactory` — that is expected; proceed to 1.3.)

- [ ] Commit:

```bash
git add agent/workflow/ atc/db/dbfakes/
git commit -m "feat(agent): workflow lifecycle fields + Annotate/SetHidden on Store and MemoryStore"
```

### Task 1.3 — DB factory: Annotate/SetHidden + lifecycle join on reads

**Files:**
- Modify `atc/db/agent_workflows_factory.go`
- Create `atc/db/agent_workflows_factory_test.go`

Steps:

- [ ] Write the failing integration test `atc/db/agent_workflows_factory_test.go`:

```go
package db_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/concourse/concourse/atc/db"
)

var _ = Describe("AgentWorkflowsFactory lifecycle", func() {
	var factory db.AgentWorkflowsFactory

	const yaml = `schema_version: 1
name: lc-wf
description: lifecycle test
prompts:
  work: "do it"
steps:
- agent: work
  prompt: work
  outputs: [workspace]
`

	BeforeEach(func() {
		factory = db.NewAgentWorkflowsFactory(dbConn)
		_, err := factory.Import("lc-wf", []byte(yaml), "importer")
		Expect(err).NotTo(HaveOccurred())
	})

	It("persists annotation and hidden and surfaces them in List", func() {
		Expect(factory.Annotate("lc-wf", "hotfix workhorse", "alice")).To(Succeed())
		Expect(factory.SetHidden("lc-wf", true, "alice")).To(Succeed())

		defs, err := factory.List()
		Expect(err).NotTo(HaveOccurred())
		var found bool
		for _, d := range defs {
			if d.Name == "lc-wf" {
				found = true
				Expect(d.Annotation).To(Equal("hotfix workhorse"))
				Expect(d.Hidden).To(BeTrue())
			}
		}
		Expect(found).To(BeTrue())
	})

	It("returns ErrVersionNotFound for an unknown workflow", func() {
		Expect(factory.Annotate("ghost", "x", "alice")).To(MatchError(db.ErrAgentWorkflowNotFound))
	})
})
```

> The interface returns `workflow.ErrVersionNotFound`; assert against that exact error rather than a db-local alias if none exists. Confirm the exported name before writing the matcher — use `workflow.ErrVersionNotFound` (import `github.com/concourse/concourse/agent/workflow`).

- [ ] Run it, expect FAIL: `ginkgo --focus="AgentWorkflowsFactory lifecycle" ./atc/db/` → `factory.Annotate undefined`.

- [ ] Implement in `atc/db/agent_workflows_factory.go`. Add the upsert methods:

```go
func (f *agentWorkflowsFactory) Annotate(name, annotation, updatedBy string) error {
	return f.upsertLifecycle(name, "annotation", annotation, updatedBy)
}

func (f *agentWorkflowsFactory) SetHidden(name string, hidden bool, updatedBy string) error {
	return f.upsertLifecycle(name, "hidden", hidden, updatedBy)
}

// upsertLifecycle sets one lifecycle column, refusing to touch a workflow that
// has no versions (ErrVersionNotFound). The row is created lazily.
func (f *agentWorkflowsFactory) upsertLifecycle(name, column string, value any, updatedBy string) error {
	var exists bool
	err := f.conn.QueryRow(
		`SELECT EXISTS (SELECT 1 FROM agent_workflow_definitions WHERE name = $1)`, name,
	).Scan(&exists)
	if err != nil {
		return err
	}
	if !exists {
		return workflow.ErrVersionNotFound
	}
	// column is a fixed literal ("annotation"|"hidden"), never user input.
	_, err = f.conn.Exec(`
		INSERT INTO agent_workflow_lifecycle (name, `+column+`, updated_by, updated_at)
		VALUES ($1, $2, $3, now())
		ON CONFLICT (name) DO UPDATE SET
			`+column+` = EXCLUDED.`+column+`,
			updated_by = EXCLUDED.updated_by,
			updated_at = now()`,
		name, value, updatedBy)
	return err
}
```

Then LEFT JOIN the lifecycle table into the read paths. Update `workflowMetaColumns` and the query bodies. Because `getOne` and the list/versions queries currently select `workflowMetaColumns` without an alias, introduce an aliased column set and join:

```go
const workflowMetaColumns = `d.id, d.name, d.version, d.content_hash, d.live, d.description, d.created_by,
	EXTRACT(EPOCH FROM d.created_at)::bigint,
	COALESCE(l.hidden, false), COALESCE(l.annotation, '')`

const workflowMetaFrom = ` FROM agent_workflow_definitions d
	LEFT JOIN agent_workflow_lifecycle l ON l.name = d.name`
```

Update every SELECT that used the bare table to use `d`/the join, and every `Scan` to add `&def.Hidden, &def.Annotation`. Specifically:
- `ImportManifest`'s idempotent-hit SELECT (add the join + two scan targets).
- `getOne` (add join; note it also selects `definition, source_manifest` → qualify as `d.definition, d.source_manifest`).
- `List` (`SELECT DISTINCT ON (d.name) …` + join + `ORDER BY d.name, d.version DESC`).
- `Versions` (join + `WHERE d.name = $1`).
- `scanWorkflowMetaRows` (add `&def.Hidden, &def.Annotation`).

> This is a mechanical rename (bare column → `d.`-qualified). Do it carefully; run `go build ./atc/db/` after each edit. Every SELECT that scans `workflowMetaColumns` MUST scan the two extra columns, or `Scan` will error at runtime.

- [ ] Run it, expect PASS: `ginkgo --focus="AgentWorkflowsFactory lifecycle" ./atc/db/` and the pre-existing `./atc/db/` workflow specs → `ok`.

- [ ] Commit:

```bash
git add atc/db/agent_workflows_factory.go atc/db/agent_workflows_factory_test.go
git commit -m "feat(db): agentWorkflowsFactory Annotate/SetHidden + lifecycle join on reads"
```

### Task 1.4 — `UpdateAgentWorkflow` route + handler + human-only auth

**Files:**
- Modify `atc/routes.go`
- Modify `agent/api/workflows/handler.go`
- Modify `agent/api/workflows/handler_test.go`
- Modify `agent/api/workflows/route_registration_test.go`
- Modify `atc/wrappa/api_auth_wrappa.go`
- Modify `atc/wrappa/api_auth_wrappa_test.go`
- Modify `atc/api/handler.go`
- Modify `agent/api/workflows/handler.go` (`WorkflowSummary` gains hidden/annotation)

Steps:

- [ ] Add const + route entry in `atc/routes.go`:

```go
	UpdateAgentWorkflow         = "UpdateAgentWorkflow"
```

```go
	{Path: "/api/v1/agent/workflows/:workflow_name", Method: "PUT", Name: UpdateAgentWorkflow},
```

- [ ] Add hidden/annotation to `WorkflowSummary` in `handler.go` and populate them in `Summarize` (the `List` path already reads `Definition` which now carries them):

```go
type WorkflowSummary struct {
	Name          string `json:"name"`
	Description   string `json:"description"`
	Annotation    string `json:"annotation,omitempty"`
	Hidden        bool   `json:"hidden"`
	LatestVersion int    `json:"latest_version"`
	ContentHash   string `json:"content_hash"`
	LiveVersion   int    `json:"live_version"`
	CreatedAt     int64  `json:"created_at"`
}
```

In `Summarize`, add `Annotation: d.Annotation, Hidden: d.Hidden,` to the appended struct.

- [ ] Add the `Update` handler to `handler.go`:

```go
// updateBody is the PUT /api/v1/agent/workflows/:workflow_name payload. Each
// field is a pointer so a caller can patch annotation OR hidden independently;
// a nil field is left unchanged.
type updateBody struct {
	Annotation *string `json:"annotation,omitempty"`
	Hidden     *bool   `json:"hidden,omitempty"`
}

// Update handles PUT /api/v1/agent/workflows/:workflow_name — the annotate /
// deprecate(hide) lifecycle verbs. Human-only (no principal tier): deprecating
// a workflow is an operator decision.
func (h *Handler) Update(w http.ResponseWriter, r *http.Request) {
	name := r.FormValue(":workflow_name")
	raw, err := io.ReadAll(io.LimitReader(r.Body, 1<<16))
	if err != nil {
		http.Error(w, "failed to read request body", http.StatusBadRequest)
		return
	}
	var body updateBody
	if err := json.Unmarshal(raw, &body); err != nil {
		http.Error(w, "malformed body: "+err.Error(), http.StatusBadRequest)
		return
	}
	if body.Annotation == nil && body.Hidden == nil {
		http.Error(w, `body must set "annotation" and/or "hidden"`, http.StatusBadRequest)
		return
	}
	user := requestUser(r)
	if body.Annotation != nil {
		if err := h.store.Annotate(name, *body.Annotation, user); err != nil {
			h.writeStoreErr(w, err)
			return
		}
	}
	if body.Hidden != nil {
		if err := h.store.SetHidden(name, *body.Hidden, user); err != nil {
			h.writeStoreErr(w, err)
			return
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) writeStoreErr(w http.ResponseWriter, err error) {
	if errors.Is(err, workflow.ErrVersionNotFound) {
		http.Error(w, "unknown workflow", http.StatusNotFound)
		return
	}
	http.Error(w, "failed to update workflow", http.StatusInternalServerError)
}
```

- [ ] Add handler tests to `handler_test.go`:

```go
func TestUpdateAnnotatesAndHides(t *testing.T) {
	h, store := newHandler(t)
	if _, err := store.Import("wf", []byte(validYAML), "importer"); err != nil {
		t.Fatal(err)
	}

	w := httptest.NewRecorder()
	h.Update(w, request("PUT", "/api/v1/agent/workflows/wf",
		url.Values{":workflow_name": {"wf"}}, `{"annotation":"note","hidden":true}`))
	if w.Code != http.StatusNoContent {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}

	defs, _ := store.List()
	if defs[0].Annotation != "note" || !defs[0].Hidden {
		t.Errorf("lifecycle not applied: %+v", defs[0])
	}
}

func TestUpdateUnknownWorkflowIs404(t *testing.T) {
	h, _ := newHandler(t)
	w := httptest.NewRecorder()
	h.Update(w, request("PUT", "/api/v1/agent/workflows/ghost",
		url.Values{":workflow_name": {"ghost"}}, `{"hidden":true}`))
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", w.Code)
	}
}

func TestUpdateEmptyBodyIs400(t *testing.T) {
	h, store := newHandler(t)
	_, _ = store.Import("wf", []byte(validYAML), "importer")
	w := httptest.NewRecorder()
	h.Update(w, request("PUT", "/api/v1/agent/workflows/wf",
		url.Values{":workflow_name": {"wf"}}, `{}`))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
}
```

- [ ] Add the route to `route_registration_test.go`:

```go
		{atc.UpdateAgentWorkflow, "PUT", "/api/v1/agent/workflows/:workflow_name"},
```

- [ ] Add `atc.UpdateAgentWorkflow` to the team-less human viewer block in `api_auth_wrappa.go` (same `CheckAgentAuthorizationHandler` case list, after `GetAgentWorkflowStats`). This is deliberately NOT the principal tier — deprecating a workflow is human-only.

- [ ] Add a tier-pinning spec in `api_auth_wrappa_test.go`. Extend the existing agent-route tier Describe (model on the `delivery-outcomes route tiers` block, lines ~256-309) so `UpdateAgentWorkflow` is wired into a `rata.Handlers` delegate and add:

```go
Describe("UpdateAgentWorkflow", func() {
	It("REJECTS a bare tickets:read agent-principal token (human-only, no principal path)", func() {
		_, token, err := store.Create(principals.CreateSpec{
			Name: "ticket-reader", Scopes: []string{principals.ScopeTicketsRead},
		})
		Expect(err).NotTo(HaveOccurred())
		fakeaccess.IsAuthenticatedReturns(false)

		resp := serve(atc.UpdateAgentWorkflow, "Bearer "+token)
		Expect(resp.StatusCode).To(Equal(http.StatusUnauthorized))
		Expect(delegateHit).To(BeFalse())
	})

	It("admits an authorized main-team member", func() {
		fakeaccess.IsAuthenticatedReturns(true)
		fakeaccess.IsAuthorizedReturns(true)

		resp := serve(atc.UpdateAgentWorkflow, "")
		Expect(resp.StatusCode).To(Equal(http.StatusOK))
		Expect(delegateHit).To(BeTrue())
	})
})
```

> Add `atc.UpdateAgentWorkflow: delegate` to whichever `rata.Handlers{…}` block the `serve` helper wraps for these specs. Confirm the `serve` helper + `fakeaccess`/`store`/`delegateHit` names against the current test file before writing.

- [ ] Register the handler in `atc/api/handler.go`:

```go
			atc.UpdateAgentWorkflow:          http.HandlerFunc(workflowsServer.Update),
```

- [ ] Run tests, expect PASS:

```bash
go test ./agent/api/workflows/... && ginkgo --focus="UpdateAgentWorkflow" ./atc/wrappa/ && go build ./atc/...
```

- [ ] Commit:

```bash
git add atc/routes.go agent/api/workflows/ atc/wrappa/ atc/api/handler.go
git commit -m "feat(agent): PUT workflow lifecycle (annotate/deprecate), human-only auth + tier pin"
```

---

## Phase 2 — fly CLI verbs

### Task 2.1 — `fly agent workflows stats | annotate | deprecate | restore`

**Files:**
- Modify `fly/commands/agent_workflows.go`
- Modify `fly/integration/agent_workflows_test.go`

Steps:

- [ ] Add the subcommands to the `AgentWorkflowsCommand` group struct:

```go
	Stats     WorkflowsStatsCommand     `command:"stats" description:"Show per-version run statistics for a workflow"`
	Annotate  WorkflowsAnnotateCommand  `command:"annotate" description:"Set an operator note on a workflow"`
	Deprecate WorkflowsDeprecateCommand `command:"deprecate" description:"Hide a workflow from default listings"`
	Restore   WorkflowsRestoreCommand   `command:"restore" description:"Un-hide a deprecated workflow"`
```

- [ ] Add the command types + a shared lifecycle PUT helper:

```go
type workflowVersionStats struct {
	Version      *int    `json:"version"`
	Runs         int     `json:"runs"`
	Tickets      int     `json:"tickets"`
	SuccessRate  float64 `json:"success_rate"`
	AvgCostUSD   float64 `json:"avg_cost_usd"`
	AvgTurns     float64 `json:"avg_turns"`
	TotalCostUSD float64 `json:"total_cost_usd"`
}

type WorkflowsStatsCommand struct {
	Args struct {
		Name string `positional-arg-name:"NAME" required:"true" description:"Workflow definition name"`
	} `positional-args:"yes"`
	Json bool `long:"json" description:"Print command result as JSON"`
}

func (command *WorkflowsStatsCommand) Execute([]string) error {
	target, err := loadAgentTarget()
	if err != nil {
		return err
	}
	resp, err := agentAPIRequest(target, "GET",
		"/api/v1/agent/workflows/"+url.PathEscape(command.Args.Name)+"/stats", nil)
	if err != nil {
		return err
	}
	var rows []workflowVersionStats
	if err := decodeOrError(resp, &rows); err != nil {
		return err
	}
	if command.Json {
		return displayhelpers.JsonPrint(rows)
	}
	table := ui.Table{Headers: ui.TableRow{
		{Contents: "version", Color: color.New(color.Bold)},
		{Contents: "runs", Color: color.New(color.Bold)},
		{Contents: "tickets", Color: color.New(color.Bold)},
		{Contents: "success", Color: color.New(color.Bold)},
		{Contents: "avg cost", Color: color.New(color.Bold)},
		{Contents: "avg turns", Color: color.New(color.Bold)},
	}}
	for _, s := range rows {
		version := "ad-hoc"
		if s.Version != nil {
			version = "v" + strconv.Itoa(*s.Version)
		}
		table.Data = append(table.Data, ui.TableRow{
			{Contents: version},
			{Contents: strconv.Itoa(s.Runs)},
			{Contents: strconv.Itoa(s.Tickets)},
			{Contents: fmt.Sprintf("%.0f%%", s.SuccessRate*100)},
			{Contents: fmt.Sprintf("$%.2f", s.AvgCostUSD)},
			{Contents: fmt.Sprintf("%.1f", s.AvgTurns)},
		})
	}
	return table.Render(os.Stdout, Fly.PrintTableHeaders)
}

func putWorkflowLifecycle(target rc.Target, name string, body map[string]any) error {
	payload, err := json.Marshal(body)
	if err != nil {
		return err
	}
	resp, err := agentAPIRequestWithType(target, "PUT",
		"/api/v1/agent/workflows/"+url.PathEscape(name),
		"application/json", bytes.NewReader(payload))
	if err != nil {
		return err
	}
	return decodeOrError(resp, nil)
}

type WorkflowsAnnotateCommand struct {
	Args struct {
		Name string `positional-arg-name:"NAME" required:"true" description:"Workflow definition name"`
		Note string `positional-arg-name:"NOTE" required:"true" description:"Operator note"`
	} `positional-args:"yes"`
}

func (command *WorkflowsAnnotateCommand) Execute([]string) error {
	target, err := loadAgentTarget()
	if err != nil {
		return err
	}
	if err := putWorkflowLifecycle(target, command.Args.Name, map[string]any{"annotation": command.Args.Note}); err != nil {
		return err
	}
	fmt.Printf("annotated %s\n", command.Args.Name)
	return nil
}

type WorkflowsDeprecateCommand struct {
	Args struct {
		Name string `positional-arg-name:"NAME" required:"true" description:"Workflow definition name"`
	} `positional-args:"yes"`
}

func (command *WorkflowsDeprecateCommand) Execute([]string) error {
	target, err := loadAgentTarget()
	if err != nil {
		return err
	}
	if err := putWorkflowLifecycle(target, command.Args.Name, map[string]any{"hidden": true}); err != nil {
		return err
	}
	fmt.Printf("deprecated %s (hidden from default listings)\n", command.Args.Name)
	return nil
}

type WorkflowsRestoreCommand struct {
	Args struct {
		Name string `positional-arg-name:"NAME" required:"true" description:"Workflow definition name"`
	} `positional-args:"yes"`
}

func (command *WorkflowsRestoreCommand) Execute([]string) error {
	target, err := loadAgentTarget()
	if err != nil {
		return err
	}
	if err := putWorkflowLifecycle(target, command.Args.Name, map[string]any{"hidden": false}); err != nil {
		return err
	}
	fmt.Printf("restored %s\n", command.Args.Name)
	return nil
}
```

- [ ] Add integration specs to `fly/integration/agent_workflows_test.go` modeled on the existing `Describe("list"…)` / `Describe("set-live"…)` blocks (which register `ghttp` handlers on the mock ATC and run the built fly binary). For `stats`:

```go
	Describe("stats", func() {
		BeforeEach(func() {
			atcServer.AppendHandlers(
				ghttp.CombineHandlers(
					ghttp.VerifyRequest("GET", "/api/v1/agent/workflows/develop/stats"),
					ghttp.RespondWithJSONEncoded(http.StatusOK, []map[string]any{
						{"version": 3, "runs": 4, "tickets": 3, "success_rate": 0.75, "avg_cost_usd": 2.0, "avg_turns": 10.0},
					}),
				),
			)
		})

		It("prints the per-version stats table", func() {
			sess := flyCmd("agent", "workflows", "stats", "develop")
			Eventually(sess).Should(gexec.Exit(0))
			Expect(sess.Out).To(gbytes.Say("v3"))
			Expect(sess.Out).To(gbytes.Say("75%"))
		})
	})
```

And an `annotate`/`deprecate` spec verifying the PUT body:

```go
	Describe("deprecate", func() {
		BeforeEach(func() {
			atcServer.AppendHandlers(
				ghttp.CombineHandlers(
					ghttp.VerifyRequest("PUT", "/api/v1/agent/workflows/develop"),
					ghttp.VerifyJSON(`{"hidden":true}`),
					ghttp.RespondWith(http.StatusNoContent, nil),
				),
			)
		})

		It("PUTs hidden=true", func() {
			sess := flyCmd("agent", "workflows", "deprecate", "develop")
			Eventually(sess).Should(gexec.Exit(0))
			Expect(sess.Out).To(gbytes.Say("deprecated develop"))
		})
	})
```

> Match the actual helper names in the file (`flyCmd`, `atcServer`, imports for `ghttp`/`gbytes`/`gexec`). Confirm by reading the existing `set-live` spec (lines ~118-157) before writing.

- [ ] Run it, expect FAIL first (commands not built), then PASS after implementation:

```bash
make test-fly-integration 2>&1 | tail -20
```

- [ ] Commit:

```bash
git add fly/commands/agent_workflows.go fly/integration/agent_workflows_test.go
git commit -m "feat(fly): agent workflows stats/annotate/deprecate/restore subcommands"
```

---

## Phase 3 — Elm workflow detail page + bundle rebuild

> Elm has no unit-test harness wired for these page modules in this repo; each Elm task's "test" is `elm make` compiling clean (the Elm type checker is the gate) plus a manual verification note. The final task rebuilds `elm.min.js`.

### Task 3.1 — Route: `AgentWorkflow { name }`

**Files:** Modify `web/elm/src/Routes.elm`

Steps:

- [ ] Add the constructor to the `Route` type union (after `AgentTicket`):

```elm
    | AgentWorkflow { name : String }
```

- [ ] Add the parser (after `agentTickets`):

```elm
agentWorkflow : Parser ((b -> Route) -> a) a
agentWorkflow =
    map (\name -> always <| AgentWorkflow { name = name })
        (s "agent-workflows" </> string)
```

- [ ] Add `agentWorkflow` to the `oneOf` parser list (after `agentTickets` at line ~507).

- [ ] Add the `toString`/`buildRoute` case (in the block near line ~618, alongside `AgentTicket { id } ->`):

```elm
        AgentWorkflow { name } ->
            Builder.absolute [ "agent-workflows", name ] []
```

> Confirm the exact builder helper the surrounding cases use (`Builder.absolute` vs a local `pathAndQuery`); mirror the `AgentTicket` case verbatim, substituting the string path segment.

- [ ] Add the `pageTitle`/title case (block near line ~741) and the sitemap/`toString` reverse case (block near line ~784), mirroring `AgentTicket _ ->`:

```elm
        AgentWorkflow _ ->
            "workflow"
```

(use whatever literal the sibling cases produce — match their shape).

- [ ] Compile-check:

```bash
cd /Users/tdmtrader/concourse/concourse/web/elm && npx elm make src/Routes.elm --output=/dev/null 2>&1 | tail -20
```

Expected: no errors, OR only errors pointing at `SubPage.elm` (fixed in Task 3.6). If `Routes.elm` itself compiles as a leaf module, expect clean.

- [ ] Commit:

```bash
git add web/elm/src/Routes.elm
git commit -m "feat(web): AgentWorkflow route"
```

### Task 3.2 — Endpoints

**Files:** Modify `web/elm/src/Api/Endpoints.elm`

Steps:

- [ ] Add the endpoint constructors to the `Endpoint` union (after `AgentWorkflowsList`):

```elm
    | AgentWorkflowVersions String
    | AgentWorkflowVersion String Int
    | AgentWorkflowStats String
    | AgentWorkflowLifecycle String
    | AgentWorkflowPromote String Int
```

- [ ] Add the path cases (after the `AgentWorkflowsList ->` case at line ~230):

```elm
        AgentWorkflowVersions name ->
            base |> appendPath [ "agent", "workflows", name, "versions" ]

        AgentWorkflowVersion name version ->
            base |> appendPath [ "agent", "workflows", name, "versions", String.fromInt version ]

        AgentWorkflowStats name ->
            base |> appendPath [ "agent", "workflows", name, "stats" ]

        AgentWorkflowLifecycle name ->
            base |> appendPath [ "agent", "workflows", name ]

        AgentWorkflowPromote name version ->
            base |> appendPath [ "agent", "workflows", name, "versions", String.fromInt version, "live" ]
```

- [ ] Compile-check `Api/Endpoints.elm`:

```bash
cd web/elm && npx elm make src/Api/Endpoints.elm --output=/dev/null 2>&1 | tail -20
```

Expected: clean.

- [ ] Commit:

```bash
git add web/elm/src/Api/Endpoints.elm
git commit -m "feat(web): workflow version/stats/lifecycle/promote endpoints"
```

### Task 3.3 — Decoders: WorkflowDefinition, Config subset, WorkflowVersionStats

**Files:** Modify `web/elm/src/Concourse/Agent.elm`

Steps:

- [ ] Add the exposed type aliases + decoders. In the module `exposing` list add: `WorkflowDefinition`, `WorkflowConfig`, `WorkflowStep`, `WorkflowGate`, `WorkflowVersionStats`, `decodeWorkflowDefinition`, `decodeWorkflowVersions`, `decodeWorkflowStats`.

- [ ] Add the types:

```elm
type alias WorkflowStep =
    { agent : String
    , checkpoint : String
    , prompt : String
    , model : String
    , maxTurns : Int
    , inputs : List String
    , outputs : List String
    , budgetSliceUsd : Float
    }


type alias WorkflowGate =
    { gate : String
    , scope : String
    , focus : String
    }


type alias WorkflowConfig =
    { schemaVersion : Int
    , name : String
    , description : String
    , specDelivery : String
    , defaultModel : String
    , defaultMaxTurns : Int
    , budgetTicketUsd : Float
    , budgetJudgeUsd : Float
    , prompts : Dict String String
    , steps : List WorkflowStep
    , gates : List WorkflowGate
    , onGateFailure : String
    }


type alias WorkflowDefinition =
    { id : Int
    , name : String
    , version : Int
    , contentHash : String
    , live : Bool
    , description : String
    , annotation : String
    , hidden : Bool
    , createdBy : String
    , createdAt : Time.Posix
    , rawYaml : String
    , config : WorkflowConfig
    }


type alias WorkflowVersionStats =
    { version : Maybe Int
    , runs : Int
    , tickets : Int
    , succeededRuns : Int
    , successRate : Float
    , avgCostUsd : Float
    , avgTurns : Float
    , totalCostUsd : Float
    }
```

- [ ] Add the decoders:

```elm
decodeWorkflowStep : Json.Decode.Decoder WorkflowStep
decodeWorkflowStep =
    Json.Decode.succeed WorkflowStep
        |> andMap (defaultTo "" <| Json.Decode.field "agent" Json.Decode.string)
        |> andMap (defaultTo "" <| Json.Decode.field "checkpoint" Json.Decode.string)
        |> andMap (defaultTo "" <| Json.Decode.field "prompt" Json.Decode.string)
        |> andMap (defaultTo "" <| Json.Decode.field "model" Json.Decode.string)
        |> andMap (defaultTo 0 <| Json.Decode.field "max_turns" Json.Decode.int)
        |> andMap (defaultTo [] <| Json.Decode.field "inputs" (Json.Decode.list Json.Decode.string))
        |> andMap (defaultTo [] <| Json.Decode.field "outputs" (Json.Decode.list Json.Decode.string))
        |> andMap (defaultTo 0 <| Json.Decode.field "budget_slice_usd" Json.Decode.float)


decodeWorkflowGate : Json.Decode.Decoder WorkflowGate
decodeWorkflowGate =
    Json.Decode.succeed WorkflowGate
        |> andMap (defaultTo "" <| Json.Decode.field "gate" Json.Decode.string)
        |> andMap (defaultTo "" <| Json.Decode.field "scope" Json.Decode.string)
        |> andMap (defaultTo "" <| Json.Decode.field "focus" Json.Decode.string)


decodeWorkflowConfig : Json.Decode.Decoder WorkflowConfig
decodeWorkflowConfig =
    Json.Decode.succeed WorkflowConfig
        |> andMap (defaultTo 1 <| Json.Decode.field "schema_version" Json.Decode.int)
        |> andMap (defaultTo "" <| Json.Decode.field "name" Json.Decode.string)
        |> andMap (defaultTo "" <| Json.Decode.field "description" Json.Decode.string)
        |> andMap (defaultTo "" <| Json.Decode.field "spec_delivery" Json.Decode.string)
        |> andMap (defaultTo "" <| Json.Decode.at [ "defaults", "model" ] Json.Decode.string)
        |> andMap (defaultTo 0 <| Json.Decode.at [ "defaults", "max_turns" ] Json.Decode.int)
        |> andMap (defaultTo 0 <| Json.Decode.at [ "budget", "ticket_usd" ] Json.Decode.float)
        |> andMap (defaultTo 0 <| Json.Decode.at [ "budget", "judge_usd" ] Json.Decode.float)
        |> andMap (defaultTo Dict.empty <| Json.Decode.field "prompts" (Json.Decode.dict Json.Decode.string))
        |> andMap (defaultTo [] <| Json.Decode.field "steps" (Json.Decode.list decodeWorkflowStep))
        |> andMap (defaultTo [] <| Json.Decode.at [ "gate_policy", "gates" ] (Json.Decode.list decodeWorkflowGate))
        |> andMap (defaultTo "" <| Json.Decode.at [ "gate_policy", "on_gate_failure" ] Json.Decode.string)


decodeWorkflowDefinition : Json.Decode.Decoder WorkflowDefinition
decodeWorkflowDefinition =
    Json.Decode.succeed WorkflowDefinition
        |> andMap (defaultTo 0 <| Json.Decode.field "id" Json.Decode.int)
        |> andMap (defaultTo "" <| Json.Decode.field "name" Json.Decode.string)
        |> andMap (defaultTo 0 <| Json.Decode.field "version" Json.Decode.int)
        |> andMap (defaultTo "" <| Json.Decode.field "content_hash" Json.Decode.string)
        |> andMap (defaultTo False <| Json.Decode.field "live" Json.Decode.bool)
        |> andMap (defaultTo "" <| Json.Decode.field "description" Json.Decode.string)
        |> andMap (defaultTo "" <| Json.Decode.field "annotation" Json.Decode.string)
        |> andMap (defaultTo False <| Json.Decode.field "hidden" Json.Decode.bool)
        |> andMap (defaultTo "" <| Json.Decode.field "created_by" Json.Decode.string)
        |> andMap (defaultTo (dateFromSeconds 0) <| Json.Decode.field "created_at" (Json.Decode.map dateFromSeconds Json.Decode.int))
        |> andMap (defaultTo "" <| Json.Decode.field "raw_yaml" Json.Decode.string)
        |> andMap (defaultTo emptyWorkflowConfig <| Json.Decode.field "config" decodeWorkflowConfig)


emptyWorkflowConfig : WorkflowConfig
emptyWorkflowConfig =
    { schemaVersion = 1, name = "", description = "", specDelivery = ""
    , defaultModel = "", defaultMaxTurns = 0, budgetTicketUsd = 0, budgetJudgeUsd = 0
    , prompts = Dict.empty, steps = [], gates = [], onGateFailure = ""
    }


decodeWorkflowVersions : Json.Decode.Decoder (List WorkflowDefinition)
decodeWorkflowVersions =
    Json.Decode.nullable (Json.Decode.list decodeWorkflowDefinition)
        |> Json.Decode.map (Maybe.withDefault [])


decodeWorkflowVersionStats : Json.Decode.Decoder WorkflowVersionStats
decodeWorkflowVersionStats =
    Json.Decode.succeed WorkflowVersionStats
        |> andMap (optionalInt "version")
        |> andMap (defaultTo 0 <| Json.Decode.field "runs" Json.Decode.int)
        |> andMap (defaultTo 0 <| Json.Decode.field "tickets" Json.Decode.int)
        |> andMap (defaultTo 0 <| Json.Decode.field "succeeded_runs" Json.Decode.int)
        |> andMap (defaultTo 0 <| Json.Decode.field "success_rate" Json.Decode.float)
        |> andMap (defaultTo 0 <| Json.Decode.field "avg_cost_usd" Json.Decode.float)
        |> andMap (defaultTo 0 <| Json.Decode.field "avg_turns" Json.Decode.float)
        |> andMap (defaultTo 0 <| Json.Decode.field "total_cost_usd" Json.Decode.float)


decodeWorkflowStats : Json.Decode.Decoder (List WorkflowVersionStats)
decodeWorkflowStats =
    Json.Decode.nullable (Json.Decode.list decodeWorkflowVersionStats)
        |> Json.Decode.map (Maybe.withDefault [])
```

- [ ] Compile-check:

```bash
cd web/elm && npx elm make src/Concourse/Agent.elm --output=/dev/null 2>&1 | tail -20
```

Expected: clean.

- [ ] Commit:

```bash
git add web/elm/src/Concourse/Agent.elm
git commit -m "feat(web): WorkflowDefinition/Config/Stats decoders"
```

### Task 3.4 — Effects + Callbacks + Messages

**Files:** Modify `web/elm/src/Message/Effects.elm`, `web/elm/src/Message/Callback.elm`, `web/elm/src/Message/Message.elm`

Steps:

- [ ] In `Message/Effects.elm` add the effect constructors (after `FetchAgentWorkflows`):

```elm
    | FetchAgentWorkflowVersions String
    | FetchAgentWorkflowVersion String Int
    | FetchAgentWorkflowStats String
    | PromoteAgentWorkflowVersion String Int
    | UpdateAgentWorkflowLifecycle String { annotation : Maybe String, hidden : Maybe Bool }
```

and the `runEffect` cases (after the `FetchAgentWorkflows ->` block at line ~820). Model the GET/PUT/POST on the existing agent effects (`FetchAgentTicket`, `SaveAgentTicket`, `TransitionAgentTicket`):

```elm
        FetchAgentWorkflowVersions name ->
            Api.get (Endpoints.AgentWorkflowVersions name)
                |> Api.expectJson Concourse.Agent.decodeWorkflowVersions
                |> Api.request
                |> Task.attempt (AgentWorkflowVersionsFetched name)

        FetchAgentWorkflowVersion name version ->
            Api.get (Endpoints.AgentWorkflowVersion name version)
                |> Api.expectJson Concourse.Agent.decodeWorkflowDefinition
                |> Api.request
                |> Task.attempt (AgentWorkflowVersionFetched name)

        FetchAgentWorkflowStats name ->
            Api.get (Endpoints.AgentWorkflowStats name)
                |> Api.expectJson Concourse.Agent.decodeWorkflowStats
                |> Api.request
                |> Task.attempt (AgentWorkflowStatsFetched name)

        PromoteAgentWorkflowVersion name version ->
            Api.put (Endpoints.AgentWorkflowPromote name version) csrfToken
                |> Api.request
                |> Task.attempt (AgentWorkflowPromoted name)

        UpdateAgentWorkflowLifecycle name patch ->
            Api.put (Endpoints.AgentWorkflowLifecycle name) csrfToken
                |> Api.withJsonBody (encodeLifecyclePatch patch)
                |> Api.request
                |> Task.attempt (AgentWorkflowLifecycleUpdated name)
```

> Verified against the existing agent effects: `Api.get` takes only the endpoint (no csrf); `Api.put`/`Api.post` take `endpoint csrfToken`; `Api.request` takes NO argument (the terminal combinator). Add a small `encodeLifecyclePatch` JSON encoder in `Effects.elm` (or inline) producing `{annotation?, hidden?}`, omitting `Nothing` fields.

- [ ] In `Message/Callback.elm` add the callbacks (after `AgentWorkflowsFetched`):

```elm
    | AgentWorkflowVersionsFetched String (Fetched (List Concourse.Agent.WorkflowDefinition))
    | AgentWorkflowVersionFetched String (Fetched Concourse.Agent.WorkflowDefinition)
    | AgentWorkflowStatsFetched String (Fetched (List Concourse.Agent.WorkflowVersionStats))
    | AgentWorkflowPromoted String (Fetched ())
    | AgentWorkflowLifecycleUpdated String (Fetched ())
```

- [ ] In `Message/Message.elm` add the page-local messages (grep for how `AgentTicket` page messages are named — e.g. a `Message` variant used via `onClick`). Add:

```elm
    | SelectWorkflowVersion Int
    | ClickPromoteWorkflowVersion String Int
    | EditWorkflowAnnotation String
    | SaveWorkflowAnnotation String
    | ClickDeprecateWorkflow String Bool
```

> Confirm whether page-local interactions in this codebase go through `Message.Message` variants or through page-owned `Msg` types. The `AgentTicket` page is the template — mirror exactly how it declares its click/input messages (it likely reuses `Message.Message` given the shared `update msg` pipeline in `SubPage`). If AgentTicket uses `Message.Message` variants, add these there; if it uses a local msg type, define them in the page module instead and skip this file.

- [ ] Compile-check the three modules (they will error until the page module in 3.5 exists and SubPage in 3.6 wires it, but syntax/type errors local to these files should be absent):

```bash
cd web/elm && npx elm make src/Message/Effects.elm --output=/dev/null 2>&1 | tail -30
```

- [ ] Commit:

```bash
git add web/elm/src/Message/Effects.elm web/elm/src/Message/Callback.elm web/elm/src/Message/Message.elm
git commit -m "feat(web): workflow-detail effects, callbacks, messages"
```

### Task 3.5 — `AgentWorkflow.AgentWorkflow` page module

**Files:** Create `web/elm/src/AgentWorkflow/AgentWorkflow.elm`

Steps:

- [ ] Create the page module. Mirror the `Agent.Agent`/`AgentTickets.AgentTicket` module surface (`Model`, `init`, `documentTitle`, `handleCallback`, `handleDelivery`, `subscriptions`, `tooltip`, `update`, `view`). `init` takes `{ name : String }`, seeds three fetches (versions, stats, and the live-or-latest full definition — begin by fetching versions, then on `AgentWorkflowVersionsFetched` pick the live-or-latest version and fetch its full definition + its predecessor for the diff).

```elm
module AgentWorkflow.AgentWorkflow exposing
    ( Model
    , documentTitle
    , handleCallback
    , handleDelivery
    , init
    , subscriptions
    , tooltip
    , update
    , view
    )

import Application.Models exposing (Session)
import Concourse.Agent as Agent
import Dict
import EffectTransformer exposing (ET)
import Html exposing (Html)
import Html.Attributes exposing (class, style)
import Html.Events exposing (onClick)
import Login.Login as Login
import Message.Callback exposing (Callback(..))
import Message.Effects exposing (Effect(..))
import Message.Message exposing (Message(..))
import Message.Subscription exposing (Delivery, Interval(..), Subscription)
import Routes
import SideBar.SideBar as SideBar
import Views.Styles


type alias Model =
    Login.Model
        { name : String
        , versions : Maybe (List Agent.WorkflowDefinition)
        , versionsError : Maybe String
        , selected : Maybe Agent.WorkflowDefinition
        , predecessor : Maybe Agent.WorkflowDefinition
        , stats : Maybe (List Agent.WorkflowVersionStats)
        , statsError : Maybe String
        , annotationDraft : Maybe String
        }


init : { name : String } -> ( Model, List Effect )
init { name } =
    ( { name = name
      , versions = Nothing
      , versionsError = Nothing
      , selected = Nothing
      , predecessor = Nothing
      , stats = Nothing
      , statsError = Nothing
      , annotationDraft = Nothing
      , isUserMenuExpanded = False
      }
    , [ FetchAgentWorkflowVersions name
      , FetchAgentWorkflowStats name
      ]
    )


documentTitle : Model -> String
documentTitle model =
    model.name ++ " · workflow"
```

> **Scope of the literal Elm above vs. below.** Only `Model`, `init`, and `documentTitle` are given as literal, paste-ready Elm. The four core functions that follow — `handleCallback`, `update`, `view` (with its DAG / prompts / gates / budget / history / stats / lifecycle rendering and the structural-diff helper), and `handleDelivery` — are specified below as **behavioral contracts the implementer authors by hand**, transcribing the corresponding function bodies from `Agent.Agent` (the named template) and adapting them to this Model. They are NOT drop-in code blocks; treat each bullet as the acceptance spec for the code you write, and compile-check with the `elm make` step at the end of the task. Budget real implementation time for these — they are the bulk of the page.

- [ ] Author `handleCallback` (transcribe the `Agent.Agent.handleCallback` shape; per-branch contract):
  - `AgentWorkflowVersionsFetched name (Ok vs)` → store `versions`; compute the target version = the `live` one, else the max `version`; if found, add `FetchAgentWorkflowVersion name target`; also add a fetch for `target - 1`'s predecessor when it exists in `vs`.
  - `AgentWorkflowVersionFetched name (Ok def)` → set `selected` when `def.version` is the current target, else `predecessor` when it is `selected.version - 1`.
  - `AgentWorkflowStatsFetched name (Ok s)` → set `stats`.
  - `AgentWorkflowPromoted name (Ok ())` → re-fetch versions (`FetchAgentWorkflowVersions model.name`) so live badges refresh.
  - `AgentWorkflowLifecycleUpdated name (Ok ())` → re-fetch versions.
  - the `Err` branches set the matching `*Error` field via a small `Http.Error -> String` helper (copy `errorMessage` from `Agent.Agent`).
  - default: `( model, effects )`.

- [ ] Author `update` handling the page messages (implementer-written body):
  - `SelectWorkflowVersion v` → set the target and issue `FetchAgentWorkflowVersion model.name v` + predecessor fetch.
  - `ClickPromoteWorkflowVersion name v` → `( model, effects ++ [ PromoteAgentWorkflowVersion name v ] )`.
  - `EditWorkflowAnnotation s` → `{ model | annotationDraft = Just s }`.
  - `SaveWorkflowAnnotation name` → issue `UpdateAgentWorkflowLifecycle name { annotation = model.annotationDraft, hidden = Nothing }`.
  - `ClickDeprecateWorkflow name hidden` → `UpdateAgentWorkflowLifecycle name { annotation = Nothing, hidden = Just hidden }`.

- [ ] Author `view session model` rendering (implementer-written body; reuse `SideBar.view` + `Views.TopBar` exactly as `Agent.Agent.view` does — copy its chrome wrapper):
  - **Header:** workflow name + hidden/deprecated badge + annotation (editable text input bound to `annotationDraft`, Save button → `SaveWorkflowAnnotation`) + a Deprecate/Restore button.
  - **Definition panel** (from `model.selected`): a horizontal **DAG preview** rendering `config.steps` as an ordered chain (each step a box showing `agent` or `checkpoint` name, model, budget slice; arrows between them), the **budget** (ticket/judge USD + default model/max turns), the **gate policy** (list of gates `gate/scope/focus` + `on_gate_failure`), and the **prompt text** (each `prompts` entry rendered in a `<pre>`).
  - **Diff line** comparing `selected` vs `predecessor`: a structural summary computed inline — step-count delta, model change, ticket-budget change, gate-count change, description change. Render "no predecessor" for v1.
  - **Version history table** (from `model.versions`): version, live/candidate badge, created_by, created_at, short hash, and a "Set live" button (`ClickPromoteWorkflowVersion`) on non-live rows + a "View" control (`SelectWorkflowVersion`).
  - **Stats panel** (from `model.stats`): per-version table — version, runs, tickets, success rate (%), avg cost ($), avg turns.
  - Each `Maybe` renders a loading/error/empty state (copy the `staleDataWarning`/`mutedLine` pattern from `Agent.Agent`).

- [ ] Author `handleDelivery` (implementer-written body; 5s polling like `Agent.Agent`: on `ClockTicked FiveSeconds _` re-issue `FetchAgentWorkflowStats model.name`), `subscriptions` (`[ OnClockTick FiveSeconds ]`), and `tooltip _ _ = Nothing`.

> Keep the diff strictly structural (field deltas), NOT a line-by-line text diff — see Open Decisions. Write the structural-diff helper as real Elm producing a `List String` of change lines; do not leave it as a placeholder.

- [ ] Compile-check the page in isolation:

```bash
cd web/elm && npx elm make src/AgentWorkflow/AgentWorkflow.elm --output=/dev/null 2>&1 | tail -40
```

Expected: clean once Effects/Callback/Message (Task 3.4) are in place.

- [ ] Commit:

```bash
git add web/elm/src/AgentWorkflow/AgentWorkflow.elm
git commit -m "feat(web): AgentWorkflow detail page (DAG, prompts, gates, budget, history, stats, lifecycle)"
```

### Task 3.6 — Wire the page into SubPage

**Files:** Modify `web/elm/src/SubPage/SubPage.elm`

Steps:

- [ ] Add the import: `import AgentWorkflow.AgentWorkflow as AgentWorkflow`.

- [ ] Add the model variant to the `Model` union: `| AgentWorkflowModel AgentWorkflow.Model`.

- [ ] Add the `init` case (near `Routes.AgentTicket { id } ->`):

```elm
        Routes.AgentWorkflow { name } ->
            AgentWorkflow.init { name = name }
                |> Tuple.mapFirst AgentWorkflowModel
```

- [ ] Thread a new function argument through `genericUpdate` (add `fAW2` alongside `fAT`), and add the dispatch case:

```elm
        AgentWorkflowModel m ->
            fAW2 ( m, effects )
                |> Tuple.mapFirst AgentWorkflowModel
```

> `genericUpdate` currently takes `fBuild … fAT`. Adding a page means adding ONE parameter and updating ALL callers (`handleCallback`, `handleDelivery`, `update` wrappers at lines ~271-346). Each caller passes the corresponding `AgentWorkflow.*` function: `AgentWorkflow.handleCallback callback`, `AgentWorkflow.handleDelivery delivery`, `Login.update msg >> AgentWorkflow.update msg`. Match the existing `AgentTicket` argument position exactly.

- [ ] Add the `view` case (near line ~518):

```elm
        AgentWorkflowModel model ->
            ( AgentWorkflow.documentTitle model
            , AgentWorkflow.view session model
            )
```

- [ ] Add `tooltip` (~line 568) and `subscriptions` (~line 611) cases mirroring `AgentTicket`.

- [ ] Compile the whole Elm app:

```bash
cd web/elm && npx elm make src/Main.elm --output=/dev/null 2>&1 | tail -40
```

Expected: clean (no errors).

- [ ] Commit:

```bash
git add web/elm/src/SubPage/SubPage.elm
git commit -m "feat(web): wire AgentWorkflow page into SubPage"
```

### Task 3.7 — Link workflow rows to the detail page

**Files:** Modify `web/elm/src/Agent/Agent.elm`

Steps:

- [ ] In `workflowRow`, wrap the workflow name in a link to the new route. Replace the name `Html.span` with an anchor built from `Routes.toString (Routes.AgentWorkflow { name = w.name })` (mirror how other links in the file build `href`). For example:

```elm
                (Html.a
                    [ style "font-weight" "700"
                    , style "color" Colors.text
                    , href (Routes.toString (Routes.AgentWorkflow { name = w.name }))
                    ]
                    [ Html.text w.name ]
                    :: workflowPills w
                )
```

> `Routes` is already imported in `Agent.Agent`; confirm the `toString`/route-render helper name (grep `Routes.` usages already in the file) and use the same one.

- [ ] Optionally hide deprecated workflows from the default list here (add a "show deprecated" toggle). If the `WorkflowSummary` decoder does not yet carry `hidden`, add `hidden`/`annotation` to `Concourse.Agent.WorkflowSummary` + its decoder first (the server now emits them). Recommended: surface a small "deprecated" pill when `w.hidden` rather than hiding, to keep the change bounded.

- [ ] Compile:

```bash
cd web/elm && npx elm make src/Main.elm --output=/dev/null 2>&1 | tail -20
```

Expected: clean.

- [ ] Commit:

```bash
git add web/elm/src/Agent/Agent.elm web/elm/src/Concourse/Agent.elm
git commit -m "feat(web): link workflow rows to detail page + deprecated pill"
```

### Task 3.8 — Rebuild and commit `elm.min.js` (MANDATORY)

**Files:** Modify (regenerate) `web/public/elm.min.js`

Steps:

- [ ] Rebuild the production Elm bundle using the repo's build path. Confirm the exact command from `web/`'s tooling (grep `web/package.json` / `web/Makefile` for the elm build target — the bundle is `web/public/elm.min.js`):

```bash
cd /Users/tdmtrader/concourse/concourse/web && make build 2>&1 | tail -20
# or, if the repo uses a direct elm+uglify pipeline:
#   cd web/elm && npx elm make src/Main.elm --optimize --output=../public/elm.min.js
```

- [ ] Verify the bundle changed and compiles by loading the app locally (or at minimum confirm the file's mtime/size updated and `git diff --stat web/public/elm.min.js` shows a change).

- [ ] Manual verification (record the result): run the web app against a dev ATC, navigate to `/agent`, click a workflow name, confirm the detail page renders the DAG, versions, stats, and that "Set live" / annotate / deprecate issue the expected requests (watch the network tab for `PUT /api/v1/agent/workflows/<name>/versions/<v>/live` and `PUT /api/v1/agent/workflows/<name>`).

- [ ] Commit:

```bash
git add web/public/elm.min.js
git commit -m "build(web): rebuild elm.min.js for AgentWorkflow detail page"
```

---

## Self-Review

**Spec coverage:**
- Step DAG preview → Task 3.5 (definition panel DAG from `config.steps`). ✅
- Prompt text → Task 3.5 (prompts `<pre>` blocks). ✅
- Gate policy → Task 3.5 (gates list + on_gate_failure). ✅
- Budget defaults → Task 3.5 (ticket/judge USD, default model/max turns). ✅
- Version history WITH diffs → Task 3.5 (history table + structural diff vs predecessor). Diff is structural, not line-level — flagged in Open Decisions. ✅ (scoped)
- Live-version promotion → Task 3.5 "Set live" → existing `PromoteAgentWorkflowVersion` endpoint. ✅
- Per-workflow stats (success rate, avg cost, avg turns, tickets-run count) → Phase 0 server aggregation + Task 3.5 stats panel. ✅
- New server aggregation over `agent_run_metrics` grouped by workflow+version → Task 0.2 (`WorkflowStats`). Feasibility confirmed: `agent_run_metrics` has `workflow_name`, `workflow_version`, `status`, `cost_usd`, `turns`, `ticket_id`, `build_id`; `builds` join gives success. ✅
- Lifecycle verbs describe/annotate + deprecate/hide → Phase 1 (migration + store + PUT route + fly). ✅
- Checked for existing description/hidden column BEFORE proposing a migration → `agent_workflow_definitions` has a per-VERSION `description` (YAML-derived) but NO name-level annotation and NO hidden column; a new name-keyed table is required. Migration slot FLAGGED, not numbered. ✅
- fly parity → Task 2.1 (stats/annotate/deprecate/restore). ✅
- elm.min.js rebuild → Task 3.8. ✅

**Placeholder scan:** No `TODO`/`TBD`/"similar to Task N"/"add error handling" left as code. Every Go code block is concrete and paste-ready. For the Elm detail page (Task 3.5), the literal code is `Model`/`init`/`documentTitle` only; the four core functions — `handleCallback`, `update`, `view` (DAG/prompts/gates/budget/history/stats/lifecycle rendering + the structural-diff helper), and `handleDelivery` — are DELIBERATELY specified as behavioral contracts the implementer authors by transcribing the `Agent.Agent` bodies, NOT as drop-in code blocks. This is an explicit, acknowledged exception to "every block is concrete," flagged inline in Task 3.5 and gated by the `elm make` compile-check; it is the single largest hand-written slice of the plan and should be budgeted as such.

**Type consistency:**
- `WorkflowVersionStats.Version *int` (Go) ↔ `version : Maybe Int` (Elm) ↔ nullable JSON. ✅
- New `Definition.Hidden bool` / `Annotation string` added to the Go struct, the DB scan, the MemoryStore decorate, and the Elm decoder. ✅
- `StatsProvider` (workflows pkg) is a strict subset of `metrics.Store`; `atc/db.AgentRunMetricsFactory` satisfies both. ✅
- `NewHandler` signature change (adds `StatsProvider`) — all call sites updated (handler_test `newHandler`, the new stats test, and `atc/api/handler.go`). ✅
- Auth: stats = viewer tier (read); update = human-only viewer (no principal), with a "REJECTS bare tickets:read principal" pin. ✅

## Open Decisions

1. **Lifecycle storage shape — new table vs. columns.** *Recommendation:* new name-keyed table `agent_workflow_lifecycle` (Task 1.1). The existing `agent_workflow_definitions.description` is per-version and YAML-derived (rewriting it would corrupt the content hash / re-mint versions); annotation and hidden are name-level operator state. A separate table keeps the definition rows immutable. Owner: platform maintainer to approve the migration slot at FLAG time.

2. **Diff fidelity — structural vs. line-level.** *Recommendation:* ship the structural field-delta diff (step-count, model, budget, gate-count, description) for v1 (Task 3.5); it is bounded, needs only the two `Config` records already fetched, and answers "what changed" for the common case. Defer a full unified `raw_yaml` line diff to a follow-up (it needs a diff algorithm in Elm or a server-rendered diff endpoint). Owner: UX.

3. **Deprecated-workflow visibility on `/agent` and dispatch.** *Recommendation:* surface a "deprecated" pill (Task 3.7) rather than hiding hidden workflows from the list, and do NOT change dispatch admission in this track — hiding is a listing hint only. Whether `hidden` should also block new dispatches is a dispatcher-track decision (out of scope here). Owner: dispatcher-track + platform.

4. **Stats "run" unit.** *Recommendation:* define a run as a distinct `build_id` (Task 0.2) — a dispatched ticket run is one build with many step rows, so success/cost/turns aggregate per build. Alternative (per-step) would inflate counts and make "success rate" meaningless for multi-step workflows. Owner: platform (confirm the semantics match how scorecards/delivery-outcomes count runs).

5. **`fly agent workflows stats` for NULL-version (ad-hoc) rows.** The query returns a `version: null` bucket for ad-hoc CI runs sharing a `workflow_name`. *Recommendation:* render it as "ad-hoc" (Task 2.1) and keep it — it is informative — but exclude it from the web stats panel's "live version" highlighting. Owner: UX.
