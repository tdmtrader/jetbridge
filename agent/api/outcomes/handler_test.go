package outcomes_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/concourse/concourse/agent/api/outcomes"
	"github.com/concourse/concourse/agent/api/tickets"
)

func newHandler(t *testing.T) (*outcomes.Handler, *outcomes.MemoryStore, *tickets.MemoryStore) {
	t.Helper()
	os := outcomes.NewMemoryStore()
	ts := tickets.NewMemoryStore()
	h := outcomes.NewHandler(os, ts, func(r *http.Request) string { return "tdmtrader" })
	return h, os, ts
}

// seedNeedsReview creates a ticket already at needs_review with an outcome row.
func seedNeedsReview(t *testing.T, os *outcomes.MemoryStore, ts *tickets.MemoryStore, id int) {
	t.Helper()
	tk := &tickets.Ticket{Title: "x", Repo: "r", TargetBranch: "main"}
	created, err := ts.Create(tk)
	if err != nil {
		t.Fatal(err)
	}
	// drive draft -> queued -> running -> needs_review
	_ = ts.Transition(created, tickets.StateDraft, tickets.StateQueued, tickets.TransitionMeta{})
	_ = ts.Transition(created, tickets.StateQueued, tickets.StateRunning, tickets.TransitionMeta{})
	if err := ts.Transition(created, tickets.StateRunning, tickets.StateNeedsReview, tickets.TransitionMeta{Branch: "agent/ticket-1"}); err != nil {
		t.Fatal(err)
	}
	_ = os.Ensure(&outcomes.Outcome{TicketID: created, Repo: "r", Branch: "agent/ticket-1"})
}

func TestGetOutcome(t *testing.T) {
	h, os, _ := newHandler(t)
	_ = os.Ensure(&outcomes.Outcome{TicketID: 1, Repo: "r", Branch: "b", PushedSha: "p"})

	req := httptest.NewRequest("GET", "/api/v1/agent/tickets/1/outcome", nil)
	req.Form = map[string][]string{":ticket_id": {"1"}}
	rec := httptest.NewRecorder()
	h.GetOutcome(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d body=%s", rec.Code, rec.Body)
	}
	var got outcomes.Outcome
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.TicketID != 1 || got.PushedSha != "p" {
		t.Fatalf("got %+v", got)
	}

	// no outcome row yet -> 404
	req2 := httptest.NewRequest("GET", "/api/v1/agent/tickets/2/outcome", nil)
	req2.Form = map[string][]string{":ticket_id": {"2"}}
	rec2 := httptest.NewRecorder()
	h.GetOutcome(rec2, req2)
	if rec2.Code != http.StatusNotFound {
		t.Fatalf("missing outcome: code = %d", rec2.Code)
	}
}

func TestSetDispositionTransitionsTicket(t *testing.T) {
	h, os, ts := newHandler(t)
	seedNeedsReview(t, os, ts, 1)

	body, _ := json.Marshal(map[string]string{
		"disposition": "sent_back", "reason": "incomplete", "notes": "needs more tests",
	})
	req := httptest.NewRequest("PUT", "/api/v1/agent/tickets/1/disposition", bytes.NewReader(body))
	req.Form = map[string][]string{":ticket_id": {"1"}}
	rec := httptest.NewRecorder()
	h.SetDisposition(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d body=%s", rec.Code, rec.Body)
	}

	// outcome recorded
	got, _, _ := os.Get(1)
	if got.Disposition != "sent_back" || got.DisposedBy != "tdmtrader" || got.MergeState != outcomes.ClosedUnmerged {
		t.Fatalf("outcome not recorded: %+v", got)
	}
	// ticket transitioned via the store
	tk, _, _ := ts.Get(1)
	if tk.State != tickets.StateSentBack {
		t.Fatalf("ticket state = %s, want sent_back", tk.State)
	}
}

func TestSetDispositionConcludedTransitionsTicket(t *testing.T) {
	h, os, ts := newHandler(t)
	seedNeedsReview(t, os, ts, 1)

	// concluded: spike/research done, human reviewed, no merge intended.
	body, _ := json.Marshal(map[string]string{
		"disposition": "concluded", "reason": "research_complete", "notes": "findings recorded in ticket body",
	})
	req := httptest.NewRequest("PUT", "/api/v1/agent/tickets/1/disposition", bytes.NewReader(body))
	req.Form = map[string][]string{":ticket_id": {"1"}}
	rec := httptest.NewRecorder()
	h.SetDisposition(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d body=%s", rec.Code, rec.Body)
	}

	// outcome row closes as 'concluded' — never closed_unmerged, never open
	got, _, _ := os.Get(1)
	if got.Disposition != outcomes.DispositionConcluded || got.MergeState != outcomes.MergeConcluded || got.DisposedBy != "tdmtrader" {
		t.Fatalf("concluded outcome not recorded: %+v", got)
	}
	// ticket took the needs_review → concluded edge via the single writer
	tk, _, _ := ts.Get(1)
	if tk.State != tickets.StateConcluded {
		t.Fatalf("ticket state = %s, want concluded", tk.State)
	}
}

