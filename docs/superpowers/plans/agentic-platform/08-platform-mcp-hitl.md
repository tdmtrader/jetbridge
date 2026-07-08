# platform-mcp sidecar with ask_human, checkpoints, and notifications — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Ship the agent's mid-flight platform surface — `read_ticket`, `submit_spec`, `submit_plan`, `update_task_status`, `ask_human` — as the platform-mcp sidecar, with park/resume over a long-poll question API, checkpoint-gate execution on the same primitive, a polling-backed webhook notification channel plus ticket-page banner, contract tests, and a live theborg restart-while-parked proof.

**Architecture:** A new `agent_run_questions` table + factory + four ATC routes (ask / list / get-long-poll / answer) carry the HITL state; the sidecar (`agent/platformmcp`, binary `cmd/platform-mcp`, image `ghcr.io/tdmtrader/mcp-platform`) is an MCP streamable-HTTP server that translates the five tools into principal-authed calls against ticket-core's routes and the new question routes, parking by blocking the MCP call over a resilient long-poll. Notifications are delivered by a polling RunnableComponent (`agent_notifier`) that POSTs a generic webhook (§8.4) — never notify-only, per the fork's lossy-NOTIFY lesson.

**Tech Stack:** Go (main module), squirrel/psql + counterfeiter in `atc/db`, `atc/api/mcpserver` for the MCP protocol, plain-Go httptest tests in `agent/*`, Ginkgo in `atc/db`/`atc/api`/`atc/wrappa`, Elm 0.19 (`web/elm`) for the banner, plain-Go `//go:build live` tests against theborg for park/resume.

---

## Context

**Charter (workstreams.json `platform-mcp-hitl`, size L, wave 3).** Scope-in items and where they land in this plan:

| scope_in item | Tasks |
|---|---|
| platform-mcp sidecar server, packaged per dev-mcp's image convention, authenticating as a scoped agent principal bound to the ticket/run | 9, 10, 13, 16 |
| Schema-constrained submit_spec/submit_plan/update_task_status through ticket-core's mutation path; read_ticket; live task progress on the ticket page | 10 (tools write via ticket-core's routes; the ticket page's task list — landed in wave 2 — renders the rows they write) |
| ask_human: park via supervisor wait semantics, question+options on the ticket page, resume on answer, parked step counts as running, per-workflow timeout policy from rendered step config (env) | 2, 3, 4, 5, 6, 9, 11, 17 |
| OWNS checkpoint-gate execution via the same park/resume primitive | 14 |
| Concrete notification mechanism: ticket-page banner + one real channel, polling-backed | 7, 8, 17 |
| Contract tests for the platform-mcp interface | 15 |
| Live theborg test: restart-while-parked | 18 |

