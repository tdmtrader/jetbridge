package agentfeedback_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/concourse/concourse/atc/api/agentfeedback"
)

func TestSubmitFeedback(t *testing.T) {
	store := agentfeedback.NewMemoryStore()
	handler := agentfeedback.NewHandler(store)

	body := agentfeedback.FeedbackRequest{
		ReviewRef: agentfeedback.ReviewRef{
			Repo:   "https://github.com/org/repo.git",
			Commit: "abc123",
		},
		FindingID:       "ISS-001",
		FindingType:     "proven_issue",
		FindingSnapshot: json.RawMessage(`{"severity":"high"}`),
		Verdict:         "accurate",
		Confidence:      0.9,
		Notes:           "real bug",
		Reviewer:        "tdm",
		Source:          "interactive",
	}

	data, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/agent/feedback", bytes.NewReader(data))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	handler.SubmitFeedback(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
	}
}

func TestSubmitFeedbackInvalidVerdict(t *testing.T) {
	store := agentfeedback.NewMemoryStore()
	handler := agentfeedback.NewHandler(store)

	body := agentfeedback.FeedbackRequest{
		ReviewRef: agentfeedback.ReviewRef{
			Repo:   "https://github.com/org/repo.git",
			Commit: "abc123",
		},
		FindingID:       "ISS-001",
		FindingType:     "proven_issue",
		FindingSnapshot: json.RawMessage(`{}`),
		Verdict:         "invalid_verdict",
		Reviewer:        "tdm",
		Source:          "interactive",
	}

	data, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/agent/feedback", bytes.NewReader(data))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	handler.SubmitFeedback(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
}

func TestSubmitFeedbackMissingFields(t *testing.T) {
	store := agentfeedback.NewMemoryStore()
	handler := agentfeedback.NewHandler(store)

	body := agentfeedback.FeedbackRequest{
		Verdict: "accurate",
	}

	data, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/agent/feedback", bytes.NewReader(data))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	handler.SubmitFeedback(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
}

func TestGetFeedback(t *testing.T) {
	store := agentfeedback.NewMemoryStore()
	handler := agentfeedback.NewHandler(store)

	// Submit a record first.
	rec := &agentfeedback.StoredFeedback{
		ReviewRef: agentfeedback.ReviewRef{
			Repo:   "https://github.com/org/repo.git",
			Commit: "abc123",
		},
		FindingID:       "ISS-001",
		FindingType:     "proven_issue",
		FindingSnapshot: json.RawMessage(`{"severity":"high"}`),
		Verdict:         "accurate",
		Confidence:      0.9,
		Reviewer:        "tdm",
		Source:          "interactive",
	}
	store.Save(rec)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/agent/feedback?repo=https://github.com/org/repo.git&commit=abc123", nil)
	w := httptest.NewRecorder()

	handler.GetFeedback(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var results []agentfeedback.StoredFeedback
	if err := json.Unmarshal(w.Body.Bytes(), &results); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].FindingID != "ISS-001" {
		t.Fatalf("expected finding ISS-001, got %s", results[0].FindingID)
	}
}

func TestGetFeedbackEmpty(t *testing.T) {
	store := agentfeedback.NewMemoryStore()
	handler := agentfeedback.NewHandler(store)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/agent/feedback?repo=none&commit=none", nil)
	w := httptest.NewRecorder()

	handler.GetFeedback(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	var results []agentfeedback.StoredFeedback
	json.Unmarshal(w.Body.Bytes(), &results)
	if len(results) != 0 {
		t.Fatalf("expected empty, got %d results", len(results))
	}
}

func TestGetSummary(t *testing.T) {
	store := agentfeedback.NewMemoryStore()
	handler := agentfeedback.NewHandler(store)

	records := []*agentfeedback.StoredFeedback{
		{ReviewRef: agentfeedback.ReviewRef{Repo: "r", Commit: "c"}, FindingID: "1", Verdict: "accurate", Reviewer: "a"},
		{ReviewRef: agentfeedback.ReviewRef{Repo: "r", Commit: "c"}, FindingID: "2", Verdict: "accurate", Reviewer: "a"},
		{ReviewRef: agentfeedback.ReviewRef{Repo: "r", Commit: "c"}, FindingID: "3", Verdict: "false_positive", Reviewer: "a"},
	}
	for _, r := range records {
		store.Save(r)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/agent/feedback/summary", nil)
	w := httptest.NewRecorder()

	handler.GetSummary(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var summary agentfeedback.SummaryResponse
	if err := json.Unmarshal(w.Body.Bytes(), &summary); err != nil {
		t.Fatalf("failed to decode: %v", err)
	}
	if summary.Total != 3 {
		t.Fatalf("expected total 3, got %d", summary.Total)
	}
	if summary.AccuracyRate < 0.66 || summary.AccuracyRate > 0.67 {
		t.Fatalf("expected accuracy ~0.67, got %f", summary.AccuracyRate)
	}
}

func TestClassifyEndpoint(t *testing.T) {
	store := agentfeedback.NewMemoryStore()
	handler := agentfeedback.NewHandler(store)

	body := agentfeedback.ClassifyRequest{Text: "good catch, real bug"}
	data, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/agent/feedback/classify", bytes.NewReader(data))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	handler.ClassifyVerdict(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp agentfeedback.ClassifyResponse
	json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.Verdict != "accurate" {
		t.Fatalf("expected accurate, got %s", resp.Verdict)
	}
	if resp.Confidence < 0.7 {
		t.Fatalf("expected confidence > 0.7, got %f", resp.Confidence)
	}
}
