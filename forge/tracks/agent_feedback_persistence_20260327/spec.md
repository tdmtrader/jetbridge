# Spec: agent feedback persistence

**Track ID:** `agent_feedback_persistence_20260327`
**Type:** bugfix

## Overview

The agent feedback system has two critical issues preventing it from functioning:

1. **Frontend/backend endpoint mismatch:** The Elm frontend calls `GET /api/v1/agent/reviews/{commit}/findings` (in `web/elm/src/AgentFeedback/Api.elm:29` and `web/elm/src/AgentFeedback/Main.elm:134`) but no backend route exists for this endpoint. The backend only registers four routes at `atc/routes.go:243-246`: `/api/v1/agent/feedback` (GET/POST), `/api/v1/agent/feedback/summary` (GET), and `/api/v1/agent/feedback/classify` (POST). This means the feedback UI fails on every page load with "Failed to load findings."

2. **Volatile in-memory storage:** The feedback store is `MemoryStore` (in-memory `[]*StoredFeedback` slice at `agent/api/feedback/handler.go:280-331`), instantiated at `atc/api/handler.go:117` as `feedback.NewHandler(feedback.NewMemoryStore())`. All feedback data is lost on every ATC restart, making the feedback loop useless for improving agent quality.

## Requirements

1. The frontend must be able to load findings for a given commit via the existing `fetchFindings` endpoint pattern (`/api/v1/agent/reviews/:commit/findings`).
2. A corresponding backend route and handler must serve findings data matching the Elm `Finding` type (id, finding_type, severity, category, title, file, line, description, test_code).
3. All feedback records must be persisted to PostgreSQL and survive ATC restarts.
4. The `Store` interface (`Save`, `GetByReview`, `GetAll`) must be implemented against PostgreSQL using the project's standard `DbConn` + Squirrel query builder pattern (following `atc/db/access_token_factory.go`).
5. `MemoryStore` must be retained for unit tests.

## Technical Approach

- **Phase 1 (API fix):** Add `GetAgentReviewFindings` route to `atc/routes.go`. Add `Finding` type and `GetFindings` handler to `agent/api/feedback/handler.go`. Wire in `atc/api/handler.go`. The handler extracts `:commit` from URL, queries the store, and returns findings.

- **Phase 2 (DB migration):** Create migration `1773105502_create_agent_feedback.{up,down}.sql`. Table columns: `id SERIAL PRIMARY KEY`, `repo TEXT NOT NULL`, `commit_sha TEXT NOT NULL`, `finding_id TEXT NOT NULL`, `finding_type TEXT`, `finding_snapshot JSONB`, `verdict TEXT NOT NULL`, `confidence DOUBLE PRECISION`, `notes TEXT`, `reviewer TEXT NOT NULL`, `source TEXT`, `created_at TIMESTAMPTZ`, `updated_at TIMESTAMPTZ`. Unique constraint on `(repo, commit_sha, finding_id, reviewer)` for upsert semantics.

- **Phase 3 (PostgresStore):** New file `atc/db/agent_feedback_factory.go` implementing `feedback.Store`. Uses `psql` Squirrel builder and `DbConn`. Wire into `atc/api/handler.go` replacing `NewMemoryStore()`. Pass `DbConn` from `atc/atccmd/command.go`.

## Key Files

**Backend routes and handlers:**
- `atc/routes.go` (lines 243-246) -- existing feedback routes, add findings route
- `atc/api/handler.go` (line 117) -- `MemoryStore` instantiation to replace
- `agent/api/feedback/handler.go` -- handler + Store interface + MemoryStore

**Frontend (read-only, no changes needed):**
- `web/elm/src/AgentFeedback/Api.elm` (line 29) -- `fetchFindings` calls `/api/v1/agent/reviews/{commit}/findings`
- `web/elm/src/AgentFeedback/Main.elm` (line 134) -- duplicate `fetchFindings`
- `web/elm/src/AgentFeedback/Types.elm` -- `Finding` type definition (source of truth for JSON shape)

**DB patterns to follow:**
- `atc/db/access_token_factory.go` -- factory pattern reference
- `atc/db/access_token.go` -- value type reference
- `atc/db/open.go` (lines 22-48) -- `DbConn` interface
- `atc/db/migration/migrations/` -- migration location

**Wiring:**
- `atc/atccmd/command.go` (~line 1285) -- where DB conn is available and handlers are constructed

## Acceptance Criteria

- [ ] `GET /api/v1/agent/reviews/:commit/findings` returns a JSON array matching the Elm `Finding` type
- [ ] Frontend loads findings without error on page load
- [ ] `POST /api/v1/agent/feedback` persists records to PostgreSQL `agent_feedback` table
- [ ] `GET /api/v1/agent/feedback?repo=X&commit=Y` returns data that survives ATC restart
- [ ] `GET /api/v1/agent/feedback/summary` aggregates from the DB table
- [ ] Upsert works: same (repo, commit, finding_id, reviewer) updates existing record
- [ ] Existing feedback handler unit tests still pass (using `MemoryStore`)
- [ ] New DB integration test verifies save/get round-trip

## Out of Scope

- Changing the verdict classification logic (`defaultClassifyText` and patterns)
- Adding new verdict types beyond the existing 6
- Elm UI redesign beyond fixing the endpoint mismatch
- Authentication/authorization on feedback endpoints (follows existing ATC convention)
- Pagination on GET endpoints (can be added later if needed)
