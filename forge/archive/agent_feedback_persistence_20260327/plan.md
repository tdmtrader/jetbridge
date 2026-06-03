# Implementation Plan: agent feedback persistence

## Phase 1: Fix frontend/backend endpoint mismatch

- [x] Task 1.1: Add `GetAgentReviewFindings` route constant and route definition 817d6a510
  - File: `atc/routes.go`
  - Add route name constant `GetAgentReviewFindings = "GetAgentReviewFindings"` alongside existing agent feedback constants
  - Add route entry: `{Path: "/api/v1/agent/reviews/:commit/findings", Method: "GET", Name: GetAgentReviewFindings}`
  - Place adjacent to existing agent feedback routes (lines 243-246)

- [x] Task 1.2: Add `Finding` type and `GetFindings` handler to feedback package 817d6a510
  - File: `agent/api/feedback/handler.go`
  - Add `Finding` struct matching the Elm `Finding` type from `web/elm/src/AgentFeedback/Types.elm`:
    ```
    type Finding struct {
        ID          string `json:"id"`
        FindingType string `json:"finding_type"`
        Severity    string `json:"severity"`
        Category    string `json:"category"`
        Title       string `json:"title"`
        File        string `json:"file"`
        Line        int    `json:"line"`
        Description string `json:"description"`
        TestCode    string `json:"test_code"`
    }
    ```
  - Add `GetFindings(w http.ResponseWriter, r *http.Request)` handler method on `Handler`
  - Extract `:commit` param from the request URL (using `rata.Param(r, "commit")`)
  - Call `store.GetByReview("", commit)` to get all feedback records for that commit
  - For each `StoredFeedback`, unmarshal `FindingSnapshot` (JSONB) into a `Finding` struct
  - Deduplicate by `FindingID` (multiple feedback records may reference the same finding)
  - Return JSON array of `Finding` objects; return `[]` (not null) when no findings exist

- [x] Task 1.3: Wire the new handler in `atc/api/handler.go` 817d6a510
  - File: `atc/api/handler.go`
  - Add handler mapping in the `handlers` map (near lines 240-243):
    `atc.GetAgentReviewFindings: http.HandlerFunc(feedbackServer.GetFindings)`

- [x] Task 1.4: Write handler unit tests for `GetFindings` 817d6a510
  - File: `agent/api/feedback/handler_test.go` (new file)
  - Test: GET with a commit that has stored findings -- returns JSON array with correct Finding shape
  - Test: GET with unknown commit -- returns empty JSON array `[]`
  - Test: Multiple feedback records for same finding (different reviewers) -- returns deduplicated finding list
  - Test: Finding with missing optional fields (description, test_code) -- returns defaults (empty string)
  - Use `MemoryStore` pre-populated with test data, `httptest.NewRecorder()`

- [x] Task 1.5: Verify Elm frontend compatibility 817d6a510
  - Confirm rata route param `:commit` matches the Elm URL construction `"/api/v1/agent/reviews/" ++ commit ++ "/findings"` in `Api.elm:29` and `Main.elm:134`
  - Confirm JSON response shape matches `findingDecoder` in `Types.elm:144-158`: fields `id`, `finding_type`, `severity`, `category`, `title`, `file`, `line`, plus optional `description` and `test_code`
  - No Elm changes needed if backend returns the expected shape

---

## Phase 2: Create PostgreSQL migration for `agent_feedback` table

- [x] Task 2.1: Write the up migration 23ddaf227
  - File: `atc/db/migration/migrations/1773105502_create_agent_feedback.up.sql`
  - SQL:
    ```sql
    CREATE TABLE agent_feedback (
        id          SERIAL PRIMARY KEY,
        repo        TEXT NOT NULL,
        commit_sha  TEXT NOT NULL,
        finding_id  TEXT NOT NULL,
        finding_type TEXT NOT NULL DEFAULT '',
        finding_snapshot JSONB,
        verdict     TEXT NOT NULL,
        confidence  DOUBLE PRECISION NOT NULL DEFAULT 0,
        notes       TEXT NOT NULL DEFAULT '',
        reviewer    TEXT NOT NULL,
        source      TEXT NOT NULL DEFAULT '',
        created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
        updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
    );
    CREATE UNIQUE INDEX idx_agent_feedback_upsert
        ON agent_feedback(repo, commit_sha, finding_id, reviewer);
    CREATE INDEX idx_agent_feedback_commit
        ON agent_feedback(commit_sha);
    CREATE INDEX idx_agent_feedback_repo_commit
        ON agent_feedback(repo, commit_sha);
    ```

