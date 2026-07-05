# Agent Review Presentation Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Surface ci-agent code review results in the Concourse product — a findings panel on the build page and a cross-run reviews page — with per-finding verdict capture, backed by a new `agent_reviews` table fed by a `ci-agent publish` subcommand.

**Architecture:** The review task publishes `review.json` to a new token-authenticated `POST /api/v1/agent/reviews` endpoint; ATC stores it in Postgres keyed by build (denormalized columns + full JSONB). The Elm web UI reads two new GET endpoints to render a build-page panel (with segmented verdict controls that POST to the existing feedback API) and a team-scoped reviews list page. Spec: `docs/superpowers/specs/2026-07-05-agent-review-presentation-design.md`.

**Tech Stack:** Go (ATC + ci-agent module), PostgreSQL (squirrel query builder, embedded SQL migrations), Elm 0.19 (Concourse web SPA, elm-test), Ginkgo for Go tests in `atc/`, plain `go test` in `ci-agent/`.

**Verification commands used throughout:**
- ATC unit tests for a package: `ginkgo ./atc/db/` (needs local Postgres, `pg_isready` to check)
- ci-agent tests: `cd ci-agent && go test ./<pkg>/ -count=1`
- Elm build: `cd web/elm && elm make src/Main.elm --output=/dev/null`
- Elm tests: `cd web/elm && elm-test tests/<File>.elm`

---

### Task 1: `agent_reviews` migration

**Files:**
- Create: `atc/db/migration/migrations/1773105504_create_agent_reviews.up.sql`
- Create: `atc/db/migration/migrations/1773105504_create_agent_reviews.down.sql`

Migrations are `.sql` files auto-discovered via `go:embed migrations` in `atc/db/migration/migration.go:153` — no registration needed. Model on `1773105502_create_agent_feedback.up.sql`.

- [ ] **Step 1: Write the up migration**

`atc/db/migration/migrations/1773105504_create_agent_reviews.up.sql`:

```sql
CREATE TABLE agent_reviews (
    id                SERIAL PRIMARY KEY,
    build_id          INTEGER NOT NULL,
    build_name        TEXT NOT NULL DEFAULT '',
    team_name         TEXT NOT NULL,
    pipeline_name     TEXT NOT NULL DEFAULT '',
    job_name          TEXT NOT NULL DEFAULT '',
    repo              TEXT NOT NULL,
    commit_sha        TEXT NOT NULL,
    branch            TEXT NOT NULL DEFAULT '',
    score             DOUBLE PRECISION NOT NULL DEFAULT 0,
    max_score         DOUBLE PRECISION NOT NULL DEFAULT 10,
    pass              BOOLEAN NOT NULL DEFAULT false,
    proven_count      INTEGER NOT NULL DEFAULT 0,
    observation_count INTEGER NOT NULL DEFAULT 0,
    summary           TEXT NOT NULL DEFAULT '',
    agent_model       TEXT NOT NULL DEFAULT '',
    duration_seconds  INTEGER NOT NULL DEFAULT 0,
    review            JSONB NOT NULL,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at        TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX idx_agent_reviews_upsert
    ON agent_reviews(build_id, repo, commit_sha);

CREATE INDEX idx_agent_reviews_team_created
    ON agent_reviews(team_name, created_at DESC);

CREATE INDEX idx_agent_reviews_repo_commit
    ON agent_reviews(repo, commit_sha);
```

- [ ] **Step 2: Write the down migration**

`atc/db/migration/migrations/1773105504_create_agent_reviews.down.sql`:

```sql
DROP TABLE agent_reviews;
```

- [ ] **Step 3: Verify migrations apply cleanly**

Run: `ginkgo ./atc/db/migration/`
Expected: PASS (the suite exercises up/down application of embedded migrations)

- [ ] **Step 4: Commit**

```bash
git add atc/db/migration/migrations/1773105504_create_agent_reviews.up.sql atc/db/migration/migrations/1773105504_create_agent_reviews.down.sql
git commit -m "feat(db): add agent_reviews table"
```

---

### Task 2: `agent/api/reviews` package — types, Store interface, MemoryStore

**Files:**
- Create: `agent/api/reviews/types.go`
- Create: `agent/api/reviews/memory_store.go`
- Test: `agent/api/reviews/types_test.go`

Mirror the sibling package `agent/api/feedback` (plain `go test`, stdlib `testing` — check `agent/api/feedback` for style; it is part of the main module so `go test ./agent/...` works from repo root).

- [ ] **Step 1: Write failing tests for payload parsing/validation**

`agent/api/reviews/types_test.go`:

```go
package reviews_test

import (
	"encoding/json"
	"testing"

	"github.com/concourse/concourse/agent/api/reviews"
)

const validReview = `{
	"schema_version": "1.0.0",
	"metadata": {"repo": "concourse", "commit": "abc123", "branch": "jetbridge", "agent_model": "claude-sonnet-5", "duration_seconds": 120},
	"score": {"value": 7.5, "max": 10, "pass": true, "threshold": 7.0},
	"proven_issues": [{"id": "PI-1", "severity": "high", "title": "nil deref", "file": "a.go", "line": 10, "category": "correctness", "test_file": "a_test.go", "test_name": "TestNil"}],
	"observations": [{"id": "OB-1", "title": "long func", "file": "b.go", "line": 5, "category": "maintainability"}],
	"summary": "one bug"
}`

func TestParseSubmission(t *testing.T) {
	body := `{"build_id": 42, "review": ` + validReview + `}`
	sub, err := reviews.ParseSubmission([]byte(body))
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if sub.BuildID != 42 {
		t.Errorf("build id = %d, want 42", sub.BuildID)
	}
	if sub.Payload.Metadata.Repo != "concourse" {
		t.Errorf("repo = %q", sub.Payload.Metadata.Repo)
	}
	if len(sub.Payload.ProvenIssues) != 1 || len(sub.Payload.Observations) != 1 {
		t.Errorf("issue counts wrong: %d/%d", len(sub.Payload.ProvenIssues), len(sub.Payload.Observations))
	}
}

func TestParseSubmissionRejectsMissingFields(t *testing.T) {
	cases := map[string]string{
		"no build_id":  `{"review": ` + validReview + `}`,
		"no review":    `{"build_id": 42}`,
		"no repo":      `{"build_id": 42, "review": {"schema_version":"1.0.0","metadata":{"commit":"abc"},"score":{"value":1},"summary":"x"}}`,
		"no commit":    `{"build_id": 42, "review": {"schema_version":"1.0.0","metadata":{"repo":"r"},"score":{"value":1},"summary":"x"}}`,
		"bad json":     `{`,
	}
	for name, body := range cases {
		if _, err := reviews.ParseSubmission([]byte(body)); err == nil {
			t.Errorf("%s: expected error, got nil", name)
		}
	}
}

func TestStoredReviewFromSubmission(t *testing.T) {
	body := `{"build_id": 42, "review": ` + validReview + `}`
	sub, err := reviews.ParseSubmission([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	rec := sub.ToStoredReview(reviews.BuildContext{
		BuildName: "3", TeamName: "main", PipelineName: "concourse-self", JobName: "agent-review",
	})
	if rec.BuildID != 42 || rec.TeamName != "main" || rec.JobName != "agent-review" {
		t.Errorf("build context not applied: %+v", rec)
	}
	if rec.Score != 7.5 || !rec.Pass || rec.ProvenCount != 1 || rec.ObservationCount != 1 {
		t.Errorf("denormalized fields wrong: %+v", rec)
	}
	if !json.Valid(rec.Review) {
		t.Error("raw review payload not preserved")
	}
}

func TestMemoryStoreUpsert(t *testing.T) {
	store := reviews.NewMemoryStore()
	rec := &reviews.StoredReview{BuildID: 1, Repo: "r", CommitSha: "c", Score: 5, Review: json.RawMessage(`{}`)}
	if err := store.Upsert(rec); err != nil {
		t.Fatal(err)
	}
	rec2 := &reviews.StoredReview{BuildID: 1, Repo: "r", CommitSha: "c", Score: 9, Review: json.RawMessage(`{}`)}
	if err := store.Upsert(rec2); err != nil {
		t.Fatal(err)
	}
	got, err := store.GetByBuild(1)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Score != 9 {
		t.Errorf("upsert did not replace: %+v", got)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./agent/api/reviews/ -count=1`
Expected: FAIL — package does not exist / undefined symbols

- [ ] **Step 3: Implement types.go**

`agent/api/reviews/types.go`:

```go
package reviews

import (
	"encoding/json"
	"fmt"
)

// ReviewPayload is the subset of ci-agent's ReviewOutput that ATC
// needs for denormalized storage. The full raw payload is stored as-is.
type ReviewPayload struct {
	SchemaVersion string `json:"schema_version"`
	Metadata      struct {
		Repo        string `json:"repo"`
		Commit      string `json:"commit"`
		Branch      string `json:"branch"`
		AgentModel  string `json:"agent_model"`
		DurationSec int    `json:"duration_seconds"`
	} `json:"metadata"`
	Score struct {
		Value float64 `json:"value"`
		Max   float64 `json:"max"`
		Pass  bool    `json:"pass"`
	} `json:"score"`
	ProvenIssues []json.RawMessage `json:"proven_issues"`
	Observations []json.RawMessage `json:"observations"`
	Summary      string            `json:"summary"`
}

// Submission is a parsed POST /api/v1/agent/reviews body.
type Submission struct {
	BuildID int             `json:"build_id"`
	Review  json.RawMessage `json:"review"`
	Payload ReviewPayload   `json:"-"`
}

func ParseSubmission(body []byte) (*Submission, error) {
	var sub Submission
	if err := json.Unmarshal(body, &sub); err != nil {
		return nil, fmt.Errorf("invalid JSON: %w", err)
	}
	if sub.BuildID == 0 {
		return nil, fmt.Errorf("build_id is required")
	}
	if len(sub.Review) == 0 {
		return nil, fmt.Errorf("review is required")
	}
	if err := json.Unmarshal(sub.Review, &sub.Payload); err != nil {
		return nil, fmt.Errorf("invalid review payload: %w", err)
	}
	if sub.Payload.Metadata.Repo == "" {
		return nil, fmt.Errorf("review.metadata.repo is required")
	}
	if sub.Payload.Metadata.Commit == "" {
		return nil, fmt.Errorf("review.metadata.commit is required")
	}
	return &sub, nil
}

// BuildContext is what ATC derives server-side from the build row —
// never trusted from the client.
type BuildContext struct {
	BuildName    string
	TeamName     string
	PipelineName string
	JobName      string
}

// StoredReview is the persisted form of a review.
type StoredReview struct {
	BuildID          int             `json:"build_id"`
	BuildName        string          `json:"build_name"`
	TeamName         string          `json:"team_name"`
	PipelineName     string          `json:"pipeline_name"`
	JobName          string          `json:"job_name"`
	Repo             string          `json:"repo"`
	CommitSha        string          `json:"commit_sha"`
	Branch           string          `json:"branch"`
	Score            float64         `json:"score"`
	MaxScore         float64         `json:"max_score"`
	Pass             bool            `json:"pass"`
	ProvenCount      int             `json:"proven_count"`
	ObservationCount int             `json:"observation_count"`
	Summary          string          `json:"summary"`
	AgentModel       string          `json:"agent_model"`
	DurationSeconds  int             `json:"duration_seconds"`
	Review           json.RawMessage `json:"review,omitempty"`
	CreatedAt        int64           `json:"created_at"`
	EvaluatedCount   int             `json:"evaluated_count"`
}

func (s *Submission) ToStoredReview(ctx BuildContext) *StoredReview {
	return &StoredReview{
		BuildID:          s.BuildID,
		BuildName:        ctx.BuildName,
		TeamName:         ctx.TeamName,
		PipelineName:     ctx.PipelineName,
		JobName:          ctx.JobName,
		Repo:             s.Payload.Metadata.Repo,
		CommitSha:        s.Payload.Metadata.Commit,
		Branch:           s.Payload.Metadata.Branch,
		Score:            s.Payload.Score.Value,
		MaxScore:         s.Payload.Score.Max,
		Pass:             s.Payload.Score.Pass,
		ProvenCount:      len(s.Payload.ProvenIssues),
		ObservationCount: len(s.Payload.Observations),
		Summary:          s.Payload.Summary,
		AgentModel:       s.Payload.Metadata.AgentModel,
		DurationSeconds:  s.Payload.Metadata.DurationSec,
		Review:           s.Review,
	}
}

// ListFilter narrows ListByTeam results.
type ListFilter struct {
	Pipeline string
	Repo     string
	Limit    int
}

// Store is the interface for review persistence.
type Store interface {
	Upsert(rec *StoredReview) error
	GetByBuild(buildID int) ([]StoredReview, error)
	ListByTeam(team string, filter ListFilter) ([]StoredReview, error)
}
```

- [ ] **Step 4: Implement memory_store.go**

`agent/api/reviews/memory_store.go`:

```go
package reviews

import "sync"

// MemoryStore is an in-memory Store for testing.
type MemoryStore struct {
	mu      sync.Mutex
	records []*StoredReview
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{}
}

func (m *MemoryStore) Upsert(rec *StoredReview) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i, existing := range m.records {
		if existing.BuildID == rec.BuildID && existing.Repo == rec.Repo && existing.CommitSha == rec.CommitSha {
			m.records[i] = rec
			return nil
		}
	}
	m.records = append(m.records, rec)
	return nil
}

func (m *MemoryStore) GetByBuild(buildID int) ([]StoredReview, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	results := []StoredReview{}
	for _, rec := range m.records {
		if rec.BuildID == buildID {
			results = append(results, *rec)
		}
	}
	return results, nil
}

func (m *MemoryStore) ListByTeam(team string, filter ListFilter) ([]StoredReview, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	results := []StoredReview{}
	for _, rec := range m.records {
		if rec.TeamName != team {
			continue
		}
		if filter.Pipeline != "" && rec.PipelineName != filter.Pipeline {
			continue
		}
		if filter.Repo != "" && rec.Repo != filter.Repo {
			continue
		}
		results = append(results, *rec)
		if filter.Limit > 0 && len(results) >= filter.Limit {
			break
		}
	}
	return results, nil
}
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./agent/api/reviews/ -count=1`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add agent/api/reviews/
git commit -m "feat(agent): reviews package types, parsing, memory store"
```

---

### Task 3: `agent/api/reviews` HTTP handler

**Files:**
- Create: `agent/api/reviews/handler.go`
- Test: `agent/api/reviews/handler_test.go`

The handler enforces the static publish token on POST itself (the route is registered in the wrappa's "unauthenticated / delegating to handler" category — Task 5). GETs rely on wrappa auth. The build-detail GET joins finding-level feedback from the existing `feedback.Store`.

- [ ] **Step 1: Write failing handler tests**

`agent/api/reviews/handler_test.go`:

```go
package reviews_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/concourse/concourse/agent/api/feedback"
	"github.com/concourse/concourse/agent/api/reviews"
)

