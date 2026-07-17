package tickets_test

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/concourse/concourse/agent/api/tickets"
)

// planSwapStore simulates a concurrent SubmitPlan landing between any
// handler-side read of the active plan and the subsequent write — the
// TOCTOU window agent-review-native #7 proved lost updates through
// (read plan_version, plan replaced, write against the stale version,
// 200 OK, change invisible). The fix routes the handler through the
// atomic Store.UpdateActiveTask, which resolves the active version and
// writes in one store operation, so this shim's stale snapshot can no
// longer be captured.
type planSwapStore struct {
	*tickets.MemoryStore
	swapped bool
}

func (s *planSwapStore) ActivePlan(id int) ([]tickets.Task, error) {
	stale, err := s.MemoryStore.ActivePlan(id)
	if !s.swapped {
		s.swapped = true
		// the "concurrent" submit: a new plan becomes active AFTER the
		// snapshot above was taken but BEFORE the caller acts on it
		s.MemoryStore.SubmitPlan(id, []tickets.Task{{Title: "replacement"}})
	}
	return stale, err
}

func TestUpdateTaskAppliesToCurrentlyActivePlan(t *testing.T) {
	mem := tickets.NewMemoryStore()
	store := &planSwapStore{MemoryStore: mem}
	h := tickets.NewHandler(store, func(*http.Request) string { return "tdm" })

	id, _ := mem.Create(&tickets.Ticket{Title: "t", Repo: "r"})
	mem.SubmitPlan(id, []tickets.Task{{Title: "one"}, {Title: "two"}})

	req := withParams(httptest.NewRequest("PUT", "/api/v1/agent/tickets/1/tasks/1",
		strings.NewReader(`{"status":"done","note":"landed"}`)),
		url.Values{":ticket_id": {"1"}, ":ordering": {"1"}})
	rec := httptest.NewRecorder()
	h.UpdateTask(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("UpdateTask = %d %s", rec.Code, rec.Body)
	}

	// A 200 must mean the CURRENTLY active plan reflects the change —
	// never a silent write to a superseded plan_version.
	active, _ := mem.ActivePlan(id)
	if len(active) == 0 {
		t.Fatal("no active plan")
	}
	if active[0].Status != tickets.TaskDone {
		t.Errorf("lost update: active plan task = %+v (status %q, want done)", active[0], active[0].Status)
	}
	if !strings.Contains(active[0].Detail, "> landed") {
		t.Errorf("note lost with the update: detail = %q", active[0].Detail)
	}
}

func TestMemoryStoreUpdateActiveTask(t *testing.T) {
	s := tickets.NewMemoryStore()
	id, _ := s.Create(newTicket())

	if _, err := s.UpdateActiveTask(id, 1, tickets.TaskDone, ""); err != tickets.ErrNoActivePlan {
		t.Errorf("no plan: got %v, want ErrNoActivePlan", err)
	}

	s.SubmitPlan(id, []tickets.Task{{Title: "old"}})
	s.SubmitPlan(id, []tickets.Task{{Title: "one", Detail: "d"}, {Title: "two"}})

	pv, err := s.UpdateActiveTask(id, 1, tickets.TaskInProgress, "note")
	if err != nil || pv != 2 {
		t.Fatalf("UpdateActiveTask = %d, %v; want 2, nil", pv, err)
	}
	active, _ := s.ActivePlan(id)
	if active[0].Status != tickets.TaskInProgress || active[0].Detail != "d\n\n> note" {
		t.Errorf("task = %+v", active[0])
	}

	if _, err := s.UpdateActiveTask(id, 99, tickets.TaskDone, ""); err != tickets.ErrTaskNotFound {
		t.Errorf("bad ordering: got %v, want ErrTaskNotFound", err)
	}
	if _, err := s.UpdateActiveTask(4242, 1, tickets.TaskDone, ""); err != tickets.ErrTicketNotFound {
		t.Errorf("missing ticket: got %v, want ErrTicketNotFound", err)
	}
}

func TestCreateAndUpdateRejectNegativeBudget(t *testing.T) {
	h, store := newTestHandler("tdm")

	req := httptest.NewRequest("POST", "/api/v1/agent/tickets",
		strings.NewReader(`{"title":"t","repo":"r","budget_usd":-1}`))
	rec := httptest.NewRecorder()
	h.CreateTicket(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("create negative budget = %d, want 400", rec.Code)
	}

	store.Create(&tickets.Ticket{Title: "t", Repo: "r"})
	req = withParams(httptest.NewRequest("PUT", "/api/v1/agent/tickets/1",
		strings.NewReader(`{"budget_usd":-0.5}`)), url.Values{":ticket_id": {"1"}})
	rec = httptest.NewRecorder()
	h.UpdateTicket(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("update negative budget = %d, want 400", rec.Code)
	}
}
