package feedback_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/concourse/concourse/agent/api/feedback"
	"github.com/concourse/concourse/agent/snapshot"
)

// setupServer creates an httptest.Server with a real ServeMux wiring the one
// surviving feedback endpoint. This validates the full HTTP round-trip
// (routing, serialization, status codes) rather than testing handler methods
// directly. The GET/summary/classify routes were deleted with the rest of the
// v1 HTTP surface — feedback is read back through the review projection.
func setupServer() (*httptest.Server, *feedback.MemoryStore) {
	store := feedback.NewMemoryStore()
	handler := feedback.NewHandler(store, feedback.WithSnapshotTeam("main"))

	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/agent/feedback", handler.SubmitFeedback)

	return httptest.NewServer(mux), store
}

func submitFeedback(t *testing.T, serverURL string, req feedback.FeedbackRequest) *http.Response {
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

func TestRoundTripSubmitStoresEveryFinding(t *testing.T) {
	server, store := setupServer()
	defer server.Close()

	reviewID := snapshot.SnapshotID(4242)
	records := []feedback.FeedbackRequest{
		{ReviewSnapshotID: reviewID, FindingID: "ISS-001", Verdict: "accurate", Reviewer: "alice"},
		{ReviewSnapshotID: reviewID, FindingID: "ISS-002", Verdict: "false_positive", Reviewer: "alice"},
		{ReviewSnapshotID: reviewID, FindingID: "ISS-003", Verdict: "accurate", Reviewer: "bob"},
	}
	for _, rec := range records {
		resp := submitFeedback(t, server.URL, rec)
		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("expected 201, got %d", resp.StatusCode)
		}
		resp.Body.Close()
	}

	stored, err := store.GetByReviewSnapshot(reviewID, "main")
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if len(stored) != 3 {
		t.Fatalf("expected 3 stored records, got %d", len(stored))
	}
}

func TestRoundTripUpsertBehavior(t *testing.T) {
	server, store := setupServer()
	defer server.Close()

	reviewID := snapshot.SnapshotID(4343)

	// Same reviewer + finding submitted twice with different verdicts.
	for _, verdict := range []string{"false_positive", "accurate"} {
		resp := submitFeedback(t, server.URL, feedback.FeedbackRequest{
			ReviewSnapshotID: reviewID, FindingID: "ISS-010",
			Verdict: verdict, Reviewer: "alice",
		})
		resp.Body.Close()
		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("submit %q: expected 201, got %d", verdict, resp.StatusCode)
		}
	}

	stored, err := store.GetByReviewSnapshot(reviewID, "main")
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if len(stored) != 1 {
		t.Fatalf("expected 1 record (upsert), got %d", len(stored))
	}
	if stored[0].Verdict != "accurate" {
		t.Fatalf("expected latest verdict 'accurate', got %q", stored[0].Verdict)
	}
}
