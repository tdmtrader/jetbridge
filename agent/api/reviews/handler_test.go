package reviews_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/concourse/concourse/agent/api/feedback"
	"github.com/concourse/concourse/agent/api/principals"
	"github.com/concourse/concourse/agent/api/reviews"
	"github.com/concourse/concourse/agent/snapshot"
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
	return reviews.NewHandler(store, fbStore, lookup, "main"), store, fbStore
}

func TestCanonicalSnapshotAndWorkflowRunReviewReadsUseSnapshotFeedbackIdentity(t *testing.T) {
	h, store, fbStore := newHandler(t)
	first, second := snapshot.SnapshotID(201), snapshot.SnapshotID(202)
	runID := snapshot.WorkflowRunID(33)
	productionID := snapshot.DatabaseID(9007199254740997)
	for index, snapshotID := range []snapshot.SnapshotID{first, second} {
		review := &reviews.StoredReview{
			BuildID: 42, TeamName: "main", Repo: "org/repo", CommitSha: "same-commit",
			SnapshotID: &snapshotID, WorkflowRunID: &runID, ProductionID: &productionID,
			Review: json.RawMessage(validReview), CreatedAt: int64(index + 1),
		}
		if err := store.Upsert(review); err != nil {
			t.Fatal(err)
		}
	}
	if err := fbStore.Save(&feedback.StoredFeedback{
		ReviewSnapshotID: &first, ReviewTeamName: "main",
		FindingID: "PI-1", Verdict: "accurate", Reviewer: "alice",
	}); err != nil {
		t.Fatal(err)
	}
	if err := fbStore.Save(&feedback.StoredFeedback{
		ReviewSnapshotID: &second, ReviewTeamName: "main",
		FindingID: "PI-1", Verdict: "false_positive", Reviewer: "alice",
	}); err != nil {
		t.Fatal(err)
	}

	snapshotReq := httptest.NewRequest(http.MethodGet, "/api/v1/agent/snapshots/201/projections/review", nil)
	snapshotReq.URL.RawQuery = "%3Asnapshot_id=201"
	snapshotResponse := httptest.NewRecorder()
	h.GetBySnapshot(snapshotResponse, snapshotReq)
	if snapshotResponse.Code != http.StatusOK {
		t.Fatalf("snapshot code = %d: %s", snapshotResponse.Code, snapshotResponse.Body.String())
	}
	if !strings.Contains(snapshotResponse.Body.String(), `"production_id":"9007199254740997"`) {
		t.Fatalf("production ID lost JSON precision: %s", snapshotResponse.Body.String())
	}
	var got reviews.BuildReviewResponse
	if err := json.Unmarshal(snapshotResponse.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.SnapshotID == nil || *got.SnapshotID != first || got.Feedback["PI-1"].Verdict != "accurate" {
		t.Fatalf("snapshot response = %#v", got)
	}

	runReq := httptest.NewRequest(http.MethodGet, "/api/v1/agent/workflows/code-review/runs/33/reviews", nil)
	runReq.URL.RawQuery = "%3Aworkflow_name=code-review&%3Aworkflow_run_id=33"
	runResponse := httptest.NewRecorder()
	h.ListByWorkflowRun(runResponse, runReq)
	if runResponse.Code != http.StatusOK {
		t.Fatalf("run code = %d: %s", runResponse.Code, runResponse.Body.String())
	}
	var runReviews []reviews.BuildReviewResponse
	if err := json.Unmarshal(runResponse.Body.Bytes(), &runReviews); err != nil {
		t.Fatal(err)
	}
	if len(runReviews) != 2 || runReviews[0].SnapshotID == nil || runReviews[1].SnapshotID == nil {
		t.Fatalf("run reviews = %#v", runReviews)
	}
	if runReviews[0].Feedback["PI-1"].Verdict == runReviews[1].Feedback["PI-1"].Verdict {
		t.Fatalf("same-commit snapshot feedback collided: %#v", runReviews)
	}
}

func TestCanonicalReviewReadsRejectShadowedRouteIdentity(t *testing.T) {
	h, _, _ := newHandler(t)

	snapshotReq := httptest.NewRequest(http.MethodGet, "/api/v1/agent/snapshots/201/projections/review", nil)
	// The router contributes exactly one reserved path value. A caller-supplied
	// duplicate must not be allowed to choose which snapshot reaches the store.
	snapshotReq.URL.RawQuery = "%3Asnapshot_id=999&%3Asnapshot_id=201"
	snapshotResponse := httptest.NewRecorder()
	h.GetBySnapshot(snapshotResponse, snapshotReq)
	if snapshotResponse.Code != http.StatusBadRequest {
		t.Fatalf("snapshot code = %d, want 400", snapshotResponse.Code)
	}

	runReq := httptest.NewRequest(http.MethodGet, "/api/v1/agent/workflows/code-review/runs/33/reviews", nil)
	runReq.URL.RawQuery = "%3Aworkflow_name=shadow&%3Aworkflow_name=code-review&%3Aworkflow_run_id=33"
	runResponse := httptest.NewRecorder()
	h.ListByWorkflowRun(runResponse, runReq)
	if runResponse.Code != http.StatusBadRequest {
		t.Fatalf("workflow run code = %d, want 400", runResponse.Code)
	}
}

func postBody() string {
	return `{"build_id": 42, "review": ` + validReview + `}`
}

func TestSubmitRequiresScopedPrincipal(t *testing.T) {
	h, _, _ := newHandler(t)
	req := httptest.NewRequest("POST", "/api/v1/agent/reviews", strings.NewReader(postBody()))
	rec := httptest.NewRecorder()
	h.SubmitReview(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Errorf("code = %d, want 403 without a scoped principal", rec.Code)
	}
}

func TestSubmitUnknownBuild(t *testing.T) {
	h, _, _ := newHandler(t)
	req := httptest.NewRequest("POST", "/api/v1/agent/reviews", strings.NewReader(`{"build_id": 999, "review": `+validReview+`}`))
	req = req.WithContext(principals.NewContext(req.Context(), principals.Principal{ID: 1, Name: "reviewer", TeamName: "main"}))
	rec := httptest.NewRecorder()
	h.SubmitReview(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Errorf("code = %d, want 404", rec.Code)
	}
}

func TestSubmitMalformed(t *testing.T) {
	h, _, _ := newHandler(t)
	req := httptest.NewRequest("POST", "/api/v1/agent/reviews", strings.NewReader(`{"build_id": 42}`))
	req = req.WithContext(principals.NewContext(req.Context(), principals.Principal{ID: 1, Name: "reviewer", TeamName: "main"}))
	rec := httptest.NewRecorder()
	h.SubmitReview(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("code = %d, want 400", rec.Code)
	}
}

func TestSubmitOversizedBody(t *testing.T) {
	h, _, _ := newHandler(t)
	req := httptest.NewRequest("POST", "/api/v1/agent/reviews",
		strings.NewReader(`{"build_id":42,"review":`+strings.Repeat(" ", 5<<20)+`}`))
	req = req.WithContext(principals.NewContext(req.Context(), principals.Principal{ID: 1, Name: "reviewer", TeamName: "main"}))
	rec := httptest.NewRecorder()
	h.SubmitReview(rec, req)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("code = %d, want 413", rec.Code)
	}
}