- [x] Task 2.2: Write the down migration 23ddaf227
  - File: `atc/db/migration/migrations/1773105502_create_agent_feedback.down.sql`
  - SQL: `DROP TABLE IF EXISTS agent_feedback;`

- [x] Task 2.3: Register migration in migrations.go 23ddaf227
  - File: `atc/db/migration/migrations/migrations.go`
  - Add the new migration entry following the existing pattern and ordering

---

## Phase 3: Implement PostgreSQL-backed feedback store

- [x] Task 3.1: Create `AgentFeedbackFactory` interface and implementation bd393d490
  - File: `atc/db/agent_feedback_factory.go` (new)
  - Add `//go:generate go run github.com/maxbrunsfeld/counterfeiter/v6 -generate` and `//counterfeiter:generate . AgentFeedbackFactory`
  - Interface `AgentFeedbackFactory` that satisfies `feedback.Store`:
    ```
    type AgentFeedbackFactory interface {
        Save(rec *feedback.StoredFeedback) error
        GetByReview(repo, commit string) ([]feedback.StoredFeedback, error)
        GetAll() ([]feedback.StoredFeedback, error)
    }
    ```
  - Struct `agentFeedbackFactory struct { conn DbConn }`
  - Constructor: `NewAgentFeedbackFactory(conn DbConn) AgentFeedbackFactory`
  - `Save()`: use `psql.Insert("agent_feedback").Columns(...)` with `Suffix("ON CONFLICT (repo, commit_sha, finding_id, reviewer) DO UPDATE SET verdict = EXCLUDED.verdict, confidence = EXCLUDED.confidence, notes = EXCLUDED.notes, finding_snapshot = EXCLUDED.finding_snapshot, source = EXCLUDED.source, updated_at = now()")` for upsert
  - `GetByReview(repo, commit)`: `psql.Select(...).From("agent_feedback").Where(sq.Eq{"repo": repo, "commit_sha": commit}).OrderBy("created_at ASC")`
  - Handle empty `repo` filter: if repo is empty, only filter by `commit_sha` (to support the findings endpoint which only has commit)
  - `GetAll()`: `psql.Select(...).From("agent_feedback").OrderBy("created_at DESC")`
  - Scan rows into `feedback.StoredFeedback` structs, mapping `commit_sha` column -> `ReviewRef.Commit`, `repo` -> `ReviewRef.Repo`

- [x] Task 3.2: Write DB integration tests for `AgentFeedbackFactory` 03dd40482
  - File: `atc/db/agent_feedback_factory_test.go` (new)
  - Requires PostgreSQL (same setup as other `atc/db` tests)
  - Test: `Save` then `GetByReview` round-trip -- verify all fields preserved including `FindingSnapshot` JSONB
  - Test: upsert -- save twice with same (repo, commit_sha, finding_id, reviewer) but different verdict -- second save updates, `GetByReview` returns 1 record with updated verdict
  - Test: `GetAll` returns all records ordered by `created_at DESC`
  - Test: `GetByReview` with no matches returns empty slice (not nil)
  - Test: `GetByReview` with empty repo filters only by commit_sha

- [x] Task 3.3: Wire `AgentFeedbackFactory` into ATC handler bd393d490
  - File: `atc/api/handler.go`
  - Add `agentFeedbackStore feedback.Store` parameter to `NewHandler()` function signature (after existing params)
  - Change line 117 from `feedbackServer := feedback.NewHandler(feedback.NewMemoryStore())` to `feedbackServer := feedback.NewHandler(agentFeedbackStore)`
  - File: `atc/atccmd/command.go`
  - Near the handler construction (~line 1285), create `agentFeedbackFactory := db.NewAgentFeedbackFactory(dbConn)`
  - Pass `agentFeedbackFactory` to `api.NewHandler(...)` call
  - Update all callers of `api.NewHandler()` (check tests that construct handlers)

- [x] Task 3.4: Update `feedback.Store` interface if needed bd393d490
  - File: `agent/api/feedback/handler.go`
  - Ensure `Store` interface methods match what `AgentFeedbackFactory` implements
  - If `GetByReview` needs to handle empty-repo filtering, document that behavior in the interface comment
  - Keep `MemoryStore` functional for handler unit tests -- update `GetByReview` to match new empty-repo behavior if changed
  - Verify `MemoryStore` still satisfies `Store` interface (compile check)

- [x] Task 3.5: Run full test suite to verify no regressions 03dd40482
  - Commands: `make test-unit` (includes atc tests), `make test-integration`
  - Verify handler tests pass with `MemoryStore`
  - Verify DB tests pass with real PostgreSQL
  - Verify no import cycles between `agent/api/feedback` and `atc/db`

---