func newHandler(t *testing.T) (*reviews.Handler, *reviews.MemoryStore, *feedback.MemoryStore) {
	store := reviews.NewMemoryStore()
	fbStore := feedback.NewMemoryStore()
	lookup := func(id int) (reviews.BuildContext, bool, error) {
		if id == 42 {
			return reviews.BuildContext{BuildName: "3", TeamName: "main", PipelineName: "concourse-self", JobName: "agent-review"}, true, nil
		}
		return reviews.BuildContext{}, false, nil
	}
	return reviews.NewHandler(store, fbStore, lookup, "secret-token"), store, fbStore
}

func postBody() string {
	return `{"build_id": 42, "review": ` + validReview + `}`
}

func TestSubmitRequiresToken(t *testing.T) {
	h, _, _ := newHandler(t)
	for name, header := range map[string]string{"missing": "", "wrong": "Bearer nope"} {
		req := httptest.NewRequest("POST", "/api/v1/agent/reviews", strings.NewReader(postBody()))
		if header != "" {
			req.Header.Set("Authorization", header)
		}
		rec := httptest.NewRecorder()
		h.SubmitReview(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%s token: code = %d, want 401", name, rec.Code)
		}
	}
}

func TestSubmitRejectedWhenNoTokenConfigured(t *testing.T) {
	h := reviews.NewHandler(reviews.NewMemoryStore(), feedback.NewMemoryStore(),
		func(int) (reviews.BuildContext, bool, error) { return reviews.BuildContext{}, false, nil }, "")
	req := httptest.NewRequest("POST", "/api/v1/agent/reviews", strings.NewReader(postBody()))
	req.Header.Set("Authorization", "Bearer anything")
	rec := httptest.NewRecorder()
	h.SubmitReview(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Errorf("code = %d, want 403 when publishing is disabled", rec.Code)
	}
}

func TestSubmitUnknownBuild(t *testing.T) {
	h, _, _ := newHandler(t)
	req := httptest.NewRequest("POST", "/api/v1/agent/reviews", strings.NewReader(`{"build_id": 999, "review": `+validReview+`}`))
	req.Header.Set("Authorization", "Bearer secret-token")
	rec := httptest.NewRecorder()
	h.SubmitReview(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Errorf("code = %d, want 404", rec.Code)
	}
}

func TestSubmitMalformed(t *testing.T) {
	h, _, _ := newHandler(t)
	req := httptest.NewRequest("POST", "/api/v1/agent/reviews", strings.NewReader(`{"build_id": 42}`))
	req.Header.Set("Authorization", "Bearer secret-token")
	rec := httptest.NewRecorder()
	h.SubmitReview(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("code = %d, want 400", rec.Code)
	}
}

func TestSubmitAndGetByBuild(t *testing.T) {
	h, _, fbStore := newHandler(t)
	req := httptest.NewRequest("POST", "/api/v1/agent/reviews", strings.NewReader(postBody()))
	req.Header.Set("Authorization", "Bearer secret-token")
	rec := httptest.NewRecorder()
	h.SubmitReview(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("code = %d, want 201: %s", rec.Code, rec.Body.String())
	}

	// One finding already has feedback.
	fbStore.Save(&feedback.StoredFeedback{
		ReviewRef: feedback.ReviewRef{Repo: "concourse", Commit: "abc123"},
		FindingID: "PI-1", Verdict: "accurate", Reviewer: "tdm",
	})

	getReq := httptest.NewRequest("GET", "/api/v1/builds/42/agent-reviews", nil)
	getReq.Form = map[string][]string{":build_id": {"42"}}
	getRec := httptest.NewRecorder()
	h.GetByBuild(getRec, getReq)
	if getRec.Code != http.StatusOK {
		t.Fatalf("get code = %d", getRec.Code)
	}
	var got []reviews.BuildReviewResponse
	if err := json.Unmarshal(getRec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d reviews", len(got))
	}
	if got[0].TeamName != "main" || got[0].Score != 7.5 {
		t.Errorf("summary fields wrong: %+v", got[0])
	}
	if len(got[0].ProvenIssues) != 1 || len(got[0].Observations) != 1 {
		t.Errorf("findings not unpacked: %+v", got[0])
	}
	fb, ok := got[0].Feedback["PI-1"]
	if !ok || fb.Verdict != "accurate" || fb.Reviewer != "tdm" {
		t.Errorf("feedback join missing: %+v", got[0].Feedback)
	}
	if got[0].EvaluatedCount != 1 || got[0].FindingCount != 2 {
		t.Errorf("evaluated %d/%d, want 1/2", got[0].EvaluatedCount, got[0].FindingCount)
	}
}

func TestSubmitIsIdempotent(t *testing.T) {
	h, store, _ := newHandler(t)
	for i := 0; i < 2; i++ {
		req := httptest.NewRequest("POST", "/api/v1/agent/reviews", strings.NewReader(postBody()))
		req.Header.Set("Authorization", "Bearer secret-token")
		rec := httptest.NewRecorder()
		h.SubmitReview(rec, req)
		if rec.Code != http.StatusCreated {
			t.Fatalf("attempt %d: code = %d", i, rec.Code)
		}
	}
	got, _ := store.GetByBuild(42)
	if len(got) != 1 {
		t.Errorf("got %d rows, want 1 (upsert)", len(got))
	}
}

