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

func TestClassifyEndpointWithCustomClassifier(t *testing.T) {
	store := agentfeedback.NewMemoryStore()
	handler := agentfeedback.NewHandler(store, agentfeedback.WithClassifier(func(text string) (string, float64) {
		return "noisy", 0.99
	}))

	body := agentfeedback.ClassifyRequest{Text: "anything"}
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
	if resp.Verdict != "noisy" {
		t.Fatalf("expected noisy from custom classifier, got %s", resp.Verdict)
	}
	if resp.Confidence != 0.99 {
		t.Fatalf("expected confidence 0.99, got %f", resp.Confidence)
	}
}

// TestClassifyAllVerdictTypes verifies the default classifier handles all
// verdict types with the comprehensive keyword set matching ci-agent/feedback/classifier.go.
func TestClassifyAllVerdictTypes(t *testing.T) {
	store := agentfeedback.NewMemoryStore()
	handler := agentfeedback.NewHandler(store)

	tests := []struct {
		input           string
		expectedVerdict string
		minConfidence   float64
	}{
		// Accurate signals
		{"good catch, this is a real bug", "accurate", 0.85},
		{"this is a real issue", "accurate", 0.85},
		{"correct finding", "accurate", 0.85},
		{"accurate assessment", "accurate", 0.85},
		{"valid finding here", "accurate", 0.85},
		{"I agree with this", "accurate", 0.85},

		// False positive signals
		{"this is a false positive", "false_positive", 0.85},
		{"not a bug at all", "false_positive", 0.85},
		{"not an issue", "false_positive", 0.85},
		{"doesn't apply here", "false_positive", 0.85},
		{"expected behavior", "false_positive", 0.85},
		{"by design", "false_positive", 0.85},
		{"this is intended", "false_positive", 0.85},

		// Noisy signals
		{"too noisy", "noisy", 0.80},
		{"not important enough", "noisy", 0.80},
		{"too minor to care", "noisy", 0.80},
		{"trivial issue", "noisy", 0.80},
		{"low priority item", "noisy", 0.80},
		{"don't care about this", "noisy", 0.80},
		{"not worth fixing", "noisy", 0.80},

		// Overly strict signals
		{"just a style issue", "overly_strict", 0.80},
		{"matter of preference", "overly_strict", 0.80},
		{"opinionated take", "overly_strict", 0.80},
		{"too subjective", "overly_strict", 0.80},
		{"just a nitpick", "overly_strict", 0.80},
		{"too strict on this one", "overly_strict", 0.80},
		{"overly strict rule", "overly_strict", 0.80},

		// Partially correct signals
		{"partially right but misses the point", "partially_correct", 0.75},
		{"partially right but not fully", "partially_correct", 0.75},
		{"right area but wrong diagnosis", "partially_correct", 0.75},
		{"close but not quite", "partially_correct", 0.75},
		{"half right at best", "partially_correct", 0.75},

		// Missed context signals
		{"missing context about the codebase", "missed_context", 0.75},
		{"lacks context for this area", "missed_context", 0.75},
		{"needs more context to evaluate", "missed_context", 0.75},
		{"agent doesn't know about this", "missed_context", 0.75},
		{"not aware of the history", "missed_context", 0.75},
		{"can't tell without more info", "missed_context", 0.75},

		// Negation patterns
		{"not a false positive, this is real", "accurate", 0.80},
		{"isn't a false positive", "accurate", 0.80},

		// Ambiguous/fallback
		{"hmm I'm not sure about this", "accurate", 0.0},
	}

	for _, tc := range tests {
		body := agentfeedback.ClassifyRequest{Text: tc.input}
		data, _ := json.Marshal(body)
		req := httptest.NewRequest(http.MethodPost, "/api/v1/agent/feedback/classify", bytes.NewReader(data))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()

		handler.ClassifyVerdict(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("input %q: expected 200, got %d", tc.input, w.Code)
		}

		var resp agentfeedback.ClassifyResponse
		json.Unmarshal(w.Body.Bytes(), &resp)
		if resp.Verdict != tc.expectedVerdict {
			t.Errorf("input %q: expected verdict %q, got %q", tc.input, tc.expectedVerdict, resp.Verdict)
		}
		if resp.Confidence < tc.minConfidence {
			t.Errorf("input %q: expected confidence >= %f, got %f", tc.input, tc.minConfidence, resp.Confidence)
		}
	}
}