func TestSetDispositionRejectsBadTaxonomy(t *testing.T) {
	h, os, ts := newHandler(t)
	seedNeedsReview(t, os, ts, 1)
	for _, bad := range []string{
		`{"disposition":"merged","reason":"other"}`,
		`{"disposition":"sent_back","reason":"nonsense"}`,
		`{"disposition":"","reason":"other"}`,
	} {
		req := httptest.NewRequest("PUT", "/x", bytes.NewReader([]byte(bad)))
		req.Form = map[string][]string{":ticket_id": {"1"}}
		rec := httptest.NewRecorder()
		h.SetDisposition(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("body %s: code = %d, want 400", bad, rec.Code)
		}
	}
}

func TestSetDispositionInvalidTransitionIs409(t *testing.T) {
	h, os, ts := newHandler(t)
	// ticket already merged: needs_review -> abandoned is not a legal edge from merged
	tk := &tickets.Ticket{Title: "x", Repo: "r", TargetBranch: "main"}
	id, _ := ts.Create(tk)
	_ = ts.Transition(id, tickets.StateDraft, tickets.StateQueued, tickets.TransitionMeta{})
	_ = ts.Transition(id, tickets.StateQueued, tickets.StateRunning, tickets.TransitionMeta{})
	_ = ts.Transition(id, tickets.StateRunning, tickets.StateNeedsReview, tickets.TransitionMeta{})
	_ = ts.Transition(id, tickets.StateNeedsReview, tickets.StateMerged, tickets.TransitionMeta{})
	_ = os.Ensure(&outcomes.Outcome{TicketID: id, Repo: "r", Branch: "b"})
	_ = os.RecordMerge(id, outcomes.MergeResult{State: outcomes.Merged})

	body, _ := json.Marshal(map[string]string{"disposition": "abandoned", "reason": "other"})
	req := httptest.NewRequest("PUT", "/x", bytes.NewReader(body))
	req.Form = map[string][]string{":ticket_id": {"1"}}
	// use the merged ticket's id
	req.Form.Set(":ticket_id", itoa(id))
	rec := httptest.NewRecorder()
	h.SetDisposition(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("code = %d, want 409", rec.Code)
	}

	// 409 aborts BEFORE any outcome write: the merged row is untouched
	got, _, _ := os.Get(id)
	if got.MergeState != outcomes.Merged || got.Disposition != "" {
		t.Fatalf("409 must not touch the outcome row: %+v", got)
	}
}

func TestSetDispositionCreatesRowWhenAbsent(t *testing.T) {
	h, os, ts := newHandler(t)
	// ticket at needs_review, but NO outcome row (dispositioned pre-harvest)
	tk := &tickets.Ticket{Title: "x", Repo: "r", TargetBranch: "main"}
	id, _ := ts.Create(tk)
	_ = ts.Transition(id, tickets.StateDraft, tickets.StateQueued, tickets.TransitionMeta{})
	_ = ts.Transition(id, tickets.StateQueued, tickets.StateRunning, tickets.TransitionMeta{})
	_ = ts.Transition(id, tickets.StateRunning, tickets.StateNeedsReview, tickets.TransitionMeta{})

	body, _ := json.Marshal(map[string]string{"disposition": "abandoned", "reason": "not_needed"})
	req := httptest.NewRequest("PUT", "/x", bytes.NewReader(body))
	req.Form = map[string][]string{":ticket_id": {itoa(id)}}
	rec := httptest.NewRecorder()
	h.SetDisposition(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d body=%s", rec.Code, rec.Body)
	}
	got, found, _ := os.Get(id)
	if !found || got.Disposition != "abandoned" || got.MergeState != outcomes.ClosedUnmerged {
		t.Fatalf("disposition must be durable without a prior row: %+v", got)
	}
}

func itoa(i int) string { b, _ := json.Marshal(i); return string(b) }