func TestListByTeam(t *testing.T) {
	h, store, _ := newHandler(t)
	store.Upsert(&reviews.StoredReview{BuildID: 1, TeamName: "main", PipelineName: "p1", Repo: "r", CommitSha: "c1", Review: json.RawMessage(`{}`)})
	store.Upsert(&reviews.StoredReview{BuildID: 2, TeamName: "main", PipelineName: "p2", Repo: "r", CommitSha: "c2", Review: json.RawMessage(`{}`)})
	store.Upsert(&reviews.StoredReview{BuildID: 3, TeamName: "other", PipelineName: "p1", Repo: "r", CommitSha: "c3", Review: json.RawMessage(`{}`)})

	req := httptest.NewRequest("GET", "/api/v1/teams/main/agent-reviews?pipeline=p1", nil)
	req.Form = map[string][]string{":team_name": {"main"}}
	rec := httptest.NewRecorder()
	h.ListByTeam(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d", rec.Code)
	}
	var got []reviews.StoredReview
	json.Unmarshal(rec.Body.Bytes(), &got)
	if len(got) != 1 || got[0].BuildID != 1 {
		t.Errorf("filter wrong: %+v", got)
	}
	if got[0].Review != nil {
		t.Error("listing must not include the JSONB payload")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./agent/api/reviews/ -count=1`
Expected: FAIL — `NewHandler`, `BuildReviewResponse` undefined

- [ ] **Step 3: Implement handler.go**

`agent/api/reviews/handler.go`:

```go
package reviews

import (
	"crypto/subtle"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/concourse/concourse/agent/api/feedback"
)

// BuildLookupFunc resolves a build ID to its context. found=false when
// the build does not exist.
type BuildLookupFunc func(buildID int) (BuildContext, bool, error)

// Handler serves the agent reviews API.
type Handler struct {
	store         Store
	feedbackStore feedback.Store
	lookupBuild   BuildLookupFunc
	publishToken  string
}

func NewHandler(store Store, feedbackStore feedback.Store, lookup BuildLookupFunc, publishToken string) *Handler {
	return &Handler{
		store:         store,
		feedbackStore: feedbackStore,
		lookupBuild:   lookup,
		publishToken:  publishToken,
	}
}

// Finding matches the shape stored in review JSON for proven issues
// and observations (superset; observations leave test fields empty).
type Finding struct {
	ID          string `json:"id"`
	Severity    string `json:"severity,omitempty"`
	Title       string `json:"title"`
	Description string `json:"description,omitempty"`
	File        string `json:"file"`
	Line        int    `json:"line"`
	Category    string `json:"category"`
	TestFile    string `json:"test_file,omitempty"`
	TestName    string `json:"test_name,omitempty"`
	TestOutput  string `json:"test_output,omitempty"`
}

// FindingFeedback is the recorded verdict for one finding.
type FindingFeedback struct {
	Verdict  string `json:"verdict"`
	Notes    string `json:"notes,omitempty"`
	Reviewer string `json:"reviewer"`
}

// BuildReviewResponse is the GET /api/v1/builds/:build_id/agent-reviews
// element: summary fields plus unpacked findings and feedback.
type BuildReviewResponse struct {
	StoredReview
	ProvenIssues   []Finding                  `json:"proven_issues"`
	Observations   []Finding                  `json:"observations"`
	Feedback       map[string]FindingFeedback `json:"feedback"`
	FindingCount   int                        `json:"finding_count"`
}

// SubmitReview handles POST /api/v1/agent/reviews.
func (h *Handler) SubmitReview(w http.ResponseWriter, r *http.Request) {
	if h.publishToken == "" {
		http.Error(w, "agent review publishing is not enabled", http.StatusForbidden)
		return
	}
	auth := r.Header.Get("Authorization")
	token, ok := strings.CutPrefix(auth, "Bearer ")
	if !ok || subtle.ConstantTimeCompare([]byte(token), []byte(h.publishToken)) != 1 {
		http.Error(w, "invalid publish token", http.StatusUnauthorized)
		return
	}

	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 4<<20))
	if err != nil {
		http.Error(w, "request too large", http.StatusBadRequest)
		return
	}
	sub, err := ParseSubmission(body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	buildCtx, found, err := h.lookupBuild(sub.BuildID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if !found {
		http.Error(w, "build not found", http.StatusNotFound)
		return
	}

	if err := h.store.Upsert(sub.ToStoredReview(buildCtx)); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(map[string]string{"status": "saved"})
}

// GetByBuild handles GET /api/v1/builds/:build_id/agent-reviews.
func (h *Handler) GetByBuild(w http.ResponseWriter, r *http.Request) {
	buildID, err := strconv.Atoi(r.FormValue(":build_id"))
	if err != nil {
		http.Error(w, "invalid build_id", http.StatusBadRequest)
		return
	}

	recs, err := h.store.GetByBuild(buildID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	responses := []BuildReviewResponse{}
	for _, rec := range recs {
		resp := BuildReviewResponse{StoredReview: rec, Feedback: map[string]FindingFeedback{}}

		var payload ReviewPayload
		if err := json.Unmarshal(rec.Review, &payload); err == nil {
			resp.ProvenIssues = decodeFindings(payload.ProvenIssues)
			resp.Observations = decodeFindings(payload.Observations)
		}
		if resp.ProvenIssues == nil {
			resp.ProvenIssues = []Finding{}
		}
		if resp.Observations == nil {
			resp.Observations = []Finding{}
		}
		resp.FindingCount = len(resp.ProvenIssues) + len(resp.Observations)

		fbs, err := h.feedbackStore.GetByReview(rec.Repo, rec.CommitSha)
		if err == nil {
			for _, fb := range fbs {
				resp.Feedback[fb.FindingID] = FindingFeedback{
					Verdict: fb.Verdict, Notes: fb.Notes, Reviewer: fb.Reviewer,
				}
			}
		}
		resp.EvaluatedCount = len(resp.Feedback)
		// The detail response carries findings explicitly; drop the raw payload.
		resp.Review = nil

		responses = append(responses, resp)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(responses)
}

func decodeFindings(raws []json.RawMessage) []Finding {
	findings := make([]Finding, 0, len(raws))
	for _, raw := range raws {
		var f Finding
		if err := json.Unmarshal(raw, &f); err == nil {
			findings = append(findings, f)
		}
	}
	return findings
}

// ListByTeam handles GET /api/v1/teams/:team_name/agent-reviews.
func (h *Handler) ListByTeam(w http.ResponseWriter, r *http.Request) {
	team := r.FormValue(":team_name")
	if team == "" {
		http.Error(w, "team_name is required", http.StatusBadRequest)
		return
	}

	limit := 100
	if l, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && l > 0 && l <= 500 {
		limit = l
	}

	recs, err := h.store.ListByTeam(team, ListFilter{
		Pipeline: r.URL.Query().Get("pipeline"),
		Repo:     r.URL.Query().Get("repo"),
		Limit:    limit,
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	for i := range recs {
		recs[i].Review = nil
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(recs)
}
```

Note: `StoredReview` already has an `EvaluatedCount` field (Task 2); the DB store fills it for listings, and `GetByBuild` overwrites it from the feedback join.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./agent/api/reviews/ -count=1`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add agent/api/reviews/handler.go agent/api/reviews/handler_test.go
git commit -m "feat(agent): reviews HTTP handler with token-authenticated ingestion"
```

---

### Task 4: `atc/db` agent reviews factory

**Files:**
- Create: `atc/db/agent_reviews_factory.go`
- Test: `atc/db/agent_reviews_factory_test.go`

Mirror `atc/db/agent_feedback_factory.go` exactly (squirrel `psql`, `DbConn`). The `atc/db` suite is Ginkgo — model the test file on `agent_feedback_factory_test.go` if it exists; otherwise on any small `*_factory_test.go` in the package (`Describe` + `BeforeEach` using the suite's shared `dbConn`).

- [ ] **Step 1: Write the failing Ginkgo test**

`atc/db/agent_reviews_factory_test.go`:

```go
package db_test

import (
	"encoding/json"

	"github.com/concourse/concourse/agent/api/reviews"
	"github.com/concourse/concourse/atc/db"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("AgentReviewsFactory", func() {
	var factory db.AgentReviewsFactory

	BeforeEach(func() {
		factory = db.NewAgentReviewsFactory(dbConn)
	})

	rec := func(buildID int, team, pipeline, repo, commit string, score float64) *reviews.StoredReview {
		return &reviews.StoredReview{
			BuildID: buildID, BuildName: "1", TeamName: team,
			PipelineName: pipeline, JobName: "agent-review",
			Repo: repo, CommitSha: commit, Branch: "jetbridge",
			Score: score, MaxScore: 10, Pass: score >= 7,
			ProvenCount: 1, ObservationCount: 2, Summary: "s",
			AgentModel: "claude-sonnet-5", DurationSeconds: 60,
			Review: json.RawMessage(`{"schema_version":"1.0.0"}`),
		}
	}

	It("round-trips a review by build", func() {
		Expect(factory.Upsert(rec(101, "main", "p", "r", "c1", 7.5))).To(Succeed())

		got, err := factory.GetByBuild(101)
		Expect(err).ToNot(HaveOccurred())
		Expect(got).To(HaveLen(1))
		Expect(got[0].TeamName).To(Equal("main"))
		Expect(got[0].Score).To(Equal(7.5))
		Expect(got[0].Review).To(MatchJSON(`{"schema_version":"1.0.0"}`))
	})

	It("upserts on (build_id, repo, commit_sha)", func() {
		Expect(factory.Upsert(rec(102, "main", "p", "r", "c2", 5.0))).To(Succeed())
		Expect(factory.Upsert(rec(102, "main", "p", "r", "c2", 9.0))).To(Succeed())

		got, err := factory.GetByBuild(102)
		Expect(err).ToNot(HaveOccurred())
		Expect(got).To(HaveLen(1))
		Expect(got[0].Score).To(Equal(9.0))
	})

	It("lists by team newest first with filters", func() {
		Expect(factory.Upsert(rec(103, "main", "pa", "r1", "c3", 8))).To(Succeed())
		Expect(factory.Upsert(rec(104, "main", "pb", "r2", "c4", 6))).To(Succeed())
		Expect(factory.Upsert(rec(105, "side", "pa", "r1", "c5", 7))).To(Succeed())

		got, err := factory.ListByTeam("main", reviews.ListFilter{Limit: 10})
		Expect(err).ToNot(HaveOccurred())
		Expect(got).To(HaveLen(2))
		Expect(got[0].BuildID).To(Equal(104)) // newest first

		filtered, err := factory.ListByTeam("main", reviews.ListFilter{Pipeline: "pa", Limit: 10})
		Expect(err).ToNot(HaveOccurred())
		Expect(filtered).To(HaveLen(1))
		Expect(filtered[0].BuildID).To(Equal(103))
	})

	It("counts evaluated findings from agent_feedback in listings", func() {
		Expect(factory.Upsert(rec(106, "main", "p", "fbrepo", "fbc", 8))).To(Succeed())

		fbFactory := db.NewAgentFeedbackFactory(dbConn)
		fbRec := feedbackRecord("fbrepo", "fbc", "PI-1", "accurate", "tdm")
		Expect(fbFactory.Save(&fbRec)).To(Succeed())

		got, err := factory.ListByTeam("main", reviews.ListFilter{Repo: "fbrepo", Limit: 10})
		Expect(err).ToNot(HaveOccurred())
		Expect(got).To(HaveLen(1))
		Expect(got[0].EvaluatedCount).To(Equal(1))
	})
})
```

Add the small helper at the bottom of the file:

```go
func feedbackRecord(repo, commit, findingID, verdict, reviewer string) feedback.StoredFeedback {
	return feedback.StoredFeedback{
		ReviewRef: feedback.ReviewRef{Repo: repo, Commit: commit},
		FindingID: findingID, Verdict: verdict, Reviewer: reviewer,
	}
}
```

(import `"github.com/concourse/concourse/agent/api/feedback"`.)

- [ ] **Step 2: Run to verify failure**

Run: `ginkgo --focus="AgentReviewsFactory" ./atc/db/`
Expected: compile FAIL — `db.NewAgentReviewsFactory` undefined

- [ ] **Step 3: Implement the factory**

`atc/db/agent_reviews_factory.go`:

```go
package db

import (
	"encoding/json"

	sq "github.com/Masterminds/squirrel"

	"github.com/concourse/concourse/agent/api/reviews"
)

//counterfeiter:generate . AgentReviewsFactory
type AgentReviewsFactory interface {
	reviews.Store
}

func NewAgentReviewsFactory(conn DbConn) AgentReviewsFactory {
	return &agentReviewsFactory{conn: conn}
}

type agentReviewsFactory struct {
	conn DbConn
}

func (f *agentReviewsFactory) Upsert(rec *reviews.StoredReview) error {
	_, err := psql.Insert("agent_reviews").
		Columns(
			"build_id", "build_name", "team_name", "pipeline_name", "job_name",
			"repo", "commit_sha", "branch",
			"score", "max_score", "pass", "proven_count", "observation_count",
			"summary", "agent_model", "duration_seconds", "review",
		).
		Values(
			rec.BuildID, rec.BuildName, rec.TeamName, rec.PipelineName, rec.JobName,
			rec.Repo, rec.CommitSha, rec.Branch,
			rec.Score, rec.MaxScore, rec.Pass, rec.ProvenCount, rec.ObservationCount,
			rec.Summary, rec.AgentModel, rec.DurationSeconds, []byte(rec.Review),
		).
		Suffix(`ON CONFLICT (build_id, repo, commit_sha) DO UPDATE SET
			build_name = EXCLUDED.build_name,
			team_name = EXCLUDED.team_name,
			pipeline_name = EXCLUDED.pipeline_name,
			job_name = EXCLUDED.job_name,
			branch = EXCLUDED.branch,
			score = EXCLUDED.score,
			max_score = EXCLUDED.max_score,
			pass = EXCLUDED.pass,
			proven_count = EXCLUDED.proven_count,
			observation_count = EXCLUDED.observation_count,
			summary = EXCLUDED.summary,
			agent_model = EXCLUDED.agent_model,
			duration_seconds = EXCLUDED.duration_seconds,
			review = EXCLUDED.review,
			updated_at = now()`).
		RunWith(f.conn).
		Exec()
	return err
}

const reviewColumns = `r.build_id, r.build_name, r.team_name, r.pipeline_name, r.job_name,
	r.repo, r.commit_sha, r.branch,
	r.score, r.max_score, r.pass, r.proven_count, r.observation_count,
	r.summary, r.agent_model, r.duration_seconds,
	EXTRACT(EPOCH FROM r.created_at)::bigint,
	(SELECT COUNT(DISTINCT fb.finding_id) FROM agent_feedback fb
	  WHERE fb.repo = r.repo AND fb.commit_sha = r.commit_sha)`

func (f *agentReviewsFactory) GetByBuild(buildID int) ([]reviews.StoredReview, error) {
	rows, err := f.conn.Query(
		`SELECT `+reviewColumns+`, r.review
		 FROM agent_reviews r WHERE r.build_id = $1 ORDER BY r.created_at ASC`,
		buildID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanReviewRows(rows, true)
}

func (f *agentReviewsFactory) ListByTeam(team string, filter reviews.ListFilter) ([]reviews.StoredReview, error) {
	query := `SELECT ` + reviewColumns + `
		 FROM agent_reviews r WHERE r.team_name = $1`
	args := []any{team}
	if filter.Pipeline != "" {
		args = append(args, filter.Pipeline)
		query += ` AND r.pipeline_name = $` + itoa(len(args))
	}
	if filter.Repo != "" {
		args = append(args, filter.Repo)
		query += ` AND r.repo = $` + itoa(len(args))
	}
	query += ` ORDER BY r.created_at DESC`
	if filter.Limit > 0 {
		args = append(args, filter.Limit)
		query += ` LIMIT $` + itoa(len(args))
	}

	rows, err := f.conn.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanReviewRows(rows, false)
}

func itoa(n int) string {
	return strconv.Itoa(n)
}
```

(imports: `strconv`, `encoding/json`; drop the squirrel import if `Upsert` is the only user — it is not, `psql` uses it. `GetByBuild` likewise ends with `defer rows.Close()`.) And the row scanner:

```go
func scanReviewRows(rows interface {
	Next() bool
	Scan(dest ...any) error
}, withPayload bool) ([]reviews.StoredReview, error) {
	results := []reviews.StoredReview{}
	for rows.Next() {
		var rec reviews.StoredReview
		var payload []byte
		dest := []any{
			&rec.BuildID, &rec.BuildName, &rec.TeamName, &rec.PipelineName, &rec.JobName,
			&rec.Repo, &rec.CommitSha, &rec.Branch,
			&rec.Score, &rec.MaxScore, &rec.Pass, &rec.ProvenCount, &rec.ObservationCount,
			&rec.Summary, &rec.AgentModel, &rec.DurationSeconds,
			&rec.CreatedAt, &rec.EvaluatedCount,
		}
		if withPayload {
			dest = append(dest, &payload)
		}
		if err := rows.Scan(dest...); err != nil {
			return nil, err
		}
		if withPayload {
			rec.Review = json.RawMessage(payload)
		}
		results = append(results, rec)
	}
	return results, nil
}
```

(If the package's `DbConn.Query` signature differs, follow how `agent_feedback_factory.go` runs squirrel queries and convert these to squirrel `psql.Select` with `sq.Expr` for the subquery — same semantics.)

- [ ] **Step 4: Run tests to verify they pass**

Run: `ginkgo --focus="AgentReviewsFactory" ./atc/db/`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add atc/db/agent_reviews_factory.go atc/db/agent_reviews_factory_test.go
git commit -m "feat(db): agent reviews factory with feedback-count join"
```

---

### Task 5: Wire routes, auth, handler, and web flag

**Files:**
- Modify: `atc/routes.go` (const block near line 121, routes list near line 250)
- Modify: `atc/wrappa/api_auth_wrappa.go` (route categories near lines 88 and 159)
- Modify: `atc/api/handler.go` (params near line 89, server construction near line 119, handlers map near line 249)
- Modify: `atc/atccmd/command.go` (flag in `RunCommand` struct near line 213; pass-through near line 2295)
- Test: `atc/api/handler_test.go` or the wrappa test (`atc/wrappa/api_auth_wrappa_test.go`) — follow whichever asserts route coverage

- [ ] **Step 1: Add route name constants and routes**

In `atc/routes.go`, const block (after `GetAgentReviewFindings`):

```go
SubmitAgentReview    = "SubmitAgentReview"
GetBuildAgentReviews = "GetBuildAgentReviews"
ListTeamAgentReviews = "ListTeamAgentReviews"
```

In the routes list (after the existing agent routes at line ~254):

```go
{Path: "/api/v1/agent/reviews", Method: "POST", Name: SubmitAgentReview},
{Path: "/api/v1/builds/:build_id/agent-reviews", Method: "GET", Name: GetBuildAgentReviews},
{Path: "/api/v1/teams/:team_name/agent-reviews", Method: "GET", Name: ListTeamAgentReviews},
```

- [ ] **Step 2: Categorize in the auth wrappa**

In `atc/wrappa/api_auth_wrappa.go`:
- Add `atc.SubmitAgentReview,` to the "unauthenticated / delegating to handler" case (the list containing `atc.DownloadCLI`, line ~88) — the handler enforces the publish token itself.
- Add `atc.GetBuildAgentReviews,` to the authenticated case (the list ending `newHandler = auth.CheckAuthenticationHandler(handler, rejector)`, line ~79-85).
- Add `atc.ListTeamAgentReviews,` to the authorized case (the list containing `atc.SubmitAgentFeedback`, line ~159).

The wrappa `default:` case panics on unhandled routes ("you missed a spot"), so a missing category fails the wrappa test immediately.

- [ ] **Step 3: Run wrappa tests to verify coverage**

Run: `ginkgo ./atc/wrappa/`
Expected: FAIL if any wrappa test enumerates expected handlers (add the three routes to its fixture per the failure message), then PASS after updating.

- [ ] **Step 4: Construct the handler in the API**

In `atc/api/handler.go`:

Add params to `NewHandler` (after `feedbackStore feedback.Store,` near line 89):

```go
reviewsStore reviewsapi.Store,
agentReviewPublishToken string,
```

with import `reviewsapi "github.com/concourse/concourse/agent/api/reviews"`.

After `feedbackServer := feedback.NewHandler(feedbackStore)` (line ~119):

```go
reviewsServer := reviewsapi.NewHandler(
	reviewsStore,
	feedbackStore,
	func(id int) (reviewsapi.BuildContext, bool, error) {
		build, found, err := dbBuildFactory.Build(id)
		if err != nil || !found {
			return reviewsapi.BuildContext{}, found, err
		}
		return reviewsapi.BuildContext{
			BuildName:    build.Name(),
			TeamName:     build.TeamName(),
			PipelineName: build.PipelineName(),
			JobName:      build.JobName(),
		}, true, nil
	},
	agentReviewPublishToken,
)
```

In the `handlers` map (after `atc.GetAgentReviewFindings`, line ~253):

```go
atc.SubmitAgentReview:    http.HandlerFunc(reviewsServer.SubmitReview),
atc.GetBuildAgentReviews: http.HandlerFunc(reviewsServer.GetByBuild),
atc.ListTeamAgentReviews: http.HandlerFunc(reviewsServer.ListByTeam),
```

(`dbBuildFactory.Build` returns a build with `Name()/TeamName()/PipelineName()/JobName()` accessors; check `atc/db/build_factory.go` for the exact return type if the compiler complains — one-off builds return empty pipeline/job names, which is fine.)

- [ ] **Step 5: Add the web flag and pass everything through**

In `atc/atccmd/command.go`, `RunCommand` struct (next to `ClusterName`, line ~213):

```go
AgentReviewPublishToken string `long:"agent-review-publish-token" description:"Static bearer token accepted for publishing agent review results via POST /api/v1/agent/reviews. Publishing is disabled when empty."`
```

At the `NewHandler`/`constructAPIHandler` call site (where `db.NewAgentFeedbackFactory(dbConn)` is passed, line ~2295), append:

```go
db.NewAgentReviewsFactory(dbConn),
cmd.AgentReviewPublishToken,
```

(Follow the existing argument threading: if `constructAPIHandler` has its own signature between `RunCommand` and `api.NewHandler`, add the two params there too.)

- [ ] **Step 6: Build and run the api package tests**

Run: `go build ./atc/... && ginkgo ./atc/api/ ./atc/wrappa/`
Expected: compiles; PASS (update any handler-count fixtures the failures point at)

- [ ] **Step 7: Commit**

```bash
git add atc/routes.go atc/wrappa/api_auth_wrappa.go atc/api/handler.go atc/atccmd/command.go
git commit -m "feat(atc): agent reviews API routes, auth, and publish-token flag"
```

---

### Task 6: Expose build identity to tasks (`TaskEnv`)

**Files:**
- Modify: `atc/exec/step_metadata.go:97-103`
- Test: `atc/exec/step_metadata_test.go` (extend existing)

- [ ] **Step 1: Write/extend the failing test**

In `atc/exec/step_metadata_test.go`, find the existing `TaskEnv` expectations (search `TaskEnv`) and update/add:

```go
It("returns build identity env for tasks", func() {
	Expect(exec.StepMetadata{
		BuildID:      42,
		BuildName:    "3",
		TeamName:     "main",
		JobName:      "agent-review",
		PipelineName: "concourse-self",
		ExternalURL:  "https://concourse.home",
	}.TaskEnv()).To(ConsistOf(
		"BUILD_ID=42",
		"BUILD_NAME=3",
		"BUILD_TEAM_NAME=main",
		"BUILD_JOB_NAME=agent-review",
		"BUILD_PIPELINE_NAME=concourse-self",
		"ATC_EXTERNAL_URL=https://concourse.home",
	))
})
```

- [ ] **Step 2: Run to verify failure**

Run: `ginkgo --focus="TaskEnv" ./atc/exec/`
Expected: FAIL — only `ATC_EXTERNAL_URL` returned

- [ ] **Step 3: Implement**

Replace `TaskEnv()` in `atc/exec/step_metadata.go`:

```go
// TaskEnv returns the env exposed to task containers. Unlike upstream
// Concourse, this fork exposes build identity so tasks (e.g. ci-agent
// publish) can report results back to the ATC keyed by build.
func (metadata StepMetadata) TaskEnv() []string {
	env := []string{}
	if metadata.BuildID != 0 {
		env = append(env, fmt.Sprintf("BUILD_ID=%d", metadata.BuildID))
	}
	if metadata.BuildName != "" {
		env = append(env, "BUILD_NAME="+metadata.BuildName)
	}
	if metadata.TeamName != "" {
		env = append(env, "BUILD_TEAM_NAME="+metadata.TeamName)
	}
	if metadata.JobName != "" {
		env = append(env, "BUILD_JOB_NAME="+metadata.JobName)
	}
	if metadata.PipelineName != "" {
		env = append(env, "BUILD_PIPELINE_NAME="+metadata.PipelineName)
	}
	if metadata.ExternalURL != "" {
		env = append(env, "ATC_EXTERNAL_URL="+metadata.ExternalURL)
	}
	return env
}
```

- [ ] **Step 4: Run the exec suite**

Run: `ginkgo ./atc/exec/`
Expected: PASS (note: `atc/exec/artifact_input_step_test.go` has a known pre-existing vet failure per project memory — a failure there is not caused by this change)

- [ ] **Step 5: Commit**

```bash
git add atc/exec/step_metadata.go atc/exec/step_metadata_test.go
git commit -m "feat(exec): expose build identity env vars to task steps"
```

---

### Task 7: `ci-agent publish` subcommand

**Files:**
- Create: `ci-agent/publish/publish.go`
- Create: `ci-agent/publish/publish_test.go`
- Modify: `ci-agent/cmd/ci-agent/main.go` (dispatch subcommand at top of `main()`)

- [ ] **Step 1: Write failing tests**

`ci-agent/publish/publish_test.go`:

```go
package publish_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/concourse/ci-agent/publish"
)

func writeReview(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "review.json")
	content := `{"schema_version":"1.0.0","metadata":{"repo":"r","commit":"c"},"score":{"value":8,"max":10,"pass":true},"proven_issues":[],"observations":[],"summary":"ok"}`
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestPublishSuccess(t *testing.T) {
	var gotAuth atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/agent/reviews" || r.Method != "POST" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		gotAuth.Store(r.Header.Get("Authorization"))
		w.WriteHeader(http.StatusCreated)
	}))
	defer srv.Close()

	err := publish.Publish(context.Background(), publish.Options{
		ATCURL:     srv.URL,
		BuildID:    "42",
		Token:      "tok",
		ReviewPath: writeReview(t),
	})
	if err != nil {
		t.Fatalf("expected success, got %v", err)
	}
	if gotAuth.Load() != "Bearer tok" {
		t.Errorf("auth header = %v", gotAuth.Load())
	}
}

func TestPublishRetriesOn5xx(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) < 3 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusCreated)
	}))
	defer srv.Close()

	err := publish.Publish(context.Background(), publish.Options{
		ATCURL: srv.URL, BuildID: "42", Token: "tok",
		ReviewPath: writeReview(t), RetryDelay: 0,
	})
	if err != nil {
		t.Fatalf("expected success after retries, got %v", err)
	}
	if calls.Load() != 3 {
		t.Errorf("calls = %d, want 3", calls.Load())
	}
}

func TestPublishGivesUpAfterRetries(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	err := publish.Publish(context.Background(), publish.Options{
		ATCURL: srv.URL, BuildID: "42", Token: "tok",
		ReviewPath: writeReview(t), RetryDelay: 0,
	})
	if err == nil {
		t.Fatal("expected error after exhausting retries")
	}
}

func TestPublishDoesNotRetry4xx(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	err := publish.Publish(context.Background(), publish.Options{
		ATCURL: srv.URL, BuildID: "42", Token: "tok",
		ReviewPath: writeReview(t), RetryDelay: 0,
	})
	if err == nil || calls.Load() != 1 {
		t.Fatalf("want 1 call and error, got calls=%d err=%v", calls.Load(), err)
	}
}

func TestPublishValidatesInputs(t *testing.T) {
	base := publish.Options{ATCURL: "http://x", BuildID: "42", Token: "t", ReviewPath: writeReview(t)}
	for name, mutate := range map[string]func(*publish.Options){
		"no url":     func(o *publish.Options) { o.ATCURL = "" },
		"no build":   func(o *publish.Options) { o.BuildID = "" },
		"no token":   func(o *publish.Options) { o.Token = "" },
		"bad review": func(o *publish.Options) { o.ReviewPath = "/nonexistent" },
	} {
		opts := base
		mutate(&opts)
		if err := publish.Publish(context.Background(), opts); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `cd ci-agent && go test ./publish/ -count=1`
Expected: FAIL — package does not exist

- [ ] **Step 3: Implement publish.go**

`ci-agent/publish/publish.go`:

```go
// Package publish uploads a review.json to the ATC's agent reviews API,
// keyed by the build that produced it.
package publish

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

type Options struct {
	ATCURL     string
	BuildID    string
	Token      string
	ReviewPath string
	HTTPClient *http.Client
	RetryDelay time.Duration
}

const maxAttempts = 3

func Publish(ctx context.Context, opts Options) error {
	if opts.ATCURL == "" {
		return fmt.Errorf("ATC_EXTERNAL_URL is not set")
	}
	if opts.BuildID == "" {
		return fmt.Errorf("BUILD_ID is not set")
	}
	if opts.Token == "" {
		return fmt.Errorf("AGENT_REVIEW_PUBLISH_TOKEN is not set")
	}

	buildID, err := strconv.Atoi(opts.BuildID)
	if err != nil {
		return fmt.Errorf("invalid BUILD_ID %q: %w", opts.BuildID, err)
	}

	review, err := os.ReadFile(opts.ReviewPath)
	if err != nil {
		return fmt.Errorf("reading review: %w", err)
	}
	if !json.Valid(review) {
		return fmt.Errorf("review file %s is not valid JSON", opts.ReviewPath)
	}

	body, err := json.Marshal(map[string]any{
		"build_id": buildID,
		"review":   json.RawMessage(review),
	})
	if err != nil {
		return fmt.Errorf("encoding submission: %w", err)
	}

	client := opts.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	delay := opts.RetryDelay
	if delay == 0 && opts.HTTPClient == nil {
		delay = 2 * time.Second
	}

	url := strings.TrimSuffix(opts.ATCURL, "/") + "/api/v1/agent/reviews"

	var lastErr error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(body))
		if err != nil {
			return err
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+opts.Token)

		resp, err := client.Do(req)
		if err != nil {
			lastErr = err
		} else {
			respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
			resp.Body.Close()
			if resp.StatusCode >= 200 && resp.StatusCode < 300 {
				return nil
			}
			lastErr = fmt.Errorf("publish failed: %s: %s", resp.Status, strings.TrimSpace(string(respBody)))
			if resp.StatusCode >= 400 && resp.StatusCode < 500 {
				return lastErr
			}
		}
		if attempt < maxAttempts {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(delay * time.Duration(attempt)):
			}
		}
	}
	return fmt.Errorf("after %d attempts: %w", maxAttempts, lastErr)
}
```

Note on `RetryDelay`: tests pass `RetryDelay: 0` with a custom-nil client — the `delay == 0 && opts.HTTPClient == nil` guard keeps production defaults at 2s while tests run instantly. (Tests use the default client but set `RetryDelay: 0` explicitly, so the guard leaves delay at 0 for them — correct and fast.)

- [ ] **Step 4: Wire the subcommand into main**

In `ci-agent/cmd/ci-agent/main.go`, at the very top of `main()` (before tracing init):

```go
if len(os.Args) > 1 && os.Args[1] == "publish" {
	os.Exit(runPublish(os.Args[2:]))
}
```

Create `ci-agent/cmd/ci-agent/publish.go`:

```go
package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"github.com/concourse/ci-agent/publish"
)

func runPublish(args []string) int {
	fs := flag.NewFlagSet("publish", flag.ContinueOnError)
	reviewPath := fs.String("review", "review/review.json", "path to review.json")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	err := publish.Publish(context.Background(), publish.Options{
		ATCURL:     os.Getenv("ATC_EXTERNAL_URL"),
		BuildID:    os.Getenv("BUILD_ID"),
		Token:      os.Getenv("AGENT_REVIEW_PUBLISH_TOKEN"),
		ReviewPath: *reviewPath,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "publish error: %v\n", err)
		return 1
	}
	fmt.Println("review published")
	return 0
}
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `cd ci-agent && go test ./publish/ ./cmd/... -count=1`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add ci-agent/publish/ ci-agent/cmd/ci-agent/publish.go ci-agent/cmd/ci-agent/main.go
git commit -m "feat(ci-agent): publish subcommand posts review.json to ATC"
```

---

### Task 8: Review task publishes after review

**Files:**
- Modify: `ci/tasks/ci-agent-review.yml`

Publish failure must not flip a passing review to red (spec: visible but not verdict-altering), so the script warns loudly instead of failing.

- [ ] **Step 1: Add the param and publish step**

In `ci/tasks/ci-agent-review.yml`, add to `params:`:

```yaml
  AGENT_REVIEW_PUBLISH_TOKEN: ""
```

Append to the end of the `run` script (after the `cat "$OUTPUT_DIR/review.json" | head -50` line):

```sh
    if [ -n "${AGENT_REVIEW_PUBLISH_TOKEN:-}" ]; then
      echo ""
      echo "=== Publishing review to ATC ==="
      if ci-agent publish --review "$OUTPUT_DIR/review.json"; then
        echo "Published for build ${BUILD_ID:-unknown}"
      else
        echo "WARNING: failed to publish review to ATC (results still available as artifact)"
      fi
    else
      echo "AGENT_REVIEW_PUBLISH_TOKEN not set; skipping publish"
    fi
```

- [ ] **Step 2: Validate task YAML**

Run: `fly validate-pipeline -c deploy/agent-pipeline.yml 2>/dev/null || python3 -c "import yaml,sys; yaml.safe_load(open('ci/tasks/ci-agent-review.yml'))" && echo YAML-OK`
Expected: `YAML-OK`

- [ ] **Step 3: Commit**

```bash
git add ci/tasks/ci-agent-review.yml
git commit -m "feat(ci): review task publishes results to ATC when token provided"
```

---

### Task 9: Elm — API types, endpoints, effects, callbacks

**Files:**
- Create: `web/elm/src/Concourse/AgentReview.elm`
- Modify: `web/elm/src/Api/Endpoints.elm` (Endpoint union + `builder`)
- Modify: `web/elm/src/Message/Effects.elm` (Effect union + `runEffect`)
- Modify: `web/elm/src/Message/Callback.elm` (Callback union)
- Test: `web/elm/tests/AgentReviewTests.elm`

- [ ] **Step 1: Write the failing decoder test**

`web/elm/tests/AgentReviewTests.elm`:

```elm
module AgentReviewTests exposing (all)

import Concourse.AgentReview as AgentReview
import Expect
import Json.Decode
import Test exposing (Test, describe, test)


all : Test
all =
    describe "agent review decoders"
        [ test "decodes a build review with findings and feedback" <|
            \_ ->
                """
                [{"build_id":42,"build_name":"3","team_name":"main","pipeline_name":"cs","job_name":"ar",
                  "repo":"concourse","commit_sha":"abc123","branch":"jetbridge",
                  "score":7.5,"max_score":10,"pass":true,"proven_count":1,"observation_count":1,
                  "summary":"one bug","agent_model":"m","duration_seconds":60,"created_at":1700000000,
                  "evaluated_count":1,"finding_count":2,
                  "proven_issues":[{"id":"PI-1","severity":"high","title":"nil deref","description":"boom","file":"a.go","line":10,"category":"correctness","test_name":"TestNil","test_output":"FAIL"}],
                  "observations":[{"id":"OB-1","title":"long func","file":"b.go","line":5,"category":"maintainability"}],
                  "feedback":{"PI-1":{"verdict":"accurate","notes":"","reviewer":"tdm"}}}]
                """
                    |> Json.Decode.decodeString (Json.Decode.list AgentReview.decodeBuildReview)
                    |> Expect.all
                        [ Result.map (List.length) >> Expect.equal (Ok 1)
                        , Result.map (List.head >> Maybe.map .score) >> Expect.equal (Ok (Just 7.5))
                        , Result.map (List.head >> Maybe.map (.provenIssues >> List.length)) >> Expect.equal (Ok (Just 1))
                        ]
        , test "decodes a review summary row" <|
            \_ ->
                """
                {"build_id":42,"build_name":"3","team_name":"main","pipeline_name":"cs","job_name":"ar",
                 "repo":"concourse","commit_sha":"abc123","branch":"jetbridge",
                 "score":4.0,"max_score":10,"pass":false,"proven_count":4,"observation_count":1,
                 "summary":"several bugs","agent_model":"m","duration_seconds":60,"created_at":1700000000,
                 "evaluated_count":5}
                """
                    |> Json.Decode.decodeString AgentReview.decodeSummary
                    |> Result.map .pass
                    |> Expect.equal (Ok False)
        ]
```

- [ ] **Step 2: Run to verify failure**

Run: `cd web/elm && elm-test tests/AgentReviewTests.elm`
Expected: FAIL — `Concourse.AgentReview` module not found

- [ ] **Step 3: Implement Concourse/AgentReview.elm**

`web/elm/src/Concourse/AgentReview.elm`:

```elm
module Concourse.AgentReview exposing
    ( BuildReview
    , Finding
    , FindingFeedback
    , Summary
    , allVerdicts
    , decodeBuildReview
    , decodeSummary
    , verdictLabel
    )

import Dict exposing (Dict)
import Json.Decode
import Json.Decode.Extra exposing (andMap)


type alias Summary =
    { buildId : Int
    , buildName : String
    , teamName : String
    , pipelineName : String
    , jobName : String
    , repo : String
    , commitSha : String
    , branch : String
    , score : Float
    , maxScore : Float
    , pass : Bool
    , provenCount : Int
    , observationCount : Int
    , summary : String
    , createdAt : Int
    , evaluatedCount : Int
    }


type alias Finding =
    { id : String
    , severity : String
    , title : String
    , description : String
    , file : String
    , line : Int
    , category : String
    , testName : String
    , testOutput : String
    }


type alias FindingFeedback =
    { verdict : String
    , notes : String
    , reviewer : String
    }


type alias BuildReview =
    { summary : Summary
    , provenIssues : List Finding
    , observations : List Finding
    , feedback : Dict String FindingFeedback
    , findingCount : Int
    }


allVerdicts : List String
allVerdicts =
    [ "accurate"
    , "false_positive"
    , "noisy"
    , "overly_strict"
    , "partially_correct"
    , "missed_context"
    ]


verdictLabel : String -> String
verdictLabel verdict =
    String.replace "_" " " verdict


defaultTo : a -> Json.Decode.Decoder a -> Json.Decode.Decoder a
defaultTo default =
    Json.Decode.maybe >> Json.Decode.map (Maybe.withDefault default)


decodeSummary : Json.Decode.Decoder Summary
decodeSummary =
    Json.Decode.succeed Summary
        |> andMap (Json.Decode.field "build_id" Json.Decode.int)
        |> andMap (Json.Decode.field "build_name" Json.Decode.string)
        |> andMap (Json.Decode.field "team_name" Json.Decode.string)
        |> andMap (Json.Decode.field "pipeline_name" Json.Decode.string)
        |> andMap (Json.Decode.field "job_name" Json.Decode.string)
        |> andMap (Json.Decode.field "repo" Json.Decode.string)
        |> andMap (Json.Decode.field "commit_sha" Json.Decode.string)
        |> andMap (defaultTo "" <| Json.Decode.field "branch" Json.Decode.string)
        |> andMap (Json.Decode.field "score" Json.Decode.float)
        |> andMap (Json.Decode.field "max_score" Json.Decode.float)
        |> andMap (Json.Decode.field "pass" Json.Decode.bool)
        |> andMap (Json.Decode.field "proven_count" Json.Decode.int)
        |> andMap (Json.Decode.field "observation_count" Json.Decode.int)
        |> andMap (defaultTo "" <| Json.Decode.field "summary" Json.Decode.string)
        |> andMap (defaultTo 0 <| Json.Decode.field "created_at" Json.Decode.int)
        |> andMap (defaultTo 0 <| Json.Decode.field "evaluated_count" Json.Decode.int)


decodeFinding : Json.Decode.Decoder Finding
decodeFinding =
    Json.Decode.succeed Finding
        |> andMap (Json.Decode.field "id" Json.Decode.string)
        |> andMap (defaultTo "" <| Json.Decode.field "severity" Json.Decode.string)
        |> andMap (Json.Decode.field "title" Json.Decode.string)
        |> andMap (defaultTo "" <| Json.Decode.field "description" Json.Decode.string)
        |> andMap (defaultTo "" <| Json.Decode.field "file" Json.Decode.string)
        |> andMap (defaultTo 0 <| Json.Decode.field "line" Json.Decode.int)
        |> andMap (defaultTo "" <| Json.Decode.field "category" Json.Decode.string)
        |> andMap (defaultTo "" <| Json.Decode.field "test_name" Json.Decode.string)
        |> andMap (defaultTo "" <| Json.Decode.field "test_output" Json.Decode.string)


decodeFeedback : Json.Decode.Decoder FindingFeedback
decodeFeedback =
    Json.Decode.succeed FindingFeedback
        |> andMap (Json.Decode.field "verdict" Json.Decode.string)
        |> andMap (defaultTo "" <| Json.Decode.field "notes" Json.Decode.string)
        |> andMap (defaultTo "" <| Json.Decode.field "reviewer" Json.Decode.string)


decodeBuildReview : Json.Decode.Decoder BuildReview
decodeBuildReview =
    Json.Decode.succeed BuildReview
        |> andMap decodeSummary
        |> andMap (defaultTo [] <| Json.Decode.field "proven_issues" (Json.Decode.list decodeFinding))
        |> andMap (defaultTo [] <| Json.Decode.field "observations" (Json.Decode.list decodeFinding))
        |> andMap (defaultTo Dict.empty <| Json.Decode.field "feedback" (Json.Decode.dict decodeFeedback))
        |> andMap (defaultTo 0 <| Json.Decode.field "finding_count" Json.Decode.int)
```

- [ ] **Step 4: Run decoder tests to verify they pass**

Run: `cd web/elm && elm-test tests/AgentReviewTests.elm`
Expected: PASS

- [ ] **Step 5: Add endpoints, effects, callbacks**

`web/elm/src/Api/Endpoints.elm` — add to the `Endpoint` union:

```elm
    | BuildAgentReviews Concourse.BuildId
    | TeamAgentReviews Concourse.TeamName
    | AgentFeedback
```

and to `builder`:

```elm
        BuildAgentReviews buildId ->
            base |> appendPath [ "builds", String.fromInt buildId, "agent-reviews" ]

        TeamAgentReviews teamName ->
            base |> appendPath [ "teams", teamName, "agent-reviews" ]

        AgentFeedback ->
            base |> appendPath [ "agent", "feedback" ]
```

(`base` is the module's existing `/api/v1` RouteBuilder root — match neighboring cases.)

`web/elm/src/Message/Callback.elm` — add to the `Callback` union:

```elm
    | BuildAgentReviewsFetched (Fetched (List Concourse.AgentReview.BuildReview))
    | TeamAgentReviewsFetched (Fetched (List Concourse.AgentReview.Summary))
    | AgentReviewVerdictSubmitted String (Fetched ())
```

(import `Concourse.AgentReview`; the `String` is the finding id.)

`web/elm/src/Message/Effects.elm` — add to the `Effect` union:

```elm
    | FetchBuildAgentReviews Concourse.BuildId
    | FetchTeamAgentReviews Concourse.TeamName
    | SubmitAgentReviewVerdict
        { repo : String
        , commitSha : String
        , findingId : String
        , verdict : String
        , notes : String
        , reviewer : String
        }
```

and to `runEffect` (model on the `FetchJob` case verbatim; the POST case models on however `SetBuildComment`/`SendTokenToFly`-style JSON posts are done in this module — search `Api.post`):

```elm
        FetchBuildAgentReviews buildId ->
            Api.get (Endpoints.BuildAgentReviews buildId)
                |> Api.expectJson (Json.Decode.list Concourse.AgentReview.decodeBuildReview)
                |> Api.request
                |> Task.attempt BuildAgentReviewsFetched

        FetchTeamAgentReviews teamName ->
            Api.get (Endpoints.TeamAgentReviews teamName)
                |> Api.expectJson (Json.Decode.list Concourse.AgentReview.decodeSummary)
                |> Api.request
                |> Task.attempt TeamAgentReviewsFetched

        SubmitAgentReviewVerdict params ->
            Api.post (Endpoints.AgentFeedback) csrfToken
                |> Api.withJsonBody
                    (Json.Encode.object
                        [ ( "review_ref"
                          , Json.Encode.object
                                [ ( "repo", Json.Encode.string params.repo )
                                , ( "commit", Json.Encode.string params.commitSha )
                                ]
                          )
                        , ( "finding_id", Json.Encode.string params.findingId )
                        , ( "verdict", Json.Encode.string params.verdict )
                        , ( "notes", Json.Encode.string params.notes )
                        , ( "reviewer", Json.Encode.string params.reviewer )
                        , ( "source", Json.Encode.string "interactive" )
                        ]
                    )
                |> Api.expectUnit
                |> Api.request
                |> Task.attempt (AgentReviewVerdictSubmitted params.findingId)
```

(If `Api.post`/`Api.withJsonBody`/`Api.expectUnit` names differ, mirror the exact helpers used by the existing build-comment or pin-comment POST effect in this file — semantics identical.)

- [ ] **Step 6: Compile**

Run: `cd web/elm && elm make src/Main.elm --output=/dev/null`
Expected: compiles (Elm will list every `case` over `Effect`/`Callback` that must be extended — fix each until clean; page handling comes in Tasks 10-11, so unhandled-in-page callbacks just fall through `_ ->` branches)

- [ ] **Step 7: Commit**

```bash
git add web/elm/src/Concourse/AgentReview.elm web/elm/src/Api/Endpoints.elm web/elm/src/Message/Effects.elm web/elm/src/Message/Callback.elm web/elm/tests/AgentReviewTests.elm
git commit -m "feat(web): agent review types, decoders, effects, callbacks"
```

---

### Task 10: Elm — build page agent review panel

**Files:**
- Create: `web/elm/src/Build/AgentReview.elm`
- Modify: `web/elm/src/Build/Models.elm` (add fields to the record in `Model`)
- Modify: `web/elm/src/Build/Build.elm` (init fetch, handleCallback, update, body render)
- Modify: `web/elm/src/Message/Message.elm` (new messages)
- Test: `web/elm/tests/BuildAgentReviewTests.elm`

- [ ] **Step 1: Add messages**

In `web/elm/src/Message/Message.elm`, add to the `Message` union:

```elm
    | ToggleAgentReviewPanel
    | ToggleAgentReviewFinding String
    | ToggleAgentReviewObservations
    | AgentReviewVerdictClicked
        { repo : String
        , commitSha : String
        , findingId : String
        , verdict : String
        , reviewer : String
        }
    | AgentReviewNoteChanged String String
```

- [ ] **Step 2: Add model state**

In `web/elm/src/Build/Models.elm`, add to the inner record of `Model` (alongside `notFound : Bool` etc.):

```elm
, agentReviews : List Concourse.AgentReview.BuildReview
, agentReviewLoadError : Bool
, agentReviewPanelExpanded : Bool
, expandedFindings : Set String
, showObservations : Bool
, agentReviewNotes : Dict String String
, verdictErrors : Set String
```

(imports: `Concourse.AgentReview`, `Set exposing (Set)`, `Dict exposing (Dict)`)

- [ ] **Step 3: Write the failing page test**

`web/elm/tests/BuildAgentReviewTests.elm` — follow `BuildTests.elm` conventions (`Application.handleCallback`, `Common.queryView`, `Query`/`Selector`):

```elm
module BuildAgentReviewTests exposing (all)

import Application.Application as Application
import Common
import Concourse.AgentReview as AgentReview
import Concourse.BuildStatus exposing (BuildStatus(..))
import Data
import Dict
import Expect
import Message.Callback as Callback
import Message.Effects as Effects
import Test exposing (Test, describe, test)
import Test.Html.Query as Query
import Test.Html.Selector exposing (class, containing, id, text)


sampleReview : AgentReview.BuildReview
sampleReview =
    { summary =
        { buildId = 1, buildName = "1", teamName = "t", pipelineName = "p", jobName = "j"
        , repo = "concourse", commitSha = "abc123def", branch = "jetbridge"
        , score = 7.5, maxScore = 10, pass = True
        , provenCount = 1, observationCount = 1, summary = "one bug"
        , createdAt = 0, evaluatedCount = 0
        }
    , provenIssues =
        [ { id = "PI-1", severity = "high", title = "nil deref", description = "boom"
          , file = "a.go", line = 10, category = "correctness"
          , testName = "TestNil", testOutput = "FAIL"
          }
        ]
    , observations = []
    , feedback = Dict.empty
    , findingCount = 2
    }


all : Test
all =
    describe "build page agent review panel"
        [ test "requests agent reviews when the build is fetched" <|
            \_ ->
                Common.init "/builds/1"
                    |> Application.handleCallback
                        (Callback.BuildFetched (Ok (Data.jobBuild BuildStatusSucceeded)))
                    |> Tuple.second
                    |> Common.contains (Effects.FetchBuildAgentReviews 1)
        , test "renders no panel when there are no reviews" <|
            \_ ->
                Common.init "/builds/1"
                    |> Application.handleCallback (Callback.BuildAgentReviewsFetched (Ok []))
                    |> Tuple.first
                    |> Common.queryView
                    |> Query.hasNot [ id "agent-review-panel" ]
        , test "renders summary bar with score and counts" <|
            \_ ->
                Common.init "/builds/1"
                    |> Application.handleCallback (Callback.BuildAgentReviewsFetched (Ok [ sampleReview ]))
                    |> Tuple.first
                    |> Common.queryView
                    |> Query.find [ id "agent-review-panel" ]
                    |> Expect.all
                        [ Query.has [ containing [ text "7.5" ] ]
                        , Query.has [ containing [ text "1 proven" ] ]
                        ]
        , test "shows all six verdicts on an expanded finding" <|
            \_ ->
                Common.init "/builds/1"
                    |> Application.handleCallback (Callback.BuildAgentReviewsFetched (Ok [ sampleReview ]))
                    |> Tuple.first
                    |> Common.queryView
                    |> Query.find [ class "agent-review-verdicts" ]
                    |> Expect.all
                        (AgentReview.allVerdicts
                            |> List.map (\v -> Query.has [ containing [ text (AgentReview.verdictLabel v) ] ])
                        )
        ]
```

(Adjust `Common.init`/`Data` helper names to what `BuildTests.elm` actually uses — same file, same helpers. First proven issue renders expanded by default so the verdict row is visible.)

- [ ] **Step 4: Run to verify failure**

Run: `cd web/elm && elm-test tests/BuildAgentReviewTests.elm`
Expected: FAIL (compile errors — model fields/messages exist but no rendering yet)

- [ ] **Step 5: Implement the panel view**

`web/elm/src/Build/AgentReview.elm` (view-only module; state lives in Build's model). Styling is inline per Build page convention (`Build/Styles.elm` pattern). Complete module:

```elm
module Build.AgentReview exposing (view)

import Concourse.AgentReview as AgentReview exposing (BuildReview, Finding)
import Dict exposing (Dict)
import Html exposing (Html)
import Html.Attributes exposing (class, id, placeholder, style, value)
import Html.Events exposing (onClick, onInput)
import Message.Message exposing (Message(..))
import Set exposing (Set)


type alias PanelState a =
    { a
        | agentReviews : List BuildReview
        , agentReviewLoadError : Bool
        , agentReviewPanelExpanded : Bool
        , expandedFindings : Set String
        , showObservations : Bool
        , agentReviewNotes : Dict String String
        , verdictErrors : Set String
    }


view : String -> PanelState a -> Html Message
view reviewer model =
    case model.agentReviews of
        [] ->
            if model.agentReviewLoadError then
                Html.p
                    [ style "margin" "8px 12px", style "color" "#7a7a7a", style "font-size" "12px" ]
                    [ Html.text "Couldn't load agent review." ]

            else
                Html.text ""

        review :: _ ->
            Html.div
                [ id "agent-review-panel"
                , style "margin" "8px"
                , style "border" "1px solid #3d3c3c"
                , style "background" "#1e1d1d"
                ]
                (summaryBar review model.agentReviewPanelExpanded
                    :: (if model.agentReviewPanelExpanded then
                            [ panelBody reviewer review model ]

                        else
                            []
                       )
                )


summaryBar : BuildReview -> Bool -> Html Message
summaryBar review expanded =
    let
        s =
            review.summary
    in
    Html.div
        [ class "agent-review-summary"
        , style "display" "flex"
        , style "align-items" "center"
        , style "gap" "12px"
        , style "padding" "8px 12px"
        , style "cursor" "pointer"
        , onClick ToggleAgentReviewPanel
        ]
        [ Html.span [ style "font-weight" "700" ] [ Html.text "agent review" ]
        , scoreBadge s
        , Html.span [ style "color" "#b0b0b0" ]
            [ Html.text
                (String.fromInt s.provenCount
                    ++ " proven · "
                    ++ String.fromInt s.observationCount
                    ++ " observations"
                )
            ]
        , Html.span [ style "margin-left" "auto", style "color" "#7a7a7a" ]
            [ Html.text
                ("evaluated "
                    ++ String.fromInt s.evaluatedCount
                    ++ " of "
                    ++ String.fromInt review.findingCount
                )
            ]
        , Html.span [] [ Html.text (if expanded then "▾" else "▸") ]
        ]


scoreBadge : { a | score : Float, maxScore : Float, pass : Bool } -> Html Message
scoreBadge s =
    Html.span
        [ style "padding" "2px 8px"
        , style "font-weight" "700"
        , style "background" (if s.pass then "#2e4f2e" else "#5c2626")
        , style "color" (if s.pass then "#9fdf9f" else "#f0a0a0")
        ]
        [ Html.text (String.fromFloat s.score ++ " / " ++ String.fromFloat s.maxScore) ]


panelBody : String -> BuildReview -> PanelState a -> Html Message
panelBody reviewer review model =
    Html.div [ style "padding" "8px 12px" ]
        ((review.provenIssues
            |> List.map (findingCard reviewer review True model)
         )
            ++ observationsSection reviewer review model
        )


observationsSection : String -> BuildReview -> PanelState a -> List (Html Message)
observationsSection reviewer review model =
    if List.isEmpty review.observations then
        []

    else
        Html.div
            [ class "agent-review-observations-toggle"
            , style "padding" "8px 0"
            , style "cursor" "pointer"
            , style "color" "#b0b0b0"
            , onClick ToggleAgentReviewObservations
            ]
            [ Html.text
                ("observations ("
                    ++ String.fromInt (List.length review.observations)
                    ++ ") — advisory, no failing test "
                    ++ (if model.showObservations then "▾" else "▸")
                )
            ]
            :: (if model.showObservations then
                    review.observations |> List.map (findingCard reviewer review False model)

                else
                    []
               )


findingCard : String -> BuildReview -> Bool -> PanelState a -> Finding -> Html Message
findingCard reviewer review isProven model finding =
    let
        expanded =
            isProven || Set.member finding.id model.expandedFindings

        recorded =
            Dict.get finding.id review.feedback
    in
    Html.div
        [ class "agent-review-finding"
        , style "border" "1px solid #3d3c3c"
        , style "margin-bottom" "8px"
        , style "padding" "8px 12px"
        ]
        ([ Html.div
            [ style "display" "flex"
            , style "align-items" "center"
            , style "gap" "8px"
            , style "cursor" "pointer"
            , onClick (ToggleAgentReviewFinding finding.id)
            ]
            [ severityBadge finding.severity
            , Html.span [ style "font-weight" "700" ] [ Html.text finding.title ]
            , Html.span
                [ style "margin-left" "auto", style "font-family" "monospace", style "color" "#7a7a7a" ]
                [ Html.text (finding.file ++ ":" ++ String.fromInt finding.line) ]
            ]
         ]
            ++ (if expanded then
                    [ Html.p [ style "color" "#b0b0b0", style "margin" "8px 0" ]
                        [ Html.text finding.description ]
                    ]
                        ++ testEvidence finding
                        ++ [ verdictRow reviewer review finding recorded model ]

                else
                    []
               )
        )


severityBadge : String -> Html Message
severityBadge severity =
    let
        ( bg, fg ) =
            case severity of
                "critical" ->
                    ( "#5c2626", "#f0a0a0" )

                "high" ->
                    ( "#5c2626", "#f0a0a0" )

                "medium" ->
                    ( "#5c4a26", "#f0d0a0" )

                _ ->
                    ( "#3d3c3c", "#b0b0b0" )
    in
    if severity == "" then
        Html.text ""

    else
        Html.span
            [ style "background" bg, style "color" fg, style "padding" "1px 6px", style "font-size" "12px" ]
            [ Html.text severity ]


testEvidence : Finding -> List (Html Message)
testEvidence finding =
    if finding.testOutput == "" then
        []

    else
        [ Html.pre
            [ style "background" "#141313"
            , style "padding" "8px"
            , style "font-size" "12px"
            , style "overflow-x" "auto"
            ]
            [ Html.text finding.testOutput ]
        ]


verdictRow :
    String
    -> BuildReview
    -> Finding
    -> Maybe AgentReview.FindingFeedback
    -> PanelState a
    -> Html Message
verdictRow reviewer review finding recorded model =
    Html.div []
        [ Html.div
            [ class "agent-review-verdicts"
            , style "display" "flex"
            , style "align-items" "center"
            , style "gap" "0"
            , style "margin-top" "8px"
            , style "border" "1px solid #555"
            , style "width" "fit-content"
            ]
            (AgentReview.allVerdicts
                |> List.map
                    (\verdict ->
                        let
                            selected =
                                recorded |> Maybe.map (.verdict >> (==) verdict) |> Maybe.withDefault False
                        in
                        Html.span
                            [ style "padding" "4px 10px"
                            , style "font-size" "12px"
                            , style "cursor" "pointer"
                            , style "border-right" "1px solid #555"
                            , style "background" (if selected then "#e0e0e0" else "transparent")
                            , style "color" (if selected then "#141313" else "#b0b0b0")
                            , onClick
                                (AgentReviewVerdictClicked
                                    { repo = review.summary.repo
                                    , commitSha = review.summary.commitSha
                                    , findingId = finding.id
                                    , verdict = verdict
                                    , reviewer = reviewer
                                    }
                                )
                            ]
                            [ Html.text (AgentReview.verdictLabel verdict) ]
                    )
            )
        , Html.input
            [ placeholder "Add a note about this verdict"
            , value (Dict.get finding.id model.agentReviewNotes |> Maybe.withDefault "")
            , onInput (AgentReviewNoteChanged finding.id)
            , style "width" "100%"
            , style "margin-top" "6px"
            , style "background" "#141313"
            , style "color" "#e0e0e0"
            , style "border" "1px solid #3d3c3c"
            , style "padding" "4px 8px"
            ]
            []
        , if Set.member finding.id model.verdictErrors then
            Html.p [ style "color" "#f0a0a0", style "font-size" "12px", style "margin" "4px 0 0" ]
                [ Html.text "Couldn't save verdict. Click a verdict to retry." ]

          else
            Html.text ""
        ]
```

- [ ] **Step 6: Wire into Build.elm**

In `web/elm/src/Build/Build.elm`:

a. Init the new model fields in `init` (inside the model record): `agentReviews = []`, `agentReviewLoadError = False`, `agentReviewPanelExpanded = True`, `expandedFindings = Set.empty`, `showObservations = False`, `agentReviewNotes = Dict.empty`, `verdictErrors = Set.empty`.

b. In `handleCallback`, in the `BuildFetched (Ok build) ->` path (inside `handleBuildFetched`), append the fetch effect once the build id is known:

```elm
effects ++ [ FetchBuildAgentReviews build.id ]
```

c. Add callback cases:

```elm
BuildAgentReviewsFetched (Ok reviews) ->
    ( { model | agentReviews = reviews, agentReviewLoadError = False }, effects )

BuildAgentReviewsFetched (Err _) ->
    -- Missing review (empty list) is a normal state and renders nothing;
    -- an API error renders a quiet one-line notice instead of breaking the page.
    ( { model | agentReviewLoadError = True }, effects )

AgentReviewVerdictSubmitted findingId (Ok ()) ->
    ( { model | verdictErrors = Set.remove findingId model.verdictErrors }
    , effects ++ [ FetchBuildAgentReviews (currentBuildId model) ]
    )

AgentReviewVerdictSubmitted findingId (Err _) ->
    ( { model | verdictErrors = Set.insert findingId model.verdictErrors }, effects )
```

(`currentBuildId` — use however the model exposes the fetched build's id, `model.id` per Build.init; re-fetch refreshes the recorded verdict + evaluated count.)

d. Add update cases in `update`:

```elm
ToggleAgentReviewPanel ->
    ( { model | agentReviewPanelExpanded = not model.agentReviewPanelExpanded }, effects )

ToggleAgentReviewFinding findingId ->
    ( { model
        | expandedFindings =
            if Set.member findingId model.expandedFindings then
                Set.remove findingId model.expandedFindings

            else
                Set.insert findingId model.expandedFindings
      }
    , effects
    )

ToggleAgentReviewObservations ->
    ( { model | showObservations = not model.showObservations }, effects )

AgentReviewVerdictClicked params ->
    ( model
    , effects
        ++ [ SubmitAgentReviewVerdict
                { repo = params.repo
                , commitSha = params.commitSha
                , findingId = params.findingId
                , verdict = params.verdict
                , notes = Dict.get params.findingId model.agentReviewNotes |> Maybe.withDefault ""
                , reviewer = params.reviewer
                }
           ]
    )

AgentReviewNoteChanged findingId note ->
    ( { model | agentReviewNotes = Dict.insert findingId note model.agentReviewNotes }, effects )
```

e. Render in `body` (in the `authorized` branch, after `viewBuildPrep prep`):

```elm
, Build.AgentReview.view (reviewerName session) params
```

where `reviewerName session` extracts the logged-in username (follow how `Login.view session.userState` gets it — `UserState.UserStateLoggedIn user -> user.userName`, else `"anonymous"`). Note `body`'s extensible record type annotation must gain the six new fields (or pass the full model — match how `Header.view` receives it).

- [ ] **Step 7: Compile and run tests**

Run: `cd web/elm && elm make src/Main.elm --output=/dev/null && elm-test tests/BuildAgentReviewTests.elm`
Expected: PASS

- [ ] **Step 8: Commit**

```bash
git add web/elm/src/Build/AgentReview.elm web/elm/src/Build/Models.elm web/elm/src/Build/Build.elm web/elm/src/Message/Message.elm web/elm/tests/BuildAgentReviewTests.elm
git commit -m "feat(web): agent review panel on the build page with verdict capture"
```

---

### Task 11: Elm — team agent reviews page

**Files:**
- Create: `web/elm/src/AgentReviews/AgentReviews.elm`
- Modify: `web/elm/src/Routes.elm` (Route union, parser, `sitemap`, `toString`)
- Modify: `web/elm/src/SubPage/SubPage.elm` (Model union, init, update/handleCallback dispatch, view)
- Test: `web/elm/tests/AgentReviewsPageTests.elm`

- [ ] **Step 1: Add the route**

In `web/elm/src/Routes.elm`:

Route union: `| AgentReviews { teamName : String }`

Parser (near `flySuccess`):

```elm
agentReviews : Parser ((b -> Route) -> a) a
agentReviews =
    map (\teamName -> always <| AgentReviews { teamName = teamName })
        (s "teams" </> string </> s "agent-reviews")
```

Add `agentReviews` to `sitemap`'s `oneOf` list (before `pipeline` so the more specific path wins — check parse order against how `build` vs `pipeline` are ordered and place accordingly).

`toString`:

```elm
AgentReviews { teamName } ->
    ( [ "teams", teamName, "agent-reviews" ], [] )
        |> RouteBuilder.build
```

- [ ] **Step 2: Write the failing page test**

`web/elm/tests/AgentReviewsPageTests.elm`:

```elm
module AgentReviewsPageTests exposing (all)

import Application.Application as Application
import Common
import Message.Callback as Callback
import Message.Effects as Effects
import Test exposing (Test, describe, test)
import Test.Html.Query as Query
import Test.Html.Selector exposing (class, containing, text)


sampleSummary =
    { buildId = 42, buildName = "3", teamName = "main", pipelineName = "cs", jobName = "ar"
    , repo = "concourse", commitSha = "abc123def456", branch = "jetbridge"
    , score = 7.5, maxScore = 10, pass = True
    , provenCount = 2, observationCount = 3, summary = "stuff"
    , createdAt = 0, evaluatedCount = 1
    }


all : Test
all =
    describe "agent reviews page"
        [ test "fetches team reviews on load" <|
            \_ ->
                Common.init "/teams/main/agent-reviews"
                    |> Tuple.second
                    |> Common.contains (Effects.FetchTeamAgentReviews "main")
        , test "renders review rows with score and evaluated count" <|
            \_ ->
                Common.init "/teams/main/agent-reviews"
                    |> Tuple.first
                    |> (\model ->
                            Application.handleCallback
                                (Callback.TeamAgentReviewsFetched (Ok [ sampleSummary ]))
                                model
                       )
                    |> Tuple.first
                    |> Common.queryView
                    |> Query.find [ class "agent-review-row" ]
                    |> Query.has
                        [ containing [ text "7.5" ]
                        , containing [ text "cs / ar" ]
                        , containing [ text "evaluated 1/5" ]
                        ]
        ]
```

- [ ] **Step 3: Run to verify failure**

Run: `cd web/elm && elm-test tests/AgentReviewsPageTests.elm`
Expected: FAIL — route/page do not exist

- [ ] **Step 4: Implement the page**

`web/elm/src/AgentReviews/AgentReviews.elm` — model on the simplest existing page (`FlySuccess` for structure, `Login.Model` wrapper like other pages if top-bar login is rendered — copy the top-bar scaffolding from `Build.view`'s `page-including-top-bar` structure):

```elm
module AgentReviews.AgentReviews exposing
    ( Model
    , documentTitle
    , handleCallback
    , init
    , update
    , view
    )

import Concourse.AgentReview as AgentReview
import EffectTransformer exposing (ET)
import Html exposing (Html)
import Html.Attributes exposing (class, href, id, style)
import Login.Login as Login
import Message.Callback exposing (Callback(..))
import Message.Effects exposing (Effect(..))
import Message.Message exposing (Message(..))
import Routes
import SideBar.SideBar as SideBar
import UserState exposing (UserState)
import Views.Styles


type alias Model =
    Login.Model
        { teamName : String
        , reviews : List AgentReview.Summary
        , loaded : Bool
        , loadError : Bool
        }


init : { teamName : String } -> ( Model, List Effect )
init { teamName } =
    ( { teamName = teamName
      , reviews = []
      , loaded = False
      , loadError = False
      , isUserMenuExpanded = False
      }
    , [ FetchTeamAgentReviews teamName ]
    )


documentTitle : String
documentTitle =
    "Agent reviews"


handleCallback : Callback -> ET Model
handleCallback callback ( model, effects ) =
    case callback of
        TeamAgentReviewsFetched (Ok reviews) ->
            ( { model | reviews = reviews, loaded = True }, effects )

        TeamAgentReviewsFetched (Err _) ->
            ( { model | loaded = True, loadError = True }, effects )

        _ ->
            ( model, effects )


update : Message -> ET Model
update _ ( model, effects ) =
    ( model, effects )


view : { a | userState : UserState } -> Model -> Html Message
view session model =
    Html.div
        (id "page-including-top-bar" :: Views.Styles.pageIncludingTopBar)
        [ Html.div
            (id "top-bar-app" :: Views.Styles.topBar False)
            [ Login.view session.userState model ]
        , Html.div
            [ id "page-below-top-bar", style "padding" "16px" ]
            [ Html.h1 [ style "font-size" "18px" ]
                [ Html.text ("Agent reviews — " ++ model.teamName) ]
            , if model.loadError then
                Html.p [ style "color" "#f0a0a0" ] [ Html.text "Couldn't load agent reviews." ]

              else if model.loaded && List.isEmpty model.reviews then
                Html.p [ style "color" "#b0b0b0" ] [ Html.text "No agent reviews yet." ]

              else
                Html.div [] (List.map reviewRow model.reviews)
            ]
        ]


reviewRow : AgentReview.Summary -> Html Message
reviewRow s =
    Html.a
        [ class "agent-review-row"
        , href ("/builds/" ++ String.fromInt s.buildId)
        , style "display" "flex"
        , style "align-items" "center"
        , style "gap" "12px"
        , style "padding" "8px 12px"
        , style "border-bottom" "1px solid #3d3c3c"
        , style "color" "inherit"
        , style "text-decoration" "none"
        ]
        [ Html.span
            [ style "padding" "2px 8px"
            , style "font-weight" "700"
            , style "background" (if s.pass then "#2e4f2e" else "#5c2626")
            , style "color" (if s.pass then "#9fdf9f" else "#f0a0a0")
            ]
            [ Html.text (String.fromFloat s.score) ]
        , Html.div []
            [ Html.div [] [ Html.text (s.pipelineName ++ " / " ++ s.jobName ++ " #" ++ s.buildName) ]
            , Html.div
                [ style "font-family" "monospace", style "font-size" "12px", style "color" "#7a7a7a" ]
                [ Html.text
                    (s.branch
                        ++ " @ "
                        ++ String.left 7 s.commitSha
                        ++ " · "
                        ++ String.fromInt s.provenCount
                        ++ " issues · "
                        ++ String.fromInt s.observationCount
                        ++ " obs"
                    )
                ]
            ]
        , Html.span [ style "margin-left" "auto", style "color" "#b0b0b0" ]
            [ Html.text
                ("evaluated "
                    ++ String.fromInt s.evaluatedCount
                    ++ "/"
                    ++ String.fromInt (s.provenCount + s.observationCount)
                )
            ]
        ]
```

(If the SideBar/top-bar scaffolding requires more session plumbing than `Login.view`, mirror the `DownloadFly` page — the simplest routed page with a top bar.)

- [ ] **Step 5: Wire into SubPage**

In `web/elm/src/SubPage/SubPage.elm`: add `| AgentReviewsModel AgentReviews.Model` to the `Model` union; add init case:

```elm
Routes.AgentReviews { teamName } ->
    AgentReviews.init { teamName = teamName }
        |> Tuple.mapFirst AgentReviewsModel
```

Extend `genericUpdate` call sites for update/handleCallback/view following how `DownloadFlyModel` is threaded (every `genericUpdate` gets one more function argument — the compiler enumerates each site).

View case:

```elm
AgentReviewsModel model ->
    ( AgentReviews.documentTitle
    , AgentReviews.view session model
    )
```

- [ ] **Step 6: Compile and run tests**

Run: `cd web/elm && elm make src/Main.elm --output=/dev/null && elm-test tests/AgentReviewsPageTests.elm`
Expected: PASS

- [ ] **Step 7: Commit**

```bash
git add web/elm/src/AgentReviews/ web/elm/src/Routes.elm web/elm/src/SubPage/SubPage.elm web/elm/tests/AgentReviewsPageTests.elm
git commit -m "feat(web): team agent reviews page at /teams/:team/agent-reviews"
```

---

### Task 12: Delete the orphaned AgentFeedback Elm app

**Files:**
- Delete: `web/elm/src/AgentFeedback/` (Main.elm, Session.elm, Summary.elm, ChatPanel.elm, Api.elm, Types.elm, FindingCard.elm, VerdictPicker.elm)
- Delete: `web/assets/css/agent-feedback.less`

The panel (Task 10) supersedes FindingCard/VerdictPicker; nothing mounts this standalone app (verified: no references in `Routes.elm`, `index.html`, or the asset bundle).

- [ ] **Step 1: Verify nothing else imports it**

Run: `grep -rn "AgentFeedback" web/elm/src --include="*.elm" | grep -v "^web/elm/src/AgentFeedback"`
Expected: no output. Also `grep -rn "agent-feedback" web/assets web/public/index.html` → only the less file itself.

- [ ] **Step 2: Delete**

```bash
git rm -r web/elm/src/AgentFeedback web/assets/css/agent-feedback.less
```

- [ ] **Step 3: Compile + full Elm test suite**

Run: `cd web/elm && elm make src/Main.elm --output=/dev/null && elm-test`
Expected: compiles, PASS (if any elm test file imported AgentFeedback, delete/fix it — the panel tests from Task 10 cover the ported behavior)

- [ ] **Step 4: Commit**

```bash
git commit -m "chore(web): remove orphaned standalone AgentFeedback app"
```

---

### Task 13: ATC integration test — POST → GET round trip

**Files:**
- Create or extend: the ATC integration suite (find it: `grep -rn "test-integration" Makefile` shows the target's package — the 21-spec suite that runs with real Postgres). Add `agent_reviews_test.go` beside its siblings.

- [ ] **Step 1: Locate the suite and its helper pattern**

Run: `grep -n "test-integration" Makefile` and open the referenced package; read one existing spec to copy its ATC-boot + HTTP-client pattern (it boots a real ATC against Postgres and hits `/api/v1/...`).

- [ ] **Step 2: Write the spec**

Following the suite's conventions (Ginkgo, real ATC with `--agent-review-publish-token=integration-token` added to the command args the suite boots with):

```go
var _ = Describe("agent reviews API", func() {
	It("ingests a published review and serves it back", func() {
		// 1. create a one-off build via the API (or trigger a job the suite already sets up)
		//    to obtain a real build ID — follow the suite's existing build-creation helper.
		buildID := createBuild() // suite helper

		review := `{"schema_version":"1.0.0","metadata":{"repo":"itest","commit":"deadbeef"},"score":{"value":8,"max":10,"pass":true},"proven_issues":[],"observations":[],"summary":"clean"}`
		body := fmt.Sprintf(`{"build_id": %d, "review": %s}`, buildID, review)

		req, _ := http.NewRequest("POST", atcURL+"/api/v1/agent/reviews", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer integration-token")
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		Expect(err).ToNot(HaveOccurred())
		Expect(resp.StatusCode).To(Equal(http.StatusCreated))

		// GET by build (authenticated — use the suite's logged-in client helper)
		getResp := authedGet(fmt.Sprintf("/api/v1/builds/%d/agent-reviews", buildID))
		Expect(getResp.StatusCode).To(Equal(http.StatusOK))
		var reviews []map[string]any
		Expect(json.NewDecoder(getResp.Body).Decode(&reviews)).To(Succeed())
		Expect(reviews).To(HaveLen(1))
		Expect(reviews[0]["repo"]).To(Equal("itest"))

		// GET team listing
		listResp := authedGet("/api/v1/teams/main/agent-reviews")
		Expect(listResp.StatusCode).To(Equal(http.StatusOK))
	})
})
```

(`createBuild`/`authedGet`/`atcURL` stand for the suite's actual helpers — every integration spec in that package creates builds and makes authed requests; reuse those exact helpers rather than inventing new ones.)

- [ ] **Step 3: Run**

Run: `make test-integration`
Expected: PASS (~12s suite)

- [ ] **Step 4: Commit**

```bash
git add <integration test file>
git commit -m "test(atc): integration coverage for agent reviews ingest and read"
```

---

### Task 14: Rollout to concourse.home

**Files:**
- Modify: `deploy/concourse-pipeline.yml` (add `agent-review` job)
- Live config: Helm values secret on theborg (manual step)

- [ ] **Step 1: Add the pipeline job**

In `deploy/concourse-pipeline.yml`, add a job following the existing job/task conventions in that file (git resource on the `jetbridge` branch already exists — reuse it):

```yaml
- name: agent-review
  plan:
  - get: concourse-repo        # match the existing git resource name in this file
    trigger: true
  - task: review
    file: concourse-repo/ci/tasks/ci-agent-review.yml
    input_mapping: {repo: concourse-repo}
    params:
      REVIEW_DIFF_ONLY: "true"
      BASE_REF: main
      AGENT_REVIEW_PUBLISH_TOKEN: ((agent_review_publish_token))
```

(Adjust resource name and var syntax to match the file's existing jobs; the task needs an Anthropic-credentialed image or `AGENT_CLI` config consistent with how `deploy/agent-pipeline.yml` jobs run ci-agent — copy those params.)

- [ ] **Step 2: Commit and let the release chain deploy**

```bash
git add deploy/concourse-pipeline.yml
git commit -m "feat(deploy): agent-review job publishing to the ATC"
```

Push per the normal jetbridge flow; the self-build pipeline builds and verify-upgrade promotes. The migration applies on web startup.

- [ ] **Step 3: Configure the publish token (manual, on theborg)**

- Add `agentReviewPublishToken` (or the raw env `CONCOURSE_AGENT_REVIEW_PUBLISH_TOKEN`) to the web deployment's Helm values secret / extra env.
- Add `agent_review_publish_token` to the pipeline's credential source (however `((vars))` are provided for this pipeline on concourse.home).
- `fly set-pipeline` for the updated `concourse-pipeline.yml`.

- [ ] **Step 4: Acceptance (the definition of done)**

1. Push a commit to `jetbridge`.
2. Watch the `agent-review` job run; task output ends with "review published".
3. Open the build at `https://concourse.home/builds/<id>` — the agent review panel renders with score and findings.
4. Click a verdict on a finding; reload; the segmented control shows it selected and "evaluated n of m" incremented.
5. Open `https://concourse.home/teams/main/agent-reviews` — the run is listed, newest first; clicking the row lands on the build.

---

## Task order and independence

Tasks 1→5 are sequential (each builds on the previous). Task 6 (TaskEnv) and Task 7 (publish) are independent of 2-5 and of each other; Task 8 needs 7. Tasks 9→12 are sequential Elm work needing only the API shape (Task 3) to be settled. Task 13 needs 1-5. Task 14 needs everything.
