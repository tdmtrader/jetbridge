package agentfeedback_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/concourse/concourse/atc/api/agentfeedback"
)

// TestFeedbackPipelineFromReviewOutput simulates the full data flow:
// ci-agent produces review.json with proven issues → each issue is submitted
// as feedback to the feedback API → summary reflects the verdicts.
//
// This validates the feedback loop between the ci-agent output and the ATC
// feedback API, even though the two modules are separate Go modules.
func TestFeedbackPipelineFromReviewOutput(t *testing.T) {
	server, _ := setupServer()
	defer server.Close()

	// Simulate ci-agent review output: a review.json with proven issues.
	// In production, the ci-agent writes this file; here we construct the
	// equivalent data structure inline.
	type provenIssue struct {
		ID       string `json:"id"`
		Title    string `json:"title"`
		Severity string `json:"severity"`
		Category string `json:"category"`
		File     string `json:"file"`
	}

	reviewOutput := struct {
		SchemaVersion string        `json:"schema_version"`
		ProvenIssues  []provenIssue `json:"proven_issues"`
		Summary       string        `json:"summary"`
	}{
		SchemaVersion: "1.0.0",
		ProvenIssues: []provenIssue{
			{ID: "ISS-001", Title: "divide by zero panic", Severity: "high", Category: "correctness", File: "math.go"},
			{ID: "ISS-002", Title: "SQL injection in query builder", Severity: "critical", Category: "security", File: "db.go"},
			{ID: "ISS-003", Title: "unused import", Severity: "low", Category: "maintainability", File: "util.go"},
		},
		Summary: "Found 3 issues in review",
	}

	// Step 1: Marshal review output (simulating results.json write).
	reviewData, err := json.Marshal(reviewOutput)
	if err != nil {
		t.Fatalf("marshal review output: %v", err)
	}

	// Verify it's valid JSON (simulating file read-back).
	var readback map[string]interface{}
	if err := json.Unmarshal(reviewData, &readback); err != nil {
		t.Fatalf("readback unmarshal: %v", err)
	}

	// Step 2: Submit each proven issue as feedback to the API.
	// This simulates the feedback loop where a human reviewer or automated
	// system classifies each proven issue.
	feedbackEntries := []agentfeedback.FeedbackRequest{
		{
			ReviewRef:       agentfeedback.ReviewRef{Repo: "github.com/org/repo", Commit: "abc123"},
			FindingID:       "ISS-001",
			FindingType:     "proven_issue",
			FindingSnapshot: json.RawMessage(`{"severity":"high","category":"correctness"}`),
			Verdict:         "accurate",
			Confidence:      0.95,
			Notes:           "confirmed divide by zero bug",
			Reviewer:        "engineer-1",
			Source:          "interactive",
		},
		{
			ReviewRef:       agentfeedback.ReviewRef{Repo: "github.com/org/repo", Commit: "abc123"},
			FindingID:       "ISS-002",
			FindingType:     "proven_issue",
			FindingSnapshot: json.RawMessage(`{"severity":"critical","category":"security"}`),
			Verdict:         "accurate",
			Confidence:      0.99,
			Notes:           "real SQL injection",
			Reviewer:        "engineer-1",
			Source:          "interactive",
		},
		{
			ReviewRef:       agentfeedback.ReviewRef{Repo: "github.com/org/repo", Commit: "abc123"},
			FindingID:       "ISS-003",
			FindingType:     "proven_issue",
			FindingSnapshot: json.RawMessage(`{"severity":"low","category":"maintainability"}`),
			Verdict:         "false_positive",
			Confidence:      0.85,
			Notes:           "import is used via init()",
			Reviewer:        "engineer-1",
			Source:          "interactive",
		},
	}

	for _, fb := range feedbackEntries {
		resp := submitFeedback(t, server.URL, fb)
		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("submit feedback for %s: expected 201, got %d", fb.FindingID, resp.StatusCode)
		}
		resp.Body.Close()
	}

	// Step 3: Verify feedback was stored by querying the API.
	resp, err := http.Get(server.URL + "/api/v1/agent/feedback?repo=github.com/org/repo&commit=abc123")
	if err != nil {
		t.Fatalf("GET feedback: %v", err)
	}
	defer resp.Body.Close()

	var stored []agentfeedback.StoredFeedback
	if err := json.NewDecoder(resp.Body).Decode(&stored); err != nil {
		t.Fatalf("decode stored feedback: %v", err)
	}
	if len(stored) != 3 {
		t.Fatalf("expected 3 stored feedback records, got %d", len(stored))
	}

	// Verify each record was stored correctly.
	verdictMap := make(map[string]string)
	for _, s := range stored {
		verdictMap[s.FindingID] = s.Verdict
	}
	if verdictMap["ISS-001"] != "accurate" {
		t.Errorf("ISS-001 verdict: expected accurate, got %s", verdictMap["ISS-001"])
	}
	if verdictMap["ISS-002"] != "accurate" {
		t.Errorf("ISS-002 verdict: expected accurate, got %s", verdictMap["ISS-002"])
	}
	if verdictMap["ISS-003"] != "false_positive" {
		t.Errorf("ISS-003 verdict: expected false_positive, got %s", verdictMap["ISS-003"])
	}

	// Step 4: Verify summary reflects the feedback loop.
	resp2, err := http.Get(server.URL + "/api/v1/agent/feedback/summary?repo=github.com/org/repo")
	if err != nil {
		t.Fatalf("GET summary: %v", err)
	}
	defer resp2.Body.Close()

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
		t.Errorf("expected accuracy ~%.2f, got %f", expectedAccuracy, summary.AccuracyRate)
	}
	// 1/3 false positive.
	expectedFP := 1.0 / 3.0
	if summary.FPRate < expectedFP-0.01 || summary.FPRate > expectedFP+0.01 {
		t.Errorf("expected FP rate ~%.2f, got %f", expectedFP, summary.FPRate)
	}

	// Step 5: Verify classify endpoint works for the feedback text.
	classifyBody := agentfeedback.ClassifyRequest{Text: "this is a real bug, good catch"}
	classifyData, _ := json.Marshal(classifyBody)
	resp3, err := http.Post(server.URL+"/api/v1/agent/feedback/classify", "application/json", bytes.NewReader(classifyData))
	if err != nil {
		t.Fatalf("POST classify: %v", err)
	}
	defer resp3.Body.Close()

	var classifyResp agentfeedback.ClassifyResponse
	if err := json.NewDecoder(resp3.Body).Decode(&classifyResp); err != nil {
		t.Fatalf("decode classify: %v", err)
	}
	if classifyResp.Verdict != "accurate" {
		t.Errorf("classify verdict: expected accurate, got %s", classifyResp.Verdict)
	}
	if classifyResp.Confidence < 0.7 {
		t.Errorf("classify confidence: expected >= 0.7, got %f", classifyResp.Confidence)
	}
}
