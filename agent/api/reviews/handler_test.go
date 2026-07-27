package reviews_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/concourse/concourse/agent/api/feedback"
	"github.com/concourse/concourse/agent/api/reviews"
	"github.com/concourse/concourse/agent/api/reviews/reviewstest"
	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/agent/snapshot/contracts"
)

func newHandler(t *testing.T) (*reviews.Handler, *reviewstest.MemoryStore, *feedback.MemoryStore) {
	t.Helper()
	store := reviewstest.NewMemoryStore()
	fbStore := feedback.NewMemoryStore()
	return reviews.NewHandler(store, fbStore, "main"), store, fbStore
}

func TestCanonicalSnapshotAndWorkflowRunReviewReadsUseSnapshotFeedbackIdentity(t *testing.T) {
	h, store, fbStore := newHandler(t)
	first, second := snapshot.SnapshotID(201), snapshot.SnapshotID(202)
	runID := snapshot.WorkflowRunID(33)
	productionID := snapshot.DatabaseID(9007199254740997)
	for index, snapshotID := range []snapshot.SnapshotID{first, second} {
		review := &reviews.StoredReview{
			BuildID: 42, TeamName: "main", Conclusion: "changes-required",
			SnapshotID: snapshotID, WorkflowRunID: &runID, ProductionID: &productionID,
			Review: json.RawMessage(sealedReviewRecord(t)), CreatedAt: int64(index + 1),
		}
		if err := store.UpsertReviewProjection(context.Background(), review); err != nil {
			t.Fatal(err)
		}
	}
	if err := fbStore.Save(&feedback.StoredFeedback{
		ReviewSnapshotID: first, ReviewTeamName: "main",
		FindingID: "blocking-1", Verdict: "accurate", Reviewer: "alice",
	}); err != nil {
		t.Fatal(err)
	}
	if err := fbStore.Save(&feedback.StoredFeedback{
		ReviewSnapshotID: second, ReviewTeamName: "main",
		FindingID: "blocking-1", Verdict: "false_positive", Reviewer: "alice",
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
	if got.SnapshotID != first || got.Feedback["blocking-1"].Verdict != "accurate" {
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
	if len(runReviews) != 2 || runReviews[0].SnapshotID == 0 || runReviews[1].SnapshotID == 0 {
		t.Fatalf("run reviews = %#v", runReviews)
	}
	if runReviews[0].Feedback["blocking-1"].Verdict == runReviews[1].Feedback["blocking-1"].Verdict {
		t.Fatalf("same-record snapshot feedback collided: %#v", runReviews)
	}
}

// sealedReviewRecord is what a projected row actually stores: the canonical
// review/v1 record.json bytes the platform sealed.
func sealedReviewRecord(t *testing.T) []byte {
	t.Helper()
	line := 12
	subject := snapshot.SnapshotRef{
		ID: 7, Type: "repository-change/v1",
		Digest: snapshot.Digest("sha256:" + strings.Repeat("a", 64)),
	}
	record, err := contracts.NewRecord(
		snapshot.TypeRef("review/v1"),
		[]contracts.Subject{contracts.SubjectFromInput(
			"change", contracts.SubjectRolePrimary, "candidate", subject,
		)},
		contracts.ReviewBody{
			Conclusion: "changes-required",
			Summary:    "the merge base assertion is client-supplied",
			Findings: []contracts.Finding{
				{
					ID: "blocking-1", Severity: "critical", Blocking: true,
					Category: "correctness", Title: "merge base is trusted from the client",
					Description: "the publisher accepts the caller's merge base",
					Evidence: []contracts.Anchor{{
						Subject: "change",
						Locator: contracts.Locator{
							Kind: "file-lines", Path: "agent/publisher/gateway.go",
							Start: &line, End: &line,
						},
					}},
					Recommendation: "derive it server-side",
				},
				{
					ID: "note-1", Severity: "observation", Category: "maintainability",
					Title: "helper could be shared", Description: "two call sites repeat the shape",
				},
			},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func TestProjectedSnapshotReviewRendersSealedRecordFindings(t *testing.T) {
	h, store, _ := newHandler(t)
	productionID := snapshot.DatabaseID(9)
	if err := store.UpsertReviewProjection(context.Background(), &reviews.StoredReview{
		BuildID: 42, TeamName: "main",
		Conclusion:     "changes-required",
		Summary:        "the merge base assertion is client-supplied",
		SeverityCounts: map[string]int{"critical": 1, "observation": 1},
		SnapshotID:     501, ProductionID: &productionID,
		Review: json.RawMessage(sealedReviewRecord(t)),
	}); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/agent/snapshots/501/projections/review", nil)
	req.URL.RawQuery = "%3Asnapshot_id=501"
	response := httptest.NewRecorder()
	h.GetBySnapshot(response, req)
	if response.Code != http.StatusOK {
		t.Fatalf("code = %d: %s", response.Code, response.Body.String())
	}
	// The v1 envelope is gone from the wire, not merely unused by the web.
	for _, gone := range []string{"score", "max_score", `"pass"`, "agent_model", "duration_seconds", "test_file", "test_name", "test_output"} {
		if strings.Contains(response.Body.String(), gone) {
			t.Errorf("v1 envelope field %s survived: %s", gone, response.Body.String())
		}
	}
	var got reviews.BuildReviewResponse
	if err := json.Unmarshal(response.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}

	if got.Conclusion != "changes-required" {
		t.Errorf("conclusion = %q", got.Conclusion)
	}
	if got.Summary != "the merge base assertion is client-supplied" {
		t.Errorf("summary = %q", got.Summary)
	}
	if got.SeverityCounts["critical"] != 1 || got.SeverityCounts["observation"] != 1 {
		t.Errorf("severity counts = %v", got.SeverityCounts)
	}
	if got.FindingCount != 2 {
		t.Errorf("finding_count = %d, want 2", got.FindingCount)
	}
	if len(got.ProvenIssues) != 1 || len(got.Observations) != 1 {
		t.Fatalf("proven=%d observations=%d, want 1/1: %+v", len(got.ProvenIssues), len(got.Observations), got)
	}
	issue := got.ProvenIssues[0]
	if issue.ID != "blocking-1" || issue.Severity != "critical" || !issue.Blocking ||
		issue.Category != "correctness" || issue.Title != "merge base is trusted from the client" ||
		issue.Description != "the publisher accepts the caller's merge base" {
		t.Errorf("proven issue = %+v", issue)
	}
	if issue.File != "agent/publisher/gateway.go" || issue.Line != 12 {
		t.Errorf("proven issue anchor = %q:%d, want agent/publisher/gateway.go:12", issue.File, issue.Line)
	}
	note := got.Observations[0]
	if note.ID != "note-1" || note.Severity != "observation" || note.Blocking || note.Title != "helper could be shared" {
		t.Errorf("observation = %+v", note)
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

// A record the read-time gate rejects is not downgraded to a looser decode. The
// findings are withheld and the projected severity counts stay the only claim.
func TestGetByBuildWithholdsFindingsFromAnUndecodableRecord(t *testing.T) {
	h, store, _ := newHandler(t)
	productionID := snapshot.DatabaseID(3)
	if err := store.UpsertReviewProjection(context.Background(), &reviews.StoredReview{
		BuildID: 42, TeamName: "main", SnapshotID: 601, ProductionID: &productionID,
		Conclusion: "accept", SeverityCounts: map[string]int{"low": 2},
		Review: json.RawMessage(`{"record_version":"1.0.0","type":"review/v1","body":{}}`),
	}); err != nil {
		t.Fatal(err)
	}

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
	if len(got[0].ProvenIssues) != 0 || len(got[0].Observations) != 0 {
		t.Errorf("ungated findings rendered: %+v", got[0])
	}
	if got[0].FindingCount != 2 {
		t.Errorf("finding_count = %d, want the projected severity total 2", got[0].FindingCount)
	}
}

func TestGetByBuildRendersTheSealedRecord(t *testing.T) {
	h, store, _ := newHandler(t)
	productionID := snapshot.DatabaseID(4)
	if err := store.UpsertReviewProjection(context.Background(), &reviews.StoredReview{
		BuildID: 42, BuildName: "3", TeamName: "main", PipelineName: "concourse-self",
		JobName: "agent-review", WorkflowName: "code-review",
		SnapshotID: 602, ProductionID: &productionID,
		Conclusion:     "changes-required",
		SeverityCounts: map[string]int{"critical": 1, "observation": 1},
		Review:         json.RawMessage(sealedReviewRecord(t)),
	}); err != nil {
		t.Fatal(err)
	}

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
	if got[0].TeamName != "main" || got[0].WorkflowName != "code-review" || got[0].Conclusion != "changes-required" {
		t.Errorf("summary fields wrong: %+v", got[0])
	}
	if len(got[0].ProvenIssues) != 1 || len(got[0].Observations) != 1 {
		t.Errorf("findings not unpacked: %+v", got[0])
	}
	if got[0].FindingCount != 2 {
		t.Errorf("finding_count = %d, want 2", got[0].FindingCount)
	}
	if len(got[0].Feedback) != 0 || got[0].EvaluatedCount != 0 {
		t.Errorf("unevaluated review must join no feedback, got %+v", got[0].Feedback)
	}
}

func TestListByTeam(t *testing.T) {
	h, store, _ := newHandler(t)
	for _, rec := range []*reviews.StoredReview{
		projected(1001, reviews.StoredReview{BuildID: 1, TeamName: "main", PipelineName: "p1"}),
		projected(1002, reviews.StoredReview{BuildID: 2, TeamName: "main", PipelineName: "p2"}),
		projected(1003, reviews.StoredReview{BuildID: 3, TeamName: "other", PipelineName: "p1"}),
	} {
		if err := store.UpsertReviewProjection(context.Background(), rec); err != nil {
			t.Fatal(err)
		}
	}

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
