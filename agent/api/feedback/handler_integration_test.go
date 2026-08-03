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

// setupRouter creates a real ServeMux wiring the one
// surviving feedback endpoint. This validates HTTP dispatch, serialization,
// and status codes rather than testing handler methods directly. The
// GET/summary/classify routes were deleted with the rest of the v1 HTTP surface
// — feedback is read back through the review projection.
func setupRouter() (http.Handler, *feedback.MemoryStore) {
	store := feedback.NewMemoryStore()
	handler := feedback.NewHandler(store, feedback.WithSnapshotTeam("research"))

	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/teams/research/agent/feedback", handler.SubmitFeedback)

	return mux, store
}

func submitFeedback(t *testing.T, router http.Handler, req feedback.FeedbackRequest) *httptest.ResponseRecorder {
	t.Helper()
	data, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	httpRequest := httptest.NewRequest(http.MethodPost, "/api/v1/teams/research/agent/feedback", bytes.NewReader(data))
	httpRequest.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httpRequest)
	return response
}

func TestRoundTripSubmitStoresEveryFinding(t *testing.T) {
	router, store := setupRouter()

	reviewID := snapshot.SnapshotID(4242)
	records := []feedback.FeedbackRequest{
		{ReviewSnapshotID: reviewID, FindingID: "ISS-001", Verdict: "accurate", Reviewer: "alice"},
		{ReviewSnapshotID: reviewID, FindingID: "ISS-002", Verdict: "false_positive", Reviewer: "alice"},
		{ReviewSnapshotID: reviewID, FindingID: "ISS-003", Verdict: "accurate", Reviewer: "bob"},
	}
	for _, rec := range records {
		resp := submitFeedback(t, router, rec)
		if resp.Code != http.StatusCreated {
			t.Fatalf("expected 201, got %d", resp.Code)
		}
	}

	stored, err := store.GetByReviewSnapshot(reviewID, "research")
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if len(stored) != 3 {
		t.Fatalf("expected 3 stored records, got %d", len(stored))
	}
}

func TestRoundTripUpsertBehavior(t *testing.T) {
	router, store := setupRouter()

	reviewID := snapshot.SnapshotID(4343)

	// Same reviewer + finding submitted twice with different verdicts.
	for _, verdict := range []string{"false_positive", "accurate"} {
		resp := submitFeedback(t, router, feedback.FeedbackRequest{
			ReviewSnapshotID: reviewID, FindingID: "ISS-010",
			Verdict: verdict, Reviewer: "alice",
		})
		if resp.Code != http.StatusCreated {
			t.Fatalf("submit %q: expected 201, got %d", verdict, resp.Code)
		}
	}

	stored, err := store.GetByReviewSnapshot(reviewID, "research")
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
