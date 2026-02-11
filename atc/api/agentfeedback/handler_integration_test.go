package agentfeedback_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/concourse/concourse/atc/api/agentfeedback"
)

// setupServer creates an httptest.Server with a real ServeMux wiring all
// feedback endpoints. This validates the full HTTP round-trip (routing,
// serialization, status codes) rather than testing handler methods directly.
func setupServer() (*httptest.Server, *agentfeedback.MemoryStore) {
	store := agentfeedback.NewMemoryStore()
	handler := agentfeedback.NewHandler(store)

	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/agent/feedback", handler.SubmitFeedback)
	mux.HandleFunc("GET /api/v1/agent/feedback", handler.GetFeedback)
	mux.HandleFunc("GET /api/v1/agent/feedback/summary", handler.GetSummary)
	mux.HandleFunc("POST /api/v1/agent/feedback/classify", handler.ClassifyVerdict)

	return httptest.NewServer(mux), store
}

func submitFeedback(t *testing.T, serverURL string, req agentfeedback.FeedbackRequest) *http.Response {
	t.Helper()
	data, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	resp, err := http.Post(serverURL+"/api/v1/agent/feedback", "application/json", bytes.NewReader(data))
	if err != nil {
		t.Fatalf("POST feedback: %v", err)
	}
	return resp
}

func TestRoundTripSubmitGetSummary(t *testing.T) {
	server, _ := setupServer()
	defer server.Close()

	// Submit 3 feedback records.
	records := []agentfeedback.FeedbackRequest{
		{
			ReviewRef: agentfeedback.ReviewRef{Repo: "repo-a", Commit: "abc"},
			FindingID: "ISS-001", Verdict: "accurate", Reviewer: "alice",
		},
		{
			ReviewRef: agentfeedback.ReviewRef{Repo: "repo-a", Commit: "abc"},
			FindingID: "ISS-002", Verdict: "false_positive", Reviewer: "alice",
		},
		{
			ReviewRef: agentfeedback.ReviewRef{Repo: "repo-a", Commit: "abc"},
			FindingID: "ISS-003", Verdict: "accurate", Reviewer: "bob",
		},
	}

	for _, rec := range records {
		resp := submitFeedback(t, server.URL, rec)
		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("expected 201, got %d", resp.StatusCode)
		}
		resp.Body.Close()
	}

	// GET all 3 back.
	resp, err := http.Get(server.URL + "/api/v1/agent/feedback?repo=repo-a&commit=abc")
	if err != nil {
		t.Fatalf("GET feedback: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var results []agentfeedback.StoredFeedback
	if err := json.NewDecoder(resp.Body).Decode(&results); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(results) != 3 {
		t.Fatalf("expected 3 results, got %d", len(results))
	}

	// GET summary with correct rates.
	resp2, err := http.Get(server.URL + "/api/v1/agent/feedback/summary")
	if err != nil {
		t.Fatalf("GET summary: %v", err)
	}
	defer resp2.Body.Close()

	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp2.StatusCode)
	}

	var summary agentfeedback.SummaryResponse
	if err := json.NewDecoder(resp2.Body).Decode(&summary); err != nil {
		t.Fatalf("decode summary: %v", err)
	}
	if summary.Total != 3 {
		t.Fatalf("expected total 3, got %d", summary.Total)
	}
	// 2/3 accurate.
	expectedAccuracy := 2.0 / 3.0
	if summary.AccuracyRate < expectedAccuracy-0.01 || summary.AccuracyRate > expectedAccuracy+0.01 {
		t.Fatalf("expected accuracy ~%.2f, got %f", expectedAccuracy, summary.AccuracyRate)
	}
	// 1/3 false positive.
	expectedFP := 1.0 / 3.0
	if summary.FPRate < expectedFP-0.01 || summary.FPRate > expectedFP+0.01 {
		t.Fatalf("expected FP rate ~%.2f, got %f", expectedFP, summary.FPRate)
	}
}