Scope-out (must NOT appear in this plan): push/publish/archive mechanics (harvest-step), `request_review`/`ask_agent` (gateway-mcp), rendering checkpoint declarations into pipelines (dispatch's renderer, wave 4).

**Assumed landed (waves 1–2, per `00-shared-contracts.md`):**
- **agent-identity:** `agent_principals` (§1.2), `cap1.` tokens, `auth.CheckAgentPrincipalHandler(handler, rejector, scope)` wrappa tier, the `CheckAgentAuthorizationHandler` main-team wrapper for team-less `/api/v1/agent/*` authorized routes (§4.2 closing paragraph, decision 21), scope vocabulary incl. `tickets:read`, `tickets:write`, `questions:answer` (§4.1).
- **ticket-core:** ticket tables (§1.7), `agent/api/tickets` types + `tickets.Store` with `Transition` single-writer (§2.1), routes `GetAgentTicket`/`SubmitAgentTicketSpec`/`SubmitAgentTicketPlan`/`UpdateAgentTicketTask` (§4.2), the Elm ticket page with live task list.
- **agent-step:** `agent/schema` extracted as a nested stdlib-only Go module with `replace` entries in the root and ci-agent `go.mod`s (conventions bullet 2), §5 event constants for the types agent-step emits, `agent_run_metrics` + ingest route, proven sidecar wiring/env contract (§8.1: `ATC_EXTERNAL_URL`, `AGENT_PRINCIPAL_TOKEN`, `AGENT_TICKET_ID`, `PLATFORM_MCP_URL=http://127.0.0.1:7781/mcp`, `PLATFORM_MCP_ASK_TIMEOUT_POLICY`/`_SECONDS`).
- **dev-mcp:** sidecar image packaging convention + the first CI image-build job as copyable template (§8.5).
- **pipeline-runs:** the parked-run contract (§1.5): a parked step keeps its build `started`, so a parked run counts as `running`. This plan relies on it by construction — `ask_human` blocks the step's tool call, nothing else needed.
- **workflow-store:** `hitl:` block in the definition YAML (§6) — consumed indirectly: the renderer (wave 4) turns it into the sidecar env vars this plan reads.

**Contract surfaces this plan PRODUCES** (00-shared-contracts.md sections): §1.9 `agent_run_questions`; §3.2 platform-mcp tool schemas + park/resume protocol; §4.2 rows `AskAgentQuestion`/`GetAgentQuestion`/`AnswerAgentQuestion`; §8.4 notification channel. **CONSUMES:** §1.7/§2.1/§4.2 (ticket routes + Store), §4.1 (principal scopes/handler), §5 (flight-recorder events), §8.1 (env contract), §8.5 (image packaging), §1.5 (parked-run contract).

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
  - §3.2: the checkpoint internal endpoint is `POST /checkpoint` on the sidecar's `MCP_LISTEN_ADDR` (same mux as `/mcp` and `/healthz`), body `{"name": "...", "description": "..."}`, blocking response `{"approved": bool, "answer": "...", "answered_by": "..."}`. Checkpoint client mode: `platform-mcp checkpoint --name <n> [--description <d>]`, exit 0 approved / exit 1 rejected-or-error. `on_reject: send_back` semantics live in dispatch's renderer (wave 4); at the step level both values fail the step on reject.
  - §8.1: new row — `PLATFORM_MCP_EVENTS_PATH` | platform | literal | NDJSON event-log path for the sidecar's flight-recorder events (`human.ask`, `human.answer`, `checkpoint.*`); unset = stdout (pod logs).
  - §3.2 timeout resolution detail: when the sidecar resolves a timed-out question it sends `answered_by: "platform-mcp"` (the per-run principal *name* is not in the §8.1 env contract; if dispatch later adds `AGENT_PRINCIPAL_NAME`, the sidecar prefers it).
  - platform-mcp packaging (§8.5 instantiation): source `agent/platformmcp` (main module), binary `cmd/platform-mcp`, image `ghcr.io/tdmtrader/mcp-platform` from `deploy/Dockerfile.platform-mcp`.
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

	newQuestion := func(ticketID int) *questions.Question {
		runID := 42
		return &questions.Question{
			TicketID:      ticketID,
			PipelineRunID: &runID,
			BuildID:       1001,
			StepName:      "implement",
			Kind:          questions.KindQuestion,
			Question:      "ship it?",
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
		Expect(got.Question).To(Equal("ship it?"))
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
		id2, err := factory.Ask(newQuestion(9004))
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
	if cfg.ListenAddr == "" {
		cfg.ListenAddr = ":7781"
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
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/concourse/concourse/agent/api/questions"
)

// ATCClient is the sidecar's principal-authed ATC API client. Its long-poll
// loop (AwaitAnswer) is the park half of the §3.2 park/resume protocol:
// transport errors are retried forever — an ATC/web-node restart while parked
// just means a few failed polls until the new node answers.
type ATCClient struct {
	baseURL  string
	token    string
	ticketID int
	http     *http.Client

	// PollWait is the server-side wait per long-poll request (default 30s).
	PollWait time.Duration
	// RetryInterval is the sleep after a failed poll (default 5s).
	RetryInterval time.Duration
}

func NewATCClient(baseURL, principalToken string, ticketID int) *ATCClient {
	return &ATCClient{
		baseURL:  baseURL,
		token:    principalToken,
		ticketID: ticketID,
		// No global timeout: long-polls legitimately hold the connection.
		// Individual requests carry contexts.
		http:          &http.Client{},
		PollWait:      30 * time.Second,
		RetryInterval: 5 * time.Second,
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
		return fmt.Errorf("%s %s: %d: %s", method, path, resp.StatusCode, bytes.TrimSpace(msg))
	}
	if out != nil {
		return json.NewDecoder(resp.Body).Decode(out)
	}
	return nil
}

// GetTicketRaw returns the GetAgentTicket response body verbatim. Per §3.2
// read_ticket returns {ticket, spec, tasks}; ticket-core's handler embeds
// spec+tasks (survey Task 1). If the response has no "ticket" key the caller
// wraps it (tolerated drift, recorded in the addendum).
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
// policy), or ctx is cancelled. Transport/5xx errors are retried indefinitely:
// parked runs must survive web-node restarts.
func (c *ATCClient) AwaitAnswer(ctx context.Context, questionID int, deadline *time.Time) (*questions.Question, bool, error) {
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
			select {
			case <-ctx.Done():
				return nil, false, ctx.Err()
			case <-time.After(c.RetryInterval):
			}
			continue
		}
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

### Task 10: MCP server assembly + read_ticket / submit_spec / submit_plan / update_task_status

**Files:**
- Create: `agent/platformmcp/server.go`
- Create: `agent/platformmcp/tools.go`
- Test: `agent/platformmcp/tools_test.go`

The MCP protocol layer is `atc/api/mcpserver` (verified: `NewServer()`, `AddTool(name, description, schema, handler)`, `ServeHTTP` at `atc/api/mcpserver/server.go:24/31/42`, `MustJSON` at `:179`). Tool input schemas below are byte-for-byte the §3.2 contract.

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
		fmt.Fprint(w, `{"ticket":{"id":42,"title":"fix flaky test","state":"running"},"spec":null,"tasks":[]}`)
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

func TestToolsListExposesExactlyFiveTools(t *testing.T) {
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
	want := []string{"read_ticket", "submit_spec", "submit_plan", "update_task_status", "ask_human"}
	if fmt.Sprint(names) != fmt.Sprint(want) {
		t.Fatalf("tools = %v, want %v", names, want)
	}
}

func TestReadTicket(t *testing.T) {
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
	}
	if err := json.Unmarshal(result, &out); err != nil {
		t.Fatal(err)
	}
	if out.Ticket.ID != 42 || out.Ticket.Title != "fix flaky test" {
		t.Fatalf("unexpected ticket: %+v", out)
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
		mcp:    mcpserver.NewServer(),
		mux:    http.NewServeMux(),
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

func (s *Server) ListenAndServe() error {
	return http.ListenAndServe(s.cfg.ListenAddr, s.mux)
}
```

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
		"Read this run's ticket: envelope, latest spec, and active plan tasks.",
		mcpserver.MustJSON(map[string]any{
			"type": "object", "properties": map[string]any{}, "additionalProperties": false,
		}),
		s.readTicket)

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

	s.mcp.AddTool("ask_human",
		"Ask the human a question; this call BLOCKS (parks the run) until answered.",
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

func (s *Server) readTicket(ctx context.Context, _ json.RawMessage) (any, error) {
	raw, err := s.client.GetTicketRaw(ctx)
	if err != nil {
		return nil, fmt.Errorf("reading ticket: %w", err)
	}
	var probe map[string]json.RawMessage
	if err := json.Unmarshal(raw, &probe); err != nil {
		return nil, fmt.Errorf("decoding ticket response: %w", err)
	}
	if _, ok := probe["ticket"]; ok {
		return json.RawMessage(raw), nil
	}
	// Bare-ticket response shape: wrap per the §3.2 result schema.
	return map[string]any{"ticket": json.RawMessage(raw), "spec": nil, "tasks": []any{}}, nil
}

func (s *Server) submitSpec(ctx context.Context, args json.RawMessage) (any, error) {
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

func (s *Server) submitPlan(ctx context.Context, args json.RawMessage) (any, error) {
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

func (s *Server) updateTaskStatus(ctx context.Context, args json.RawMessage) (any, error) {
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

  `askHuman` is implemented in Task 11 — for this task's compile, add a temporary body in `tools.go` returning `nil, fmt.Errorf("ask_human lands in Task 11")` and REPLACE it in Task 11 (the Task 11 test would fail against the temporary body, so nothing placeholder survives).

- [ ] Run to verify pass (the `ask_human` presence assertion in `TestToolsListExposesExactlyFiveTools` passes because the tool is registered; only Task 11 exercises its behavior):

```bash
go test ./agent/platformmcp/
```

- [ ] Commit:

```bash
git add agent/platformmcp/
git commit -m "feat(platform-mcp): MCP server with read_ticket/submit_spec/submit_plan/update_task_status" -m "Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
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
func (s *Server) askHuman(ctx context.Context, args json.RawMessage) (any, error) {
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

	answered, timedOut, err := s.awaitWithPolicy(ctx, created.ID, created.AskedAt, in.Default)
	if err != nil {
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

Checkpoint gates (§3.2, Task 1 addendum) reuse the ask_human primitive: the rendered pipeline (wave 4; wave-3 hand-written pipelines do the same by hand) inserts a step whose main container runs `platform-mcp checkpoint --name <n>` with the platform sidecar mounted; the client POSTs the sidecar's `/checkpoint`, which files a `kind=checkpoint` question (`options: ["approve","reject"]`, always `park`) and blocks until a human resolves it. Reject ⇒ exit 1 ⇒ step fails ⇒ run fails ⇒ ticket `needs_review` (dispatch's renderer owns `on_reject: send_back` refinements — out of scope here).

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
```

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
	"net/http"
	"os"
	"strings"
	"time"
)

// runCheckpoint is checkpoint-client mode: POST the sidecar's /checkpoint and
// block until approved/rejected. Exit 0 = approved, 1 = rejected or transport
// failure after retries, 2 = usage error. Transport errors before a response
// are retried (the sidecar may still be starting; §8.5 readiness ordering).
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
			fmt.Fprintf(os.Stderr, "checkpoint: sidecar returned %d\n", resp.StatusCode)
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
```

- [ ] Run to verify it fails:

```bash
go test ./agent/platformmcp/contracttest/
```

Expected failure: package does not exist.

- [ ] Write `agent/platformmcp/contracttest/stub_atc.go` — a real-handler-backed stub any packaging can point at (env `ATC_EXTERNAL_URL`):

```go
// Package contracttest validates any platform-mcp endpoint against the §3.2
// contract: five tools, exact schema names, park/resume behavior. Run it
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
		fmt.Fprintf(w, `{"ticket":{"id":%d,"title":"contract ticket","state":"running"},"spec":null,"tasks":[]}`, ticketID)
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

	// tools/list: exactly the five §3.2 tools.
	list := rpc("tools/list", nil)
	tools := list["result"].(map[string]any)["tools"].([]any)
	names := map[string]bool{}
	for _, tool := range tools {
		names[tool.(map[string]any)["name"].(string)] = true
	}
	for _, want := range []string{"read_ticket", "submit_spec", "submit_plan", "update_task_status", "ask_human"} {
		if !names[want] {
			t.Fatalf("tools/list missing %q (got %v)", want, names)
		}
	}
	if len(names) != 5 {
		t.Fatalf("expected exactly 5 tools, got %v", names)
	}

	// read_ticket returns a ticket object.
	text, isErr := callTool("read_ticket", map[string]any{})
	if isErr || !bytes.Contains([]byte(text), []byte(`"ticket"`)) {
		t.Fatalf("read_ticket: isErr=%v %s", isErr, text)
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
```

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

- [ ] Write `deploy/Dockerfile.platform-mcp` (per §8.5: static Go binary, serves on `MCP_LISTEN_ADDR`, `GET /healthz`, non-root; precedent: `deploy/Dockerfile.ci-agent`):

```dockerfile
# platform-mcp sidecar image (shared contracts §8.5)
# ghcr.io/tdmtrader/mcp-platform:<release-tag>
FROM golang:1.25-bookworm AS builder

WORKDIR /src
COPY . .
# agent/schema is a nested module resolved via the root go.mod replace.
RUN CGO_ENABLED=0 go build -o /platform-mcp ./cmd/platform-mcp

FROM gcr.io/distroless/static-debian12:nonroot

COPY --from=builder /platform-mcp /platform-mcp
ENV MCP_LISTEN_ADDR=:7781
EXPOSE 7781
ENTRYPOINT ["/platform-mcp"]
```

- [ ] Verify the image builds and serves (requires Docker; on this machine Colima is usually down — if `docker info` fails, run this step on theborg CI only and verify the local binary path instead with `go build ./cmd/platform-mcp/`):

```bash
docker build -f deploy/Dockerfile.platform-mcp -t mcp-platform:dev . \
  && docker run --rm -d --name mcp-platform-smoke \
       -e ATC_EXTERNAL_URL=http://127.0.0.1:1 \
       -e AGENT_PRINCIPAL_TOKEN=cap1.0.smoke \
       -e AGENT_TICKET_ID=1 \
       -p 7781:7781 mcp-platform:dev \
  && sleep 2 && curl -fsS http://127.0.0.1:7781/healthz \
  && docker rm -f mcp-platform-smoke
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

### Task 19: full verification sweep

**Files:** none new — runs the workstream's whole surface.

- [ ] Go side (PostgreSQL running; ~5 min):

```bash
go vet ./agent/... ./cmd/platform-mcp/ && \
go test ./agent/api/questions/ ./agent/notify/ ./agent/platformmcp/... ./cmd/platform-mcp/ && \
ginkgo ./atc/db/migration/ && ginkgo ./atc/db/ && \
ginkgo ./atc/wrappa/ && ginkgo ./atc/api/ && ginkgo ./atc/atccmd/ && ginkgo ./atc/auditor/
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
ginkgo ./atc/db/migration/ ./atc/db/        # needs PostgreSQL (pg_isready); never --race
ginkgo ./atc/wrappa/ ./atc/api/ ./atc/auditor/ ./atc/atccmd/
yarn test && yarn build-elm
make test-quick                             # final gate
```

**Live-test requirements (theborg):** `KUBECONFIG=~/.kube/config`, kube-context `theborg` (https://theborg.home:6443), a THROWAWAY namespace via `kubectl create ns` (never `cicd`/`concourse` — live workloads), no pod-security label. Colima/Docker is usually down on this machine, so testcontainers is not an option — theborg is the live target. The Task 18 test cleans up its pod via `cleanupPod` + namespace deletion. The Task 16 docker smoke also needs Docker; when unavailable locally, rely on the theborg CI image job (dev-mcp's template) to build/contract-test/push `ghcr.io/tdmtrader/mcp-platform`, then `fly -t cicd set-pipeline` on concourse.home per the template's instructions.

**Manual end-to-end sanity (post-merge, wave-3 hand-written pipeline):** add the platform sidecar to an `agent:` step (`sidecars: [platform]`, image `ghcr.io/tdmtrader/mcp-platform:<tag>`), set `--agent-notify-webhook-url` to an ntfy topic, have the agent call `ask_human`; verify the run shows `running` while parked (pipeline-runs contract §1.5), the webhook fires within ~10s, the banner renders on the ticket page, answering resumes the step, and a checkpoint step (`platform-mcp checkpoint --name plan-approval`) blocks/exits per the approve/reject buttons.

**Rollback notes for the risky diffs:**
- *Route registration (Task 6)* touches four exhaustive switches; a missed entry panics the ATC at wrap time — this fails loudly in `ginkgo ./atc/wrappa/`, not in production. Reverting is a clean `git revert` of the Task 6 commit; nothing else depends on the routes until the sidecar ships.
- *Migrations (Task 2)*: both have exact down migrations; `agent_run_questions` has no FKs into core tables (plain join keys per the contracts conventions), so down-migrating drops nothing shared.
- *`atccmd` component wiring (Task 8)* is gated behind `--agent-notify-webhook-url`; with the flag unset the component never registers — deploys are unaffected until opted in.
- *Sidecar/binary/image (Tasks 9–16)* are net-new packages consumed by nothing in-tree; reverting them cannot break CI pipelines.
- *Elm (Task 17)*: the banner renders `text ""` with no open questions; if the page integration misbehaves, reverting the page-module hunk while keeping the new modules is safe (they are pure).
- *Known duplicate-row edge*: a checkpoint client re-POSTing after a client-container restart files a second question row unless the sidecar's per-name dedupe (Task 14) holds; if a stray open checkpoint row appears, answer it via the UI — it is join-key-only data, safe to resolve by hand in `agent_run_questions`.
