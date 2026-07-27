# platform-mcp sidecar with ask_human, checkpoints, and notifications — Implementation Plan

> **SUPERSEDED — historical implementation record (not executable).** The approved [Agentic Workflows as Functions over Snapshots design](../../specs/2026-07-21-agentic-workflows-as-functions-design.md) and [Agentic Functions program plan](../2026-07-21-agentic-functions-program.md) are authoritative. The platform-mcp sidecar (ask_human, checkpoints, notifications) landed and remains live; the ticket-centric park/resume identity and PARK-V2 exit-and-respawn seam described below are historical.

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Ship the agent's mid-flight platform surface — `read_ticket`, `list_tasks`, `get_task`, `submit_spec`, `submit_plan`, `update_task_status`, `ask_human` — as the platform-mcp sidecar, with park/resume over a long-poll question API, checkpoint-gate execution on the same primitive, a polling-backed webhook notification channel plus ticket-page banner, contract tests, and a live theborg restart-while-parked proof. **PARK-V2 (2026-07-10 frozen seam delta):** plus this plan's sidecar/questions halves of exit-and-respawn — the short-park threshold timer with the `flight/park.json` sentinel, checkpoint `202 {"parked": true}`/exit-3, DB-enforced idempotency-by-question (find-or-create with the answered-row resume fast path), the answer-route dispatcher notify, and the "Awaiting human" chip (Tasks 5b, 6b, 9c, 11b, 14c, 17b).

**Read model (default = MCP):** the agent reaches spec/plan ONLY through the platform-mcp read tools (`read_ticket` → envelope + spec; `list_tasks` → the cheap task skeleton; `get_task` → one task's detail). No spec/plan bytes are injected into any agent step by default — the DB stays the single source of truth and nothing is flattened into a monolithic markdown blob. The `files` opt-in (rendered read-only `spec.md`/`plan.md` mounted as the `ticket` artifact) is owned by dispatch's renderer (workflow field `spec_delivery`, wave 4); this plan implements only the MCP read tools, which stay mounted and functional in BOTH delivery modes.

**Architecture:** A new `agent_run_questions` table + factory + four ATC routes (ask / list / get-long-poll / answer) carry the HITL state; the sidecar (`agent/platformmcp`, binary `cmd/platform-mcp`, image `ghcr.io/tdmtrader/mcp-platform`) is an MCP streamable-HTTP server **with SSE progress heartbeats** (Task 9b upgrades the shared `atc/api/mcpserver` in place — mandatory, per the 2026-07-09 SSE seam delta/F13: the claude CLI silently abandons a progress-free buffered tools/call at exactly 60s, so a parked `ask_human` can NEVER deliver its answer without <60s heartbeats) that translates the seven tools into principal-authed calls against ticket-core's routes and the new question routes, parking by blocking the MCP call over a resilient long-poll. The long-poll client treats transport/5xx errors as retry-forever but N consecutive 401/403s as fatal (F31 leg 3), so an expired/revoked principal fails loudly instead of parking forever. The three read tools (`read_ticket`/`list_tasks`/`get_task`) all resolve against ticket-core's `GetAgentTicket` route, which embeds spec + tasks server-side (backed by Store `Get`/`LatestSpec`/`ActivePlan`); the sidecar projects that one payload into the three typed results, dropping tasks from `read_ticket`. Notifications are delivered by a polling RunnableComponent (`agent_notifier`) that POSTs a generic webhook (§8.4) — never notify-only, per the fork's lossy-NOTIFY lesson.

**PARK-V2 layer (2026-07-10 frozen seam delta; amends the 2026-07-09 SSE/park entries WITHOUT retracting them):** the SSE park above is the SHORT-PARK mechanism only. Past `--agent-short-park-max` (default 30m; rendered into the sidecar as `PLATFORM_MCP_SHORT_PARK_MAX_SECONDS`; `0` = never exit — the pure-PARK-V1 rollback hatch) the sidecar signals exit-and-respawn instead of holding the call: for `ask_human` it atomically writes the `flight/park.json` sentinel (temp + rename; the agent-runner's 5s stat loop sees it, SIGTERMs claude, and exits 86 — plan 07's side) and expects the client disconnect; for `/checkpoint` it answers the blocked POST `202 {"parked": true}` and the checkpoint client exits with frozen code 3. In both cases the question row STAYS OPEN — it is the durable representation of the wait and the authority the platform resumes on (never the build status). `ask_human`/`/checkpoint` become idempotent-by-question (DB-enforced find-or-create on `(pipeline_run_id, step_name, kind, question_hash)`, migration `1773106072`), so a continuation build's re-issued call returns the answered row immediately — no park, no SSE wait. The `awaiting_human` run status, the runner watch/exit-86, `agent_run_step_state`, and dispatch's `reconcileAwaitingRuns` are owned by plans 03/07/11; this plan owns only the sidecar/questions halves (Tasks 5b, 6b, 9c, 11b, 14c, 17b).

**Tech Stack:** Go (main module), squirrel/psql + counterfeiter in `atc/db`, `atc/api/mcpserver` for the MCP protocol, plain-Go httptest tests in `agent/*`, Ginkgo in `atc/db`/`atc/api`/`atc/wrappa`, Elm 0.19 (`web/elm`) for the banner, plain-Go `//go:build live` tests against theborg for park/resume.

---

## Context

**Charter (workstreams.json `platform-mcp-hitl`, size L, wave 3).** Scope-in items and where they land in this plan:

| scope_in item | Tasks |
|---|---|
| platform-mcp sidecar server, packaged per dev-mcp's image convention, authenticating as a scoped agent principal bound to the ticket/run | 9, 10, 13, 16 |
| Schema-constrained submit_spec/submit_plan/update_task_status through ticket-core's mutation path; read_ticket/list_tasks/get_task; live task progress on the ticket page | 10 (write tools go through ticket-core's routes; the read tools project the `GetAgentTicket` payload — `read_ticket` = envelope + spec, `list_tasks` = task skeleton, `get_task` = one task's detail; the ticket page's task list — landed in wave 2 — renders the rows they write) |
| ask_human: park via supervisor wait semantics, question+options on the ticket page, resume on answer, parked step counts as running, per-workflow timeout policy from rendered step config (env) | 2, 3, 4, 5, 6, 9, 9b, 11, 17 |
| OWNS checkpoint-gate execution via the same park/resume primitive | 14, 14b |
| Concrete notification mechanism: ticket-page banner + one real channel, polling-backed | 7, 8, 17 |
| Contract tests for the platform-mcp interface | 15 |
| Live theborg test: restart-while-parked | 18 (+ 18b: the mandatory real-CLI >5-minute park pin — the FIRST wave-3 deliverable; gates 10 Task 7 merge) |
| PARK-V2 sidecar/questions halves (2026-07-10 frozen delta §A/§B/§D/§E/§H): threshold config, `flight/park.json` sentinel, checkpoint 202/exit-3, idempotency-by-question + resume fast paths, answer→dispatcher notify, awaiting-human chip | 5b, 6b, 9c, 11b, 14c, 17b |

Scope-out (must NOT appear in this plan): push/publish/archive mechanics (harvest-step), `request_review`/`ask_agent` (gateway-mcp), rendering checkpoint declarations into pipelines (dispatch's renderer, wave 4); and the PARK-V2 halves owned elsewhere — the agent-runner's sentinel watch/SIGTERM/exit-86, stream-json teeing, and `flight/session.jsonl` capture (agent-step, plan 07), `agent_run_step_state` + continuation replay/resume semantics (agent-step), the `awaiting_human` run status with lifecycler entry/exit and the revised `--agent-park-timeout` wall clock (pipeline-runs, plan 03), and `reconcileAwaitingRuns` + principal/secret re-mint + the continuation build (dispatch, plan 11).

**Assumed landed (waves 1–2, per `00-shared-contracts.md`):**
- **agent-identity:** `agent_principals` (§1.2), `cap1.` tokens, `auth.CheckAgentPrincipalHandler(handler, rejector, scope)` wrappa tier, the `CheckAgentAuthorizationHandler` main-team wrapper for team-less `/api/v1/agent/*` authorized routes (§4.2 closing paragraph, decision 21), scope vocabulary incl. `tickets:read`, `tickets:write`, `questions:answer` (§4.1).
- **ticket-core:** ticket tables (§1.7), `agent/api/tickets` types + `tickets.Store` with `Transition` single-writer (§2.1), routes `GetAgentTicket`/`SubmitAgentTicketSpec`/`SubmitAgentTicketPlan`/`UpdateAgentTicketTask` (§4.2), the Elm ticket page with live task list.
- **agent-step:** `agent/schema` extracted as a nested stdlib-only Go module with `replace` entries in the root and ci-agent `go.mod`s (conventions bullet 2), §5 event constants for the types agent-step emits, `agent_run_metrics` + ingest route, proven sidecar wiring/env contract (§8.1: `ATC_EXTERNAL_URL`, `AGENT_PRINCIPAL_TOKEN`, `AGENT_TICKET_ID`, `PLATFORM_MCP_URL=http://127.0.0.1:7781/mcp`, `PLATFORM_MCP_ASK_TIMEOUT_POLICY`/`_SECONDS`).
- **dev-mcp:** sidecar image packaging convention + the first CI image-build job as copyable template (§8.5).
- **pipeline-runs:** the parked-run contract (§1.5): a parked step keeps its build `started`, so a parked run counts as `running`. This plan relies on it by construction — `ask_human` blocks the step's tool call, nothing else needed. *[AMENDED by the PARK-V2 delta, 2026-07-10: this holds only BELOW `--agent-short-park-max`. Past the threshold the step exits (agent-runner 86 / checkpoint client 3), the build finishes `failed`-as-carrier (§B5 — no fifth build status), and the run enters the non-terminal `awaiting_human` status (pipeline-runs migration `1773106032`, plan 03), keyed off this plan's OPEN park-policy question rows. `--agent-park-timeout` (72h default) is REVISED to bound the `awaiting_human` wall clock — the lifecycler errors a run whose OLDEST open park question exceeds `asked_at + park_timeout`, releasing the rows via `Answer(id, "", "platform")` — rather than bounding a live in-pod park. This plan's sidecar owns only the threshold timer + exit signals; run-state entry/exit is the lifecycler's and resume is dispatch's `reconcileAwaitingRuns` (plan 11 Task 11c).]*
- **workflow-store:** `hitl:` block in the definition YAML (§6) — consumed indirectly: the renderer (wave 4) turns it into the sidecar env vars this plan reads.

**Contract surfaces this plan PRODUCES** (00-shared-contracts.md sections): §1.9 `agent_run_questions` (incl. the PARK-V2 `question_hash` + dedup index, migration `1773106072`); §3.2 platform-mcp tool schemas + park/resume protocol (incl. idempotency-by-question, the `/checkpoint` 202 response, and checkpoint-client exit 3); §4.2 rows `AskAgentQuestion`/`GetAgentQuestion`/`AnswerAgentQuestion` (Ask is find-or-create; Answer fires the dispatcher notify); §8.4 notification channel. **CONSUMES:** §1.7/§2.1/§4.2 (ticket routes + Store), §4.1 (principal scopes/handler), §5 (flight-recorder events), §8.1 (env contract), §8.5 (image packaging), §1.5 (parked-run contract).

**Known contract gaps this plan closes via a signed addendum (Task 1):** the §4.2 route table has no *list* route for a ticket's questions (the banner needs one); `agent_run_questions` needs a `notified_at` column for the polling notifier; the checkpoint "internal endpoint" (§3.2) has no concrete path/CLI shape; the sidecar's event-log path env var is not in §8.1.

**Execution-time anchor warning:** `atc/routes.go`, `api_auth_wrappa.go`, `reject_archived_wrappa.go`, `auditor.go`, `roles.go`, `atc/api/handler.go`, `atc/atccmd/command.go`, and `web/elm` will all have been extended by waves 1–2. Line anchors below are verified against today's `jetbridge` HEAD (`dab0f2c6e2`) and will have drifted — anchor to the named neighbors, not the raw numbers.

---

### Task 1: Landed-seam survey + shared-contracts addendum

**Files:**
- Modify: `docs/superpowers/plans/agentic-platform/00-shared-contracts.md` (append to §11 Amendment log, currently ends at line 1471)

- [ ] Survey the landed wave-1/2 seams and capture the results (paste command output into the addendum's survey block below):

```bash
# principal wrappa helpers (agent-identity) + the combined principal-or-main-team helper (ticket-core, wave 2).
# The combined helper is named AgentPrincipalOrMainTeamHandler (contains neither "CheckAgentPrincipal"
# nor "CheckAgentAuthorization"), so it MUST be in the alternation or the survey misses it — Task 6 needs it.
grep -n "CheckAgentPrincipal\|CheckAgentAuthorization\|AgentPrincipalOrMainTeam\|func.*HandlerFor" atc/api/auth/*.go atc/wrappa/api_auth_wrappa.go
# ticket-core routes + the wrappa case groups my routes must join
grep -n "AgentTicket" atc/routes.go atc/wrappa/api_auth_wrappa.go atc/api/accessor/roles.go
# GetAgentTicket response shape: does it embed spec + tasks?
grep -n "func (h \*Handler) GetTicket\|\"spec\"\|\"tasks\"" agent/api/tickets/handler.go
# agent/schema nested module + which §5 event constants agent-step already added
ls agent/schema/go.mod && grep -n "EventType = " agent/schema/event.go
# ticket page Elm module path + its fetch/poll pattern
find web/elm/src -iname "*Ticket*"
# dev-mcp packaging template: Dockerfile + CI job location
ls deploy/Dockerfile.* && grep -rn "mcp-dev" ci/ deploy/ 2>/dev/null
```

- [ ] Append this addendum (with the survey results filled in) to §11 of `00-shared-contracts.md`:

```markdown
- 2026-07-XX (platform-mcp-hitl wave-3 addendum; owner: platform-mcp-hitl):
  - §1.9: `agent_run_questions` gains `notified_at TIMESTAMPTZ` (NULL = webhook not yet delivered) via migration `1773106071`, plus partial index `agent_run_questions_unnotified ON agent_run_questions (asked_at) WHERE notified_at IS NULL`. Additive; consumed only by the notifier component.
  - §4.2: new route `ListAgentTicketQuestions` — GET `/api/v1/agent/tickets/:ticket_id/questions` (`?open=true` filters unanswered) — auth `authorized viewer; also principal(tickets:read)`. The frozen table omitted a list route; the ticket-page banner requires one.
  - §8.4: the notifier is component `atc.ComponentAgentNotifier` ("agent_notifier"), polling every 10s (never notify-only), gated on web flag `--agent-notify-webhook-url`; it delivers question/checkpoint notifications and marks rows via `notified_at`. Ticket-state and budget notification kinds are emitted by their owners calling `agent/notify.Notifier` when those workstreams land.
  - §3.2 read-model (FROZEN DELTA — supersedes the prior "rendered spec.md/plan.md via `AGENT_SPEC_MD`/`AGENT_PLAN_MD`" design): agents reach spec/plan ONLY through granular platform-mcp read tools; no spec/plan bytes are injected into any agent step by default. `read_ticket` returns envelope + spec ONLY (input `{}`; tasks REMOVED). Two new read tools: `list_tasks` (input `{}` → `{"tasks":[{"ordering","title","status"}]}`, the cheap skeleton, no detail bodies) and `get_task` (input `{"ordering":int}` → `{"ordering","title","status","detail_md"}`, unknown ordering → MCP tool error `isError=true` — the shared `atc/api/mcpserver` surfaces every handler error as a `tools/call` result with `isError=true`, NOT a JSON-RPC `-32602` object). `update_task_status` is UNCHANGED and remains the write-back in both delivery modes. All three read tools back onto ticket-core Store `Get`/`LatestSpec`/`ActivePlan` (here via the embedding `GetAgentTicket` route). The optional `files` delivery mode (workflow field `spec_delivery: mcp|files`, default `mcp`; owned by workflow-store §6 + dispatch's renderer, wave 4) materializes read-only `spec.md`/`plan.md` mounted as the `ticket` artifact — the platform-mcp read tools stay mounted and functional in BOTH modes. Affected workstreams: platform-mcp-hitl, dispatch, workflow-store, ticket-core-consumers.
  - §3.2: the checkpoint internal endpoint is `POST /checkpoint` on the sidecar's `MCP_LISTEN_ADDR` (same mux as `/mcp` and `/healthz`), body `{"name": "...", "description": "..."}`, blocking response `{"approved": bool, "answer": "...", "answered_by": "..."}`. Frozen response codes: **200** = resolved (body above); **400** = empty/missing `name` or invalid JSON; **502** = ATC transport error filing or awaiting the row (the sidecar KEEPS the per-name reservation so a client retry re-awaits the same open row). This bullet **SUPERSEDES** (not merely amends) the retracted §3.2/§11-F1/decision-12 client→ATC wording — the sentences saying the checkpoint client "inserts the row", "long-polls the ATC route", "reads reject-policy from argv", and is "NOT a call to a sidecar internal checkpoint endpoint" are RETRACTED per the 2026-07-09 checkpoint-seam delta (F14): the CLIENT talks ONLY to the pod-local sidecar over loopback HTTP and authenticates to nothing; the SIDECAR is the trust boundary and the only ATC caller. Checkpoint client mode: `platform-mcp checkpoint --name <n> [--description <d>]`, exit 0 approved / exit 1 rejected-or-error / exit 2 usage error. `on_reject: send_back` semantics live in dispatch's **run-completion reconciler** (plan 11 Task 11b) — NOT the renderer, which emits the identical bare failing TaskStep for both values; at the step level both values fail the step on reject.
  - §8.1: new row — `PLATFORM_MCP_EVENTS_PATH` | platform | literal | NDJSON event-log path for the sidecar's flight-recorder events (`human.ask`, `human.answer`, `checkpoint.*`); unset = stdout (pod logs).
  - §3.2 timeout resolution detail: when the sidecar resolves a timed-out question it sends `answered_by: "platform-mcp"` (the per-run principal *name* is not in the §8.1 env contract; if dispatch later adds `AGENT_PRINCIPAL_NAME`, the sidecar prefers it).
  - platform-mcp packaging (§8.5 instantiation): source `agent/platformmcp` (main module), binary `cmd/platform-mcp`, image `ghcr.io/tdmtrader/mcp-platform` from `deploy/Dockerfile.platform-mcp`.
  - §8.1 (PARK-V2 seam delta, 2026-07-10; producer corrected 2026-07-10 follow-up): new row — `PLATFORM_MCP_PARK_PATH` | platform | literal, set by the **agent-step exec** via `ContainerSpec.SidecarEnv["platform"]` (F15; plan 07 Task 26), agent steps only | absolute path of the §B1 park sentinel (`<flight mount>/park.json`); the exec is the producer because only it knows the flight artifact's mount path at container-spec time — dispatch's renderer cannot (F15: sidecar rows for exec-owned steps are populated programmatically by the owning exec; the renderer's only PARK-V2 row is `PLATFORM_MCP_SHORT_PARK_MAX_SECONDS`). The flight volume already reaches sidecars via jetbridge's `buildSidecarContainers` mount inheritance, so the path is valid inside the platform container. Unset = the sidecar never writes a sentinel — the legal checkpoint-pod shape, where the `202` response is the exit signal. (`PLATFORM_MCP_SHORT_PARK_MAX_SECONDS` itself is frozen by the PARK-V2 delta — rendered by dispatch from `--agent-short-park-max`, `0` = never exit; this plan consumes both, Task 9c.)
  - §1.9/§3.2 (PARK-V2 seam delta, 2026-07-10; decision 31): `ask_human`/`/checkpoint` are idempotent-by-question — `agent_run_questions` gains `question_hash` + UNIQUE partial index `agent_run_questions_dedup (pipeline_run_id, step_name, kind, question_hash) WHERE pipeline_run_id IS NOT NULL` (migration `1773106072`); `AskAgentQuestion` is FIND-OR-CREATE with the hash computed server-side (`hex(sha256(question || '\x00' || options-joined-by-'\x00'))`): an answered row is returned as-is (the resume fast path), an open row is joined. The `ask_human` tool description carries the vary-the-text note. `/checkpoint` gains frozen response `202 {"parked": true}` = parked-past-threshold, and the checkpoint client gains frozen exit code 3 = parked-past-threshold (0/1/2 unchanged). `AnswerAgentQuestion` additionally fires the dispatcher component notify (`agent_dispatcher` channel; polling remains the guaranteed resume path — never notify-only).
  - Landed-seam survey results (recorded at execution time): combined principal-or-main-team wrappa helper name = `<fill — expected `auth.AgentPrincipalOrMainTeamHandler(principalTier, mainTeamTier http.Handler) http.Handler` per ticket-core plan 06; two already-wrapped handler tiers, NOT `(handler, rejector, scope)`>`; principal-tier factory method = `<fill — expected `checkAgentPrincipalHandlerFactory.HandlerFor(delegate, rejector, scope)`>`; main-team tier = `<fill — expected `auth.CheckAgentAuthorizationHandler(handler, rejector)`>`; questions:answer scope constant = `<fill — expected `principals.ScopeQuestionsAnswer`>`; GetAgentTicket response embeds spec/tasks = `<yes/no>`; ticket page Elm module = `<path>`; dev-mcp CI job template lives at `<path/pipeline>`.
```

  The `<fill>` markers are filled by THIS task from the survey output before committing — they must not survive into the committed addendum.

- [ ] Commit:

```bash
git add docs/superpowers/plans/agentic-platform/00-shared-contracts.md
git commit -m "docs(agentic-platform): platform-mcp-hitl wave-3 contract addendum" -m "Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 2: `agent_run_questions` migrations (1773106070, 1773106071)

**Files:**
- Create: `atc/db/migration/migrations/1773106070_create_agent_run_questions.up.sql`
- Create: `atc/db/migration/migrations/1773106070_create_agent_run_questions.down.sql`
- Create: `atc/db/migration/migrations/1773106071_add_notified_at_to_agent_run_questions.up.sql`
- Create: `atc/db/migration/migrations/1773106071_add_notified_at_to_agent_run_questions.down.sql`

- [ ] Write `1773106070_create_agent_run_questions.up.sql` — DDL exactly per contracts §1.9:

```sql
CREATE TABLE agent_run_questions (
    id              SERIAL PRIMARY KEY,
    ticket_id       INTEGER NOT NULL,
    pipeline_run_id INTEGER,
    build_id        INTEGER NOT NULL DEFAULT 0,
    step_name       TEXT NOT NULL DEFAULT '',
    kind            TEXT NOT NULL DEFAULT 'question'
                    CHECK (kind IN ('question','checkpoint')),
    question        TEXT NOT NULL,
    options         JSONB NOT NULL DEFAULT '[]',
    timeout_policy  TEXT NOT NULL DEFAULT 'park'
                    CHECK (timeout_policy IN ('park','default','fail')),
    timeout_seconds INTEGER NOT NULL DEFAULT 0,
    default_answer  TEXT,
    asked_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    answered_at     TIMESTAMPTZ,
    answer          TEXT,
    answered_by     TEXT NOT NULL DEFAULT ''
);

CREATE INDEX agent_run_questions_open   ON agent_run_questions (ticket_id) WHERE answered_at IS NULL;
CREATE INDEX agent_run_questions_ticket ON agent_run_questions (ticket_id, asked_at DESC);
```

- [ ] Write `1773106070_create_agent_run_questions.down.sql`:

```sql
DROP TABLE agent_run_questions;
```

- [ ] Write `1773106071_add_notified_at_to_agent_run_questions.up.sql` (the Task-1 addendum column):

```sql
ALTER TABLE agent_run_questions ADD COLUMN notified_at TIMESTAMPTZ;

CREATE INDEX agent_run_questions_unnotified ON agent_run_questions (asked_at) WHERE notified_at IS NULL;
```

- [ ] Write `1773106071_add_notified_at_to_agent_run_questions.down.sql`:

```sql
DROP INDEX agent_run_questions_unnotified;
ALTER TABLE agent_run_questions DROP COLUMN notified_at;
```

- [ ] Verify the migration suite accepts them (PostgreSQL must be running — `pg_isready`):

```bash
ginkgo ./atc/db/migration/
```

Expected: suite green (the suite runs every embedded migration up; a SQL error here fails loudly).

- [ ] Commit:

```bash
git add atc/db/migration/migrations/1773106070_* atc/db/migration/migrations/1773106071_*
git commit -m "feat(atc): agent_run_questions table for HITL park/resume (1773106070-71)" -m "Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 3: `agent/api/questions` domain types, validation, Store interface, memory store

**Files:**
- Create: `agent/api/questions/types.go`
- Create: `agent/api/questions/memory_store.go`
- Test: `agent/api/questions/types_test.go`

- [ ] Write the failing test `agent/api/questions/types_test.go` (plain Go, mirroring `agent/api/reviews/types_test.go` style):

```go
package questions_test

import (
	"strings"
	"testing"

	"github.com/concourse/concourse/agent/api/questions"
)

func validAsk() *questions.Question {
	return &questions.Question{
		TicketID: 7,
		Kind:     questions.KindQuestion,
		Question: "Which auth flow should I extend?",
		Options:  []string{"legacy", "oidc"},
	}
}

func TestValidateAskDefaults(t *testing.T) {
	q := validAsk()
	if err := q.ValidateAsk(); err != nil {
		t.Fatalf("expected valid, got %v", err)
	}
	if q.TimeoutPolicy != questions.TimeoutPark {
		t.Fatalf("expected default policy park, got %q", q.TimeoutPolicy)
	}
}

func TestValidateAskRejects(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*questions.Question)
		substr string
	}{
		{"missing question", func(q *questions.Question) { q.Question = "" }, "question is required"},
		{"missing ticket", func(q *questions.Question) { q.TicketID = 0 }, "ticket_id is required"},
		{"bad kind", func(q *questions.Question) { q.Kind = "poll" }, "invalid kind"},
		{"bad policy", func(q *questions.Question) { q.TimeoutPolicy = "wait" }, "invalid timeout_policy"},
		{"negative timeout", func(q *questions.Question) { q.TimeoutSeconds = -1 }, "timeout_seconds"},
		{"default policy without default answer", func(q *questions.Question) {
			q.TimeoutPolicy = questions.TimeoutDefault
		}, "default_answer is required"},
		{"default answer not in options", func(q *questions.Question) {
			q.TimeoutPolicy = questions.TimeoutDefault
			q.DefaultAnswer = "gopher"
		}, "must be one of options"},
	}
	for _, c := range cases {
		q := validAsk()
		c.mutate(q)
		err := q.ValidateAsk()
		if err == nil || !strings.Contains(err.Error(), c.substr) {
			t.Fatalf("%s: expected error containing %q, got %v", c.name, c.substr, err)
		}
	}
}

func TestMemoryStoreAskAnswerLifecycle(t *testing.T) {
	s := questions.NewMemoryStore()
	q := validAsk()
	id, err := s.Ask(q)
	if err != nil || id == 0 {
		t.Fatalf("Ask: id=%d err=%v", id, err)
	}

	open, err := s.OpenForTicket(7)
	if err != nil || len(open) != 1 {
		t.Fatalf("OpenForTicket: %d %v", len(open), err)
	}

	if err := s.Answer(7, id, "oidc", "tdm"); err != nil {
		t.Fatalf("Answer: %v", err)
	}
	got, found, err := s.Get(7, id)
	if err != nil || !found {
		t.Fatalf("Get: found=%v err=%v", found, err)
	}
	if got.Answer != "oidc" || got.AnsweredBy != "tdm" || got.AnsweredAt == 0 {
		t.Fatalf("unexpected answered row: %+v", got)
	}

	if err := s.Answer(7, id, "legacy", "someone-else"); err != questions.ErrAlreadyAnswered {
		t.Fatalf("expected ErrAlreadyAnswered, got %v", err)
	}

	unnotified, err := s.Unnotified(10)
	if err != nil || len(unnotified) != 1 {
		t.Fatalf("Unnotified: %d %v", len(unnotified), err)
	}
	if err := s.MarkNotified(id); err != nil {
		t.Fatalf("MarkNotified: %v", err)
	}
	unnotified, _ = s.Unnotified(10)
	if len(unnotified) != 0 {
		t.Fatalf("expected 0 unnotified after MarkNotified, got %d", len(unnotified))
	}
}
```

- [ ] Run it to verify it fails:

```bash
go test ./agent/api/questions/
```

Expected failure: `no required module provides package .../agent/api/questions` (package does not exist yet).

- [ ] Write `agent/api/questions/types.go`:

```go
// Package questions is the HITL question/checkpoint domain: the rows behind
// platform-mcp's ask_human park/resume protocol (shared contracts §1.9/§3.2).
package questions

import (
	"errors"
	"fmt"
)

type Kind string

const (
	KindQuestion   Kind = "question"
	KindCheckpoint Kind = "checkpoint"
)

type TimeoutPolicy string

const (
	TimeoutPark    TimeoutPolicy = "park"
	TimeoutDefault TimeoutPolicy = "default"
	TimeoutFail    TimeoutPolicy = "fail"
)

// ErrAlreadyAnswered is returned by Store.Answer when answered_at is set —
// the first writer wins; callers surface 409.
var ErrAlreadyAnswered = errors.New("question already answered")

// Question mirrors agent_run_questions (shared contracts §1.9 + the
// notified_at addendum). Timestamps are epoch seconds in JSON; zero = unset.
type Question struct {
	ID             int           `json:"id"`
	TicketID       int           `json:"ticket_id"`
	PipelineRunID  *int          `json:"pipeline_run_id,omitempty"`
	BuildID        int           `json:"build_id"`
	StepName       string        `json:"step_name"`
	Kind           Kind          `json:"kind"`
	Question       string        `json:"question"`
	Options        []string      `json:"options"`
	TimeoutPolicy  TimeoutPolicy `json:"timeout_policy"`
	TimeoutSeconds int           `json:"timeout_seconds"`
	DefaultAnswer  string        `json:"default_answer,omitempty"`
	AskedAt        int64         `json:"asked_at"`
	AnsweredAt     int64         `json:"answered_at,omitempty"`
	Answer         string        `json:"answer,omitempty"`
	AnsweredBy     string        `json:"answered_by,omitempty"`
	NotifiedAt     int64         `json:"notified_at,omitempty"`
}

// ValidateAsk validates a to-be-inserted question and fills defaults
// (kind=question, timeout_policy=park). It enforces §3.2's default-answer
// rules at the API boundary as well as in the sidecar.
func (q *Question) ValidateAsk() error {
	if q.TicketID <= 0 {
		return fmt.Errorf("ticket_id is required")
	}
	if q.Question == "" {
		return fmt.Errorf("question is required")
	}
	if q.Kind == "" {
		q.Kind = KindQuestion
	}
	if q.Kind != KindQuestion && q.Kind != KindCheckpoint {
		return fmt.Errorf("invalid kind %q", q.Kind)
	}
	if q.TimeoutPolicy == "" {
		q.TimeoutPolicy = TimeoutPark
	}
	switch q.TimeoutPolicy {
	case TimeoutPark, TimeoutDefault, TimeoutFail:
	default:
		return fmt.Errorf("invalid timeout_policy %q", q.TimeoutPolicy)
	}
	if q.TimeoutSeconds < 0 {
		return fmt.Errorf("timeout_seconds must be >= 0")
	}
	if q.TimeoutPolicy == TimeoutDefault && q.DefaultAnswer == "" {
		return fmt.Errorf("default_answer is required when timeout_policy is default")
	}
	if q.DefaultAnswer != "" && len(q.Options) > 0 && !contains(q.Options, q.DefaultAnswer) {
		return fmt.Errorf("default_answer must be one of options")
	}
	return nil
}

func contains(opts []string, s string) bool {
	for _, o := range opts {
		if o == s {
			return true
		}
	}
	return false
}

// Store is implemented by atc/db.NewAgentQuestionsFactory (SQL) and
// MemoryStore (tests, contracttest stub). Answer is guarded: only the first
// writer succeeds (ErrAlreadyAnswered otherwise) — this is what makes the
// sidecar's timeout resolution race-safe against a simultaneous human answer.
type Store interface {
	Ask(q *Question) (int, error)
	Get(ticketID, questionID int) (*Question, bool, error)
	Answer(ticketID, questionID int, answer, answeredBy string) error
	OpenForTicket(ticketID int) ([]Question, error)
	ListForTicket(ticketID, limit int) ([]Question, error)
	Unnotified(limit int) ([]Question, error)
	MarkNotified(questionID int) error
}
```

- [ ] Write `agent/api/questions/memory_store.go` (mirrors `agent/api/reviews/memory_store.go`):

```go
package questions

import (
	"sync"
	"time"
)

// MemoryStore is an in-memory Store for tests and the contracttest stub ATC.
type MemoryStore struct {
	mu     sync.Mutex
	nextID int
	rows   map[int]*Question
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{nextID: 1, rows: map[int]*Question{}}
}

func (m *MemoryStore) Ask(q *Question) (int, error) {
	if err := q.ValidateAsk(); err != nil {
		return 0, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := *q
	cp.ID = m.nextID
	cp.AskedAt = time.Now().Unix()
	m.nextID++
	m.rows[cp.ID] = &cp
	return cp.ID, nil
}

func (m *MemoryStore) Get(ticketID, questionID int) (*Question, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	row, ok := m.rows[questionID]
	if !ok || row.TicketID != ticketID {
		return nil, false, nil
	}
	cp := *row
	return &cp, true, nil
}

func (m *MemoryStore) Answer(ticketID, questionID int, answer, answeredBy string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	row, ok := m.rows[questionID]
	if !ok || row.TicketID != ticketID {
		return ErrNotFound
	}
	if row.AnsweredAt != 0 {
		return ErrAlreadyAnswered
	}
	row.Answer = answer
	row.AnsweredBy = answeredBy
	row.AnsweredAt = time.Now().Unix()
	return nil
}

func (m *MemoryStore) OpenForTicket(ticketID int) ([]Question, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []Question
	for _, row := range m.rows {
		if row.TicketID == ticketID && row.AnsweredAt == 0 {
			out = append(out, *row)
		}
	}
	return out, nil
}

func (m *MemoryStore) ListForTicket(ticketID, limit int) ([]Question, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []Question
	for _, row := range m.rows {
		if row.TicketID == ticketID {
			out = append(out, *row)
		}
		if limit > 0 && len(out) == limit {
			break
		}
	}
	return out, nil
}

func (m *MemoryStore) Unnotified(limit int) ([]Question, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []Question
	for _, row := range m.rows {
		if row.NotifiedAt == 0 {
			out = append(out, *row)
		}
		if limit > 0 && len(out) == limit {
			break
		}
	}
	return out, nil
}

func (m *MemoryStore) MarkNotified(questionID int) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	row, ok := m.rows[questionID]
	if !ok {
		return ErrNotFound
	}
	row.NotifiedAt = time.Now().Unix()
	return nil
}
```

  Also add to `types.go` (next to `ErrAlreadyAnswered`):

```go
// ErrNotFound is returned when a question id does not exist under the ticket.
var ErrNotFound = errors.New("question not found")
```

- [ ] Run to verify pass:

```bash
go test ./agent/api/questions/
```

- [ ] Commit:

```bash
git add agent/api/questions/
git commit -m "feat(agent): questions domain types, validation, Store + memory store" -m "Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 4: `atc/db` questions factory (SQL Store) + counterfeiter fake

**Files:**
- Create: `atc/db/agent_questions_factory.go`
- Test: `atc/db/agent_questions_factory_test.go` (Ginkgo, joins the existing `atc/db` suite)

- [ ] Write the failing Ginkgo spec `atc/db/agent_questions_factory_test.go` (follow `agent_reviews_factory` test conventions in the suite — `dbConn` is the suite-provided connection):

```go
package db_test

import (
	"fmt"

	"github.com/concourse/concourse/agent/api/questions"
	"github.com/concourse/concourse/atc/db"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("AgentQuestionsFactory", func() {
	var factory db.AgentQuestionsFactory

	BeforeEach(func() {
		factory = db.NewAgentQuestionsFactory(dbConn)
	})

	// Question text varies per ticket: the PARK-V2 §E dedup key
	// (pipeline_run_id, step_name, kind, question_hash) — Task 5b — excludes
	// ticket_id, and every row here shares run 42 + step "implement".
	newQuestion := func(ticketID int) *questions.Question {
		runID := 42
		return &questions.Question{
			TicketID:      ticketID,
			PipelineRunID: &runID,
			BuildID:       1001,
			StepName:      "implement",
			Kind:          questions.KindQuestion,
			Question:      fmt.Sprintf("ship it? (ticket %d)", ticketID),
			Options:       []string{"yes", "no"},
			TimeoutPolicy: questions.TimeoutPark,
		}
	}

	It("round-trips ask/get with options and epoch timestamps", func() {
		id, err := factory.Ask(newQuestion(9001))
		Expect(err).ToNot(HaveOccurred())
		Expect(id).To(BeNumerically(">", 0))

		got, found, err := factory.Get(9001, id)
		Expect(err).ToNot(HaveOccurred())
		Expect(found).To(BeTrue())
		Expect(got.Question).To(Equal("ship it? (ticket 9001)"))
		Expect(got.Options).To(Equal([]string{"yes", "no"}))
		Expect(got.StepName).To(Equal("implement"))
		Expect(*got.PipelineRunID).To(Equal(42))
		Expect(got.AskedAt).To(BeNumerically(">", 0))
		Expect(got.AnsweredAt).To(BeZero())
	})

	It("scopes Get by ticket id", func() {
		id, err := factory.Ask(newQuestion(9002))
		Expect(err).ToNot(HaveOccurred())
		_, found, err := factory.Get(999999, id)
		Expect(err).ToNot(HaveOccurred())
		Expect(found).To(BeFalse())
	})

	It("answers exactly once", func() {
		id, err := factory.Ask(newQuestion(9003))
		Expect(err).ToNot(HaveOccurred())

		Expect(factory.Answer(9003, id, "yes", "tdm")).To(Succeed())

		got, _, err := factory.Get(9003, id)
		Expect(err).ToNot(HaveOccurred())
		Expect(got.Answer).To(Equal("yes"))
		Expect(got.AnsweredBy).To(Equal("tdm"))
		Expect(got.AnsweredAt).To(BeNumerically(">", 0))

		Expect(factory.Answer(9003, id, "no", "late")).To(MatchError(questions.ErrAlreadyAnswered))
		Expect(factory.Answer(9003, 424242, "x", "y")).To(MatchError(questions.ErrNotFound))
	})

	It("lists open questions and the unnotified backlog", func() {
		id1, err := factory.Ask(newQuestion(9004))
		Expect(err).ToNot(HaveOccurred())
		// Distinct text: a byte-identical second ask would JOIN id1 once
		// Task 5b's find-or-create lands (same run/step/kind/hash).
		q2 := newQuestion(9004)
		q2.Question += " (redux)"
		id2, err := factory.Ask(q2)
		Expect(err).ToNot(HaveOccurred())
		Expect(factory.Answer(9004, id2, "yes", "tdm")).To(Succeed())

		open, err := factory.OpenForTicket(9004)
		Expect(err).ToNot(HaveOccurred())
		Expect(open).To(HaveLen(1))
		Expect(open[0].ID).To(Equal(id1))

		all, err := factory.ListForTicket(9004, 50)
		Expect(err).ToNot(HaveOccurred())
		Expect(all).To(HaveLen(2))

		unnotified, err := factory.Unnotified(100)
		Expect(err).ToNot(HaveOccurred())
		var ids []int
		for _, q := range unnotified {
			ids = append(ids, q.ID)
		}
		Expect(ids).To(ContainElements(id1, id2))

		Expect(factory.MarkNotified(id1)).To(Succeed())
		unnotified, err = factory.Unnotified(100)
		Expect(err).ToNot(HaveOccurred())
		ids = nil
		for _, q := range unnotified {
			ids = append(ids, q.ID)
		}
		Expect(ids).ToNot(ContainElement(id1))
	})
})
```

- [ ] Run to verify it fails (compile error — factory does not exist):

```bash
ginkgo ./atc/db/
```

Expected: build failure `undefined: db.AgentQuestionsFactory`. (Note CLAUDE.md: never `--race`; if `testdb_template already exists`, another run is live — wait.)

- [ ] Write `atc/db/agent_questions_factory.go` (squirrel + `ON CONFLICT`-free inserts; epoch scans match `agent_reviews_factory.go:65`):

```go
package db

import (
	"database/sql"
	"encoding/json"

	"github.com/concourse/concourse/agent/api/questions"
)

//counterfeiter:generate . AgentQuestionsFactory
type AgentQuestionsFactory interface {
	questions.Store
}

func NewAgentQuestionsFactory(conn DbConn) AgentQuestionsFactory {
	return &agentQuestionsFactory{conn: conn}
}

type agentQuestionsFactory struct {
	conn DbConn
}

func (f *agentQuestionsFactory) Ask(q *questions.Question) (int, error) {
	if err := q.ValidateAsk(); err != nil {
		return 0, err
	}
	optsJSON, err := json.Marshal(q.Options)
	if err != nil {
		return 0, err
	}
	var id int
	err = psql.Insert("agent_run_questions").
		Columns(
			"ticket_id", "pipeline_run_id", "build_id", "step_name", "kind",
			"question", "options", "timeout_policy", "timeout_seconds", "default_answer",
		).
		Values(
			q.TicketID, q.PipelineRunID, q.BuildID, q.StepName, string(q.Kind),
			q.Question, optsJSON, string(q.TimeoutPolicy), q.TimeoutSeconds, nullableString(q.DefaultAnswer),
		).
		Suffix("RETURNING id").
		RunWith(f.conn).
		QueryRow().
		Scan(&id)
	if err != nil {
		return 0, err
	}
	return id, nil
}

func nullableString(s string) any {
	if s == "" {
		return nil
	}
	return s
}

const questionColumns = `q.id, q.ticket_id, q.pipeline_run_id, q.build_id, q.step_name, q.kind,
	q.question, q.options, q.timeout_policy, q.timeout_seconds,
	COALESCE(q.default_answer, ''),
	EXTRACT(EPOCH FROM q.asked_at)::bigint,
	COALESCE(EXTRACT(EPOCH FROM q.answered_at)::bigint, 0),
	COALESCE(q.answer, ''), q.answered_by,
	COALESCE(EXTRACT(EPOCH FROM q.notified_at)::bigint, 0)`

func (f *agentQuestionsFactory) Get(ticketID, questionID int) (*questions.Question, bool, error) {
	rows, err := f.conn.Query(
		`SELECT `+questionColumns+` FROM agent_run_questions q WHERE q.id = $1 AND q.ticket_id = $2`,
		questionID, ticketID,
	)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	qs, err := scanQuestionRows(rows)
	if err != nil {
		return nil, false, err
	}
	if len(qs) == 0 {
		return nil, false, nil
	}
	return &qs[0], true, nil
}

func (f *agentQuestionsFactory) Answer(ticketID, questionID int, answer, answeredBy string) error {
	res, err := f.conn.Exec(
		`UPDATE agent_run_questions
		 SET answer = $1, answered_by = $2, answered_at = now()
		 WHERE id = $3 AND ticket_id = $4 AND answered_at IS NULL`,
		answer, answeredBy, questionID, ticketID,
	)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 1 {
		return nil
	}
	_, found, err := f.Get(ticketID, questionID)
	if err != nil {
		return err
	}
	if !found {
		return questions.ErrNotFound
	}
	return questions.ErrAlreadyAnswered
}

func (f *agentQuestionsFactory) OpenForTicket(ticketID int) ([]questions.Question, error) {
	rows, err := f.conn.Query(
		`SELECT `+questionColumns+` FROM agent_run_questions q
		 WHERE q.ticket_id = $1 AND q.answered_at IS NULL
		 ORDER BY q.asked_at ASC, q.id ASC`,
		ticketID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanQuestionRows(rows)
}

func (f *agentQuestionsFactory) ListForTicket(ticketID, limit int) ([]questions.Question, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := f.conn.Query(
		`SELECT `+questionColumns+` FROM agent_run_questions q
		 WHERE q.ticket_id = $1 ORDER BY q.asked_at DESC, q.id DESC LIMIT $2`,
		ticketID, limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanQuestionRows(rows)
}

func (f *agentQuestionsFactory) Unnotified(limit int) ([]questions.Question, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := f.conn.Query(
		`SELECT `+questionColumns+` FROM agent_run_questions q
		 WHERE q.notified_at IS NULL ORDER BY q.asked_at ASC LIMIT $1`,
		limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanQuestionRows(rows)
}

func (f *agentQuestionsFactory) MarkNotified(questionID int) error {
	_, err := f.conn.Exec(
		`UPDATE agent_run_questions SET notified_at = now() WHERE id = $1`,
		questionID,
	)
	return err
}

func scanQuestionRows(rows *sql.Rows) ([]questions.Question, error) {
	out := []questions.Question{}
	for rows.Next() {
		var q questions.Question
		var runID sql.NullInt64
		var optsJSON []byte
		var kind, policy string
		err := rows.Scan(
			&q.ID, &q.TicketID, &runID, &q.BuildID, &q.StepName, &kind,
			&q.Question, &optsJSON, &policy, &q.TimeoutSeconds,
			&q.DefaultAnswer, &q.AskedAt, &q.AnsweredAt,
			&q.Answer, &q.AnsweredBy, &q.NotifiedAt,
		)
		if err != nil {
			return nil, err
		}
		q.Kind = questions.Kind(kind)
		q.TimeoutPolicy = questions.TimeoutPolicy(policy)
		if runID.Valid {
			v := int(runID.Int64)
			q.PipelineRunID = &v
		}
		if err := json.Unmarshal(optsJSON, &q.Options); err != nil {
			return nil, err
		}
		out = append(out, q)
	}
	return out, rows.Err()
}
```

- [ ] Generate the counterfeiter fake (the `//go:generate` directive already exists in `atc/db/agent_reviews_factory.go:11`; the new `//counterfeiter:generate` rides it):

```bash
go generate ./atc/db/
git status --short atc/db/dbfakes/ | head   # expect fake_agent_questions_factory.go
```

- [ ] Run to verify pass:

```bash
ginkgo ./atc/db/
```

- [ ] Commit:

```bash
git add atc/db/agent_questions_factory.go atc/db/agent_questions_factory_test.go atc/db/dbfakes/fake_agent_questions_factory.go
git commit -m "feat(atc): agent questions SQL factory + counterfeiter fake" -m "Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 5: questions HTTP handler — ask, list, long-poll get, guarded answer

**Files:**
- Create: `agent/api/questions/handler.go`
- Test: `agent/api/questions/handler_test.go`

- [ ] Write the failing test `agent/api/questions/handler_test.go` (httptest style per `agent/api/feedback/handler_test.go`; rata params arrive as `:ticket_id` form values, per `agent/api/reviews/handler.go:119`):

```go
package questions_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/concourse/concourse/agent/api/questions"
)

func askBody(t *testing.T) *bytes.Reader {
	t.Helper()
	body, err := json.Marshal(map[string]any{
		"kind":            "question",
		"question":        "proceed?",
		"options":         []string{"yes", "no"},
		"timeout_policy":  "park",
		"timeout_seconds": 0,
		"build_id":        77,
		"step_name":       "implement",
	})
	if err != nil {
		t.Fatal(err)
	}
	return bytes.NewReader(body)
}

func doAsk(t *testing.T, h *questions.Handler) questions.Question {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/agent/tickets/12/questions", askBody(t))
	req.Form = url.Values{":ticket_id": {"12"}}
	w := httptest.NewRecorder()
	h.AskQuestion(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("ask: expected 201, got %d: %s", w.Code, w.Body.String())
	}
	var q questions.Question
	if err := json.Unmarshal(w.Body.Bytes(), &q); err != nil {
		t.Fatal(err)
	}
	return q
}

func TestAskAndListQuestions(t *testing.T) {
	store := questions.NewMemoryStore()
	h := questions.NewHandler(store)

	q := doAsk(t, h)
	if q.ID == 0 || q.TicketID != 12 || q.AskedAt == 0 {
		t.Fatalf("unexpected created question: %+v", q)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/agent/tickets/12/questions?open=true", nil)
	req.Form = url.Values{":ticket_id": {"12"}, "open": {"true"}}
	w := httptest.NewRecorder()
	h.ListQuestions(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("list: expected 200, got %d", w.Code)
	}
	var list []questions.Question
	if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].ID != q.ID {
		t.Fatalf("expected the open question, got %+v", list)
	}
}

func TestAskRejectsInvalid(t *testing.T) {
	h := questions.NewHandler(questions.NewMemoryStore())
	req := httptest.NewRequest(http.MethodPost, "/api/v1/agent/tickets/12/questions",
		strings.NewReader(`{"question": ""}`))
	req.Form = url.Values{":ticket_id": {"12"}}
	w := httptest.NewRecorder()
	h.AskQuestion(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
}

func TestAnswerOnceThenConflict(t *testing.T) {
	store := questions.NewMemoryStore()
	h := questions.NewHandler(store)
	q := doAsk(t, h)

	answer := func(body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPut,
			fmt.Sprintf("/api/v1/agent/tickets/12/questions/%d/answer", q.ID),
			strings.NewReader(body))
		req.Form = url.Values{":ticket_id": {"12"}, ":question_id": {fmt.Sprint(q.ID)}}
		w := httptest.NewRecorder()
		h.AnswerQuestion(w, req)
		return w
	}

	if w := answer(`{"answer": "maybe", "answered_by": "tdm"}`); w.Code != http.StatusBadRequest {
		t.Fatalf("off-options answer: expected 400, got %d", w.Code)
	}
	if w := answer(`{"answer": "yes", "answered_by": "tdm"}`); w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if w := answer(`{"answer": "no", "answered_by": "late"}`); w.Code != http.StatusConflict {
		t.Fatalf("expected 409 on second answer, got %d", w.Code)
	}
}

func TestGetQuestionLongPoll(t *testing.T) {
	store := questions.NewMemoryStore()
	h := questions.NewHandler(store)
	h.PollInterval = 10 * time.Millisecond
	q := doAsk(t, h)

	// Immediate GET (no wait) returns the unanswered row.
	get := func(wait string) (*httptest.ResponseRecorder, questions.Question) {
		target := fmt.Sprintf("/api/v1/agent/tickets/12/questions/%d", q.ID)
		req := httptest.NewRequest(http.MethodGet, target, nil)
		form := url.Values{":ticket_id": {"12"}, ":question_id": {fmt.Sprint(q.ID)}}
		if wait != "" {
			form.Set("wait", wait)
		}
		req.Form = form
		w := httptest.NewRecorder()
		h.GetQuestion(w, req)
		var got questions.Question
		_ = json.Unmarshal(w.Body.Bytes(), &got)
		return w, got
	}

	w, got := get("")
	if w.Code != http.StatusOK || got.AnsweredAt != 0 {
		t.Fatalf("immediate get: code=%d row=%+v", w.Code, got)
	}

	// Long-poll: answer arrives mid-wait and the request returns early.
	go func() {
		time.Sleep(30 * time.Millisecond)
		_ = store.Answer(12, q.ID, "yes", "tdm")
	}()
	start := time.Now()
	w, got = get("2s")
	if w.Code != http.StatusOK || got.Answer != "yes" {
		t.Fatalf("long-poll get: code=%d row=%+v", w.Code, got)
	}
	if time.Since(start) > time.Second {
		t.Fatalf("long-poll did not return early on answer")
	}
}
```

- [ ] Run to verify it fails:

```bash
go test ./agent/api/questions/
```

Expected failure: `undefined: questions.NewHandler`.

- [ ] Write `agent/api/questions/handler.go`:

```go
package questions

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"
)

// Handler serves the /api/v1/agent/tickets/:ticket_id/questions routes.
// GetQuestion implements the §3.2 long-poll leg of the park/resume protocol:
// it POLLS the store (never LISTEN/NOTIFY — fork lesson) every PollInterval
// until the row is answered or the requested wait elapses, then returns the
// row either way.
type Handler struct {
	store Store

	// PollInterval is exported for tests; production default 2s.
	PollInterval time.Duration
	// MaxWait caps the client-requested wait. Default 60s.
	MaxWait time.Duration
}

func NewHandler(store Store) *Handler {
	return &Handler{store: store, PollInterval: 2 * time.Second, MaxWait: 60 * time.Second}
}

func (h *Handler) ticketID(r *http.Request) (int, error) {
	return strconv.Atoi(r.FormValue(":ticket_id"))
}

// AskQuestion handles POST /api/v1/agent/tickets/:ticket_id/questions
// (auth: principal(tickets:write), wired in api_auth_wrappa).
func (h *Handler) AskQuestion(w http.ResponseWriter, r *http.Request) {
	ticketID, err := h.ticketID(r)
	if err != nil {
		http.Error(w, "invalid ticket id", http.StatusBadRequest)
		return
	}
	var q Question
	if err := json.NewDecoder(r.Body).Decode(&q); err != nil {
		http.Error(w, fmt.Sprintf("invalid JSON: %s", err), http.StatusBadRequest)
		return
	}
	q.TicketID = ticketID
	if err := q.ValidateAsk(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	id, err := h.store.Ask(&q)
	if err != nil {
		http.Error(w, "failed to store question", http.StatusInternalServerError)
		return
	}
	created, found, err := h.store.Get(ticketID, id)
	if err != nil || !found {
		http.Error(w, "failed to read back question", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(created)
}

// ListQuestions handles GET /api/v1/agent/tickets/:ticket_id/questions[?open=true]
// (auth: authorized viewer; also principal(tickets:read) — Task 1 addendum route).
func (h *Handler) ListQuestions(w http.ResponseWriter, r *http.Request) {
	ticketID, err := h.ticketID(r)
	if err != nil {
		http.Error(w, "invalid ticket id", http.StatusBadRequest)
		return
	}
	var list []Question
	if r.FormValue("open") == "true" {
		list, err = h.store.OpenForTicket(ticketID)
	} else {
		list, err = h.store.ListForTicket(ticketID, 50)
	}
	if err != nil {
		http.Error(w, "failed to list questions", http.StatusInternalServerError)
		return
	}
	if list == nil {
		list = []Question{}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(list)
}

// GetQuestion handles GET /api/v1/agent/tickets/:ticket_id/questions/:question_id?wait=30s
// (auth: principal(tickets:read); also authorized viewer).
func (h *Handler) GetQuestion(w http.ResponseWriter, r *http.Request) {
	ticketID, err := h.ticketID(r)
	if err != nil {
		http.Error(w, "invalid ticket id", http.StatusBadRequest)
		return
	}
	questionID, err := strconv.Atoi(r.FormValue(":question_id"))
	if err != nil {
		http.Error(w, "invalid question id", http.StatusBadRequest)
		return
	}

	var wait time.Duration
	if raw := r.FormValue("wait"); raw != "" {
		wait, err = time.ParseDuration(raw)
		if err != nil || wait < 0 {
			http.Error(w, "invalid wait duration", http.StatusBadRequest)
			return
		}
		if wait > h.MaxWait {
			wait = h.MaxWait
		}
	}

	deadline := time.Now().Add(wait)
	for {
		q, found, err := h.store.Get(ticketID, questionID)
		if err != nil {
			http.Error(w, "failed to read question", http.StatusInternalServerError)
			return
		}
		if !found {
			http.Error(w, "question not found", http.StatusNotFound)
			return
		}
		if q.AnsweredAt != 0 || time.Now().Add(h.PollInterval).After(deadline) {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(q)
			return
		}
		select {
		case <-r.Context().Done():
			return
		case <-time.After(h.PollInterval):
		}
	}
}

type answerRequest struct {
	Answer     string `json:"answer"`
	AnsweredBy string `json:"answered_by"`
}

// AnswerQuestion handles PUT /api/v1/agent/tickets/:ticket_id/questions/:question_id/answer
// (auth: authorized member; also principal(questions:answer) for the sidecar's
// timeout resolution, §3.2). Empty answer is legal ONLY as the fail-policy
// resolution; when options exist, a non-empty answer must be one of them.
func (h *Handler) AnswerQuestion(w http.ResponseWriter, r *http.Request) {
	ticketID, err := h.ticketID(r)
	if err != nil {
		http.Error(w, "invalid ticket id", http.StatusBadRequest)
		return
	}
	questionID, err := strconv.Atoi(r.FormValue(":question_id"))
	if err != nil {
		http.Error(w, "invalid question id", http.StatusBadRequest)
		return
	}
	var req answerRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, fmt.Sprintf("invalid JSON: %s", err), http.StatusBadRequest)
		return
	}
	if req.AnsweredBy == "" {
		http.Error(w, "answered_by is required", http.StatusBadRequest)
		return
	}

	q, found, err := h.store.Get(ticketID, questionID)
	if err != nil {
		http.Error(w, "failed to read question", http.StatusInternalServerError)
		return
	}
	if !found {
		http.Error(w, "question not found", http.StatusNotFound)
		return
	}
	if req.Answer != "" && len(q.Options) > 0 && !contains(q.Options, req.Answer) {
		http.Error(w, "answer must be one of the question's options", http.StatusBadRequest)
		return
	}

	err = h.store.Answer(ticketID, questionID, req.Answer, req.AnsweredBy)
	switch {
	case errors.Is(err, ErrAlreadyAnswered):
		http.Error(w, "question already answered", http.StatusConflict)
		return
	case errors.Is(err, ErrNotFound):
		http.Error(w, "question not found", http.StatusNotFound)
		return
	case err != nil:
		http.Error(w, "failed to store answer", http.StatusInternalServerError)
		return
	}

	updated, _, err := h.store.Get(ticketID, questionID)
	if err != nil {
		http.Error(w, "failed to read back answer", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(updated)
}
```

- [ ] Run to verify pass:

```bash
go test ./agent/api/questions/
```

- [ ] Commit:

```bash
git add agent/api/questions/handler.go agent/api/questions/handler_test.go
git commit -m "feat(agent): questions HTTP handler with long-poll get and guarded answer" -m "Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 5b: idempotent-by-question — migration 1773106072, server-side `question_hash`, find-or-create Ask (PARK-V2 §E, 2026-07-10)

**Files:**
- Create: `atc/db/migration/migrations/1773106072_add_question_hash_to_agent_run_questions.up.sql`
- Create: `atc/db/migration/migrations/1773106072_add_question_hash_to_agent_run_questions.down.sql`
- Modify: `agent/api/questions/types.go` (`QuestionHash` field, `ComputeQuestionHash`, `Ask` contract doc)
- Modify: `agent/api/questions/memory_store.go`, `agent/api/questions/handler.go`
- Modify: `atc/db/agent_questions_factory.go`
- Test: `agent/api/questions/types_test.go`, `agent/api/questions/handler_test.go`, `atc/db/agent_questions_factory_test.go` (extend)

**Why (PARK-V2 §E, frozen 2026-07-10; decision 31):** a continuation build runs a FRESH sidecar with an empty in-memory `ckOpen` map, so the dedup that makes a re-issued `ask_human` / re-POSTed `/checkpoint` join its original row must be DB-enforced. `Ask` becomes FIND-OR-CREATE on `(pipeline_run_id, step_name, kind, question_hash)`: an existing ANSWERED row is returned as-is — the caller sees `answered_at` set and resolves immediately, the resume fast path consumed by Tasks 11b/14c — and an existing OPEN row is joined (same id; a fresh sidecar parks on it and inherits the ORIGINAL park clock). Rows without a `pipeline_run_id` never dedup (partial-index scope). The key deliberately excludes `ticket_id` (a run belongs to one ticket). The hash is computed SERVER-SIDE by the ask route — client-sent values are overwritten — with the frozen formula `hex(sha256(question || '\x00' || options-joined-by-'\x00'))`; the factory also computes it when empty (defense in depth: run-scoped rows must never collide on `''` under the unique index). Migration and hash-computing factory land in ONE task so the index never coexists with `''`-hash inserts. The route keeps answering 201 for joins — the sidecar inspects the returned row's `answered_at`, not the status code.

- [ ] Write `1773106072_add_question_hash_to_agent_run_questions.up.sql`:

```sql
ALTER TABLE agent_run_questions ADD COLUMN question_hash TEXT NOT NULL DEFAULT '';

CREATE UNIQUE INDEX agent_run_questions_dedup
    ON agent_run_questions (pipeline_run_id, step_name, kind, question_hash)
    WHERE pipeline_run_id IS NOT NULL;
```

- [ ] Write `1773106072_add_question_hash_to_agent_run_questions.down.sql`:

```sql
DROP INDEX agent_run_questions_dedup;
ALTER TABLE agent_run_questions DROP COLUMN question_hash;
```

- [ ] Verify the migration suite accepts them:

```bash
ginkgo ./atc/db/migration/
```

- [ ] Write the failing tests. Append to `agent/api/questions/types_test.go` (add `"crypto/sha256"` and `"encoding/hex"` to its imports):

```go
func TestComputeQuestionHash(t *testing.T) {
	// Frozen §E formula: hex(sha256(question || '\x00' || options joined by '\x00')).
	want := sha256.Sum256([]byte("Which auth flow?\x00legacy\x00oidc"))
	got := questions.ComputeQuestionHash("Which auth flow?", []string{"legacy", "oidc"})
	if got != hex.EncodeToString(want[:]) {
		t.Fatalf("hash mismatch: %s", got)
	}
	// No options: the '\x00' separator is still appended (frozen byte layout).
	wantFree := sha256.Sum256([]byte("why?\x00"))
	if questions.ComputeQuestionHash("why?", nil) != hex.EncodeToString(wantFree[:]) {
		t.Fatal("free-text hash must be sha256(question + NUL)")
	}
}

// TestAskFindOrCreate (PARK-V2 §E): new → creates; open → joins (same id,
// still one open row); answered → returned as-is (the resume fast path);
// different text or a nil pipeline_run_id → a fresh row.
func TestAskFindOrCreate(t *testing.T) {
	store := questions.NewMemoryStore()
	runID := 7
	newQ := func(text string) *questions.Question {
		return &questions.Question{
			TicketID: 42, PipelineRunID: &runID, StepName: "implement",
			Kind: questions.KindQuestion, Question: text,
			Options: []string{"legacy", "oidc"},
		}
	}

	first, err := store.Ask(newQ("Which auth flow?"))
	if err != nil {
		t.Fatal(err)
	}

	joined, err := store.Ask(newQ("Which auth flow?"))
	if err != nil || joined != first {
		t.Fatalf("expected to join open row %d, got %d (err %v)", first, joined, err)
	}
	open, _ := store.OpenForTicket(42)
	if len(open) != 1 {
		t.Fatalf("expected exactly 1 open row after the join, got %d", len(open))
	}

	if err := store.Answer(42, first, "oidc", "tdm"); err != nil {
		t.Fatal(err)
	}
	again, err := store.Ask(newQ("Which auth flow?"))
	if err != nil || again != first {
		t.Fatalf("expected the answered row %d back, got %d (err %v)", first, again, err)
	}
	got, _, _ := store.Get(42, again)
	if got.Answer != "oidc" || got.AnsweredAt == 0 {
		t.Fatalf("expected the answered row as-is, got %+v", got)
	}

	fresh, err := store.Ask(newQ("Which auth flow? (take 2)"))
	if err != nil || fresh == first {
		t.Fatalf("different text must file a fresh row, got %d (err %v)", fresh, err)
	}

	q := newQ("Which auth flow?")
	q.PipelineRunID = nil
	loose, err := store.Ask(q)
	if err != nil || loose == first {
		t.Fatalf("nil pipeline_run_id must never dedup, got %d (err %v)", loose, err)
	}
}
```

  Append to `agent/api/questions/handler_test.go`:

```go
// TestAskRouteIdempotentByQuestion (PARK-V2 §E): the route computes
// question_hash SERVER-SIDE and Ask is find-or-create — a byte-identical
// re-ask from a continuation build's fresh sidecar returns the SAME row,
// already answered, so the resumed agent's re-issued tool call resolves
// immediately (no park).
func TestAskRouteIdempotentByQuestion(t *testing.T) {
	store := questions.NewMemoryStore()
	h := questions.NewHandler(store)

	ask := func() questions.Question {
		body := strings.NewReader(`{"question":"proceed?","options":["yes","no"],"pipeline_run_id":7,"step_name":"implement"}`)
		req := httptest.NewRequest(http.MethodPost, "/api/v1/agent/tickets/12/questions", body)
		req.Form = url.Values{":ticket_id": {"12"}}
		w := httptest.NewRecorder()
		h.AskQuestion(w, req)
		if w.Code != http.StatusCreated {
			t.Fatalf("ask: expected 201, got %d: %s", w.Code, w.Body.String())
		}
		var q questions.Question
		if err := json.Unmarshal(w.Body.Bytes(), &q); err != nil {
			t.Fatal(err)
		}
		return q
	}

	first := ask()
	if first.QuestionHash == "" {
		t.Fatal("route must compute question_hash server-side")
	}
	if second := ask(); second.ID != first.ID || second.AnsweredAt != 0 {
		t.Fatalf("expected to join open row %d, got %+v", first.ID, second)
	}
	if err := store.Answer(12, first.ID, "yes", "tdm"); err != nil {
		t.Fatal(err)
	}
	resumed := ask()
	if resumed.ID != first.ID || resumed.Answer != "yes" || resumed.AnsweredAt == 0 {
		t.Fatalf("expected the answered row immediately, got %+v", resumed)
	}
}
```

- [ ] Run to verify they fail:

```bash
go test ./agent/api/questions/
```

Expected failures: `undefined: questions.ComputeQuestionHash`; the find-or-create tests create duplicate rows against the pre-delta stores.

- [ ] Implement the domain side. In `agent/api/questions/types.go`, add `"crypto/sha256"`, `"encoding/hex"`, `"strings"` to the imports, add the field after `Options` in the `Question` struct:

```go
	QuestionHash   string        `json:"question_hash,omitempty"`
```

  add next to `ValidateAsk`:

```go
// ComputeQuestionHash is the frozen PARK-V2 §E dedup hash over the question
// content: hex(sha256(question || '\x00' || options joined by '\x00')).
// Computed SERVER-SIDE by the ask route (client-sent values are overwritten);
// run-scoped rows are unique on (pipeline_run_id, step_name, kind,
// question_hash) — migration 1773106072.
func ComputeQuestionHash(question string, options []string) string {
	sum := sha256.Sum256([]byte(question + "\x00" + strings.Join(options, "\x00")))
	return hex.EncodeToString(sum[:])
}
```

  and replace the `Ask(q *Question) (int, error)` line in the `Store` interface with:

```go
	// Ask is FIND-OR-CREATE (PARK-V2 §E): when q carries a pipeline_run_id,
	// an existing row with the same (pipeline_run_id, step_name, kind,
	// question_hash) is returned instead of inserting — answered or open
	// alike. Callers detect the resume fast path via the returned row's
	// answered_at. Implementations compute QuestionHash when empty.
	Ask(q *Question) (int, error)
```

- [ ] Replace `MemoryStore.Ask` in `agent/api/questions/memory_store.go`:

```go
func (m *MemoryStore) Ask(q *Question) (int, error) {
	if err := q.ValidateAsk(); err != nil {
		return 0, err
	}
	if q.QuestionHash == "" {
		q.QuestionHash = ComputeQuestionHash(q.Question, q.Options)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if q.PipelineRunID != nil {
		// FIND-OR-CREATE (PARK-V2 §E): answered or open, the existing row wins.
		for _, row := range m.rows {
			if row.PipelineRunID != nil && *row.PipelineRunID == *q.PipelineRunID &&
				row.StepName == q.StepName && row.Kind == q.Kind &&
				row.QuestionHash == q.QuestionHash {
				return row.ID, nil
			}
		}
	}
	cp := *q
	cp.ID = m.nextID
	cp.AskedAt = time.Now().Unix()
	m.nextID++
	m.rows[cp.ID] = &cp
	return cp.ID, nil
}
```

- [ ] In `agent/api/questions/handler.go`'s `AskQuestion`, directly after the `q.ValidateAsk()` check:

```go
	// PARK-V2 §E: the hash is computed server-side — never trusted from the
	// client — and makes Ask find-or-create for run-scoped questions. Joins
	// still answer 201; callers read the row's answered_at, not the code.
	q.QuestionHash = ComputeQuestionHash(q.Question, q.Options)
```

- [ ] Run to verify the agent side passes:

```bash
go test ./agent/api/questions/
```

- [ ] Extend the SQL side. Append to the `atc/db/agent_questions_factory_test.go` Describe block:

```go
	It("find-or-creates on (pipeline_run_id, step_name, kind, question_hash) — PARK-V2 §E", func() {
		runID := 777
		ask := func() (int, *questions.Question) {
			q := &questions.Question{
				TicketID: 9005, PipelineRunID: &runID, StepName: "implement",
				Kind: questions.KindQuestion, Question: "resume me?",
				Options: []string{"yes", "no"},
			}
			id, err := factory.Ask(q)
			Expect(err).ToNot(HaveOccurred())
			got, found, err := factory.Get(9005, id)
			Expect(err).ToNot(HaveOccurred())
			Expect(found).To(BeTrue())
			return id, got
		}

		first, row := ask()
		Expect(row.QuestionHash).ToNot(BeEmpty())

		joined, _ := ask()
		Expect(joined).To(Equal(first))
		open, err := factory.OpenForTicket(9005)
		Expect(err).ToNot(HaveOccurred())
		Expect(open).To(HaveLen(1))

		Expect(factory.Answer(9005, first, "yes", "tdm")).To(Succeed())
		resumed, answeredRow := ask()
		Expect(resumed).To(Equal(first))
		Expect(answeredRow.Answer).To(Equal("yes"))
		Expect(answeredRow.AnsweredAt).To(BeNumerically(">", 0))
	})
```

- [ ] Implement in `atc/db/agent_questions_factory.go`: add `"errors"` to the imports, append `q.question_hash` to the `questionColumns` constant (after the `notified_at` line) with `&q.QuestionHash` added last in `scanQuestionRows`'s `Scan`, and replace `Ask` with:

```go
func (f *agentQuestionsFactory) Ask(q *questions.Question) (int, error) {
	if err := q.ValidateAsk(); err != nil {
		return 0, err
	}
	if q.QuestionHash == "" {
		// Defense in depth: the ask route computes this, but EVERY insert must
		// carry a real hash or run-scoped rows would collide on '' under
		// agent_run_questions_dedup (migration 1773106072).
		q.QuestionHash = questions.ComputeQuestionHash(q.Question, q.Options)
	}
	optsJSON, err := json.Marshal(q.Options)
	if err != nil {
		return 0, err
	}
	insert := psql.Insert("agent_run_questions").
		Columns(
			"ticket_id", "pipeline_run_id", "build_id", "step_name", "kind",
			"question", "options", "timeout_policy", "timeout_seconds", "default_answer",
			"question_hash",
		).
		Values(
			q.TicketID, q.PipelineRunID, q.BuildID, q.StepName, string(q.Kind),
			q.Question, optsJSON, string(q.TimeoutPolicy), q.TimeoutSeconds, nullableString(q.DefaultAnswer),
			q.QuestionHash,
		)
	if q.PipelineRunID != nil {
		// FIND-OR-CREATE (PARK-V2 §E): keep the existing row — answered or
		// open — and return its id. DO NOTHING + re-select is the race-safe
		// two-step: a concurrent inserter of the same key makes exactly one
		// row and both callers get its id.
		insert = insert.Suffix(`ON CONFLICT (pipeline_run_id, step_name, kind, question_hash)
			WHERE pipeline_run_id IS NOT NULL
			DO NOTHING
			RETURNING id`)
	} else {
		insert = insert.Suffix("RETURNING id")
	}
	var id int
	err = insert.RunWith(f.conn).QueryRow().Scan(&id)
	if errors.Is(err, sql.ErrNoRows) && q.PipelineRunID != nil {
		err = f.conn.QueryRow(
			`SELECT id FROM agent_run_questions
			 WHERE pipeline_run_id = $1 AND step_name = $2 AND kind = $3 AND question_hash = $4`,
			*q.PipelineRunID, q.StepName, string(q.Kind), q.QuestionHash,
		).Scan(&id)
	}
	if err != nil {
		return 0, err
	}
	return id, nil
}
```

  (No `go generate` needed: the `questions.Store` method set is unchanged, so the counterfeiter fake stands.)

- [ ] Update the `ask_human` tool description in `agent/platformmcp/tools.go` if Task 10 has already landed at execution time (the Task 10 block in this plan already carries the amended text): the description must end with the §E vary-the-text note.

- [ ] Run to verify pass:

```bash
go test ./agent/api/questions/ && ginkgo ./atc/db/migration/ && ginkgo ./atc/db/
```

- [ ] Commit:

```bash
git add atc/db/migration/migrations/1773106072_* agent/api/questions/ atc/db/agent_questions_factory.go atc/db/agent_questions_factory_test.go
git commit -m "feat(agent): idempotent-by-question find-or-create + question_hash dedup (PARK-V2 §E, 1773106072)" -m "Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 6: ATC route registration — routes, auth wrappa, archived wrappa, auditor, roles, handler wiring

**Files:**
- Modify: `atc/routes.go` (constants block ends at `ListTeamAgentReviews`, line 129 today; route table agent entries at lines 254–262)
- Modify: `atc/wrappa/api_auth_wrappa.go` (exhaustive switch; agent entries at lines 169–174; `default:` panic at line 181)
- Modify: `atc/wrappa/reject_archived_wrappa.go` (no-op case list ends line 137; `default:` panic at 139–140)
- Modify: `atc/auditor/auditor.go` (`EnableSystemAuditLog` case list, lines 150–157; `default:` panic at 175–176)
- Modify: `atc/api/accessor/roles.go` (agent entries at lines 108–115)
- Modify: `atc/api/handler.go` (NewHandler params near line 90–92; server construction near 122; handler map agent entries at 269–277)
- Modify: `atc/api/api_suite_test.go` (NewHandler call, memory stores at lines 225–227)
- Modify: `atc/atccmd/command.go` (NewHandler call site near line 2296–2299)
- Test: existing `atc/api`, `atc/wrappa` Ginkgo suites (the exhaustive-switch panics ARE the test)

All four switches panic on unknown route names, so this task is compile-and-suite-driven; every anchor above will have shifted by ticket-core's wave-2 additions — anchor to the named neighbors.

- [ ] Add route constants in `atc/routes.go` after `ListTeamAgentReviews` (and after ticket-core's ticket constants):

```go
	AskAgentQuestion         = "AskAgentQuestion"
	ListAgentTicketQuestions = "ListAgentTicketQuestions"
	GetAgentQuestion         = "GetAgentQuestion"
	AnswerAgentQuestion      = "AnswerAgentQuestion"
```

- [ ] Add route entries in the `Routes` table next to the other `/api/v1/agent/` entries (order matters for rata only in that the more specific `/answer` path must not be shadowed — rata matches method+path so plain adjacency is fine):

```go
	{Path: "/api/v1/agent/tickets/:ticket_id/questions", Method: "POST", Name: AskAgentQuestion},
	{Path: "/api/v1/agent/tickets/:ticket_id/questions", Method: "GET", Name: ListAgentTicketQuestions},
	{Path: "/api/v1/agent/tickets/:ticket_id/questions/:question_id", Method: "GET", Name: GetAgentQuestion},
	{Path: "/api/v1/agent/tickets/:ticket_id/questions/:question_id/answer", Method: "PUT", Name: AnswerAgentQuestion},
```

- [ ] Run `go build ./atc/...` — expect panic-free build but the wrappa suites now FAIL (routes reach the `default: panic` cases). Verify:

```bash
go build ./atc/... && ginkgo ./atc/wrappa/
```

Expected: `you missed a spot: "AskAgentQuestion"` panic in the api-auth wrappa specs.

- [ ] Wire auth tiers in `atc/wrappa/api_auth_wrappa.go`, reusing the case groups and the combined helper ticket-core landed in wave 2 (§4.1/§4.2 define the tiers; the Task 1 survey grep — now including `AgentPrincipalOrMainTeam`/`HandlerFor` — records the exact landed names). Ticket-core's registration task (plan 06 lines 2768–2795) is the copyable template: the combined helper is `auth.AgentPrincipalOrMainTeamHandler(principalTier, mainTeamTier http.Handler) http.Handler` (two ALREADY-WRAPPED handler tiers — NOT `(handler, rejector, scope)`), the principal tier is built via `wrappa.checkAgentPrincipalHandlerFactory.HandlerFor(handler, rejector, <scope>)`, the main-team tier via `auth.CheckAgentAuthorizationHandler(handler, rejector)`, and scope constants come from `agent/api/principals` (`principals.ScopeTicketsWrite`, `principals.ScopeTicketsRead`, `principals.ScopeQuestionsAnswer`). The file already imports `principals` after wave 2; if the survey shows it does not, add `"github.com/concourse/concourse/agent/api/principals"`. Following the contract route table (§4.2):
  - `AskAgentQuestion` → `principal(tickets:write)` only (no viewer tier). Add its name to ticket-core's principal-only case group (the one holding `atc.UpdateAgentTicketTask`, which uses `wrappa.checkAgentPrincipalHandlerFactory.HandlerFor(handler, rejector, principals.ScopeTicketsWrite)`).
  - `ListAgentTicketQuestions` and `GetAgentQuestion` → `principal(tickets:read); also authorized viewer`. Add both names to ticket-core's combined-read case group (the `atc.GetAgentTicket` case that reads `principals.ScopeTicketsRead`).
  - `AnswerAgentQuestion` → new combined case, scope `questions:answer`, member-level authorization: the same `AgentPrincipalOrMainTeamHandler` composition, with the principal tier scoped to `principals.ScopeQuestionsAnswer`:

```go
		// combined tier: agent principal (questions:answer, sidecar timeout
		// resolution only — §3.2) OR authorized main-team member (human answer).
		// Same combined helper the ticket-core routes use (plan 06 :2779).
		case atc.AnswerAgentQuestion:
			newHandler = auth.AgentPrincipalOrMainTeamHandler(
				wrappa.checkAgentPrincipalHandlerFactory.HandlerFor(handler, rejector, principals.ScopeQuestionsAnswer),
				auth.CheckAgentAuthorizationHandler(handler, rejector),
			)
```

- [ ] Add all four route names to the no-op case list in `atc/wrappa/reject_archived_wrappa.go` (after `atc.ListTeamAgentReviews`, line 134) and to the `EnableSystemAuditLog` case in `atc/auditor/auditor.go` (after `atc.ListTeamAgentReviews`, line 157).

- [ ] Add role entries in `atc/api/accessor/roles.go` after `atc.GetBuildAgentReviews` (only the routes that pass through an authorization handler need entries — the file's comment at lines 102–107 explains missing entries silently become admin-only):

```go
	atc.ListAgentTicketQuestions: ViewerRole,
	atc.GetAgentQuestion:         ViewerRole,
	atc.AnswerAgentQuestion:      MemberRole,
```

- [ ] Wire the handler in `atc/api/handler.go`: add a param after `agentReviewPublishToken string` (line 92 today; after ticket-core's params in reality):

```go
	questionsStore questions.Store,
```

  import `"github.com/concourse/concourse/agent/api/questions"`, construct next to `reviewsServer` (line 123):

```go
	questionsServer := questions.NewHandler(questionsStore)
```

  and add handler-map entries next to the agent reviews block (line 275–277):

```go
		atc.AskAgentQuestion:         http.HandlerFunc(questionsServer.AskQuestion),
		atc.ListAgentTicketQuestions: http.HandlerFunc(questionsServer.ListQuestions),
		atc.GetAgentQuestion:         http.HandlerFunc(questionsServer.GetQuestion),
		atc.AnswerAgentQuestion:      http.HandlerFunc(questionsServer.AnswerQuestion),
```

- [ ] Update both callers of `api.NewHandler`:
  - `atc/api/api_suite_test.go` (line 227 today): add `questions.NewMemoryStore(),` after `"test-agent-review-publish-token",`.
  - `atc/atccmd/command.go` (line 2299 today): add `db.NewAgentQuestionsFactory(dbConn),` after `cmd.AgentReviewPublishToken,`.

- [ ] Run to verify pass:

```bash
go build ./atc/... && ginkgo ./atc/wrappa/ && ginkgo ./atc/api/ && ginkgo ./atc/auditor/
```

- [ ] Commit:

```bash
git add atc/routes.go atc/wrappa/ atc/auditor/auditor.go atc/api/ atc/atccmd/command.go
git commit -m "feat(atc): register agent question routes (ask/list/long-poll/answer)" -m "Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 6b: AnswerAgentQuestion fires the dispatcher component notify (PARK-V2 §D, 2026-07-10)

**Files:**
- Modify: `agent/api/questions/handler.go` (`OnAnswer` hook)
- Modify: `atc/api/handler.go`, `atc/api/api_suite_test.go`, `atc/atccmd/command.go` (wiring)
- Test: `agent/api/questions/handler_test.go` (extend)

**Why (PARK-V2 §D):** dispatch's `reconcileAwaitingRuns` (plan 11 Task 11c) re-arms a parked run's continuation on its polling pass; the answer route ADDITIONALLY nudges the dispatcher component so resume is prompt instead of waiting out a poll interval. Never notify-only (the fork's lossy-NOTIFY lesson: `handleNotification` silently drops on a full channel) — the notify is best-effort and a lost one only delays resume to the next poll. The wiring lands NOW even though `atc.ComponentAgentDispatcher` is wave-4 (plan 11 Task 13): `pg_notify` on a channel with no listener is a harmless no-op.

- [ ] Write the failing test — append to `agent/api/questions/handler_test.go`:

```go
// TestAnswerFiresOnAnswerHook (PARK-V2 §D): a SUCCESSFUL answer write fires
// the hook exactly once (the ATC wires it to the dispatcher component's
// notify channel so parked-run resume is prompt); rejected writes (validation
// 400, conflict 409, not-found 404) must NOT fire it — polling remains the
// only guaranteed path (never notify-only).
func TestAnswerFiresOnAnswerHook(t *testing.T) {
	store := questions.NewMemoryStore()
	h := questions.NewHandler(store)
	fired := 0
	h.OnAnswer = func() { fired++ }
	q := doAsk(t, h)

	answer := func(id int, body string) int {
		req := httptest.NewRequest(http.MethodPut,
			fmt.Sprintf("/api/v1/agent/tickets/12/questions/%d/answer", id),
			strings.NewReader(body))
		req.Form = url.Values{":ticket_id": {"12"}, ":question_id": {fmt.Sprint(id)}}
		w := httptest.NewRecorder()
		h.AnswerQuestion(w, req)
		return w.Code
	}

	if code := answer(q.ID, `{"answer":"maybe","answered_by":"tdm"}`); code != http.StatusBadRequest || fired != 0 {
		t.Fatalf("invalid answer: code=%d fired=%d", code, fired)
	}
	if code := answer(q.ID, `{"answer":"yes","answered_by":"tdm"}`); code != http.StatusOK || fired != 1 {
		t.Fatalf("first answer: code=%d fired=%d", code, fired)
	}
	if code := answer(q.ID, `{"answer":"no","answered_by":"late"}`); code != http.StatusConflict || fired != 1 {
		t.Fatalf("conflict: code=%d fired=%d", code, fired)
	}
	if code := answer(424242, `{"answer":"yes","answered_by":"tdm"}`); code != http.StatusNotFound || fired != 1 {
		t.Fatalf("not-found: code=%d fired=%d", code, fired)
	}
}
```

- [ ] Run to verify it fails (`h.OnAnswer` undefined):

```bash
go test ./agent/api/questions/
```

- [ ] Add the hook to the `Handler` struct in `agent/api/questions/handler.go` (after `MaxWait`):

```go
	// OnAnswer, when non-nil, is invoked after every SUCCESSFUL answer write.
	// The ATC wires it to the dispatcher component's notify channel (PARK-V2
	// §D) so an answered park re-arms its continuation promptly; the
	// dispatcher's polling pass remains the guaranteed path — never
	// notify-only, per the fork's lossy-NOTIFY lesson.
	OnAnswer func()
```

  and in `AnswerQuestion`, directly after the `h.store.Answer` error switch (the success path, before the read-back):

```go
	if h.OnAnswer != nil {
		h.OnAnswer()
	}
```

- [ ] Wire it through the ATC:
  - `atc/api/handler.go`: add a param `questionAnswerNotify func(),` directly after `questionsStore questions.Store,` (Task 6's param), and after constructing `questionsServer` set `questionsServer.OnAnswer = questionAnswerNotify` (nil-safe — the handler checks non-nil).
  - `atc/api/api_suite_test.go`: pass `nil,` after `questions.NewMemoryStore(),`.
  - `atc/atccmd/command.go`: after `db.NewAgentQuestionsFactory(dbConn),` (Task 6's argument) pass:

```go
		func() {
			// PARK-V2 §D: nudge the dispatcher so an answered park resumes
			// promptly. "agent_dispatcher" is atc.ComponentAgentDispatcher
			// (plan 11 Task 13, wave 4); until that constant lands, the
			// string literal notifies an unlistened channel — a harmless
			// no-op — and is swapped for the constant when plan 11 merges.
			// Errors are logged, never fatal: polling is the guaranteed
			// resume path.
			if err := dbConn.Bus().Notify("agent_dispatcher"); err != nil {
				logger.Error("failed-to-notify-agent-dispatcher-on-answer", err)
			}
		},
```

  (anchor to the Task 6 call-site edit; the surrounding `dbConn`/`logger` names are the ones already in scope at `api.NewHandler`'s construction — verify against the landed wave-2 file, per the execution-time anchor warning.)

- [ ] Run to verify pass:

```bash
go test ./agent/api/questions/ && go build ./atc/... && ginkgo ./atc/api/
```

- [ ] Commit:

```bash
git add agent/api/questions/handler.go agent/api/questions/handler_test.go atc/api/ atc/atccmd/command.go
git commit -m "feat(atc): answer route nudges the agent dispatcher for prompt resume (PARK-V2 §D)" -m "Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 7: `agent/notify` — webhook notifier library (§8.4)

**Files:**
- Create: `agent/notify/notifier.go`
- Test: `agent/notify/notifier_test.go`

- [ ] Write the failing test `agent/notify/notifier_test.go`:

```go
package notify_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/concourse/concourse/agent/notify"
)

func TestWebhookNotifierPostsPayload(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if ct := r.Header.Get("Content-Type"); ct != "application/json" {
			t.Errorf("expected application/json, got %s", ct)
		}
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Errorf("decode: %v", err)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	n := notify.NewWebhookNotifier(srv.URL, srv.Client())
	err := n.Notify(context.Background(), notify.Notification{
		Kind:     "question",
		TicketID: 42,
		Title:    "Agent question on ticket 42",
		URL:      "https://concourse.home/agent/tickets/42",
		Body:     "Which auth flow should I extend?",
	})
	if err != nil {
		t.Fatalf("Notify: %v", err)
	}
	// Exact §8.4 payload keys.
	if got["kind"] != "question" || got["ticket_id"] != float64(42) ||
		got["title"] != "Agent question on ticket 42" ||
		got["url"] != "https://concourse.home/agent/tickets/42" ||
		got["body"] != "Which auth flow should I extend?" {
		t.Fatalf("unexpected payload: %v", got)
	}
}

func TestWebhookNotifierErrorsOnNon2xx(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer srv.Close()

	n := notify.NewWebhookNotifier(srv.URL, srv.Client())
	if err := n.Notify(context.Background(), notify.Notification{Kind: "question", TicketID: 1}); err == nil {
		t.Fatal("expected error on 502, got nil")
	}
}
```

- [ ] Run to verify it fails:

```bash
go test ./agent/notify/
```

Expected failure: package does not exist.

- [ ] Write `agent/notify/notifier.go`:

```go
// Package notify implements the §8.4 notification channel: a single generic
// webhook the ATC POSTs on HITL questions/checkpoints (this workstream) and,
// later, ticket state changes and budget stops (their owners call Notifier).
// The ticket page remains the source of truth; the webhook is fan-out only.
package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// Notification is the exact §8.4 webhook payload.
type Notification struct {
	Kind     string `json:"kind"` // question | checkpoint | state | budget
	TicketID int    `json:"ticket_id"`
	Title    string `json:"title"`
	URL      string `json:"url"`
	Body     string `json:"body"`
}

//counterfeiter:generate . Notifier
type Notifier interface {
	Notify(ctx context.Context, n Notification) error
}

type webhookNotifier struct {
	url    string
	client *http.Client
}

// NewWebhookNotifier posts notifications to webhookURL. A nil client gets a
// 10s-timeout default (never hang a poller on a dead webhook).
func NewWebhookNotifier(webhookURL string, client *http.Client) Notifier {
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	return &webhookNotifier{url: webhookURL, client: client}
}

func (w *webhookNotifier) Notify(ctx context.Context, n Notification) error {
	body, err := json.Marshal(n)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, w.url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := w.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("webhook returned %d", resp.StatusCode)
	}
	return nil
}
```

- [ ] Run to verify pass:

```bash
go test ./agent/notify/
```

- [ ] Commit:

```bash
git add agent/notify/
git commit -m "feat(agent): generic webhook notifier (shared-contracts 8.4)" -m "Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 8: `agent_notifier` RunnableComponent + web flag wiring

**Files:**
- Create: `agent/notify/question_notifier.go`
- Modify: `atc/component.go` (component name constants, lines 23–26 — after `ComponentK8sWorkerReaper`)
- Modify: `atc/atccmd/command.go` (flag near `AgentReviewPublishToken` line 218; components list in `backgroundComponents`, after the syslog block near line 1324)
- Test: `agent/notify/question_notifier_test.go`

- [ ] Write the failing test `agent/notify/question_notifier_test.go` (uses the memory store; the notifier never fails the run — a webhook error leaves the row unnotified for the next poll):

```go
package notify_test

import (
	"context"
	"fmt"
	"testing"

	"code.cloudfoundry.org/lager/lagertest"
	"github.com/concourse/concourse/agent/api/questions"
	"github.com/concourse/concourse/agent/notify"
)

type recordingNotifier struct {
	notes []notify.Notification
	err   error
}

func (r *recordingNotifier) Notify(_ context.Context, n notify.Notification) error {
	if r.err != nil {
		return r.err
	}
	r.notes = append(r.notes, n)
	return nil
}

func ask(t *testing.T, store questions.Store, kind questions.Kind) int {
	t.Helper()
	id, err := store.Ask(&questions.Question{
		TicketID: 5, Kind: kind, Question: "approve the plan?",
		Options: []string{"approve", "reject"},
	})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestQuestionNotifierDeliversAndMarks(t *testing.T) {
	store := questions.NewMemoryStore()
	rec := &recordingNotifier{}
	qn := notify.NewQuestionNotifier(
		lagertest.NewTestLogger("qn"), store, rec,
		func(ticketID int) string { return fmt.Sprintf("https://web/agent/tickets/%d", ticketID) },
	)

	ask(t, store, questions.KindCheckpoint)

	if err := qn.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(rec.notes) != 1 {
		t.Fatalf("expected 1 notification, got %d", len(rec.notes))
	}
	n := rec.notes[0]
	if n.Kind != "checkpoint" || n.TicketID != 5 ||
		n.URL != "https://web/agent/tickets/5" || n.Body != "approve the plan?" {
		t.Fatalf("unexpected notification: %+v", n)
	}

	// Second run: already marked, no re-delivery.
	if err := qn.Run(context.Background()); err != nil {
		t.Fatalf("Run 2: %v", err)
	}
	if len(rec.notes) != 1 {
		t.Fatalf("expected no re-delivery, got %d", len(rec.notes))
	}
}

func TestQuestionNotifierRetriesFailedDelivery(t *testing.T) {
	store := questions.NewMemoryStore()
	rec := &recordingNotifier{err: fmt.Errorf("webhook down")}
	qn := notify.NewQuestionNotifier(lagertest.NewTestLogger("qn"), store, rec,
		func(int) string { return "u" })

	ask(t, store, questions.KindQuestion)

	if err := qn.Run(context.Background()); err != nil {
		t.Fatalf("Run must not error on delivery failure: %v", err)
	}

	rec.err = nil
	if err := qn.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(rec.notes) != 1 {
		t.Fatalf("expected delivery on retry, got %d", len(rec.notes))
	}
}
```

- [ ] Run to verify it fails:

```bash
go test ./agent/notify/
```

Expected failure: `undefined: notify.NewQuestionNotifier`.

- [ ] Write `agent/notify/question_notifier.go` (injected-logger component pattern, like `jetbridge.Registrar`):

```go
package notify

import (
	"context"
	"fmt"

	"code.cloudfoundry.org/lager"
	"github.com/concourse/concourse/agent/api/questions"
)

// QuestionNotifier is the Runnable behind atc.ComponentAgentNotifier: every
// poll it delivers webhook notifications for unnotified agent_run_questions
// rows and marks them. Polling-backed by design (fork lesson: never
// notify-only); a failed webhook POST is retried on the next poll because the
// row stays unnotified. Delivery is at-least-once; the webhook consumer must
// tolerate duplicates (only possible if MarkNotified fails after a delivery).
type QuestionNotifier struct {
	logger    lager.Logger
	store     questions.Store
	notifier  Notifier
	ticketURL func(ticketID int) string
}

func NewQuestionNotifier(
	logger lager.Logger,
	store questions.Store,
	notifier Notifier,
	ticketURL func(ticketID int) string,
) *QuestionNotifier {
	return &QuestionNotifier{logger: logger, store: store, notifier: notifier, ticketURL: ticketURL}
}

func (qn *QuestionNotifier) Run(ctx context.Context) error {
	backlog, err := qn.store.Unnotified(50)
	if err != nil {
		qn.logger.Error("failed-to-list-unnotified-questions", err)
		return err
	}
	for _, q := range backlog {
		title := fmt.Sprintf("Agent question on ticket %d", q.TicketID)
		if q.Kind == questions.KindCheckpoint {
			title = fmt.Sprintf("Checkpoint approval needed on ticket %d", q.TicketID)
		}
		err := qn.notifier.Notify(ctx, Notification{
			Kind:     string(q.Kind),
			TicketID: q.TicketID,
			Title:    title,
			URL:      qn.ticketURL(q.TicketID),
			Body:     q.Question,
		})
		if err != nil {
			// Leave unnotified; next poll retries. Never fail the component
			// for a webhook outage.
			qn.logger.Error("failed-to-deliver-notification", err, lager.Data{"question": q.ID})
			continue
		}
		if err := qn.store.MarkNotified(q.ID); err != nil {
			qn.logger.Error("failed-to-mark-notified", err, lager.Data{"question": q.ID})
		}
	}
	return nil
}
```

- [ ] Run to verify pass:

```bash
go test ./agent/notify/
```

- [ ] Add the component constant in `atc/component.go` after `ComponentK8sWorkerReaper` (line 24):

```go
	ComponentAgentNotifier = "agent_notifier"
```

- [ ] Add the web flag in `atc/atccmd/command.go` directly below `AgentReviewPublishToken` (line 218):

```go
	AgentNotifyWebhookURL string `long:"agent-notify-webhook-url" description:"Webhook URL POSTed on agent HITL questions/checkpoints (JSON: kind, ticket_id, title, url, body). Notifications are UI-only when empty."`
```

- [ ] Wire the component in `backgroundComponents` (`atc/atccmd/command.go`; append after the syslog-drainer block that ends near line 1338, inside the same function — `dbConn`, `logger`, and `cmd` are all in scope there, as the k8s block at lines 1275–1322 shows):

```go
	if cmd.AgentNotifyWebhookURL != "" {
		components = append(components, RunnableComponent{
			Component: atc.Component{
				Name: atc.ComponentAgentNotifier,
			},
			Runnable: notify.NewQuestionNotifier(
				logger.Session(atc.ComponentAgentNotifier),
				db.NewAgentQuestionsFactory(dbConn),
				notify.NewWebhookNotifier(cmd.AgentNotifyWebhookURL, nil),
				func(ticketID int) string {
					return fmt.Sprintf("%s/agent/tickets/%d", cmd.ExternalURL.String(), ticketID)
				},
			),
			Interval: 10 * time.Second,
		})
	}
```

  Import `"github.com/concourse/concourse/agent/notify"`. Confirm the ticket-page URL path against the survey result from Task 1 (ticket-core's Elm route); adjust the format string if ticket-core landed a different path.

- [ ] Verify the whole tree builds and the atccmd suite passes:

```bash
go build ./atc/... && ginkgo ./atc/atccmd/
```

- [ ] Commit:

```bash
git add agent/notify/ atc/component.go atc/atccmd/command.go
git commit -m "feat(atc): agent_notifier polling component behind --agent-notify-webhook-url" -m "Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 9: sidecar env config + resilient ATC client (`agent/platformmcp`)

**Files:**
- Create: `agent/platformmcp/config.go`
- Create: `agent/platformmcp/atcclient.go`
- Test: `agent/platformmcp/config_test.go`
- Test: `agent/platformmcp/atcclient_test.go`

- [ ] Write the failing test `agent/platformmcp/config_test.go` (env names are the §8.1 contract + the Task-1 addendum):

```go
package platformmcp_test

import (
	"testing"
	"time"

	"github.com/concourse/concourse/agent/platformmcp"
)

func TestConfigFromEnv(t *testing.T) {
	t.Setenv("ATC_EXTERNAL_URL", "https://concourse.home")
	t.Setenv("AGENT_PRINCIPAL_TOKEN", "cap1.9.secret")
	t.Setenv("AGENT_TICKET_ID", "42")
	t.Setenv("AGENT_PIPELINE_RUN_ID", "7")
	t.Setenv("BUILD_ID", "1001")
	t.Setenv("AGENT_STEP_NAME", "implement")
	t.Setenv("PLATFORM_MCP_ASK_TIMEOUT_POLICY", "default")
	t.Setenv("PLATFORM_MCP_ASK_TIMEOUT_SECONDS", "300")

	cfg, err := platformmcp.ConfigFromEnv()
	if err != nil {
		t.Fatalf("ConfigFromEnv: %v", err)
	}
	if cfg.ATCURL != "https://concourse.home" || cfg.PrincipalToken != "cap1.9.secret" ||
		cfg.TicketID != 42 || cfg.PipelineRunID != 7 || cfg.BuildID != 1001 ||
		cfg.StepName != "implement" || cfg.TimeoutPolicy != "default" || cfg.TimeoutSeconds != 300 {
		t.Fatalf("unexpected config: %+v", cfg)
	}
	if cfg.ListenAddr != ":7781" {
		t.Fatalf("expected default listen addr :7781, got %q", cfg.ListenAddr)
	}
	if cfg.EventsPath != "" {
		t.Fatalf("expected empty default events path, got %q", cfg.EventsPath)
	}
}

func TestConfigFromEnvDefaultsAndErrors(t *testing.T) {
	t.Setenv("ATC_EXTERNAL_URL", "https://concourse.home")
	t.Setenv("AGENT_PRINCIPAL_TOKEN", "cap1.9.secret")
	t.Setenv("AGENT_TICKET_ID", "42")
	t.Setenv("BUILD_ID", "")
	t.Setenv("AGENT_PIPELINE_RUN_ID", "")
	t.Setenv("AGENT_STEP_NAME", "")
	t.Setenv("PLATFORM_MCP_ASK_TIMEOUT_POLICY", "")
	t.Setenv("PLATFORM_MCP_ASK_TIMEOUT_SECONDS", "")
	t.Setenv("MCP_LISTEN_ADDR", "127.0.0.1:9999")

	cfg, err := platformmcp.ConfigFromEnv()
	if err != nil {
		t.Fatalf("ConfigFromEnv: %v", err)
	}
	if cfg.TimeoutPolicy != "park" || cfg.TimeoutSeconds != 0 || cfg.ListenAddr != "127.0.0.1:9999" {
		t.Fatalf("unexpected defaults: %+v", cfg)
	}

	t.Setenv("ATC_EXTERNAL_URL", "")
	if _, err := platformmcp.ConfigFromEnv(); err == nil {
		t.Fatal("expected error when ATC_EXTERNAL_URL is missing")
	}
}

// TestConfigFromEnvTimeoutPolicyRequiresPositiveSeconds is the defense-in-depth
// cross-field check (mirrors workflow-store): a non-park policy with a
// non-positive timeout can never fire, so a hand-set sidecar env must fail
// loudly at startup rather than park forever. park+0 stays legal (indefinite).
func TestConfigFromEnvTimeoutPolicyRequiresPositiveSeconds(t *testing.T) {
	base := func() {
		t.Setenv("ATC_EXTERNAL_URL", "https://concourse.home")
		t.Setenv("AGENT_PRINCIPAL_TOKEN", "cap1.9.secret")
		t.Setenv("AGENT_TICKET_ID", "42")
		t.Setenv("PLATFORM_MCP_ASK_TIMEOUT_SECONDS", "0")
	}
	for _, policy := range []string{"default", "fail"} {
		base()
		t.Setenv("PLATFORM_MCP_ASK_TIMEOUT_POLICY", policy)
		if _, err := platformmcp.ConfigFromEnv(); err == nil {
			t.Fatalf("policy %q + 0 seconds: expected error, got nil", policy)
		}
	}

	// park + 0 is legal (wait indefinitely).
	base()
	t.Setenv("PLATFORM_MCP_ASK_TIMEOUT_POLICY", "park")
	if _, err := platformmcp.ConfigFromEnv(); err != nil {
		t.Fatalf("park + 0 seconds: expected no error, got %v", err)
	}
}

// TestConfigFromEnvProgressInterval is the D3 SSE-heartbeat validation
// (2026-07-09 SSE seam delta): unset = 0 (server defaults to 15s); a set
// value must parse as a Go duration, be > 0, and be <= 30s (contracts §3.1
// progress mandate — the claude CLI abandons a progress-free tools/call at
// exactly 60s). Never clamp silently: invalid/<=0/>30s are fatal at startup.
func TestConfigFromEnvProgressInterval(t *testing.T) {
	base := func() {
		t.Setenv("ATC_EXTERNAL_URL", "https://concourse.home")
		t.Setenv("AGENT_PRINCIPAL_TOKEN", "cap1.9.secret")
		t.Setenv("AGENT_TICKET_ID", "42")
	}

	base()
	cfg, err := platformmcp.ConfigFromEnv()
	if err != nil {
		t.Fatalf("ConfigFromEnv: %v", err)
	}
	if cfg.ProgressInterval != 0 {
		t.Fatalf("unset PLATFORM_MCP_PROGRESS_INTERVAL: expected 0 (server default), got %s", cfg.ProgressInterval)
	}

	base()
	t.Setenv("PLATFORM_MCP_PROGRESS_INTERVAL", "10s")
	cfg, err = platformmcp.ConfigFromEnv()
	if err != nil {
		t.Fatalf("ConfigFromEnv with 10s: %v", err)
	}
	if cfg.ProgressInterval != 10*time.Second {
		t.Fatalf("expected 10s, got %s", cfg.ProgressInterval)
	}

	for _, bad := range []string{"bogus", "0s", "-5s", "45s", "31s"} {
		base()
		t.Setenv("PLATFORM_MCP_PROGRESS_INTERVAL", bad)
		if _, err := platformmcp.ConfigFromEnv(); err == nil {
			t.Fatalf("PLATFORM_MCP_PROGRESS_INTERVAL=%q: expected fatal error, got nil", bad)
		}
	}
}
```

- [ ] Run to verify it fails:

```bash
go test ./agent/platformmcp/
```

Expected failure: package does not exist.

- [ ] Write `agent/platformmcp/config.go`:

```go
// Package platformmcp is the platform-mcp sidecar: the agent's mid-flight
// interaction surface with the platform (shared contracts §3.2). It serves
// MCP streamable HTTP on MCP_LISTEN_ADDR and calls the ATC API with its
// per-run principal token. All tools operate on the ticket in AGENT_TICKET_ID
// — agents cannot address other tickets.
package platformmcp

import (
	"fmt"
	"os"
	"strconv"
	"time"
)

type Config struct {
	ATCURL         string // ATC_EXTERNAL_URL (required)
	PrincipalToken string // AGENT_PRINCIPAL_TOKEN (required)
	TicketID       int    // AGENT_TICKET_ID (required, > 0)
	PipelineRunID  int    // AGENT_PIPELINE_RUN_ID (0 = none)
	BuildID        int    // BUILD_ID (0 = none)
	StepName       string // AGENT_STEP_NAME
	TimeoutPolicy  string // PLATFORM_MCP_ASK_TIMEOUT_POLICY: park|default|fail (default park)
	TimeoutSeconds int    // PLATFORM_MCP_ASK_TIMEOUT_SECONDS (default 0 = indefinite)
	ListenAddr     string // MCP_LISTEN_ADDR (default :7781, §8.1)
	EventsPath     string // PLATFORM_MCP_EVENTS_PATH ("" = stdout; Task 1 addendum)
	// ProgressInterval is the SSE progress-heartbeat interval
	// (PLATFORM_MCP_PROGRESS_INTERVAL, Go duration; SSE seam delta D3).
	// 0 = unset = mcpserver.DefaultHeartbeat (15s). Set values must be
	// > 0 and <= 30s — never clamped, always fatal at startup.
	ProgressInterval time.Duration
}

func ConfigFromEnv() (Config, error) {
	cfg := Config{
		ATCURL:         os.Getenv("ATC_EXTERNAL_URL"),
		PrincipalToken: os.Getenv("AGENT_PRINCIPAL_TOKEN"),
		StepName:       os.Getenv("AGENT_STEP_NAME"),
		TimeoutPolicy:  os.Getenv("PLATFORM_MCP_ASK_TIMEOUT_POLICY"),
		ListenAddr:     os.Getenv("MCP_LISTEN_ADDR"),
		EventsPath:     os.Getenv("PLATFORM_MCP_EVENTS_PATH"),
	}
	if cfg.ATCURL == "" {
		return cfg, fmt.Errorf("ATC_EXTERNAL_URL is required")
	}
	if cfg.PrincipalToken == "" {
		return cfg, fmt.Errorf("AGENT_PRINCIPAL_TOKEN is required")
	}
	var err error
	if cfg.TicketID, err = intEnv("AGENT_TICKET_ID"); err != nil {
		return cfg, err
	}
	if cfg.TicketID <= 0 {
		return cfg, fmt.Errorf("AGENT_TICKET_ID must be a positive integer")
	}
	if cfg.PipelineRunID, err = intEnv("AGENT_PIPELINE_RUN_ID"); err != nil {
		return cfg, err
	}
	if cfg.BuildID, err = intEnv("BUILD_ID"); err != nil {
		return cfg, err
	}
	if cfg.TimeoutSeconds, err = intEnv("PLATFORM_MCP_ASK_TIMEOUT_SECONDS"); err != nil {
		return cfg, err
	}
	switch cfg.TimeoutPolicy {
	case "":
		cfg.TimeoutPolicy = "park"
	case "park", "default", "fail":
	default:
		return cfg, fmt.Errorf("invalid PLATFORM_MCP_ASK_TIMEOUT_POLICY %q", cfg.TimeoutPolicy)
	}
	// Defense-in-depth (mirrors workflow-store's cross-field check): a
	// default/fail policy with a non-positive timeout would never fire and
	// the sidecar would park indefinitely — the opposite of the operator's
	// intent. A hand-set sidecar env must fail loudly at startup rather than
	// silently degrade to park-forever. (park is the ONE policy where a 0
	// timeout is legal — it means "wait indefinitely".)
	if (cfg.TimeoutPolicy == "default" || cfg.TimeoutPolicy == "fail") && cfg.TimeoutSeconds <= 0 {
		return cfg, fmt.Errorf("PLATFORM_MCP_ASK_TIMEOUT_SECONDS must be > 0 when PLATFORM_MCP_ASK_TIMEOUT_POLICY is %q", cfg.TimeoutPolicy)
	}
	if cfg.ListenAddr == "" {
		cfg.ListenAddr = ":7781"
	}
	// D3 (SSE seam delta, 2026-07-09): the §3.1 progress mandate requires a
	// heartbeat at least every 30s; the empirical cliff is the claude CLI
	// abandoning a progress-free tools/call at exactly 60s. A set-but-invalid
	// value, a value <= 0, or a value > 30s is a FATAL startup error — never
	// clamp silently.
	if raw := os.Getenv("PLATFORM_MCP_PROGRESS_INTERVAL"); raw != "" {
		d, err := time.ParseDuration(raw)
		if err != nil {
			return cfg, fmt.Errorf("PLATFORM_MCP_PROGRESS_INTERVAL must be a Go duration: %w", err)
		}
		if d <= 0 || d > 30*time.Second {
			return cfg, fmt.Errorf("PLATFORM_MCP_PROGRESS_INTERVAL must be > 0 and <= 30s (progress mandate, contracts §3.1), got %s", d)
		}
		cfg.ProgressInterval = d
	}
	return cfg, nil
}

func intEnv(name string) (int, error) {
	raw := os.Getenv(name)
	if raw == "" {
		return 0, nil
	}
	v, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("%s must be an integer: %w", name, err)
	}
	return v, nil
}
```

- [ ] Run the config test to verify pass, then write the failing client test `agent/platformmcp/atcclient_test.go`. The restart test is the unit-level proof of "survives a web-node restart while parked": the stub ATC is killed mid-long-poll and re-listened on the same address, and `AwaitAnswer` keeps polling through it.

```go
package platformmcp_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/concourse/concourse/agent/api/questions"
	"github.com/concourse/concourse/agent/platformmcp"
)

func testClient(atcURL string) *platformmcp.ATCClient {
	return platformmcp.NewATCClient(atcURL, "cap1.9.secret", 42)
}

// stubQuestionMux serves the question routes against a memory store, checking
// the bearer token — the shape the real ATC exposes after Task 6.
func stubQuestionMux(t *testing.T, store *questions.MemoryStore) http.Handler {
	t.Helper()
	h := questions.NewHandler(store)
	h.PollInterval = 20 * time.Millisecond
	mux := http.NewServeMux()
	auth := func(next http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Authorization") != "Bearer cap1.9.secret" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			// emulate rata param extraction for the fixed test ticket/route shape
			r.ParseForm()
			r.Form.Set(":ticket_id", "42")
			if id := r.PathValue("question_id"); id != "" {
				r.Form.Set(":question_id", id)
			}
			next(w, r)
		}
	}
	mux.HandleFunc("POST /api/v1/agent/tickets/42/questions", auth(h.AskQuestion))
	mux.HandleFunc("GET /api/v1/agent/tickets/42/questions/{question_id}", auth(h.GetQuestion))
	mux.HandleFunc("PUT /api/v1/agent/tickets/42/questions/{question_id}/answer", auth(h.AnswerQuestion))
	return mux
}

func TestAskAndAwaitAnswer(t *testing.T) {
	store := questions.NewMemoryStore()
	srv := httptest.NewServer(stubQuestionMux(t, store))
	defer srv.Close()
	c := testClient(srv.URL)

	created, err := c.AskQuestion(context.Background(), &questions.Question{
		Question: "proceed?", Options: []string{"yes", "no"}, TimeoutPolicy: questions.TimeoutPark,
	})
	if err != nil {
		t.Fatalf("AskQuestion: %v", err)
	}
	if created.ID == 0 || created.TicketID != 42 {
		t.Fatalf("unexpected created: %+v", created)
	}

	go func() {
		time.Sleep(50 * time.Millisecond)
		_ = store.Answer(42, created.ID, "yes", "tdm")
	}()

	answered, timedOut, err := c.AwaitAnswer(context.Background(), created.ID, nil)
	if err != nil || timedOut {
		t.Fatalf("AwaitAnswer: timedOut=%v err=%v", timedOut, err)
	}
	if answered.Answer != "yes" || answered.AnsweredBy != "tdm" {
		t.Fatalf("unexpected answer: %+v", answered)
	}
}

func TestAwaitAnswerDeadline(t *testing.T) {
	store := questions.NewMemoryStore()
	srv := httptest.NewServer(stubQuestionMux(t, store))
	defer srv.Close()
	c := testClient(srv.URL)
	c.PollWait = 50 * time.Millisecond

	created, err := c.AskQuestion(context.Background(), &questions.Question{Question: "anyone?"})
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(120 * time.Millisecond)
	_, timedOut, err := c.AwaitAnswer(context.Background(), created.ID, &deadline)
	if err != nil {
		t.Fatalf("AwaitAnswer: %v", err)
	}
	if !timedOut {
		t.Fatal("expected timedOut=true")
	}
}

func TestAwaitAnswerSurvivesATCRestart(t *testing.T) {
	store := questions.NewMemoryStore()
	mux := stubQuestionMux(t, store)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	srv1 := &http.Server{Handler: mux}
	go srv1.Serve(ln)

	c := testClient("http://" + addr)
	c.PollWait = 50 * time.Millisecond
	c.RetryInterval = 20 * time.Millisecond

	created, err := c.AskQuestion(context.Background(), &questions.Question{Question: "still there?"})
	if err != nil {
		t.Fatal(err)
	}

	done := make(chan struct{})
	var answered atomic.Pointer[questions.Question]
	go func() {
		defer close(done)
		q, timedOut, err := c.AwaitAnswer(context.Background(), created.ID, nil)
		if err != nil || timedOut {
			t.Errorf("AwaitAnswer after restart: timedOut=%v err=%v", timedOut, err)
			return
		}
		answered.Store(q)
	}()

	// Kill the "web node" mid-park.
	time.Sleep(80 * time.Millisecond)
	srv1.Close()

	// Restart on the same address (retry the bind while the OS releases it).
	var ln2 net.Listener
	for i := 0; i < 50; i++ {
		ln2, err = net.Listen("tcp", addr)
		if err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("rebind %s: %v", addr, err)
	}
	srv2 := &http.Server{Handler: mux}
	defer srv2.Close()
	go srv2.Serve(ln2)

	// Answer via the restarted web node.
	time.Sleep(80 * time.Millisecond)
	if err := store.Answer(42, created.ID, "yes", "tdm"); err != nil {
		t.Fatal(err)
	}

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("AwaitAnswer did not resume after ATC restart")
	}
	if q := answered.Load(); q == nil || q.Answer != "yes" {
		t.Fatalf("unexpected resumed answer: %+v", q)
	}
}

const pendingQuestionBody = `{"id":7,"ticket_id":42,"kind":"question","question":"q","asked_at":1}`
const answeredQuestionBody = `{"id":7,"ticket_id":42,"kind":"question","question":"q","asked_at":1,"answered_at":2,"answer":"yes","answered_by":"tdm"}`

// scriptedStub serves GET question polls with per-attempt scripted responses —
// it drives AwaitAnswer's error classification (D6, 2026-07-09 SSE seam delta).
func scriptedStub(t *testing.T, respond func(attempt int) (status int, body string)) (*httptest.Server, *atomic.Int64) {
	t.Helper()
	var attempts atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := int(attempts.Add(1))
		status, body := respond(n)
		if body != "" {
			w.Header().Set("Content-Type", "application/json")
		}
		w.WriteHeader(status)
		fmt.Fprint(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv, &attempts
}

// D6 t1: a principal the ATC rejects forever becomes fatal after EXACTLY
// AuthFailureLimit (frozen 12) consecutive 401s — never a silent forever-park.
func TestAwaitAnswerFatalAfterConsecutiveAuthFailures(t *testing.T) {
	srv, attempts := scriptedStub(t, func(int) (int, string) {
		return http.StatusUnauthorized, ""
	})
	c := testClient(srv.URL)
	c.RetryInterval = time.Millisecond

	_, _, err := c.AwaitAnswer(context.Background(), 7, nil)
	if !errors.Is(err, platformmcp.ErrPrincipalRejected) {
		t.Fatalf("expected ErrPrincipalRejected, got %v", err)
	}
	if got := attempts.Load(); got != 12 {
		t.Fatalf("expected exactly 12 attempts (frozen AuthFailureLimit), got %d", got)
	}
}

// D6 t2: 5xx is NEVER fatal — a web restart may 500 for far more than the
// auth limit's worth of polls and the park must ride through it.
func TestAwaitAnswerRetries5xxForever(t *testing.T) {
	srv, _ := scriptedStub(t, func(attempt int) (int, string) {
		if attempt <= 20 { // > AuthFailureLimit
			return http.StatusInternalServerError, ""
		}
		return http.StatusOK, answeredQuestionBody
	})
	c := testClient(srv.URL)
	c.RetryInterval = time.Millisecond

	q, timedOut, err := c.AwaitAnswer(context.Background(), 7, nil)
	if err != nil || timedOut {
		t.Fatalf("AwaitAnswer: timedOut=%v err=%v", timedOut, err)
	}
	if q.Answer != "yes" {
		t.Fatalf("unexpected answer: %+v", q)
	}
}

// D6 t3: the counter is CONSECUTIVE — any success (even a still-pending poll)
// resets it, so alternating 401/success-pending never trips the fatal path.
func TestAwaitAnswerAuthFailureCounterResets(t *testing.T) {
	srv, _ := scriptedStub(t, func(attempt int) (int, string) {
		switch {
		case attempt <= 40 && attempt%2 == 1:
			return http.StatusUnauthorized, "" // 20 total 401s, never consecutive
		case attempt <= 40:
			return http.StatusOK, pendingQuestionBody
		default:
			return http.StatusOK, answeredQuestionBody
		}
	})
	c := testClient(srv.URL)
	c.PollWait = time.Millisecond
	c.RetryInterval = time.Millisecond

	q, timedOut, err := c.AwaitAnswer(context.Background(), 7, nil)
	if err != nil || timedOut {
		t.Fatalf("AwaitAnswer: timedOut=%v err=%v (counter must reset on success)", timedOut, err)
	}
	if q.Answer != "yes" {
		t.Fatalf("unexpected answer: %+v", q)
	}
}

func TestAnswerQuestionSendsBody(t *testing.T) {
	var gotBody map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"id":1}`))
	}))
	defer srv.Close()
	c := testClient(srv.URL)
	if err := c.AnswerQuestion(context.Background(), 1, "yes", "platform-mcp"); err != nil {
		t.Fatal(err)
	}
	if gotBody["answer"] != "yes" || gotBody["answered_by"] != "platform-mcp" {
		t.Fatalf("unexpected body: %v", gotBody)
	}
}
```

- [ ] Run to verify it fails:

```bash
go test ./agent/platformmcp/
```

Expected failure: `undefined: platformmcp.NewATCClient`.

- [ ] Write `agent/platformmcp/atcclient.go`:

```go
package platformmcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/concourse/concourse/agent/api/questions"
)

// StatusError is returned by do for EVERY non-2xx ATC response so callers can
// inspect the status via errors.As — AwaitAnswer's fatal-auth counting depends
// on it (D6, 2026-07-09 SSE seam delta / F31 leg 3).
type StatusError struct {
	Method string
	Path   string
	Code   int
	Body   string
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("%s %s: %d: %s", e.Method, e.Path, e.Code, e.Body)
}

// ErrPrincipalRejected is returned (wrapped) by AwaitAnswer after
// AuthFailureLimit CONSECUTIVE 401/403 responses: the per-run principal is
// expired or revoked, and the step must fail loudly rather than park forever.
var ErrPrincipalRejected = errors.New("agent principal rejected: consecutive auth failures exceeded limit")

// ATCClient is the sidecar's principal-authed ATC API client. Its long-poll
// loop (AwaitAnswer) is the park half of the §3.2 park/resume protocol:
// transport errors and 5xx responses are retried forever — an ATC/web-node
// restart while parked just means a few failed polls until the new node
// answers — but AuthFailureLimit CONSECUTIVE 401/403 responses are fatal
// (ErrPrincipalRejected): a revoked or expired per-run principal must fail
// the step loudly, never silently park it forever (F31 leg 3).
type ATCClient struct {
	baseURL  string
	token    string
	ticketID int
	http     *http.Client

	// PollWait is the server-side wait per long-poll request (default 30s).
	PollWait time.Duration
	// RetryInterval is the sleep after a failed poll (default 5s).
	RetryInterval time.Duration
	// AuthFailureLimit is the number of CONSECUTIVE 401/403 responses after
	// which AwaitAnswer gives up with ErrPrincipalRejected. FROZEN default 12:
	// with RetryInterval 5s that is >= 60s of sustained auth failures, which
	// outlives the §1.2 60s principal-verification cache — a revoked principal
	// is confirmed while a cache-warm blip cannot trip it.
	AuthFailureLimit int
}

func NewATCClient(baseURL, principalToken string, ticketID int) *ATCClient {
	return &ATCClient{
		baseURL:  baseURL,
		token:    principalToken,
		ticketID: ticketID,
		// No global timeout: long-polls legitimately hold the connection.
		// Individual requests carry contexts.
		http:             &http.Client{},
		PollWait:         30 * time.Second,
		RetryInterval:    5 * time.Second,
		AuthFailureLimit: 12,
	}
}

func (c *ATCClient) do(ctx context.Context, method, path string, body any, out any) error {
	var rdr io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rdr = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, rdr)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return &StatusError{
			Method: method, Path: path, Code: resp.StatusCode,
			Body: string(bytes.TrimSpace(msg)),
		}
	}
	if out != nil {
		return json.NewDecoder(resp.Body).Decode(out)
	}
	return nil
}

// TicketPayload mirrors the GetAgentTicket response: envelope + latest spec +
// active-plan tasks, all embedded by ticket-core's handler (backed by Store
// Get / LatestSpec / ActivePlan — survey Task 1). The three read tools each
// fetch this ONE payload and project it: read_ticket keeps ticket+spec and
// DROPS tasks; list_tasks projects the task skeleton; get_task returns one
// task's detail. Tasks are never flattened into markdown — the structure is
// preserved and served through typed tools (§3.2 read model).
type TicketPayload struct {
	Ticket json.RawMessage `json:"ticket"`
	Spec   json.RawMessage `json:"spec"`
	Tasks  []TicketTask    `json:"tasks"`
}

// TicketTask is one active-plan task as embedded in the GetAgentTicket payload.
// DetailMD is the per-task markdown body; list_tasks omits it, get_task returns it.
type TicketTask struct {
	Ordering int    `json:"ordering"`
	Title    string `json:"title"`
	Status   string `json:"status"`
	DetailMD string `json:"detail_md"`
}

// GetTicket fetches and decodes the GetAgentTicket payload. It tolerates the
// bare-ticket drift the survey warned about: a response with no "ticket" key
// is treated as the envelope itself (spec null, no tasks).
func (c *ATCClient) GetTicket(ctx context.Context) (*TicketPayload, error) {
	raw, err := c.GetTicketRaw(ctx)
	if err != nil {
		return nil, err
	}
	var probe map[string]json.RawMessage
	if err := json.Unmarshal(raw, &probe); err != nil {
		return nil, fmt.Errorf("decoding ticket response: %w", err)
	}
	if _, ok := probe["ticket"]; !ok {
		return &TicketPayload{Ticket: json.RawMessage(raw)}, nil
	}
	var payload TicketPayload
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, fmt.Errorf("decoding ticket payload: %w", err)
	}
	return &payload, nil
}

// GetTicketRaw returns the GetAgentTicket response body verbatim; ticket-core's
// handler embeds spec+tasks (survey Task 1). Kept for read_ticket, which
// re-projects the payload to envelope + spec ONLY.
func (c *ATCClient) GetTicketRaw(ctx context.Context) (json.RawMessage, error) {
	var raw json.RawMessage
	err := c.do(ctx, http.MethodGet, fmt.Sprintf("/api/v1/agent/tickets/%d", c.ticketID), nil, &raw)
	return raw, err
}

type SpecSubmission struct {
	Title              string              `json:"title"`
	Body               string              `json:"body"`
	AcceptanceCriteria []string            `json:"acceptance_criteria,omitempty"`
	Links              []map[string]string `json:"links,omitempty"`
}

func (c *ATCClient) SubmitSpec(ctx context.Context, spec SpecSubmission) (int, error) {
	var out struct {
		Version int `json:"version"`
	}
	err := c.do(ctx, http.MethodPost, fmt.Sprintf("/api/v1/agent/tickets/%d/spec", c.ticketID), spec, &out)
	return out.Version, err
}

type TaskSubmission struct {
	Title  string `json:"title"`
	Detail string `json:"detail,omitempty"`
}

func (c *ATCClient) SubmitPlan(ctx context.Context, tasks []TaskSubmission) (int, error) {
	var out struct {
		PlanVersion int `json:"plan_version"`
	}
	body := map[string]any{"tasks": tasks}
	err := c.do(ctx, http.MethodPost, fmt.Sprintf("/api/v1/agent/tickets/%d/plan", c.ticketID), body, &out)
	return out.PlanVersion, err
}

func (c *ATCClient) UpdateTaskStatus(ctx context.Context, ordering int, status, note string) error {
	body := map[string]string{"status": status}
	if note != "" {
		body["note"] = note
	}
	return c.do(ctx, http.MethodPut,
		fmt.Sprintf("/api/v1/agent/tickets/%d/tasks/%d", c.ticketID, ordering), body, nil)
}

func (c *ATCClient) AskQuestion(ctx context.Context, q *questions.Question) (*questions.Question, error) {
	var created questions.Question
	err := c.do(ctx, http.MethodPost,
		fmt.Sprintf("/api/v1/agent/tickets/%d/questions", c.ticketID), q, &created)
	if err != nil {
		return nil, err
	}
	return &created, nil
}

func (c *ATCClient) GetQuestion(ctx context.Context, questionID int, wait time.Duration) (*questions.Question, error) {
	path := fmt.Sprintf("/api/v1/agent/tickets/%d/questions/%d", c.ticketID, questionID)
	if wait > 0 {
		path += fmt.Sprintf("?wait=%s", wait)
	}
	var q questions.Question
	if err := c.do(ctx, http.MethodGet, path, nil, &q); err != nil {
		return nil, err
	}
	return &q, nil
}

func (c *ATCClient) AnswerQuestion(ctx context.Context, questionID int, answer, answeredBy string) error {
	body := map[string]string{"answer": answer, "answered_by": answeredBy}
	return c.do(ctx, http.MethodPut,
		fmt.Sprintf("/api/v1/agent/tickets/%d/questions/%d/answer", c.ticketID, questionID), body, nil)
}

// AwaitAnswer long-polls until the question is answered (returns q, false),
// the deadline passes (returns nil, true — caller applies the timeout
// policy), or ctx is cancelled. Transport errors and 5xx responses are
// retried indefinitely (parked runs must survive web-node restarts);
// CONSECUTIVE 401/403 responses are counted and become fatal at
// AuthFailureLimit, returning an error wrapping ErrPrincipalRejected. The
// counter resets on any success and on any non-auth error (D6/F31 leg 3).
func (c *ATCClient) AwaitAnswer(ctx context.Context, questionID int, deadline *time.Time) (*questions.Question, bool, error) {
	authFailures := 0
	for {
		if deadline != nil && time.Now().After(*deadline) {
			return nil, true, nil
		}
		wait := c.PollWait
		if deadline != nil {
			if until := time.Until(*deadline); until < wait {
				wait = until
			}
		}
		q, err := c.GetQuestion(ctx, questionID, wait)
		if err != nil {
			if ctx.Err() != nil {
				return nil, false, ctx.Err()
			}
			var se *StatusError
			if errors.As(err, &se) && (se.Code == http.StatusUnauthorized || se.Code == http.StatusForbidden) {
				authFailures++
				if authFailures >= c.AuthFailureLimit {
					return nil, false, fmt.Errorf("question %d: %d consecutive 401/403 responses: %w",
						questionID, c.AuthFailureLimit, ErrPrincipalRejected)
				}
			} else {
				authFailures = 0 // transport or 5xx: retry forever
			}
			select {
			case <-ctx.Done():
				return nil, false, ctx.Err()
			case <-time.After(c.RetryInterval):
			}
			continue
		}
		authFailures = 0
		if q.AnsweredAt != 0 {
			return q, false, nil
		}
	}
}
```

- [ ] Run to verify pass:

```bash
go test ./agent/platformmcp/
```

- [ ] Commit:

```bash
git add agent/platformmcp/
git commit -m "feat(platform-mcp): sidecar env config + restart-resilient ATC client" -m "Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 9b: SSE progress-heartbeat transport in `atc/api/mcpserver` (D2, 2026-07-09 SSE seam delta — lands BEFORE Task 10 and before gateway 10 Task 7)

**Files:**
- Modify: `atc/api/mcpserver/server.go` (upgraded IN PLACE — no new module)
- Modify: `atc/api/mcpserver/protocol.go` (`callToolParams` gains `_meta.progressToken`; new notification type)
- Modify: `atc/api/mcpserver/tools.go` + `atc/api/mcpserver/tools_test.go` + existing `server_test.go` handler literals (mechanical 3-arg signature update)
- Test: `atc/api/mcpserver/server_test.go` (new SSE Describe block, MIRRORED from `ci-agent/devmcp/server_test.go`, 04 Task 4)

**Why (F13, empirical):** the claude CLI (v2.1.77) silently abandons a buffered, progress-free MCP `tools/call` at exactly 60s — the model sees "(completed with no output)", no error flag; `MCP_TOOL_TIMEOUT` does NOT prevent it (it drives only the outer ~27.8h watchdog). A parked `ask_human` therefore can NEVER deliver its answer over the current buffered-only `atc/api/mcpserver`. The proven-surviving implementation is dev-mcp's SSE progress-heartbeat server (`ci-agent/devmcp`, 04 Task 4). `ci-agent` is a separate Go module and neither module may require the other, so this task is an explicit **MIRRORED IMPLEMENTATION** — a byte-similar port, drift-guarded by mirrored tests; the wire spec of record is 04 Task 1's §3-preamble amendment. Consumers: platform-mcp (Task 10) and gateway (10 Task 7) get SSE for free; dev-mcp keeps `ci-agent/devmcp` unchanged. Frozen error-mapping difference from devmcp is PRESERVED, not ported: handler errors remain `isError=true` tool results (never `-32602`), in both buffered and SSE modes — the final SSE frame carries exactly the response the buffered path would have written.

- [ ] Write the failing mirrored SSE tests — append this Describe block to `atc/api/mcpserver/server_test.go` (add `"fmt"`, `"strings"`, and `"time"` to its imports; `"io"`, `"net/http"`, `"net/http/httptest"` are already there):

```go
var _ = Describe("SSE progress streaming (mirrored from ci-agent/devmcp/server_test.go — 04 Task 4 wire spec)", func() {
	newSSEServer := func(heartbeat time.Duration, handler mcpserver.ToolHandler) *httptest.Server {
		s := mcpserver.NewServerWithHeartbeat(heartbeat)
		s.AddTool("echo", "echoes back", json.RawMessage(`{"type":"object"}`), handler)
		ts := httptest.NewServer(s)
		DeferCleanup(ts.Close)
		return ts
	}

	sseCall := func(url string) (*http.Response, error) {
		req, err := http.NewRequest(http.MethodPost, url, strings.NewReader(
			`{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":"echo","arguments":{},"_meta":{"progressToken":"tok-1"}}}`))
		Expect(err).NotTo(HaveOccurred())
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "text/event-stream")
		return http.DefaultClient.Do(req)
	}

	It("streams progress notifications over SSE and ends with the final response frame", func() {
		ts := newSSEServer(50*time.Millisecond, func(_ context.Context, _ json.RawMessage, progress func(string)) (any, error) {
			progress("halfway there")
			time.Sleep(150 * time.Millisecond)
			return map[string]any{"status": "ok"}, nil
		})

		resp, err := sseCall(ts.URL)
		Expect(err).NotTo(HaveOccurred())
		defer resp.Body.Close()
		Expect(resp.Header.Get("Content-Type")).To(Equal("text/event-stream"))

		body, err := io.ReadAll(resp.Body)
		Expect(err).NotTo(HaveOccurred())
		frames := string(body)
		Expect(frames).To(ContainSubstring("event: message"))
		Expect(frames).To(ContainSubstring(`"notifications/progress"`))
		Expect(frames).To(ContainSubstring(`"tok-1"`))
		Expect(frames).To(ContainSubstring("halfway there"))
		// The final JSON-RPC response is the LAST SSE frame.
		Expect(frames).To(ContainSubstring(`"id":7`))
		lastFrame := frames[strings.LastIndex(frames, "event: message"):]
		Expect(lastFrame).To(ContainSubstring(`"status":"ok"`))
	})

	It("emits heartbeats even when the handler produces no progress output", func() {
		ts := newSSEServer(30*time.Millisecond, func(_ context.Context, _ json.RawMessage, _ func(string)) (any, error) {
			time.Sleep(120 * time.Millisecond)
			return map[string]any{"status": "ok"}, nil
		})
		resp, err := sseCall(ts.URL)
		Expect(err).NotTo(HaveOccurred())
		defer resp.Body.Close()
		body, err := io.ReadAll(resp.Body)
		Expect(err).NotTo(HaveOccurred())
		// >= 2 heartbeat frames with the default "running <tool>" message.
		Expect(strings.Count(string(body), `"running echo"`)).To(BeNumerically(">=", 2))
	})

	It("stays buffered JSON when the client does not opt in", func() {
		ts := newSSEServer(30*time.Millisecond, func(_ context.Context, _ json.RawMessage, progress func(string)) (any, error) {
			progress("dropped on the buffered path")
			return map[string]any{"status": "ok"}, nil
		})
		// No Accept: text/event-stream, no _meta.progressToken.
		resp, err := http.Post(ts.URL, "application/json", strings.NewReader(
			`{"jsonrpc":"2.0","id":8,"method":"tools/call","params":{"name":"echo","arguments":{}}}`))
		Expect(err).NotTo(HaveOccurred())
		defer resp.Body.Close()
		Expect(resp.Header.Get("Content-Type")).To(ContainSubstring("application/json"))
		body, err := io.ReadAll(resp.Body)
		Expect(err).NotTo(HaveOccurred())
		Expect(string(body)).NotTo(ContainSubstring("notifications/progress"))
		Expect(string(body)).To(ContainSubstring(`"status":"ok"`))
	})

	It("carries handler errors as isError=true in the final SSE frame — never -32602", func() {
		ts := newSSEServer(30*time.Millisecond, func(_ context.Context, _ json.RawMessage, _ func(string)) (any, error) {
			return nil, fmt.Errorf("kaboom")
		})
		resp, err := sseCall(ts.URL)
		Expect(err).NotTo(HaveOccurred())
		defer resp.Body.Close()
		body, err := io.ReadAll(resp.Body)
		Expect(err).NotTo(HaveOccurred())
		frames := string(body)
		Expect(frames).To(ContainSubstring(`"isError":true`))
		Expect(frames).To(ContainSubstring("kaboom"))
		Expect(frames).NotTo(ContainSubstring(`-32602`))
	})
})
```

- [ ] Run to verify it fails:

```bash
ginkgo ./atc/api/mcpserver/
```

Expected failure: `undefined: mcpserver.NewServerWithHeartbeat` (and the 3-arg handler literals do not compile against the 2-arg `ToolHandler`).

- [ ] Update `atc/api/mcpserver/protocol.go` — `callToolParams` gains the progress opt-in and a notification type is added:

```go
type callToolParams struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments,omitempty"`
	// Meta carries the MCP progress opt-in: a client that wants SSE progress
	// sends params._meta.progressToken; it is echoed verbatim in every
	// notifications/progress frame (04 Task 1 wire spec).
	Meta *struct {
		ProgressToken json.RawMessage `json:"progressToken"`
	} `json:"_meta,omitempty"`
}

// jsonRPCNotification is a server-initiated message (no id) — the
// notifications/progress frames on the SSE path.
type jsonRPCNotification struct {
	JSONRPC string `json:"jsonrpc"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}
```

- [ ] Rewrite `atc/api/mcpserver/server.go` (the upgrade in place; `handleInitialize`/`handleToolsList`/`writeHTTPError`/`MustJSON` are unchanged and omitted here):

```go
package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const protocolVersion = "2024-11-05"

// DefaultHeartbeat is half the contract's "progress at least every 30s" bound
// (contracts §3.1), leaving 4x margin under the claude CLI's empirical 60s
// abandonment of progress-free tools/call requests (F13). Same value and
// rationale as ci-agent/devmcp.DefaultHeartbeat — mirrored, not imported.
const DefaultHeartbeat = 15 * time.Second

// ToolHandler is a function that handles an MCP tool call. progress reports
// the latest human-readable progress line for a long-running call; it is
// never nil — buffered (non-SSE) calls receive a no-op func(string) {}.
type ToolHandler func(ctx context.Context, args json.RawMessage, progress func(string)) (any, error)

// Server is an MCP server that dispatches tool calls over HTTP.
// It implements http.Handler using the MCP Streamable HTTP transport,
// answering progress-bearing tools/call requests over SSE with coalescing
// heartbeat notifications (mirrored from ci-agent/devmcp — 04 Task 4).
type Server struct {
	tools     []ToolDef
	handlers  map[string]ToolHandler
	heartbeat time.Duration
}

// NewServer creates an MCP server with no tools registered and the default
// 15s progress heartbeat (existing ATC callers compile unchanged).
func NewServer() *Server {
	return NewServerWithHeartbeat(0)
}

// NewServerWithHeartbeat creates a server with the given progress-heartbeat
// interval; d <= 0 uses DefaultHeartbeat. Sidecars construct via
// NewServerWithHeartbeat(cfg.ProgressInterval).
func NewServerWithHeartbeat(d time.Duration) *Server {
	if d <= 0 {
		d = DefaultHeartbeat
	}
	return &Server{
		handlers:  make(map[string]ToolHandler),
		heartbeat: d,
	}
}

// AddTool registers a tool with the server.
func (s *Server) AddTool(name, description string, schema json.RawMessage, handler ToolHandler) {
	s.tools = append(s.tools, ToolDef{
		Name:        name,
		Description: description,
		InputSchema: schema,
	})
	s.handlers[name] = handler
}

// ServeHTTP implements http.Handler for the MCP Streamable HTTP transport.
// POST requests contain JSON-RPC messages; responses are JSON, or SSE for
// progress-bearing tools/call requests.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, 10*1024*1024))
	if err != nil {
		writeHTTPError(w, -32700, "failed to read request body")
		return
	}

	var req jsonRPCRequest
	if err := json.Unmarshal(body, &req); err != nil {
		writeHTTPError(w, -32700, "parse error")
		return
	}

	if req.Method == "tools/call" {
		s.handleToolsCall(w, r, &req)
		return
	}

	resp := s.dispatch(r.Context(), &req)
	if resp == nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

func (s *Server) dispatch(ctx context.Context, req *jsonRPCRequest) *jsonRPCResponse {
	switch req.Method {
	case "initialize":
		return s.handleInitialize(req)
	case "notifications/initialized":
		return nil
	case "tools/list":
		return s.handleToolsList(req)
	case "ping":
		return &jsonRPCResponse{JSONRPC: "2.0", ID: req.ID, Result: map[string]any{}}
	default:
		return &jsonRPCResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Error:   &jsonRPCError{Code: -32601, Message: fmt.Sprintf("method not found: %s", req.Method)},
		}
	}
}

// handleToolsCall answers a tools/call request: buffered JSON by default, SSE
// with heartbeat progress when the client opts in via Accept: text/event-stream
// AND params._meta.progressToken (the devmcp wire spec, ported verbatim).
// Error mapping is UNCHANGED from the buffered-only server: a handler error is
// an isError=true tool result — never -32602 — in both modes.
func (s *Server) handleToolsCall(w http.ResponseWriter, r *http.Request, req *jsonRPCRequest) {
	var params callToolParams
	if err := json.Unmarshal(req.Params, &params); err != nil {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(&jsonRPCResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Error:   &jsonRPCError{Code: -32602, Message: "invalid params"},
		})
		return
	}

	handler, ok := s.handlers[params.Name]
	if !ok {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(s.toolErrorResponse(req.ID, fmt.Sprintf("unknown tool: %s", params.Name)))
		return
	}

	flusher, canFlush := w.(http.Flusher)
	wantSSE := canFlush &&
		strings.Contains(r.Header.Get("Accept"), "text/event-stream") &&
		params.Meta != nil && len(params.Meta.ProgressToken) > 0

	if !wantSSE {
		result, err := handler(r.Context(), params.Arguments, func(string) {})
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(s.toolResponse(req.ID, result, err))
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	progressCh := make(chan string, 64)
	done := make(chan *jsonRPCResponse, 1)
	go func() {
		result, err := handler(r.Context(), params.Arguments, func(msg string) {
			select {
			case progressCh <- msg:
			default: // never block the running tool on a slow consumer
			}
		})
		done <- s.toolResponse(req.ID, result, err)
	}()

	emit := func(msg string) {
		writeSSE(w, &jsonRPCNotification{
			JSONRPC: "2.0",
			Method:  "notifications/progress",
			Params: map[string]any{
				"progressToken": params.Meta.ProgressToken,
				"message":       msg,
			},
		})
		flusher.Flush()
	}

	ticker := time.NewTicker(s.heartbeat)
	defer ticker.Stop()
	lastMsg := fmt.Sprintf("running %s", params.Name)
	for {
		select {
		case msg := <-progressCh:
			lastMsg = msg // coalesce: remember, emit on the next tick
		case <-ticker.C:
			emit(lastMsg)
		case resp := <-done:
			writeSSE(w, resp)
			flusher.Flush()
			return
		}
	}
}

// toolResponse builds the tools/call response with the buffered path's exact
// (frozen) error mapping: handler error => isError=true tool result.
func (s *Server) toolResponse(id json.RawMessage, result any, err error) *jsonRPCResponse {
	if err != nil {
		return s.toolErrorResponse(id, fmt.Sprintf("error: %s", err.Error()))
	}
	resultJSON, merr := json.Marshal(result)
	if merr != nil {
		return s.toolErrorResponse(id, fmt.Sprintf("error marshaling result: %s", merr.Error()))
	}
	return &jsonRPCResponse{
		JSONRPC: "2.0",
		ID:      id,
		Result: callToolResult{
			Content: []contentBlock{{Type: "text", Text: string(resultJSON)}},
		},
	}
}

func (s *Server) toolErrorResponse(id json.RawMessage, msg string) *jsonRPCResponse {
	return &jsonRPCResponse{
		JSONRPC: "2.0",
		ID:      id,
		Result: callToolResult{
			Content: []contentBlock{{Type: "text", Text: msg}},
			IsError: true,
		},
	}
}

func writeSSE(w io.Writer, msg any) {
	data, _ := json.Marshal(msg)
	fmt.Fprintf(w, "event: message\ndata: %s\n\n", data)
}
```

- [ ] Mechanically update every handler literal to the 3-arg signature (they all ignore `progress`; the signature is uniform across the tree — verified by grep):

```bash
perl -pi -e 's/func\(ctx context\.Context, args json\.RawMessage\) \(any, error\)/func(ctx context.Context, args json.RawMessage, _ func(string)) (any, error)/g' \
  atc/api/mcpserver/tools.go atc/api/mcpserver/tools_test.go atc/api/mcpserver/server_test.go
grep -rn "json.RawMessage) (any, error)" atc/api/mcpserver/ | grep -v "func(string)" && echo "MISSED A HANDLER" || echo OK
```

  `atc/api/handler.go` calls only `mcpserver.NewServer()` + `mcpserver.RegisterTools(...)` — it compiles unchanged (verified at `atc/api/handler.go:145-146`).

- [ ] Run to verify pass (the whole ATC must still compile — the signature change is breaking inside the package only):

```bash
ginkgo ./atc/api/mcpserver/ && go build ./atc/... && ginkgo ./atc/api/
```

- [ ] Commit:

```bash
git add atc/api/mcpserver/
git commit -m "feat(mcpserver): SSE progress-heartbeat transport mirrored from ci-agent/devmcp (F13)" -m "Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 9c: short-park threshold config — `PLATFORM_MCP_SHORT_PARK_MAX_SECONDS` + `PLATFORM_MCP_PARK_PATH` (PARK-V2 §A/§B1, 2026-07-10)

**Files:**
- Modify: `agent/platformmcp/config.go`
- Test: `agent/platformmcp/config_test.go`

**Why (PARK-V2 §A):** the threshold TIMER is owned by the platform sidecar — it starts the park, holds `asked_at`, and already runs the SSE heartbeat ticker — and applies to BOTH `ask_human` parks and `/checkpoint` parks, measured from the row's `asked_at`. `0` = never exit = pure PARK-V1 (the delta's rollback hatch). Bounds-validation mirrors the rest of this file: set-but-invalid or negative is FATAL at startup, never clamped; the existing `park`+0 TIMEOUT-POLICY rule stays legal and orthogonal — the threshold decides WHERE the wait lives (SSE park vs exited step), not whether it resolves. `PLATFORM_MCP_PARK_PATH` is the §B1 sentinel destination; unset = never write, which is the LEGAL checkpoint-pod shape (no flight volume there; the `202` response is that pod's exit signal), so threshold-without-path is NOT a startup error — an `ask_human` crossing without a path instead degrades LOUDLY at crossing time (Task 11b).

- [ ] Write the failing test — append to `agent/platformmcp/config_test.go`:

```go
// TestConfigFromEnvShortParkMax (PARK-V2 §A): PLATFORM_MCP_SHORT_PARK_MAX_SECONDS
// is integer seconds, rendered literally by dispatch from --agent-short-park-max;
// unset or "0" = never exit (pure PARK-V1 — the rollback hatch); negative or
// garbage is FATAL at startup, never clamped. PLATFORM_MCP_PARK_PATH rides
// along verbatim, and its absence is legal (checkpoint pods).
func TestConfigFromEnvShortParkMax(t *testing.T) {
	base := func() {
		t.Setenv("ATC_EXTERNAL_URL", "https://concourse.home")
		t.Setenv("AGENT_PRINCIPAL_TOKEN", "cap1.9.secret")
		t.Setenv("AGENT_TICKET_ID", "42")
	}

	base()
	cfg, err := platformmcp.ConfigFromEnv()
	if err != nil {
		t.Fatalf("ConfigFromEnv: %v", err)
	}
	if cfg.ShortParkMax != 0 || cfg.ParkPath != "" {
		t.Fatalf("unset short-park env: expected zero values, got %+v", cfg)
	}

	base()
	t.Setenv("PLATFORM_MCP_SHORT_PARK_MAX_SECONDS", "1800")
	t.Setenv("PLATFORM_MCP_PARK_PATH", "/flight/park.json")
	cfg, err = platformmcp.ConfigFromEnv()
	if err != nil {
		t.Fatalf("ConfigFromEnv: %v", err)
	}
	if cfg.ShortParkMax != 30*time.Minute || cfg.ParkPath != "/flight/park.json" {
		t.Fatalf("expected 30m + /flight/park.json, got %+v", cfg)
	}

	base()
	t.Setenv("PLATFORM_MCP_SHORT_PARK_MAX_SECONDS", "0")
	cfg, err = platformmcp.ConfigFromEnv()
	if err != nil || cfg.ShortParkMax != 0 {
		t.Fatalf("explicit 0 must mean never-exit: %v %+v", err, cfg)
	}

	// Threshold WITHOUT a park path is the legal checkpoint-pod shape.
	base()
	t.Setenv("PLATFORM_MCP_SHORT_PARK_MAX_SECONDS", "1800")
	if _, err := platformmcp.ConfigFromEnv(); err != nil {
		t.Fatalf("threshold without PLATFORM_MCP_PARK_PATH must be legal (checkpoint pods): %v", err)
	}

	for _, bad := range []string{"-1", "bogus", "30m"} {
		base()
		t.Setenv("PLATFORM_MCP_SHORT_PARK_MAX_SECONDS", bad)
		if _, err := platformmcp.ConfigFromEnv(); err == nil {
			t.Fatalf("PLATFORM_MCP_SHORT_PARK_MAX_SECONDS=%q: expected fatal error, got nil", bad)
		}
	}
}
```

- [ ] Run to verify it fails:

```bash
go test ./agent/platformmcp/
```

Expected failure: `cfg.ShortParkMax undefined`.

- [ ] Add the fields to `Config` in `agent/platformmcp/config.go` (after `ProgressInterval`):

```go
	// ShortParkMax is the PARK-V2 §A exit-and-respawn threshold
	// (PLATFORM_MCP_SHORT_PARK_MAX_SECONDS, integer seconds — rendered
	// literally by dispatch from the web flag --agent-short-park-max).
	// 0 = never exit: every park stays a PARK-V1 SSE park (the delta's
	// rollback hatch). Applies to BOTH ask_human and /checkpoint parks,
	// measured from the question row's asked_at.
	ShortParkMax time.Duration
	// ParkPath is the §B1 park-sentinel destination (PLATFORM_MCP_PARK_PATH,
	// Task 1 addendum) — `<flight mount>/park.json` in agent-step pods, set
	// by the agent-step exec via SidecarEnv (F15; plan 07 Task 26 — only the
	// exec knows the flight mount path), where the agent-runner's 5s stat
	// loop watches for it. "" = never write a sentinel: the legal
	// checkpoint-pod shape (no flight volume; the 202 response is the exit
	// signal there). An agent-step pod missing this env is an agent-step
	// exec bug — Task 11b logs the degradation loudly at crossing.
	ParkPath string
```

  and the parsing at the end of `ConfigFromEnv` (after the progress-interval block, before `return`):

```go
	// PARK-V2 §A: bounds-validate like the rest of the env contract — a
	// set-but-invalid or negative threshold is FATAL at startup, never
	// clamped. Integer SECONDS, not a Go duration: dispatch renders the
	// flag's rounded seconds literally.
	shortParkSecs, err := intEnv("PLATFORM_MCP_SHORT_PARK_MAX_SECONDS")
	if err != nil {
		return cfg, err
	}
	if shortParkSecs < 0 {
		return cfg, fmt.Errorf("PLATFORM_MCP_SHORT_PARK_MAX_SECONDS must be >= 0 (0 = never exit-and-respawn), got %d", shortParkSecs)
	}
	cfg.ShortParkMax = time.Duration(shortParkSecs) * time.Second
	cfg.ParkPath = os.Getenv("PLATFORM_MCP_PARK_PATH")
```

- [ ] Run to verify pass:

```bash
go test ./agent/platformmcp/
```

- [ ] Commit:

```bash
git add agent/platformmcp/config.go agent/platformmcp/config_test.go
git commit -m "feat(platform-mcp): short-park threshold + park-sentinel path config (PARK-V2 §A/§B1)" -m "Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 10: MCP server assembly + read_ticket / list_tasks / get_task / submit_spec / submit_plan / update_task_status

**Files:**
- Create: `agent/platformmcp/server.go`
- Create: `agent/platformmcp/tools.go`
- Test: `agent/platformmcp/tools_test.go`

The MCP protocol layer is `atc/api/mcpserver` **as upgraded by Task 9b** (SSE progress heartbeats; 3-arg `ToolHandler` with `progress func(string)`; `NewServerWithHeartbeat`, `AddTool(name, description, schema, handler)`, `ServeHTTP`, `MustJSON`). The sidecar constructs it via `mcpserver.NewServerWithHeartbeat(cfg.ProgressInterval)` so `PLATFORM_MCP_PROGRESS_INTERVAL` (Task 9) drives the heartbeat; 0 = the frozen 15s default. Tool input schemas below are byte-for-byte the §3.2 contract.

This task registers all seven tools; six are implemented here (`read_ticket`, `list_tasks`, `get_task`, `submit_spec`, `submit_plan`, `update_task_status`) and `ask_human` is stubbed (Task 11). The three read tools project the single `GetAgentTicket` payload (`ATCClient.GetTicket`, Task 9): `read_ticket` returns envelope + spec and DROPS tasks; `list_tasks` returns the `{ordering,title,status}` skeleton with no detail bodies; `get_task(ordering)` returns one task including `detail_md`, or an MCP tool error (`isError=true`) on an unknown ordering — the shared `atc/api/mcpserver` maps a handler's returned error to a `tools/call` result with `isError=true`, so an unknown ordering is a tool-level error, NOT a JSON-RPC `-32602` error object.

- [ ] Write the failing test `agent/platformmcp/tools_test.go`. The helper drives the server exactly the way an MCP client does — JSON-RPC `tools/call` POSTs against `ServeHTTP` — with a stub ATC behind it:

```go
package platformmcp_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/concourse/concourse/agent/platformmcp"
)

// callTool posts a JSON-RPC tools/call and returns (resultJSON, isError).
// mcpserver wraps tool results as {content: [{type: "text", text: <json>}]}.
func callTool(t *testing.T, h http.Handler, tool string, args any) (json.RawMessage, bool) {
	t.Helper()
	argsRaw, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	body := fmt.Sprintf(
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":%q,"arguments":%s}}`,
		tool, argsRaw)
	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(body))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("tools/call %s: HTTP %d: %s", tool, w.Code, w.Body.String())
	}
	var resp struct {
		Result struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
			IsError bool `json:"isError"`
		} `json:"result"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode rpc response: %v: %s", err, w.Body.String())
	}
	if len(resp.Result.Content) == 0 {
		t.Fatalf("empty content for %s: %s", tool, w.Body.String())
	}
	return json.RawMessage(resp.Result.Content[0].Text), resp.Result.IsError
}

// stubTicketATC fakes ticket-core's routes for ticket 42.
func stubTicketATC(t *testing.T) (*httptest.Server, *map[string]any) {
	t.Helper()
	recorded := map[string]any{}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/agent/tickets/42", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"ticket":{"id":42,"title":"fix flaky test","state":"running"},`+
			`"spec":{"title":"Fix flake","acceptance_criteria":["green 10x"],"body_md":"## Rationale"},`+
			`"tasks":[`+
			`{"ordering":1,"title":"write failing test","status":"done","detail_md":"repro the flake"},`+
			`{"ordering":2,"title":"fix","status":"pending","detail_md":"see spec"}]}`)
	})
	mux.HandleFunc("POST /api/v1/agent/tickets/42/spec", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		recorded["spec"] = body
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"version":3}`)
	})
	mux.HandleFunc("POST /api/v1/agent/tickets/42/plan", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		recorded["plan"] = body
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"plan_version":2}`)
	})
	mux.HandleFunc("PUT /api/v1/agent/tickets/42/tasks/3", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		recorded["task"] = body
		w.WriteHeader(http.StatusOK)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, &recorded
}

func newTestServer(t *testing.T, atcURL string) *platformmcp.Server {
	t.Helper()
	srv, err := platformmcp.NewServer(platformmcp.Config{
		ATCURL:         atcURL,
		PrincipalToken: "cap1.9.secret",
		TicketID:       42,
		BuildID:        1001,
		StepName:       "implement",
		TimeoutPolicy:  "park",
		ListenAddr:     ":0",
	})
	if err != nil {
		t.Fatal(err)
	}
	return srv
}

func TestToolsListExposesExactlySevenTools(t *testing.T) {
	atc, _ := stubTicketATC(t)
	srv := newTestServer(t, atc.URL)

	req := httptest.NewRequest(http.MethodPost, "/mcp",
		strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
	w := httptest.NewRecorder()
	srv.Mux().ServeHTTP(w, req)
	var resp struct {
		Result struct {
			Tools []struct {
				Name string `json:"name"`
			} `json:"tools"`
		} `json:"result"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tool := range resp.Result.Tools {
		names = append(names, tool.Name)
	}
	want := []string{"read_ticket", "list_tasks", "get_task", "submit_spec", "submit_plan", "update_task_status", "ask_human"}
	if fmt.Sprint(names) != fmt.Sprint(want) {
		t.Fatalf("tools = %v, want %v", names, want)
	}
}

func TestReadTicketReturnsEnvelopeAndSpecOnly(t *testing.T) {
	atc, _ := stubTicketATC(t)
	srv := newTestServer(t, atc.URL)

	result, isErr := callTool(t, srv.Mux(), "read_ticket", map[string]any{})
	if isErr {
		t.Fatalf("read_ticket errored: %s", result)
	}
	var out struct {
		Ticket struct {
			ID    int    `json:"id"`
			Title string `json:"title"`
		} `json:"ticket"`
		Spec *struct {
			Title string `json:"title"`
		} `json:"spec"`
	}
	if err := json.Unmarshal(result, &out); err != nil {
		t.Fatal(err)
	}
	if out.Ticket.ID != 42 || out.Ticket.Title != "fix flaky test" {
		t.Fatalf("unexpected ticket: %+v", out)
	}
	if out.Spec == nil || out.Spec.Title != "Fix flake" {
		t.Fatalf("expected spec embedded, got %+v", out.Spec)
	}
	// tasks MUST NOT appear in read_ticket's result (§3.2 read model).
	var probe map[string]json.RawMessage
	if err := json.Unmarshal(result, &probe); err != nil {
		t.Fatal(err)
	}
	if _, ok := probe["tasks"]; ok {
		t.Fatalf("read_ticket must not include tasks, got %s", result)
	}
}

func TestListTasksReturnsSkeletonWithoutDetail(t *testing.T) {
	atc, _ := stubTicketATC(t)
	srv := newTestServer(t, atc.URL)

	result, isErr := callTool(t, srv.Mux(), "list_tasks", map[string]any{})
	if isErr {
		t.Fatalf("list_tasks errored: %s", result)
	}
	var out struct {
		Tasks []map[string]json.RawMessage `json:"tasks"`
	}
	if err := json.Unmarshal(result, &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Tasks) != 2 {
		t.Fatalf("expected 2 tasks, got %+v", out.Tasks)
	}
	for _, task := range out.Tasks {
		if _, ok := task["detail_md"]; ok {
			t.Fatalf("list_tasks must omit detail_md, got %v", task)
		}
		for _, key := range []string{"ordering", "title", "status"} {
			if _, ok := task[key]; !ok {
				t.Fatalf("list_tasks task missing %q: %v", key, task)
			}
		}
	}
}

func TestGetTaskReturnsDetailAndErrorsOnUnknownOrdering(t *testing.T) {
	atc, _ := stubTicketATC(t)
	srv := newTestServer(t, atc.URL)

	result, isErr := callTool(t, srv.Mux(), "get_task", map[string]any{"ordering": 2})
	if isErr {
		t.Fatalf("get_task errored: %s", result)
	}
	var task struct {
		Ordering int    `json:"ordering"`
		Title    string `json:"title"`
		Status   string `json:"status"`
		DetailMD string `json:"detail_md"`
	}
	if err := json.Unmarshal(result, &task); err != nil {
		t.Fatal(err)
	}
	if task.Ordering != 2 || task.Title != "fix" || task.Status != "pending" || task.DetailMD != "see spec" {
		t.Fatalf("unexpected task: %+v", task)
	}

	// Unknown ordering is an MCP tool error (isError=true) — the shared mcpserver
	// maps the handler's returned error to a tools/call result with isError=true,
	// NOT a JSON-RPC -32602 error object. Assert both the isError flag AND that the
	// tool-result content names the ordering, and that no top-level JSON-RPC error
	// (which -32602 would carry) is present.
	body := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"get_task","arguments":{"ordering":99}}}`
	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(body))
	w := httptest.NewRecorder()
	srv.Mux().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("unknown-ordering call: HTTP %d: %s", w.Code, w.Body.String())
	}
	var resp struct {
		Error  *struct {
			Code int `json:"code"`
		} `json:"error"`
		Result struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
			IsError bool `json:"isError"`
		} `json:"result"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode rpc response: %v: %s", err, w.Body.String())
	}
	if resp.Error != nil {
		t.Fatalf("unknown ordering must be a tool error, not a JSON-RPC error object (got code %d)", resp.Error.Code)
	}
	if !resp.Result.IsError {
		t.Fatalf("expected tool result isError=true for unknown ordering, got %s", w.Body.String())
	}
	if len(resp.Result.Content) == 0 || !strings.Contains(resp.Result.Content[0].Text, "99") {
		t.Fatalf("expected error content naming the unknown ordering, got %s", w.Body.String())
	}
}

func TestSubmitSpecValidatesAndForwards(t *testing.T) {
	atc, recorded := stubTicketATC(t)
	srv := newTestServer(t, atc.URL)

	if result, isErr := callTool(t, srv.Mux(), "submit_spec", map[string]any{"body": "no title"}); !isErr {
		t.Fatalf("expected input error, got %s", result)
	}

	result, isErr := callTool(t, srv.Mux(), "submit_spec", map[string]any{
		"title":               "Fix the flaky spec",
		"body":                "## Rationale\n...",
		"acceptance_criteria": []string{"suite green 10x"},
	})
	if isErr {
		t.Fatalf("submit_spec errored: %s", result)
	}
	var out struct {
		Version int `json:"version"`
	}
	json.Unmarshal(result, &out)
	if out.Version != 3 {
		t.Fatalf("expected version 3, got %+v", out)
	}
	spec := (*recorded)["spec"].(map[string]any)
	if spec["title"] != "Fix the flaky spec" {
		t.Fatalf("spec not forwarded: %v", spec)
	}
}

func TestSubmitPlanAndUpdateTaskStatus(t *testing.T) {
	atc, recorded := stubTicketATC(t)
	srv := newTestServer(t, atc.URL)

	if result, isErr := callTool(t, srv.Mux(), "submit_plan", map[string]any{"tasks": []any{}}); !isErr {
		t.Fatalf("expected minItems error, got %s", result)
	}

	result, isErr := callTool(t, srv.Mux(), "submit_plan", map[string]any{
		"tasks": []map[string]string{{"title": "write failing test"}, {"title": "fix", "detail": "see spec"}},
	})
	if isErr {
		t.Fatalf("submit_plan errored: %s", result)
	}
	var planOut struct {
		PlanVersion int `json:"plan_version"`
	}
	json.Unmarshal(result, &planOut)
	if planOut.PlanVersion != 2 {
		t.Fatalf("expected plan_version 2, got %+v", planOut)
	}

	if result, isErr := callTool(t, srv.Mux(), "update_task_status",
		map[string]any{"ordering": 3, "status": "nope"}); !isErr {
		t.Fatalf("expected status enum error, got %s", result)
	}
	result, isErr = callTool(t, srv.Mux(), "update_task_status",
		map[string]any{"ordering": 3, "status": "done", "note": "all green"})
	if isErr {
		t.Fatalf("update_task_status errored: %s", result)
	}
	task := (*recorded)["task"].(map[string]any)
	if task["status"] != "done" || task["note"] != "all green" {
		t.Fatalf("task update not forwarded: %v", task)
	}
}

func TestHealthz(t *testing.T) {
	atc, _ := stubTicketATC(t)
	srv := newTestServer(t, atc.URL)
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	w := httptest.NewRecorder()
	srv.Mux().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("healthz: %d", w.Code)
	}
}
```

- [ ] Run to verify it fails:

```bash
go test ./agent/platformmcp/
```

Expected failure: `undefined: platformmcp.NewServer`.

- [ ] Write `agent/platformmcp/server.go`:

```go
package platformmcp

import (
	"net/http"

	"github.com/concourse/concourse/atc/api/mcpserver"
)

// Server assembles the sidecar: the MCP endpoint at POST /mcp, the pod
// readiness probe at GET /healthz (§8.5), and the internal checkpoint
// endpoint at POST /checkpoint (Task 14 / §3.2 addendum).
type Server struct {
	cfg    Config
	client *ATCClient
	events *EventLog
	mcp    *mcpserver.Server
	mux    *http.ServeMux
}

func NewServer(cfg Config) (*Server, error) {
	events, err := NewEventLog(cfg.EventsPath)
	if err != nil {
		return nil, err
	}
	s := &Server{
		cfg:    cfg,
		client: NewATCClient(cfg.ATCURL, cfg.PrincipalToken, cfg.TicketID),
		events: events,
		// SSE heartbeat interval from PLATFORM_MCP_PROGRESS_INTERVAL; 0 =
		// mcpserver.DefaultHeartbeat (15s). Task 9b's upgraded server keeps a
		// parked ask_human alive past the claude CLI's 60s abandonment (F13).
		mcp: mcpserver.NewServerWithHeartbeat(cfg.ProgressInterval),
		mux: http.NewServeMux(),
	}
	s.registerTools()
	s.mux.Handle("/mcp", s.mcp)
	s.mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	s.mux.HandleFunc("POST /checkpoint", s.handleCheckpoint)
	return s, nil
}

func (s *Server) Mux() *http.ServeMux { return s.mux }

// ListenAndServe serves /mcp, /healthz, and /checkpoint. SERVER-TIMEOUT RULE
// (D4, 2026-07-09 SSE seam delta — frozen for all three sidecar binaries):
// WriteTimeout and IdleTimeout MUST be 0 — any nonzero WriteTimeout severs
// long SSE streams and blocking /checkpoint responses mid-park.
// ReadHeaderTimeout 5s is allowed (and set) to bound slow-header abuse.
func (s *Server) ListenAndServe() error {
	srv := &http.Server{
		Addr:              s.cfg.ListenAddr,
		Handler:           s.mux,
		ReadHeaderTimeout: 5 * time.Second,
		WriteTimeout:      0,
		IdleTimeout:       0,
	}
	return srv.ListenAndServe()
}
```

  (add `"time"` to `server.go`'s imports).

  So this task compiles and passes in-order, temporarily define in `server.go` (Task 12 replaces `EventLog` with the real NDJSON writer — its tests fail against this no-op; Task 14 replaces `handleCheckpoint` — its tests fail against the 501; neither survives):

```go
// Temporary in-order bridge: replaced by Task 12 (EventLog) / Task 14
// (handleCheckpoint).
type EventLog struct{}

func NewEventLog(string) (*EventLog, error) { return &EventLog{}, nil }

// Emit is a no-op until Task 12. Call sites pass untyped string constants,
// so they compile unchanged against Task 12's schema.EventType signature.
func (l *EventLog) Emit(eventType string, data map[string]interface{}) {}

func (s *Server) handleCheckpoint(w http.ResponseWriter, r *http.Request) {
	http.Error(w, "checkpoint endpoint lands in Task 14", http.StatusNotImplemented)
}
```

- [ ] Write `agent/platformmcp/tools.go` (schemas verbatim from §3.2; validation mirrors the schemas so misuse is an MCP-level error, per the §3 taxonomy):

```go
package platformmcp

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/concourse/concourse/atc/api/mcpserver"
)

var taskStatuses = map[string]bool{
	"pending": true, "in_progress": true, "done": true, "skipped": true, "blocked": true,
}

func (s *Server) registerTools() {
	s.mcp.AddTool("read_ticket",
		"Read this run's ticket: envelope and latest spec (call list_tasks / get_task for the plan).",
		mcpserver.MustJSON(map[string]any{
			"type": "object", "properties": map[string]any{}, "additionalProperties": false,
		}),
		s.readTicket)

	s.mcp.AddTool("list_tasks",
		"List the active plan's tasks (ordering, title, status) — a cheap skeleton with no detail bodies.",
		mcpserver.MustJSON(map[string]any{
			"type": "object", "properties": map[string]any{}, "additionalProperties": false,
		}),
		s.listTasks)

	s.mcp.AddTool("get_task",
		"Get one active-plan task by its ordering, including its detail_md body.",
		mcpserver.MustJSON(map[string]any{
			"type":     "object",
			"required": []string{"ordering"},
			"properties": map[string]any{
				"ordering": map[string]any{"type": "integer", "description": "task position in the active plan"},
			},
			"additionalProperties": false,
		}),
		s.getTask)

	s.mcp.AddTool("submit_spec",
		"Submit the spec for this ticket. Structure enters here — never as markdown files.",
		mcpserver.MustJSON(map[string]any{
			"type":     "object",
			"required": []string{"title", "body"},
			"properties": map[string]any{
				"title": map[string]any{"type": "string"},
				"body":  map[string]any{"type": "string", "description": "markdown; rationale and tradeoffs belong here"},
				"acceptance_criteria": map[string]any{
					"type": "array", "items": map[string]any{"type": "string"}, "minItems": 1,
				},
				"links": map[string]any{
					"type": "array",
					"items": map[string]any{
						"type": "object", "required": []string{"title", "url"},
						"properties": map[string]any{
							"title": map[string]any{"type": "string"},
							"url":   map[string]any{"type": "string"},
						},
					},
				},
			},
			"additionalProperties": false,
		}),
		s.submitSpec)

	s.mcp.AddTool("submit_plan",
		"Replace the active plan with an ordered task list (orderings 1..N as given).",
		mcpserver.MustJSON(map[string]any{
			"type":     "object",
			"required": []string{"tasks"},
			"properties": map[string]any{
				"tasks": map[string]any{
					"type": "array", "minItems": 1,
					"items": map[string]any{
						"type": "object", "required": []string{"title"},
						"properties": map[string]any{
							"title":  map[string]any{"type": "string"},
							"detail": map[string]any{"type": "string", "description": "optional markdown"},
						},
					},
				},
			},
			"additionalProperties": false,
		}),
		s.submitPlan)

	s.mcp.AddTool("update_task_status",
		"Update one active-plan task's status by its ordering.",
		mcpserver.MustJSON(map[string]any{
			"type":     "object",
			"required": []string{"ordering", "status"},
			"properties": map[string]any{
				"ordering": map[string]any{"type": "integer", "minimum": 1, "description": "task position in the active plan"},
				"status":   map[string]any{"type": "string", "enum": []string{"pending", "in_progress", "done", "skipped", "blocked"}},
				"note":     map[string]any{"type": "string"},
			},
			"additionalProperties": false,
		}),
		s.updateTaskStatus)

	// PARK-V2 §E tool-description note (2026-07-10): repeated byte-identical
	// questions within one step are idempotent — the description must say so.
	s.mcp.AddTool("ask_human",
		"Ask the human a question; this call BLOCKS (parks the run) until answered. Repeated byte-identical questions within one step return the FIRST answer (idempotent-by-question) — vary the question text if you genuinely need a fresh answer.",
		mcpserver.MustJSON(map[string]any{
			"type":     "object",
			"required": []string{"question"},
			"properties": map[string]any{
				"question": map[string]any{"type": "string", "description": "markdown; include enough context to answer without reading the transcript"},
				"options":  map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "optional multiple choice; empty = free text"},
				"default":  map[string]any{"type": "string", "description": "answer used if the question times out under timeout_policy=default"},
			},
			"additionalProperties": false,
		}),
		s.askHuman)
}

// readTicket returns envelope + spec ONLY — tasks are deliberately dropped from
// this result (§3.2 read model). Agents reach the plan through list_tasks /
// get_task so the whole plan is never dumped into context.
func (s *Server) readTicket(ctx context.Context, _ json.RawMessage, _ func(string)) (any, error) {
	payload, err := s.client.GetTicket(ctx)
	if err != nil {
		return nil, fmt.Errorf("reading ticket: %w", err)
	}
	out := map[string]any{"ticket": payload.Ticket}
	if len(payload.Spec) > 0 {
		out["spec"] = payload.Spec
	} else {
		out["spec"] = nil
	}
	return out, nil
}

// listTasks returns the cheap task skeleton — {ordering, title, status} with no
// detail bodies (§3.2). It backs onto ticket-core's ActivePlan via GetAgentTicket.
func (s *Server) listTasks(ctx context.Context, _ json.RawMessage, _ func(string)) (any, error) {
	payload, err := s.client.GetTicket(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing tasks: %w", err)
	}
	type taskSkeleton struct {
		Ordering int    `json:"ordering"`
		Title    string `json:"title"`
		Status   string `json:"status"`
	}
	skeleton := make([]taskSkeleton, 0, len(payload.Tasks))
	for _, task := range payload.Tasks {
		skeleton = append(skeleton, taskSkeleton{
			Ordering: task.Ordering, Title: task.Title, Status: task.Status,
		})
	}
	return map[string]any{"tasks": skeleton}, nil
}

// getTask returns one active-plan task including its detail_md. An unknown
// ordering returns a handler error, which the shared atc/api/mcpserver maps to
// an MCP tool error — a tools/call result with isError=true carrying the error
// text (§3.2). This is a tool-level error, NOT a JSON-RPC -32602 error object;
// the mcpserver only emits -32602 for a malformed tools/call envelope.
func (s *Server) getTask(ctx context.Context, args json.RawMessage, _ func(string)) (any, error) {
	var in struct {
		Ordering int `json:"ordering"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return nil, fmt.Errorf("invalid arguments: %w", err)
	}
	payload, err := s.client.GetTicket(ctx)
	if err != nil {
		return nil, fmt.Errorf("getting task: %w", err)
	}
	for _, task := range payload.Tasks {
		if task.Ordering == in.Ordering {
			return map[string]any{
				"ordering":  task.Ordering,
				"title":     task.Title,
				"status":    task.Status,
				"detail_md": task.DetailMD,
			}, nil
		}
	}
	return nil, fmt.Errorf("no task with ordering %d in the active plan", in.Ordering)
}

func (s *Server) submitSpec(ctx context.Context, args json.RawMessage, _ func(string)) (any, error) {
	var in struct {
		Title              string              `json:"title"`
		Body               string              `json:"body"`
		AcceptanceCriteria []string            `json:"acceptance_criteria"`
		Links              []map[string]string `json:"links"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return nil, fmt.Errorf("invalid arguments: %w", err)
	}
	if in.Title == "" || in.Body == "" {
		return nil, fmt.Errorf("title and body are required")
	}
	version, err := s.client.SubmitSpec(ctx, SpecSubmission{
		Title: in.Title, Body: in.Body,
		AcceptanceCriteria: in.AcceptanceCriteria, Links: in.Links,
	})
	if err != nil {
		return nil, fmt.Errorf("submitting spec: %w", err)
	}
	return map[string]int{"version": version}, nil
}

func (s *Server) submitPlan(ctx context.Context, args json.RawMessage, _ func(string)) (any, error) {
	var in struct {
		Tasks []TaskSubmission `json:"tasks"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return nil, fmt.Errorf("invalid arguments: %w", err)
	}
	if len(in.Tasks) == 0 {
		return nil, fmt.Errorf("tasks requires at least one entry")
	}
	for i, task := range in.Tasks {
		if task.Title == "" {
			return nil, fmt.Errorf("tasks[%d].title is required", i)
		}
	}
	planVersion, err := s.client.SubmitPlan(ctx, in.Tasks)
	if err != nil {
		return nil, fmt.Errorf("submitting plan: %w", err)
	}
	return map[string]int{"plan_version": planVersion}, nil
}

func (s *Server) updateTaskStatus(ctx context.Context, args json.RawMessage, _ func(string)) (any, error) {
	var in struct {
		Ordering int    `json:"ordering"`
		Status   string `json:"status"`
		Note     string `json:"note"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return nil, fmt.Errorf("invalid arguments: %w", err)
	}
	if in.Ordering < 1 {
		return nil, fmt.Errorf("ordering must be >= 1")
	}
	if !taskStatuses[in.Status] {
		return nil, fmt.Errorf("invalid status %q", in.Status)
	}
	if err := s.client.UpdateTaskStatus(ctx, in.Ordering, in.Status, in.Note); err != nil {
		return nil, fmt.Errorf("updating task: %w", err)
	}
	return map[string]bool{"ok": true}, nil
}
```

  `askHuman` is implemented in Task 11 — for this task's compile, add a temporary body in `tools.go` (`func (s *Server) askHuman(ctx context.Context, args json.RawMessage, progress func(string)) (any, error)` — the Task 9b 3-arg signature) returning `nil, fmt.Errorf("ask_human lands in Task 11")` and REPLACE it in Task 11 (the Task 11 test would fail against the temporary body, so nothing placeholder survives).

- [ ] Run to verify pass (the `ask_human` presence assertion in `TestToolsListExposesExactlySevenTools` passes because the tool is registered; only Task 11 exercises its behavior):

```bash
go test ./agent/platformmcp/
```

- [ ] Commit:

```bash
git add agent/platformmcp/
git commit -m "feat(platform-mcp): MCP server with read_ticket/list_tasks/get_task + spec/plan/task writes" -m "Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 11: `ask_human` — park, resume, timeout policies

**Files:**
- Create: `agent/platformmcp/askhuman.go` (replaces the Task 10 temporary body in `tools.go`)
- Test: `agent/platformmcp/askhuman_test.go`

- [ ] Write the failing test `agent/platformmcp/askhuman_test.go` (reuses `callTool` and the questions stub from Tasks 9/10; the stub ATC combines ticket routes + question routes):

```go
package platformmcp_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/concourse/concourse/agent/api/questions"
	"github.com/concourse/concourse/agent/platformmcp"
)

// fullStubATC = a ticket read route + the real question routes on one server.
func fullStubATC(t *testing.T, store *questions.MemoryStore) *httptest.Server {
	t.Helper()
	qmux := stubQuestionMux(t, store)
	mux := http.NewServeMux()
	mux.Handle("/api/v1/agent/tickets/42/questions", qmux)
	mux.Handle("/api/v1/agent/tickets/42/questions/", qmux)
	mux.HandleFunc("GET /api/v1/agent/tickets/42", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"ticket":{"id":42,"title":"fix flaky test","state":"running"},"spec":null,"tasks":[]}`)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func newAskServer(t *testing.T, atcURL, policy string, timeoutSeconds int) *platformmcp.Server {
	t.Helper()
	srv, err := platformmcp.NewServer(platformmcp.Config{
		ATCURL:         atcURL,
		PrincipalToken: "cap1.9.secret",
		TicketID:       42,
		BuildID:        1001,
		StepName:       "implement",
		TimeoutPolicy:  policy,
		TimeoutSeconds: timeoutSeconds,
		ListenAddr:     ":0",
	})
	if err != nil {
		t.Fatal(err)
	}
	srv.TunePolling(50*time.Millisecond, 20*time.Millisecond)
	return srv
}

func TestAskHumanParksUntilAnswered(t *testing.T) {
	store := questions.NewMemoryStore()
	atc := fullStubATC(t, store)
	srv := newAskServer(t, atc.URL, "park", 0)

	// Answer the (only) question shortly after it appears.
	go func() {
		for i := 0; i < 100; i++ {
			open, _ := store.OpenForTicket(42)
			if len(open) == 1 {
				_ = store.Answer(42, open[0].ID, "oidc", "tdm")
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
	}()

	result, isErr := callTool(t, srv.Mux(), "ask_human", map[string]any{
		"question": "Which auth flow?",
		"options":  []string{"legacy", "oidc"},
	})
	if isErr {
		t.Fatalf("ask_human errored: %s", result)
	}
	if !strings.Contains(string(result), `"answer":"oidc"`) ||
		!strings.Contains(string(result), `"answered_by":"tdm"`) ||
		!strings.Contains(string(result), `"timed_out":false`) {
		t.Fatalf("unexpected result: %s", result)
	}
}

func TestAskHumanTimeoutDefaultPolicy(t *testing.T) {
	store := questions.NewMemoryStore()
	atc := fullStubATC(t, store)
	srv := newAskServer(t, atc.URL, "default", 1)

	result, isErr := callTool(t, srv.Mux(), "ask_human", map[string]any{
		"question": "Proceed?",
		"options":  []string{"yes", "no"},
		"default":  "yes",
	})
	if isErr {
		t.Fatalf("ask_human errored: %s", result)
	}
	if !strings.Contains(string(result), `"answer":"yes"`) ||
		!strings.Contains(string(result), `"timed_out":true`) {
		t.Fatalf("expected timed-out default answer, got: %s", result)
	}
	// The sidecar resolved the row so it never stays open (§3.2).
	open, _ := store.OpenForTicket(42)
	if len(open) != 0 {
		t.Fatalf("timed-out question left open: %+v", open)
	}
	all, _ := store.ListForTicket(42, 10)
	if all[0].AnsweredBy != "platform-mcp" {
		t.Fatalf("expected sidecar resolution, got %+v", all[0])
	}
}

func TestAskHumanTimeoutFailPolicy(t *testing.T) {
	store := questions.NewMemoryStore()
	atc := fullStubATC(t, store)
	srv := newAskServer(t, atc.URL, "fail", 1)

	result, isErr := callTool(t, srv.Mux(), "ask_human", map[string]any{"question": "Proceed?"})
	if !isErr {
		t.Fatalf("expected MCP-level failure, got: %s", result)
	}
	open, _ := store.OpenForTicket(42)
	if len(open) != 0 {
		t.Fatalf("timed-out question left open: %+v", open)
	}
}

func TestAskHumanInputErrors(t *testing.T) {
	store := questions.NewMemoryStore()
	atc := fullStubATC(t, store)

	// policy=default requires the call's default field (§3.2).
	srv := newAskServer(t, atc.URL, "default", 60)
	if result, isErr := callTool(t, srv.Mux(), "ask_human", map[string]any{"question": "Proceed?"}); !isErr {
		t.Fatalf("expected input error without default, got %s", result)
	}
	// default must be one of options when options given.
	if result, isErr := callTool(t, srv.Mux(), "ask_human", map[string]any{
		"question": "Proceed?", "options": []string{"yes", "no"}, "default": "maybe",
	}); !isErr {
		t.Fatalf("expected default-not-in-options error, got %s", result)
	}
	// question required.
	if result, isErr := callTool(t, srv.Mux(), "ask_human", map[string]any{}); !isErr {
		t.Fatalf("expected question-required error, got %s", result)
	}
}

// TestAskHumanPrincipalRejectedFailsLoudly (D6/F31 leg 3): once the per-run
// principal is revoked or expired the ATC 401s every poll; after the frozen
// 12 consecutive auth failures the tool call must fail LOUDLY with a
// "principal rejected:"-prefixed MCP tool error (isError=true) — never park
// forever on a dead principal.
func TestAskHumanPrincipalRejectedFailsLoudly(t *testing.T) {
	store := questions.NewMemoryStore()
	qmux := stubQuestionMux(t, store)
	mux := http.NewServeMux()
	// Asking succeeds; every subsequent poll is rejected (revoked principal).
	mux.Handle("POST /api/v1/agent/tickets/42/questions", qmux)
	mux.HandleFunc("GET /api/v1/agent/tickets/42/questions/", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	})
	atc := httptest.NewServer(mux)
	t.Cleanup(atc.Close)

	srv := newAskServer(t, atc.URL, "park", 0)

	result, isErr := callTool(t, srv.Mux(), "ask_human", map[string]any{"question": "anyone?"})
	if !isErr {
		t.Fatalf("expected MCP tool error after consecutive 401s, got %s", result)
	}
	if !strings.Contains(string(result), "principal rejected:") {
		t.Fatalf("expected 'principal rejected:' prefix in the tool error, got %s", result)
	}
}
```

- [ ] Run to verify it fails:

```bash
go test ./agent/platformmcp/
```

Expected failures: `undefined: (*platformmcp.Server).TunePolling` and `ask_human lands in Task 11` errors in the park test.

- [ ] Write `agent/platformmcp/askhuman.go` and delete the temporary `askHuman` body from `tools.go`:

```go
package platformmcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/concourse/concourse/agent/api/questions"
)

// TunePolling shortens the client long-poll and retry intervals (tests only).
func (s *Server) TunePolling(pollWait, retry time.Duration) {
	s.client.PollWait = pollWait
	s.client.RetryInterval = retry
}

type askHumanInput struct {
	Question string   `json:"question"`
	Options  []string `json:"options"`
	Default  string   `json:"default"`
}

type askHumanResult struct {
	Answer     string `json:"answer"`
	AnsweredBy string `json:"answered_by"`
	TimedOut   bool   `json:"timed_out"`
}

// askHuman implements the §3.2 park/resume protocol: insert the question row,
// emit human.ask, then BLOCK the MCP call on a resilient long-poll until the
// row is answered. The sidecar itself enforces the timeout and, on expiry, is
// the writer that resolves the row (policy default/fail) so a timed-out row
// never stays open. Policy park = no deadline: only a human resolves it.
//
// ask_human is a MUST-stream tool (D4, 2026-07-09 SSE seam delta): it can
// block unboundedly, so it is served over Task 9b's SSE progress path. The
// progress call below sets the parked message once; the server's heartbeat
// ticker repeats the latest message every interval (<60s — the claude CLI's
// empirical abandonment bound, F13). A consecutive-401/403 fatal from
// AwaitAnswer surfaces as a LOUD "principal rejected:" tool error
// (isError=true) instead of an eternal park (D6/F31 leg 3).
func (s *Server) askHuman(ctx context.Context, args json.RawMessage, progress func(string)) (any, error) {
	var in askHumanInput
	if err := json.Unmarshal(args, &in); err != nil {
		return nil, fmt.Errorf("invalid arguments: %w", err)
	}
	if in.Question == "" {
		return nil, fmt.Errorf("question is required")
	}
	if s.cfg.TimeoutPolicy == "default" && in.Default == "" {
		return nil, fmt.Errorf("'default' is required: this workflow's ask_human timeout policy is 'default'")
	}
	if in.Default != "" && len(in.Options) > 0 && !containsOption(in.Options, in.Default) {
		return nil, fmt.Errorf("'default' must be one of options")
	}

	q := s.newQuestion(questions.KindQuestion, in.Question, in.Options, in.Default)
	created, err := s.client.AskQuestion(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("filing question: %w", err)
	}
	s.events.Emit("human.ask", map[string]any{
		"question_id": created.ID,
		"kind":        "question",
		"question":    in.Question,
		"options":     in.Options,
	})
	// Park-start progress line (D4): emitted once here, repeated by the SSE
	// heartbeat ticker for the whole park.
	progress(fmt.Sprintf("parked: waiting for human answer to question %d", created.ID))

	answered, timedOut, err := s.awaitWithPolicy(ctx, created.ID, created.AskedAt, in.Default)
	if err != nil {
		if errors.Is(err, ErrPrincipalRejected) {
			// Fail LOUDLY: the step errors instead of parking forever on a
			// revoked/expired principal (D6/F31 leg 3).
			return nil, fmt.Errorf("principal rejected: %w", err)
		}
		return nil, err
	}
	waitSeconds := time.Now().Unix() - created.AskedAt
	s.events.Emit("human.answer", map[string]any{
		"question_id":  created.ID,
		"answer":       answered.Answer,
		"answered_by":  answered.AnsweredBy,
		"wait_seconds": waitSeconds,
		"timed_out":    timedOut,
	})
	return askHumanResult{Answer: answered.Answer, AnsweredBy: answered.AnsweredBy, TimedOut: timedOut}, nil
}

// awaitWithPolicy blocks until answered or the policy resolves the timeout.
func (s *Server) awaitWithPolicy(ctx context.Context, questionID int, askedAt int64, defaultAnswer string) (*resolvedAnswer, bool, error) {
	var deadline *time.Time
	if s.cfg.TimeoutPolicy != "park" && s.cfg.TimeoutSeconds > 0 {
		d := time.Unix(askedAt, 0).Add(time.Duration(s.cfg.TimeoutSeconds) * time.Second)
		deadline = &d
	}

	q, timedOut, err := s.client.AwaitAnswer(ctx, questionID, deadline)
	if err != nil {
		return nil, false, fmt.Errorf("awaiting answer: %w", err)
	}
	if !timedOut {
		return &resolvedAnswer{Answer: q.Answer, AnsweredBy: q.AnsweredBy}, false, nil
	}

	// Timeout: the sidecar resolves the row (§3.2). A concurrent human answer
	// wins the Answer race (409) — in that case fetch and use theirs.
	resolution := ""
	if s.cfg.TimeoutPolicy == "default" {
		resolution = defaultAnswer
	}
	answerErr := s.client.AnswerQuestion(ctx, questionID, resolution, "platform-mcp")
	if answerErr != nil {
		if latest, gerr := s.client.GetQuestion(ctx, questionID, 0); gerr == nil && latest.AnsweredAt != 0 {
			return &resolvedAnswer{Answer: latest.Answer, AnsweredBy: latest.AnsweredBy}, false, nil
		}
		return nil, false, fmt.Errorf("resolving timed-out question: %w", answerErr)
	}

	if s.cfg.TimeoutPolicy == "fail" {
		return nil, false, fmt.Errorf("ask_human timed out after %ds (timeout_policy=fail)", s.cfg.TimeoutSeconds)
	}
	return &resolvedAnswer{Answer: resolution, AnsweredBy: "platform-mcp"}, true, nil
}

type resolvedAnswer struct {
	Answer     string
	AnsweredBy string
}

func containsOption(opts []string, s string) bool {
	for _, o := range opts {
		if o == s {
			return true
		}
	}
	return false
}
```

  Add the shared row constructor to `askhuman.go` (also used by Task 14's checkpoint handler; the wire payload IS `questions.Question` — the Task 5 handler ignores client-set id/timestamps and overrides `TicketID` from the URL, so no separate payload type is needed and the Task 9 `AskQuestion(ctx, *questions.Question)` signature stands):

```go
func (s *Server) newQuestion(kind questions.Kind, question string, options []string, defaultAnswer string) *questions.Question {
	q := &questions.Question{
		Kind:           kind,
		Question:       question,
		Options:        options,
		TimeoutPolicy:  questions.TimeoutPolicy(s.cfg.TimeoutPolicy),
		TimeoutSeconds: s.cfg.TimeoutSeconds,
		DefaultAnswer:  defaultAnswer,
		BuildID:        s.cfg.BuildID,
		StepName:       s.cfg.StepName,
	}
	if s.cfg.PipelineRunID > 0 {
		runID := s.cfg.PipelineRunID
		q.PipelineRunID = &runID
	}
	return q
}
```

  (Checkpoint rows override `TimeoutPolicy` to `questions.TimeoutPark`/`0` — Task 14 sets those fields explicitly after calling `newQuestion`.)

- [ ] Run to verify pass:

```bash
go test ./agent/platformmcp/
```

- [ ] Commit:

```bash
git add agent/platformmcp/
git commit -m "feat(platform-mcp): ask_human park/resume with park|default|fail timeout policies" -m "Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 11b: ask_human long-park exit — threshold timer, `flight/park.json` sentinel, answered-row fast path (PARK-V2 §B1/§B3/§E, 2026-07-10)

**Files:**
- Create: `agent/platformmcp/parkexit.go`
- Modify: `agent/platformmcp/askhuman.go`
- Test: `agent/platformmcp/parkexit_test.go`

**What happens at a threshold crossing (§B1/§B3):** the sidecar atomically writes the park sentinel — temp file + rename in the SAME directory, so the agent-runner's 5s stat loop can never observe a partial file — with the frozen payload `{"question_id", "kind", "step_name", "asked_at" (RFC3339), "threshold_seconds", "crossed_at" (RFC3339)}`, KEEPS the question row open (it is the durable representation of the wait), and takes no further action: the runner SIGTERMs claude, the blocked MCP `tools/call` connection drops, and `AwaitAnswer` cancels via the request context (already threaded — `GetQuestion` carries `ctx`, and net/http cancels it on client disconnect). The sentinel was chosen over a `GET /park-status` poll because (§B1): zero new HTTP surface, it survives a sidecar crash (the file persists), it rides the ingested flight artifact as free park-exit provenance, and the runner's watch is a trivial stat loop with no liveness coupling.

**Resume fast path (§E):** Task 5b's find-or-create means the continuation's re-issued `ask_human` gets back an ALREADY-ANSWERED row — return the result immediately: no park, no threshold timer, no SSE wait. **Timeout-policy interplay:** `awaitWithPolicy`'s deadline is absolute from the row's `asked_at`, so a `default`/`fail` timeout longer than the threshold still resolves correctly in the respawned step — the fresh sidecar's joined row re-arms from the ORIGINAL `asked_at`, not a fresh clock.

- [ ] Write the failing test `agent/platformmcp/parkexit_test.go`:

```go
package platformmcp_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/concourse/concourse/agent/api/questions"
	"github.com/concourse/concourse/agent/platformmcp"
)

// newParkExitServer is newAskServer plus a pipeline run id (the §E dedup key
// scope), a tiny short-park threshold, and a sentinel path in a temp dir.
func newParkExitServer(t *testing.T, atcURL string, threshold time.Duration) (*platformmcp.Server, string) {
	t.Helper()
	srv, err := platformmcp.NewServer(platformmcp.Config{
		ATCURL:         atcURL,
		PrincipalToken: "cap1.9.secret",
		TicketID:       42,
		PipelineRunID:  7,
		BuildID:        1001,
		StepName:       "implement",
		TimeoutPolicy:  "park",
		ListenAddr:     ":0",
	})
	if err != nil {
		t.Fatal(err)
	}
	srv.TunePolling(50*time.Millisecond, 20*time.Millisecond)
	parkPath := filepath.Join(t.TempDir(), "park.json")
	srv.TuneShortPark(threshold, parkPath)
	return srv, parkPath
}

// TestAskHumanWritesParkSentinelAtThreshold (PARK-V2 §B1/§B3): when a park
// crosses the threshold the sidecar atomically writes flight/park.json with
// the frozen payload and KEEPS the question row open; the parked call is then
// unblocked by the CLIENT DISCONNECT (the runner SIGTERMs claude) — and that
// disconnect must NOT resolve the row: it is the durable representation of
// the wait.
func TestAskHumanWritesParkSentinelAtThreshold(t *testing.T) {
	store := questions.NewMemoryStore()
	atc := fullStubATC(t, store)
	srv, parkPath := newParkExitServer(t, atc.URL, 200*time.Millisecond)

	sidecar := httptest.NewServer(srv.Mux())
	t.Cleanup(sidecar.Close)

	// Park via a real connection so cancelling the request context actually
	// severs it (the disconnect leg of §B3).
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	body := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"ask_human","arguments":{"question":"long wait?"}}}`
	req, err := http.NewRequestWithContext(ctx, "POST", sidecar.URL+"/mcp", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	done := make(chan struct{})
	go func() {
		defer close(done)
		resp, err := http.DefaultClient.Do(req)
		if err == nil {
			resp.Body.Close()
		}
	}()

	// The sentinel appears once the threshold crosses.
	var raw []byte
	deadline := time.Now().Add(3 * time.Second)
	for {
		raw, err = os.ReadFile(parkPath)
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("park sentinel never appeared")
		}
		time.Sleep(20 * time.Millisecond)
	}

	var sentinel struct {
		QuestionID       int    `json:"question_id"`
		Kind             string `json:"kind"`
		StepName         string `json:"step_name"`
		AskedAt          string `json:"asked_at"`
		ThresholdSeconds int    `json:"threshold_seconds"`
		CrossedAt        string `json:"crossed_at"`
	}
	if err := json.Unmarshal(raw, &sentinel); err != nil {
		t.Fatalf("park.json is not valid JSON (atomic write violated?): %v: %q", err, raw)
	}
	open, _ := store.OpenForTicket(42)
	if len(open) != 1 {
		t.Fatalf("expected the question to STAY OPEN at park-exit, got %d open", len(open))
	}
	if sentinel.QuestionID != open[0].ID || sentinel.Kind != "question" || sentinel.StepName != "implement" {
		t.Fatalf("unexpected sentinel: %+v", sentinel)
	}
	for _, ts := range []string{sentinel.AskedAt, sentinel.CrossedAt} {
		if _, err := time.Parse(time.RFC3339, ts); err != nil {
			t.Fatalf("sentinel timestamp %q is not RFC3339: %v", ts, err)
		}
	}
	if _, err := os.Stat(parkPath + ".tmp"); !os.IsNotExist(err) {
		t.Fatal("temp file left behind — the write must be tmp + rename")
	}

	// The runner now kills claude; the connection drops. The parked call must
	// return WITHOUT resolving the row.
	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("parked ask_human did not return on client disconnect")
	}
	open, _ = store.OpenForTicket(42)
	if len(open) != 1 {
		t.Fatalf("client disconnect must NOT resolve the row; got %d open", len(open))
	}
}

// TestAskHumanAnsweredRowFastPath (PARK-V2 §E): the continuation build's
// re-issued ask_human hits find-or-create, gets the already-answered row, and
// returns IMMEDIATELY — no park, no SSE wait, no threshold timer.
func TestAskHumanAnsweredRowFastPath(t *testing.T) {
	store := questions.NewMemoryStore()
	atc := fullStubATC(t, store)
	srv, parkPath := newParkExitServer(t, atc.URL, time.Hour)

	// The row the PREVIOUS execution filed and a human answered while the
	// step was exited: same (pipeline_run_id, step_name, kind, hash) — the
	// build id differs and is deliberately NOT part of the key.
	runID := 7
	id, err := store.Ask(&questions.Question{
		TicketID: 42, PipelineRunID: &runID, BuildID: 900, StepName: "implement",
		Kind: questions.KindQuestion, Question: "resume me?",
		Options: []string{"go", "stop"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Answer(42, id, "go", "tdm"); err != nil {
		t.Fatal(err)
	}

	start := time.Now()
	result, isErr := callTool(t, srv.Mux(), "ask_human", map[string]any{
		"question": "resume me?",
		"options":  []string{"go", "stop"},
	})
	if isErr {
		t.Fatalf("ask_human errored: %s", result)
	}
	if !strings.Contains(string(result), `"answer":"go"`) ||
		!strings.Contains(string(result), `"answered_by":"tdm"`) {
		t.Fatalf("expected the stored answer immediately, got: %s", result)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("fast path took %s — it must not park", elapsed)
	}
	if _, err := os.Stat(parkPath); !os.IsNotExist(err) {
		t.Fatal("fast path must not arm the park-exit timer / write a sentinel")
	}
}
```

- [ ] Run to verify it fails:

```bash
go test ./agent/platformmcp/
```

Expected failures: `undefined: (*platformmcp.Server).TuneShortPark`; the fast-path test parks instead of returning.

- [ ] Write `agent/platformmcp/parkexit.go`:

```go
package platformmcp

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/concourse/concourse/agent/api/questions"
)

// TuneShortPark overrides the §A threshold and §B1 sentinel path (tests only).
func (s *Server) TuneShortPark(threshold time.Duration, parkPath string) {
	s.cfg.ShortParkMax = threshold
	s.cfg.ParkPath = parkPath
}

// armParkExit starts the PARK-V2 §A threshold timer for a park on q and
// returns a stop func the caller MUST defer: an answered park must never fire
// a stale sentinel. The timer measures from the row's asked_at — a joiner
// (a dedup'd re-ask) therefore inherits the ORIGINAL park clock, never a
// fresh one.
func (s *Server) armParkExit(q *questions.Question) func() {
	if s.cfg.ShortParkMax <= 0 {
		return func() {} // 0 = never exit (pure PARK-V1; the rollback hatch)
	}
	remaining := time.Until(time.Unix(q.AskedAt, 0).Add(s.cfg.ShortParkMax))
	if remaining < 0 {
		remaining = 0
	}
	timer := time.AfterFunc(remaining, func() { s.crossParkThreshold(q) })
	return func() { timer.Stop() }
}

// crossParkThreshold writes the §B1 sentinel. The question row is NOT touched
// — it stays open as the durable representation of the wait (§B3); the
// agent-runner's 5s stat loop sees the file, SIGTERMs claude, and this call's
// connection drops (AwaitAnswer cancels on the request context). A pod with
// no PLATFORM_MCP_PARK_PATH — an agent-step-exec bug in an agent-step pod
// (the exec sets it via SidecarEnv, F15 — plan 07 Task 26); normal in
// checkpoint pods, which exit via the 202 response instead — degrades LOUDLY
// to the PARK-V1 SSE park, bounded platform-side by --agent-park-timeout.
func (s *Server) crossParkThreshold(q *questions.Question) {
	if s.cfg.ParkPath == "" {
		log.Printf("park-exit: question %d crossed the %s short-park threshold but PLATFORM_MCP_PARK_PATH is unset — staying on the SSE park (PARK-V1 degradation)", q.ID, s.cfg.ShortParkMax)
		return
	}
	payload := map[string]any{
		"question_id":       q.ID,
		"kind":              string(q.Kind),
		"step_name":         s.cfg.StepName,
		"asked_at":          time.Unix(q.AskedAt, 0).UTC().Format(time.RFC3339),
		"threshold_seconds": int(s.cfg.ShortParkMax / time.Second),
		"crossed_at":        time.Now().UTC().Format(time.RFC3339),
	}
	if err := writeSentinelAtomic(s.cfg.ParkPath, payload); err != nil {
		log.Printf("park-exit: question %d: failed to write park sentinel %s: %s — staying on the SSE park", q.ID, s.cfg.ParkPath, err)
	}
}

// writeSentinelAtomic is §B1's write-temp-then-mv: the temp file lives in the
// SAME directory (same filesystem), so the rename is atomic and the runner's
// stat loop can never observe a partial file.
func writeSentinelAtomic(path string, payload map[string]any) error {
	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	tmp := filepath.Join(filepath.Dir(path), filepath.Base(path)+".tmp")
	if err := os.WriteFile(tmp, raw, 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("renaming sentinel into place: %w", err)
	}
	return nil
}
```

- [ ] Wire it into `agent/platformmcp/askhuman.go`'s `askHuman`. Directly after the `s.events.Emit("human.ask", ...)` call (BEFORE the `progress(...)` line), insert the §E fast path:

```go
	// PARK-V2 §E resume fast path: find-or-create returned an ALREADY-ANSWERED
	// row — the continuation's re-issued call gets its answer immediately (no
	// park, no threshold timer, no SSE wait).
	if created.AnsweredAt != 0 {
		s.events.Emit("human.answer", map[string]any{
			"question_id":  created.ID,
			"answer":       created.Answer,
			"answered_by":  created.AnsweredBy,
			"wait_seconds": created.AnsweredAt - created.AskedAt,
			"timed_out":    false,
			"resumed":      true,
		})
		return askHumanResult{Answer: created.Answer, AnsweredBy: created.AnsweredBy, TimedOut: false}, nil
	}
```

  and directly before the `answered, timedOut, err := s.awaitWithPolicy(...)` call, arm the timer:

```go
	// PARK-V2 §A/§B1: arm the exit-and-respawn threshold for this park.
	stopParkExit := s.armParkExit(created)
	defer stopParkExit()
```

  (Task 12 later converts the raw `"human.answer"` string to `schema.EventHumanAnswer` along with the other `askhuman.go` call sites — its existing step already covers every `Emit` in that file.)

- [ ] Run to verify pass:

```bash
go test ./agent/platformmcp/
```

- [ ] Commit:

```bash
git add agent/platformmcp/
git commit -m "feat(platform-mcp): ask_human park-exit sentinel + answered-row fast path (PARK-V2 §B1/§B3/§E)" -m "Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 12: flight-recorder events — schema constants + sidecar event log

**Files:**
- Modify: `agent/schema/event.go` (constants block, lines 12–22 today; agent-step will have extended it)
- Create: `agent/platformmcp/events.go` (replaces the Task 10 temporary `EventLog`)
- Test: `agent/schema/event_test.go` (extend), `agent/platformmcp/events_test.go`

Per contracts §5, `human.ask`/`human.answer` are emitted by the platform-mcp sidecar and `checkpoint.wait`/`checkpoint.release` by the checkpoint step — the emitting workstream (this one) owns those four constants. Agent-step owns the module and may already have added them (§5 lists all new types in its table): **check first, add only what is missing** (the Task 1 survey recorded the current constant list).

- [ ] If (and only if) the constants are absent from `agent/schema/event.go`, add after `EventError` (line 21):

```go
	EventHumanAsk          EventType = "human.ask"
	EventHumanAnswer       EventType = "human.answer"
	EventCheckpointWait    EventType = "checkpoint.wait"
	EventCheckpointRelease EventType = "checkpoint.release"
```

  and add an assertion to the schema test file:

```go
func TestHITLEventConstants(t *testing.T) {
	if EventHumanAsk != "human.ask" || EventHumanAnswer != "human.answer" ||
		EventCheckpointWait != "checkpoint.wait" || EventCheckpointRelease != "checkpoint.release" {
		t.Fatal("HITL event constants must match shared-contracts §5 wire values")
	}
}
```

  Run `go test ./agent/schema/` (note: if agent-step's extraction made this a nested module, run the same command from the repo root — the root `go.mod` `replace` covers it).

- [ ] Write the failing test `agent/platformmcp/events_test.go`:

```go
package platformmcp_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/concourse/concourse/agent/platformmcp"
)

func TestEventLogWritesNDJSON(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.ndjson")
	log, err := platformmcp.NewEventLog(path)
	if err != nil {
		t.Fatal(err)
	}
	log.Emit("human.ask", map[string]any{"question_id": 7, "kind": "question"})
	log.Emit("human.answer", map[string]any{"question_id": 7, "answer": "yes"})

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if len(lines) != 2 {
		t.Fatalf("expected 2 NDJSON lines, got %d: %q", len(lines), raw)
	}
	var first struct {
		TS    string         `json:"ts"`
		Event string         `json:"event"`
		Data  map[string]any `json:"data"`
	}
	if err := json.Unmarshal([]byte(lines[0]), &first); err != nil {
		t.Fatal(err)
	}
	if first.Event != "human.ask" || first.TS == "" || first.Data["question_id"] != float64(7) {
		t.Fatalf("unexpected event line: %+v", first)
	}
}

func TestEventLogStdoutFallbackNeverPanics(t *testing.T) {
	log, err := platformmcp.NewEventLog("")
	if err != nil {
		t.Fatal(err)
	}
	log.Emit("human.ask", map[string]any{"question_id": 1})
}
```

- [ ] Run to verify it fails (`NewEventLog` is still the Task 10 no-op stub without `Emit` semantics):

```bash
go test ./agent/platformmcp/
```

Expected failure: `log.Emit undefined` (or file-empty assertion).

- [ ] Replace the Task 10 stub with `agent/platformmcp/events.go` (delete the temporary `EventLog`/`NewEventLog` from `server.go`):

```go
package platformmcp

import (
	"os"
	"sync"
	"time"

	"github.com/concourse/concourse/agent/schema"
)

// EventLog appends §5 flight-recorder events as NDJSON. Path "" = stdout
// (pod logs); a file path is used when the pod shares an events volume
// (PLATFORM_MCP_EVENTS_PATH, Task 1 addendum). Emission is best-effort:
// a broken event log must never fail a tool call.
type EventLog struct {
	mu sync.Mutex
	w  *schema.EventWriter
}

func NewEventLog(path string) (*EventLog, error) {
	if path == "" {
		return &EventLog{w: schema.NewEventWriter(os.Stdout)}, nil
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, err
	}
	return &EventLog{w: schema.NewEventWriter(f)}, nil
}

func (l *EventLog) Emit(eventType schema.EventType, data map[string]interface{}) {
	l.mu.Lock()
	defer l.mu.Unlock()
	_ = l.w.Write(schema.Event{
		Timestamp: time.Now().UTC().Format(time.RFC3339Nano),
		Type:      eventType,
		Data:      data,
	})
}
```

  Adjust the `Emit` call sites in `askhuman.go` to pass `schema.EventHumanAsk` / `schema.EventHumanAnswer` instead of raw strings.

- [ ] Run to verify pass:

```bash
go test ./agent/schema/ ./agent/platformmcp/
```

- [ ] Commit:

```bash
git add agent/schema/event.go agent/schema/*_test.go agent/platformmcp/
git commit -m "feat(platform-mcp): HITL flight-recorder events (human.ask/answer, checkpoint.*)" -m "Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 13: `cmd/platform-mcp` binary (serve mode)

**Files:**
- Create: `cmd/platform-mcp/main.go`
- Test: `cmd/platform-mcp/main_test.go`

- [ ] Write the failing test `cmd/platform-mcp/main_test.go` (spawns the built binary against a stub ATC and probes `/healthz` + `tools/list` — an end-to-end smoke of the exact container entrypoint):

```go
package main_test

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

func TestServeModeSmoke(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "platform-mcp")
	build := exec.Command("go", "build", "-o", bin, ".")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v: %s", err, out)
	}

	stubATC := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer stubATC.Close()

	port := freePort(t)
	cmd := exec.Command(bin)
	cmd.Env = append(os.Environ(),
		"ATC_EXTERNAL_URL="+stubATC.URL,
		"AGENT_PRINCIPAL_TOKEN=cap1.9.secret",
		"AGENT_TICKET_ID=42",
		fmt.Sprintf("MCP_LISTEN_ADDR=127.0.0.1:%d", port),
	)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer cmd.Process.Kill()

	base := fmt.Sprintf("http://127.0.0.1:%d", port)
	var healthy bool
	for i := 0; i < 50; i++ {
		resp, err := http.Get(base + "/healthz")
		if err == nil && resp.StatusCode == http.StatusOK {
			resp.Body.Close()
			healthy = true
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if !healthy {
		t.Fatal("healthz never came up")
	}

	resp, err := http.Post(base+"/mcp", "application/json",
		strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "ask_human") {
		t.Fatalf("tools/list missing ask_human: %s", raw)
	}
}

func TestServeModeFailsFastOnBadEnv(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "platform-mcp")
	build := exec.Command("go", "build", "-o", bin, ".")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v: %s", err, out)
	}
	cmd := exec.Command(bin)
	cmd.Env = []string{} // no ATC_EXTERNAL_URL
	if err := cmd.Run(); err == nil {
		t.Fatal("expected non-zero exit without required env")
	}
}
```

- [ ] Run to verify it fails:

```bash
go test ./cmd/platform-mcp/
```

Expected failure: no Go files in the package.

- [ ] Write `cmd/platform-mcp/main.go`:

```go
// platform-mcp is the platform MCP sidecar (shared contracts §3.2): serve
// mode (default) runs the MCP server; "checkpoint" mode is the checkpoint
// step's client (Task 14 / §3.2 addendum).
package main

import (
	"fmt"
	"os"

	"github.com/concourse/concourse/agent/platformmcp"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "checkpoint" {
		os.Exit(runCheckpoint(os.Args[2:]))
	}

	cfg, err := platformmcp.ConfigFromEnv()
	if err != nil {
		fmt.Fprintf(os.Stderr, "platform-mcp: %s\n", err)
		os.Exit(2)
	}
	srv, err := platformmcp.NewServer(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "platform-mcp: %s\n", err)
		os.Exit(2)
	}
	fmt.Fprintf(os.Stderr, "platform-mcp: serving MCP on %s (ticket %d)\n", cfg.ListenAddr, cfg.TicketID)
	if err := srv.ListenAndServe(); err != nil {
		fmt.Fprintf(os.Stderr, "platform-mcp: %s\n", err)
		os.Exit(1)
	}
}
```

  For this task, `runCheckpoint` is a one-line function in `main.go` that prints "checkpoint mode lands in Task 14" and returns 2; Task 14 replaces it with the real client (its tests fail against this body, so it cannot survive).

  The binary inherits BOTH halves of the D4 server rules without further code here: the progress-interval validation is `ConfigFromEnv`'s (Task 9 — a set-but-invalid, <= 0, or > 30s `PLATFORM_MCP_PROGRESS_INTERVAL` errors, so `main` exits 2), and the serving `http.Server` is `Server.ListenAndServe`'s (Task 10 — `WriteTimeout: 0`, `IdleTimeout: 0`, `ReadHeaderTimeout: 5s`; any nonzero WriteTimeout would sever long SSE streams and blocking `/checkpoint` responses).

- [ ] Add the D3 validation smoke to `cmd/platform-mcp/main_test.go` (fails against a binary that ignores or clamps the env):

```go
// TestServeModeFailsFastOnBadProgressInterval (D3, 2026-07-09 SSE seam delta):
// a PLATFORM_MCP_PROGRESS_INTERVAL that is invalid, <= 0, or > 30s must be a
// FATAL startup error — never clamped silently.
func TestServeModeFailsFastOnBadProgressInterval(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "platform-mcp")
	build := exec.Command("go", "build", "-o", bin, ".")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v: %s", err, out)
	}
	for _, bad := range []string{"bogus", "0s", "45s"} {
		cmd := exec.Command(bin)
		cmd.Env = append(os.Environ(),
			"ATC_EXTERNAL_URL=http://127.0.0.1:1",
			"AGENT_PRINCIPAL_TOKEN=cap1.9.secret",
			"AGENT_TICKET_ID=42",
			"PLATFORM_MCP_PROGRESS_INTERVAL="+bad,
		)
		if err := cmd.Run(); err == nil {
			t.Fatalf("PLATFORM_MCP_PROGRESS_INTERVAL=%q: expected non-zero exit", bad)
		}
	}
}
```

- [ ] Run to verify pass:

```bash
go test ./cmd/platform-mcp/
```

- [ ] Commit:

```bash
git add cmd/platform-mcp/
git commit -m "feat(platform-mcp): sidecar binary serve mode" -m "Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 14: checkpoint-gate execution — internal endpoint + client mode

**Files:**
- Create: `agent/platformmcp/checkpoint.go` (replaces the Task 10 `handleCheckpoint` temporary body in `server.go`)
- Create: `cmd/platform-mcp/checkpoint.go` (replaces the Task 13 `runCheckpoint` temporary body)
- Test: `agent/platformmcp/checkpoint_test.go`, `cmd/platform-mcp/checkpoint_test.go`

Checkpoint gates (§3.2, Task 1 addendum) reuse the ask_human primitive. **Render contract (co-signed with dispatch, §11 2026-07-09):** dispatch's renderer materializes a checkpoint declaration as a plain **`atc.TaskStep`** whose main container runs the **deterministic `platform-mcp checkpoint --name <n>` client** — NOT an LLM `AgentStep`. There is no model in the loop: the checkpoint is a container that runs a fixed CLI, POSTs the sidecar, and blocks on its HTTP response. The rendered pipeline (wave 4; wave-3 hand-written pipelines do the same by hand) mounts the platform sidecar into that task; the client POSTs the sidecar's `/checkpoint`, which files a `kind=checkpoint` question (`options: ["approve","reject"]`, always `park`) and blocks until a human resolves it.

**Exit-code semantics (this plan OWNS them; FROZEN) — confirmed explicit:**
- **Approve** ⇒ sidecar returns `{"approved": true}` ⇒ client **exits 0** ⇒ TaskStep succeeds ⇒ run continues.
- **Reject / non-200 / bad response / transport failure after retries / fatal-auth** ⇒ client **exits 1** ⇒ TaskStep fails ⇒ run fails ⇒ dispatch's run-completion reconciler applies the `on_reject` branch (below). The fatal-auth case (sidecar's `AwaitAnswer` hit the consecutive-401/403 limit, D6) additionally carries the frozen stderr line prefix **`principal rejected:`**.
- **Usage error** (missing `--name` / `PLATFORM_MCP_URL`) ⇒ client **exits 2**.
- **Parked past `--agent-short-park-max`** *(added by the PARK-V2 delta, 2026-07-10 — Task 14c)* ⇒ sidecar answers the blocked POST **`202 {"parked": true}`** ⇒ client **exits 3** (FROZEN; 0/1/2 unchanged) ⇒ TaskStep fails as the §B5 carrier. The question row STAYS OPEN — it, not the build status, is the authority dispatch's `reconcileAwaitingRuns` resumes on; the continuation TaskStep re-runs the client, whose re-POST joins the answered row (DB dedup, Task 5b) and exits 0/1 immediately.

**`on_reject` mapping (CORRECTED per the 2026-07-09 checkpoint-seam delta/F14 — the branch lives in dispatch's run-completion RECONCILER, plan 11 Task 11b, NOT the renderer):**
- The renderer emits the IDENTICAL bare failing `atc.TaskStep` for BOTH `on_reject` values — never wrapped in try/on_failure/ensure modifiers. Reject ⇒ client exits 1 ⇒ step fails ⇒ run fails.
- After the run completes failed, dispatch's run-completion reconciler (plan 11 Task 11b) reads the run's latest answered `kind='checkpoint'` row (via this plan's `ListByRun`, Task 14b), resolves the ticket's frozen workflow config, and branches: `on_reject: fail` (or step not found) ⇒ ticket `needs_review`; `on_reject: send_back` ⇒ ticket re-queued (`running→queued`, attempt_count++, capped by dispatch's MaxAttempts guard). At the **step** level both values fail the step on reject; the difference is purely what the reconciler does with the completed run.

**Sidecar env in checkpoint pods (delta §2/§3, informative here — dispatch's renderer owns the emitting code):** the platform sidecar's §8.1 env arrives as renderer-emitted literal `SidecarConfig` env rows (`ATC_EXTERNAL_URL`, `AGENT_TICKET_ID`, `AGENT_PIPELINE_RUN_ID=((run_id))`, `AGENT_STEP_NAME=checkpoint-<name>`) plus `AGENT_PRINCIPAL_TOKEN` as a `secretKeyRef` ValueFrom entry (`agent-run-((run_id))`/`principal-token`), gated by the web flag `--kubernetes-sidecar-secret-prefixes`. The checkpoint CLIENT gets NO token — it authenticates to nothing (pod-local loopback is the auth boundary) and reads only `PLATFORM_MCP_URL`. Checkpoint question rows carry `build_id=0` in v1; `pipeline_run_id` + `step_name` are the join keys.

**Checkpoint SSE exemption (D4, 2026-07-09 SSE seam delta):** the checkpoint gate is NOT an MCP `tools/call` — it is a deterministic CLI blocking on the sidecar's internal `POST /checkpoint`, with no claude CLI in the loop, so the 60s abandonment (F13) cannot occur and the SSE mandate does not apply to it. It is instead bound by: (a) the D4 server-timeout rule (`WriteTimeout: 0` / `IdleTimeout: 0` on the serving mux — Task 10's `ListenAndServe`), (b) the client's `http.Client` having NO global timeout, and (c) the full F31 park hardening — the sidecar awaits via the same `ATCClient.AwaitAnswer`, so a dead principal becomes a loud `principal rejected:` failure (exit 1), not an eternal park.

- [ ] Write the failing test `agent/platformmcp/checkpoint_test.go`:

```go
package platformmcp_test

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/concourse/concourse/agent/api/questions"
)

func checkpointRoundTrip(t *testing.T, answer string) (map[string]any, *questions.MemoryStore) {
	t.Helper()
	store := questions.NewMemoryStore()
	atc := fullStubATC(t, store)
	srv := newAskServer(t, atc.URL, "park", 0)

	go func() {
		for i := 0; i < 100; i++ {
			open, _ := store.OpenForTicket(42)
			if len(open) == 1 {
				if open[0].Kind != questions.KindCheckpoint {
					t.Errorf("expected checkpoint kind, got %q", open[0].Kind)
				}
				if len(open[0].Options) != 2 || open[0].Options[0] != "approve" || open[0].Options[1] != "reject" {
					t.Errorf("expected approve/reject options, got %v", open[0].Options)
				}
				_ = store.Answer(42, open[0].ID, answer, "tdm")
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
	}()

	req := httptest.NewRequest("POST", "/checkpoint",
		strings.NewReader(`{"name": "plan-approval", "description": "Approve the submitted plan"}`))
	w := httptest.NewRecorder()
	srv.Mux().ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("checkpoint endpoint: %d: %s", w.Code, w.Body.String())
	}
	var out map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	return out, store
}

func TestCheckpointApproved(t *testing.T) {
	out, _ := checkpointRoundTrip(t, "approve")
	if out["approved"] != true || out["answered_by"] != "tdm" {
		t.Fatalf("unexpected checkpoint result: %v", out)
	}
}

func TestCheckpointRejected(t *testing.T) {
	out, _ := checkpointRoundTrip(t, "reject")
	if out["approved"] != false {
		t.Fatalf("unexpected checkpoint result: %v", out)
	}
}

// TestCheckpointConcurrentDedup asserts the per-name dedup guard: two
// simultaneous POSTs for the same checkpoint name (the client-restart-mid-park
// case) must file exactly ONE agent_run_questions row, and both POSTs must
// return the same resolved answer once a human answers that single row.
func TestCheckpointConcurrentDedup(t *testing.T) {
	store := questions.NewMemoryStore()
	atc := fullStubATC(t, store)
	srv := newAskServer(t, atc.URL, "park", 0)

	// Answer the single open checkpoint once it appears, and record the peak
	// number of simultaneously-open rows — the guard must keep it at 1.
	maxOpen := 0
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 200; i++ {
			open, _ := store.OpenForTicket(42)
			if len(open) > maxOpen {
				maxOpen = len(open)
			}
			if len(open) >= 1 {
				_ = store.Answer(42, open[0].ID, "approve", "tdm")
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
	}()

	post := func() (int, map[string]any) {
		req := httptest.NewRequest("POST", "/checkpoint",
			strings.NewReader(`{"name": "plan-approval", "description": "Approve the plan"}`))
		w := httptest.NewRecorder()
		srv.Mux().ServeHTTP(w, req)
		var out map[string]any
		_ = json.Unmarshal(w.Body.Bytes(), &out)
		return w.Code, out
	}

	type result struct {
		code int
		out  map[string]any
	}
	results := make(chan result, 2)
	for i := 0; i < 2; i++ {
		go func() {
			code, out := post()
			results <- result{code, out}
		}()
	}

	for i := 0; i < 2; i++ {
		r := <-results
		if r.code != 200 {
			t.Fatalf("checkpoint POST %d: expected 200, got %d", i, r.code)
		}
		if r.out["approved"] != true || r.out["answered_by"] != "tdm" {
			t.Fatalf("checkpoint POST %d: unexpected result: %v", i, r.out)
		}
	}
	<-done

	// Exactly one row filed across both concurrent POSTs.
	all, _ := store.ListForTicket(42, 50)
	if len(all) != 1 {
		t.Fatalf("expected exactly 1 checkpoint row, got %d: %+v", len(all), all)
	}
	if maxOpen > 1 {
		t.Fatalf("dedup guard failed: %d rows open simultaneously", maxOpen)
	}
}

func TestCheckpointRequiresName(t *testing.T) {
	store := questions.NewMemoryStore()
	atc := fullStubATC(t, store)
	srv := newAskServer(t, atc.URL, "park", 0)
	req := httptest.NewRequest("POST", "/checkpoint", strings.NewReader(`{}`))
	w := httptest.NewRecorder()
	srv.Mux().ServeHTTP(w, req)
	if w.Code != 400 {
		t.Fatalf("expected 400 without name, got %d", w.Code)
	}
}
```

- [ ] Run to verify it fails:

```bash
go test ./agent/platformmcp/
```

Expected failure: `501 checkpoint endpoint lands in Task 14`.

- [ ] Add the per-name dedup guard to the `Server` struct in `agent/platformmcp/server.go` (the struct landed in Task 11 with fields `cfg`/`client`/`events`/`mcp`/`mux`). Add two fields and initialize the map in `NewServer` (next to the other field assignments):

```go
// added to the Server struct (Task 11) for the Task 14 checkpoint dedup:
	ckMu       sync.Mutex     // guards ckOpen
	ckOpen     map[string]int // checkpoint name -> open question id, this process lifetime
```

```go
// added inside NewServer's &Server{...} literal:
	ckOpen: map[string]int{},
```

  (add `"sync"` to `server.go`'s imports if the file does not already import it).

- [ ] Write `agent/platformmcp/checkpoint.go` (delete the temporary handler from `server.go`):

```go
package platformmcp

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/concourse/concourse/agent/api/questions"
	"github.com/concourse/concourse/agent/schema"
)

type checkpointRequest struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

type checkpointResponse struct {
	Approved   bool   `json:"approved"`
	Answer     string `json:"answer"`
	AnsweredBy string `json:"answered_by"`
}

// handleCheckpoint is the internal (non-MCP) checkpoint endpoint (§3.2 +
// Task 1 addendum). It files a kind=checkpoint question and BLOCKS until a
// human approves or rejects. Checkpoints always park — no timeout.
//
// Per-name dedup: if the checkpoint CLIENT process restarts mid-park it
// re-POSTs the same name; without a guard that files a second open row.
// ckOpen maps name -> the open question id for this sidecar's lifetime; a
// concurrent/repeat POST for a name already in flight re-awaits the SAME
// row instead of filing a new one. The guard is released once the row
// resolves (so a later, distinct checkpoint of the same name still files a
// fresh question).
func (s *Server) handleCheckpoint(w http.ResponseWriter, r *http.Request) {
	var req checkpointRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, fmt.Sprintf("invalid JSON: %s", err), http.StatusBadRequest)
		return
	}
	if req.Name == "" {
		http.Error(w, "name is required", http.StatusBadRequest)
		return
	}

	// Reserve (or join) the open question for this name.
	s.ckMu.Lock()
	questionID, inFlight := s.ckOpen[req.Name]
	if !inFlight {
		question := req.Description
		if question == "" {
			question = fmt.Sprintf("Approve checkpoint %q for ticket %d?", req.Name, s.cfg.TicketID)
		}
		q := s.newQuestion(questions.KindCheckpoint, question, []string{"approve", "reject"}, "")
		q.TimeoutPolicy = questions.TimeoutPark
		q.TimeoutSeconds = 0
		created, err := s.client.AskQuestion(r.Context(), q)
		if err != nil {
			s.ckMu.Unlock()
			http.Error(w, fmt.Sprintf("filing checkpoint: %s", err), http.StatusBadGateway)
			return
		}
		questionID = created.ID
		s.ckOpen[req.Name] = questionID
		s.ckMu.Unlock()

		s.events.Emit(schema.EventCheckpointWait, map[string]any{
			"question_id": questionID,
			"checkpoint":  req.Name,
		})
	} else {
		// A POST for this name is already parked on questionID; fall through
		// and await the same row. Only the first filer emits checkpoint.wait.
		s.ckMu.Unlock()
	}

	answered, _, err := s.client.AwaitAnswer(r.Context(), questionID, nil)
	if err != nil {
		// Transport error awaiting the answer: leave the reservation in place
		// so a client retry re-awaits the same open row rather than re-filing.
		// A consecutive-401/403 fatal (D6/F31 leg 3) is surfaced with the
		// frozen "principal rejected:" prefix so the checkpoint client can
		// echo it verbatim to stderr before exiting 1.
		if errors.Is(err, ErrPrincipalRejected) {
			http.Error(w, fmt.Sprintf("principal rejected: awaiting checkpoint: %s", err), http.StatusBadGateway)
			return
		}
		http.Error(w, fmt.Sprintf("awaiting checkpoint: %s", err), http.StatusBadGateway)
		return
	}

	// Resolved: release the name so a later distinct checkpoint files fresh.
	s.ckMu.Lock()
	if s.ckOpen[req.Name] == questionID {
		delete(s.ckOpen, req.Name)
	}
	s.ckMu.Unlock()

	approved := answered.Answer == "approve"
	s.events.Emit(schema.EventCheckpointRelease, map[string]any{
		"question_id": questionID,
		"approved":    approved,
		"answered_by": answered.AnsweredBy,
	})
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(checkpointResponse{
		Approved: approved, Answer: answered.Answer, AnsweredBy: answered.AnsweredBy,
	})
}
```

  Note: `AwaitAnswer` is called for BOTH the filer and any joiner, so every concurrent POST for the same name returns the resolved approve/reject once the human answers — the guard only prevents a second `agent_run_questions` row, it does not drop responses.

- [ ] Write the failing client test `cmd/platform-mcp/checkpoint_test.go`:

```go
package main_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func buildBinary(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "platform-mcp")
	build := exec.Command("go", "build", "-o", bin, ".")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v: %s", err, out)
	}
	return bin
}

func runCheckpointClient(t *testing.T, sidecarURL string, args ...string) *exec.ExitError {
	t.Helper()
	cmd := exec.Command(buildBinary(t), append([]string{"checkpoint"}, args...)...)
	cmd.Env = append(os.Environ(), "PLATFORM_MCP_URL="+sidecarURL+"/mcp")
	out, err := cmd.CombinedOutput()
	t.Logf("checkpoint client output: %s", out)
	if err == nil {
		return nil
	}
	exitErr, ok := err.(*exec.ExitError)
	if !ok {
		t.Fatalf("unexpected error type: %v", err)
	}
	return exitErr
}

func TestCheckpointClientApproved(t *testing.T) {
	sidecar := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/checkpoint" || r.Method != http.MethodPost {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		fmt.Fprint(w, `{"approved": true, "answer": "approve", "answered_by": "tdm"}`)
	}))
	defer sidecar.Close()
	if exitErr := runCheckpointClient(t, sidecar.URL, "--name", "plan-approval"); exitErr != nil {
		t.Fatalf("expected exit 0, got %d", exitErr.ExitCode())
	}
}

func TestCheckpointClientRejected(t *testing.T) {
	sidecar := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"approved": false, "answer": "reject", "answered_by": "tdm"}`)
	}))
	defer sidecar.Close()
	exitErr := runCheckpointClient(t, sidecar.URL, "--name", "plan-approval")
	if exitErr == nil || exitErr.ExitCode() != 1 {
		t.Fatalf("expected exit 1 on reject, got %v", exitErr)
	}
}

func TestCheckpointClientRequiresName(t *testing.T) {
	exitErr := runCheckpointClient(t, "http://127.0.0.1:1")
	if exitErr == nil || exitErr.ExitCode() != 2 {
		t.Fatalf("expected exit 2 without --name, got %v", exitErr)
	}
}

// TestCheckpointClientPrincipalRejected (D6/F31 leg 3): when the sidecar's
// AwaitAnswer hits the consecutive-401/403 limit it answers 502 with a
// "principal rejected:"-prefixed body; the client must exit 1 (frozen code)
// and echo that prefix on stderr — a loud failure, never a silent hang.
func TestCheckpointClientPrincipalRejected(t *testing.T) {
	sidecar := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "principal rejected: awaiting checkpoint: question 7: 12 consecutive 401/403 responses: agent principal rejected: consecutive auth failures exceeded limit", http.StatusBadGateway)
	}))
	defer sidecar.Close()

	cmd := exec.Command(buildBinary(t), "checkpoint", "--name", "plan-approval")
	cmd.Env = append(os.Environ(), "PLATFORM_MCP_URL="+sidecar.URL+"/mcp")
	out, err := cmd.CombinedOutput()
	exitErr, ok := err.(*exec.ExitError)
	if !ok || exitErr.ExitCode() != 1 {
		t.Fatalf("expected exit 1 on fatal-auth, got %v (out=%s)", err, out)
	}
	if !strings.Contains(string(out), "principal rejected:") {
		t.Fatalf("expected 'principal rejected:' on stderr, got %s", out)
	}
}
```

  (add `"strings"` to `cmd/platform-mcp/checkpoint_test.go`'s imports).

- [ ] Run to verify it fails (`runCheckpoint` is still the Task 13 stub returning 2):

```bash
go test ./cmd/platform-mcp/
```

- [ ] Write `cmd/platform-mcp/checkpoint.go` (delete the Task 13 stub):

```go
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// runCheckpoint is checkpoint-client mode: POST the sidecar's /checkpoint and
// block until approved/rejected. Exit codes FROZEN (checkpoint-seam + SSE seam
// deltas, 2026-07-09): 0 = approved; 1 = rejected OR non-200 OR bad response
// OR retries exhausted (a sidecar fatal-auth arrives as a 502 whose body
// carries the frozen "principal rejected:" prefix — echoed verbatim to
// stderr); 2 = usage error. Transport errors before a response are retried
// 60 x 5s (the sidecar may still be starting; §8.5 readiness ordering).
// The http.Client MUST have no global timeout (D4): this call blocks for the
// entire park — checkpoints are exempt from the SSE mandate (no claude CLI in
// the loop) but not from the no-timeout rules.
func runCheckpoint(args []string) int {
	fs := flag.NewFlagSet("checkpoint", flag.ContinueOnError)
	name := fs.String("name", "", "checkpoint name from the workflow definition (required)")
	description := fs.String("description", "", "what is being approved")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *name == "" {
		fmt.Fprintln(os.Stderr, "checkpoint: --name is required")
		return 2
	}

	mcpURL := os.Getenv("PLATFORM_MCP_URL") // e.g. http://127.0.0.1:7781/mcp (§8.1)
	if mcpURL == "" {
		fmt.Fprintln(os.Stderr, "checkpoint: PLATFORM_MCP_URL is required")
		return 2
	}
	endpoint := strings.TrimSuffix(mcpURL, "/mcp") + "/checkpoint"

	body, _ := json.Marshal(map[string]string{"name": *name, "description": *description})
	client := &http.Client{} // no timeout: this call blocks while parked

	for attempt := 1; ; attempt++ {
		resp, err := client.Post(endpoint, "application/json", bytes.NewReader(body))
		if err != nil {
			if attempt >= 60 {
				fmt.Fprintf(os.Stderr, "checkpoint: sidecar unreachable after %d attempts: %s\n", attempt, err)
				return 1
			}
			time.Sleep(5 * time.Second)
			continue
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			// Echo the sidecar's error body verbatim — the fatal-auth path's
			// "principal rejected:" prefix must reach the step log (D6).
			msg, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
			fmt.Fprintf(os.Stderr, "checkpoint: sidecar returned %d: %s\n", resp.StatusCode, bytes.TrimSpace(msg))
			return 1
		}
		var out struct {
			Approved   bool   `json:"approved"`
			Answer     string `json:"answer"`
			AnsweredBy string `json:"answered_by"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
			fmt.Fprintf(os.Stderr, "checkpoint: bad response: %s\n", err)
			return 1
		}
		if out.Approved {
			fmt.Printf("checkpoint %q approved by %s\n", *name, out.AnsweredBy)
			return 0
		}
		fmt.Printf("checkpoint %q rejected by %s\n", *name, out.AnsweredBy)
		return 1
	}
}
```

  The per-name dedup guard (`ckOpen` map + `ckMu`, added to the `Server` struct above and implemented in `handleCheckpoint`) is exercised by `TestCheckpointConcurrentDedup` in the checkpoint test block above: two simultaneous POSTs for the same name observe exactly one open `agent_run_questions` row and both return the same resolved answer.

- [ ] Run to verify pass:

```bash
go test ./agent/platformmcp/ ./cmd/platform-mcp/
```

- [ ] Commit:

```bash
git add agent/platformmcp/ cmd/platform-mcp/
git commit -m "feat(platform-mcp): checkpoint-gate endpoint + client mode on the park/resume primitive" -m "Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 14b: `ListByRun` store surface + checkpoint answer validation (checkpoint-seam delta, 2026-07-09 — co-signed with dispatch)

**Files:**
- Modify: `agent/api/questions/types.go` (Store interface), `agent/api/questions/memory_store.go`, `agent/api/questions/handler.go`
- Modify: `atc/db/agent_questions_factory.go` (+ regenerate `atc/db/dbfakes/fake_agent_questions_factory.go`)
- Test: `agent/api/questions/handler_test.go` (extend), `agent/api/questions/store_test.go` (extend), `atc/db/agent_questions_factory_test.go` (extend)

Two additive surfaces frozen by the checkpoint-seam delta:
1. **`ListByRun(pipelineRunID int) ([]Question, error)`** — consumed by dispatch's run-completion reconciler (plan 11 Task 11b) through its narrow `dispatch.QuestionLister` interface (`ListByRun` + the already-existing `Answer`). The reconciler reads the run's latest answered checkpoint row to branch `on_reject`, and releases orphaned unanswered rows.
2. **Checkpoint answer validation (delta §4)** — the ATC answer route rejects any answer not in the row's options when `kind='checkpoint'`, so the stored answer is EXACTLY `'approve'` or `'reject'`. (There is NO `approved` column; the reconciler's normative predicate is `answer <> 'approve'`.) Empty answers — legal for ordinary questions as the fail-policy resolution — are rejected on checkpoint rows at the ROUTE level; the reconciler's own orphan release (`Answer(id, "", "dispatcher")`) goes through the Store directly, not this route.

- [ ] Write the failing tests. Append to `agent/api/questions/store_test.go`:

```go
func TestListByRun(t *testing.T) {
	store := questions.NewMemoryStore()
	run7, run8 := 7, 8
	ask := func(runID *int, text string) int {
		id, err := store.Ask(&questions.Question{
			TicketID: 42, PipelineRunID: runID, Question: text,
		})
		if err != nil {
			t.Fatalf("Ask(%s): %v", text, err)
		}
		return id
	}
	first := ask(&run7, "first for run 7")
	second := ask(&run7, "second for run 7")
	ask(&run8, "for run 8")
	ask(nil, "no run")

	byRun, err := store.ListByRun(7)
	if err != nil {
		t.Fatalf("ListByRun: %v", err)
	}
	if len(byRun) != 2 {
		t.Fatalf("expected 2 questions for run 7, got %d: %+v", len(byRun), byRun)
	}
	// Newest-asked first (asked_at DESC, id DESC) — the reconciler reads
	// the LATEST checkpoint row, so ordering is part of the contract.
	if byRun[0].ID != second || byRun[1].ID != first {
		t.Fatalf("expected newest-first [%d %d], got [%d %d]", second, first, byRun[0].ID, byRun[1].ID)
	}
}
```

  Append to `agent/api/questions/handler_test.go`:

```go
// TestAnswerCheckpointRejectsNonOptionAnswer (checkpoint-seam delta §4): for
// kind=checkpoint rows the route-stored answer must be exactly one of the
// row's options — including rejecting the empty answer — so dispatch's
// reconciler can treat answer <> 'approve' as an explicit human rejection.
func TestAnswerCheckpointRejectsNonOptionAnswer(t *testing.T) {
	store := questions.NewMemoryStore()
	h := questions.NewHandler(store)
	runID := 7
	id, err := store.Ask(&questions.Question{
		TicketID: 42, PipelineRunID: &runID, Kind: questions.KindCheckpoint,
		Question: "Approve checkpoint?", Options: []string{"approve", "reject"},
		TimeoutPolicy: questions.TimeoutPark,
	})
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}

	put := func(answer string) int {
		body := fmt.Sprintf(`{"answer":%q,"answered_by":"tdm"}`, answer)
		r := httptest.NewRequest(http.MethodPut, "/answer", strings.NewReader(body))
		r.ParseForm()
		r.Form.Set(":ticket_id", "42")
		r.Form.Set(":question_id", fmt.Sprint(id))
		w := httptest.NewRecorder()
		h.AnswerQuestion(w, r)
		return w.Code
	}

	if code := put("maybe"); code != http.StatusBadRequest {
		t.Fatalf("non-option answer on a checkpoint: expected 400, got %d", code)
	}
	if code := put(""); code != http.StatusBadRequest {
		t.Fatalf("empty answer on a checkpoint: expected 400 (route level), got %d", code)
	}
	if code := put("reject"); code != http.StatusOK {
		t.Fatalf("valid reject: expected 200, got %d", code)
	}
}
```

- [ ] Run to verify failure (`store.ListByRun` undefined; the empty-answer case returns 200 against the pre-delta handler):

```bash
go test ./agent/api/questions/
```

- [ ] Add `ListByRun` to the Store interface in `agent/api/questions/types.go` (after `ListForTicket`):

```go
	// ListByRun returns every question filed under the pipeline run,
	// newest-asked first — consumed by dispatch's run-completion reconciler
	// (plan 11 Task 11b) via its narrow QuestionLister interface.
	ListByRun(pipelineRunID int) ([]Question, error)
```

- [ ] Implement it in `agent/api/questions/memory_store.go` (add `"sort"` to the imports):

```go
func (m *MemoryStore) ListByRun(pipelineRunID int) ([]Question, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []Question
	for _, row := range m.rows {
		if row.PipelineRunID != nil && *row.PipelineRunID == pipelineRunID {
			out = append(out, *row)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].AskedAt != out[j].AskedAt {
			return out[i].AskedAt > out[j].AskedAt
		}
		return out[i].ID > out[j].ID
	})
	return out, nil
}
```

- [ ] Add the checkpoint validation to `AnswerQuestion` in `agent/api/questions/handler.go`, directly after the existing options check:

```go
	// Checkpoint rows are stricter (checkpoint-seam delta §4): the stored
	// answer must be EXACTLY one of the row's options ('approve'/'reject') —
	// the empty answer is rejected too, so answer <> 'approve' always means
	// an explicit human rejection to dispatch's reconciler.
	if q.Kind == KindCheckpoint && !contains(q.Options, req.Answer) {
		http.Error(w, "checkpoint answer must be one of the question's options", http.StatusBadRequest)
		return
	}
```

- [ ] Run to verify pass, then extend the SQL side. Append to the `atc/db/agent_questions_factory_test.go` Describe block:

```go
	It("lists questions by pipeline run, newest first, for the dispatch reconciler", func() {
		runID := 512
		firstID, err := factory.Ask(&questions.Question{
			TicketID: 9004, PipelineRunID: &runID, Kind: questions.KindCheckpoint,
			Question: "Approve checkpoint \"plan-approval\"?", Options: []string{"approve", "reject"},
			TimeoutPolicy: questions.TimeoutPark,
		})
		Expect(err).ToNot(HaveOccurred())
		secondID, err := factory.Ask(&questions.Question{
			TicketID: 9004, PipelineRunID: &runID, Question: "unrelated question on the same run",
		})
		Expect(err).ToNot(HaveOccurred())
		otherRun := 513
		_, err = factory.Ask(&questions.Question{
			TicketID: 9004, PipelineRunID: &otherRun, Question: "different run",
		})
		Expect(err).ToNot(HaveOccurred())

		byRun, err := factory.ListByRun(512)
		Expect(err).ToNot(HaveOccurred())
		Expect(byRun).To(HaveLen(2))
		Expect(byRun[0].ID).To(Equal(secondID))
		Expect(byRun[1].ID).To(Equal(firstID))
	})
```

- [ ] Implement `ListByRun` in `atc/db/agent_questions_factory.go` (next to `ListForTicket`) and regenerate the fake:

```go
func (f *agentQuestionsFactory) ListByRun(pipelineRunID int) ([]questions.Question, error) {
	rows, err := f.conn.Query(
		`SELECT `+questionColumns+` FROM agent_run_questions q
		 WHERE q.pipeline_run_id = $1
		 ORDER BY q.asked_at DESC, q.id DESC`,
		pipelineRunID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanQuestionRows(rows)
}
```

```bash
go generate ./atc/db/
```

- [ ] Run to verify pass:

```bash
go test ./agent/api/questions/ && ginkgo ./atc/db/
```

- [ ] Commit:

```bash
git add agent/api/questions/ atc/db/agent_questions_factory.go atc/db/agent_questions_factory_test.go atc/db/dbfakes/fake_agent_questions_factory.go
git commit -m "feat(agent): ListByRun store surface + strict checkpoint answer validation for the dispatch reconciler" -m "Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 14c: checkpoint long-park exit — 202 `{"parked": true}` at threshold, client exit 3, DB-backed dedup fast path (PARK-V2 §B4/§E, 2026-07-10)

**Files:**
- Modify: `agent/platformmcp/server.go` (`ckOpen` becomes `map[string]ckReservation`)
- Modify: `agent/platformmcp/checkpoint.go` (full `handleCheckpoint` replacement)
- Modify: `cmd/platform-mcp/checkpoint.go` (202 → exit 3)
- Test: `agent/platformmcp/checkpoint_test.go`, `cmd/platform-mcp/checkpoint_test.go` (extend)

**Why (§B4):** there is no claude in the checkpoint loop, so the exit signal is the HTTP response itself: at the threshold the sidecar answers the blocked POST `202 {"parked": true}` and the client exits with FROZEN code 3 (parked-past-threshold; 0/1/2 unchanged). Exit 3 fails the TaskStep — exactly the §B5 carrier PARK-V2 wants; no TaskStep exec change anywhere. The row stays OPEN. **ckOpen demotion (§E):** the DB unique key `(pipeline_run_id, step_name, kind, question_hash)` is now the dedup AUTHORITY — necessary because a continuation pod runs a FRESH sidecar with an empty map — and `ckOpen` is retained as a same-pod optimization only. **Resume:** the continuation TaskStep re-runs the client; the re-POST's find-or-create returns the ANSWERED row; the handler responds 200 immediately; the client exits 0/1. `AwaitAnswer`'s existing deadline leg doubles as the threshold timer, measured from the row's `asked_at` (a re-POST joins the ORIGINAL park clock). The sentinel is also written when `ParkPath` is set (payload `kind: "checkpoint"`, best-effort flight provenance) — normally unset in checkpoint pods, where the 202 IS the signal.

- [ ] Write the failing sidecar tests — append to `agent/platformmcp/checkpoint_test.go`:

```go
// TestCheckpointParksPastThreshold (PARK-V2 §B4): with no human answer, the
// blocked POST gets 202 {"parked": true} once the short-park threshold
// crosses, and the row STAYS OPEN — the checkpoint client turns this into
// frozen exit code 3.
func TestCheckpointParksPastThreshold(t *testing.T) {
	store := questions.NewMemoryStore()
	atc := fullStubATC(t, store)
	srv := newAskServer(t, atc.URL, "park", 0)
	srv.TuneShortPark(200*time.Millisecond, "")

	start := time.Now()
	req := httptest.NewRequest("POST", "/checkpoint",
		strings.NewReader(`{"name": "plan-approval", "description": "Approve the plan"}`))
	w := httptest.NewRecorder()
	srv.Mux().ServeHTTP(w, req)
	if w.Code != 202 {
		t.Fatalf("expected 202 at threshold, got %d: %s", w.Code, w.Body.String())
	}
	var out map[string]bool
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil || !out["parked"] {
		t.Fatalf("expected {\"parked\": true}, got %s (err %v)", w.Body.String(), err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("threshold response took %s", elapsed)
	}
	open, _ := store.OpenForTicket(42)
	if len(open) != 1 {
		t.Fatalf("the parked checkpoint row must STAY OPEN, got %d open", len(open))
	}
}

// TestCheckpointRePostAfterAnswerResolvesImmediately (PARK-V2 §E): the
// continuation pod's FRESH sidecar (empty ckOpen map) re-POSTs the same
// checkpoint; the DB-level find-or-create returns the answered row and the
// response is an immediate 200 — the client exits 0/1 with no park and no
// second row.
func TestCheckpointRePostAfterAnswerResolvesImmediately(t *testing.T) {
	store := questions.NewMemoryStore()
	atc := fullStubATC(t, store)
	srv, _ := newParkExitServer(t, atc.URL, time.Hour)

	// The row the PREVIOUS pod's client filed and a human approved while the
	// step was exited: same (pipeline_run_id, step_name, kind, hash).
	runID := 7
	id, err := store.Ask(&questions.Question{
		TicketID: 42, PipelineRunID: &runID, StepName: "implement",
		Kind: questions.KindCheckpoint, Question: "Approve the plan",
		Options: []string{"approve", "reject"}, TimeoutPolicy: questions.TimeoutPark,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Answer(42, id, "approve", "tdm"); err != nil {
		t.Fatal(err)
	}

	start := time.Now()
	req := httptest.NewRequest("POST", "/checkpoint",
		strings.NewReader(`{"name": "plan-approval", "description": "Approve the plan"}`))
	w := httptest.NewRecorder()
	srv.Mux().ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("expected immediate 200 on the answered row, got %d: %s", w.Code, w.Body.String())
	}
	var out map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	if out["approved"] != true || out["answered_by"] != "tdm" {
		t.Fatalf("unexpected result: %v", out)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("fast path took %s — it must not park", elapsed)
	}
	all, _ := store.ListForTicket(42, 50)
	if len(all) != 1 {
		t.Fatalf("re-POST must NOT file a second row, got %d", len(all))
	}
}
```

- [ ] Run to verify they fail:

```bash
go test ./agent/platformmcp/
```

Expected failures: the threshold test blocks (no 202 branch); the re-POST test files a second row and parks.

- [ ] Change the dedup guard's shape in `agent/platformmcp/server.go` (the Task 14 fields):

```go
// ckReservation is the same-pod fast path for a repeated checkpoint POST; the
// cross-pod (continuation) dedup is DB-enforced by agent_run_questions_dedup
// (PARK-V2 §E — the map is an optimization, never the authority).
type ckReservation struct {
	ID      int
	AskedAt int64
}
```

  replace the two struct fields with:

```go
	ckMu   sync.Mutex               // guards ckOpen
	ckOpen map[string]ckReservation // checkpoint name -> open row; same-pod optimization only
```

  and the `NewServer` literal entry with `ckOpen: map[string]ckReservation{}`.

- [ ] Replace `handleCheckpoint` in `agent/platformmcp/checkpoint.go` (add `"time"` to its imports) and add the shared responder:

```go
// handleCheckpoint is the internal (non-MCP) checkpoint endpoint (§3.2 +
// Task 1 addendum). It files a kind=checkpoint question and BLOCKS until a
// human approves or rejects — or, past the PARK-V2 short-park threshold,
// answers 202 {"parked": true} so the client exits 3 and the step becomes
// the §B5 carrier. Checkpoints always park — no timeout policy.
func (s *Server) handleCheckpoint(w http.ResponseWriter, r *http.Request) {
	var req checkpointRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, fmt.Sprintf("invalid JSON: %s", err), http.StatusBadRequest)
		return
	}
	if req.Name == "" {
		http.Error(w, "name is required", http.StatusBadRequest)
		return
	}

	// Reserve (or join) the open question for this name. Since PARK-V2 §E the
	// map is a same-pod OPTIMIZATION only — the authority is the DB unique key
	// (pipeline_run_id, step_name, kind, question_hash): a continuation pod's
	// fresh sidecar re-POSTs, AskQuestion find-or-creates, and gets the SAME
	// row back even though its map is empty.
	s.ckMu.Lock()
	reservation, inFlight := s.ckOpen[req.Name]
	if !inFlight {
		question := req.Description
		if question == "" {
			question = fmt.Sprintf("Approve checkpoint %q for ticket %d?", req.Name, s.cfg.TicketID)
		}
		q := s.newQuestion(questions.KindCheckpoint, question, []string{"approve", "reject"}, "")
		q.TimeoutPolicy = questions.TimeoutPark
		q.TimeoutSeconds = 0
		created, err := s.client.AskQuestion(r.Context(), q)
		if err != nil {
			s.ckMu.Unlock()
			http.Error(w, fmt.Sprintf("filing checkpoint: %s", err), http.StatusBadGateway)
			return
		}
		if created.AnsweredAt != 0 {
			// PARK-V2 §E resume fast path: the re-POST joined a row a human
			// already resolved — return it immediately; no park, no
			// reservation.
			s.ckMu.Unlock()
			s.respondCheckpointResolved(w, created.ID, created.Answer, created.AnsweredBy)
			return
		}
		reservation = ckReservation{ID: created.ID, AskedAt: created.AskedAt}
		s.ckOpen[req.Name] = reservation
		s.ckMu.Unlock()

		s.events.Emit(schema.EventCheckpointWait, map[string]any{
			"question_id": reservation.ID,
			"checkpoint":  req.Name,
		})
	} else {
		// A POST for this name is already parked on the row; fall through and
		// await the same row. Only the first filer emits checkpoint.wait.
		s.ckMu.Unlock()
	}

	// PARK-V2 §A/§B4: the short-park threshold bounds the blocking response.
	// AwaitAnswer's deadline leg doubles as the threshold timer, measured
	// from the row's asked_at — a re-POST joins the ORIGINAL park clock.
	var parkDeadline *time.Time
	if s.cfg.ShortParkMax > 0 {
		d := time.Unix(reservation.AskedAt, 0).Add(s.cfg.ShortParkMax)
		parkDeadline = &d
	}

	answered, crossedThreshold, err := s.client.AwaitAnswer(r.Context(), reservation.ID, parkDeadline)
	if err != nil {
		// Transport error awaiting the answer: leave the reservation in place
		// so a client retry re-awaits the same open row rather than re-filing.
		// A consecutive-401/403 fatal (D6/F31 leg 3) keeps the frozen
		// "principal rejected:" prefix so the client echoes it and exits 1.
		if errors.Is(err, ErrPrincipalRejected) {
			http.Error(w, fmt.Sprintf("principal rejected: awaiting checkpoint: %s", err), http.StatusBadGateway)
			return
		}
		http.Error(w, fmt.Sprintf("awaiting checkpoint: %s", err), http.StatusBadGateway)
		return
	}
	if crossedThreshold {
		// §B4: answer the blocked POST 202 — the client exits 3 and the
		// TaskStep fails as the §B5 carrier. The row STAYS OPEN (the durable
		// representation of the wait) and the reservation is kept so a
		// same-pod retry re-awaits the same row. Best-effort sentinel for
		// flight provenance when a flight volume is mounted (normally it is
		// not in checkpoint pods — the 202 IS the exit signal there).
		if s.cfg.ParkPath != "" {
			_ = writeSentinelAtomic(s.cfg.ParkPath, map[string]any{
				"question_id":       reservation.ID,
				"kind":              string(questions.KindCheckpoint),
				"step_name":         s.cfg.StepName,
				"asked_at":          time.Unix(reservation.AskedAt, 0).UTC().Format(time.RFC3339),
				"threshold_seconds": int(s.cfg.ShortParkMax / time.Second),
				"crossed_at":        time.Now().UTC().Format(time.RFC3339),
			})
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		json.NewEncoder(w).Encode(map[string]bool{"parked": true})
		return
	}

	// Resolved: release the name so a later distinct checkpoint files fresh.
	s.ckMu.Lock()
	if s.ckOpen[req.Name].ID == reservation.ID {
		delete(s.ckOpen, req.Name)
	}
	s.ckMu.Unlock()

	s.respondCheckpointResolved(w, reservation.ID, answered.Answer, answered.AnsweredBy)
}

// respondCheckpointResolved emits checkpoint.release and writes the frozen
// 200 body — shared by the await path and the §E answered-row fast path.
func (s *Server) respondCheckpointResolved(w http.ResponseWriter, questionID int, answer, answeredBy string) {
	approved := answer == "approve"
	s.events.Emit(schema.EventCheckpointRelease, map[string]any{
		"question_id": questionID,
		"approved":    approved,
		"answered_by": answeredBy,
	})
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(checkpointResponse{
		Approved: approved, Answer: answer, AnsweredBy: answeredBy,
	})
}
```

- [ ] Run to verify the sidecar side passes:

```bash
go test ./agent/platformmcp/
```

- [ ] Write the failing client test — append to `cmd/platform-mcp/checkpoint_test.go`:

```go
// TestCheckpointClientParkedExit3 (PARK-V2 §B4): 202 {"parked": true} from
// the sidecar means the park crossed the short-park threshold — the client
// exits with FROZEN code 3 (parked-past-threshold; 0/1/2 unchanged) so the
// TaskStep fails as the §B5 carrier while the open question row remains the
// authority the platform resumes on.
func TestCheckpointClientParkedExit3(t *testing.T) {
	sidecar := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		fmt.Fprint(w, `{"parked": true}`)
	}))
	defer sidecar.Close()
	exitErr := runCheckpointClient(t, sidecar.URL, "--name", "plan-approval")
	if exitErr == nil || exitErr.ExitCode() != 3 {
		t.Fatalf("expected exit 3 on parked-past-threshold, got %v", exitErr)
	}
}
```

- [ ] Run to verify it fails (the pre-delta client treats 202 as non-200 and exits 1):

```bash
go test ./cmd/platform-mcp/
```

- [ ] Add the 202 branch to `runCheckpoint` in `cmd/platform-mcp/checkpoint.go`, directly BEFORE the `if resp.StatusCode != http.StatusOK` check, and extend the doc comment's exit-code list with `3 = parked past --agent-short-park-max (PARK-V2 §B4)`:

```go
		if resp.StatusCode == http.StatusAccepted {
			// PARK-V2 §B4: parked past --agent-short-park-max. FROZEN exit
			// code 3 — the TaskStep fails as the §B5 carrier; the open
			// question row (not this exit) is what the platform resumes on,
			// and the continuation's re-POST will join the answered row and
			// exit 0/1 immediately.
			var out struct {
				Parked bool `json:"parked"`
			}
			if err := json.NewDecoder(resp.Body).Decode(&out); err != nil || !out.Parked {
				fmt.Fprintf(os.Stderr, "checkpoint: bad 202 response: %v\n", err)
				return 1
			}
			fmt.Printf("checkpoint %q parked past the short-park threshold; exiting for respawn\n", *name)
			return 3
		}
```

- [ ] Run to verify pass:

```bash
go test ./agent/platformmcp/ ./cmd/platform-mcp/
```

- [ ] Commit:

```bash
git add agent/platformmcp/ cmd/platform-mcp/
git commit -m "feat(platform-mcp): checkpoint 202/exit-3 past the short-park threshold + DB-dedup fast path (PARK-V2 §B4/§E)" -m "Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 15: contract-test kit (`agent/platformmcp/contracttest`)

**Files:**
- Create: `agent/platformmcp/contracttest/contracttest.go`
- Create: `agent/platformmcp/contracttest/stub_atc.go`
- Test: `agent/platformmcp/contracttest/contracttest_test.go`

The spec's testing approach requires contract tests for all three MCP surfaces; dev-mcp's `agent/devmcp/contracttest` (wave 1) set the style: `contracttest.Run(t, endpointURL)` validates any serving endpoint — in-process here, `docker run` in the CI image job (Task 16).

- [ ] Write the failing self-test `agent/platformmcp/contracttest/contracttest_test.go` (runs the kit against an in-process real server + stub ATC — proving both kit and server):

```go
package contracttest_test

import (
	"net/http/httptest"
	"testing"
	"time"

	"github.com/concourse/concourse/agent/platformmcp"
	"github.com/concourse/concourse/agent/platformmcp/contracttest"
)

func TestPlatformMCPContract(t *testing.T) {
	stub := contracttest.NewStubATC(t, 42)
	stub.AutoAnswer("yes", "contract-bot", 50*time.Millisecond)

	srv, err := platformmcp.NewServer(platformmcp.Config{
		ATCURL:         stub.URL(),
		PrincipalToken: contracttest.StubToken,
		TicketID:       42,
		BuildID:        1,
		StepName:       "contract",
		TimeoutPolicy:  "park",
		ListenAddr:     ":0",
	})
	if err != nil {
		t.Fatal(err)
	}
	srv.TunePolling(50*time.Millisecond, 20*time.Millisecond)

	endpoint := httptest.NewServer(srv.Mux())
	defer endpoint.Close()

	contracttest.Run(t, endpoint.URL)
}

// TestPlatformMCPSSEHeartbeats (D4/D7, 2026-07-09 SSE seam delta): a server
// with a fast heartbeat and a slow-answering stub must stream >= 2
// notifications/progress frames (spaced < 30s) before the final result frame
// on a parked ask_human. This is the contract-kit twin of gateway 10 Task 7's
// 40s-fake-adapter test; the REAL claude-CLI cadence is pinned by Task 18b.
func TestPlatformMCPSSEHeartbeats(t *testing.T) {
	stub := contracttest.NewStubATC(t, 42)
	stub.AutoAnswer("yes", "contract-bot", 500*time.Millisecond)

	srv, err := platformmcp.NewServer(platformmcp.Config{
		ATCURL:           stub.URL(),
		PrincipalToken:   contracttest.StubToken,
		TicketID:         42,
		BuildID:          1,
		StepName:         "contract",
		TimeoutPolicy:    "park",
		ListenAddr:       ":0",
		ProgressInterval: 100 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	srv.TunePolling(50*time.Millisecond, 20*time.Millisecond)

	endpoint := httptest.NewServer(srv.Mux())
	defer endpoint.Close()

	contracttest.RunSSEHeartbeats(t, endpoint.URL)
}
```

- [ ] Run to verify it fails:

```bash
go test ./agent/platformmcp/contracttest/
```

Expected failure: package does not exist.

- [ ] Write `agent/platformmcp/contracttest/stub_atc.go` — a real-handler-backed stub any packaging can point at (env `ATC_EXTERNAL_URL`):

```go
// Package contracttest validates any platform-mcp endpoint against the §3.2
// contract: seven tools, exact schema names, park/resume behavior. Run it
// in-process (this repo) or against a container (CI image job, §8.5).
package contracttest

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/concourse/concourse/agent/api/questions"
)

// StubToken is the principal token the stub accepts.
const StubToken = "cap1.0.contract-test-token"

// StubATC is a minimal ATC: ticket read/spec/plan/task routes plus the real
// questions handler over a memory store.
type StubATC struct {
	t        *testing.T
	ticketID int
	store    *questions.MemoryStore
	server   *httptest.Server

	SpecVersions int
	PlanVersions int
	TaskUpdates  []map[string]any
}

func NewStubATC(t *testing.T, ticketID int) *StubATC {
	s := &StubATC{t: t, ticketID: ticketID, store: questions.NewMemoryStore()}
	qh := questions.NewHandler(s.store)
	qh.PollInterval = 20 * time.Millisecond

	mux := http.NewServeMux()
	base := fmt.Sprintf("/api/v1/agent/tickets/%d", ticketID)
	withTicket := func(next http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Authorization") != "Bearer "+StubToken {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			r.ParseForm()
			r.Form.Set(":ticket_id", fmt.Sprint(ticketID))
			if id := r.PathValue("question_id"); id != "" {
				r.Form.Set(":question_id", id)
			}
			next(w, r)
		}
	}

	mux.HandleFunc("GET "+base, withTicket(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"ticket":{"id":%d,"title":"contract ticket","state":"running"},`+
			`"spec":{"title":"contract spec","acceptance_criteria":["ok"],"body_md":"body"},`+
			`"tasks":[{"ordering":1,"title":"first","status":"pending","detail_md":"do the thing"}]}`, ticketID)
	}))
	mux.HandleFunc("POST "+base+"/spec", withTicket(func(w http.ResponseWriter, r *http.Request) {
		s.SpecVersions++
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"version":%d}`, s.SpecVersions)
	}))
	mux.HandleFunc("POST "+base+"/plan", withTicket(func(w http.ResponseWriter, r *http.Request) {
		s.PlanVersions++
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"plan_version":%d}`, s.PlanVersions)
	}))
	mux.HandleFunc("PUT "+base+"/tasks/{ordering}", withTicket(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		body["ordering"] = r.PathValue("ordering")
		s.TaskUpdates = append(s.TaskUpdates, body)
		w.WriteHeader(http.StatusOK)
	}))
	mux.HandleFunc("POST "+base+"/questions", withTicket(qh.AskQuestion))
	mux.HandleFunc("GET "+base+"/questions/{question_id}", withTicket(qh.GetQuestion))
	mux.HandleFunc("PUT "+base+"/questions/{question_id}/answer", withTicket(qh.AnswerQuestion))

	s.server = httptest.NewServer(mux)
	t.Cleanup(s.server.Close)
	return s
}

func (s *StubATC) URL() string { return s.server.URL }

// AutoAnswer answers every open question with the given answer after delay —
// the contract kit's stand-in human.
func (s *StubATC) AutoAnswer(answer, by string, delay time.Duration) {
	go func() {
		for {
			time.Sleep(delay)
			open, err := s.store.OpenForTicket(s.ticketID)
			if err != nil {
				return
			}
			for _, q := range open {
				_ = s.store.Answer(s.ticketID, q.ID, answer, by)
			}
		}
	}()
}
```

- [ ] Write `agent/platformmcp/contracttest/contracttest.go`:

```go
package contracttest

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"
)

// Run validates a serving platform-mcp endpoint (base URL WITHOUT /mcp)
// against the §3.2 contract. The endpoint must be configured against a
// StubATC with AutoAnswer enabled.
func Run(t *testing.T, endpointURL string) {
	t.Helper()

	rpc := func(method string, params any) map[string]any {
		t.Helper()
		var paramsJSON []byte
		if params != nil {
			paramsJSON, _ = json.Marshal(params)
		} else {
			paramsJSON = []byte("{}")
		}
		body := fmt.Sprintf(`{"jsonrpc":"2.0","id":1,"method":%q,"params":%s}`, method, paramsJSON)
		resp, err := http.Post(endpointURL+"/mcp", "application/json", bytes.NewReader([]byte(body)))
		if err != nil {
			t.Fatalf("%s: %v", method, err)
		}
		defer resp.Body.Close()
		var out map[string]any
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
			t.Fatalf("%s decode: %v", method, err)
		}
		return out
	}

	callTool := func(name string, args any) (string, bool) {
		t.Helper()
		out := rpc("tools/call", map[string]any{"name": name, "arguments": args})
		result, _ := out["result"].(map[string]any)
		if result == nil {
			t.Fatalf("tools/call %s: no result: %v", name, out)
		}
		content, _ := result["content"].([]any)
		if len(content) == 0 {
			t.Fatalf("tools/call %s: empty content", name)
		}
		text := content[0].(map[string]any)["text"].(string)
		isError, _ := result["isError"].(bool)
		return text, isError
	}

	// healthz (§8.5 readiness contract).
	resp, err := http.Get(endpointURL + "/healthz")
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("healthz: %v / %v", err, resp)
	}
	resp.Body.Close()

	// initialize.
	init := rpc("initialize", map[string]any{})
	if init["result"] == nil {
		t.Fatalf("initialize failed: %v", init)
	}

	// tools/list: exactly the seven §3.2 tools.
	list := rpc("tools/list", nil)
	tools := list["result"].(map[string]any)["tools"].([]any)
	names := map[string]bool{}
	for _, tool := range tools {
		names[tool.(map[string]any)["name"].(string)] = true
	}
	for _, want := range []string{"read_ticket", "list_tasks", "get_task", "submit_spec", "submit_plan", "update_task_status", "ask_human"} {
		if !names[want] {
			t.Fatalf("tools/list missing %q (got %v)", want, names)
		}
	}
	if len(names) != 7 {
		t.Fatalf("expected exactly 7 tools, got %v", names)
	}

	// read_ticket returns envelope + spec but NOT tasks (§3.2 read model).
	text, isErr := callTool("read_ticket", map[string]any{})
	if isErr || !bytes.Contains([]byte(text), []byte(`"ticket"`)) || !bytes.Contains([]byte(text), []byte(`"spec"`)) {
		t.Fatalf("read_ticket: isErr=%v %s", isErr, text)
	}
	if bytes.Contains([]byte(text), []byte(`"tasks"`)) {
		t.Fatalf("read_ticket must not include tasks: %s", text)
	}

	// list_tasks returns the skeleton; get_task returns detail; unknown ordering errors.
	text, isErr = callTool("list_tasks", map[string]any{})
	if isErr || !bytes.Contains([]byte(text), []byte(`"ordering"`)) {
		t.Fatalf("list_tasks: isErr=%v %s", isErr, text)
	}
	if bytes.Contains([]byte(text), []byte(`"detail_md"`)) {
		t.Fatalf("list_tasks must omit detail_md: %s", text)
	}
	text, isErr = callTool("get_task", map[string]any{"ordering": 1})
	if isErr || !bytes.Contains([]byte(text), []byte(`"detail_md"`)) {
		t.Fatalf("get_task: isErr=%v %s", isErr, text)
	}
	// Unknown ordering is an MCP tool error (isError=true), NOT a JSON-RPC -32602
	// error object — the shared mcpserver maps handler errors to isError results.
	unknownOrdering := rpc("tools/call", map[string]any{"name": "get_task", "arguments": map[string]any{"ordering": 999}})
	if unknownOrdering["error"] != nil {
		t.Fatalf("get_task unknown ordering must be a tool error, not a JSON-RPC error object: %v", unknownOrdering["error"])
	}
	if result, _ := unknownOrdering["result"].(map[string]any); result == nil || result["isError"] != true {
		t.Fatalf("get_task must reject an unknown ordering with a tool error (isError=true): %v", unknownOrdering)
	}

	// submit_spec: input error without title, version on success.
	if _, isErr := callTool("submit_spec", map[string]any{"body": "b"}); !isErr {
		t.Fatal("submit_spec accepted missing title")
	}
	text, isErr = callTool("submit_spec", map[string]any{"title": "t", "body": "b"})
	if isErr || !bytes.Contains([]byte(text), []byte(`"version"`)) {
		t.Fatalf("submit_spec: isErr=%v %s", isErr, text)
	}

	// submit_plan: minItems enforced, plan_version returned.
	if _, isErr := callTool("submit_plan", map[string]any{"tasks": []any{}}); !isErr {
		t.Fatal("submit_plan accepted empty tasks")
	}
	text, isErr = callTool("submit_plan", map[string]any{"tasks": []map[string]string{{"title": "a"}}})
	if isErr || !bytes.Contains([]byte(text), []byte(`"plan_version"`)) {
		t.Fatalf("submit_plan: isErr=%v %s", isErr, text)
	}

	// update_task_status: enum enforced, ok returned.
	if _, isErr := callTool("update_task_status", map[string]any{"ordering": 1, "status": "bogus"}); !isErr {
		t.Fatal("update_task_status accepted bogus status")
	}
	text, isErr = callTool("update_task_status", map[string]any{"ordering": 1, "status": "done"})
	if isErr {
		t.Fatalf("update_task_status: %s", text)
	}

	// ask_human parks then resumes via the stub's auto-answer.
	start := time.Now()
	text, isErr = callTool("ask_human", map[string]any{"question": "contract check?"})
	if isErr {
		t.Fatalf("ask_human: %s", text)
	}
	var answer struct {
		Answer     string `json:"answer"`
		AnsweredBy string `json:"answered_by"`
		TimedOut   bool   `json:"timed_out"`
	}
	if err := json.Unmarshal([]byte(text), &answer); err != nil {
		t.Fatalf("ask_human result: %v: %s", err, text)
	}
	if answer.Answer == "" || answer.TimedOut {
		t.Fatalf("ask_human did not resume with a real answer: %+v (waited %s)", answer, time.Since(start))
	}
}

// RunSSEHeartbeats validates the D4 MUST-stream contract on ask_human
// (2026-07-09 SSE seam delta): a progress-opted tools/call that parks longer
// than the heartbeat interval must yield >= 2 notifications/progress frames
// spaced < 30s apart BEFORE the final result frame, with the progressToken
// echoed verbatim. The endpoint must be configured with a sub-second
// ProgressInterval and an AutoAnswer delay of at least ~5x that interval.
func RunSSEHeartbeats(t *testing.T, endpointURL string) {
	t.Helper()

	body := `{"jsonrpc":"2.0","id":9,"method":"tools/call","params":{"name":"ask_human","arguments":{"question":"sse heartbeat check?"},"_meta":{"progressToken":"hb-1"}}}`
	req, err := http.NewRequest(http.MethodPost, endpointURL+"/mcp", bytes.NewReader([]byte(body)))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("ask_human SSE: %v", err)
	}
	defer resp.Body.Close()
	if got := resp.Header.Get("Content-Type"); got != "text/event-stream" {
		t.Fatalf("expected text/event-stream, got %q", got)
	}

	scanner := bufio.NewScanner(resp.Body)
	var progressTimes []time.Time
	var finalSeen bool
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		payload := strings.TrimPrefix(line, "data: ")
		if strings.Contains(payload, `"notifications/progress"`) {
			if finalSeen {
				t.Fatal("progress frame AFTER the final result frame")
			}
			if !strings.Contains(payload, `"hb-1"`) {
				t.Fatalf("progress frame missing the echoed progressToken: %s", payload)
			}
			progressTimes = append(progressTimes, time.Now())
			continue
		}
		if strings.Contains(payload, `"id":9`) {
			finalSeen = true
			if !strings.Contains(payload, "answer") {
				t.Fatalf("final frame missing the tool result: %s", payload)
			}
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("reading SSE stream: %v", err)
	}
	if !finalSeen {
		t.Fatal("never saw the final JSON-RPC response frame (it must be the LAST SSE frame)")
	}
	if len(progressTimes) < 2 {
		t.Fatalf("expected >= 2 progress heartbeats before the answer, got %d", len(progressTimes))
	}
	for i := 1; i < len(progressTimes); i++ {
		if gap := progressTimes[i].Sub(progressTimes[i-1]); gap >= 30*time.Second {
			t.Fatalf("heartbeat gap %s >= 30s (contracts §3.1 progress mandate)", gap)
		}
	}
}
```

  (add `"bufio"` and `"strings"` to `contracttest.go`'s imports).

- [ ] Run to verify pass:

```bash
go test ./agent/platformmcp/contracttest/
```

- [ ] Commit:

```bash
git add agent/platformmcp/contracttest/
git commit -m "feat(platform-mcp): contract-test kit + stub ATC" -m "Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 16: sidecar image packaging (§8.5) — Dockerfile + CI job

**Files:**
- Create: `deploy/Dockerfile.platform-mcp`
- Modify: the cicd pipeline config where dev-mcp's image job landed (survey Task 1 recorded the location; dev-mcp's job is the copyable template)

**Image rule (F28, 2026-07-09 checkpoint-seam delta §8 — the final stage is FROZEN):** the platform-mcp image is BOTH the platform sidecar image AND the checkpoint TASK MAIN image, so it must satisfy jetbridge's task-main constraint: POSIX `sh` plus `tail`, `mv`, `cat`, `sleep`, `mkdir`, `kill` (the pause command at `atc/worker/jetbridge/container.go:363` is `sh -c "trap 'exit 0' TERM; sleep 86400 & wait"`, and task steps run under the sh-based supervisor, `supervisor.go:35-77`). A distroless base has NO `sh` — the checkpoint pod never even reaches Running. Hence alpine, NOT distroless. The general constraint is recorded as a new §8.5 note in `00-shared-contracts.md`: **"any image used as a jetbridge task MAIN image needs POSIX sh + tail/mv/cat/sleep/mkdir/kill; distroless bases are sidecar-only"** — `mcp-dev-concourse` and `mcp-gateway` remain sidecar-only and MAY stay distroless. Sidecar use runs the ENTRYPOINT (MCP server mode); checkpoint task use ignores the ENTRYPOINT (explicit `run.path: platform-mcp`, resolved on PATH, executed under the sh supervisor).

- [ ] Write `deploy/Dockerfile.platform-mcp` (per §8.5: static Go binary, serves on `MCP_LISTEN_ADDR`, `GET /healthz`, non-root; builder stage per `deploy/Dockerfile.ci-agent`; final stage frozen per delta §8):

```dockerfile
# platform-mcp sidecar image (shared contracts §8.5)
# ghcr.io/tdmtrader/mcp-platform:<release-tag>
#
# ALSO the checkpoint TASK MAIN image (checkpoint-seam delta §8/F28): the
# final stage must keep POSIX sh + tail/mv/cat/sleep/mkdir/kill for
# jetbridge's pause command and sh-based task supervisor. Never distroless.
FROM golang:1.25-bookworm AS builder

WORKDIR /src
COPY . .
# agent/schema is a nested module resolved via the root go.mod replace.
RUN CGO_ENABLED=0 go build -o /platform-mcp ./cmd/platform-mcp

FROM alpine:3.21
RUN adduser -D -u 65532 nonroot
COPY --from=builder /platform-mcp /usr/local/bin/platform-mcp
# build-time smoke check: task-main-image contract (F28)
RUN command -v sh && for c in tail mv cat sleep mkdir kill; do command -v "$c" >/dev/null || exit 1; done
USER nonroot
ENV MCP_LISTEN_ADDR=:7781
EXPOSE 7781
ENTRYPOINT ["/usr/local/bin/platform-mcp"]
```

- [ ] Verify the image builds and serves, and that the task-main-image contract holds at run time too (requires Docker; on this machine Colima is usually down — if `docker info` fails, run this step on theborg CI only and verify the local binary path instead with `go build ./cmd/platform-mcp/`):

```bash
docker build -f deploy/Dockerfile.platform-mcp -t mcp-platform:dev . \
  && docker run --rm -d --name mcp-platform-smoke \
       -e ATC_EXTERNAL_URL=http://127.0.0.1:1 \
       -e AGENT_PRINCIPAL_TOKEN=cap1.0.smoke \
       -e AGENT_TICKET_ID=1 \
       -p 7781:7781 mcp-platform:dev \
  && sleep 2 && curl -fsS http://127.0.0.1:7781/healthz \
  && docker rm -f mcp-platform-smoke
# F28 runtime smoke: the checkpoint-task shape — sh supervisor + binary on PATH.
docker run --rm --entrypoint sh mcp-platform:dev \
  -c 'command -v platform-mcp && for c in tail mv cat sleep mkdir kill; do command -v "$c" >/dev/null || exit 1; done && echo task-main-contract-ok'
```

- [ ] Add the image job to the cicd pipeline, copying dev-mcp's template job verbatim with these substitutions: job name `build-mcp-platform`, dockerfile `deploy/Dockerfile.platform-mcp`, image `ghcr.io/tdmtrader/mcp-platform`, contract-test command `go test ./agent/platformmcp/contracttest/` (the kit's docker-mode wiring follows whatever shape dev-mcp's template established — same task file, different env). Then `fly -t cicd set-pipeline` per the template's own instructions (see Execution notes).

- [ ] Commit:

```bash
git add deploy/Dockerfile.platform-mcp ci/
git commit -m "feat(platform-mcp): sidecar image packaging per dev-mcp convention" -m "Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 17: ticket-page question banner + answer UI (Elm)

**Files:**
- Create: `web/elm/src/Concourse/AgentQuestion.elm`
- Create: `web/elm/src/AgentTicket/Questions.elm`
- Modify: `web/elm/src/Api/Endpoints.elm` (agent endpoints, `AgentFeedback` case at lines 207–209 today)
- Modify: ticket-core's ticket page module (path recorded by the Task 1 survey — the wave-2 charter's "Minimal Elm ticket page"; follow its existing task-list polling pattern)
- Test: `web/elm/tests/AgentQuestionTests.elm`

The banner is the UI half of the §8.4 decision: open questions surface on the ticket page (polled — consistent with the page's live task list), answering PUTs the answer route, and the parked run resumes. Option questions render one button per option; free-text questions render a textarea + submit.

- [ ] Write the failing test `web/elm/tests/AgentQuestionTests.elm`:

```elm
module AgentQuestionTests exposing (all)

import AgentTicket.Questions as Questions
import Concourse.AgentQuestion as AgentQuestion
import Expect
import Html.Attributes as Attr
import Json.Decode as JD
import Test exposing (Test, describe, test)
import Test.Html.Query as Query
import Test.Html.Selector exposing (attribute, class, tag, text)


sampleJson : String
sampleJson =
    """
    {"id":3,"ticket_id":42,"kind":"question",
     "question":"Which auth flow?","options":["legacy","oidc"],
     "timeout_policy":"park","timeout_seconds":0,
     "asked_at":1751900000,"build_id":7,"step_name":"implement"}
    """


all : Test
all =
    describe "agent questions"
        [ test "decoder reads the API row" <|
            \_ ->
                case JD.decodeString AgentQuestion.decoder sampleJson of
                    Ok q ->
                        Expect.all
                            [ \_ -> Expect.equal 3 q.id
                            , \_ -> Expect.equal 42 q.ticketId
                            , \_ -> Expect.equal [ "legacy", "oidc" ] q.options
                            , \_ -> Expect.equal Nothing q.answer
                            ]
                            ()

                    Err err ->
                        Expect.fail (JD.errorToString err)
        , test "option questions render one button per option" <|
            \_ ->
                case JD.decodeString AgentQuestion.decoder sampleJson of
                    Ok q ->
                        Questions.view
                            { draftAnswer = ""
                            , onDraftChanged = \_ -> ()
                            , onSubmit = \_ _ -> ()
                            }
                            [ q ]
                            |> Query.fromHtml
                            |> Query.findAll [ class "agent-question-option" ]
                            |> Query.count (Expect.equal 2)

                    Err err ->
                        Expect.fail (JD.errorToString err)
        , test "free-text questions render a textarea" <|
            \_ ->
                case JD.decodeString AgentQuestion.decoder """{"id":4,"ticket_id":42,"kind":"question","question":"why?","options":[],"timeout_policy":"park","timeout_seconds":0,"asked_at":1,"build_id":0,"step_name":""}""" of
                    Ok q ->
                        Questions.view
                            { draftAnswer = ""
                            , onDraftChanged = \_ -> ()
                            , onSubmit = \_ _ -> ()
                            }
                            [ q ]
                            |> Query.fromHtml
                            |> Query.has [ tag "textarea" ]

                    Err err ->
                        Expect.fail (JD.errorToString err)
        , test "no banner when no open questions" <|
            \_ ->
                Questions.view
                    { draftAnswer = ""
                    , onDraftChanged = \_ -> ()
                    , onSubmit = \_ _ -> ()
                    }
                    []
                    |> Query.fromHtml
                    |> Query.hasNot [ attribute (Attr.id "agent-question-banner") ]
        ]
```

- [ ] Run to verify it fails:

```bash
yarn test
```

Expected failure: `Cannot find module 'AgentTicket.Questions'` (elm-test compile error).

- [ ] Write `web/elm/src/Concourse/AgentQuestion.elm`:

```elm
module Concourse.AgentQuestion exposing (AgentQuestion, decoder)

import Json.Decode as JD


type alias AgentQuestion =
    { id : Int
    , ticketId : Int
    , kind : String
    , question : String
    , options : List String
    , askedAt : Int
    , answer : Maybe String
    , answeredBy : Maybe String
    }


decoder : JD.Decoder AgentQuestion
decoder =
    JD.map8 AgentQuestion
        (JD.field "id" JD.int)
        (JD.field "ticket_id" JD.int)
        (JD.field "kind" JD.string)
        (JD.field "question" JD.string)
        (JD.field "options" (JD.list JD.string))
        (JD.field "asked_at" JD.int)
        (JD.maybe (JD.field "answer" JD.string))
        (JD.maybe (JD.field "answered_by" JD.string))
```

- [ ] Write `web/elm/src/AgentTicket/Questions.elm` (pure view; effects stay in the page module):

```elm
module AgentTicket.Questions exposing (Config, view)

import Concourse.AgentQuestion exposing (AgentQuestion)
import Html exposing (Html, button, div, text, textarea)
import Html.Attributes exposing (class, id, placeholder, value)
import Html.Events exposing (onClick, onInput)


type alias Config msg =
    { draftAnswer : String
    , onDraftChanged : String -> msg
    , onSubmit : AgentQuestion -> String -> msg
    }


view : Config msg -> List AgentQuestion -> Html msg
view config open =
    case open of
        [] ->
            text ""

        questions ->
            div [ class "agent-question-banner", id "agent-question-banner" ]
                (List.map (viewQuestion config) questions)


viewQuestion : Config msg -> AgentQuestion -> Html msg
viewQuestion config q =
    div [ class "agent-question" ]
        [ div [ class "agent-question-kind" ]
            [ text
                (if q.kind == "checkpoint" then
                    "CHECKPOINT — agent parked awaiting approval"

                 else
                    "QUESTION — agent parked awaiting an answer"
                )
            ]
        , div [ class "agent-question-text" ] [ text q.question ]
        , if List.isEmpty q.options then
            div [ class "agent-question-freetext" ]
                [ textarea
                    [ placeholder "Type an answer to resume the run"
                    , value config.draftAnswer
                    , onInput config.onDraftChanged
                    ]
                    []
                , button
                    [ class "agent-question-submit"
                    , onClick (config.onSubmit q config.draftAnswer)
                    ]
                    [ text "Answer" ]
                ]

          else
            div [ class "agent-question-options" ]
                (List.map
                    (\opt ->
                        button
                            [ class "agent-question-option"
                            , onClick (config.onSubmit q opt)
                            ]
                            [ text opt ]
                    )
                    q.options
                )
        ]
```

- [ ] Add the endpoints to `web/elm/src/Api/Endpoints.elm` — extend the endpoint union next to `AgentFeedback` and add the cases beside it (lines 207–209):

```elm
        AgentTicketQuestions ticketId ->
            base |> appendPath [ "agent", "tickets", String.fromInt ticketId, "questions" ]

        AgentQuestionAnswer ticketId questionId ->
            base
                |> appendPath
                    [ "agent"
                    , "tickets"
                    , String.fromInt ticketId
                    , "questions"
                    , String.fromInt questionId
                    , "answer"
                    ]
```

  (Add `AgentTicketQuestions Int` and `AgentQuestionAnswer Int Int` to the type union at the top of the module, and satisfy the compiler everywhere the union is matched — the Elm compiler enumerates the sites.)

- [ ] Wire into the ticket page module (path from the Task 1 survey), following the page's existing task-list refresh pattern exactly:
  - model: `openQuestions : List AgentQuestion`, `draftAnswer : String`;
  - fetch: GET `AgentTicketQuestions ticketId` with `?open=true` on page load and on the page's existing polling tick (the wave-2 page already refreshes the task list; add the questions fetch to the same tick — do NOT add a second timer), decoding `JD.list AgentQuestion.decoder`;
  - answer: `onSubmit` sends PUT `AgentQuestionAnswer ticketId q.id` with body `{"answer": <answer>, "answered_by": <userName from the session model>}`, then re-fetches;
  - view: render `AgentTicket.Questions.view` at the top of the page body, above the lifecycle badge.

- [ ] Run to verify pass, plus a production build to catch union-match omissions:

```bash
yarn test && yarn build-elm
```

- [ ] Commit:

```bash
git add web/elm/
git commit -m "feat(web): agent question banner + answer UI on the ticket page" -m "Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 17b: "Awaiting human" state chip on the question banner (PARK-V2 §H, 2026-07-10)

**Files:**
- Modify: `web/elm/src/AgentTicket/Questions.elm`
- Modify: ticket-core's ticket page module (the Task 17 integration site)
- Test: `web/elm/tests/AgentQuestionTests.elm` (extend)

**Why (§H):** NO ticket-state enum change — the ticket stays `running`; parked-ness is DERIVED: "awaiting human" = (run status `awaiting_human` — pipeline-runs §C, plan 03) OR (open `agent_run_questions` rows exist). The Task 17 banner gains an explicit amber "AWAITING HUMAN" state chip driven by that derivation. The banner must now also render in the zero-open-questions window (row answered, continuation not yet running) when the run status alone says `awaiting_human` — so the empty-case condition changes. The run/pipeline-view badge is pipeline-runs' half (plan 03); this chip is the ticket-page half only.

- [ ] Write the failing tests — `view` gains a `Bool` (runAwaitingHuman). Add a shared config helper to `web/elm/tests/AgentQuestionTests.elm`, thread `False` through the four EXISTING `Questions.view` call sites (mechanical: `Questions.view <config> [ q ]` → `Questions.view <config> False [ q ]`), and append:

```elm
viewConfig : Questions.Config ()
viewConfig =
    { draftAnswer = ""
    , onDraftChanged = \_ -> ()
    , onSubmit = \_ _ -> ()
    }
```

```elm
        , test "awaiting-human chip renders when open questions exist" <|
            \_ ->
                case JD.decodeString AgentQuestion.decoder sampleJson of
                    Ok q ->
                        Questions.view viewConfig False [ q ]
                            |> Query.fromHtml
                            |> Query.has
                                [ attribute (Attr.id "agent-awaiting-human-chip")
                                , text "AWAITING HUMAN"
                                ]

                    Err err ->
                        Expect.fail (JD.errorToString err)
        , test "awaiting-human chip renders on run status alone (answered row, continuation pending)" <|
            \_ ->
                Questions.view viewConfig True []
                    |> Query.fromHtml
                    |> Query.has [ attribute (Attr.id "agent-awaiting-human-chip") ]
        , test "nothing renders with no open questions and a run not awaiting" <|
            \_ ->
                Questions.view viewConfig False []
                    |> Query.fromHtml
                    |> Query.hasNot [ attribute (Attr.id "agent-awaiting-human-chip") ]
```

- [ ] Run to verify it fails:

```bash
yarn test
```

Expected failure: elm-test compile error — `view` has the old 2-argument shape.

- [ ] Change `view` in `web/elm/src/AgentTicket/Questions.elm` and add the chip:

```elm
view : Config msg -> Bool -> List AgentQuestion -> Html msg
view config runAwaitingHuman open =
    if List.isEmpty open && not runAwaitingHuman then
        text ""

    else
        div [ class "agent-question-banner", id "agent-question-banner" ]
            (viewAwaitingChip :: List.map (viewQuestion config) open)


{-| viewAwaitingChip is the PARK-V2 §H "Awaiting human" state chip. It renders
whenever the banner does, because the banner's presence condition IS the §H
derivation — open park-policy questions OR run status `awaiting_human`. The
ticket-state enum is deliberately untouched (the ticket stays `running`);
`running` stops lying on the RUN surfaces, and this chip is the ticket-page
mirror of that.
-}
viewAwaitingChip : Html msg
viewAwaitingChip =
    div [ class "agent-awaiting-human-chip", id "agent-awaiting-human-chip" ]
        [ text "AWAITING HUMAN" ]
```

  (update the module's `exposing` line only if it enumerates internals — `Config`/`view` remain the public surface; style the chip amber via the `agent-awaiting-human-chip` class next to the banner's existing styles.)

- [ ] Wire the `Bool` in the ticket page module (the Task 17 integration site): `runAwaitingHuman = model.runStatus == Just "awaiting_human"` if the wave-2 page model carries the run status (pipeline-runs plan 03 adds the value to the run-status API and `fly runs`; the exact field name comes from the landed page — execution-time anchor). If the landed page has no run-status field yet, pass `False`: the open-questions half of the derivation still drives the chip through the banner condition, and the run-status half lights up when pipeline-runs' Elm leg gives the page the field.

- [ ] Run to verify pass, plus the production build:

```bash
yarn test && yarn build-elm
```

- [ ] Commit:

```bash
git add web/elm/
git commit -m "feat(web): awaiting-human state chip on the ticket-page question banner (PARK-V2 §H)" -m "Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 18: live theborg test — restart-while-parked

**Files:**
- Create: `atc/worker/jetbridge/live_parked_step_test.go`
- Test: itself (`//go:build live`, plain Go — NOT Ginkgo, per the live-test convention)

Two layers already cover the protocol: Task 9's `TestAwaitAnswerSurvivesATCRestart` proves the sidecar's long-poll rides through an ATC restart, and `TestLiveTaskResume` (`live_task_resume_test.go:33`) proves supervisor exec-session resume for a *producing* process. The remaining live risk is a **silently idle** main process (parked on a tool call, no output for minutes) surviving a severed web session in a pod with a sidecar — plus measuring what a parked pod costs. This test covers exactly that using the worker API (`FindOrCreateContainer` with `Sidecars`, per `live_sidecar_test.go:152-168`) and busybox containers, so no image publishing is needed.

- [ ] Write `atc/worker/jetbridge/live_parked_step_test.go`:

```go
//go:build live
// +build live

package jetbridge_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/runtime"
	"github.com/concourse/concourse/atc/worker/jetbridge"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// TestLiveParkedStepResume proves the restart-while-parked contract: a main
// process that is silently idle-waiting on a localhost sidecar (the ask_human
// pod shape) survives a severed web exec session, a fresh web takes over via
// attachOrRun, and the process resumes the moment the sidecar releases (the
// "human answers"). Marker discipline mirrors TestLiveTaskResume: exactly one
// "parked" proves no restart.
//
// Run against a THROWAWAY namespace (never cicd/concourse):
//
//	kubectl create ns jetbridge-parked-test
//	KUBECONFIG=~/.kube/config K8S_TEST_NAMESPACE=jetbridge-parked-test \
//	  go test -tags live -run '^TestLiveParkedStepResume$' -v -count=1 -timeout 10m \
//	  ./atc/worker/jetbridge/
//	kubectl delete ns jetbridge-parked-test
func TestLiveParkedStepResume(t *testing.T) {
	handle := "live-parked-" + time.Now().Format("150405")
	ctx := context.Background()
	clientset, cfg := kubeClient(t)
	cleanupPod(t, clientset, cfg.Namespace, handle)

	restConfig, err := jetbridge.RestConfig(*cfg)
	if err != nil {
		t.Fatalf("rest config: %v", err)
	}
	executor := jetbridge.NewSPDYExecutor(clientset, restConfig)

	containerSpec := runtime.ContainerSpec{
		TeamID:    1,
		ImageSpec: runtime.ImageSpec{ImageURL: "docker:///busybox"},
		Sidecars: []atc.SidecarConfig{
			{
				Name:  "platform-stub",
				Image: "busybox",
				// The stand-in sidecar: parks until "answered" (a file test
				// touches via exec), then serves one line on localhost:7781.
				Command: []string{"sh", "-c",
					"while [ ! -f /tmp/answer ]; do sleep 1; done; echo yes | nc -l -p 7781"},
			},
		},
	}
	// The stand-in agent: prints one marker, then waits SILENTLY (retrying
	// the localhost connect like the sidecar's long-poll retry loop) — no
	// output at all while parked, the property that distinguishes this from
	// TestLiveTaskResume.
	processSpec := runtime.ProcessSpec{
		Path: "sh",
		Args: []string{"-c",
			"echo parked; until RESP=$(nc 127.0.0.1 7781 </dev/null) && [ -n \"$RESP\" ]; do sleep 2; done; echo resumed:$RESP; exit 0"},
		TTY: &runtime.TTYSpec{
			WindowSize: runtime.WindowSize{Columns: 500, Rows: 500},
		},
	}

	// --- web 1: start, see the park marker, then die ---
	worker1, delegate1 := setupLiveWorker(t, handle)
	container1, _, err := worker1.FindOrCreateContainer(
		ctx, db.NewFixedHandleContainerOwner(handle),
		db.ContainerMetadata{Type: db.ContainerTypeTask}, containerSpec, delegate1)
	if err != nil {
		t.Fatalf("web1 FindOrCreateContainer: %v", err)
	}
	web1Ctx, cancelWeb1 := context.WithCancel(ctx)
	defer cancelWeb1()
	web1Out := &syncBuffer{}
	process1, err := container1.Run(web1Ctx, processSpec, runtime.ProcessIO{Stdout: web1Out, Stderr: web1Out})
	if err != nil {
		t.Fatalf("web1 Run: %v", err)
	}
	web1Done := make(chan error, 1)
	go func() { _, waitErr := process1.Wait(web1Ctx); web1Done <- waitErr }()

	deadline := time.Now().Add(90 * time.Second)
	for !strings.Contains(web1Out.String(), "parked") {
		select {
		case waitErr := <-web1Done:
			t.Fatalf("web1 Wait returned early: err=%v out=%q", waitErr, web1Out.String())
		case <-time.After(500 * time.Millisecond):
		}
		if time.Now().After(deadline) {
			t.Fatalf("never saw park marker; out=%q", web1Out.String())
		}
	}

	// Let it sit parked (idle, zero output) before severing — long enough
	// that any idle-session reaping would have shown itself.
	t.Logf("parked; idling 30s before severing the web session")
	time.Sleep(30 * time.Second)
	cancelWeb1()
	if waitErr := <-web1Done; waitErr == nil {
		t.Fatalf("web1 Wait unexpectedly succeeded; want severed-session error")
	}

	// Pod survives; measure what the parked pod costs.
	pod, err := clientset.CoreV1().Pods(cfg.Namespace).Get(ctx, handle, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("pod did not survive web1 death: %v", err)
	}
	for _, c := range pod.Spec.Containers {
		t.Logf("parked-pod cost: container=%s requests=%v limits=%v",
			c.Name, c.Resources.Requests, c.Resources.Limits)
	}
	t.Logf("parked-pod cost: qosClass=%s (record in the workstream notes)", pod.Status.QOSClass)

	// --- web 2: take over the same handle while still parked ---
	worker2, delegate2 := setupLiveWorker(t, handle)
	container2, _, err := worker2.FindOrCreateContainer(
		ctx, db.NewFixedHandleContainerOwner(handle),
		db.ContainerMetadata{Type: db.ContainerTypeTask}, containerSpec, delegate2)
	if err != nil {
		t.Fatalf("web2 FindOrCreateContainer: %v", err)
	}
	if _, err := container2.Attach(ctx, handle, runtime.ProcessIO{}); err == nil {
		t.Fatalf("web2 Attach unexpectedly succeeded; want no-completion-status error")
	} else if !strings.Contains(err.Error(), "no completion status") {
		t.Fatalf("web2 Attach unexpected error: %v", err)
	}
	web2Out := &syncBuffer{}
	process2, err := container2.Run(ctx, processSpec, runtime.ProcessIO{Stdout: web2Out, Stderr: web2Out})
	if err != nil {
		t.Fatalf("web2 Run: %v", err)
	}

	// The "human answers": release the sidecar via exec.
	if err := executor.ExecInPod(ctx, cfg.Namespace, handle, "platform-stub",
		[]string{"touch", "/tmp/answer"}, nil, nil, nil, false, jetbridge.ExecAttrs{}); err != nil {
		t.Fatalf("answering (exec touch): %v", err)
	}

	result, err := process2.Wait(ctx)
	if err != nil {
		t.Fatalf("web2 Wait: %v (out=%q)", err, web2Out.String())
	}
	if result.ExitStatus != 0 {
		t.Fatalf("expected exit 0 after resume, got %d (out=%q)", result.ExitStatus, web2Out.String())
	}
	out := web2Out.String()
	if got := strings.Count(out, "parked"); got != 1 {
		t.Fatalf("expected exactly 1 park marker (no restart), got %d (out=%q)", got, out)
	}
	if !strings.Contains(out, "resumed:yes") {
		t.Fatalf("expected resumed:yes in replayed output, got %q", out)
	}
	t.Logf("parked step survived web restart and resumed on answer: %q", out)
}
```

- [ ] Compile-check without a cluster (the build tag keeps it out of normal runs):

```bash
go vet -tags live ./atc/worker/jetbridge/
```

- [ ] Run it against theborg (kube-context `theborg`, https://theborg.home:6443; NEVER `cicd`/`concourse` namespaces):

```bash
kubectl create ns jetbridge-parked-test
KUBECONFIG=~/.kube/config K8S_TEST_NAMESPACE=jetbridge-parked-test \
  go test -tags live -run '^TestLiveParkedStepResume$' -v -count=1 -timeout 10m \
  ./atc/worker/jetbridge/
kubectl delete ns jetbridge-parked-test
```

Expected: PASS with the `parked-pod cost:` log lines; copy those lines into `forge/notes/` (or the track's cgx.md) — they answer the charter's "measure pause-pod cost while parked" risk.

- [ ] Commit:

```bash
git add atc/worker/jetbridge/live_parked_step_test.go
git commit -m "test(jetbridge): live restart-while-parked proof with pause-cost measurement" -m "Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 18b: MANDATORY real-CLI >5-minute park pin (D7, 2026-07-09 SSE seam delta — FIRST wave-3 deliverable; gates gateway 10 Task 7 merge)

**Files:**
- Create: `agent/platformmcp/live_cli_park_test.go` (`//go:build live`, plain Go — NOT Ginkgo)

This is the empirical pin that the F13 failure mode cannot regress: the REAL `claude` CLI (>= 2.1.77) is driven through an `ask_human` that parks **longer than 5 minutes**, against the real platform-mcp sidecar on the Task 9b transport. Without SSE heartbeats the CLI abandons the call at exactly 60s, silently, with the model seeing "(completed with no output)" — this test fails loudly on that signature. It is fully hermetic: no theborg, no real Anthropic API — `ANTHROPIC_BASE_URL` points at a local stub that scripts the model into exactly one `ask_human` tools/call and then echoes the tool result; it runs on any host with the claude CLI on PATH (`t.Skip` otherwise) and is pinnable in CI. Gateway does NOT duplicate this pin; 10 Task 7's contract tests cover its side with the 40s-fake-adapter SSE assertion.

- [ ] Write `agent/platformmcp/live_cli_park_test.go`:

```go
//go:build live
// +build live

package platformmcp_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/concourse/concourse/agent/platformmcp"
	"github.com/concourse/concourse/agent/platformmcp/contracttest"
)

// sseRecorder tees the sidecar's responses and timestamps every
// notifications/progress frame — the CLI consumes the stream, so cadence is
// observed at the wire, between CLI and sidecar.
type sseRecorder struct {
	mu    sync.Mutex
	times []time.Time
	next  http.Handler
}

func (rec *sseRecorder) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	rec.next.ServeHTTP(&recordingWriter{ResponseWriter: w, rec: rec}, r)
}

type recordingWriter struct {
	http.ResponseWriter
	rec *sseRecorder
}

func (w *recordingWriter) Write(p []byte) (int, error) {
	if bytes.Contains(p, []byte("notifications/progress")) {
		w.rec.mu.Lock()
		w.rec.times = append(w.rec.times, time.Now())
		w.rec.mu.Unlock()
	}
	return w.ResponseWriter.Write(p)
}

func (w *recordingWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// anthropicStub is the hermetic model: turn 1 (no tool_result in the request)
// scripts one ask_human tool_use; turn 2 echoes the tool result as text.
func anthropicStub(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/v1/messages") {
			http.NotFound(w, r)
			return
		}
		raw, _ := io.ReadAll(r.Body)
		body := string(raw)

		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)

		if !strings.Contains(body, "tool_result") {
			writeAnthropicEvents(w,
				`{"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"stub","content":[],"stop_reason":null,"usage":{"input_tokens":1,"output_tokens":1}}}`,
				`{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_1","name":"mcp__platform__ask_human","input":{}}}`,
				`{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"question\":\"Live park pin: proceed?\"}"}}`,
				`{"type":"content_block_stop","index":0}`,
				`{"type":"message_delta","delta":{"stop_reason":"tool_use","stop_sequence":null},"usage":{"output_tokens":5}}`,
				`{"type":"message_stop"}`,
			)
			return
		}

		echoJSON, _ := json.Marshal("TOOL RESULT: " + extractToolResult(body))
		writeAnthropicEvents(w,
			`{"type":"message_start","message":{"id":"msg_2","type":"message","role":"assistant","model":"stub","content":[],"stop_reason":null,"usage":{"input_tokens":1,"output_tokens":1}}}`,
			`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
			fmt.Sprintf(`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":%s}}`, echoJSON),
			`{"type":"content_block_stop","index":0}`,
			`{"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"output_tokens":5}}`,
			`{"type":"message_stop"}`,
		)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func writeAnthropicEvents(w http.ResponseWriter, events ...string) {
	f, _ := w.(http.Flusher)
	for _, data := range events {
		var probe struct {
			Type string `json:"type"`
		}
		_ = json.Unmarshal([]byte(data), &probe)
		fmt.Fprintf(w, "event: %s\ndata: %s\n\n", probe.Type, data)
		if f != nil {
			f.Flush()
		}
	}
}

func extractToolResult(body string) string {
	var req struct {
		Messages []struct {
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal([]byte(body), &req); err != nil {
		return ""
	}
	for i := len(req.Messages) - 1; i >= 0; i-- {
		var blocks []map[string]any
		if err := json.Unmarshal(req.Messages[i].Content, &blocks); err != nil {
			continue
		}
		for _, b := range blocks {
			if b["type"] == "tool_result" {
				out, _ := json.Marshal(b["content"])
				return string(out)
			}
		}
	}
	return ""
}

// TestLiveCLIParkPin is the mandatory F13 pin (D7): the REAL claude CLI parks
// on ask_human for > 5 minutes and MUST receive the human answer, with ~15s
// progress heartbeats observed at the wire throughout the park. Run:
//
//	go test -tags live -run '^TestLiveCLIParkPin$' -v -count=1 -timeout 12m \
//	  ./agent/platformmcp/
func TestLiveCLIParkPin(t *testing.T) {
	claudeBin, err := exec.LookPath("claude")
	if err != nil {
		t.Skip("claude CLI not on PATH — the F13 pin requires the real CLI (>= 2.1.77)")
	}
	if out, verr := exec.Command(claudeBin, "--version").CombinedOutput(); verr == nil {
		t.Logf("claude CLI: %s (need >= 2.1.77 — the version the F13 experiment pinned)", strings.TrimSpace(string(out)))
	}

	const parkFor = 5*time.Minute + 30*time.Second // answered at t+5m30s: > 5 minutes parked

	// (1) stub ATC: the real questions handler over a memory store; the
	// stand-in human answers every open question parkFor after it appears.
	stub := contracttest.NewStubATC(t, 42)
	stub.AutoAnswer("push on", "live-pin", parkFor)

	// (2) the REAL platform-mcp sidecar on the upgraded atc/api/mcpserver —
	// ProgressInterval 0 = the frozen 15s default heartbeat.
	srv, err := platformmcp.NewServer(platformmcp.Config{
		ATCURL:         stub.URL(),
		PrincipalToken: contracttest.StubToken,
		TicketID:       42,
		StepName:       "live-cli-park",
		TimeoutPolicy:  "park",
		ListenAddr:     ":0",
	})
	if err != nil {
		t.Fatal(err)
	}
	recorder := &sseRecorder{next: srv.Mux()}
	sidecar := httptest.NewServer(recorder)
	defer sidecar.Close()

	// (3) hermetic model + MCP config pointing ONLY at the sidecar.
	model := anthropicStub(t)
	mcpConfig := filepath.Join(t.TempDir(), "mcp.json")
	cfg := fmt.Sprintf(`{"mcpServers":{"platform":{"type":"http","url":"%s/mcp"}}}`, sidecar.URL)
	if err := os.WriteFile(mcpConfig, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 9*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, claudeBin,
		"-p", "Call the ask_human tool once, then repeat its answer verbatim.",
		"--strict-mcp-config", "--mcp-config", mcpConfig,
		"--output-format", "text",
	)
	cmd.Env = append(os.Environ(),
		"ANTHROPIC_BASE_URL="+model.URL,
		"ANTHROPIC_API_KEY=live-pin-dummy",
	)
	start := time.Now()
	out, err := cmd.CombinedOutput()
	elapsed := time.Since(start)
	transcript := string(out)
	t.Logf("claude ran %s; transcript:\n%s", elapsed, transcript)
	if err != nil {
		t.Fatalf("claude CLI failed: %v", err)
	}

	// (4) it really parked > 5 minutes. (The AutoAnswer sweep clock starts a
	// beat before the CLI launches, so compare against the 5-minute floor,
	// not the full 5m30s.)
	if elapsed < 5*time.Minute {
		t.Fatalf("CLI returned after %s — it cannot have parked > 5 minutes", elapsed)
	}
	// (5a) the human answer made it back through the > 5-minute park.
	if !strings.Contains(transcript, "push on") {
		t.Fatalf("transcript missing the human answer %q", "push on")
	}
	// (5b) the F13 failure signature must NOT appear.
	if strings.Contains(transcript, "(completed with no output)") {
		t.Fatal("F13 REGRESSION: the CLI silently abandoned the parked tools/call")
	}
	// (5c) ~15s heartbeat cadence throughout the park, observed at the wire.
	recorder.mu.Lock()
	times := append([]time.Time(nil), recorder.times...)
	recorder.mu.Unlock()
	if len(times) < 15 { // 5m30s / 15s ≈ 22 frames; allow scheduling slack
		t.Fatalf("expected >= 15 progress frames across the park, got %d", len(times))
	}
	for i := 1; i < len(times); i++ {
		if gap := times[i].Sub(times[i-1]); gap >= 30*time.Second {
			t.Fatalf("heartbeat gap %s >= 30s at frame %d (contracts §3.1)", gap, i)
		}
	}
	t.Logf("park pin: %d progress frames over %s, answer delivered", len(times), elapsed)
}
```

- [ ] Compile-check without the CLI (the build tag keeps it out of normal runs):

```bash
go vet -tags live ./agent/platformmcp/
```

- [ ] Run it (any host with the claude CLI; NO cluster or theborg needed):

```bash
go test -tags live -run '^TestLiveCLIParkPin$' -v -count=1 -timeout 12m ./agent/platformmcp/
```

Expected: PASS in ~6 minutes with `park pin: N progress frames over 5mXXs` in the log. **Sequencing (D9):** this test is the FIRST wave-3 deliverable and its PASS gates the merge of gateway 10 Task 7 — park empirically cannot work without the SSE transport, so nothing downstream builds on an unpinned seam.

- [ ] Commit:

```bash
git add agent/platformmcp/live_cli_park_test.go
git commit -m "test(platform-mcp): mandatory real-CLI >5-minute park pin (F13/D7)" -m "Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 19: full verification sweep

**Files:** none new — runs the workstream's whole surface.

- [ ] Go side (PostgreSQL running; ~5 min):

```bash
go vet ./agent/... ./cmd/platform-mcp/ && \
go test ./agent/api/questions/ ./agent/notify/ ./agent/platformmcp/... ./cmd/platform-mcp/ && \
ginkgo ./atc/api/mcpserver/ && \
ginkgo ./atc/db/migration/ && ginkgo ./atc/db/ && \
ginkgo ./atc/wrappa/ && ginkgo ./atc/api/ && ginkgo ./atc/atccmd/ && ginkgo ./atc/auditor/
```

- [ ] The mandatory F13 park pin (Task 18b — required by this sweep on any host with the claude CLI; it `t.Skip`s cleanly elsewhere, but a wave-3 sign-off needs one PASSING run on record):

```bash
go test -tags live -run '^TestLiveCLIParkPin$' -v -count=1 -timeout 12m ./agent/platformmcp/
```

- [ ] Elm side:

```bash
yarn test && yarn build-elm
```

- [ ] Broad unit tier as the final gate (CLAUDE.md: ~3 min, 79 suites):

```bash
make test-quick
```

- [ ] If anything was fixed during the sweep, commit the fixes:

```bash
git add -A && git commit -m "fix(platform-mcp): verification sweep fixes" -m "Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

## Execution notes

**Full workstream test suite** (fastest-first):

```bash
go test ./agent/api/questions/ ./agent/notify/ ./agent/platformmcp/... ./cmd/platform-mcp/   # plain Go, no DB
ginkgo ./atc/api/mcpserver/                 # Task 9b SSE transport (mirrored anti-drift tests)
ginkgo ./atc/db/migration/ ./atc/db/        # needs PostgreSQL (pg_isready); never --race
ginkgo ./atc/wrappa/ ./atc/api/ ./atc/auditor/ ./atc/atccmd/
yarn test && yarn build-elm
make test-quick                             # final gate
go test -tags live -run '^TestLiveCLIParkPin$' -v -count=1 -timeout 12m ./agent/platformmcp/  # F13 pin (needs claude CLI; skips without)
```

**Live-test requirements (theborg):** `KUBECONFIG=~/.kube/config`, kube-context `theborg` (https://theborg.home:6443), a THROWAWAY namespace via `kubectl create ns` (never `cicd`/`concourse` — live workloads), no pod-security label. Colima/Docker is usually down on this machine, so testcontainers is not an option — theborg is the live target. The Task 18 test cleans up its pod via `cleanupPod` + namespace deletion. The Task 16 docker smoke also needs Docker; when unavailable locally, rely on the theborg CI image job (dev-mcp's template) to build/contract-test/push `ghcr.io/tdmtrader/mcp-platform`, then `fly -t cicd set-pipeline` on concourse.home per the template's instructions.

**Manual end-to-end sanity (post-merge, wave-3 hand-written pipeline):** add the platform sidecar to an `agent:` step (`sidecars: [platform]`, image `ghcr.io/tdmtrader/mcp-platform:<tag>`), set `--agent-notify-webhook-url` to an ntfy topic, have the agent call `ask_human`; verify the run shows `running` while parked (pipeline-runs contract §1.5), the webhook fires within ~10s, the banner renders on the ticket page, answering resumes the step, and a checkpoint step (`platform-mcp checkpoint --name plan-approval`) blocks/exits per the approve/reject buttons. **PARK-V2 sanity (2026-07-10):** at the 30m `--agent-short-park-max` default a quick manual answer never leaves the SSE path; to exercise the exit half, set the flag to `1m`, let the park cross, and verify the sidecar wrote `flight/park.json` in the agent pod (checkpoint variant: the client logs "parked past the short-park threshold" and exits 3), the run shows `awaiting_human` with the amber chip on ticket + run views, and answering re-arms a continuation whose re-issued call resolves instantly (the §E fast path — `human.answer` with `"resumed": true` in the flight events).

**Seed-prompt convention (default `spec_delivery: mcp`):** the workflow's first agent step prompt instructs the agent to begin by calling `read_ticket` and `list_tasks` (then `get_task` per task as it works). No spec/plan bytes are injected — the read tools are the entry point. Keep this to a one-line prompt convention; the prompt text itself is authored in the workflow definition (workflow-store), not here.

**Amendment log (this plan):**
- 2026-07-08 (platform-mcp-hitl wave-3; FROZEN DELTA "spec/plan via granular platform-mcp read tools + optional file mount"): replaced the read model. `read_ticket` now returns envelope + spec ONLY (tasks removed); added `list_tasks` (skeleton) and `get_task(ordering)` (detail; unknown ordering → MCP tool error `isError=true`, matching how the shared `atc/api/mcpserver` surfaces handler errors — NOT a JSON-RPC `-32602` object) to Task 10 (registration, handlers via `ATCClient.GetTicket`, tests) and Task 15 (contract kit + stub). `update_task_status` unchanged. Tool count 5 → 7 updated everywhere it is enumerated (Goal, Architecture, scope table, Task 10 header/intro/tests, Task 15 `Run`). No spec/plan bytes are injected by default; the optional `files` mode (workflow field `spec_delivery`) is owned by workflow-store + dispatch (wave 4) and does not change this plan's tasks. Affected workstreams: platform-mcp-hitl, dispatch, workflow-store, ticket-core-consumers. Contract addendum bullet added to the Task 1 §11 block.
- 2026-07-09 (design-review F1 — checkpoint render contract co-signed with dispatch; owners: platform-mcp-hitl + dispatch): recorded the render contract for the checkpoint mechanism this plan OWNS. Dispatch's renderer (wave 4) materializes a checkpoint declaration as a plain `atc.TaskStep` running the **deterministic `platform-mcp checkpoint --name <n>` client** (Task 14 / `cmd/platform-mcp/checkpoint.go`), NOT an LLM `AgentStep` — there is no model awaiting the checkpoint; a fixed CLI POSTs the sidecar's `POST /checkpoint` and blocks on its HTTP response. Confirmed the plan documents the client's exit-code semantics explicitly (approve → exit 0 → step succeeds; reject or transport-failure-after-retries → exit 1 → step fails → run fails → ticket `needs_review`; usage error → exit 2) and the `on_reject` mapping (`on_reject: fail` = the default this plan implements: client exits non-zero → step fails → run fails → ticket `needs_review`; `on_reject: send_back` = dispatch's renderer branches on the non-zero exit to route the run back and signal the sent-back outcome — renderer-owned, out of scope here; at the step level both values fail the step on reject). Reworded the Task 14 intro to state the render contract and enumerate the exit-code/on_reject mapping. Verified no task text implies the checkpoint is awaited by an LLM prompt (the sidecar `handleCheckpoint` "await" is the deterministic HTTP handler blocking on the answer row). This co-signs the already-frozen contracts §11 F1 bullet (2026-07-09, dispatch + platform-mcp-hitl) and decision 12, which are authoritative for the render contract; no new contracts bullet is added (the decision already exists there). [PARTIALLY SUPERSEDED by the 2026-07-09 checkpoint-seam entry (final review F14 + F28) below, mirroring the marker on contracts §11's F1 bullet: the `on_reject` branch lives in dispatch's **run-completion reconciler** (plan 11 Task 11b), NOT the renderer — the renderer emits the identical bare failing TaskStep for both `on_reject` values; and a rejected `send_back` checkpoint re-queues the ticket (`running`→`queued`, `attempt_count++`) rather than routing to `needs_review`. The "reject → run fails → ticket `needs_review`" chain above is unconditional only for `on_reject: fail`; the exit-code semantics (approve → 0, reject/transport-failure → 1, usage → 2) remain current.]
- 2026-07-09 (design-review F7 — timeout defense-in-depth; owner: platform-mcp-hitl): added a cross-field check to `ConfigFromEnv` (Task 9, `agent/platformmcp/config.go`) mirroring workflow-store's new validation — a sidecar config with `PLATFORM_MCP_ASK_TIMEOUT_POLICY ∈ {default,fail}` but `PLATFORM_MCP_ASK_TIMEOUT_SECONDS <= 0` now fails loudly at startup (`must be > 0 when ... is %q`) rather than parking indefinitely; `park`+0 stays legal (wait forever). Added `TestConfigFromEnvTimeoutPolicyRequiresPositiveSeconds` to `agent/platformmcp/config_test.go` asserting default/fail+0 error and park+0 succeed. Defense-in-depth: the renderer (wave 4) normally guarantees this, but a hand-set sidecar env must not silently degrade.
- 2026-07-09 (final review F14 + F28 — checkpoint-seam delta legs owned by platform-mcp-hitl; co-signed dispatch/ticket-core/contracts): **(F14)** the sidecar-POST `/checkpoint` model is now the SINGLE frozen checkpoint mechanism — the Task 1 addendum bullet gained the frozen response codes (200 resolved / 400 bad name / 502 ATC transport error with the reservation kept) and now records that it SUPERSEDES (not merely amends) contracts §3.2's retracted client→ATC wording ("client inserts the row / long-polls the ATC route / reads reject-policy from argv / NOT a sidecar endpoint" — all retracted); corrected "`on_reject: send_back` semantics live in dispatch's renderer" to "live in dispatch's **run-completion reconciler** (plan 11 Task 11b)" in both the addendum bullet and the Task 14 intro (the renderer emits the identical bare failing TaskStep for both values; the reconciler branches on the latest answered checkpoint row after the run completes). Task 14 mechanics (handleCheckpoint + ckOpen dedup + runCheckpoint client, exit 0/1/2, 60x5s retry) are unchanged and now the single frozen model; the Task 14 intro also records the delta §2/§3 sidecar-env seam (renderer-emitted literal env + AGENT_PRINCIPAL_TOKEN secretKeyRef from `agent-run-((run_id))`/`principal-token`, gated by `--kubernetes-sidecar-secret-prefixes`; the CLIENT gets no token). NEW Task 14b adds the delta's two additive surfaces with TDD: `Store.ListByRun(pipelineRunID)` (memory + SQL + fake; newest-first ordering asserted — consumed by the reconciler's `dispatch.QuestionLister`) and answer-route validation for `kind='checkpoint'` rows (answer must be one of the row's options, empty included — tests `TestListByRun`, `TestAnswerCheckpointRejectsNonOptionAnswer`, and a factory It-block). **(F28)** `deploy/Dockerfile.platform-mcp` (Task 16) replaced its distroless final stage with the delta §8 FROZEN `alpine:3.21` stage (nonroot uid 65532, binary at `/usr/local/bin/platform-mcp`, build-time `command -v` smoke for sh/tail/mv/cat/sleep/mkdir/kill, ENTRYPOINT, `MCP_LISTEN_ADDR=:7781`) — the image is BOTH the platform sidecar image and the checkpoint TASK MAIN image, and jetbridge's pause command (`container.go:363`, `sh -c ...`) plus sh-based supervisor (`supervisor.go:35-77`) make a shell mandatory; added the runtime docker smoke for the task-main shape and documented the constraint ("task MAIN images need POSIX sh + tail/mv/cat/sleep/mkdir/kill; distroless is sidecar-only" — normative §8.5 note lives in 00).
- 2026-07-09 (final review F13 + F31 leg 3 — SSE seam delta D2/D4/D6/D7, platform legs; owner: platform-mcp-hitl): **(D2, new Task 9b — lands BEFORE Task 10 and gates gateway 10 Task 7)** upgraded `atc/api/mcpserver` IN PLACE with a byte-similar MIRRORED port of `ci-agent/devmcp`'s SSE progress path (modules must not require each other): `DefaultHeartbeat = 15s`, `NewServerWithHeartbeat(d)` (d <= 0 → default; `NewServer()` unchanged so ATC callers compile), BREAKING 3-arg `ToolHandler` gaining `progress func(string)` (buffered calls get a no-op; all handler literals updated mechanically via one perl one-liner), `callToolParams` gains `_meta.progressToken`, SSE gating on `Accept: text/event-stream` AND the token, `event: message` frames, coalescing heartbeat ticker (buffered chan 64, non-blocking send, `lastMsg` default `running <tool>`), final JSON-RPC response as the LAST SSE frame; the locked "handler error ⇒ isError=true, never -32602" mapping preserved in BOTH modes; mirrored SSE tests added to `server_test.go` as the anti-drift guard. **(D6, Task 9)** `ATCClient.do` now returns `*StatusError{Method,Path,Code,Body}` for every non-2xx; added `ErrPrincipalRejected` and `AuthFailureLimit` (frozen default 12 ≈ >= 60s at 5s retry, outliving the §1.2 60s verification cache); `AwaitAnswer` counts CONSECUTIVE 401/403 (reset on success or non-auth error) and returns the wrapped fatal at the limit while still retrying transport/5xx forever — the contradicting doc comments were rewritten; three frozen unit tests added (fatal-after-exactly-12-401s, 5xx-never-fatal, alternating-counter-reset); `Config.ProgressInterval` + `PLATFORM_MCP_PROGRESS_INTERVAL` parsing added (unset = 15s default; set-but-invalid/<= 0/> 30s = fatal, never clamped; `TestConfigFromEnvProgressInterval`). **(D4/D6 propagation)** Task 10 constructs via `NewServerWithHeartbeat(cfg.ProgressInterval)` and its `ListenAndServe` sets the frozen server-timeout rule (`WriteTimeout: 0`, `IdleTimeout: 0`, `ReadHeaderTimeout: 5s`); Task 11's `ask_human` is a MUST-stream tool — it calls `progress("parked: waiting for human answer to question <id>")` at park start and surfaces `ErrPrincipalRejected` as a LOUD `principal rejected:`-prefixed tool error (test `TestAskHumanPrincipalRejectedFailsLoudly`); Task 13 gained the binary-level env-validation smoke (`TestServeModeFailsFastOnBadProgressInterval`); Task 14 records the checkpoint SSE EXEMPTION (POST /checkpoint is not an MCP tools/call — no claude CLI in the loop) with its own hardening (no-timeout `http.Client`, D4 mux timeouts, fatal-auth ⇒ exit 1 + `principal rejected:` stderr prefix echoed from the sidecar's 502 body — tests `TestCheckpointClientPrincipalRejected` and the handleCheckpoint `errors.Is` branch); Task 15's kit gained `RunSSEHeartbeats` (>= 2 progress frames spaced < 30s before the final frame on a parked ask_human; self-test `TestPlatformMCPSSEHeartbeats`). **(D7, new Task 18b)** the mandatory real-CLI park pin `agent/platformmcp/live_cli_park_test.go` (`//go:build live`): the REAL `claude` CLI (>= 2.1.77, `--strict-mcp-config`, hermetic `ANTHROPIC_BASE_URL` stub scripting one ask_human then echoing the tool result) parks > 5 minutes (stub ATC answers at t+5m30s); asserts the answer is delivered, "(completed with no output)" never appears, and ~15s heartbeat cadence at the wire (>= 15 frames, every gap < 30s) — declared the FIRST wave-3 deliverable, gating 10 Task 7 merge, and added to Task 19's sweep. Exit codes and `MCP_LISTEN_ADDR`/ports/endpoints unchanged (D8: no image content changes beyond the F28 leg above).
- 2026-07-10 (PARK-V2 seam delta — exit-and-respawn for long human-waits; sidecar/questions halves owned here; implements FLOWS.md P2.5 #1–#4; co-signed agent-step/pipeline-runs/dispatch/contracts; decisions 30–32; amends but does NOT retract the 2026-07-09 SSE/park entries — Tasks 9b/11/18b all stand as the SHORT-PARK mechanism): past `--agent-short-park-max` (default 30m; `0` = pure PARK-V1, the rollback hatch) a wait stops impersonating a running step. **(§A, new Task 9c)** `Config` gains `ShortParkMax` (`PLATFORM_MCP_SHORT_PARK_MAX_SECONDS`, integer seconds; bounds-validated — negative/garbage fatal at startup, never clamped; 0 legal; the timeout-policy `park`+0 rule untouched and orthogonal) and `ParkPath` (`PLATFORM_MCP_PARK_PATH`, new Task 1 addendum §8.1 row; unset = never write — the legal checkpoint-pod shape, NOT a startup error). The threshold timer is sidecar-owned, measured from the row's `asked_at`, and applies to BOTH park kinds. **(§B1/§B3, new Task 11b)** at an `ask_human` crossing the sidecar atomically writes the frozen `flight/park.json` sentinel (`parkexit.go` `writeSentinelAtomic`: same-dir temp + rename — the runner's 5s stat loop can never see a partial file) with payload `{question_id, kind, step_name, asked_at RFC3339, threshold_seconds, crossed_at RFC3339}`, KEEPS the row open (the durable representation of the wait), and expects the client disconnect — the runner SIGTERMs claude and `AwaitAnswer` cancels on the request context (`TestAskHumanWritesParkSentinelAtThreshold` pins sentinel shape, atomicity, row-stays-open, and disconnect-does-not-resolve); an unset `ParkPath` at an `ask_human` crossing degrades LOUDLY (log line) to the SSE park — PARK-V1 behavior, bounded platform-side by `--agent-park-timeout`, whose semantics the delta REVISES to bounding the `awaiting_human` wall clock (lifecycler-owned, plan 03; recorded in this plan's Context bullet). **(§B4, new Task 14c)** `/checkpoint` at threshold answers the blocked POST `202 {"parked": true}` (`AwaitAnswer`'s deadline leg doubles as the timer; row stays open; reservation kept; best-effort sentinel when a flight volume exists); the checkpoint client gains FROZEN exit code 3 = parked-past-threshold (0/1/2 unchanged; Task 14's exit-code block amended) — exit 3 fails the TaskStep, the §B5 carrier; the authority the platform resumes on is the OPEN question row, never the build status. **(§E, new Task 5b + Task 10 edit)** idempotency-by-question: migration `1773106072` adds `question_hash` + UNIQUE `agent_run_questions_dedup (pipeline_run_id, step_name, kind, question_hash) WHERE pipeline_run_id IS NOT NULL`; `ComputeQuestionHash` is the frozen `hex(sha256(question || '\x00' || options-joined-by-'\x00'))`; the ask route computes the hash SERVER-SIDE and `Store.Ask` becomes race-safe find-or-create (`ON CONFLICT ... DO NOTHING` + re-select; answered row returned as-is = the resume fast path, open row joined — same id, ORIGINAL park clock); Task 4's specs amended in place to vary question text (the key excludes `ticket_id`); `ckOpen` DEMOTED to a same-pod optimization (`ckReservation{ID, AskedAt}`); the `ask_human` tool description gains the vary-the-text note; fast paths pinned by `TestAskFindOrCreate`/`TestAskRouteIdempotentByQuestion`/`TestAskHumanAnsweredRowFastPath`/`TestCheckpointRePostAfterAnswerResolvesImmediately` (the `human.answer` fast-path event carries `"resumed": true`). **(§D, new Task 6b)** `AnswerAgentQuestion` additionally fires the dispatcher component notify (`Handler.OnAnswer` hook → `dbConn.Bus().Notify("agent_dispatcher")`; fired ONLY on a successful write — `TestAnswerFiresOnAnswerHook`; polling remains the guaranteed path, never notify-only; a no-op until plan 11's component lands). **(§H, new Task 17b)** the Task 17 banner gains the derivation-only "AWAITING HUMAN" chip (open questions OR run status `awaiting_human`) — NO ticket enum change, the ticket stays `running`; the banner now renders on run status alone so the answered-row/continuation-pending window stays visible. OWNED ELSEWHERE (scope-out, recorded for cross-reference): `awaiting_human` run status + lifecycler entry/exit/72h expiry (pipeline-runs, migration `1773106032`), the runner sentinel-watch/SIGTERM/exit-86 + `flight/session.jsonl` + stream-json teeing (agent-step plan 07, incl. the gating §I pin `TestLiveClaudeParkExitResume`), `agent_run_step_state` + replay/resume (agent-step, migration `1773106065`), `reconcileAwaitingRuns` + principal/secret re-mint + continuation build (dispatch plan 11 Task 11c). Every leg here is inert at `PLATFORM_MCP_SHORT_PARK_MAX_SECONDS=0` — the delta's zero-schema-waste rollback.
- 2026-07-08 (consistency fix; owner: platform-mcp-hitl): corrected the `get_task` unknown-ordering error mechanism. The plan claimed an unknown ordering yields a JSON-RPC `-32602` error object, but Task 10's handler returns a plain `fmt.Errorf`, and the shared `atc/api/mcpserver` (already committed) maps every handler error to a `tools/call` result with `isError=true` — it only emits `-32602` for a malformed `tools/call` envelope, never for a handler's returned error (locked in by its committed tests). Reworded the §3.2 read-model addendum bullet (Task 1), the Task 10 intro + `getTask` doc comment, and the Task 10 header amendment (08:78) to state "MCP tool error (`isError=true`)" rather than "JSON-RPC `-32602`". Strengthened the Task 10 and Task 15 unknown-ordering tests to assert the response carries NO top-level JSON-RPC `error` object AND `result.isError=true` (previously they only checked `isErr` truthiness, which passed regardless of mechanism). Matching bullet appended to the contracts §11 amendment log.

**Rollback notes for the risky diffs:**
- *Route registration (Task 6)* touches four exhaustive switches; a missed entry panics the ATC at wrap time — this fails loudly in `ginkgo ./atc/wrappa/`, not in production. Reverting is a clean `git revert` of the Task 6 commit; nothing else depends on the routes until the sidecar ships.
- *Migrations (Task 2)*: both have exact down migrations; `agent_run_questions` has no FKs into core tables (plain join keys per the contracts conventions), so down-migrating drops nothing shared.
- *`atccmd` component wiring (Task 8)* is gated behind `--agent-notify-webhook-url`; with the flag unset the component never registers — deploys are unaffected until opted in.
- *`atc/api/mcpserver` SSE upgrade (Task 9b)* is the one NON-net-new diff in the sidecar range: the 3-arg `ToolHandler` is a breaking in-package signature change (all registrations updated mechanically; `atc/api/handler.go` compiles unchanged). Reverting it alone re-breaks Tasks 10–15 and re-opens F13 — revert the whole Task 9b–15 range together or not at all. Buffered behavior is bit-identical for clients that don't opt in, so ATC's own `/api/v1/mcp` consumers are unaffected either way.
- *Sidecar/binary/image (Tasks 9–16)* are otherwise net-new packages consumed by nothing in-tree; reverting them cannot break CI pipelines.
- *Elm (Task 17)*: the banner renders `text ""` with no open questions; if the page integration misbehaves, reverting the page-module hunk while keeping the new modules is safe (they are pure).
- *Known duplicate-row edge*: a checkpoint client re-POSTing after a client-container restart files a second question row unless the sidecar's per-name dedupe (Task 14) holds; if a stray open checkpoint row appears, answer it via the UI — it is join-key-only data, safe to resolve by hand in `agent_run_questions`. *[Superseded 2026-07-10 by Task 5b's DB-enforced dedup: the re-POST joins the same row AT THE DATABASE, across sidecar restarts and continuation pods alike; the by-hand note remains only for deployments predating migration `1773106072`.]*
- *PARK-V2 legs (Tasks 5b, 6b, 9c, 11b, 14c, 17b)*: all runtime behavior is inert with `PLATFORM_MCP_SHORT_PARK_MAX_SECONDS` unset/`0` — no sentinel, no 202, no exit 3, no `awaiting_human` entry. The find-or-create dedup and the answer-route notify stay active regardless, and are wanted regardless (byte-identical re-asks joining is correct in PARK-V1 too; the notify is a no-op without the wave-4 dispatcher). The escape hatch is the FLAG, not a revert: if plan 07's gating §I empirical pin (`TestLiveClaudeParkExitResume`) goes red, ship with `--agent-short-park-max=0` — pure PARK-V1, zero schema waste.
