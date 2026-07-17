# Ticket Core Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Land the `agent_tickets` / `agent_ticket_specs` / `agent_ticket_tasks` data model with a single-writer state-transition function, principal-aware `/api/v1/agent/tickets` CRUD routes, `fly agent tickets` commands, a minimal Elm ticket page, the spec.md/plan.md render helper, and the Jira phase-2 design note.

**Architecture:** A new `agent/api/tickets` package owns the domain types (contracts §2.1), the state machine, the HTTP handler, a MemoryStore, and the markdown render helper; `atc/db.NewAgentTicketsFactory` implements `tickets.Store` over migrations `1773106050–52` following the `agent_reviews_factory.go` recipe, with `Transition` as the ONLY code path that mutates `agent_tickets.state` (optimistic concurrency guarded by the expected `from` state). Routes ride the wave-1 agent-identity surfaces (`CheckAgentAuthorizationHandler`, `CheckAgentPrincipalHandlerFactory`, `principals.FromContext`) plus one new composition helper for the "authorized member; also principal" tiers; go-concourse client methods back three `fly agent tickets` subcommands, and an `AgentTickets/AgentTicket.elm` page (patterned on `AgentReviews/AgentReviews.elm`) gives view/edit + lifecycle badge + a 5s-polling task list.

**Tech Stack:** Go, PostgreSQL migrations (`atc/db/migration/migrations`, embedded via `go:embed` at `atc/db/migration/migration.go:153`), squirrel + raw SQL (factory recipe), counterfeiter fakes, Ginkgo/Gomega (atc/db, atc/wrappa, atc/api/auth, go-concourse, fly/integration), plain `testing` (agent/* packages, matching `agent/api/reviews`), jessevdk/go-flags (fly), Elm 0.19 + elm-test (web/elm).

---

## Context

**Charter (workstreams.json id `ticket-core`, size M, wave 2, depends_on: agent-identity):** migrations + factories for the three ticket tables (full §9 lifecycle enum from day one; origin enum `web/fly/jira/retrospective`; repo; workflow name+version ref; budget; triggering user); the single state-transition function every later component (dispatch, platform-mcp, harvest, outcome watcher) must go through; `/api/v1/agent/tickets` CRUD on agent principals + accessor role entries; `fly agent tickets list/create/show`; minimal Elm ticket page (view + edit of title/body/budget, lifecycle badge, live task-status list, read side only); the spec.md/plan.md render helper; and the open-item-10 Jira-sync phase-2 design note. **Scope OUT:** dispatch of queued tickets (dispatch), the `submit_spec`/`submit_plan` MCP tool surface (platform-mcp-hitl — we land the HTTP routes it will call), and the diff/review/judge PR view and dispositions (delivery-outcomes).

**Scope-in → task mapping:** migrations + factories → Tasks 2, 4, 5, 6; single-writer transition function → Tasks 3 (state machine), 5 (DB writer); CRUD API on principals + role entries + fly → Tasks 7, 8, 10, 11; minimal Elm ticket page → Tasks 12, 13; spec.md/plan.md render helper → Task 9; open item 10 design note → Task 14. Wave-start agreements other workstreams consume → Task 1.

**Prior waves (assumed LANDED exactly as 00-shared-contracts.md and the wave-1 plan addenda define):**
- **agent-identity** (wave 1): `agent_principals` (migration 1773106010); package `agent/api/principals` — types `Principal`, `Store`, `Verifier`, `MemoryStore`; funcs `NewContext(ctx, Principal) context.Context`, `FromContext(ctx) (Principal, bool)`; scope constants `ScopeTicketsRead`, `ScopeTicketsWrite`; `TokenVersionPrefix = "cap1."`. In `atc/api/auth`: `CheckAgentPrincipalHandlerFactory` (method `HandlerFor(delegate http.Handler, rejector Rejector, scope string) http.Handler`), already a field/constructor param of `wrappa.NewAPIAuthWrappa` (5th argument, `checkAgentPrincipalHandlerFactory`), and `CheckAgentAuthorizationHandler(handler http.Handler, rejector Rejector) http.Handler` (main-team authorization for team-less `/api/v1/agent/*` routes, contracts decision 21 — the five existing agent feedback routes already sit in its wrappa case group). The per-route scope audit lives at `docs/superpowers/plans/agentic-platform/agent-route-scopes.md` and already contains rows for all eight ticket routes with status "planned (wave 2)".
- **credentials-and-budgets** (wave 1): `fly/commands/agent.go` defines the shared `AgentCommand` struct (fields `Auth`, `Costs`) registered as `Agent AgentCommand \`command:"agent" ...\`` in `fly/commands/fly.go`; the wave-1 contract addendum says later workstreams append their own subcommand fields (`Tickets`) — additive merges only. `agent_cost_ledger.ticket_id` is a plain nullable column that starts joining to our `agent_tickets.id` the moment Task 2 lands.
- **workflow-store** (wave 1): `agent_workflow_definitions` + `agent/workflow` package. `agent_tickets.workflow_name`/`workflow_version` are plain join-key columns (no FK, per contracts conventions); this plan stores them opaquely and does NOT validate against the workflow store (dispatch resolves and freezes `workflow_definition_id` in wave 4).
- **pipeline-runs**, **dev-mcp** (wave 1): no surface consumed directly; `agent_tickets.pipeline_run_id` is a plain join-key column.

**Wave-mate (parallel, NOT landed):** agent-step. Shared files: `atc/db/migration/legacy_upgrade_test.go`'s `jetbridgeHeadMigration` const (both bump it; higher number wins — Task 2) and additive merges in `atc/routes.go` / wrappa / roles.go.

**This plan PRODUCES (contract surface `ticket-tables-and-transition-function`):**
- 00-shared-contracts.md **§1.7 "Ticket tables"** — the three tables, DDL as written, plus two additive columns declared in Task 1's addendum (`created_by`, `external_ref`).
- 00-shared-contracts.md **§2.1 "Ticket"** — `agent/api/tickets` types + `Store` interface (with one additive method, `AppendTaskNote`, declared in Task 1's addendum).
- 00-shared-contracts.md **§4.2 route table** — the eight `*AgentTicket*` rows (`ListAgentTickets`, `CreateAgentTicket`, `GetAgentTicket`, `UpdateAgentTicket`, `TransitionAgentTicket`, `SubmitAgentTicketSpec`, `SubmitAgentTicketPlan`, `UpdateAgentTicketTask`).
- The spec.md/plan.md render helper (`tickets.RenderSpecMarkdown` / `tickets.RenderPlanMarkdown`) consumed by dispatch's renderer in wave 4.
- The combined-tier auth helper `auth.AgentPrincipalOrMainTeamHandler`, reused by platform-mcp-hitl in wave 3.

**This plan CONSUMES:**
- 00-shared-contracts.md **§4.1 "Auth tiers and principal scopes"** — the `principal(<scope>)` tier, scopes `tickets:read`/`tickets:write`, decision 21's `CheckAgentAuthorizationHandler`, and agent-identity's frozen Go surface names (its planning addendum in §11).
- 00-shared-contracts.md **§1.2 `agent_principals`** (indirectly via the auth tier) and the audit-attribution convention (`created_by`/`submitted_by` columns holding the principal name or human username, resolved via `principals.FromContext`).
- 00-shared-contracts.md **§3.2 platform-mcp** — read-only alignment: our `GET /api/v1/agent/tickets/:ticket_id` response is the payload `read_ticket` wraps in wave 3, and our spec/plan/task route bodies are exactly the `submit_spec`/`submit_plan`/`update_task_status` tool inputs.

**Anchor caveat:** `Modify:` line anchors were verified on branch `jetbridge` at the current head (`fb1c54fac2`, pre-wave-1). Wave-1 landings (agent-identity edits to `atc/wrappa/api_auth_wrappa.go`, `atc/api/handler.go`; workflow-store edits to `atc/routes.go`, `accessor/roles.go`; credentials-and-budgets edits to `atc/api/handler.go`, `fly/commands/fly.go`) shift them — treat anchors as "the location of the quoted code", not absolute line numbers.

---

### Task 1: Wave-start contract addendum

Freeze, in writing, the small extensions this plan makes beyond the literal text of 00-shared-contracts.md, so wave-2/3 consumers (agent-step, platform-mcp-hitl, dispatch, harvest-step) build against them instead of assuming.

**Files:**
- Modify: `docs/superpowers/plans/agentic-platform/00-shared-contracts.md` (append to `## 11. Amendment log` at the end of the file, after the agent-identity planning addendum)

**Steps:**

- [ ] Append this entry to the `## 11. Amendment log` section at the end of `docs/superpowers/plans/agentic-platform/00-shared-contracts.md`:

```markdown
- 2026-07-08 (ticket-core planning addendum, cross-workstream sign-off note):
  - §1.7 (affects: platform-mcp-hitl, dispatch, harvest-step, delivery-outcomes, process-intel-experiments): `agent_tickets` gains two additive columns in migration `1773106050`: `created_by TEXT NOT NULL DEFAULT ''` (the agent-identity audit-attribution convention — principal name or human username that created the row) and `external_ref TEXT NOT NULL DEFAULT ''` (the Jira phase-2 seam from spec open item 10: holds the external issue key, e.g. `PROJ-123`; empty for native tickets; see docs/superpowers/plans/agentic-platform/ticket-jira-sync-phase2.md). Field growth only; all other §1.7 columns, checks, and indexes are byte-identical.
  - §2.1 (affects: platform-mcp-hitl, dispatch, harvest-step, delivery-outcomes): Go surface names frozen by ticket-core, all in `agent/api/tickets`: `Ticket` gains `CreatedBy`/`ExternalRef` fields; supporting types `Spec`, `Link`, `Task`, `TaskStatus` (constants `TaskPending`, `TaskInProgress`, `TaskDone`, `TaskSkipped`, `TaskBlocked`), `ListFilter{State, Repo, Origin, Limit}`, `Update{Title, Body, BudgetUSD, WorkflowName, WorkflowVersion, TargetBranch}` (all pointers, nil = unchanged), `TransitionMeta{PipelineRunID *int, Branch string, ErrorDetail string}`, `TicketDetail{Ticket, Spec *Spec, Tasks []Task}`; HTTP request types `CreateRequest`, `UpdateRequest`, `TransitionRequest`, `SpecSubmission`, `PlanSubmission` (+`PlanTask`), `TaskStatusRequest`; errors `ErrInvalidTransition`, `ErrTicketNotFound`, `ErrStaleTransition`, `ErrNoActivePlan`, `ErrTaskNotFound`; funcs `ValidTransition(from, to State) bool`, `ValidState`, `ValidOrigin`, `ValidTaskStatus`; `MemoryStore`. The `Store` interface gains one additive method: `AppendTaskNote(ticketID, planVersion, ordering int, note string) error` — the persistence carrier for §3.2 `update_task_status`'s optional `note` field (appended to the task's `detail` as a markdown blockquote, `"> <note>"`, joined with blank lines). `atc/db.AgentTicketsFactory` / `NewAgentTicketsFactory` (dbfakes: `FakeAgentTicketsFactory`).
  - §2.1 transition side effects (affects: dispatch, harvest-step, platform-mcp-hitl, delivery-outcomes): `Transition` records: → `queued`: `queued_at=now()`, `completed_at=NULL`, `attempt_count+1` when from=`running` — the edge reads `running → queued (retryable platform error OR rejected send_back checkpoint re-dispatch; attempt_count++)`; its two legitimate callers are dispatch's retry path and dispatch's run-completion reconciler (checkpoint-seam delta §6, 2026-07-09); → `draft` (unqueue): `queued_at=NULL`; → `running`: `dispatched_at=now()`, `pipeline_run_id` from meta; → `needs_review`: `branch` from meta when non-empty — TWO writers: harvest (primary, per 09-harvest-step) and dispatch's run-completion reconciler (backup/safety net, empty meta); → `merged`/`merged_with_fixes`/`sent_back`/`abandoned`/`concluded`/`failed`/`errored`: `completed_at=now()`, plus `error_detail` from meta on `errored`. The `needs_review → concluded` edge (flow-decoupling delta, 2026-07-09, per FLOWS.md §3 spike-research / §4 state-enum decision) is TERMINAL — "run finished, human reviewed, no merge intended" — the positive sibling of `abandoned`, reachable ONLY via explicit human disposition from `needs_review`, no outgoing edges; it lands in the frozen enum NOW (pre-freeze) so no later migration is needed. The UPDATE is guarded by `WHERE id=$id AND state=$from`; zero rows updated resolves to `ErrTicketNotFound` (row gone) or `ErrStaleTransition` (state changed concurrently). `Store.Create` always inserts `state='draft'`; queueing is a separate Transition call (single-writer discipline).
  - §2.1/§3.2 (affects: platform-mcp-hitl): the `GetAgentTicket` response body is exactly `tickets.TicketDetail` JSON — `{"ticket": <§2.1 Ticket>, "spec": <latest Spec or null>, "tasks": [<active-plan Task>...]}` — the payload `read_ticket` returns verbatim in wave 3. `TransitionAgentTicket` returns 409 on `ErrInvalidTransition`/`ErrStaleTransition`, 404 on missing ticket. `CreateAgentTicket` origin rules: principal writes may only create `origin:"retrospective"`; human writes may create `web`/`fly`; `jira` is rejected (400) until the phase-2 sync component exists.
  - §4.1 (affects: platform-mcp-hitl, delivery-outcomes): combined route tiers ("authorized member (main); also principal(<scope>)") are implemented by a new composition helper owned by ticket-core: `atc/api/auth.AgentPrincipalOrMainTeamHandler(principalTier, mainTeamTier http.Handler) http.Handler` — dispatches on the `cap1.` bearer-token prefix: cap1 tokens go to the principal tier (`CheckAgentPrincipalHandlerFactory.HandlerFor`), everything else to `CheckAgentAuthorizationHandler`. platform-mcp-hitl reuses it for `GetAgentQuestion`/`AnswerAgentQuestion` in wave 3.
  - Render helper (affects: dispatch): `tickets.RenderSpecMarkdown(t Ticket, spec *Spec) []byte` and `tickets.RenderPlanMarkdown(t Ticket, tasks []Task) []byte` produce the deterministic read-only `spec.md`/`plan.md` workspace inputs dispatch materializes at render time. Plan task glyphs: `[ ]` pending, `[~]` in_progress, `[x]` done, `[-]` skipped, `[!]` blocked.
  - Coordination note (affects: agent-step, wave-mate): both wave-2 plans bump `atc/db/migration/legacy_upgrade_test.go`'s `jetbridgeHeadMigration`. Merge rule: the const is always the highest landed migration number; whoever merges second keeps the larger value (ticket-core: 1773106052, agent-step: 1773106060).
```

- [ ] Commit: `git add docs/superpowers/plans/agentic-platform/00-shared-contracts.md && git commit -m "docs(ticket-core): wave-start contract addendum for ticket tables and auth composition"`

---

### Task 2: Migrations 1773106050–52

The three ticket tables, DDL per contracts §1.7 plus the two Task-1 addendum columns. Migration files are picked up automatically via `go:embed migrations` (`atc/db/migration/migration.go:153`) — no registration code.

**Convention:** migration head bump — follow [CONVENTIONS.md §C2](CONVENTIONS.md) (also bump `docs/migration/migrate-preflight.sh` `JETBRIDGE_VERSION`, same commit).

**Files:**
- Create: `atc/db/migration/migrations/1773106050_create_agent_tickets.up.sql`
- Create: `atc/db/migration/migrations/1773106050_create_agent_tickets.down.sql`
- Create: `atc/db/migration/migrations/1773106051_create_agent_ticket_specs.up.sql`
- Create: `atc/db/migration/migrations/1773106051_create_agent_ticket_specs.down.sql`
- Create: `atc/db/migration/migrations/1773106052_create_agent_ticket_tasks.up.sql`
- Create: `atc/db/migration/migrations/1773106052_create_agent_ticket_tasks.down.sql`
- Modify: `atc/db/migration/legacy_upgrade_test.go:37` (`jetbridgeHeadMigration` const)

**Steps:**

- [ ] Check PostgreSQL is up: `pg_isready` (required for every atc/db test in this plan).

- [ ] Update the head-migration const in `atc/db/migration/legacy_upgrade_test.go:37` (pre-wave-1 value `1773105504`; after wave 1 it reads `1773106040`). Set it to `1773106052` — **unless** the wave-mate agent-step has already landed and set it to `1773106060` or higher, in which case leave it untouched and skip the next (failing-run) step:

```go
// JetBridge HEAD (last migration)
const jetbridgeHeadMigration = 1773106052
```

- [ ] Run to verify it fails (const points at migrations that don't exist yet): `ginkgo --focus="Legacy Database Upgrade" ./atc/db/migration/` — expected failure: `ExpectDatabaseMigrationVersionToEqual` mismatch / migration to 1773106052 not found.

- [ ] Write `atc/db/migration/migrations/1773106050_create_agent_tickets.up.sql` (contracts §1.7 DDL + the two addendum columns after `user_name`):

```sql
CREATE TABLE agent_tickets (
    id                     SERIAL PRIMARY KEY,    -- ticket number; branch is agent/ticket-<id>
    title                  TEXT NOT NULL,
    body                   TEXT NOT NULL DEFAULT '',  -- markdown problem statement
    state                  TEXT NOT NULL DEFAULT 'draft'
                           CHECK (state IN ('draft','queued','running','needs_review',
                                            'merged','merged_with_fixes','sent_back',
                                            'abandoned','concluded','failed','errored')),
    origin                 TEXT NOT NULL DEFAULT 'web'
                           CHECK (origin IN ('web','fly','jira','retrospective')),
    repo                   TEXT NOT NULL,             -- canonical slug, joins agent_reviews.repo
    target_branch          TEXT NOT NULL DEFAULT 'main',
    workflow_name          TEXT NOT NULL DEFAULT '',
    workflow_version       INTEGER,                   -- NULL = live version at dispatch time
    workflow_definition_id INTEGER,                   -- join key; resolved+frozen by dispatch
    budget_usd             NUMERIC(12,6),             -- NULL = workflow definition default
    user_id                INTEGER,                   -- join key users.id (triggering user)
    user_name              TEXT NOT NULL DEFAULT '',
    created_by             TEXT NOT NULL DEFAULT '',  -- audit attribution: principal name or username
    external_ref           TEXT NOT NULL DEFAULT '',  -- Jira phase-2 seam (issue key), '' = native
    pipeline_run_id        INTEGER,                   -- join key pipeline_runs.id (latest attempt)
    branch                 TEXT NOT NULL DEFAULT '',  -- set by harvest after push
    attempt_count          INTEGER NOT NULL DEFAULT 0,
    error_detail           TEXT NOT NULL DEFAULT '',  -- populated on state 'errored'
    created_at             TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at             TIMESTAMPTZ NOT NULL DEFAULT now(),
    queued_at              TIMESTAMPTZ,
    dispatched_at          TIMESTAMPTZ,
    completed_at           TIMESTAMPTZ
);

CREATE INDEX agent_tickets_state ON agent_tickets (state);
CREATE INDEX agent_tickets_repo  ON agent_tickets (repo, created_at DESC);
```

- [ ] Write `atc/db/migration/migrations/1773106050_create_agent_tickets.down.sql`:

```sql
DROP TABLE agent_tickets;
```

- [ ] Write `atc/db/migration/migrations/1773106051_create_agent_ticket_specs.up.sql` (contracts §1.7 verbatim):

```sql
CREATE TABLE agent_ticket_specs (
    id                  SERIAL PRIMARY KEY,
    ticket_id           INTEGER NOT NULL REFERENCES agent_tickets (id) ON DELETE CASCADE,
    version             INTEGER NOT NULL DEFAULT 1,   -- resubmission bumps version, old rows kept
    title               TEXT NOT NULL,
    body                TEXT NOT NULL DEFAULT '',     -- markdown prose (rationale is load-bearing)
    acceptance_criteria JSONB NOT NULL DEFAULT '[]',  -- ["criterion", ...]
    links               JSONB NOT NULL DEFAULT '[]',  -- [{"title": "", "url": ""}, ...]
    submitted_by        TEXT NOT NULL DEFAULT '',     -- principal name (agent) or username (human edit)
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (ticket_id, version)
);
```

- [ ] Write `atc/db/migration/migrations/1773106051_create_agent_ticket_specs.down.sql`:

```sql
DROP TABLE agent_ticket_specs;
```

- [ ] Write `atc/db/migration/migrations/1773106052_create_agent_ticket_tasks.up.sql` (contracts §1.7 verbatim):

```sql
CREATE TABLE agent_ticket_tasks (
    id           SERIAL PRIMARY KEY,
    ticket_id    INTEGER NOT NULL REFERENCES agent_tickets (id) ON DELETE CASCADE,
    plan_version INTEGER NOT NULL DEFAULT 1,      -- submit_plan replaces the active plan by bumping version
    ordering     INTEGER NOT NULL,
    title        TEXT NOT NULL,
    detail       TEXT NOT NULL DEFAULT '',        -- optional markdown
    status       TEXT NOT NULL DEFAULT 'pending'
                 CHECK (status IN ('pending','in_progress','done','skipped','blocked')),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (ticket_id, plan_version, ordering)
);

CREATE INDEX agent_ticket_tasks_ticket ON agent_ticket_tasks (ticket_id, plan_version, ordering);
```

- [ ] Write `atc/db/migration/migrations/1773106052_create_agent_ticket_tasks.down.sql`:

```sql
DROP TABLE agent_ticket_tasks;
```

- [ ] Run to verify pass: `ginkgo --focus="Legacy Database Upgrade" ./atc/db/migration/` — expect green (the v7.13→HEAD and v8.0.1→HEAD specs now migrate through 1773106052; the suite also exercises the down files).

- [ ] Commit: `git add atc/db/migration && git commit -m "feat(ticket-core): agent_tickets/agent_ticket_specs/agent_ticket_tasks migrations (1773106050-52)"`

---

### Task 3: `agent/api/tickets` domain package — types, state machine, MemoryStore

Contracts §2.1 types verbatim (plus the Task-1 additive fields), the state-machine table from §1.7, and an in-memory `Store` for handler tests and the atc/api suite (the `reviews.MemoryStore` precedent at `agent/api/reviews/memory_store.go`).

**Files:**
- Create: `agent/api/tickets/types.go`
- Create: `agent/api/tickets/memory_store.go`
- Test: `agent/api/tickets/types_test.go`
- Test: `agent/api/tickets/memory_store_test.go`

**Steps:**

- [ ] Write the failing state-machine test `agent/api/tickets/types_test.go`:

```go
package tickets_test

import (
	"testing"

	"github.com/concourse/concourse/agent/api/tickets"
)

func TestValidTransitionMatrix(t *testing.T) {
	allowed := []struct{ from, to tickets.State }{
		{tickets.StateDraft, tickets.StateQueued},
		{tickets.StateDraft, tickets.StateAbandoned},
		{tickets.StateQueued, tickets.StateRunning},
		{tickets.StateQueued, tickets.StateDraft},
		{tickets.StateQueued, tickets.StateAbandoned},
		// running→queued: retryable platform error OR rejected send_back
		// checkpoint re-dispatch (attempt_count++). TWO legitimate callers —
		// dispatch's retry path AND dispatch's run-completion reconciler
		// (checkpoint-seam delta §6, 2026-07-09). Do not narrow this edge.
		{tickets.StateRunning, tickets.StateQueued},
		// running→needs_review: TWO writers — harvest (primary, 09) and
		// dispatch's run-completion reconciler (backup/safety net). Do not
		// narrow this edge either.
		{tickets.StateRunning, tickets.StateNeedsReview},
		{tickets.StateRunning, tickets.StateFailed},
		{tickets.StateRunning, tickets.StateErrored},
		{tickets.StateNeedsReview, tickets.StateMerged},
		{tickets.StateNeedsReview, tickets.StateMergedWithFixes},
		{tickets.StateNeedsReview, tickets.StateSentBack},
		{tickets.StateNeedsReview, tickets.StateAbandoned},
		// needs_review→concluded: TERMINAL positive sibling of abandoned —
		// "run finished, human reviewed, no merge intended" (spike/research
		// flows; FLOWS.md §3 spike-research, §4 state-enum decision).
		// Explicit human disposition ONLY; added pre-freeze (2026-07-09) so
		// the frozen enum never needs a later migration.
		{tickets.StateNeedsReview, tickets.StateConcluded},
		{tickets.StateNeedsReview, tickets.StateQueued},
		{tickets.StateSentBack, tickets.StateQueued},
		{tickets.StateFailed, tickets.StateQueued},
		{tickets.StateErrored, tickets.StateQueued},
	}
	for _, tr := range allowed {
		if !tickets.ValidTransition(tr.from, tr.to) {
			t.Errorf("ValidTransition(%s, %s) = false, want true", tr.from, tr.to)
		}
	}

	forbidden := []struct{ from, to tickets.State }{
		{tickets.StateDraft, tickets.StateRunning},       // must queue first
		{tickets.StateDraft, tickets.StateDraft},         // self-transition
		{tickets.StateQueued, tickets.StateNeedsReview},  // must run first
		{tickets.StateRunning, tickets.StateDraft},
		{tickets.StateRunning, tickets.StateMerged},
		{tickets.StateMerged, tickets.StateQueued},       // merged is terminal
		{tickets.StateMergedWithFixes, tickets.StateQueued},
		{tickets.StateAbandoned, tickets.StateQueued},    // abandoned is terminal
		{tickets.StateNeedsReview, tickets.StateRunning}, // re-dispatch goes via queued
		{tickets.StateDraft, tickets.StateConcluded},     // concluding requires a reviewed run
		{tickets.StateRunning, tickets.StateConcluded},   // must land in needs_review first
		{tickets.StateConcluded, tickets.StateQueued},    // concluded is terminal — no exits
	}
	for _, tr := range forbidden {
		if tickets.ValidTransition(tr.from, tr.to) {
			t.Errorf("ValidTransition(%s, %s) = true, want false", tr.from, tr.to)
		}
	}
}

func TestValidStateOriginTaskStatus(t *testing.T) {
	for _, s := range []tickets.State{
		tickets.StateDraft, tickets.StateQueued, tickets.StateRunning,
		tickets.StateNeedsReview, tickets.StateMerged, tickets.StateMergedWithFixes,
		tickets.StateSentBack, tickets.StateAbandoned, tickets.StateConcluded,
		tickets.StateFailed, tickets.StateErrored,
	} {
		if !tickets.ValidState(s) {
			t.Errorf("ValidState(%q) = false, want true", s)
		}
	}
	if tickets.ValidState("open") || tickets.ValidState("") {
		t.Error("ValidState accepted an unknown state")
	}

	for _, o := range []string{"web", "fly", "jira", "retrospective"} {
		if !tickets.ValidOrigin(o) {
			t.Errorf("ValidOrigin(%q) = false, want true", o)
		}
	}
	if tickets.ValidOrigin("email") || tickets.ValidOrigin("") {
		t.Error("ValidOrigin accepted an unknown origin")
	}

	for _, s := range []tickets.TaskStatus{
		tickets.TaskPending, tickets.TaskInProgress, tickets.TaskDone,
		tickets.TaskSkipped, tickets.TaskBlocked,
	} {
		if !tickets.ValidTaskStatus(s) {
			t.Errorf("ValidTaskStatus(%q) = false, want true", s)
		}
	}
	if tickets.ValidTaskStatus("started") {
		t.Error("ValidTaskStatus accepted an unknown status")
	}
}
```

- [ ] Run to verify it fails: `go test ./agent/api/tickets/` — expected failure: package does not exist / `no Go files`.

- [ ] Write `agent/api/tickets/types.go` (contracts §2.1 verbatim + Task-1 addendum growth):

```go
// Package tickets is the ticket-core domain: agent_tickets /
// agent_ticket_specs / agent_ticket_tasks types, the lifecycle state
// machine, and the single-writer Store contract
// (00-shared-contracts.md §1.7 / §2.1 + ticket-core addendum).
package tickets

import "errors"

type State string

const (
	StateDraft           State = "draft"
	StateQueued          State = "queued"
	StateRunning         State = "running"
	StateNeedsReview     State = "needs_review"
	StateMerged          State = "merged"
	StateMergedWithFixes State = "merged_with_fixes"
	StateSentBack        State = "sent_back"
	StateAbandoned       State = "abandoned"
	// StateConcluded is TERMINAL: run finished, human reviewed, no merge
	// intended (spike/research flows) — the positive sibling of abandoned.
	// In the frozen enum from day one per FLOWS.md §3/§4 (pre-freeze add,
	// 2026-07-09), so it never needs a later migration.
	StateConcluded       State = "concluded"
	StateFailed          State = "failed"
	StateErrored         State = "errored"
)

// validTransitions is the §1.7 state machine. Transition (the
// single-writer function) consults it; nothing else writes state.
//
// Edge notes (do not narrow):
//   - running → queued (retryable platform error OR rejected send_back
//     checkpoint re-dispatch; attempt_count++) — callers: dispatch's
//     retry path and dispatch's run-completion reconciler.
//   - running → needs_review — two writers: harvest (primary) and
//     dispatch's run-completion reconciler (backup/safety net).
//   - needs_review → concluded — TERMINAL, explicit human disposition
//     ONLY: "run finished, human reviewed, no merge intended"
//     (spike/research flows; FLOWS.md §3). Positive sibling of abandoned.
var validTransitions = map[State][]State{
	StateDraft:       {StateQueued, StateAbandoned},
	StateQueued:      {StateRunning, StateDraft, StateAbandoned},
	StateRunning:     {StateQueued, StateNeedsReview, StateFailed, StateErrored},
	StateNeedsReview: {StateMerged, StateMergedWithFixes, StateSentBack, StateAbandoned, StateConcluded, StateQueued},
	StateSentBack:    {StateQueued},
	StateFailed:      {StateQueued},
	StateErrored:     {StateQueued},
}

func ValidTransition(from, to State) bool {
	for _, t := range validTransitions[from] {
		if t == to {
			return true
		}
	}
	return false
}

func ValidState(s State) bool {
	if _, ok := validTransitions[s]; ok {
		return true
	}
	// terminal states have no outgoing edges but are still valid
	switch s {
	case StateMerged, StateMergedWithFixes, StateAbandoned, StateConcluded:
		return true
	}
	return false
}

func ValidOrigin(o string) bool {
	switch o {
	case "web", "fly", "jira", "retrospective":
		return true
	}
	return false
}

type TaskStatus string

const (
	TaskPending    TaskStatus = "pending"
	TaskInProgress TaskStatus = "in_progress"
	TaskDone       TaskStatus = "done"
	TaskSkipped    TaskStatus = "skipped"
	TaskBlocked    TaskStatus = "blocked"
)

func ValidTaskStatus(s TaskStatus) bool {
	switch s {
	case TaskPending, TaskInProgress, TaskDone, TaskSkipped, TaskBlocked:
		return true
	}
	return false
}

var (
	ErrInvalidTransition = errors.New("invalid ticket state transition")
	ErrTicketNotFound    = errors.New("ticket not found")
	ErrStaleTransition   = errors.New("ticket state changed concurrently")
	ErrNoActivePlan      = errors.New("ticket has no submitted plan")
	ErrTaskNotFound      = errors.New("plan task not found")
)

// Ticket is the §2.1 API shape (epoch-seconds timestamps).
type Ticket struct {
	ID                   int      `json:"id"`
	Title                string   `json:"title"`
	Body                 string   `json:"body"`
	State                State    `json:"state"`
	Origin               string   `json:"origin"`
	Repo                 string   `json:"repo"`
	TargetBranch         string   `json:"target_branch"`
	WorkflowName         string   `json:"workflow_name"`
	WorkflowVersion      *int     `json:"workflow_version,omitempty"`
	WorkflowDefinitionID *int     `json:"workflow_definition_id,omitempty"`
	BudgetUSD            *float64 `json:"budget_usd,omitempty"`
	UserID               *int     `json:"user_id,omitempty"`
	UserName             string   `json:"user_name"`
	PipelineRunID        *int     `json:"pipeline_run_id,omitempty"`
	Branch               string   `json:"branch"`
	AttemptCount         int      `json:"attempt_count"`
	ErrorDetail          string   `json:"error_detail,omitempty"`
	CreatedBy            string   `json:"created_by,omitempty"`   // audit attribution (addendum)
	ExternalRef          string   `json:"external_ref,omitempty"` // Jira phase-2 seam (addendum)
	CreatedAt            int64    `json:"created_at"`
	UpdatedAt            int64    `json:"updated_at"`
	CompletedAt          int64    `json:"completed_at,omitempty"`
}

type Link struct {
	Title string `json:"title"`
	URL   string `json:"url"`
}

// Spec is one agent_ticket_specs row (structured envelope + markdown body).
type Spec struct {
	ID                 int      `json:"id"`
	TicketID           int      `json:"ticket_id"`
	Version            int      `json:"version"`
	Title              string   `json:"title"`
	Body               string   `json:"body"`
	AcceptanceCriteria []string `json:"acceptance_criteria"`
	Links              []Link   `json:"links"`
	SubmittedBy        string   `json:"submitted_by"`
	CreatedAt          int64    `json:"created_at"`
}

// Task is one agent_ticket_tasks row.
type Task struct {
	ID          int        `json:"id"`
	TicketID    int        `json:"ticket_id"`
	PlanVersion int        `json:"plan_version"`
	Ordering    int        `json:"ordering"`
	Title       string     `json:"title"`
	Detail      string     `json:"detail,omitempty"`
	Status      TaskStatus `json:"status"`
	UpdatedAt   int64      `json:"updated_at"`
}

// TicketDetail is the GetAgentTicket response and (wave 3) the
// platform-mcp read_ticket payload — contract addendum.
type TicketDetail struct {
	Ticket Ticket `json:"ticket"`
	Spec   *Spec  `json:"spec"`
	Tasks  []Task `json:"tasks"`
}

type ListFilter struct {
	State  State
	Repo   string
	Origin string
	Limit  int
}

// Update is the non-state mutation set (title/body/budget/workflow
// ref/target branch). nil = leave unchanged. State is NEVER here —
// Transition is the only state writer.
type Update struct {
	Title           *string
	Body            *string
	BudgetUSD       *float64
	WorkflowName    *string
	WorkflowVersion *int
	TargetBranch    *string
}

// TransitionMeta carries the side-band values a transition records.
type TransitionMeta struct {
	PipelineRunID *int   // recorded on → running (set by dispatch)
	Branch        string // recorded on → needs_review (harvest, the primary writer; the reconciler backup-writer leaves it empty)
	ErrorDetail   string // recorded on → errored
}

// Store is the single-writer contract. Transition is THE ONLY way any
// code path (API handler, dispatcher — including its run-completion
// reconciler — harvest, outcome watcher, HITL) changes Ticket.State. It enforces the state machine in
// shared-contracts §1.7, records timestamps, and returns
// ErrInvalidTransition otherwise. It uses optimistic concurrency: the
// UPDATE is guarded by the expected `from` state.
//
//counterfeiter:generate . Store
type Store interface {
	Create(t *Ticket) (int, error)
	Get(id int) (*Ticket, bool, error)
	List(filter ListFilter) ([]Ticket, error)
	Update(id int, upd Update) error // title/body/budget/workflow ref; never state
	Transition(id int, from, to State, meta TransitionMeta) error

	SubmitSpec(ticketID int, spec Spec) (version int, err error)
	SubmitPlan(ticketID int, tasks []Task) (planVersion int, err error)
	UpdateTaskStatus(ticketID int, planVersion, ordering int, status TaskStatus) error
	AppendTaskNote(ticketID int, planVersion, ordering int, note string) error
	ActivePlan(ticketID int) ([]Task, error)
	LatestSpec(ticketID int) (*Spec, bool, error)
}

// --- HTTP request bodies (also used by the go-concourse client) ---

type CreateRequest struct {
	Title           string   `json:"title"`
	Body            string   `json:"body"`
	Origin          string   `json:"origin,omitempty"` // default "web"; fly sends "fly"
	Repo            string   `json:"repo"`
	TargetBranch    string   `json:"target_branch,omitempty"`
	WorkflowName    string   `json:"workflow_name,omitempty"`
	WorkflowVersion *int     `json:"workflow_version,omitempty"`
	BudgetUSD       *float64 `json:"budget_usd,omitempty"`
	ExternalRef     string   `json:"external_ref,omitempty"`
}

type UpdateRequest struct {
	Title           *string  `json:"title,omitempty"`
	Body            *string  `json:"body,omitempty"`
	BudgetUSD       *float64 `json:"budget_usd,omitempty"`
	WorkflowName    *string  `json:"workflow_name,omitempty"`
	WorkflowVersion *int     `json:"workflow_version,omitempty"`
	TargetBranch    *string  `json:"target_branch,omitempty"`
}

type TransitionRequest struct {
	From          State  `json:"from"`
	To            State  `json:"to"`
	PipelineRunID *int   `json:"pipeline_run_id,omitempty"`
	Branch        string `json:"branch,omitempty"`
	ErrorDetail   string `json:"error_detail,omitempty"`
}

// SpecSubmission mirrors the §3.2 submit_spec tool input.
type SpecSubmission struct {
	Title              string   `json:"title"`
	Body               string   `json:"body"`
	AcceptanceCriteria []string `json:"acceptance_criteria,omitempty"`
	Links              []Link   `json:"links,omitempty"`
}

// PlanSubmission mirrors the §3.2 submit_plan tool input.
type PlanSubmission struct {
	Tasks []PlanTask `json:"tasks"`
}

type PlanTask struct {
	Title  string `json:"title"`
	Detail string `json:"detail,omitempty"`
}

// TaskStatusRequest mirrors the §3.2 update_task_status tool input.
type TaskStatusRequest struct {
	Status TaskStatus `json:"status"`
	Note   string     `json:"note,omitempty"`
}
```

- [ ] Run to verify pass: `go test ./agent/api/tickets/` — expect both tests green.

- [ ] Write the failing MemoryStore test `agent/api/tickets/memory_store_test.go`:

```go
package tickets_test

import (
	"errors"
	"testing"

	"github.com/concourse/concourse/agent/api/tickets"
)

func newTicket() *tickets.Ticket {
	return &tickets.Ticket{
		Title: "fix flaky spec", Body: "it flakes", Origin: "web",
		Repo: "tdmtrader/concourse", TargetBranch: "main",
		UserName: "tdm", CreatedBy: "tdm",
	}
}

func TestMemoryStoreCreateGetListUpdate(t *testing.T) {
	s := tickets.NewMemoryStore()

	id, err := s.Create(newTicket())
	if err != nil || id != 1 {
		t.Fatalf("Create = %d, %v; want 1, nil", id, err)
	}

	got, found, err := s.Get(id)
	if err != nil || !found {
		t.Fatalf("Get = %v, %v, %v", got, found, err)
	}
	if got.State != tickets.StateDraft || got.Title != "fix flaky spec" || got.CreatedAt == 0 {
		t.Errorf("unexpected ticket: %+v", got)
	}

	newTitle := "fix the flaky spec"
	budget := 5.0
	if err := s.Update(id, tickets.Update{Title: &newTitle, BudgetUSD: &budget}); err != nil {
		t.Fatalf("Update: %v", err)
	}
	got, _, _ = s.Get(id)
	if got.Title != newTitle || got.BudgetUSD == nil || *got.BudgetUSD != 5.0 {
		t.Errorf("update not applied: %+v", got)
	}

	all, err := s.List(tickets.ListFilter{})
	if err != nil || len(all) != 1 {
		t.Fatalf("List = %d, %v", len(all), err)
	}
	none, _ := s.List(tickets.ListFilter{State: tickets.StateQueued})
	if len(none) != 0 {
		t.Errorf("state filter leaked: %+v", none)
	}
}

func TestMemoryStoreTransition(t *testing.T) {
	s := tickets.NewMemoryStore()
	id, _ := s.Create(newTicket())

	if err := s.Transition(id, tickets.StateDraft, tickets.StateQueued, tickets.TransitionMeta{}); err != nil {
		t.Fatalf("draft->queued: %v", err)
	}
	err := s.Transition(id, tickets.StateDraft, tickets.StateQueued, tickets.TransitionMeta{})
	if !errors.Is(err, tickets.ErrStaleTransition) {
		t.Errorf("stale from-state: got %v, want ErrStaleTransition", err)
	}
	err = s.Transition(id, tickets.StateQueued, tickets.StateMerged, tickets.TransitionMeta{})
	if !errors.Is(err, tickets.ErrInvalidTransition) {
		t.Errorf("illegal edge: got %v, want ErrInvalidTransition", err)
	}
	err = s.Transition(999, tickets.StateDraft, tickets.StateQueued, tickets.TransitionMeta{})
	if !errors.Is(err, tickets.ErrTicketNotFound) {
		t.Errorf("missing ticket: got %v, want ErrTicketNotFound", err)
	}
}

func TestMemoryStoreSpecsAndPlans(t *testing.T) {
	s := tickets.NewMemoryStore()
	id, _ := s.Create(newTicket())

	v, err := s.SubmitSpec(id, tickets.Spec{Title: "spec", Body: "b", SubmittedBy: "run-1-platform"})
	if err != nil || v != 1 {
		t.Fatalf("SubmitSpec = %d, %v", v, err)
	}
	v, _ = s.SubmitSpec(id, tickets.Spec{Title: "spec2", Body: "b2"})
	if v != 2 {
		t.Fatalf("second SubmitSpec = %d, want 2", v)
	}
	latest, found, _ := s.LatestSpec(id)
	if !found || latest.Title != "spec2" || latest.Version != 2 {
		t.Errorf("LatestSpec = %+v, %v", latest, found)
	}

	pv, err := s.SubmitPlan(id, []tickets.Task{{Title: "one"}, {Title: "two"}})
	if err != nil || pv != 1 {
		t.Fatalf("SubmitPlan = %d, %v", pv, err)
	}
	pv, _ = s.SubmitPlan(id, []tickets.Task{{Title: "redo"}})
	if pv != 2 {
		t.Fatalf("second SubmitPlan = %d, want 2", pv)
	}
	active, _ := s.ActivePlan(id)
	if len(active) != 1 || active[0].Title != "redo" || active[0].Ordering != 1 ||
		active[0].Status != tickets.TaskPending {
		t.Errorf("ActivePlan = %+v", active)
	}

	if err := s.UpdateTaskStatus(id, 2, 1, tickets.TaskDone); err != nil {
		t.Fatalf("UpdateTaskStatus: %v", err)
	}
	if err := s.AppendTaskNote(id, 2, 1, "was easy"); err != nil {
		t.Fatalf("AppendTaskNote: %v", err)
	}
	active, _ = s.ActivePlan(id)
	if active[0].Status != tickets.TaskDone || active[0].Detail != "> was easy" {
		t.Errorf("task after update = %+v", active[0])
	}
	err = s.UpdateTaskStatus(id, 2, 99, tickets.TaskDone)
	if !errors.Is(err, tickets.ErrTaskNotFound) {
		t.Errorf("missing task: got %v, want ErrTaskNotFound", err)
	}
}
```

- [ ] Run to verify it fails: `go test ./agent/api/tickets/` — expected failure: `undefined: tickets.NewMemoryStore`.

- [ ] Write `agent/api/tickets/memory_store.go`:

```go
package tickets

import (
	"sync"
	"time"
)

// MemoryStore is an in-memory Store for testing (reviews.MemoryStore
// precedent). It mirrors the DB factory's semantics, including the
// single-writer transition rules.
type MemoryStore struct {
	mu      sync.Mutex
	nextID  int
	byID    map[int]*Ticket
	specs   map[int][]Spec // keyed by ticket id, ascending version
	tasks   map[int][]Task // keyed by ticket id, all plan versions
	taskSeq int
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{byID: map[int]*Ticket{}, specs: map[int][]Spec{}, tasks: map[int][]Task{}}
}

func (m *MemoryStore) Create(t *Ticket) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.nextID++
	cp := *t
	cp.ID = m.nextID
	cp.State = StateDraft
	if cp.Origin == "" {
		cp.Origin = "web"
	}
	if cp.TargetBranch == "" {
		cp.TargetBranch = "main"
	}
	now := time.Now().Unix()
	cp.CreatedAt, cp.UpdatedAt = now, now
	m.byID[cp.ID] = &cp
	return cp.ID, nil
}

func (m *MemoryStore) Get(id int) (*Ticket, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	t, ok := m.byID[id]
	if !ok {
		return nil, false, nil
	}
	cp := *t
	return &cp, true, nil
}

func (m *MemoryStore) List(filter ListFilter) ([]Ticket, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []Ticket{}
	// newest-first by id (ids are monotonic here)
	for id := m.nextID; id >= 1; id-- {
		t, ok := m.byID[id]
		if !ok {
			continue
		}
		if filter.State != "" && t.State != filter.State {
			continue
		}
		if filter.Repo != "" && t.Repo != filter.Repo {
			continue
		}
		if filter.Origin != "" && t.Origin != filter.Origin {
			continue
		}
		out = append(out, *t)
		if filter.Limit > 0 && len(out) >= filter.Limit {
			break
		}
	}
	return out, nil
}

func (m *MemoryStore) Update(id int, upd Update) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	t, ok := m.byID[id]
	if !ok {
		return ErrTicketNotFound
	}
	if upd.Title != nil {
		t.Title = *upd.Title
	}
	if upd.Body != nil {
		t.Body = *upd.Body
	}
	if upd.BudgetUSD != nil {
		v := *upd.BudgetUSD
		t.BudgetUSD = &v
	}
	if upd.WorkflowName != nil {
		t.WorkflowName = *upd.WorkflowName
	}
	if upd.WorkflowVersion != nil {
		v := *upd.WorkflowVersion
		t.WorkflowVersion = &v
	}
	if upd.TargetBranch != nil {
		t.TargetBranch = *upd.TargetBranch
	}
	t.UpdatedAt = time.Now().Unix()
	return nil
}

func (m *MemoryStore) Transition(id int, from, to State, meta TransitionMeta) error {
	if !ValidTransition(from, to) {
		return ErrInvalidTransition
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	t, ok := m.byID[id]
	if !ok {
		return ErrTicketNotFound
	}
	if t.State != from {
		return ErrStaleTransition
	}
	t.State = to
	t.UpdatedAt = time.Now().Unix()
	switch to {
	case StateQueued:
		t.CompletedAt = 0
		if from == StateRunning {
			// running → queued (retryable platform error OR rejected
			// send_back checkpoint re-dispatch; attempt_count++).
			t.AttemptCount++
		}
	case StateRunning:
		if meta.PipelineRunID != nil {
			v := *meta.PipelineRunID
			t.PipelineRunID = &v
		}
	case StateNeedsReview:
		if meta.Branch != "" {
			t.Branch = meta.Branch
		}
	case StateMerged, StateMergedWithFixes, StateSentBack, StateAbandoned, StateConcluded, StateFailed, StateErrored:
		t.CompletedAt = time.Now().Unix()
		if to == StateErrored {
			t.ErrorDetail = meta.ErrorDetail
		}
	}
	return nil
}

func (m *MemoryStore) SubmitSpec(ticketID int, spec Spec) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.byID[ticketID]; !ok {
		return 0, ErrTicketNotFound
	}
	version := len(m.specs[ticketID]) + 1
	spec.ID = version
	spec.TicketID = ticketID
	spec.Version = version
	spec.CreatedAt = time.Now().Unix()
	if spec.AcceptanceCriteria == nil {
		spec.AcceptanceCriteria = []string{}
	}
	if spec.Links == nil {
		spec.Links = []Link{}
	}
	m.specs[ticketID] = append(m.specs[ticketID], spec)
	return version, nil
}

func (m *MemoryStore) LatestSpec(ticketID int) (*Spec, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	specs := m.specs[ticketID]
	if len(specs) == 0 {
		return nil, false, nil
	}
	cp := specs[len(specs)-1]
	return &cp, true, nil
}

func (m *MemoryStore) SubmitPlan(ticketID int, ts []Task) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.byID[ticketID]; !ok {
		return 0, ErrTicketNotFound
	}
	maxVersion := 0
	for _, existing := range m.tasks[ticketID] {
		if existing.PlanVersion > maxVersion {
			maxVersion = existing.PlanVersion
		}
	}
	planVersion := maxVersion + 1
	for i, task := range ts {
		m.taskSeq++
		task.ID = m.taskSeq
		task.TicketID = ticketID
		task.PlanVersion = planVersion
		task.Ordering = i + 1
		if task.Status == "" {
			task.Status = TaskPending
		}
		task.UpdatedAt = time.Now().Unix()
		m.tasks[ticketID] = append(m.tasks[ticketID], task)
	}
	return planVersion, nil
}

func (m *MemoryStore) ActivePlan(ticketID int) ([]Task, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	maxVersion := 0
	for _, t := range m.tasks[ticketID] {
		if t.PlanVersion > maxVersion {
			maxVersion = t.PlanVersion
		}
	}
	out := []Task{}
	for _, t := range m.tasks[ticketID] {
		if t.PlanVersion == maxVersion {
			out = append(out, t)
		}
	}
	return out, nil
}

func (m *MemoryStore) UpdateTaskStatus(ticketID int, planVersion, ordering int, status TaskStatus) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i, t := range m.tasks[ticketID] {
		if t.PlanVersion == planVersion && t.Ordering == ordering {
			m.tasks[ticketID][i].Status = status
			m.tasks[ticketID][i].UpdatedAt = time.Now().Unix()
			return nil
		}
	}
	return ErrTaskNotFound
}

func (m *MemoryStore) AppendTaskNote(ticketID int, planVersion, ordering int, note string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i, t := range m.tasks[ticketID] {
		if t.PlanVersion == planVersion && t.Ordering == ordering {
			if t.Detail == "" {
				m.tasks[ticketID][i].Detail = "> " + note
			} else {
				m.tasks[ticketID][i].Detail = t.Detail + "\n\n> " + note
			}
			m.tasks[ticketID][i].UpdatedAt = time.Now().Unix()
			return nil
		}
	}
	return ErrTaskNotFound
}
```

- [ ] Run to verify pass: `go test ./agent/api/tickets/` — expect all green. Also `go vet ./agent/api/tickets/`.

- [ ] Commit: `git add agent/api/tickets && git commit -m "feat(ticket-core): tickets domain types, state machine, and MemoryStore"`

---

### Task 4: DB factory — Create/Get/List/Update

`atc/db/agent_tickets_factory.go` following the `agent_reviews_factory.go` recipe (`atc/db/agent_reviews_factory.go:14-24`: interface embedding the domain Store, `NewX(conn DbConn)`, squirrel `psql` + raw SQL, epoch scans). The atc/db Ginkgo suite provides `dbConn` migrated to HEAD (see `atc/db/agent_reviews_factory_test.go:18`).

**Files:**
- Create: `atc/db/agent_tickets_factory.go`
- Test: `atc/db/agent_tickets_factory_test.go`

**Steps:**

- [ ] Write the failing Ginkgo spec `atc/db/agent_tickets_factory_test.go`:

```go
package db_test

import (
	"database/sql"

	"github.com/concourse/concourse/agent/api/tickets"
	"github.com/concourse/concourse/atc/db"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("AgentTicketsFactory", func() {
	var factory db.AgentTicketsFactory

	BeforeEach(func() {
		factory = db.NewAgentTicketsFactory(dbConn)
	})

	newTicket := func(title, repo string) *tickets.Ticket {
		budget := 12.5
		version := 3
		return &tickets.Ticket{
			Title: title, Body: "body md", Origin: "fly", Repo: repo,
			WorkflowName: "standard-dev", WorkflowVersion: &version,
			BudgetUSD: &budget, UserName: "tdm", CreatedBy: "tdm",
		}
	}

	It("creates a draft ticket and round-trips every column", func() {
		id, err := factory.Create(newTicket("fix flaky spec", "tdmtrader/concourse"))
		Expect(err).ToNot(HaveOccurred())
		Expect(id).To(BeNumerically(">", 0))

		got, found, err := factory.Get(id)
		Expect(err).ToNot(HaveOccurred())
		Expect(found).To(BeTrue())
		Expect(got.State).To(Equal(tickets.StateDraft))
		Expect(got.Origin).To(Equal("fly"))
		Expect(got.TargetBranch).To(Equal("main")) // defaulted
		Expect(got.WorkflowName).To(Equal("standard-dev"))
		Expect(*got.WorkflowVersion).To(Equal(3))
		Expect(got.WorkflowDefinitionID).To(BeNil())
		Expect(*got.BudgetUSD).To(Equal(12.5))
		Expect(got.UserID).To(BeNil())
		Expect(got.UserName).To(Equal("tdm"))
		Expect(got.CreatedBy).To(Equal("tdm"))
		Expect(got.ExternalRef).To(Equal(""))
		Expect(got.PipelineRunID).To(BeNil())
		Expect(got.AttemptCount).To(BeZero())
		Expect(got.CreatedAt).To(BeNumerically(">", 0))
		Expect(got.UpdatedAt).To(BeNumerically(">", 0))
		Expect(got.CompletedAt).To(BeZero())
	})

	It("Get returns found=false for a missing id", func() {
		_, found, err := factory.Get(999999)
		Expect(err).ToNot(HaveOccurred())
		Expect(found).To(BeFalse())
	})

	It("lists newest-first and honors filters", func() {
		id1, err := factory.Create(newTicket("a", "repo/one"))
		Expect(err).ToNot(HaveOccurred())
		id2, err := factory.Create(newTicket("b", "repo/two"))
		Expect(err).ToNot(HaveOccurred())

		all, err := factory.List(tickets.ListFilter{})
		Expect(err).ToNot(HaveOccurred())
		Expect(all).To(HaveLen(2))
		Expect(all[0].ID).To(Equal(id2)) // newest first
		Expect(all[1].ID).To(Equal(id1))

		byRepo, err := factory.List(tickets.ListFilter{Repo: "repo/one"})
		Expect(err).ToNot(HaveOccurred())
		Expect(byRepo).To(HaveLen(1))
		Expect(byRepo[0].ID).To(Equal(id1))

		byState, err := factory.List(tickets.ListFilter{State: tickets.StateQueued})
		Expect(err).ToNot(HaveOccurred())
		Expect(byState).To(BeEmpty())

		limited, err := factory.List(tickets.ListFilter{Limit: 1})
		Expect(err).ToNot(HaveOccurred())
		Expect(limited).To(HaveLen(1))
		Expect(limited[0].ID).To(Equal(id2))
	})

	It("updates only the provided fields and bumps updated_at", func() {
		id, err := factory.Create(newTicket("t", "r"))
		Expect(err).ToNot(HaveOccurred())

		var before sql.NullTime
		Expect(dbConn.QueryRow(`SELECT updated_at FROM agent_tickets WHERE id = $1`, id).
			Scan(&before)).To(Succeed())

		title := "new title"
		budget := 3.25
		Expect(factory.Update(id, tickets.Update{Title: &title, BudgetUSD: &budget})).To(Succeed())

		got, _, err := factory.Get(id)
		Expect(err).ToNot(HaveOccurred())
		Expect(got.Title).To(Equal("new title"))
		Expect(*got.BudgetUSD).To(Equal(3.25))
		Expect(got.Body).To(Equal("body md"))            // untouched
		Expect(got.WorkflowName).To(Equal("standard-dev")) // untouched
	})

	It("Update returns ErrTicketNotFound for a missing id", func() {
		title := "x"
		Expect(factory.Update(424242, tickets.Update{Title: &title})).
			To(MatchError(tickets.ErrTicketNotFound))
	})
})
```

- [ ] Run to verify it fails: `ginkgo --focus="AgentTicketsFactory" ./atc/db/` — expected failure: compile error `undefined: db.AgentTicketsFactory`. (If `database "testdb_template" already exists` appears, another test run is live — wait for it, per CLAUDE.md.)

- [ ] Write `atc/db/agent_tickets_factory.go` (CRUD half; Transition and spec/plan methods land in Tasks 5–6 in this same file):

```go
package db

import (
	"database/sql"
	"errors"
	"strconv"

	sq "github.com/Masterminds/squirrel"
	"github.com/concourse/concourse/agent/api/tickets"
)

//counterfeiter:generate . AgentTicketsFactory
type AgentTicketsFactory interface {
	tickets.Store
}

func NewAgentTicketsFactory(conn DbConn) AgentTicketsFactory {
	return &agentTicketsFactory{conn: conn}
}

type agentTicketsFactory struct {
	conn DbConn
}

func ticketNullInt(p *int) any {
	if p == nil {
		return nil
	}
	return *p
}

func ticketNullFloat(p *float64) any {
	if p == nil {
		return nil
	}
	return *p
}

// Create inserts a ticket in state 'draft' (queueing is a separate
// Transition call — single-writer discipline) and returns its id, which
// IS the ticket number (branch agent/ticket-<id>, contracts §1.7).
func (f *agentTicketsFactory) Create(t *tickets.Ticket) (int, error) {
	origin := t.Origin
	if origin == "" {
		origin = "web"
	}
	targetBranch := t.TargetBranch
	if targetBranch == "" {
		targetBranch = "main"
	}

	var id int
	err := psql.Insert("agent_tickets").
		Columns(
			"title", "body", "origin", "repo", "target_branch",
			"workflow_name", "workflow_version", "budget_usd",
			"user_id", "user_name", "created_by", "external_ref",
		).
		Values(
			t.Title, t.Body, origin, t.Repo, targetBranch,
			t.WorkflowName, ticketNullInt(t.WorkflowVersion), ticketNullFloat(t.BudgetUSD),
			ticketNullInt(t.UserID), t.UserName, t.CreatedBy, t.ExternalRef,
		).
		Suffix("RETURNING id").
		RunWith(f.conn).
		QueryRow().
		Scan(&id)
	return id, err
}

const ticketColumns = `t.id, t.title, t.body, t.state, t.origin, t.repo, t.target_branch,
	t.workflow_name, t.workflow_version, t.workflow_definition_id,
	t.budget_usd, t.user_id, t.user_name, t.created_by, t.external_ref,
	t.pipeline_run_id, t.branch, t.attempt_count, t.error_detail,
	EXTRACT(EPOCH FROM t.created_at)::bigint,
	EXTRACT(EPOCH FROM t.updated_at)::bigint,
	COALESCE(EXTRACT(EPOCH FROM t.completed_at)::bigint, 0)`

type ticketScanner interface {
	Scan(...any) error
}

func scanTicket(row ticketScanner) (*tickets.Ticket, error) {
	var t tickets.Ticket
	var wfVersion, wfDefID, userID, runID sql.NullInt64
	var budget sql.NullFloat64
	err := row.Scan(
		&t.ID, &t.Title, &t.Body, &t.State, &t.Origin, &t.Repo, &t.TargetBranch,
		&t.WorkflowName, &wfVersion, &wfDefID,
		&budget, &userID, &t.UserName, &t.CreatedBy, &t.ExternalRef,
		&runID, &t.Branch, &t.AttemptCount, &t.ErrorDetail,
		&t.CreatedAt, &t.UpdatedAt, &t.CompletedAt,
	)
	if err != nil {
		return nil, err
	}
	if wfVersion.Valid {
		v := int(wfVersion.Int64)
		t.WorkflowVersion = &v
	}
	if wfDefID.Valid {
		v := int(wfDefID.Int64)
		t.WorkflowDefinitionID = &v
	}
	if userID.Valid {
		v := int(userID.Int64)
		t.UserID = &v
	}
	if runID.Valid {
		v := int(runID.Int64)
		t.PipelineRunID = &v
	}
	if budget.Valid {
		v := budget.Float64
		t.BudgetUSD = &v
	}
	return &t, nil
}

func (f *agentTicketsFactory) Get(id int) (*tickets.Ticket, bool, error) {
	t, err := scanTicket(f.conn.QueryRow(
		`SELECT `+ticketColumns+` FROM agent_tickets t WHERE t.id = $1`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return t, true, nil
}

func (f *agentTicketsFactory) List(filter tickets.ListFilter) ([]tickets.Ticket, error) {
	query := `SELECT ` + ticketColumns + ` FROM agent_tickets t WHERE true`
	args := []any{}
	if filter.State != "" {
		args = append(args, string(filter.State))
		query += ` AND t.state = $` + strconv.Itoa(len(args))
	}
	if filter.Repo != "" {
		args = append(args, filter.Repo)
		query += ` AND t.repo = $` + strconv.Itoa(len(args))
	}
	if filter.Origin != "" {
		args = append(args, filter.Origin)
		query += ` AND t.origin = $` + strconv.Itoa(len(args))
	}
	query += ` ORDER BY t.created_at DESC, t.id DESC`
	if filter.Limit > 0 {
		args = append(args, filter.Limit)
		query += ` LIMIT $` + strconv.Itoa(len(args))
	}

	rows, err := f.conn.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	results := []tickets.Ticket{}
	for rows.Next() {
		t, err := scanTicket(rows)
		if err != nil {
			return nil, err
		}
		results = append(results, *t)
	}
	return results, rows.Err()
}

func (f *agentTicketsFactory) Update(id int, upd tickets.Update) error {
	q := psql.Update("agent_tickets").
		Set("updated_at", sq.Expr("now()")).
		Where(sq.Eq{"id": id})
	if upd.Title != nil {
		q = q.Set("title", *upd.Title)
	}
	if upd.Body != nil {
		q = q.Set("body", *upd.Body)
	}
	if upd.BudgetUSD != nil {
		q = q.Set("budget_usd", *upd.BudgetUSD)
	}
	if upd.WorkflowName != nil {
		q = q.Set("workflow_name", *upd.WorkflowName)
	}
	if upd.WorkflowVersion != nil {
		q = q.Set("workflow_version", *upd.WorkflowVersion)
	}
	if upd.TargetBranch != nil {
		q = q.Set("target_branch", *upd.TargetBranch)
	}

	res, err := q.RunWith(f.conn).Exec()
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return tickets.ErrTicketNotFound
	}
	return nil
}
```

The remaining `tickets.Store` methods don't exist yet, so the interface is unsatisfied — add temporary compile stubs at the end of the file, each `panic("implemented in task 5/6")`? **No.** Instead implement the interface incrementally by declaring the factory methods as they land and keeping `AgentTicketsFactory interface { tickets.Store }` from day one: write minimal not-yet-implemented bodies now and replace them in Tasks 5–6 (they are real code, tested and replaced within this same plan, never merged unimplemented — Tasks 4–6 form one PR-sized unit):

```go
// Transition is implemented in Task 5 (single-writer state machine).
func (f *agentTicketsFactory) Transition(id int, from, to tickets.State, meta tickets.TransitionMeta) error {
	return errors.New("agentTicketsFactory.Transition: not yet implemented (plan task 5)")
}

// The spec/plan family is implemented in Task 6.
func (f *agentTicketsFactory) SubmitSpec(ticketID int, spec tickets.Spec) (int, error) {
	return 0, errors.New("agentTicketsFactory.SubmitSpec: not yet implemented (plan task 6)")
}

func (f *agentTicketsFactory) SubmitPlan(ticketID int, ts []tickets.Task) (int, error) {
	return 0, errors.New("agentTicketsFactory.SubmitPlan: not yet implemented (plan task 6)")
}

func (f *agentTicketsFactory) UpdateTaskStatus(ticketID int, planVersion, ordering int, status tickets.TaskStatus) error {
	return errors.New("agentTicketsFactory.UpdateTaskStatus: not yet implemented (plan task 6)")
}

func (f *agentTicketsFactory) AppendTaskNote(ticketID int, planVersion, ordering int, note string) error {
	return errors.New("agentTicketsFactory.AppendTaskNote: not yet implemented (plan task 6)")
}

func (f *agentTicketsFactory) ActivePlan(ticketID int) ([]tickets.Task, error) {
	return nil, errors.New("agentTicketsFactory.ActivePlan: not yet implemented (plan task 6)")
}

func (f *agentTicketsFactory) LatestSpec(ticketID int) (*tickets.Spec, bool, error) {
	return nil, false, errors.New("agentTicketsFactory.LatestSpec: not yet implemented (plan task 6)")
}
```

- [ ] Run to verify pass: `ginkgo --focus="AgentTicketsFactory" ./atc/db/` — expect the 5 CRUD specs green.

- [ ] Commit: `git add atc/db && git commit -m "feat(ticket-core): AgentTicketsFactory CRUD over agent_tickets"`

---

### Task 5: DB factory — `Transition`, the single writer

The one function that mutates `agent_tickets.state`. Optimistic concurrency (`WHERE id AND state = from`), §1.7 side effects, and the three error cases. Every later component (dispatch wave 4 — both its queued-dispatch loop and its run-completion reconciler, harvest wave 3, platform-mcp wave 3, outcome watcher wave 4) calls this and nothing else. `ErrStaleTransition`/`ErrTicketNotFound` are BENIGN to racing callers (harvest vs. reconciler on `running→needs_review` — they log and continue), which is exactly what the optimistic `WHERE state = from` guard is for.

**Files:**
- Modify: `atc/db/agent_tickets_factory.go` (replace the Task-4 `Transition` stub)
- Test: `atc/db/agent_tickets_factory_test.go` (append a `Describe`)

**Steps:**

- [ ] Append the failing transition specs inside the top-level `Describe("AgentTicketsFactory", ...)` in `atc/db/agent_tickets_factory_test.go` (uses the existing `factory` and `newTicket` helpers):

```go
	Describe("Transition (the single writer)", func() {
		var id int

		BeforeEach(func() {
			var err error
			id, err = factory.Create(newTicket("lifecycle", "tdmtrader/concourse"))
			Expect(err).ToNot(HaveOccurred())
		})

		It("walks draft→queued→running→needs_review→merged recording side effects", func() {
			Expect(factory.Transition(id, tickets.StateDraft, tickets.StateQueued,
				tickets.TransitionMeta{})).To(Succeed())
			var queuedAt sql.NullTime
			Expect(dbConn.QueryRow(`SELECT queued_at FROM agent_tickets WHERE id = $1`, id).
				Scan(&queuedAt)).To(Succeed())
			Expect(queuedAt.Valid).To(BeTrue())

			runID := 42
			Expect(factory.Transition(id, tickets.StateQueued, tickets.StateRunning,
				tickets.TransitionMeta{PipelineRunID: &runID})).To(Succeed())
			got, _, err := factory.Get(id)
			Expect(err).ToNot(HaveOccurred())
			Expect(*got.PipelineRunID).To(Equal(42))
			var dispatchedAt sql.NullTime
			Expect(dbConn.QueryRow(`SELECT dispatched_at FROM agent_tickets WHERE id = $1`, id).
				Scan(&dispatchedAt)).To(Succeed())
			Expect(dispatchedAt.Valid).To(BeTrue())

			Expect(factory.Transition(id, tickets.StateRunning, tickets.StateNeedsReview,
				tickets.TransitionMeta{Branch: "agent/ticket-7"})).To(Succeed())
			got, _, _ = factory.Get(id)
			Expect(got.Branch).To(Equal("agent/ticket-7"))
			Expect(got.CompletedAt).To(BeZero()) // needs_review is not terminal

			Expect(factory.Transition(id, tickets.StateNeedsReview, tickets.StateMerged,
				tickets.TransitionMeta{})).To(Succeed())
			got, _, _ = factory.Get(id)
			Expect(got.State).To(Equal(tickets.StateMerged))
			Expect(got.CompletedAt).To(BeNumerically(">", 0))
		})

		It("stamps completed_at on needs_review→concluded (spike disposition, terminal)", func() {
			Expect(factory.Transition(id, tickets.StateDraft, tickets.StateQueued,
				tickets.TransitionMeta{})).To(Succeed())
			Expect(factory.Transition(id, tickets.StateQueued, tickets.StateRunning,
				tickets.TransitionMeta{})).To(Succeed())
			Expect(factory.Transition(id, tickets.StateRunning, tickets.StateNeedsReview,
				tickets.TransitionMeta{})).To(Succeed())

			// needs_review → concluded: explicit human disposition — "run
			// finished, human reviewed, no merge intended" (FLOWS.md §3
			// spike-research). Positive sibling of abandoned; TERMINAL.
			Expect(factory.Transition(id, tickets.StateNeedsReview, tickets.StateConcluded,
				tickets.TransitionMeta{})).To(Succeed())
			got, _, _ := factory.Get(id)
			Expect(got.State).To(Equal(tickets.StateConcluded))
			Expect(got.CompletedAt).To(BeNumerically(">", 0))

			// No exits: concluded tickets never re-enter the queue.
			Expect(factory.Transition(id, tickets.StateConcluded, tickets.StateQueued,
				tickets.TransitionMeta{})).To(MatchError(tickets.ErrInvalidTransition))
		})

		It("records error_detail on errored, clears completed_at on requeue, and counts attempts", func() {
			Expect(factory.Transition(id, tickets.StateDraft, tickets.StateQueued,
				tickets.TransitionMeta{})).To(Succeed())
			Expect(factory.Transition(id, tickets.StateQueued, tickets.StateRunning,
				tickets.TransitionMeta{})).To(Succeed())
			Expect(factory.Transition(id, tickets.StateRunning, tickets.StateErrored,
				tickets.TransitionMeta{ErrorDetail: "web node died"})).To(Succeed())

			got, _, _ := factory.Get(id)
			Expect(got.ErrorDetail).To(Equal("web node died"))
			Expect(got.CompletedAt).To(BeNumerically(">", 0))
			Expect(got.AttemptCount).To(BeZero()) // errored, not requeued

			Expect(factory.Transition(id, tickets.StateErrored, tickets.StateQueued,
				tickets.TransitionMeta{})).To(Succeed())
			got, _, _ = factory.Get(id)
			Expect(got.CompletedAt).To(BeZero()) // cleared on requeue

			Expect(factory.Transition(id, tickets.StateQueued, tickets.StateRunning,
				tickets.TransitionMeta{})).To(Succeed())
			// running→queued (retryable platform error OR rejected send_back
			// checkpoint re-dispatch; attempt_count++). Second legitimate
			// caller: dispatch's run-completion reconciler (checkpoint-seam
			// delta §6, 2026-07-09), which requeues with TransitionMeta{}
			// exactly as below — these side-effect assertions are its
			// contract; do not narrow them.
			Expect(factory.Transition(id, tickets.StateRunning, tickets.StateQueued,
				tickets.TransitionMeta{})).To(Succeed())
			got, _, _ = factory.Get(id)
			Expect(got.AttemptCount).To(Equal(1)) // running→queued increments
			Expect(got.CompletedAt).To(BeZero())  // stays cleared for re-dispatch
			var requeuedAt sql.NullTime
			Expect(dbConn.QueryRow(`SELECT queued_at FROM agent_tickets WHERE id = $1`, id).
				Scan(&requeuedAt)).To(Succeed())
			Expect(requeuedAt.Valid).To(BeTrue()) // queued_at re-stamped
		})

		It("rejects illegal edges without touching the row", func() {
			Expect(factory.Transition(id, tickets.StateDraft, tickets.StateMerged,
				tickets.TransitionMeta{})).To(MatchError(tickets.ErrInvalidTransition))
			got, _, _ := factory.Get(id)
			Expect(got.State).To(Equal(tickets.StateDraft))
		})

		It("returns ErrStaleTransition when the from-state no longer matches", func() {
			Expect(factory.Transition(id, tickets.StateDraft, tickets.StateQueued,
				tickets.TransitionMeta{})).To(Succeed())
			Expect(factory.Transition(id, tickets.StateDraft, tickets.StateQueued,
				tickets.TransitionMeta{})).To(MatchError(tickets.ErrStaleTransition))
		})

		It("returns ErrTicketNotFound for a missing ticket", func() {
			Expect(factory.Transition(987654, tickets.StateDraft, tickets.StateQueued,
				tickets.TransitionMeta{})).To(MatchError(tickets.ErrTicketNotFound))
		})
	})
```

- [ ] Run to verify it fails: `ginkgo --focus="AgentTicketsFactory" ./atc/db/` — expected failure: the seven new specs fail with `not yet implemented (plan task 5)`.

- [ ] Replace the Task-4 `Transition` stub in `atc/db/agent_tickets_factory.go` with the real single writer:

```go
// Transition is THE single writer of agent_tickets.state
// (00-shared-contracts.md §2.1). It validates the edge against the
// §1.7 state machine, then updates guarded by the expected `from`
// state (optimistic concurrency): a concurrent writer that moved the
// ticket first makes this call return ErrStaleTransition instead of
// silently double-applying. Side effects per the ticket-core contract
// addendum.
func (f *agentTicketsFactory) Transition(id int, from, to tickets.State, meta tickets.TransitionMeta) error {
	if !tickets.ValidTransition(from, to) {
		return tickets.ErrInvalidTransition
	}

	q := psql.Update("agent_tickets").
		Set("state", string(to)).
		Set("updated_at", sq.Expr("now()")).
		Where(sq.Eq{"id": id, "state": string(from)})

	switch to {
	case tickets.StateDraft: // unqueue
		q = q.Set("queued_at", nil)
	case tickets.StateQueued:
		q = q.Set("queued_at", sq.Expr("now()")).
			Set("completed_at", nil)
		if from == tickets.StateRunning {
			// running → queued (retryable platform error OR rejected
			// send_back checkpoint re-dispatch; attempt_count++) — called
			// by dispatch's retry path and its run-completion reconciler.
			q = q.Set("attempt_count", sq.Expr("attempt_count + 1"))
		}
	case tickets.StateRunning:
		q = q.Set("dispatched_at", sq.Expr("now()"))
		if meta.PipelineRunID != nil {
			q = q.Set("pipeline_run_id", *meta.PipelineRunID)
		}
	case tickets.StateNeedsReview:
		if meta.Branch != "" {
			q = q.Set("branch", meta.Branch)
		}
	case tickets.StateMerged, tickets.StateMergedWithFixes, tickets.StateSentBack,
		tickets.StateAbandoned, tickets.StateConcluded, tickets.StateFailed, tickets.StateErrored:
		q = q.Set("completed_at", sq.Expr("now()"))
		if to == tickets.StateErrored {
			q = q.Set("error_detail", meta.ErrorDetail)
		}
	}

	res, err := q.RunWith(f.conn).Exec()
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		// Distinguish "ticket gone" from "state moved under us".
		_, found, err := f.Get(id)
		if err != nil {
			return err
		}
		if !found {
			return tickets.ErrTicketNotFound
		}
		return tickets.ErrStaleTransition
	}
	return nil
}
```

- [ ] Run to verify pass: `ginkgo --focus="AgentTicketsFactory" ./atc/db/` — expect all specs green.

- [ ] Commit: `git add atc/db && git commit -m "feat(ticket-core): single-writer Transition with optimistic concurrency"`

---

### Task 6: DB factory — specs, plans, task status + counterfeiter fake

The `agent_ticket_specs` / `agent_ticket_tasks` half of the Store: versioned spec submission, plan replacement by version bump, task-status updates, note appends, and the reads (`LatestSpec`, `ActivePlan`). Version allocation runs in a transaction holding a `FOR UPDATE` lock on the ticket row so concurrent submits serialize (`f.conn.Begin()` returns the atc/db `Tx` from `atc/db/open.go:51`; `defer Rollback(tx)` is the package idiom, see `atc/db/team_factory.go:47`).

**Files:**
- Modify: `atc/db/agent_tickets_factory.go` (replace the six Task-4 stubs; add `encoding/json` import)
- Create: `atc/db/dbfakes/fake_agent_tickets_factory.go` (generated)
- Test: `atc/db/agent_tickets_factory_test.go` (append a `Describe`)

**Steps:**

- [ ] Append the failing spec/plan specs inside the top-level `Describe("AgentTicketsFactory", ...)`:

```go
	Describe("specs and plans", func() {
		var id int

		BeforeEach(func() {
			var err error
			id, err = factory.Create(newTicket("spec'd", "tdmtrader/concourse"))
			Expect(err).ToNot(HaveOccurred())
		})

		It("versions specs, keeps old rows, and returns the latest", func() {
			v, err := factory.SubmitSpec(id, tickets.Spec{
				Title: "spec", Body: "prose",
				AcceptanceCriteria: []string{"tests pass"},
				Links:              []tickets.Link{{Title: "design", URL: "https://x"}},
				SubmittedBy:        "run-42-platform",
			})
			Expect(err).ToNot(HaveOccurred())
			Expect(v).To(Equal(1))

			v, err = factory.SubmitSpec(id, tickets.Spec{Title: "spec2", Body: "prose2"})
			Expect(err).ToNot(HaveOccurred())
			Expect(v).To(Equal(2))

			latest, found, err := factory.LatestSpec(id)
			Expect(err).ToNot(HaveOccurred())
			Expect(found).To(BeTrue())
			Expect(latest.Version).To(Equal(2))
			Expect(latest.Title).To(Equal("spec2"))
			Expect(latest.AcceptanceCriteria).To(BeEmpty())
			Expect(latest.Links).To(BeEmpty())
			Expect(latest.CreatedAt).To(BeNumerically(">", 0))

			var count int
			Expect(dbConn.QueryRow(`SELECT COUNT(*) FROM agent_ticket_specs WHERE ticket_id = $1`, id).
				Scan(&count)).To(Succeed())
			Expect(count).To(Equal(2)) // v1 retained for process intelligence
		})

		It("round-trips acceptance criteria and links JSON", func() {
			_, err := factory.SubmitSpec(id, tickets.Spec{
				Title: "spec", Body: "prose",
				AcceptanceCriteria: []string{"a", "b"},
				Links:              []tickets.Link{{Title: "l1", URL: "u1"}, {Title: "l2", URL: "u2"}},
			})
			Expect(err).ToNot(HaveOccurred())

			latest, _, err := factory.LatestSpec(id)
			Expect(err).ToNot(HaveOccurred())
			Expect(latest.AcceptanceCriteria).To(Equal([]string{"a", "b"}))
			Expect(latest.Links).To(Equal([]tickets.Link{{Title: "l1", URL: "u1"}, {Title: "l2", URL: "u2"}}))
		})

		It("LatestSpec is found=false without specs; SubmitSpec 404s a missing ticket", func() {
			_, found, err := factory.LatestSpec(id)
			Expect(err).ToNot(HaveOccurred())
			Expect(found).To(BeFalse())

			_, err = factory.SubmitSpec(313131, tickets.Spec{Title: "t", Body: "b"})
			Expect(err).To(MatchError(tickets.ErrTicketNotFound))
		})

		It("replaces the active plan by bumping plan_version", func() {
			pv, err := factory.SubmitPlan(id, []tickets.Task{{Title: "one"}, {Title: "two", Detail: "d"}})
			Expect(err).ToNot(HaveOccurred())
			Expect(pv).To(Equal(1))

			pv, err = factory.SubmitPlan(id, []tickets.Task{{Title: "redone"}})
			Expect(err).ToNot(HaveOccurred())
			Expect(pv).To(Equal(2))

			active, err := factory.ActivePlan(id)
			Expect(err).ToNot(HaveOccurred())
			Expect(active).To(HaveLen(1))
			Expect(active[0].Title).To(Equal("redone"))
			Expect(active[0].Ordering).To(Equal(1))
			Expect(active[0].PlanVersion).To(Equal(2))
			Expect(active[0].Status).To(Equal(tickets.TaskPending))

			var total int
			Expect(dbConn.QueryRow(`SELECT COUNT(*) FROM agent_ticket_tasks WHERE ticket_id = $1`, id).
				Scan(&total)).To(Succeed())
			Expect(total).To(Equal(3)) // v1's two tasks retained

			_, err = factory.SubmitPlan(313131, []tickets.Task{{Title: "x"}})
			Expect(err).To(MatchError(tickets.ErrTicketNotFound))
		})

		It("updates task status and appends notes as blockquotes", func() {
			_, err := factory.SubmitPlan(id, []tickets.Task{{Title: "one"}})
			Expect(err).ToNot(HaveOccurred())

			Expect(factory.UpdateTaskStatus(id, 1, 1, tickets.TaskInProgress)).To(Succeed())
			Expect(factory.AppendTaskNote(id, 1, 1, "halfway")).To(Succeed())
			Expect(factory.AppendTaskNote(id, 1, 1, "done now")).To(Succeed())

			active, err := factory.ActivePlan(id)
			Expect(err).ToNot(HaveOccurred())
			Expect(active[0].Status).To(Equal(tickets.TaskInProgress))
			Expect(active[0].Detail).To(Equal("> halfway\n\n> done now"))
			Expect(active[0].UpdatedAt).To(BeNumerically(">", 0))

			Expect(factory.UpdateTaskStatus(id, 1, 99, tickets.TaskDone)).
				To(MatchError(tickets.ErrTaskNotFound))
			Expect(factory.AppendTaskNote(id, 9, 1, "x")).
				To(MatchError(tickets.ErrTaskNotFound))
		})
	})
```

- [ ] Run to verify it fails: `ginkgo --focus="AgentTicketsFactory" ./atc/db/` — expected failure: the new specs fail with `not yet implemented (plan task 6)`.

- [ ] Replace the six Task-4 stubs in `atc/db/agent_tickets_factory.go` (add `"encoding/json"` to imports):

```go
func emptyIfNilStrings(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func emptyIfNilLinks(l []tickets.Link) []tickets.Link {
	if l == nil {
		return []tickets.Link{}
	}
	return l
}

// lockTicket takes a FOR UPDATE row lock on the ticket inside tx so
// concurrent spec/plan submissions serialize their version allocation.
func lockTicket(tx Tx, ticketID int) error {
	var one int
	err := tx.QueryRow(`SELECT 1 FROM agent_tickets WHERE id = $1 FOR UPDATE`, ticketID).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return tickets.ErrTicketNotFound
	}
	return err
}

func (f *agentTicketsFactory) SubmitSpec(ticketID int, spec tickets.Spec) (int, error) {
	criteria, err := json.Marshal(emptyIfNilStrings(spec.AcceptanceCriteria))
	if err != nil {
		return 0, err
	}
	links, err := json.Marshal(emptyIfNilLinks(spec.Links))
	if err != nil {
		return 0, err
	}

	tx, err := f.conn.Begin()
	if err != nil {
		return 0, err
	}
	defer Rollback(tx)

	if err := lockTicket(tx, ticketID); err != nil {
		return 0, err
	}

	var version int
	err = tx.QueryRow(
		`SELECT COALESCE(MAX(version), 0) + 1 FROM agent_ticket_specs WHERE ticket_id = $1`,
		ticketID).Scan(&version)
	if err != nil {
		return 0, err
	}

	_, err = tx.Exec(
		`INSERT INTO agent_ticket_specs
			(ticket_id, version, title, body, acceptance_criteria, links, submitted_by)
		 VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		ticketID, version, spec.Title, spec.Body, criteria, links, spec.SubmittedBy)
	if err != nil {
		return 0, err
	}

	return version, tx.Commit()
}

func (f *agentTicketsFactory) LatestSpec(ticketID int) (*tickets.Spec, bool, error) {
	var s tickets.Spec
	var criteria, links []byte
	err := f.conn.QueryRow(
		`SELECT id, ticket_id, version, title, body, acceptance_criteria, links, submitted_by,
			EXTRACT(EPOCH FROM created_at)::bigint
		 FROM agent_ticket_specs
		 WHERE ticket_id = $1
		 ORDER BY version DESC
		 LIMIT 1`, ticketID).
		Scan(&s.ID, &s.TicketID, &s.Version, &s.Title, &s.Body, &criteria, &links,
			&s.SubmittedBy, &s.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if err := json.Unmarshal(criteria, &s.AcceptanceCriteria); err != nil {
		return nil, false, err
	}
	if err := json.Unmarshal(links, &s.Links); err != nil {
		return nil, false, err
	}
	return &s, true, nil
}

// SubmitPlan replaces the active plan: new plan_version, orderings 1..N
// as given (contracts §3.2 submit_plan). Old versions are retained for
// process intelligence (§1.7).
func (f *agentTicketsFactory) SubmitPlan(ticketID int, ts []tickets.Task) (int, error) {
	tx, err := f.conn.Begin()
	if err != nil {
		return 0, err
	}
	defer Rollback(tx)

	if err := lockTicket(tx, ticketID); err != nil {
		return 0, err
	}

	var planVersion int
	err = tx.QueryRow(
		`SELECT COALESCE(MAX(plan_version), 0) + 1 FROM agent_ticket_tasks WHERE ticket_id = $1`,
		ticketID).Scan(&planVersion)
	if err != nil {
		return 0, err
	}

	for i, task := range ts {
		status := task.Status
		if status == "" {
			status = tickets.TaskPending
		}
		_, err = tx.Exec(
			`INSERT INTO agent_ticket_tasks
				(ticket_id, plan_version, ordering, title, detail, status)
			 VALUES ($1, $2, $3, $4, $5, $6)`,
			ticketID, planVersion, i+1, task.Title, task.Detail, string(status))
		if err != nil {
			return 0, err
		}
	}

	return planVersion, tx.Commit()
}

func (f *agentTicketsFactory) ActivePlan(ticketID int) ([]tickets.Task, error) {
	rows, err := f.conn.Query(
		`SELECT id, ticket_id, plan_version, ordering, title, detail, status,
			EXTRACT(EPOCH FROM updated_at)::bigint
		 FROM agent_ticket_tasks
		 WHERE ticket_id = $1
		   AND plan_version = (SELECT COALESCE(MAX(plan_version), 0)
		                       FROM agent_ticket_tasks WHERE ticket_id = $1)
		 ORDER BY ordering ASC`, ticketID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []tickets.Task{}
	for rows.Next() {
		var t tickets.Task
		if err := rows.Scan(&t.ID, &t.TicketID, &t.PlanVersion, &t.Ordering,
			&t.Title, &t.Detail, &t.Status, &t.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func (f *agentTicketsFactory) UpdateTaskStatus(ticketID int, planVersion, ordering int, status tickets.TaskStatus) error {
	res, err := f.conn.Exec(
		`UPDATE agent_ticket_tasks SET status = $1, updated_at = now()
		 WHERE ticket_id = $2 AND plan_version = $3 AND ordering = $4`,
		string(status), ticketID, planVersion, ordering)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return tickets.ErrTaskNotFound
	}
	return nil
}

// AppendTaskNote appends the §3.2 update_task_status note as a markdown
// blockquote on the task's detail (ticket-core contract addendum).
func (f *agentTicketsFactory) AppendTaskNote(ticketID int, planVersion, ordering int, note string) error {
	res, err := f.conn.Exec(
		`UPDATE agent_ticket_tasks
		 SET detail = CASE WHEN detail = '' THEN '> ' || $1
		                   ELSE detail || E'\n\n> ' || $1 END,
		     updated_at = now()
		 WHERE ticket_id = $2 AND plan_version = $3 AND ordering = $4`,
		note, ticketID, planVersion, ordering)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return tickets.ErrTaskNotFound
	}
	return nil
}
```

- [ ] Run to verify pass: `ginkgo --focus="AgentTicketsFactory" ./atc/db/` — expect all specs green.

- [ ] Generate the counterfeiter fake consumers (dispatch, harvest, delivery-outcomes) will use: `cd atc/db && go run github.com/maxbrunsfeld/counterfeiter/v6 -o dbfakes/fake_agent_tickets_factory.go . AgentTicketsFactory && cd ../..` — then verify `go build ./atc/db/...`.

- [ ] Commit: `git add atc/db && git commit -m "feat(ticket-core): spec/plan/task persistence + FakeAgentTicketsFactory"`

---

### Task 7: HTTP handler — all eight ticket routes

`agent/api/tickets/handler.go`, patterned on `agent/api/reviews/handler.go`. Identity resolution: principal writes via `principals.FromContext` (agent-identity's frozen context helper); human writes via an injected `UserNameFunc` — the handler must NOT import `atc/api/accessor` because accessor imports `atc/db` (`atc/api/accessor/accessor.go:9`) which imports this package via the factory: a cycle. The ATC wiring injects `accessor.GetAccessor(r).Claims().UserName` in Task 8.

**Files:**
- Create: `agent/api/tickets/handler.go`
- Test: `agent/api/tickets/handler_test.go`

**Steps:**

- [ ] Write the failing handler tests `agent/api/tickets/handler_test.go`:

```go
package tickets_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/concourse/concourse/agent/api/principals"
	"github.com/concourse/concourse/agent/api/tickets"
)

func newTestHandler(username string) (*tickets.Handler, *tickets.MemoryStore) {
	store := tickets.NewMemoryStore()
	h := tickets.NewHandler(store, func(*http.Request) string { return username })
	return h, store
}

func asPrincipal(r *http.Request, name string) *http.Request {
	return r.WithContext(principals.NewContext(r.Context(), principals.Principal{ID: 3, Name: name}))
}

func withParams(r *http.Request, params url.Values) *http.Request {
	r.Form = params
	return r
}

func TestCreateTicketAsHuman(t *testing.T) {
	h, store := newTestHandler("tdm")
	req := httptest.NewRequest("POST", "/api/v1/agent/tickets",
		strings.NewReader(`{"title":"fix X","repo":"tdmtrader/concourse","origin":"fly","budget_usd":5}`))
	rec := httptest.NewRecorder()
	h.CreateTicket(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("code = %d body %s, want 201", rec.Code, rec.Body)
	}
	var created tickets.Ticket
	json.Unmarshal(rec.Body.Bytes(), &created)
	if created.ID != 1 || created.State != tickets.StateDraft ||
		created.UserName != "tdm" || created.CreatedBy != "tdm" {
		t.Errorf("created = %+v", created)
	}
	if got, _, _ := store.Get(1); got.BudgetUSD == nil || *got.BudgetUSD != 5 {
		t.Errorf("budget not stored: %+v", got)
	}
}

func TestCreateTicketValidation(t *testing.T) {
	h, _ := newTestHandler("tdm")
	for body, want := range map[string]int{
		`{"repo":"r"}`:                       http.StatusBadRequest, // no title
		`{"title":"t"}`:                      http.StatusBadRequest, // no repo
		`{"title":"t","repo":"r","origin":"email"}`: http.StatusBadRequest,
		`not json`:                           http.StatusBadRequest,
	} {
		req := httptest.NewRequest("POST", "/api/v1/agent/tickets", strings.NewReader(body))
		rec := httptest.NewRecorder()
		h.CreateTicket(rec, req)
		if rec.Code != want {
			t.Errorf("body %q: code = %d, want %d", body, rec.Code, want)
		}
	}
}

func TestCreateTicketOriginRules(t *testing.T) {
	h, _ := newTestHandler("tdm")

	// human + retrospective -> 403
	req := httptest.NewRequest("POST", "/api/v1/agent/tickets",
		strings.NewReader(`{"title":"t","repo":"r","origin":"retrospective"}`))
	rec := httptest.NewRecorder()
	h.CreateTicket(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Errorf("human retrospective = %d, want 403", rec.Code)
	}

	// jira -> 400 until phase 2
	req = httptest.NewRequest("POST", "/api/v1/agent/tickets",
		strings.NewReader(`{"title":"t","repo":"r","origin":"jira"}`))
	rec = httptest.NewRecorder()
	h.CreateTicket(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("jira = %d, want 400", rec.Code)
	}

	// principal + retrospective -> 201 attributed to the principal, no triggering user
	req = asPrincipal(httptest.NewRequest("POST", "/api/v1/agent/tickets",
		strings.NewReader(`{"title":"add lint rule","repo":"r","origin":"retrospective"}`)), "retro-agent")
	rec = httptest.NewRecorder()
	h.CreateTicket(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("principal retrospective = %d body %s, want 201", rec.Code, rec.Body)
	}
	var created tickets.Ticket
	json.Unmarshal(rec.Body.Bytes(), &created)
	if created.CreatedBy != "retro-agent" || created.UserName != "" {
		t.Errorf("attribution = %+v", created)
	}

	// principal + web -> 403
	req = asPrincipal(httptest.NewRequest("POST", "/api/v1/agent/tickets",
		strings.NewReader(`{"title":"t","repo":"r","origin":"web"}`)), "retro-agent")
	rec = httptest.NewRecorder()
	h.CreateTicket(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Errorf("principal web = %d, want 403", rec.Code)
	}
}

func TestListTickets(t *testing.T) {
	h, store := newTestHandler("tdm")
	store.Create(&tickets.Ticket{Title: "a", Repo: "r1", Origin: "web"})
	store.Create(&tickets.Ticket{Title: "b", Repo: "r2", Origin: "fly"})

	req := httptest.NewRequest("GET", "/api/v1/agent/tickets?repo=r2", nil)
	rec := httptest.NewRecorder()
	h.ListTickets(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d", rec.Code)
	}
	var list []tickets.Ticket
	json.Unmarshal(rec.Body.Bytes(), &list)
	if len(list) != 1 || list[0].Title != "b" {
		t.Errorf("list = %+v", list)
	}

	req = httptest.NewRequest("GET", "/api/v1/agent/tickets?state=bogus", nil)
	rec = httptest.NewRecorder()
	h.ListTickets(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("bogus state filter = %d, want 400", rec.Code)
	}
}

func TestGetTicketDetail(t *testing.T) {
	h, store := newTestHandler("tdm")
	id, _ := store.Create(&tickets.Ticket{Title: "t", Repo: "r"})

	get := func() (int, tickets.TicketDetail) {
		req := withParams(httptest.NewRequest("GET", "/api/v1/agent/tickets/1", nil),
			url.Values{":ticket_id": {"1"}})
		rec := httptest.NewRecorder()
		h.GetTicket(rec, req)
		var detail tickets.TicketDetail
		json.Unmarshal(rec.Body.Bytes(), &detail)
		return rec.Code, detail
	}

	code, detail := get()
	if code != http.StatusOK || detail.Spec != nil || len(detail.Tasks) != 0 {
		t.Fatalf("empty detail = %d %+v", code, detail)
	}

	store.SubmitSpec(id, tickets.Spec{Title: "s", Body: "b"})
	store.SubmitPlan(id, []tickets.Task{{Title: "one"}})
	code, detail = get()
	if code != http.StatusOK || detail.Spec == nil || detail.Spec.Version != 1 || len(detail.Tasks) != 1 {
		t.Fatalf("filled detail = %d %+v", code, detail)
	}

	req := withParams(httptest.NewRequest("GET", "/api/v1/agent/tickets/99", nil),
		url.Values{":ticket_id": {"99"}})
	rec := httptest.NewRecorder()
	h.GetTicket(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Errorf("missing ticket = %d, want 404", rec.Code)
	}
}

func TestUpdateTicket(t *testing.T) {
	h, store := newTestHandler("tdm")
	store.Create(&tickets.Ticket{Title: "t", Repo: "r"})

	req := withParams(httptest.NewRequest("PUT", "/api/v1/agent/tickets/1",
		strings.NewReader(`{"title":"t2","budget_usd":7.5}`)), url.Values{":ticket_id": {"1"}})
	rec := httptest.NewRecorder()
	h.UpdateTicket(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d body %s", rec.Code, rec.Body)
	}
	got, _, _ := store.Get(1)
	if got.Title != "t2" || got.BudgetUSD == nil || *got.BudgetUSD != 7.5 {
		t.Errorf("update = %+v", got)
	}

	req = withParams(httptest.NewRequest("PUT", "/api/v1/agent/tickets/1",
		strings.NewReader(`{}`)), url.Values{":ticket_id": {"1"}})
	rec = httptest.NewRecorder()
	h.UpdateTicket(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("empty update = %d, want 400", rec.Code)
	}
}

func TestTransitionTicket(t *testing.T) {
	h, store := newTestHandler("tdm")
	store.Create(&tickets.Ticket{Title: "t", Repo: "r"})

	transition := func(body string) *httptest.ResponseRecorder {
		req := withParams(httptest.NewRequest("PUT", "/api/v1/agent/tickets/1/state",
			strings.NewReader(body)), url.Values{":ticket_id": {"1"}})
		rec := httptest.NewRecorder()
		h.TransitionTicket(rec, req)
		return rec
	}

	if rec := transition(`{"from":"draft","to":"queued"}`); rec.Code != http.StatusOK {
		t.Fatalf("draft->queued = %d body %s", rec.Code, rec.Body)
	}
	if rec := transition(`{"from":"draft","to":"queued"}`); rec.Code != http.StatusConflict {
		t.Errorf("stale = %d, want 409", rec.Code)
	}
	if rec := transition(`{"from":"queued","to":"merged"}`); rec.Code != http.StatusConflict {
		t.Errorf("illegal = %d, want 409", rec.Code)
	}
	if rec := transition(`{"from":"queued","to":"open"}`); rec.Code != http.StatusBadRequest {
		t.Errorf("bogus state = %d, want 400", rec.Code)
	}
	if got, _, _ := store.Get(1); got.State != tickets.StateQueued {
		t.Errorf("state = %s, want queued", got.State)
	}
}

func TestSpecPlanTaskRoutes(t *testing.T) {
	h, store := newTestHandler("")
	store.Create(&tickets.Ticket{Title: "t", Repo: "r"})

	req := asPrincipal(withParams(httptest.NewRequest("POST", "/api/v1/agent/tickets/1/spec",
		strings.NewReader(`{"title":"spec","body":"prose","acceptance_criteria":["a"],"links":[{"title":"l","url":"u"}]}`)),
		url.Values{":ticket_id": {"1"}}), "run-42-platform")
	rec := httptest.NewRecorder()
	h.SubmitSpec(rec, req)
	if rec.Code != http.StatusCreated || !strings.Contains(rec.Body.String(), `"version":1`) {
		t.Fatalf("spec = %d %s", rec.Code, rec.Body)
	}
	spec, _, _ := store.LatestSpec(1)
	if spec.SubmittedBy != "run-42-platform" {
		t.Errorf("spec attribution = %+v", spec)
	}

	// missing body -> 400
	req = withParams(httptest.NewRequest("POST", "/api/v1/agent/tickets/1/spec",
		strings.NewReader(`{"title":"only"}`)), url.Values{":ticket_id": {"1"}})
	rec = httptest.NewRecorder()
	h.SubmitSpec(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("spec without body = %d, want 400", rec.Code)
	}

	req = asPrincipal(withParams(httptest.NewRequest("POST", "/api/v1/agent/tickets/1/plan",
		strings.NewReader(`{"tasks":[{"title":"one"},{"title":"two","detail":"d"}]}`)),
		url.Values{":ticket_id": {"1"}}), "run-42-platform")
	rec = httptest.NewRecorder()
	h.SubmitPlan(rec, req)
	if rec.Code != http.StatusCreated || !strings.Contains(rec.Body.String(), `"plan_version":1`) {
		t.Fatalf("plan = %d %s", rec.Code, rec.Body)
	}

	// empty plan -> 400
	req = withParams(httptest.NewRequest("POST", "/api/v1/agent/tickets/1/plan",
		strings.NewReader(`{"tasks":[]}`)), url.Values{":ticket_id": {"1"}})
	rec = httptest.NewRecorder()
	h.SubmitPlan(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("empty plan = %d, want 400", rec.Code)
	}

	req = asPrincipal(withParams(httptest.NewRequest("PUT", "/api/v1/agent/tickets/1/tasks/2",
		strings.NewReader(`{"status":"done","note":"trivial"}`)),
		url.Values{":ticket_id": {"1"}, ":ordering": {"2"}}), "run-42-platform")
	rec = httptest.NewRecorder()
	h.UpdateTask(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("task update = %d %s", rec.Code, rec.Body)
	}
	tasks, _ := store.ActivePlan(1)
	if tasks[1].Status != tickets.TaskDone || tasks[1].Detail != "d\n\n> trivial" {
		t.Errorf("task after update = %+v", tasks[1])
	}

	// bad status -> 400
	req = withParams(httptest.NewRequest("PUT", "/api/v1/agent/tickets/1/tasks/2",
		strings.NewReader(`{"status":"started"}`)),
		url.Values{":ticket_id": {"1"}, ":ordering": {"2"}})
	rec = httptest.NewRecorder()
	h.UpdateTask(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("bad status = %d, want 400", rec.Code)
	}

	// ordering beyond the plan -> 404
	req = withParams(httptest.NewRequest("PUT", "/api/v1/agent/tickets/1/tasks/9",
		strings.NewReader(`{"status":"done"}`)),
		url.Values{":ticket_id": {"1"}, ":ordering": {"9"}})
	rec = httptest.NewRecorder()
	h.UpdateTask(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Errorf("missing task = %d, want 404", rec.Code)
	}
}
```

- [ ] Run to verify it fails: `go test ./agent/api/tickets/` — expected failure: `undefined: tickets.NewHandler`.

- [ ] Write `agent/api/tickets/handler.go`:

```go
package tickets

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"

	"github.com/concourse/concourse/agent/api/principals"
)

// UserNameFunc resolves the authenticated human username for a request
// ("" when anonymous). Injected because this package must not import
// atc/api/accessor (accessor imports atc/db, which imports this
// package via AgentTicketsFactory — a cycle). atc/api/handler.go wires
// accessor.GetAccessor(r).Claims().UserName.
type UserNameFunc func(r *http.Request) string

// Handler serves the eight /api/v1/agent/tickets* routes. Auth is
// enforced by the wrappa tiers (00-shared-contracts.md §4.2); this
// handler only reads WHO the verified writer is.
type Handler struct {
	store    Store
	userName UserNameFunc
}

func NewHandler(store Store, userName UserNameFunc) *Handler {
	return &Handler{store: store, userName: userName}
}

// writer returns (name, isPrincipal): the verified agent principal's
// name when the principal(<scope>) tier authenticated the request
// (audit-attribution convention), else the human username.
func (h *Handler) writer(r *http.Request) (string, bool) {
	if p, ok := principals.FromContext(r.Context()); ok {
		return p.Name, true
	}
	return h.userName(r), false
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

func readJSON(w http.ResponseWriter, r *http.Request, into any) bool {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
	if err != nil {
		http.Error(w, "failed to read request body", http.StatusBadRequest)
		return false
	}
	if err := json.Unmarshal(body, into); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return false
	}
	return true
}

func ticketIDParam(w http.ResponseWriter, r *http.Request) (int, bool) {
	id, err := strconv.Atoi(r.FormValue(":ticket_id"))
	if err != nil || id <= 0 {
		http.Error(w, "invalid ticket_id", http.StatusBadRequest)
		return 0, false
	}
	return id, true
}

// CreateTicket handles POST /api/v1/agent/tickets.
//
// Origin rules (contract addendum): principal-authenticated writes may
// ONLY create origin 'retrospective'; human writes may create 'web' or
// 'fly'; 'jira' is rejected until the phase-2 sync component exists
// (docs/superpowers/plans/agentic-platform/ticket-jira-sync-phase2.md).
func (h *Handler) CreateTicket(w http.ResponseWriter, r *http.Request) {
	var req CreateRequest
	if !readJSON(w, r, &req) {
		return
	}
	if req.Title == "" {
		http.Error(w, "title is required", http.StatusBadRequest)
		return
	}
	if req.Repo == "" {
		http.Error(w, "repo is required", http.StatusBadRequest)
		return
	}
	origin := req.Origin
	if origin == "" {
		origin = "web"
	}
	if !ValidOrigin(origin) {
		http.Error(w, "invalid origin", http.StatusBadRequest)
		return
	}
	if origin == "jira" {
		http.Error(w, "origin 'jira' arrives with the phase-2 sync component", http.StatusBadRequest)
		return
	}

	name, isPrincipal := h.writer(r)
	if isPrincipal && origin != "retrospective" {
		http.Error(w, "agent principals may only create retrospective tickets", http.StatusForbidden)
		return
	}
	if !isPrincipal && origin == "retrospective" {
		http.Error(w, "retrospective tickets are created by agent principals", http.StatusForbidden)
		return
	}

	t := &Ticket{
		Title:           req.Title,
		Body:            req.Body,
		Origin:          origin,
		Repo:            req.Repo,
		TargetBranch:    req.TargetBranch,
		WorkflowName:    req.WorkflowName,
		WorkflowVersion: req.WorkflowVersion,
		BudgetUSD:       req.BudgetUSD,
		CreatedBy:       name,
		ExternalRef:     req.ExternalRef,
	}
	if !isPrincipal {
		// triggering user: credential attachment + cost attribution
		// (dispatch resolves user_id from users.username in wave 4)
		t.UserName = name
	}

	id, err := h.store.Create(t)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	created, _, err := h.store.Get(id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusCreated, created)
}

// ListTickets handles GET /api/v1/agent/tickets (?state=&repo=&origin=&limit=).
func (h *Handler) ListTickets(w http.ResponseWriter, r *http.Request) {
	filter := ListFilter{Limit: 100}
	if s := r.URL.Query().Get("state"); s != "" {
		if !ValidState(State(s)) {
			http.Error(w, "invalid state filter", http.StatusBadRequest)
			return
		}
		filter.State = State(s)
	}
	filter.Repo = r.URL.Query().Get("repo")
	if o := r.URL.Query().Get("origin"); o != "" {
		if !ValidOrigin(o) {
			http.Error(w, "invalid origin filter", http.StatusBadRequest)
			return
		}
		filter.Origin = o
	}
	if l, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && l > 0 && l <= 500 {
		filter.Limit = l
	}

	list, err := h.store.List(filter)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

// GetTicket handles GET /api/v1/agent/tickets/:ticket_id. The response
// is TicketDetail — the exact payload platform-mcp's read_ticket
// returns in wave 3 (contract addendum).
func (h *Handler) GetTicket(w http.ResponseWriter, r *http.Request) {
	id, ok := ticketIDParam(w, r)
	if !ok {
		return
	}
	t, found, err := h.store.Get(id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if !found {
		http.Error(w, "ticket not found", http.StatusNotFound)
		return
	}
	spec, _, err := h.store.LatestSpec(id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	tasks, err := h.store.ActivePlan(id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, TicketDetail{Ticket: *t, Spec: spec, Tasks: tasks})
}

// UpdateTicket handles PUT /api/v1/agent/tickets/:ticket_id —
// title/body/budget/workflow ref/target branch. NEVER state.
func (h *Handler) UpdateTicket(w http.ResponseWriter, r *http.Request) {
	id, ok := ticketIDParam(w, r)
	if !ok {
		return
	}
	var req UpdateRequest
	if !readJSON(w, r, &req) {
		return
	}
	if req.Title == nil && req.Body == nil && req.BudgetUSD == nil &&
		req.WorkflowName == nil && req.WorkflowVersion == nil && req.TargetBranch == nil {
		http.Error(w, "no fields to update", http.StatusBadRequest)
		return
	}
	err := h.store.Update(id, Update{
		Title: req.Title, Body: req.Body, BudgetUSD: req.BudgetUSD,
		WorkflowName: req.WorkflowName, WorkflowVersion: req.WorkflowVersion,
		TargetBranch: req.TargetBranch,
	})
	if errors.Is(err, ErrTicketNotFound) {
		http.Error(w, "ticket not found", http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	updated, _, err := h.store.Get(id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, updated)
}

// TransitionTicket handles PUT /api/v1/agent/tickets/:ticket_id/state —
// the ONLY HTTP path that changes ticket state, delegating to the
// single-writer Store.Transition. 409 on illegal/stale transitions.
func (h *Handler) TransitionTicket(w http.ResponseWriter, r *http.Request) {
	id, ok := ticketIDParam(w, r)
	if !ok {
		return
	}
	var req TransitionRequest
	if !readJSON(w, r, &req) {
		return
	}
	if !ValidState(req.From) || !ValidState(req.To) {
		http.Error(w, "invalid state", http.StatusBadRequest)
		return
	}
	err := h.store.Transition(id, req.From, req.To, TransitionMeta{
		PipelineRunID: req.PipelineRunID,
		Branch:        req.Branch,
		ErrorDetail:   req.ErrorDetail,
	})
	switch {
	case errors.Is(err, ErrTicketNotFound):
		http.Error(w, "ticket not found", http.StatusNotFound)
		return
	case errors.Is(err, ErrInvalidTransition), errors.Is(err, ErrStaleTransition):
		http.Error(w, err.Error(), http.StatusConflict)
		return
	case err != nil:
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	updated, _, err := h.store.Get(id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, updated)
}

// SubmitSpec handles POST /api/v1/agent/tickets/:ticket_id/spec
// (body = §3.2 submit_spec tool input).
func (h *Handler) SubmitSpec(w http.ResponseWriter, r *http.Request) {
	id, ok := ticketIDParam(w, r)
	if !ok {
		return
	}
	var req SpecSubmission
	if !readJSON(w, r, &req) {
		return
	}
	if req.Title == "" || req.Body == "" {
		http.Error(w, "title and body are required", http.StatusBadRequest)
		return
	}
	name, _ := h.writer(r)
	version, err := h.store.SubmitSpec(id, Spec{
		Title:              req.Title,
		Body:               req.Body,
		AcceptanceCriteria: req.AcceptanceCriteria,
		Links:              req.Links,
		SubmittedBy:        name,
	})
	if errors.Is(err, ErrTicketNotFound) {
		http.Error(w, "ticket not found", http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]int{"version": version})
}

// SubmitPlan handles POST /api/v1/agent/tickets/:ticket_id/plan
// (body = §3.2 submit_plan tool input). Replaces the active plan by
// bumping plan_version; orderings are 1..N as given.
func (h *Handler) SubmitPlan(w http.ResponseWriter, r *http.Request) {
	id, ok := ticketIDParam(w, r)
	if !ok {
		return
	}
	var req PlanSubmission
	if !readJSON(w, r, &req) {
		return
	}
	if len(req.Tasks) == 0 {
		http.Error(w, "tasks must contain at least one task", http.StatusBadRequest)
		return
	}
	ts := make([]Task, len(req.Tasks))
	for i, pt := range req.Tasks {
		if pt.Title == "" {
			http.Error(w, "every task needs a title", http.StatusBadRequest)
			return
		}
		ts[i] = Task{Title: pt.Title, Detail: pt.Detail}
	}
	planVersion, err := h.store.SubmitPlan(id, ts)
	if errors.Is(err, ErrTicketNotFound) {
		http.Error(w, "ticket not found", http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]int{"plan_version": planVersion})
}

// UpdateTask handles PUT /api/v1/agent/tickets/:ticket_id/tasks/:ordering
// (body = §3.2 update_task_status tool input). Operates on the ACTIVE
// plan; a non-empty note is appended to the task's detail as a
// blockquote (contract addendum).
func (h *Handler) UpdateTask(w http.ResponseWriter, r *http.Request) {
	id, ok := ticketIDParam(w, r)
	if !ok {
		return
	}
	ordering, err := strconv.Atoi(r.FormValue(":ordering"))
	if err != nil || ordering <= 0 {
		http.Error(w, "invalid ordering", http.StatusBadRequest)
		return
	}
	var req TaskStatusRequest
	if !readJSON(w, r, &req) {
		return
	}
	if !ValidTaskStatus(req.Status) {
		http.Error(w, "invalid status", http.StatusBadRequest)
		return
	}
	active, err := h.store.ActivePlan(id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if len(active) == 0 {
		http.Error(w, ErrNoActivePlan.Error(), http.StatusNotFound)
		return
	}
	planVersion := active[0].PlanVersion
	err = h.store.UpdateTaskStatus(id, planVersion, ordering, req.Status)
	if errors.Is(err, ErrTaskNotFound) {
		http.Error(w, "plan task not found", http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if req.Note != "" {
		if err := h.store.AppendTaskNote(id, planVersion, ordering, req.Note); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
```

- [ ] Run to verify pass: `go test ./agent/api/tickets/` — expect all handler tests green.

- [ ] Commit: `git add agent/api/tickets && git commit -m "feat(ticket-core): HTTP handler for the eight agent ticket routes"`

---

### Task 8: Route registration, auth tiers, ATC wiring

Registers the eight routes (contracts §4.2 rows exactly), lands the combined-tier composition helper `auth.AgentPrincipalOrMainTeamHandler`, wires the wrappa switch, `DefaultRoles`, `atc/api/handler.go`, and `atc/atccmd/command.go`, and flips the route-audit rows to live. The wrappa test (`atc/wrappa/api_auth_wrappa_test.go:36` "handles each route") iterates every `atc.Routes` entry and panics on a missing case — that panic is this task's failing test.

**Convention:** route-adding task — follow [CONVENTIONS.md §C1](CONVENTIONS.md): all SIX touchpoints (including `atc/wrappa/reject_archived_wrappa.go` and `atc/auditor/auditor.go` `ValidateAction`, not listed below) in the same commit; verify with `go test ./atc/wrappa/... ./atc/auditor/...`.

**Files:**
- Modify: `atc/routes.go` (route-name consts after `GetAgentReviewFindings` block at :121-129; route entries after the agent reviews block at :254-262)
- Create: `atc/api/auth/agent_principal_or_main_team_handler.go`
- Test: `atc/api/auth/agent_principal_or_main_team_handler_test.go` (Ginkgo; the atc/api/auth suite exists post agent-identity)
- Modify: `atc/wrappa/api_auth_wrappa.go` (the exhaustive switch: authorized team-less case group + new cases; pre-wave-1 anchor :169-177 — post wave 1 the file has agent-identity's `CheckAgentAuthorizationHandler` case group and the `checkAgentPrincipalHandlerFactory` field)
- Modify: `atc/api/accessor/roles.go:114` (after `atc.GetBuildAgentReviews: ViewerRole`)
- Modify: `atc/api/handler.go` (param list after `reviewsStore reviewsapi.Store` at :91; server construction after `reviewsServer` at :123-139; handlers map after `atc.ListTeamAgentReviews` at :277)
- Modify: `atc/api/api_suite_test.go` (NewHandler args after `reviews.NewMemoryStore()` at :226)
- Modify: `atc/atccmd/command.go` (NewHandler args after `db.NewAgentReviewsFactory(dbConn)` at :2298)
- Modify: `docs/superpowers/plans/agentic-platform/agent-route-scopes.md` (flip the eight ticket rows from "planned (wave 2)" to "live")
- Test: `agent/api/tickets/route_registration_test.go`

**Steps:**

- [ ] Write the failing route-registration test `agent/api/tickets/route_registration_test.go` (precedent: `agent/api/feedback/route_registration_test.go`):

```go
package tickets_test

import (
	"testing"

	"github.com/concourse/concourse/atc"
)

// TestTicketRoutesRegistered verifies the eight ticket-core routes are
// in the main ATC route table with the §4.2 methods and paths.
func TestTicketRoutesRegistered(t *testing.T) {
	required := []struct {
		name   string
		method string
		path   string
	}{
		{atc.ListAgentTickets, "GET", "/api/v1/agent/tickets"},
		{atc.CreateAgentTicket, "POST", "/api/v1/agent/tickets"},
		{atc.GetAgentTicket, "GET", "/api/v1/agent/tickets/:ticket_id"},
		{atc.UpdateAgentTicket, "PUT", "/api/v1/agent/tickets/:ticket_id"},
		{atc.TransitionAgentTicket, "PUT", "/api/v1/agent/tickets/:ticket_id/state"},
		{atc.SubmitAgentTicketSpec, "POST", "/api/v1/agent/tickets/:ticket_id/spec"},
		{atc.SubmitAgentTicketPlan, "POST", "/api/v1/agent/tickets/:ticket_id/plan"},
		{atc.UpdateAgentTicketTask, "PUT", "/api/v1/agent/tickets/:ticket_id/tasks/:ordering"},
	}

	for _, rr := range required {
		found := false
		for _, route := range atc.Routes {
			if route.Name == rr.name {
				found = true
				if route.Method != rr.method {
					t.Errorf("route %q: method = %s, want %s", rr.name, route.Method, rr.method)
				}
				if route.Path != rr.path {
					t.Errorf("route %q: path = %s, want %s", rr.name, route.Path, rr.path)
				}
			}
		}
		if !found {
			t.Errorf("route %q (%s %s) not registered in atc.Routes", rr.name, rr.method, rr.path)
		}
	}
}
```

- [ ] Run to verify it fails: `go test ./agent/api/tickets/ -run TestTicketRoutesRegistered` — expected failure: compile error `undefined: atc.ListAgentTickets`.

- [ ] Add the route-name constants to `atc/routes.go` after the `SubmitAgentReview`/`GetBuildAgentReviews`/`ListTeamAgentReviews` const block (:127-129):

```go
	ListAgentTickets      = "ListAgentTickets"
	CreateAgentTicket     = "CreateAgentTicket"
	GetAgentTicket        = "GetAgentTicket"
	UpdateAgentTicket     = "UpdateAgentTicket"
	TransitionAgentTicket = "TransitionAgentTicket"
	SubmitAgentTicketSpec = "SubmitAgentTicketSpec"
	SubmitAgentTicketPlan = "SubmitAgentTicketPlan"
	UpdateAgentTicketTask = "UpdateAgentTicketTask"
```

and the route entries to `atc.Routes` after the `{Path: "/api/v1/agent/reviews", Method: "POST", Name: SubmitAgentReview}` entry (:260):

```go
	{Path: "/api/v1/agent/tickets", Method: "GET", Name: ListAgentTickets},
	{Path: "/api/v1/agent/tickets", Method: "POST", Name: CreateAgentTicket},
	{Path: "/api/v1/agent/tickets/:ticket_id", Method: "GET", Name: GetAgentTicket},
	{Path: "/api/v1/agent/tickets/:ticket_id", Method: "PUT", Name: UpdateAgentTicket},
	{Path: "/api/v1/agent/tickets/:ticket_id/state", Method: "PUT", Name: TransitionAgentTicket},
	{Path: "/api/v1/agent/tickets/:ticket_id/spec", Method: "POST", Name: SubmitAgentTicketSpec},
	{Path: "/api/v1/agent/tickets/:ticket_id/plan", Method: "POST", Name: SubmitAgentTicketPlan},
	{Path: "/api/v1/agent/tickets/:ticket_id/tasks/:ordering", Method: "PUT", Name: UpdateAgentTicketTask},
```

- [ ] Run: `go test ./agent/api/tickets/ -run TestTicketRoutesRegistered` — expect PASS. Then run `ginkgo ./atc/wrappa/` — expected failure: panic `you missed a spot: "ListAgentTickets"` (the exhaustive-switch guard at `atc/wrappa/api_auth_wrappa.go:181`).

- [ ] Write the failing composition-helper spec `atc/api/auth/agent_principal_or_main_team_handler_test.go`:

```go
package auth_test

import (
	"net/http"
	"net/http/httptest"

	"github.com/concourse/concourse/atc/api/auth"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("AgentPrincipalOrMainTeamHandler", func() {
	var principalCalled, teamCalled bool
	var handler http.Handler

	BeforeEach(func() {
		principalCalled, teamCalled = false, false
		handler = auth.AgentPrincipalOrMainTeamHandler(
			http.HandlerFunc(func(http.ResponseWriter, *http.Request) { principalCalled = true }),
			http.HandlerFunc(func(http.ResponseWriter, *http.Request) { teamCalled = true }),
		)
	})

	It("routes cap1 bearer tokens to the principal tier", func() {
		req := httptest.NewRequest("GET", "/api/v1/agent/tickets/1", nil)
		req.Header.Set("Authorization", "Bearer cap1.7.s3cret")
		handler.ServeHTTP(httptest.NewRecorder(), req)
		Expect(principalCalled).To(BeTrue())
		Expect(teamCalled).To(BeFalse())
	})

	It("routes user JWTs to the main-team tier", func() {
		req := httptest.NewRequest("GET", "/api/v1/agent/tickets/1", nil)
		req.Header.Set("Authorization", "Bearer eyJhbGciOi.something.jwt")
		handler.ServeHTTP(httptest.NewRecorder(), req)
		Expect(principalCalled).To(BeFalse())
		Expect(teamCalled).To(BeTrue())
	})

	It("routes tokenless requests to the main-team tier", func() {
		req := httptest.NewRequest("GET", "/api/v1/agent/tickets/1", nil)
		handler.ServeHTTP(httptest.NewRecorder(), req)
		Expect(principalCalled).To(BeFalse())
		Expect(teamCalled).To(BeTrue())
	})
})
```

- [ ] Run to verify it fails: `ginkgo ./atc/api/auth/` — expected failure: `undefined: auth.AgentPrincipalOrMainTeamHandler`.

- [ ] Write `atc/api/auth/agent_principal_or_main_team_handler.go`:

```go
package auth

import (
	"net/http"
	"strings"

	"github.com/concourse/concourse/agent/api/principals"
)

// AgentPrincipalOrMainTeamHandler implements the combined route tiers
// of 00-shared-contracts.md §4.2 ("authorized member/viewer (main);
// also principal(<scope>)"): requests bearing a cap1 principal token
// are authenticated by the principal tier
// (CheckAgentPrincipalHandlerFactory.HandlerFor); everything else —
// user JWTs, anonymous — falls through to main-team authorization
// (CheckAgentAuthorizationHandler). Owned by ticket-core; reused by
// platform-mcp-hitl for GetAgentQuestion/AnswerAgentQuestion in wave 3
// (ticket-core contract addendum).
func AgentPrincipalOrMainTeamHandler(principalTier, mainTeamTier http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bearer, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if strings.HasPrefix(bearer, principals.TokenVersionPrefix) {
			principalTier.ServeHTTP(w, r)
			return
		}
		mainTeamTier.ServeHTTP(w, r)
	})
}
```

- [ ] Run to verify pass: `ginkgo ./atc/api/auth/` — expect green.

- [ ] Wire the wrappa. In `atc/wrappa/api_auth_wrappa.go` (post wave 1 the struct has the `checkAgentPrincipalHandlerFactory auth.CheckAgentPrincipalHandlerFactory` field and the file imports `"github.com/concourse/concourse/agent/api/principals"`; if the import is missing, add it):
  1. Add `atc.ListAgentTickets` and `atc.UpdateAgentTicket` to agent-identity's team-less `CheckAgentAuthorizationHandler` case group (the group holding `atc.SubmitAgentFeedback` etc.).
  2. Add the new case groups before the `default:` panic:

```go
		// combined tier: agent principal (tickets:write) OR authorized
		// main-team member — 00-shared-contracts.md §4.2 + ticket-core addendum
		case atc.CreateAgentTicket,
			atc.TransitionAgentTicket,
			atc.SubmitAgentTicketSpec,
			atc.SubmitAgentTicketPlan:
			newHandler = auth.AgentPrincipalOrMainTeamHandler(
				wrappa.checkAgentPrincipalHandlerFactory.HandlerFor(handler, rejector, principals.ScopeTicketsWrite),
				auth.CheckAgentAuthorizationHandler(handler, rejector),
			)

		// combined tier: agent principal (tickets:read) OR authorized
		// main-team viewer
		case atc.GetAgentTicket:
			newHandler = auth.AgentPrincipalOrMainTeamHandler(
				wrappa.checkAgentPrincipalHandlerFactory.HandlerFor(handler, rejector, principals.ScopeTicketsRead),
				auth.CheckAgentAuthorizationHandler(handler, rejector),
			)

		// principal-only: the platform-mcp sidecar's task ticker
		case atc.UpdateAgentTicketTask:
			newHandler = wrappa.checkAgentPrincipalHandlerFactory.HandlerFor(handler, rejector, principals.ScopeTicketsWrite)
```

- [ ] Add the `DefaultRoles` entries in `atc/api/accessor/roles.go` after `atc.GetBuildAgentReviews: ViewerRole,` (:114) — every route that can reach `CheckAgentAuthorizationHandler` needs one (the file's comment at :102-107 warns missing entries silently become admin-only); `UpdateAgentTicketTask` is principal-only and deliberately absent:

```go
	atc.ListAgentTickets:      ViewerRole,
	atc.CreateAgentTicket:     MemberRole,
	atc.GetAgentTicket:        ViewerRole,
	atc.UpdateAgentTicket:     MemberRole,
	atc.TransitionAgentTicket: MemberRole,
	atc.SubmitAgentTicketSpec: MemberRole,
	atc.SubmitAgentTicketPlan: MemberRole,
```

- [ ] Run to verify: `ginkgo ./atc/wrappa/ && ginkgo ./atc/api/accessor/` — expect green (no more missed-spot panic; roles map complete).

- [ ] Wire the API handler. In `atc/api/handler.go`:
  1. Import `ticketsapi "github.com/concourse/concourse/agent/api/tickets"` and `"github.com/concourse/concourse/atc/api/accessor"`.
  2. Add the param after `reviewsStore reviewsapi.Store,` (:91): `ticketsStore ticketsapi.Store,`.
  3. Construct the server after `reviewsServer := ...` (:139):

```go
	ticketsServer := ticketsapi.NewHandler(ticketsStore, func(r *http.Request) string {
		return accessor.GetAccessor(r).Claims().UserName
	})
```

  4. Add the handlers-map entries after `atc.ListTeamAgentReviews: ...` (:277):

```go
		atc.ListAgentTickets:      http.HandlerFunc(ticketsServer.ListTickets),
		atc.CreateAgentTicket:     http.HandlerFunc(ticketsServer.CreateTicket),
		atc.GetAgentTicket:        http.HandlerFunc(ticketsServer.GetTicket),
		atc.UpdateAgentTicket:     http.HandlerFunc(ticketsServer.UpdateTicket),
		atc.TransitionAgentTicket: http.HandlerFunc(ticketsServer.TransitionTicket),
		atc.SubmitAgentTicketSpec: http.HandlerFunc(ticketsServer.SubmitSpec),
		atc.SubmitAgentTicketPlan: http.HandlerFunc(ticketsServer.SubmitPlan),
		atc.UpdateAgentTicketTask: http.HandlerFunc(ticketsServer.UpdateTask),
```

- [ ] Update the two NewHandler call sites:
  - `atc/api/api_suite_test.go` after `reviews.NewMemoryStore(),` (:226): add `tickets.NewMemoryStore(),` with import `"github.com/concourse/concourse/agent/api/tickets"`.
  - `atc/atccmd/command.go` after `db.NewAgentReviewsFactory(dbConn),` (:2298): add `db.NewAgentTicketsFactory(dbConn),`.

- [ ] Run to verify pass: `go build ./atc/... ./agent/... && ginkgo ./atc/wrappa/ && ginkgo -p ./atc/api/` — expect green (the api suite recompiles with the new param and its route-count assertions pick up the eight handlers).

- [ ] Flip the eight ticket-core rows in `docs/superpowers/plans/agentic-platform/agent-route-scopes.md` from `planned (wave 2)` to `live (ticket-core)` — the audit doc's own header requires route changes and audit updates in the same change.

- [ ] Commit: `git add atc/ agent/api/tickets/ docs/superpowers/plans/agentic-platform/agent-route-scopes.md && git commit -m "feat(ticket-core): register /api/v1/agent/tickets routes with principal-aware auth tiers"`

---

### Task 9: spec.md / plan.md render helper

Deterministic markdown renderers producing the read-only workspace inputs (spec §1: "Workspaces receive rendered spec.md/plan.md as read-only inputs"). Dispatch's renderer (wave 4) calls these when materializing run inputs; signatures frozen in the Task-1 addendum.

**Files:**
- Create: `agent/api/tickets/render.go`
- Test: `agent/api/tickets/render_test.go`

**Steps:**

- [ ] Write the failing golden tests `agent/api/tickets/render_test.go`:

```go
package tickets_test

import (
	"testing"

	"github.com/concourse/concourse/agent/api/tickets"
)

func TestRenderSpecMarkdown(t *testing.T) {
	version := 2
	ticket := tickets.Ticket{
		ID: 12, Title: "fix flaky spec", Body: "worker_cache_test flakes under load",
		Origin: "fly", Repo: "tdmtrader/concourse", TargetBranch: "jetbridge",
		WorkflowName: "standard-dev", WorkflowVersion: &version,
	}
	spec := &tickets.Spec{
		Version: 2, Title: "Deflake worker cache spec",
		Body:               "Root cause: refresh interval racing the clock.",
		AcceptanceCriteria: []string{"suite green 10x", "no timeout bumps"},
		Links:              []tickets.Link{{Title: "flake log", URL: "https://ci/build/9"}},
	}

	got := string(tickets.RenderSpecMarkdown(ticket, spec))
	want := `# Ticket #12: fix flaky spec

- repo: tdmtrader/concourse
- target branch: jetbridge
- origin: fly
- workflow: standard-dev v2

## Problem statement

worker_cache_test flakes under load

## Spec v2: Deflake worker cache spec

Root cause: refresh interval racing the clock.

### Acceptance criteria

- [ ] suite green 10x
- [ ] no timeout bumps

### Links

- [flake log](https://ci/build/9)
`
	if got != want {
		t.Errorf("RenderSpecMarkdown mismatch:\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}

	// specless ticket: envelope + problem statement only
	got = string(tickets.RenderSpecMarkdown(tickets.Ticket{
		ID: 3, Title: "t", Body: "b", Origin: "web", Repo: "r", TargetBranch: "main",
	}, nil))
	want = `# Ticket #3: t

- repo: r
- target branch: main
- origin: web

## Problem statement

b
`
	if got != want {
		t.Errorf("specless mismatch:\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}

func TestRenderPlanMarkdown(t *testing.T) {
	ticket := tickets.Ticket{ID: 12}
	got := string(tickets.RenderPlanMarkdown(ticket, []tickets.Task{
		{PlanVersion: 3, Ordering: 1, Title: "write failing test", Status: tickets.TaskDone},
		{PlanVersion: 3, Ordering: 2, Title: "fix the race", Status: tickets.TaskInProgress,
			Detail: "clock injection\nsecond line"},
		{PlanVersion: 3, Ordering: 3, Title: "run suite 10x", Status: tickets.TaskPending},
		{PlanVersion: 3, Ordering: 4, Title: "skipped idea", Status: tickets.TaskSkipped},
		{PlanVersion: 3, Ordering: 5, Title: "blocked on infra", Status: tickets.TaskBlocked},
	}))
	want := `# Plan v3 — ticket #12

- [x] 1. write failing test
- [~] 2. fix the race
  clock injection
  second line
- [ ] 3. run suite 10x
- [-] 4. skipped idea
- [!] 5. blocked on infra
`
	if got != want {
		t.Errorf("RenderPlanMarkdown mismatch:\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}

	got = string(tickets.RenderPlanMarkdown(ticket, nil))
	want = "# Plan — ticket #12\n\nNo plan submitted yet.\n"
	if got != want {
		t.Errorf("empty plan mismatch:\n--- got ---\n%q\n--- want ---\n%q", got, want)
	}
}
```

- [ ] Run to verify it fails: `go test ./agent/api/tickets/ -run TestRender` — expected failure: `undefined: tickets.RenderSpecMarkdown`.

- [ ] Write `agent/api/tickets/render.go`:

```go
package tickets

import (
	"fmt"
	"strings"
)

// RenderSpecMarkdown produces the read-only spec.md workspace input for
// a ticket. Dispatch's renderer (wave 4) materializes it into run
// inputs at render time. Deterministic: identical rows produce
// identical bytes (golden-tested).
func RenderSpecMarkdown(t Ticket, spec *Spec) []byte {
	var b strings.Builder
	fmt.Fprintf(&b, "# Ticket #%d: %s\n\n", t.ID, t.Title)
	fmt.Fprintf(&b, "- repo: %s\n", t.Repo)
	fmt.Fprintf(&b, "- target branch: %s\n", t.TargetBranch)
	fmt.Fprintf(&b, "- origin: %s\n", t.Origin)
	if t.WorkflowName != "" {
		if t.WorkflowVersion != nil {
			fmt.Fprintf(&b, "- workflow: %s v%d\n", t.WorkflowName, *t.WorkflowVersion)
		} else {
			fmt.Fprintf(&b, "- workflow: %s (live)\n", t.WorkflowName)
		}
	}
	b.WriteString("\n## Problem statement\n\n")
	b.WriteString(strings.TrimRight(t.Body, "\n"))
	b.WriteString("\n")

	if spec != nil {
		fmt.Fprintf(&b, "\n## Spec v%d: %s\n\n", spec.Version, spec.Title)
		b.WriteString(strings.TrimRight(spec.Body, "\n"))
		b.WriteString("\n")
		if len(spec.AcceptanceCriteria) > 0 {
			b.WriteString("\n### Acceptance criteria\n\n")
			for _, c := range spec.AcceptanceCriteria {
				fmt.Fprintf(&b, "- [ ] %s\n", c)
			}
		}
		if len(spec.Links) > 0 {
			b.WriteString("\n### Links\n\n")
			for _, l := range spec.Links {
				fmt.Fprintf(&b, "- [%s](%s)\n", l.Title, l.URL)
			}
		}
	}
	return []byte(b.String())
}

// taskGlyph maps a task status to its plan.md checkbox glyph (contract
// addendum): pending [ ], in_progress [~], done [x], skipped [-],
// blocked [!].
func taskGlyph(s TaskStatus) string {
	switch s {
	case TaskDone:
		return "[x]"
	case TaskInProgress:
		return "[~]"
	case TaskSkipped:
		return "[-]"
	case TaskBlocked:
		return "[!]"
	default:
		return "[ ]"
	}
}

// RenderPlanMarkdown produces the read-only plan.md workspace input
// from the ticket's active plan (tasks ordered by Ordering, as
// Store.ActivePlan returns them).
func RenderPlanMarkdown(t Ticket, tasks []Task) []byte {
	var b strings.Builder
	if len(tasks) == 0 {
		fmt.Fprintf(&b, "# Plan — ticket #%d\n\nNo plan submitted yet.\n", t.ID)
		return []byte(b.String())
	}
	fmt.Fprintf(&b, "# Plan v%d — ticket #%d\n\n", tasks[0].PlanVersion, t.ID)
	for _, task := range tasks {
		fmt.Fprintf(&b, "- %s %d. %s\n", taskGlyph(task.Status), task.Ordering, task.Title)
		if task.Detail != "" {
			for _, line := range strings.Split(strings.TrimRight(task.Detail, "\n"), "\n") {
				fmt.Fprintf(&b, "  %s\n", line)
			}
		}
	}
	return []byte(b.String())
}
```

- [ ] Run to verify pass: `go test ./agent/api/tickets/` — expect all green.

- [ ] Commit: `git add agent/api/tickets && git commit -m "feat(ticket-core): deterministic spec.md/plan.md render helpers"`

---

### Task 10: go-concourse client methods

Three client methods backing the fly subcommands, following the `wall.go`/`builds.go` idiom (`connection.Send` + `internal.Response{Result: ...}`; not-found via `internal.ResourceNotFoundError`, see `go-concourse/concourse/builds.go:105`). The suite provides `atcServer` (ghttp) and `client` (`go-concourse/concourse/concourse_suite_test.go:22-32`).

**Files:**
- Modify: `go-concourse/concourse/client.go:39` (Client interface, after `ClearWall() error`)
- Create: `go-concourse/concourse/agent_tickets.go`
- Create (regenerated): `go-concourse/concourse/concoursefakes/fake_client.go`
- Test: `go-concourse/concourse/agent_tickets_test.go`

**Steps:**

- [ ] Write the failing spec `go-concourse/concourse/agent_tickets_test.go`:

```go
package concourse_test

import (
	"net/http"

	"github.com/concourse/concourse/agent/api/tickets"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/ghttp"
)

var _ = Describe("Agent Tickets", func() {
	Describe("ListAgentTickets", func() {
		BeforeEach(func() {
			atcServer.AppendHandlers(
				ghttp.CombineHandlers(
					ghttp.VerifyRequest("GET", "/api/v1/agent/tickets", "state=queued&limit=5"),
					ghttp.RespondWithJSONEncoded(http.StatusOK, []tickets.Ticket{
						{ID: 7, Title: "fix X", State: tickets.StateQueued, Repo: "tdmtrader/concourse"},
					}),
				),
			)
		})

		It("sends the filters and decodes the list", func() {
			list, err := client.ListAgentTickets(tickets.ListFilter{State: tickets.StateQueued, Limit: 5})
			Expect(err).NotTo(HaveOccurred())
			Expect(list).To(HaveLen(1))
			Expect(list[0].ID).To(Equal(7))
			Expect(list[0].State).To(Equal(tickets.StateQueued))
		})
	})

	Describe("CreateAgentTicket", func() {
		BeforeEach(func() {
			atcServer.AppendHandlers(
				ghttp.CombineHandlers(
					ghttp.VerifyRequest("POST", "/api/v1/agent/tickets"),
					ghttp.VerifyJSON(`{"title":"fix X","body":"details","origin":"fly","repo":"tdmtrader/concourse","target_branch":"main"}`),
					ghttp.RespondWithJSONEncoded(http.StatusCreated, tickets.Ticket{
						ID: 8, Title: "fix X", State: tickets.StateDraft,
					}),
				),
			)
		})

		It("posts the request body and decodes the created ticket", func() {
			created, err := client.CreateAgentTicket(tickets.CreateRequest{
				Title: "fix X", Body: "details", Origin: "fly",
				Repo: "tdmtrader/concourse", TargetBranch: "main",
			})
			Expect(err).NotTo(HaveOccurred())
			Expect(created.ID).To(Equal(8))
			Expect(created.State).To(Equal(tickets.StateDraft))
		})
	})

	Describe("GetAgentTicket", func() {
		Context("when the ticket exists", func() {
			BeforeEach(func() {
				atcServer.AppendHandlers(
					ghttp.CombineHandlers(
						ghttp.VerifyRequest("GET", "/api/v1/agent/tickets/7"),
						ghttp.RespondWithJSONEncoded(http.StatusOK, tickets.TicketDetail{
							Ticket: tickets.Ticket{ID: 7, Title: "fix X", State: tickets.StateRunning},
							Tasks:  []tickets.Task{{Ordering: 1, Title: "one", Status: tickets.TaskDone}},
						}),
					),
				)
			})

			It("returns the detail and found=true", func() {
				detail, found, err := client.GetAgentTicket(7)
				Expect(err).NotTo(HaveOccurred())
				Expect(found).To(BeTrue())
				Expect(detail.Ticket.ID).To(Equal(7))
				Expect(detail.Tasks).To(HaveLen(1))
			})
		})

		Context("when the ticket is missing", func() {
			BeforeEach(func() {
				atcServer.AppendHandlers(
					ghttp.CombineHandlers(
						ghttp.VerifyRequest("GET", "/api/v1/agent/tickets/99"),
						ghttp.RespondWith(http.StatusNotFound, ""),
					),
				)
			})

			It("returns found=false without an error", func() {
				_, found, err := client.GetAgentTicket(99)
				Expect(err).NotTo(HaveOccurred())
				Expect(found).To(BeFalse())
			})
		})
	})
})
```

- [ ] Run to verify it fails: `ginkgo ./go-concourse/concourse/` — expected failure: compile error `client.ListAgentTickets undefined`.

- [ ] Add the three methods to the `Client` interface in `go-concourse/concourse/client.go` after `ClearWall() error` (:39), importing `"github.com/concourse/concourse/agent/api/tickets"`:

```go
	ListAgentTickets(filter tickets.ListFilter) ([]tickets.Ticket, error)
	CreateAgentTicket(req tickets.CreateRequest) (tickets.Ticket, error)
	GetAgentTicket(id int) (tickets.TicketDetail, bool, error)
```

- [ ] Create `go-concourse/concourse/agent_tickets.go`:

```go
package concourse

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"

	"github.com/concourse/concourse/agent/api/tickets"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/go-concourse/concourse/internal"
	"github.com/tedsuo/rata"
)

func (client *client) ListAgentTickets(filter tickets.ListFilter) ([]tickets.Ticket, error) {
	query := url.Values{}
	if filter.State != "" {
		query.Set("state", string(filter.State))
	}
	if filter.Repo != "" {
		query.Set("repo", filter.Repo)
	}
	if filter.Origin != "" {
		query.Set("origin", filter.Origin)
	}
	if filter.Limit > 0 {
		query.Set("limit", strconv.Itoa(filter.Limit))
	}

	var result []tickets.Ticket
	err := client.connection.Send(internal.Request{
		RequestName: atc.ListAgentTickets,
		Query:       query,
	}, &internal.Response{
		Result: &result,
	})
	return result, err
}

func (client *client) CreateAgentTicket(req tickets.CreateRequest) (tickets.Ticket, error) {
	buffer := &bytes.Buffer{}
	if err := json.NewEncoder(buffer).Encode(req); err != nil {
		return tickets.Ticket{}, err
	}

	var created tickets.Ticket
	err := client.connection.Send(internal.Request{
		RequestName: atc.CreateAgentTicket,
		Body:        buffer,
		Header:      http.Header{"Content-Type": []string{"application/json"}},
	}, &internal.Response{
		Result: &created,
	})
	return created, err
}

func (client *client) GetAgentTicket(id int) (tickets.TicketDetail, bool, error) {
	var detail tickets.TicketDetail
	err := client.connection.Send(internal.Request{
		RequestName: atc.GetAgentTicket,
		Params:      rata.Params{"ticket_id": strconv.Itoa(id)},
	}, &internal.Response{
		Result: &detail,
	})
	switch err.(type) {
	case nil:
		return detail, true, nil
	case internal.ResourceNotFoundError:
		return detail, false, nil
	default:
		return detail, false, err
	}
}
```

- [ ] Regenerate the client fake: `cd go-concourse/concourse && go run github.com/maxbrunsfeld/counterfeiter/v6 -o concoursefakes/fake_client.go . Client && cd ../..`

- [ ] Run to verify pass: `ginkgo ./go-concourse/concourse/ && go build ./go-concourse/...` — expect green.

- [ ] Commit: `git add go-concourse && git commit -m "feat(ticket-core): go-concourse agent ticket client methods"`

---

### Task 11: `fly agent tickets list/create/show`

Extends the shared `AgentCommand` family created by credentials-and-budgets (wave-1 addendum: wave-mates append fields, additive merges only). Table output via `fly/ui.Table` (`fly/ui/table.go:12`, render idiom `fly/commands/pipelines.go:54-97`). Integration tests against the mock ATC (`fly/integration/get_wall_test.go` idiom).

**Files:**
- Modify: `fly/commands/agent.go` (add the `Tickets` field to `AgentCommand`)
- Create: `fly/commands/agent_tickets.go`
- Test: `fly/integration/agent_tickets_test.go`

**Steps:**

- [ ] Write the failing integration specs `fly/integration/agent_tickets_test.go`:

```go
package integration_test

import (
	"os/exec"

	"github.com/concourse/concourse/agent/api/tickets"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/gbytes"
	"github.com/onsi/gomega/gexec"
	"github.com/onsi/gomega/ghttp"
)

var _ = Describe("fly agent tickets", func() {
	Describe("list", func() {
		It("renders a table of tickets", func() {
			atcServer.AppendHandlers(
				ghttp.CombineHandlers(
					ghttp.VerifyRequest("GET", "/api/v1/agent/tickets", "state=queued&limit=50"),
					ghttp.RespondWithJSONEncoded(200, []tickets.Ticket{
						{ID: 7, Title: "fix X", State: tickets.StateQueued,
							Repo: "tdmtrader/concourse", UserName: "tdm"},
					}),
				),
			)

			flyCmd := exec.Command(flyPath, "-t", targetName, "agent", "tickets", "list", "--state", "queued")
			sess, err := gexec.Start(flyCmd, GinkgoWriter, GinkgoWriter)
			Expect(err).NotTo(HaveOccurred())
			<-sess.Exited
			Expect(sess.ExitCode()).To(Equal(0))
			Expect(sess.Out).To(gbytes.Say("7"))
			Expect(sess.Out).To(gbytes.Say("queued"))
			Expect(sess.Out).To(gbytes.Say("tdmtrader/concourse"))
			Expect(sess.Out).To(gbytes.Say("fix X"))
		})
	})

	Describe("create", func() {
		It("posts origin fly and prints the new ticket id", func() {
			atcServer.AppendHandlers(
				ghttp.CombineHandlers(
					ghttp.VerifyRequest("POST", "/api/v1/agent/tickets"),
					ghttp.VerifyJSON(`{"title":"fix X","body":"details","origin":"fly","repo":"tdmtrader/concourse","target_branch":"main","budget_usd":5}`),
					ghttp.RespondWithJSONEncoded(201, tickets.Ticket{ID: 8, State: tickets.StateDraft}),
				),
			)

			flyCmd := exec.Command(flyPath, "-t", targetName, "agent", "tickets", "create",
				"--title", "fix X", "--body", "details",
				"--repo", "tdmtrader/concourse", "--budget", "5")
			sess, err := gexec.Start(flyCmd, GinkgoWriter, GinkgoWriter)
			Expect(err).NotTo(HaveOccurred())
			<-sess.Exited
			Expect(sess.ExitCode()).To(Equal(0))
			Expect(sess.Out).To(gbytes.Say("created ticket #8"))
		})
	})

	Describe("show", func() {
		It("prints ticket, spec, and plan", func() {
			atcServer.AppendHandlers(
				ghttp.CombineHandlers(
					ghttp.VerifyRequest("GET", "/api/v1/agent/tickets/7"),
					ghttp.RespondWithJSONEncoded(200, tickets.TicketDetail{
						Ticket: tickets.Ticket{ID: 7, Title: "fix X", State: tickets.StateRunning,
							Origin: "fly", Repo: "tdmtrader/concourse", TargetBranch: "main", Body: "details"},
						Spec: &tickets.Spec{Version: 2, Title: "the spec"},
						Tasks: []tickets.Task{
							{Ordering: 1, Title: "one", Status: tickets.TaskDone},
							{Ordering: 2, Title: "two", Status: tickets.TaskPending},
						},
					}),
				),
			)

			flyCmd := exec.Command(flyPath, "-t", targetName, "agent", "tickets", "show", "--id", "7")
			sess, err := gexec.Start(flyCmd, GinkgoWriter, GinkgoWriter)
			Expect(err).NotTo(HaveOccurred())
			<-sess.Exited
			Expect(sess.ExitCode()).To(Equal(0))
			Expect(sess.Out).To(gbytes.Say(`ticket #7: fix X`))
			Expect(sess.Out).To(gbytes.Say(`state: running`))
			Expect(sess.Out).To(gbytes.Say(`spec v2: the spec`))
			Expect(sess.Out).To(gbytes.Say(`1. \[done\] one`))
			Expect(sess.Out).To(gbytes.Say(`2. \[pending\] two`))
		})

		It("exits 1 when the ticket does not exist", func() {
			atcServer.AppendHandlers(
				ghttp.CombineHandlers(
					ghttp.VerifyRequest("GET", "/api/v1/agent/tickets/99"),
					ghttp.RespondWith(404, ""),
				),
			)

			flyCmd := exec.Command(flyPath, "-t", targetName, "agent", "tickets", "show", "--id", "99")
			sess, err := gexec.Start(flyCmd, GinkgoWriter, GinkgoWriter)
			Expect(err).NotTo(HaveOccurred())
			<-sess.Exited
			Expect(sess.ExitCode()).To(Equal(1))
			Expect(sess.Err).To(gbytes.Say("ticket 99 not found"))
		})
	})
})
```

- [ ] Run to verify it fails: `ginkgo ./fly/integration/ --focus="fly agent tickets"` — expected failure: fly exits non-zero with `Unknown command 'tickets'` (the `AgentCommand` struct has no Tickets field yet).

- [ ] Add the field to `AgentCommand` in `fly/commands/agent.go` (struct created by credentials-and-budgets in wave 1):

```go
	Tickets AgentTicketsCommand `command:"tickets" description:"File and track agent tickets"`
```

- [ ] Create `fly/commands/agent_tickets.go`:

```go
package commands

import (
	"fmt"
	"os"
	"strconv"

	"github.com/concourse/concourse/agent/api/tickets"
	"github.com/concourse/concourse/fly/rc"
	"github.com/concourse/concourse/fly/ui"
	"github.com/fatih/color"
)

type AgentTicketsCommand struct {
	List   AgentTicketsListCommand   `command:"list" description:"List agent tickets"`
	Create AgentTicketsCreateCommand `command:"create" description:"File a new agent ticket (state: draft)"`
	Show   AgentTicketsShowCommand   `command:"show" description:"Show one ticket with its spec and plan"`
}

type AgentTicketsListCommand struct {
	State  string `long:"state" description:"Filter by lifecycle state (draft, queued, running, needs_review, merged, merged_with_fixes, sent_back, abandoned, concluded, failed, errored)"`
	Repo   string `long:"repo" description:"Filter by repo slug (e.g. tdmtrader/concourse)"`
	Origin string `long:"origin" description:"Filter by origin (web, fly, jira, retrospective)"`
	Limit  int    `long:"limit" default:"50" description:"Maximum tickets to list"`
}

func (command *AgentTicketsListCommand) Execute([]string) error {
	target, err := rc.LoadTarget(Fly.Target, Fly.Verbose)
	if err != nil {
		return err
	}
	if err := target.Validate(); err != nil {
		return err
	}

	list, err := target.Client().ListAgentTickets(tickets.ListFilter{
		State:  tickets.State(command.State),
		Repo:   command.Repo,
		Origin: command.Origin,
		Limit:  command.Limit,
	})
	if err != nil {
		return err
	}

	table := ui.Table{Headers: ui.TableRow{
		{Contents: "id", Color: color.New(color.Bold)},
		{Contents: "state", Color: color.New(color.Bold)},
		{Contents: "repo", Color: color.New(color.Bold)},
		{Contents: "title", Color: color.New(color.Bold)},
		{Contents: "user", Color: color.New(color.Bold)},
	}}
	for _, t := range list {
		table.Data = append(table.Data, ui.TableRow{
			{Contents: strconv.Itoa(t.ID)},
			{Contents: string(t.State)},
			{Contents: t.Repo},
			{Contents: t.Title},
			{Contents: t.UserName},
		})
	}
	return table.Render(os.Stdout, Fly.PrintTableHeaders)
}

type AgentTicketsCreateCommand struct {
	Title        string  `long:"title" required:"true" description:"Ticket title"`
	Body         string  `long:"body" short:"m" description:"Markdown problem statement"`
	Repo         string  `long:"repo" required:"true" description:"Target repo slug (e.g. tdmtrader/concourse)"`
	TargetBranch string  `long:"target-branch" default:"main" description:"Branch the work targets"`
	Workflow     string  `long:"workflow" description:"Workflow definition name (empty = decided at dispatch)"`
	WorkflowVer  int     `long:"workflow-version" description:"Pin a workflow definition version (0 = live version)"`
	Budget       float64 `long:"budget" description:"Per-ticket budget in USD (0 = workflow default)"`
}

func (command *AgentTicketsCreateCommand) Execute([]string) error {
	target, err := rc.LoadTarget(Fly.Target, Fly.Verbose)
	if err != nil {
		return err
	}
	if err := target.Validate(); err != nil {
		return err
	}

	req := tickets.CreateRequest{
		Title:        command.Title,
		Body:         command.Body,
		Origin:       "fly",
		Repo:         command.Repo,
		TargetBranch: command.TargetBranch,
		WorkflowName: command.Workflow,
	}
	if command.WorkflowVer > 0 {
		req.WorkflowVersion = &command.WorkflowVer
	}
	if command.Budget > 0 {
		req.BudgetUSD = &command.Budget
	}

	created, err := target.Client().CreateAgentTicket(req)
	if err != nil {
		return err
	}
	fmt.Printf("created ticket #%d (%s)\n", created.ID, created.State)
	return nil
}

type AgentTicketsShowCommand struct {
	ID int `long:"id" required:"true" description:"Ticket id"`
}

func (command *AgentTicketsShowCommand) Execute([]string) error {
	target, err := rc.LoadTarget(Fly.Target, Fly.Verbose)
	if err != nil {
		return err
	}
	if err := target.Validate(); err != nil {
		return err
	}

	detail, found, err := target.Client().GetAgentTicket(command.ID)
	if err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("ticket %d not found", command.ID)
	}

	t := detail.Ticket
	fmt.Printf("ticket #%d: %s\n", t.ID, t.Title)
	fmt.Printf("state: %s · origin: %s · repo: %s @ %s\n", t.State, t.Origin, t.Repo, t.TargetBranch)
	if t.BudgetUSD != nil {
		fmt.Printf("budget: $%.2f\n", *t.BudgetUSD)
	}
	if t.Branch != "" {
		fmt.Printf("branch: %s\n", t.Branch)
	}
	if t.Body != "" {
		fmt.Printf("\n%s\n", t.Body)
	}
	if detail.Spec != nil {
		fmt.Printf("\nspec v%d: %s\n", detail.Spec.Version, detail.Spec.Title)
	}
	if len(detail.Tasks) > 0 {
		fmt.Println("\nplan:")
		for _, task := range detail.Tasks {
			fmt.Printf("  %d. [%s] %s\n", task.Ordering, task.Status, task.Title)
		}
	}
	return nil
}
```

- [ ] Run to verify pass: `ginkgo ./fly/integration/ --focus="fly agent tickets"` — expect all 4 specs green (the suite builds the fly binary; the mock ATC version must match `versions.go`, currently `0.1.0`, per CLAUDE.md).

- [ ] Commit: `git add fly && git commit -m "feat(ticket-core): fly agent tickets list/create/show"`

---

### Task 12: Elm data layer — types, endpoint, effects, callbacks, messages

The read/save plumbing the page module (Task 13) consumes. Precedents: `Concourse/AgentReview.elm` (decoder module), `Api/Endpoints.elm:36-38` + `:201-208` (endpoint variant + path), `Message/Effects.elm:215-217` + `:764-772` (effect + interpretation; PUT-with-body idiom at `:413-423` `SetBuildComment`), `Message/Callback.elm:68-70`, `Message/Message.elm:31-42`.

**Files:**
- Create: `web/elm/src/Concourse/AgentTicket.elm`
- Modify: `web/elm/src/Api/Endpoints.elm:38` (variant after `AgentFeedback`), `:207` (path case)
- Modify: `web/elm/src/Message/Effects.elm:217` (effect variants), `:772` (interpretation)
- Modify: `web/elm/src/Message/Callback.elm:69` (callback variants)
- Modify: `web/elm/src/Message/Message.elm:42` (page messages)
- Test: `web/elm/tests/ApiEndpointsTests.elm` (append one test)

**Steps:**

- [ ] Append the failing endpoint test to the test list in `web/elm/tests/ApiEndpointsTests.elm` (match the file's existing `test` style):

```elm
        , test "AgentTicket" <|
            \_ ->
                Endpoints.AgentTicket 12
                    |> Endpoints.toString []
                    |> Expect.equal "/api/v1/agent/tickets/12"
```

- [ ] Run to verify it fails: `cd web/elm && ../../node_modules/.bin/elm-test tests/ApiEndpointsTests.elm` — expected failure: compile error `Endpoints.AgentTicket` not found.

- [ ] Create `web/elm/src/Concourse/AgentTicket.elm`:

```elm
module Concourse.AgentTicket exposing
    ( Detail
    , Task
    , Ticket
    , decodeDetail
    )

import Json.Decode
import Json.Decode.Extra exposing (andMap)


type alias Ticket =
    { id : Int
    , title : String
    , body : String
    , state : String
    , origin : String
    , repo : String
    , targetBranch : String
    , workflowName : String
    , budgetUsd : Maybe Float
    , userName : String
    , branch : String
    , createdAt : Int
    , updatedAt : Int
    }


type alias Task =
    { ordering : Int
    , title : String
    , detail : String
    , status : String
    }


type alias Detail =
    { ticket : Ticket
    , tasks : List Task
    }


decodeTicket : Json.Decode.Decoder Ticket
decodeTicket =
    Json.Decode.succeed Ticket
        |> andMap (Json.Decode.field "id" Json.Decode.int)
        |> andMap (Json.Decode.field "title" Json.Decode.string)
        |> andMap (Json.Decode.field "body" Json.Decode.string)
        |> andMap (Json.Decode.field "state" Json.Decode.string)
        |> andMap (Json.Decode.field "origin" Json.Decode.string)
        |> andMap (Json.Decode.field "repo" Json.Decode.string)
        |> andMap (Json.Decode.field "target_branch" Json.Decode.string)
        |> andMap (Json.Decode.field "workflow_name" Json.Decode.string)
        |> andMap (Json.Decode.maybe (Json.Decode.field "budget_usd" Json.Decode.float))
        |> andMap (Json.Decode.field "user_name" Json.Decode.string)
        |> andMap (Json.Decode.field "branch" Json.Decode.string)
        |> andMap (Json.Decode.field "created_at" Json.Decode.int)
        |> andMap (Json.Decode.field "updated_at" Json.Decode.int)


decodeTask : Json.Decode.Decoder Task
decodeTask =
    Json.Decode.map4 Task
        (Json.Decode.field "ordering" Json.Decode.int)
        (Json.Decode.field "title" Json.Decode.string)
        (Json.Decode.maybe (Json.Decode.field "detail" Json.Decode.string)
            |> Json.Decode.map (Maybe.withDefault "")
        )
        (Json.Decode.field "status" Json.Decode.string)


decodeDetail : Json.Decode.Decoder Detail
decodeDetail =
    Json.Decode.map2 Detail
        (Json.Decode.field "ticket" decodeTicket)
        (Json.Decode.field "tasks" (Json.Decode.list decodeTask))
```

- [ ] In `web/elm/src/Api/Endpoints.elm`: add the variant after `| AgentFeedback` (:38):

```elm
    | AgentTicket Int
```

and the path case after the `AgentFeedback ->` case (:207):

```elm
        AgentTicket id ->
            base |> appendPath [ "agent", "tickets", String.fromInt id ]
```

- [ ] Run to verify pass: `cd web/elm && ../../node_modules/.bin/elm-test tests/ApiEndpointsTests.elm` — expect green.

- [ ] In `web/elm/src/Message/Callback.elm`: import `Concourse.AgentTicket` and add after `| AgentReviewVerdictSubmitted ...` (:70):

```elm
    | AgentTicketFetched (Fetched Concourse.AgentTicket.Detail)
    | AgentTicketSaved Int (Fetched ())
```

- [ ] In `web/elm/src/Message/Effects.elm`: import `Concourse.AgentTicket`, add the effect variants after `| SubmitAgentReviewVerdict ...` (:217):

```elm
    | FetchAgentTicket Int
    | SaveAgentTicket { id : Int, title : String, body : String, budgetUsd : Maybe Float }
```

and the interpretation after the `FetchTeamAgentReviews ...` case (:770-772), mirroring the `SetBuildComment` PUT idiom (:413-423):

```elm
        FetchAgentTicket id ->
            Api.get (Endpoints.AgentTicket id)
                |> Api.expectJson Concourse.AgentTicket.decodeDetail
                |> Api.request
                |> Task.attempt AgentTicketFetched

        SaveAgentTicket { id, title, body, budgetUsd } ->
            Api.put (Endpoints.AgentTicket id) csrfToken
                |> Api.withJsonBody
                    (Json.Encode.object
                        ([ ( "title", Json.Encode.string title )
                         , ( "body", Json.Encode.string body )
                         ]
                            ++ (case budgetUsd of
                                    Just b ->
                                        [ ( "budget_usd", Json.Encode.float b ) ]

                                    Nothing ->
                                        []
                               )
                        )
                    )
                |> Api.request
                |> Task.attempt (AgentTicketSaved id)
```

- [ ] In `web/elm/src/Message/Message.elm`: add the page messages after the Comment Bar block (:39-42):

```elm
      -- Agent Ticket page
    | ClickAgentTicketEdit
    | AgentTicketTitleChanged String
    | AgentTicketBodyChanged String
    | AgentTicketBudgetChanged String
    | ClickAgentTicketSave
    | ClickAgentTicketCancel
```

- [ ] Run to verify everything still compiles: `cd web/elm && ../../node_modules/.bin/elm-test` — expect the full suite green (no page consumes the new messages yet; unused constructors compile fine).

- [ ] Commit: `git add web/elm && git commit -m "feat(ticket-core): Elm data layer for the agent ticket page"`

---

### Task 13: Elm ticket page — view/edit, lifecycle badge, live task list

The page module patterned on `AgentReviews/AgentReviews.elm` (top-bar/side-bar skeleton, `Login.Model` extension), plus route + SubPage wiring. Live task list = `OnClockTick FiveSeconds` subscription refetching the detail (suppressed while editing). URL: `/agent-tickets/:id`.

**Files:**
- Create: `web/elm/src/AgentTickets/AgentTicket.elm`
- Modify: `web/elm/src/Routes.elm:61` (Route variant), `:315-318` (parser), `:486` (sitemap), `:597` (toString), `:708` and `:742` (catch-all cases)
- Modify: `web/elm/src/SubPage/SubPage.elm:14` (import), `:55` (Model variant), `:132-134` (init case), `:183-225` (genericUpdate arity + case), `:240` (handleCallback), `:283` (handleDelivery), `:301` (update), `:460-462` (title/view), `:496-497` (tooltip), `:530-531` (subscriptions)
- Test: `web/elm/tests/AgentTicketPageTests.elm`
- Modify (generated): `web/public/elm.js`, `web/public/elm.min.js` (bundle rebuild)

**Steps:**

- [ ] Write the failing page tests `web/elm/tests/AgentTicketPageTests.elm` (idiom: `web/elm/tests/AgentReviewsPageTests.elm`):

```elm
module AgentTicketPageTests exposing (all)

import Application.Application as Application
import Common
import Data
import Expect
import Message.Callback as Callback
import Message.Effects as Effects
import Message.Message
import Message.Subscription as Subscription
import Message.TopLevelMessage as Msgs
import Test exposing (Test, describe, test)
import Test.Html.Query as Query
import Test.Html.Selector exposing (class, containing, text)
import Time
import Url


sampleTicket =
    { id = 12
    , title = "fix flaky spec"
    , body = "it flakes"
    , state = "queued"
    , origin = "fly"
    , repo = "tdmtrader/concourse"
    , targetBranch = "main"
    , workflowName = ""
    , budgetUsd = Just 5
    , userName = "tdm"
    , branch = ""
    , createdAt = 0
    , updatedAt = 0
    }


sampleDetail =
    { ticket = sampleTicket
    , tasks =
        [ { ordering = 1, title = "write failing test", detail = "", status = "done" }
        , { ordering = 2, title = "fix it", detail = "", status = "in_progress" }
        ]
    }


initTicketPage : ( Application.Model, List Effects.Effect )
initTicketPage =
    Application.init Data.flags
        { protocol = Url.Http
        , host = ""
        , port_ = Nothing
        , path = "/agent-tickets/12"
        , query = Nothing
        , fragment = Nothing
        }


loadedPage : Application.Model
loadedPage =
    Common.init "/agent-tickets/12"
        |> Application.handleCallback (Callback.AgentTicketFetched (Ok sampleDetail))
        |> Tuple.first


all : Test
all =
    describe "agent ticket page"
        [ test "fetches the ticket on load" <|
            \_ ->
                initTicketPage
                    |> Tuple.second
                    |> Common.contains (Effects.FetchAgentTicket 12)
        , test "renders title, lifecycle badge, and task rows" <|
            \_ ->
                loadedPage
                    |> Common.queryView
                    |> Expect.all
                        [ Query.has [ text "fix flaky spec" ]
                        , Query.find [ class "ticket-state-badge" ]
                            >> Query.has [ text "queued" ]
                        , Query.find [ class "ticket-task-list" ]
                            >> Query.has
                                [ containing [ text "write failing test" ]
                                , containing [ text "fix it" ]
                                ]
                        ]
        , test "clock tick refetches the ticket (live task list)" <|
            \_ ->
                loadedPage
                    |> Application.handleDelivery
                        (Subscription.ClockTicked Subscription.FiveSeconds (Time.millisToPosix 0))
                    |> Tuple.second
                    |> Common.contains (Effects.FetchAgentTicket 12)
        , test "edit mode shows inputs and save emits SaveAgentTicket" <|
            \_ ->
                loadedPage
                    |> Application.update
                        (Msgs.Update <| Message.Message.ClickAgentTicketEdit)
                    |> Tuple.first
                    |> Application.update
                        (Msgs.Update <| Message.Message.AgentTicketTitleChanged "sharper title")
                    |> Tuple.first
                    |> Application.update
                        (Msgs.Update <| Message.Message.ClickAgentTicketSave)
                    |> Tuple.second
                    |> Common.contains
                        (Effects.SaveAgentTicket
                            { id = 12, title = "sharper title", body = "it flakes", budgetUsd = Just 5 }
                        )
        ]
```

(the `Msgs.Update <| Message.Message.X` dispatch idiom is the suite convention — see `web/elm/tests/BuildAgentReviewTests.elm:106-110`).

- [ ] Run to verify it fails: `cd web/elm && ../../node_modules/.bin/elm-test tests/AgentTicketPageTests.elm` — expected failure: compile error (`Routes` has no `/agent-tickets` route; `AgentTickets.AgentTicket` module missing).

- [ ] Add the route in `web/elm/src/Routes.elm`:
  1. Route variant after `| AgentReviews { teamName : String }` (:61): `| AgentTicket { id : Int }`
  2. Parser after `agentReviews` (:315-318):

```elm
agentTicket : Parser ((b -> Route) -> a) a
agentTicket =
    map (\id -> always <| AgentTicket { id = id })
        (s "agent-tickets" </> int)
```

  3. Add `agentTicket` to the `sitemap` `oneOf` list after `agentReviews` (:486).
  4. `toString` case after the `AgentReviews` case (:597):

```elm
        AgentTicket { id } ->
            ( [ "agent-tickets", String.fromInt id ], [] )
                |> RouteBuilder.build
```

  5. Add `AgentTicket _ ->` cases to the two catch-all functions that pattern-match every route (the `AgentReviews _ -> []` at :708 and `AgentReviews _ -> route` inside `withGroups` at :742) — the compiler's missing-patterns errors point at every spot.

- [ ] Create `web/elm/src/AgentTickets/AgentTicket.elm`:

```elm
module AgentTickets.AgentTicket exposing
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
import Concourse.AgentTicket as AgentTicket
import EffectTransformer exposing (ET)
import Html exposing (Html)
import Html.Attributes exposing (class, id, style, value)
import Html.Events exposing (onClick, onInput)
import Login.Login as Login
import Message.Callback exposing (Callback(..))
import Message.Effects exposing (Effect(..))
import Message.Message exposing (Message(..))
import Message.Subscription exposing (Delivery(..), Interval(..), Subscription(..))
import Routes
import SideBar.SideBar as SideBar
import Tooltip
import Views.Styles
import Views.TopBar as TopBar


type alias Model =
    Login.Model
        { ticketId : Int
        , detail : Maybe AgentTicket.Detail
        , loaded : Bool
        , loadError : Bool
        , editing : Bool
        , editTitle : String
        , editBody : String
        , editBudget : String
        }


init : { id : Int } -> ( Model, List Effect )
init { id } =
    ( { ticketId = id
      , detail = Nothing
      , loaded = False
      , loadError = False
      , editing = False
      , editTitle = ""
      , editBody = ""
      , editBudget = ""
      , isUserMenuExpanded = False
      }
    , [ FetchAgentTicket id ]
    )


documentTitle : Model -> String
documentTitle model =
    "Ticket #" ++ String.fromInt model.ticketId


handleCallback : Callback -> ET Model
handleCallback callback ( model, effects ) =
    case callback of
        AgentTicketFetched (Ok detail) ->
            ( { model | detail = Just detail, loaded = True, loadError = False }, effects )

        AgentTicketFetched (Err _) ->
            ( { model | loaded = True, loadError = True }, effects )

        AgentTicketSaved ticketId (Ok ()) ->
            ( model, effects ++ [ FetchAgentTicket ticketId ] )

        AgentTicketSaved _ (Err _) ->
            ( { model | loadError = True }, effects )

        _ ->
            ( model, effects )


handleDelivery : Delivery -> ET Model
handleDelivery delivery ( model, effects ) =
    case delivery of
        ClockTicked FiveSeconds _ ->
            if model.editing then
                ( model, effects )

            else
                ( model, effects ++ [ FetchAgentTicket model.ticketId ] )

        _ ->
            ( model, effects )


update : Message -> ET Model
update msg ( model, effects ) =
    case msg of
        ClickAgentTicketEdit ->
            case model.detail of
                Just detail ->
                    ( { model
                        | editing = True
                        , editTitle = detail.ticket.title
                        , editBody = detail.ticket.body
                        , editBudget =
                            detail.ticket.budgetUsd
                                |> Maybe.map String.fromFloat
                                |> Maybe.withDefault ""
                      }
                    , effects
                    )

                Nothing ->
                    ( model, effects )

        AgentTicketTitleChanged s ->
            ( { model | editTitle = s }, effects )

        AgentTicketBodyChanged s ->
            ( { model | editBody = s }, effects )

        AgentTicketBudgetChanged s ->
            ( { model | editBudget = s }, effects )

        ClickAgentTicketCancel ->
            ( { model | editing = False }, effects )

        ClickAgentTicketSave ->
            ( { model | editing = False }
            , effects
                ++ [ SaveAgentTicket
                        { id = model.ticketId
                        , title = model.editTitle
                        , body = model.editBody
                        , budgetUsd = String.toFloat model.editBudget
                        }
                   ]
            )

        _ ->
            ( model, effects )


tooltip : Model -> a -> Maybe Tooltip.Tooltip
tooltip _ _ =
    Nothing


subscriptions : List Subscription
subscriptions =
    [ OnClockTick FiveSeconds ]


stateColors : String -> ( String, String )
stateColors state =
    case state of
        "queued" ->
            ( "#1e3a5c", "#9fc6f0" )

        "running" ->
            ( "#5c531e", "#f0e09f" )

        "needs_review" ->
            ( "#5c3d1e", "#f0c99f" )

        "merged" ->
            ( "#2e4f2e", "#9fdf9f" )

        "merged_with_fixes" ->
            ( "#2e4f2e", "#9fdf9f" )

        "sent_back" ->
            ( "#5c3d1e", "#f0c99f" )

        "concluded" ->
            -- terminal, positive, no merge intended (spike/research)
            ( "#1e4f4a", "#9fdfd0" )

        "failed" ->
            ( "#5c2626", "#f0a0a0" )

        "errored" ->
            ( "#5c2626", "#f0a0a0" )

        _ ->
            -- draft, abandoned, unknown
            ( "#3d3c3c", "#b0b0b0" )


statusGlyph : String -> String
statusGlyph status =
    case status of
        "done" ->
            "[x]"

        "in_progress" ->
            "[~]"

        "skipped" ->
            "[-]"

        "blocked" ->
            "[!]"

        _ ->
            "[ ]"


view : Session -> Model -> Html Message
view session model =
    let
        route =
            Routes.AgentTicket { id = model.ticketId }
    in
    Html.div
        (id "page-including-top-bar" :: Views.Styles.pageIncludingTopBar)
        [ Html.div
            (id "top-bar-app" :: Views.Styles.topBar False)
            [ Html.div
                [ style "display" "flex", style "align-items" "center" ]
                (SideBar.sideBarIcon session
                    :: TopBar.breadcrumbs session route
                )
            , Login.view session.userState model
            ]
        , Html.div
            (id "page-below-top-bar" :: Views.Styles.pageBelowTopBar route)
            [ SideBar.view session Nothing
            , Html.div
                [ style "padding" "16px", style "width" "100%" ]
                (if model.loadError then
                    [ Html.p [ style "color" "#f0a0a0" ] [ Html.text "Couldn't load the ticket." ] ]

                 else
                    case model.detail of
                        Nothing ->
                            [ Html.p [ style "color" "#b0b0b0" ] [ Html.text "Loading…" ] ]

                        Just detail ->
                            if model.editing then
                                editView model

                            else
                                detailView detail
                )
            ]
        ]


detailView : AgentTicket.Detail -> List (Html Message)
detailView detail =
    let
        t =
            detail.ticket

        ( bg, fg ) =
            stateColors t.state
    in
    [ Html.div
        [ style "display" "flex", style "align-items" "center", style "gap" "12px" ]
        [ Html.h1 [ style "font-size" "18px", style "margin" "0" ]
            [ Html.text ("#" ++ String.fromInt t.id ++ " " ++ t.title) ]
        , Html.span
            [ class "ticket-state-badge"
            , style "padding" "2px 10px"
            , style "font-weight" "700"
            , style "background" bg
            , style "color" fg
            ]
            [ Html.text t.state ]
        , Html.button
            [ class "ticket-edit-button", onClick ClickAgentTicketEdit ]
            [ Html.text "edit" ]
        ]
    , Html.div
        [ style "font-family" "monospace", style "font-size" "12px", style "color" "#7a7a7a", style "margin" "8px 0" ]
        [ Html.text
            (t.repo
                ++ " @ "
                ++ t.targetBranch
                ++ " · "
                ++ t.origin
                ++ (case t.budgetUsd of
                        Just b ->
                            " · $" ++ String.fromFloat b

                        Nothing ->
                            ""
                   )
                ++ (if t.branch /= "" then
                        " · " ++ t.branch

                    else
                        ""
                   )
            )
        ]
    , Html.pre
        [ style "white-space" "pre-wrap", style "margin" "12px 0" ]
        [ Html.text t.body ]
    , Html.div [ class "ticket-task-list" ]
        (if List.isEmpty detail.tasks then
            [ Html.p [ style "color" "#b0b0b0" ] [ Html.text "No plan submitted yet." ] ]

         else
            List.map taskRow detail.tasks
        )
    ]


taskRow : AgentTicket.Task -> Html Message
taskRow task =
    Html.div
        [ class "ticket-task-row"
        , style "display" "flex"
        , style "gap" "8px"
        , style "padding" "4px 0"
        , style "font-family" "monospace"
        ]
        [ Html.span [] [ Html.text (statusGlyph task.status) ]
        , Html.span [] [ Html.text (String.fromInt task.ordering ++ ". " ++ task.title) ]
        , Html.span [ style "color" "#7a7a7a" ] [ Html.text task.status ]
        ]


editView : Model -> List (Html Message)
editView model =
    [ Html.div [ style "display" "flex", style "flex-direction" "column", style "gap" "8px", style "max-width" "640px" ]
        [ Html.input
            [ id "ticket-title-input"
            , value model.editTitle
            , onInput AgentTicketTitleChanged
            ]
            []
        , Html.textarea
            [ id "ticket-body-input"
            , style "min-height" "160px"
            , value model.editBody
            , onInput AgentTicketBodyChanged
            ]
            []
        , Html.input
            [ id "ticket-budget-input"
            , Html.Attributes.placeholder "budget USD (empty = workflow default)"
            , value model.editBudget
            , onInput AgentTicketBudgetChanged
            ]
            []
        , Html.div [ style "display" "flex", style "gap" "8px" ]
            [ Html.button [ class "ticket-save-button", onClick ClickAgentTicketSave ] [ Html.text "save" ]
            , Html.button [ class "ticket-cancel-button", onClick ClickAgentTicketCancel ] [ Html.text "cancel" ]
            ]
        ]
    ]
```

- [ ] Wire `web/elm/src/SubPage/SubPage.elm` exactly as `AgentReviewsModel` is wired (every anchor below names the existing AgentReviews line to mirror):
  1. `import AgentTickets.AgentTicket as AgentTicket` (next to the AgentReviews import, :14).
  2. Model variant after `| AgentReviewsModel AgentReviews.Model` (:55): `| AgentTicketModel AgentTicket.Model`.
  3. Init case after the `Routes.AgentReviews` case (:132-134):

```elm
        Routes.AgentTicket { id } ->
            AgentTicket.init { id = id }
                |> Tuple.mapFirst AgentTicketModel
```

  4. Extend `genericUpdate` (:183-225) with an 11th function argument `fAT : ET AgentTicket.Model` (after `fAR`) and a final case:

```elm
        AgentTicketModel agentTicketModel ->
            fAT ( agentTicketModel, effects )
                |> Tuple.mapFirst AgentTicketModel
```

  5. Thread the new argument through every `genericUpdate` call site (the compiler enumerates them): `handleCallback` gets `(AgentTicket.handleCallback callback)` (after the AgentReviews arg, :240) and `handleLoggedOut` in its LoggedOut branch; `handleDelivery` gets `(AgentTicket.handleDelivery delivery)` (:283, replacing nothing — append as the 11th arg where AgentReviews has `identity`); `update` gets `(Login.update msg >> AgentTicket.update msg)` (:301).
  6. Title/view case after the `AgentReviewsModel` case (:460-462):

```elm
        AgentTicketModel model ->
            ( AgentTicket.documentTitle model
            , AgentTicket.view session model
            )
```

  7. Tooltip case after the `AgentReviewsModel` case (:496-497), same shape (`AgentTicket.tooltip : Model -> a -> Maybe Tooltip.Tooltip`, partially applied exactly like `AgentReviews.tooltip model`):

```elm
        AgentTicketModel model ->
            AgentTicket.tooltip model
```

  8. Subscriptions case (:530-531):

```elm
        AgentTicketModel _ ->
            AgentTicket.subscriptions
```

- [ ] Run to verify pass: `cd web/elm && ../../node_modules/.bin/elm-test tests/AgentTicketPageTests.elm` — expect the 4 tests green. Then run the full Elm suite: `cd web/elm && ../../node_modules/.bin/elm-test` — expect green.

- [ ] Rebuild the frontend bundle (repo convention — see commit `6f16d19ab5` "rebuild frontend bundle"): `yarn build` from the repo root; verify `git status` shows `web/public/elm.js` + `web/public/elm.min.js` changed.

- [ ] Commit: `git add web/elm web/public && git commit -m "feat(ticket-core): Elm agent ticket page with edit mode and live task list"`

---

### Task 14: Jira-sync phase-2 design note (spec open item 10)

A short design note proving the `origin` + `external_ref` seam admits a phase-2 Jira sync with no schema redesign. Documentation only — the charter's deliverable is the written proof.

**Files:**
- Create: `docs/superpowers/plans/agentic-platform/ticket-jira-sync-phase2.md`

**Steps:**

- [ ] Write `docs/superpowers/plans/agentic-platform/ticket-jira-sync-phase2.md`:

```markdown
# Jira Sync, Phase 2 — Design Note (spec open item 10)

- **Status:** design note, written by ticket-core (wave 2). Nothing here is
  implemented; the point is to prove the wave-2 schema needs NO redesign
  when Jira arrives.
- **Spec basis:** §1 ("`origin` field from day one so the future Jira sync
  is just another writer"), §9 ("Jira status sync rides the same seam in
  phase 2"), out-of-scope list ("Jira is phase 2, via the ticket `origin`
  seam").

## The seam, as landed in wave 2

| Piece | Where | Why it is sufficient |
|---|---|---|
| `origin` enum incl. `'jira'` | `agent_tickets.origin` CHECK (migration 1773106050) | the sync component is just another writer with `origin='jira'`; no ALTER needed |
| `external_ref TEXT NOT NULL DEFAULT ''` | `agent_tickets` (same migration) | holds the Jira issue key (`PROJ-123`); '' = native ticket; indexable later with a partial index if lookup volume warrants (additive `CREATE INDEX`, not a redesign) |
| `jira_account_id` | `agent_user_credentials` (credentials-and-budgets, §1.3) | maps the Jira reporter to a platform user so dispatch attaches the right vaulted credential and the ledger attributes spend correctly |
| Single-writer transition function | `tickets.Store.Transition` | the sync component walks tickets through the same state machine as everyone else — no Jira-specific lifecycle |
| CRUD API on principals | `/api/v1/agent/tickets` + `principal(tickets:write)` | the sync component authenticates as an ordinary agent principal (e.g. name `jira-sync`), mintable/revocable via agent-identity's admin API |

## What phase 2 adds (all additive)

1. **A `jira-sync` RunnableComponent** (registrar/reaper pattern; polling +
   notify, NEVER notify-only — the fork's lossy-NOTIFY lesson): polls the
   Jira API for issues labeled for agent work, creates tickets via
   `tickets.Store` with `origin='jira'`, `external_ref=<issue key>`,
   `user_name` mapped through `agent_user_credentials.jira_account_id`.
2. **Status write-back:** on ticket transitions (needs_review, merged,
   failed), the component transitions the Jira issue via its API. State
   mapping lives in the component's config, not in the schema.
3. **One API change, additive:** the v1 API rejects `origin:"jira"`
   (400) so nothing writes half-synced rows before the component exists;
   phase 2 relaxes that check for the `jira-sync` principal only.
4. **Optional partial index** if Jira lookups get hot:
   `CREATE INDEX agent_tickets_external_ref ON agent_tickets (external_ref) WHERE external_ref <> ''`.

## Why nothing else is touched

- Ticket identity stays `agent_tickets.id` (branch `agent/ticket-<id>`);
  the Jira key is a reference, never the primary key — renumbering
  machinery is avoided exactly as contracts decision 6 intends.
- Specs/plans/tasks are Jira-agnostic: agents submit them through
  platform-mcp regardless of origin.
- Cost attribution already flows through `user_name`/`user_id` +
  `agent_user_credentials`; the Jira mapping seam column exists since
  wave 1.
- The Elm page and fly render `origin`/`external_ref` as plain fields; a
  later nicety (linkifying `PROJ-123`) is a view-only change.

## Explicitly deferred to phase 2

- Jira webhook ingestion (v1 stance: poll; webhooks are an optimization).
- Comment mirroring and attachment sync (out of end-state scope: external
  inboxes).
- Two-way field edits (title/body) — phase 2 decides a source-of-truth
  policy; the envelope (spec version rows) already supports resubmission
  without data loss.

**Conclusion:** the phase-2 sync is one new RunnableComponent, one relaxed
handler check, and zero migrations beyond an optional additive index. The
seam holds.
```

- [ ] Verify the note's claims against the landed schema: `grep -n "external_ref\|origin" atc/db/migration/migrations/1773106050_create_agent_tickets.up.sql` — both columns present; `grep -n "jira_account_id" atc/db/migration/migrations/1773106020_create_agent_user_credentials.up.sql` — the wave-1 seam column exists.

- [ ] Commit: `git add docs/superpowers/plans/agentic-platform/ticket-jira-sync-phase2.md && git commit -m "docs(ticket-core): Jira-sync phase-2 design note (open item 10)"`

---

## Execution notes

**Full workstream test suite (in dependency order):**

```bash
pg_isready                                                  # Postgres required for atc/db + atc/api
ginkgo --focus="Legacy Database Upgrade" ./atc/db/migration/  # migrations up+down
go test ./agent/api/tickets/                                # domain, handler, render (plain go test)
ginkgo --focus="AgentTicketsFactory" ./atc/db/              # factory (template-DB suite, ~90s if full)
ginkgo ./atc/api/auth/ ./atc/wrappa/                        # auth helper + exhaustive switch
ginkgo -p ./atc/api/                                        # API wiring
ginkgo ./go-concourse/concourse/                            # client
ginkgo ./fly/integration/ --focus="fly agent tickets"       # fly (builds the binary; mock ATC v0.1.0)
cd web/elm && ../../node_modules/.bin/elm-test && cd ../..  # Elm suite
make test-unit                                              # full gate before merge (~3 min)
```

Never use `--race` (parallel compilation failures, per CLAUDE.md). If `database "testdb_template" already exists`, another atc/db run is live — wait for it.

**Live-test requirements:** none. This workstream has no K8s/jetbridge surface, so no theborg live tests are needed. Optional post-deploy smoke on the live theborg web (`concourse.home`, see memory notes on fly-login): `fly -t ci agent tickets create --title smoke --repo tdmtrader/concourse && fly -t ci agent tickets list --origin fly` then verify the ticket page renders at `https://concourse.home/agent-tickets/<id>`. Note the smoke ticket stays in `draft` — nothing dispatches it in wave 2.

**Rollback notes for the risky diffs:**
- **Migrations (Task 2):** purely additive tables; both down files are plain `DROP TABLE`s and are exercised by the legacy-upgrade suite. Rolling back = down-migrate 3 versions; no existing table is touched. The only cross-branch coupling is the `jetbridgeHeadMigration` const shared with agent-step — on merge conflict keep the higher number.
- **Wrappa/auth (Task 8):** the exhaustive-switch panic (`you missed a spot`) makes a route/tier mismatch fail closed at wrappa-construction time (caught by `ginkgo ./atc/wrappa/`), and a missing `DefaultRoles` entry fails toward admin-only (documented at `atc/api/accessor/roles.go:102-107`) — over-restrictive, never over-permissive. Reverting Task 8's commit fully de-registers the routes.
- **API surface:** consumers arrive in wave 3+ (platform-mcp-hitl, dispatch). Until then the routes are only exercised by fly and the web page, so a revert has no cross-workstream blast radius beyond re-planning wave 3 against the restored addendum.
- **Elm bundle (Task 13):** `web/public/elm*.js` are generated; a bad bundle reverts with the commit. The page is reachable only via direct URL (no nav entry in v1) — a rendering bug cannot break existing CI pages beyond the shared SubPage wiring, which the full elm-test suite covers.

---

## Amendment log (this plan)

- **2026-07-09 (final-review F17, ticket-core leg — checkpoint-seam delta §7, co-signed dispatch/platform-mcp-hitl/ticket-core/shared-contracts):** Dispatch gains a run-completion reconciler (plan 11, new Task 11b) that walks `StateRunning` tickets whose pipeline run completed; on a rejected `send_back` checkpoint it calls `Transition(running→queued, TransitionMeta{})`. **NO `validTransitions` matrix change** — `StateRunning` already lists `StateQueued`; this plan's change is semantic broadening + guard rails so no future edit narrows the edges the reconciler depends on. Edits landed (all in-place, no task renumbering): (1) the edge annotation everywhere it appears is now `running → queued (retryable platform error OR rejected send_back checkpoint re-dispatch; attempt_count++)` — Task 1 §2.1 side-effects addendum bullet, Task 3 `validTransitions` doc comment, Task 3 `MemoryStore.Transition`, Task 5 DB `Transition`; (2) the §2.1 side effects for the new caller are pinned by test — the Task 5 requeue spec now asserts, on `running→queued` with empty `TransitionMeta{}` (the reconciler's exact call shape), `attempt_count == 1`, `completed_at` cleared, and `queued_at` re-stamped (`sql.NullTime` query); (3) matrix-test comments in Task 3's `TestValidTransitionMatrix` name dispatch's run-completion reconciler as the SECOND legitimate caller of `running→queued`, and record the TWO-WRITERS rule for `running→needs_review` (harvest primary per 09-harvest-step:94, reconciler backup/safety net) — mirrored into the Task 1 addendum, `TransitionMeta.Branch` comment, `Store` doc comment, and Task 5 intro (racing writers see benign `ErrStaleTransition`/`ErrTicketNotFound`). The re-dispatch loop cap is NOT this plan's concern: the reconciler requeues unconditionally; dispatch's existing MaxAttempts guard (plan 11 Task 11) errors a queued ticket at the cap. Ticket-core surface is otherwise unchanged (no new types, methods, routes, or migrations).

- **2026-07-09 (flow-decoupling, CONCLUDED terminal state — FLOWS.md §3 spike-research / §4 state-enum decision, owner-approved):** The lifecycle enum gains one state, `concluded` — "run finished, human reviewed, no merge intended" (spike/research flows) — the positive sibling of `abandoned`, reachable ONLY from `needs_review` via explicit human disposition, TERMINAL (no outgoing edges). Rationale: this plan freezes the enum up front (charter: "full lifecycle enum from day one"; migration `1773106050`'s CHECK cannot be amended cheaply later), so per FLOWS.md §4 the state-enum decision is the one spike-flow piece that must NOT be deferred — `concluded` is added PRE-FREEZE; the harvest push knob and repo-relaxation that make spike flows fully expressible remain deferred to plans 05/11. Edits landed (all in-place, no task renumbering): (1) Task 2 migration `1773106050` state CHECK gains `'concluded'`; (2) Task 3 `types.go` gains `StateConcluded` (doc comment pins terminal + human-disposition-only semantics), `validTransitions[StateNeedsReview]` gains `StateConcluded` (no `validTransitions[StateConcluded]` entry — terminal), `ValidState`'s terminal-state switch gains it, and `MemoryStore.Transition`'s terminal case stamps `completed_at`; (3) Task 3 `TestValidTransitionMatrix` gains an allowed case (`needs_review→concluded`, comment pinning the FLOWS.md semantics) and three forbidden cases (`draft→concluded`, `running→concluded`, `concluded→queued` — terminal, no exits); `TestValidStateOriginTaskStatus` lists it; (4) Task 5 DB `Transition` terminal case gains `tickets.StateConcluded` and a new seventh spec pins `completed_at` stamped on `needs_review→concluded` plus `ErrInvalidTransition` on any exit; (5) Task 1's §2.1 side-effects addendum bullet adds `concluded` to the terminal set and documents the edge; (6) UI surfaces list it — fly `--state` filter description and the Elm `stateColors` badge case (distinct teal: terminal-positive, not merged-green). No new routes, Store methods, or migrations beyond the CHECK-list membership; handlers validate via `ValidState`/`ValidTransition` and pick the state up automatically. Wave-3+ consumers: delivery-outcomes owns the human disposition button; harvest/dispatch never write `concluded`.

- **2026-07-17 (execution session — migration renumber + core-slice scoping, mirroring the 2026-07-12 agent-step renumber precedent):**
  - **Migration renumber (Task 2):** the reserved block `1773106050–52` was overtaken by the deploy head before this plan executed — agent-step landed `1773106060`/`1773106061` and theborg's DB is already at `1773106061`; the version-pointer migrator would silently skip lower numbers forever. The three migrations landed as `1773106062_create_agent_tickets` / `1773106063_create_agent_ticket_specs` / `1773106064_create_agent_ticket_tasks`; `jetbridgeHeadMigration` and `migrate-preflight.sh`'s `JETBRIDGE_VERSION` are now `1773106064`. The deferred PARK-V2 `agent_run_step_state` (agent-step Task 25), which had re-reserved `1773106062`, moves to `1773106065`. Every `1773106050–52` reference in the task text above should be read through this mapping; contracts §1.1/§1.7/§1.14 and plans 07/08/11 updated (see 00-shared-contracts.md §11, 2026-07-17 renumber entry).
  - **Core-slice scoping ("tickets exist" slice, owner-directed):** this session landed Tasks 1–8 and 10–11 plus an end-to-end attribution proof (a real `AGENT_TICKET_ID` claim verifying through `PipelineRunFactory.TicketBelongsToRun` against a factory-created ticket and attributing an `agent_run_metrics` row). **Deferred, not built:** Task 9 (spec.md/plan.md render helpers — signatures stay frozen in the Task-1 addendum; land with dispatch, their only consumer), Task 12/13 (Elm data layer + ticket page), Task 14 (Jira-sync phase-2 design note), and all budget ADMISSION/ledger enforcement beyond the seams (the `budget_usd` column, `BudgetUSD` fields, and fly `--budget` flag exist and round-trip; nothing enforces them until dispatch lands). The Task-2 side effect that `agent_cost_ledger.ticket_id` starts joining to real `agent_tickets.id` rows holds as described.

  - **Post-land review fix (same day):** agent-review-native #7 proved a TOCTOU lost-update in the Task-7 `UpdateTask` handler (read active plan_version, write against the captured version; concurrent `SubmitPlan` in the window → 200 OK but the update lands on the superseded plan). Fixed by the additive `Store.UpdateActiveTask` (atomic resolve+write, FOR UPDATE-serialized with SubmitPlan; see the 2026-07-17 fix entry in 00-shared-contracts §11); regression pinned by `agent/api/tickets/task_race_test.go`. Negative `budget_usd` now 400s on create/update (reviewer observation).
