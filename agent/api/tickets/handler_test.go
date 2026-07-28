package tickets_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/concourse/concourse/agent/api/tickets"
	"github.com/concourse/concourse/agent/api/tickets/ticketstest"
)

func newTestHandler(username string) (*tickets.Handler, *ticketstest.MemoryStore) {
	store := ticketstest.NewMemoryStore()
	h := tickets.NewHandler(store, func(*http.Request) string { return username })
	return h, store
}

func withParams(r *http.Request, params url.Values) *http.Request {
	r.Form = params
	return r
}

func TestCreateTicketAsHuman(t *testing.T) {
	h, store := newTestHandler("tdm")
	req := httptest.NewRequest("POST", "/api/v1/agent/tickets",
		strings.NewReader(`{"title":"fix X","repo":"tdmtrader/concourse","origin":"fly"}`))
	rec := httptest.NewRecorder()
	h.CreateTicket(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("code = %d body %s, want 201", rec.Code, rec.Body)
	}
	var created tickets.Ticket
	json.Unmarshal(rec.Body.Bytes(), &created)
	if created.ID != 1 || created.State != tickets.StateDraft ||
		created.UserName != "tdm" || created.CreatedBy != "tdm" {
		t.Errorf("created = %+v", created)
	}
	if got, _, _ := store.Get(1); got.Title != "fix X" {
		t.Errorf("stored ticket = %+v", got)
	}
}

func TestCreateTicketValidation(t *testing.T) {
	h, _ := newTestHandler("tdm")
	for body, want := range map[string]int{
		`{"repo":"r"}`:  http.StatusBadRequest, // no title
		`{"title":"t"}`: http.StatusBadRequest, // no repo
		`{"title":"t","repo":"r","origin":"email"}`: http.StatusBadRequest,
		`not json`: http.StatusBadRequest,
	} {
		req := httptest.NewRequest("POST", "/api/v1/agent/tickets", strings.NewReader(body))
		rec := httptest.NewRecorder()
		h.CreateTicket(rec, req)
		if rec.Code != want {
			t.Errorf("body %q: code = %d, want %d", body, rec.Code, want)
		}
	}
}

func TestCreateTicketRejectsRetiredRetrospectiveOrigin(t *testing.T) {
	h, _ := newTestHandler("tdm")

	req := httptest.NewRequest("POST", "/api/v1/agent/tickets",
		strings.NewReader(`{"title":"t","repo":"r","origin":"retrospective"}`))
	rec := httptest.NewRecorder()
	h.CreateTicket(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("retrospective = %d, want 400", rec.Code)
	}

	req = httptest.NewRequest("POST", "/api/v1/agent/tickets",
		strings.NewReader(`{"title":"t","repo":"r","origin":"jira"}`))
	rec = httptest.NewRecorder()
	h.CreateTicket(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("jira = %d, want 400", rec.Code)
	}
}

func TestListTickets(t *testing.T) {
	h, store := newTestHandler("tdm")
	store.Create(&tickets.Ticket{Title: "a", Repo: "r1", Origin: "web"})
	store.Create(&tickets.Ticket{Title: "b", Repo: "r2", Origin: "fly"})

	req := httptest.NewRequest("GET", "/api/v1/agent/tickets?repo=r2", nil)
	rec := httptest.NewRecorder()
	h.ListTickets(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d", rec.Code)
	}
	var list []tickets.Ticket
	json.Unmarshal(rec.Body.Bytes(), &list)
	if len(list) != 1 || list[0].Title != "b" {
		t.Errorf("list = %+v", list)
	}

	req = httptest.NewRequest("GET", "/api/v1/agent/tickets?state=bogus", nil)
	rec = httptest.NewRecorder()
	h.ListTickets(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("bogus state filter = %d, want 400", rec.Code)
	}
}

// The detail payload is the ticket and nothing else: the spec and task-plan
// tables went with their (deleted) agent write routes, so the markdown body
// is the only ticket prose there is.
func TestGetTicketDetail(t *testing.T) {
	h, store := newTestHandler("tdm")
	store.Create(&tickets.Ticket{Title: "t", Repo: "r", Body: "the whole story"})

	req := withParams(httptest.NewRequest("GET", "/api/v1/agent/tickets/1", nil),
		url.Values{":ticket_id": {"1"}})
	rec := httptest.NewRecorder()
	h.GetTicket(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("detail = %d body %s", rec.Code, rec.Body)
	}
	var detail tickets.TicketDetail
	json.Unmarshal(rec.Body.Bytes(), &detail)
	if detail.Ticket.ID != 1 || detail.Ticket.Body != "the whole story" {
		t.Fatalf("detail = %+v", detail)
	}
	for _, retired := range []string{`"spec"`, `"tasks"`} {
		if strings.Contains(rec.Body.String(), retired) {
			t.Errorf("detail body still carries %s: %s", retired, rec.Body.String())
		}
	}

	req = withParams(httptest.NewRequest("GET", "/api/v1/agent/tickets/99", nil),
		url.Values{":ticket_id": {"99"}})
	rec = httptest.NewRecorder()
	h.GetTicket(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Errorf("missing ticket = %d, want 404", rec.Code)
	}
}