func TestRoundTripUpsertBehavior(t *testing.T) {
	server, _ := setupServer()
	defer server.Close()

	// Submit same reviewer+finding twice with different verdicts.
	first := agentfeedback.FeedbackRequest{
		ReviewRef: agentfeedback.ReviewRef{Repo: "repo-b", Commit: "def"},
		FindingID: "ISS-010", Verdict: "false_positive", Reviewer: "alice",
	}
	resp := submitFeedback(t, server.URL, first)
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("first submit: expected 201, got %d", resp.StatusCode)
	}

	// Update verdict.
	second := agentfeedback.FeedbackRequest{
		ReviewRef: agentfeedback.ReviewRef{Repo: "repo-b", Commit: "def"},
		FindingID: "ISS-010", Verdict: "accurate", Reviewer: "alice",
	}
	resp = submitFeedback(t, server.URL, second)
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("second submit: expected 201, got %d", resp.StatusCode)
	}

	// GET should return only 1 record with the latest verdict.
	resp2, err := http.Get(server.URL + "/api/v1/agent/feedback?repo=repo-b&commit=def")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp2.Body.Close()

	var results []agentfeedback.StoredFeedback
	if err := json.NewDecoder(resp2.Body).Decode(&results); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result (upsert), got %d", len(results))
	}
	if results[0].Verdict != "accurate" {
		t.Fatalf("expected latest verdict 'accurate', got %q", results[0].Verdict)
	}
}

func TestRoundTripRepoFilterOnSummary(t *testing.T) {
	server, _ := setupServer()
	defer server.Close()

	// Submit records for two different repos.
	repoARecords := []agentfeedback.FeedbackRequest{
		{
			ReviewRef: agentfeedback.ReviewRef{Repo: "repo-alpha", Commit: "a1"},
			FindingID: "ISS-001", Verdict: "accurate", Reviewer: "alice",
		},
		{
			ReviewRef: agentfeedback.ReviewRef{Repo: "repo-alpha", Commit: "a1"},
			FindingID: "ISS-002", Verdict: "accurate", Reviewer: "alice",
		},
	}
	repoBRecords := []agentfeedback.FeedbackRequest{
		{
			ReviewRef: agentfeedback.ReviewRef{Repo: "repo-beta", Commit: "b1"},
			FindingID: "ISS-001", Verdict: "false_positive", Reviewer: "bob",
		},
	}

	for _, rec := range append(repoARecords, repoBRecords...) {
		resp := submitFeedback(t, server.URL, rec)
		resp.Body.Close()
	}

	// Summary filtered by repo-alpha should only count 2 records.
	resp, err := http.Get(server.URL + "/api/v1/agent/feedback/summary?repo=repo-alpha")
	if err != nil {
		t.Fatalf("GET summary: %v", err)
	}
	defer resp.Body.Close()

	var summary agentfeedback.SummaryResponse
	if err := json.NewDecoder(resp.Body).Decode(&summary); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if summary.Total != 2 {
		t.Fatalf("expected 2 for repo-alpha filter, got %d", summary.Total)
	}
	if summary.AccuracyRate != 1.0 {
		t.Fatalf("expected accuracy 1.0 for repo-alpha, got %f", summary.AccuracyRate)
	}

	// Summary filtered by repo-beta should only count 1 record.
	resp2, err := http.Get(server.URL + "/api/v1/agent/feedback/summary?repo=repo-beta")
	if err != nil {
		t.Fatalf("GET summary: %v", err)
	}
	defer resp2.Body.Close()

	var summaryB agentfeedback.SummaryResponse
	if err := json.NewDecoder(resp2.Body).Decode(&summaryB); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if summaryB.Total != 1 {
		t.Fatalf("expected 1 for repo-beta filter, got %d", summaryB.Total)
	}
	if summaryB.FPRate != 1.0 {
		t.Fatalf("expected FP rate 1.0 for repo-beta, got %f", summaryB.FPRate)
	}
}
