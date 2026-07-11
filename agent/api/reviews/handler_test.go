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

func TestSubmitOversizedBody(t *testing.T) {
	h, _, _ := newHandler(t)
	req := httptest.NewRequest("POST", "/api/v1/agent/reviews",
		strings.NewReader(`{"build_id":42,"review":`+strings.Repeat(" ", 5<<20)+`}`))
	req.Header.Set("Authorization", "Bearer secret-token")
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

func TestSubmitWithPrincipalContextSkipsStaticToken(t *testing.T) {
	h, store, _ := newHandler(t)

	req := httptest.NewRequest("POST", "/api/v1/agent/reviews", strings.NewReader(postBody()))
	// no Authorization header at all — the wrappa already verified the principal
	req = req.WithContext(principals.NewContext(req.Context(), principals.Principal{
		ID: 3, Name: "itest-reviewer",
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

func TestSubmitWithStaticTokenAttributesLegacyPublish(t *testing.T) {
	h, store, _ := newHandler(t)

	req := httptest.NewRequest("POST", "/api/v1/agent/reviews", strings.NewReader(postBody()))
	req.Header.Set("Authorization", "Bearer secret-token")
	rec := httptest.NewRecorder()
	h.SubmitReview(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("code = %d body %s, want 201", rec.Code, rec.Body)
	}
	recs, _ := store.GetByBuild(42)
	if len(recs) != 1 || recs[0].SubmittedBy != principals.LegacyPublishPrincipalName {
		t.Errorf("submitted_by = %+v, want legacy-publish", recs)
	}
}
