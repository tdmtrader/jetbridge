package feedback_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/concourse/concourse/agent/api/feedback"
	"github.com/concourse/concourse/agent/snapshot"
)

func submit(t *testing.T, handler *feedback.Handler, body feedback.FeedbackRequest) *httptest.ResponseRecorder {
	t.Helper()
	data, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/agent/feedback", bytes.NewReader(data))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	handler.SubmitFeedback(w, req)
	return w
}

func TestSubmitFeedback(t *testing.T) {
	store := feedback.NewMemoryStore()
	handler := feedback.NewHandler(store)

	w := submit(t, handler, feedback.FeedbackRequest{
		ReviewSnapshotID: snapshot.SnapshotID(77),
		FindingID:        "ISS-001",
		FindingType:      "proven_issue",
		FindingSnapshot:  json.RawMessage(`{"severity":"high"}`),
		Verdict:          "accurate",
		Confidence:       0.9,
		Notes:            "real bug",
		Reviewer:         "tdm",
		Source:           "interactive",
	})

	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
	}
}

func TestSnapshotFeedbackIdentityDoesNotCollideForTwoReviewsOfOneCommit(t *testing.T) {
	store := feedback.NewMemoryStore()
	handler := feedback.NewHandler(store)
	first, second := snapshot.SnapshotID(101), snapshot.SnapshotID(102)

	for _, snapshotID := range []snapshot.SnapshotID{first, second} {
		w := submit(t, handler, feedback.FeedbackRequest{
			ReviewSnapshotID: snapshotID,
			FindingID:        "ISS-001", Verdict: "accurate", Reviewer: "alice",
		})
		if w.Code != http.StatusCreated {
			t.Fatalf("snapshot %s: code %d: %s", snapshotID, w.Code, w.Body.String())
		}
	}

	for _, snapshotID := range []snapshot.SnapshotID{first, second} {
		rows, err := store.GetByReviewSnapshot(snapshotID, "")
		if err != nil {
			t.Fatal(err)
		}
		if len(rows) != 1 || rows[0].ReviewSnapshotID != snapshotID {
			t.Fatalf("snapshot %s rows = %#v", snapshotID, rows)
		}
	}
}

// The review snapshot is the ONLY identity a feedback record has. A body that
// names none — as the deleted repo/commit review_ref did — cannot be resolved to
// a review at all, so it must be rejected rather than stored under an identity
// no read path can select on.
func TestSubmitFeedbackRejectsABodyWithNoReviewSnapshot(t *testing.T) {
	store := feedback.NewMemoryStore()
	handler := feedback.NewHandler(store)

	for name, tc := range map[string]struct {
		body []byte
		// A body that omits the field is rejected by validate(), which names it.
		// A body that supplies a malformed one is rejected by the ID's own strict
		// decoder, which reports the type rather than the field.
		wants string
	}{
		"absent":     {[]byte(`{"finding_id":"ISS-001","verdict":"accurate","reviewer":"tdm"}`), "review_snapshot_id"},
		"review_ref": {[]byte(`{"review_ref":{"repo":"org/repo","commit":"abc123"},"finding_id":"ISS-001","verdict":"accurate","reviewer":"tdm"}`), "review_snapshot_id"},
		"zero":       {[]byte(`{"review_snapshot_id":"0","finding_id":"ISS-001","verdict":"accurate","reviewer":"tdm"}`), "snapshot ID"},
		"unquoted":   {[]byte(`{"review_snapshot_id":7,"finding_id":"ISS-001","verdict":"accurate","reviewer":"tdm"}`), "snapshot ID"},
	} {
		t.Run(name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/api/v1/agent/feedback", bytes.NewReader(tc.body))
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			handler.SubmitFeedback(w, req)
			if w.Code != http.StatusBadRequest {
				t.Fatalf("code = %d, want 400: %s", w.Code, w.Body.String())
			}
			if !strings.Contains(w.Body.String(), tc.wants) {
				t.Fatalf("error %q does not mention %q", w.Body.String(), tc.wants)
			}
		})
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
	w := submit(t, handler, feedback.FeedbackRequest{
		ReviewSnapshotID: snapshotID,
		FindingID:        "ISS-003",
		Verdict:          "accurate",
		Reviewer:         "victim",
	})
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

	w := submit(t, handler, feedback.FeedbackRequest{
		ReviewSnapshotID: snapshot.SnapshotID(77),
		FindingID:        "ISS-001",
		FindingType:      "proven_issue",
		FindingSnapshot:  json.RawMessage(`{}`),
		Verdict:          "invalid_verdict",
		Reviewer:         "tdm",
		Source:           "interactive",
	})

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
}

func TestSubmitFeedbackMissingFields(t *testing.T) {
	store := feedback.NewMemoryStore()
	handler := feedback.NewHandler(store)

	body := []byte(`{"review_snapshot_id":"77","verdict":"accurate"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/agent/feedback", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	handler.SubmitFeedback(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
}
