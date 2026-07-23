package tickets_test

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/concourse/concourse/agent/api/principals"
	"github.com/concourse/concourse/agent/api/tickets"
)

func newTestHandler(username string) (*tickets.Handler, *tickets.MemoryStore) {
	store := tickets.NewMemoryStore()
	h := tickets.NewHandler(store, func(*http.Request) string { return username }, nil)
	return h, store
}

func asPrincipal(r *http.Request, name string) *http.Request {
	return r.WithContext(principals.NewContext(r.Context(), principals.Principal{ID: 3, Name: name}))
}

func withParams(r *http.Request, params url.Values) *http.Request {
	r.Form = params
	return r
}

func TestCreateTicketAsHuman(t *testing.T) {
	h, store := newTestHandler("tdm")
	req := httptest.NewRequest("POST", "/api/v1/agent/tickets",
		strings.NewReader(`{"title":"fix X","repo":"tdmtrader/concourse","origin":"fly","budget_usd":5}`))
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
	if got, _, _ := store.Get(1); got.BudgetUSD == nil || *got.BudgetUSD != 5 {
		t.Errorf("budget not stored: %+v", got)
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

func TestCreateTicketOriginRules(t *testing.T) {
	h, _ := newTestHandler("tdm")

	// human + retrospective -> 403
	req := httptest.NewRequest("POST", "/api/v1/agent/tickets",
		strings.NewReader(`{"title":"t","repo":"r","origin":"retrospective"}`))
	rec := httptest.NewRecorder()
	h.CreateTicket(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Errorf("human retrospective = %d, want 403", rec.Code)
	}

	// jira -> 400 until phase 2
	req = httptest.NewRequest("POST", "/api/v1/agent/tickets",
		strings.NewReader(`{"title":"t","repo":"r","origin":"jira"}`))
	rec = httptest.NewRecorder()
	h.CreateTicket(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("jira = %d, want 400", rec.Code)
	}

	// principal + retrospective -> 201 attributed to the principal, no triggering user
	req = asPrincipal(httptest.NewRequest("POST", "/api/v1/agent/tickets",
		strings.NewReader(`{"title":"add lint rule","repo":"r","origin":"retrospective"}`)), "retro-agent")
	rec = httptest.NewRecorder()
	h.CreateTicket(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("principal retrospective = %d body %s, want 201", rec.Code, rec.Body)
	}
	var created tickets.Ticket
	json.Unmarshal(rec.Body.Bytes(), &created)
	if created.CreatedBy != "retro-agent" || created.UserName != "" {
		t.Errorf("attribution = %+v", created)
	}

	// principal + web -> 403
	req = asPrincipal(httptest.NewRequest("POST", "/api/v1/agent/tickets",
		strings.NewReader(`{"title":"t","repo":"r","origin":"web"}`)), "retro-agent")
	rec = httptest.NewRecorder()
	h.CreateTicket(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Errorf("principal web = %d, want 403", rec.Code)
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

func TestGetTicketDetail(t *testing.T) {
	h, store := newTestHandler("tdm")
	id, _ := store.Create(&tickets.Ticket{Title: "t", Repo: "r"})

	get := func() (int, tickets.TicketDetail) {
		req := withParams(httptest.NewRequest("GET", "/api/v1/agent/tickets/1", nil),
			url.Values{":ticket_id": {"1"}})
		rec := httptest.NewRecorder()
		h.GetTicket(rec, req)
		var detail tickets.TicketDetail
		json.Unmarshal(rec.Body.Bytes(), &detail)
		return rec.Code, detail
	}

	code, detail := get()
	if code != http.StatusOK || detail.Spec != nil || len(detail.Tasks) != 0 {
		t.Fatalf("empty detail = %d %+v", code, detail)
	}

	store.SubmitSpec(id, tickets.Spec{Title: "s", Body: "b"})
	store.SubmitPlan(id, []tickets.Task{{Title: "one"}})
	code, detail = get()
	if code != http.StatusOK || detail.Spec == nil || detail.Spec.Version != 1 || len(detail.Tasks) != 1 {
		t.Fatalf("filled detail = %d %+v", code, detail)
	}

	req := withParams(httptest.NewRequest("GET", "/api/v1/agent/tickets/99", nil),
		url.Values{":ticket_id": {"99"}})
	rec := httptest.NewRecorder()
	h.GetTicket(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Errorf("missing ticket = %d, want 404", rec.Code)
	}
}

func TestUpdateTicket(t *testing.T) {
	h, store := newTestHandler("tdm")
	store.Create(&tickets.Ticket{Title: "t", Repo: "r"})

	req := withParams(httptest.NewRequest("PUT", "/api/v1/agent/tickets/1",
		strings.NewReader(`{"title":"t2","budget_usd":7.5}`)), url.Values{":ticket_id": {"1"}})
	rec := httptest.NewRecorder()
	h.UpdateTicket(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d body %s", rec.Code, rec.Body)
	}
	got, _, _ := store.Get(1)
	if got.Title != "t2" || got.BudgetUSD == nil || *got.BudgetUSD != 7.5 {
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
	if rec := transition(`{"from":"queued","to":"merged"}`); rec.Code != http.StatusConflict {
		t.Errorf("illegal = %d, want 409", rec.Code)
	}
	if rec := transition(`{"from":"queued","to":"open"}`); rec.Code != http.StatusBadRequest {
		t.Errorf("bogus state = %d, want 400", rec.Code)
	}
	if got, _, _ := store.Get(1); got.State != tickets.StateQueued {
		t.Errorf("state = %s, want queued", got.State)
	}
}

// F30 hardening (2026-07-18): pipeline_run_id in the transition body is
// attacker-writable by any tickets:write principal or main-team member,
// and it poisons display surfaces plus any future consumer that trusts
// agent_tickets.pipeline_run_id. The handler must only record a run id
// the injected RunForTicketFunc proves belongs to the ticket's own
// agent-ticket-<id> pipeline — and must fail closed when no checker is
// wired at all.
func TestTransitionPipelineRunIDValidation(t *testing.T) {
	newHandler := func(check tickets.RunForTicketFunc) (*tickets.Handler, *tickets.MemoryStore) {
		store := tickets.NewMemoryStore()
		h := tickets.NewHandler(store, func(*http.Request) string { return "tdm" }, check)
		store.Create(&tickets.Ticket{Title: "t", Repo: "r"})
		store.Transition(1, tickets.StateDraft, tickets.StateQueued, tickets.TransitionMeta{})
		return h, store
	}

	transition := func(h *tickets.Handler, body string) *httptest.ResponseRecorder {
		req := withParams(httptest.NewRequest("PUT", "/api/v1/agent/tickets/1/state",
			strings.NewReader(body)), url.Values{":ticket_id": {"1"}})
		rec := httptest.NewRecorder()
		h.TransitionTicket(rec, req)
		return rec
	}

	// checker says the run is not the ticket's → 422, ticket must not move
	h, store := newHandler(func(int, int) (bool, error) { return false, nil })
	rec := transition(h, `{"from":"queued","to":"running","pipeline_run_id":99}`)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("mismatched run id = %d body %s, want 422", rec.Code, rec.Body)
	}
	if got, _, _ := store.Get(1); got.State != tickets.StateQueued || got.PipelineRunID != nil {
		t.Errorf("ticket after rejected transition = %+v", got)
	}

	// checker confirms ownership → transition proceeds and records the id
	var gotTicket, gotRun int
	h, store = newHandler(func(ticketID, runID int) (bool, error) {
		gotTicket, gotRun = ticketID, runID
		return true, nil
	})
	rec = transition(h, `{"from":"queued","to":"running","pipeline_run_id":42}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("owned run id = %d body %s, want 200", rec.Code, rec.Body)
	}
	if gotTicket != 1 || gotRun != 42 {
		t.Errorf("checker saw (%d, %d), want (1, 42)", gotTicket, gotRun)
	}
	if got, _, _ := store.Get(1); got.PipelineRunID == nil || *got.PipelineRunID != 42 {
		t.Errorf("ticket after accepted transition = %+v", got)
	}

	// checker failure → 500, not silently accepted
	h, _ = newHandler(func(int, int) (bool, error) { return false, errors.New("db down") })
	rec = transition(h, `{"from":"queued","to":"running","pipeline_run_id":42}`)
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("checker error = %d, want 500", rec.Code)
	}

	// no checker wired → fail closed on any pipeline_run_id
	h, _ = newHandler(nil)
	rec = transition(h, `{"from":"queued","to":"running","pipeline_run_id":42}`)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("nil checker = %d, want 422", rec.Code)
	}

	// a body without pipeline_run_id never consults the checker
	called := false
	h, _ = newHandler(func(int, int) (bool, error) { called = true; return false, nil })
	rec = transition(h, `{"from":"queued","to":"running"}`)
	if rec.Code != http.StatusOK || called {
		t.Errorf("plain transition = %d (checker called: %v), want 200 without a check", rec.Code, called)
	}
}

func TestSpecPlanTaskRoutes(t *testing.T) {
	h, store := newTestHandler("")
	store.Create(&tickets.Ticket{Title: "t", Repo: "r"})

	req := asPrincipal(withParams(httptest.NewRequest("POST", "/api/v1/agent/tickets/1/spec",
		strings.NewReader(`{"title":"spec","body":"prose","acceptance_criteria":["a"],"links":[{"title":"l","url":"u"}]}`)),
		url.Values{":ticket_id": {"1"}}), "run-42-platform")
	rec := httptest.NewRecorder()
	h.SubmitSpec(rec, req)
	if rec.Code != http.StatusCreated || !strings.Contains(rec.Body.String(), `"version":1`) {
		t.Fatalf("spec = %d %s", rec.Code, rec.Body)
	}
	spec, _, _ := store.LatestSpec(1)
	if spec.SubmittedBy != "run-42-platform" {
		t.Errorf("spec attribution = %+v", spec)
	}

	// missing body -> 400
	req = withParams(httptest.NewRequest("POST", "/api/v1/agent/tickets/1/spec",
		strings.NewReader(`{"title":"only"}`)), url.Values{":ticket_id": {"1"}})
	rec = httptest.NewRecorder()
	h.SubmitSpec(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("spec without body = %d, want 400", rec.Code)
	}

	req = asPrincipal(withParams(httptest.NewRequest("POST", "/api/v1/agent/tickets/1/plan",
		strings.NewReader(`{"tasks":[{"title":"one"},{"title":"two","detail":"d"}]}`)),
		url.Values{":ticket_id": {"1"}}), "run-42-platform")
	rec = httptest.NewRecorder()
	h.SubmitPlan(rec, req)
	if rec.Code != http.StatusCreated || !strings.Contains(rec.Body.String(), `"plan_version":1`) {
		t.Fatalf("plan = %d %s", rec.Code, rec.Body)
	}

	// empty plan -> 400
	req = withParams(httptest.NewRequest("POST", "/api/v1/agent/tickets/1/plan",
		strings.NewReader(`{"tasks":[]}`)), url.Values{":ticket_id": {"1"}})
	rec = httptest.NewRecorder()
	h.SubmitPlan(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("empty plan = %d, want 400", rec.Code)
	}

	req = asPrincipal(withParams(httptest.NewRequest("PUT", "/api/v1/agent/tickets/1/tasks/2",
		strings.NewReader(`{"status":"done","note":"trivial"}`)),
		url.Values{":ticket_id": {"1"}, ":ordering": {"2"}}), "run-42-platform")
	rec = httptest.NewRecorder()
	h.UpdateTask(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("task update = %d %s", rec.Code, rec.Body)
	}
	tasks, _ := store.ActivePlan(1)
	if tasks[1].Status != tickets.TaskDone || tasks[1].Detail != "d\n\n> trivial" {
		t.Errorf("task after update = %+v", tasks[1])
	}

	// bad status -> 400
	req = withParams(httptest.NewRequest("PUT", "/api/v1/agent/tickets/1/tasks/2",
		strings.NewReader(`{"status":"started"}`)),
		url.Values{":ticket_id": {"1"}, ":ordering": {"2"}})
	rec = httptest.NewRecorder()
	h.UpdateTask(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("bad status = %d, want 400", rec.Code)
	}

	// ordering beyond the plan -> 404
	req = withParams(httptest.NewRequest("PUT", "/api/v1/agent/tickets/1/tasks/9",
		strings.NewReader(`{"status":"done"}`)),
		url.Values{":ticket_id": {"1"}, ":ordering": {"9"}})
	rec = httptest.NewRecorder()
	h.UpdateTask(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Errorf("missing task = %d, want 404", rec.Code)
	}
}
