package dispatch_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/concourse/concourse/agent/api/tickets"
	"github.com/concourse/concourse/agent/budget"
	"github.com/concourse/concourse/agent/budget/budgetfakes"
	"github.com/concourse/concourse/agent/dispatch"
	"github.com/concourse/concourse/agent/workflow"
)

func dispatchRequest(id string) *http.Request {
	req := httptest.NewRequest("POST", "/api/v1/agent/tickets/"+id+"/dispatch", nil)
	req.Form = url.Values{":ticket_id": {id}}
	return req
}

func TestDispatchHandlerHappyPath(t *testing.T) {
	deps, store, _, _ := dispatchDeps(t)
	id := queuedTicket(t, store, "smoke")
	h := dispatch.NewHTTPHandler(deps, func(*http.Request) string { return "tdm" })

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, dispatchRequest("1"))
	if rec.Code != http.StatusCreated {
		t.Fatalf("code = %d body %s", rec.Code, rec.Body)
	}
	var resp tickets.DispatchResponse
	json.Unmarshal(rec.Body.Bytes(), &resp)
	if resp.RunID != 555 || resp.PipelineName != "agent-ticket-1" {
		t.Errorf("response = %+v", resp)
	}

	got, _, _ := store.Get(id)
	if got.State != tickets.StateRunning {
		t.Errorf("state = %s", got.State)
	}
}

// TestDispatchHandlerSpecLintWarningsAdditive: the warnings field is
// ADVISORY and ADDITIVE (ticket #46) — present when the spec prose
// matches the false-refusal vocabulary table, entirely absent (omitempty)
// when clean, and never changes the 201 or the dispatched state.
func TestDispatchHandlerSpecLintWarningsAdditive(t *testing.T) {
	deps, store, _, _ := dispatchDeps(t)
	h := dispatch.NewHTTPHandler(deps, func(*http.Request) string { return "tdm" })

	// clean spec: no warnings key at all (additive omitempty)
	cleanID := queuedTicket(t, store, "smoke")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, dispatchRequest(itoa(cleanID)))
	if rec.Code != http.StatusCreated {
		t.Fatalf("clean dispatch = %d body %s", rec.Code, rec.Body)
	}
	if strings.Contains(rec.Body.String(), "warnings") {
		t.Errorf("clean spec must omit the warnings key, got %s", rec.Body)
	}

	// lint-hitting spec: warnings ride the same 201
	hitID, err := store.Create(&tickets.Ticket{
		Title: "wire the flight recorder", Body: "record everything", Origin: "fly",
		Repo: "tdmtrader/jetbridge", TargetBranch: "main",
		WorkflowName: "smoke", UserName: "tdm", CreatedBy: "tdm",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Transition(hitID, tickets.StateDraft, tickets.StateQueued, tickets.TransitionMeta{}); err != nil {
		t.Fatal(err)
	}
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, dispatchRequest(itoa(hitID)))
	if rec.Code != http.StatusCreated {
		t.Fatalf("lint hit must NOT block: code = %d body %s", rec.Code, rec.Body)
	}
	var resp tickets.DispatchResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Warnings) == 0 {
		t.Fatalf("flight-recorder prose must warn, body %s", rec.Body)
	}
	if !strings.Contains(resp.Warnings[0], "flight recorder") {
		t.Errorf("warning must name the matched phrase, got %q", resp.Warnings[0])
	}
	got, _, _ := store.Get(hitID)
	if got.State != tickets.StateRunning {
		t.Errorf("warned ticket must still dispatch, state = %s", got.State)
	}
}

func TestDispatchHandlerErrorMapping(t *testing.T) {
	deps, store, _, _ := dispatchDeps(t)

	h := dispatch.NewHTTPHandler(deps, func(*http.Request) string { return "tdm" })

	// missing ticket -> 404
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, dispatchRequest("99"))
	if rec.Code != http.StatusNotFound {
		t.Errorf("missing = %d, want 404", rec.Code)
	}

	// draft (not queued) -> 409
	store.Create(&tickets.Ticket{Title: "d", Repo: "r", WorkflowName: "smoke"})
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, dispatchRequest("1"))
	if rec.Code != http.StatusConflict {
		t.Errorf("draft = %d, want 409", rec.Code)
	}

	// queued but no workflow name -> 422
	id := queuedTicket(t, store, "")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, dispatchRequest(itoa(id)))
	if rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("no workflow = %d, want 422", rec.Code)
	}

	// unknown workflow -> 422
	id = queuedTicket(t, store, "nope")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, dispatchRequest(itoa(id)))
	if rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("unknown workflow = %d, want 422", rec.Code)
	}

	// render refusal (v0-unenforceable workflow) -> 422, not 500
	// judge renders now (judge-evidence Slice E) — hitl remains the
	// canonical still-refused surface for this mapping test.
	deps.Workflows.(*fakeWorkflows).byName["parked"] = &workflow.Definition{
		Name: "parked", Version: 1, Live: true,
		Config: workflow.Config{
			Name: "parked", SpecDelivery: "files",
			Prompts: map[string]string{"do": "x"},
			HITL:    workflow.HITL{AskTimeout: "park"},
			Steps:   []workflow.Step{{Agent: "a", Prompt: "do", Inputs: []string{"ticket"}, Outputs: []string{"workspace"}}},
		},
	}
	id = queuedTicket(t, store, "parked")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, dispatchRequest(itoa(id)))
	if rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("render refusal = %d, want 422", rec.Code)
	}

	// bad param -> 400
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, dispatchRequest("zero"))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("bad id = %d, want 400", rec.Code)
	}
}

func TestDispatchHandlerBudgetExhaustedMapsTo409(t *testing.T) {
	deps, store, _, _ := dispatchDeps(t)
	checker := new(budgetfakes.FakeChecker)
	checker.TicketRemainingReturns(budget.Remaining{LimitUSD: 5, SpentUSD: 6, RemainingUSD: -1, Exhausted: true}, nil)
	deps.Budget = checker
	id := queuedTicket(t, store, "smoke")
	h := dispatch.NewHTTPHandler(deps, func(*http.Request) string { return "tdm" })

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, dispatchRequest(itoa(id)))
	if rec.Code != http.StatusConflict {
		t.Fatalf("budget exhausted = %d, want 409 (body %s)", rec.Code, rec.Body)
	}
	if !strings.Contains(rec.Body.String(), "budget exhausted") {
		t.Errorf("body must name the deferral, got %q", rec.Body.String())
	}

	got, _, _ := store.Get(id)
	if got.State != tickets.StateQueued {
		t.Errorf("over-cap ticket must stay queued, state = %s", got.State)
	}
}

func itoa(i int) string { return strconv.Itoa(i) }