func TestGetByBuildToleratesMalformedFinding(t *testing.T) {
	h, store, _ := newHandler(t)
	// One well-formed and one malformed proven issue ("line": "ten").
	review := `{
		"schema_version": "1.0.0",
		"metadata": {"repo": "concourse", "commit": "def456"},
		"score": {"value": 5, "max": 10, "pass": false},
		"proven_issues": [
			{"id": "PI-1", "title": "good", "file": "a.go", "line": 1, "category": "correctness"},
			{"id": "PI-2", "title": "bad line", "file": "b.go", "line": "ten", "category": "correctness"}
		],
		"observations": [],
		"summary": "mixed"
	}`
	store.Upsert(&reviews.StoredReview{
		BuildID: 42, TeamName: "main", Repo: "concourse", CommitSha: "def456",
		ProvenCount: 2, ObservationCount: 0, Review: json.RawMessage(review),
	})

	req := httptest.NewRequest("GET", "/api/v1/builds/42/agent-reviews", nil)
	req.Form = map[string][]string{":build_id": {"42"}}
	rec := httptest.NewRecorder()
	h.GetByBuild(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d", rec.Code)
	}
	var got []reviews.BuildReviewResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d reviews", len(got))
	}
	if len(got[0].ProvenIssues) != 2 {
		t.Errorf("proven issues = %d, want 2 (malformed entry must not vanish)", len(got[0].ProvenIssues))
	}
	if got[0].FindingCount != got[0].ProvenCount+got[0].ObservationCount {
		t.Errorf("finding_count %d disagrees with proven+observation %d",
			got[0].FindingCount, got[0].ProvenCount+got[0].ObservationCount)
	}
}

func TestSubmitAndGetByBuild(t *testing.T) {
	h, _, fbStore := newHandler(t)
	req := httptest.NewRequest("POST", "/api/v1/agent/reviews", strings.NewReader(postBody()))
	req = req.WithContext(principals.NewContext(req.Context(), principals.Principal{ID: 1, Name: "reviewer", TeamName: "main"}))
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
		req = req.WithContext(principals.NewContext(req.Context(), principals.Principal{ID: 1, Name: "reviewer", TeamName: "main"}))
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

func TestSubmitWithPrincipalContext(t *testing.T) {
	h, store, _ := newHandler(t)

	req := httptest.NewRequest("POST", "/api/v1/agent/reviews", strings.NewReader(postBody()))
	// no Authorization header at all — the wrappa already verified the principal
	req = req.WithContext(principals.NewContext(req.Context(), principals.Principal{
		ID: 3, Name: "itest-reviewer", TeamName: "main",
	}))
	rec := httptest.NewRecorder()
	h.SubmitReview(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("code = %d body %s, want 201", rec.Code, rec.Body)
	}
	recs, _ := store.GetByBuild(42)
	if len(recs) != 1 || recs[0].SubmittedBy != "itest-reviewer" {
		t.Errorf("submitted_by = %+v, want itest-reviewer", recs)
	}
}

func TestSubmitWithPrincipalRejectsBuildFromAnotherTeam(t *testing.T) {
	h, store, _ := newHandler(t)

	req := httptest.NewRequest("POST", "/api/v1/agent/reviews", strings.NewReader(postBody()))
	req = req.WithContext(principals.NewContext(req.Context(), principals.Principal{
		ID: 4, Name: "other-team-reviewer", TeamName: "other",
	}))
	rec := httptest.NewRecorder()
	h.SubmitReview(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("code = %d body %s, want 403", rec.Code, rec.Body)
	}
	recs, err := store.GetByBuild(42)
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 0 {
		t.Fatalf("stored cross-team review = %+v", recs)
	}
}