func TestUpdateTicket(t *testing.T) {
	h, store := newTestHandler("tdm")
	store.Create(&tickets.Ticket{Title: "t", Repo: "r"})

	req := withParams(httptest.NewRequest("PUT", "/api/v1/agent/tickets/1",
		strings.NewReader(`{"title":"t2","body":"b2"}`)), url.Values{":ticket_id": {"1"}})
	rec := httptest.NewRecorder()
	h.UpdateTicket(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d body %s", rec.Code, rec.Body)
	}
	got, _, _ := store.Get(1)
	if got.Title != "t2" || got.Body != "b2" {
		t.Errorf("update = %+v", got)
	}

	req = withParams(httptest.NewRequest("PUT", "/api/v1/agent/tickets/1",
		strings.NewReader(`{"repository_snapshot_id":"123"}`)), url.Values{":ticket_id": {"1"}})
	rec = httptest.NewRecorder()
	h.UpdateTicket(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("repository snapshot update = %d body %s", rec.Code, rec.Body)
	}
	got, _, _ = store.Get(1)
	if got.RepositorySnapshotID == nil || got.RepositorySnapshotID.String() != "123" {
		t.Fatalf("repository snapshot not selected: %+v", got)
	}

	req = withParams(httptest.NewRequest("PUT", "/api/v1/agent/tickets/1",
		strings.NewReader(`{}`)), url.Values{":ticket_id": {"1"}})
	rec = httptest.NewRecorder()
	h.UpdateTicket(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("empty update = %d, want 400", rec.Code)
	}
}

func TestTransitionTicket(t *testing.T) {
	h, store := newTestHandler("tdm")
	store.Create(&tickets.Ticket{Title: "t", Repo: "r"})

	transition := func(body string) *httptest.ResponseRecorder {
		req := withParams(httptest.NewRequest("PUT", "/api/v1/agent/tickets/1/state",
			strings.NewReader(body)), url.Values{":ticket_id": {"1"}})
		rec := httptest.NewRecorder()
		h.TransitionTicket(rec, req)
		return rec
	}

	if rec := transition(`{"from":"draft","to":"queued"}`); rec.Code != http.StatusOK {
		t.Fatalf("draft->queued = %d body %s", rec.Code, rec.Body)
	}
	if rec := transition(`{"from":"draft","to":"queued"}`); rec.Code != http.StatusConflict {
		t.Errorf("stale = %d, want 409", rec.Code)
	}
	if rec := transition(`{"from":"queued","to":"needs_review"}`); rec.Code != http.StatusConflict {
		t.Errorf("illegal = %d, want 409", rec.Code)
	}
	if rec := transition(`{"from":"queued","to":"open"}`); rec.Code != http.StatusBadRequest {
		t.Errorf("bogus state = %d, want 400", rec.Code)
	}
	// The retired disposition verbs are not states any more: a stale client
	// asking for one gets 400, not a silent no-op.
	if rec := transition(`{"from":"queued","to":"merged"}`); rec.Code != http.StatusBadRequest {
		t.Errorf("retired disposition = %d, want 400", rec.Code)
	}
	if got, _, _ := store.Get(1); got.State != tickets.StateQueued {
		t.Errorf("state = %s, want queued", got.State)
	}
}

// v3-only cleanup (2026-07-24): pipeline_run_id is server-owned. The
// durable workflow/pipeline link is written only by in-process dispatch,
// never accepted from an HTTP caller (F30 id class). The transition body's
// custom decoder rejects the retired key with 400 instead of silently
// dropping it, and the ticket must not move.
func TestTransitionRejectsServerOwnedPipelineRunID(t *testing.T) {
	h, store := newTestHandler("tdm")
	store.Create(&tickets.Ticket{Title: "t", Repo: "r"})
	store.Transition(1, tickets.StateDraft, tickets.StateQueued, tickets.TransitionMeta{})

	req := withParams(httptest.NewRequest("PUT", "/api/v1/agent/tickets/1/state",
		strings.NewReader(`{"from":"queued","to":"running","pipeline_run_id":42}`)),
		url.Values{":ticket_id": {"1"}})
	rec := httptest.NewRecorder()
	h.TransitionTicket(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("server-owned pipeline_run_id = %d body %s, want 400", rec.Code, rec.Body)
	}
	if !strings.Contains(rec.Body.String(), "pipeline_run_id is server-owned") {
		t.Errorf("body = %q, want mention of server-owned pipeline_run_id", rec.Body.String())
	}
	if got, _, _ := store.Get(1); got.State != tickets.StateQueued || got.PipelineRunID != nil {
		t.Errorf("ticket after rejected transition = %+v", got)
	}
}
