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

func TestSubmitFeedback(t *testing.T) {
	store := feedback.NewMemoryStore()
	handler := feedback.NewHandler(store)

	body := feedback.FeedbackRequest{
		ReviewRef: feedback.ReviewRef{
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

func TestSnapshotFeedbackIdentityDoesNotCollideForTwoReviewsOfOneCommit(t *testing.T) {
	store := feedback.NewMemoryStore()
	handler := feedback.NewHandler(store)
	first, second := snapshot.SnapshotID(101), snapshot.SnapshotID(102)

	for _, snapshotID := range []snapshot.SnapshotID{first, second} {
		body := feedback.FeedbackRequest{
			ReviewSnapshotID: &snapshotID,
			ReviewRef:        feedback.ReviewRef{Repo: "org/repo", Commit: "same-commit"},
			FindingID:        "ISS-001", Verdict: "accurate", Reviewer: "alice",
		}
		data, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest(http.MethodPost, "/api/v1/agent/feedback", bytes.NewReader(data))
		w := httptest.NewRecorder()
		handler.SubmitFeedback(w, req)
		if w.Code != http.StatusCreated {
			t.Fatalf("snapshot %s: code %d: %s", snapshotID, w.Code, w.Body.String())
		}
	}

	for _, snapshotID := range []snapshot.SnapshotID{first, second} {
		rows, err := store.GetByReviewSnapshot(snapshotID, "")
		if err != nil {
			t.Fatal(err)
		}
		if len(rows) != 1 || rows[0].ReviewSnapshotID == nil || *rows[0].ReviewSnapshotID != snapshotID {
			t.Fatalf("snapshot %s rows = %#v", snapshotID, rows)
		}
	}
}

func TestSnapshotFeedbackDoesNotRequireLegacyRepoCommitIdentity(t *testing.T) {
	store := feedback.NewMemoryStore()
	handler := feedback.NewHandler(store)
	snapshotID := snapshot.SnapshotID(103)
	body := feedback.FeedbackRequest{
		ReviewSnapshotID: &snapshotID,
		FindingID:        "ISS-002", Verdict: "noisy", Reviewer: "bob",
	}
	data, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	handler.SubmitFeedback(w, httptest.NewRequest(http.MethodPost, "/api/v1/agent/feedback", bytes.NewReader(data)))
	if w.Code != http.StatusCreated {
		t.Fatalf("code = %d: %s", w.Code, w.Body.String())
	}
}

func TestSnapshotFeedbackUsesAuthenticatedReviewerAndTeamScope(t *testing.T) {
	store := feedback.NewMemoryStore()
	handler := feedback.NewHandler(
		store,
		feedback.WithSnapshotTeam("main"),
		feedback.WithIdentity(func(*http.Request) string { return "attacker" }),
	)
	snapshotID := snapshot.SnapshotID(104)
	body := feedback.FeedbackRequest{
		ReviewSnapshotID: &snapshotID,
		FindingID:        "ISS-003",
		Verdict:          "accurate",
		Reviewer:         "victim",
	}
	data, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	handler.SubmitFeedback(w, httptest.NewRequest(http.MethodPost, "/api/v1/agent/feedback", bytes.NewReader(data)))
	if w.Code != http.StatusCreated {
		t.Fatalf("code = %d: %s", w.Code, w.Body.String())
	}
	rows, err := store.GetByReviewSnapshot(snapshotID, "main")
	if err != nil || len(rows) != 1 {
		t.Fatalf("rows = %#v, err = %v", rows, err)
	}
	if rows[0].Reviewer != "attacker" {
		t.Fatalf("reviewer = %q, want authenticated identity", rows[0].Reviewer)
	}
	otherRows, err := store.GetByReviewSnapshot(snapshotID, "other")
	if err != nil || len(otherRows) != 0 {
		t.Fatalf("cross-team rows = %#v, err = %v", otherRows, err)
	}
}

func TestSubmitFeedbackInvalidVerdict(t *testing.T) {
	store := feedback.NewMemoryStore()
	handler := feedback.NewHandler(store)

	body := feedback.FeedbackRequest{
		ReviewRef: feedback.ReviewRef{
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
	store := feedback.NewMemoryStore()
	handler := feedback.NewHandler(store)

	body := feedback.FeedbackRequest{
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
