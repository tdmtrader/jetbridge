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
	"github.com/concourse/concourse/agent/dispatch"
	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/agent/workflowrun"
)

func dispatchRequest(id string) *http.Request {
	req := httptest.NewRequest("POST", "/api/v1/agent/tickets/"+id+"/dispatch", nil)
	req.Form = url.Values{":ticket_id": {id}}
	return req
}

func TestDispatchHandlerHappyPath(t *testing.T) {
	deps, store, _, _ := v3DispatchDeps(t)
	id := queuedTicket(t, store, "smoke")
	setRepositorySnapshot(t, store, id, 101)
	h := dispatch.NewHTTPHandler(deps, func(*http.Request) string { return "tdm" })

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, dispatchRequest("1"))
	if rec.Code != http.StatusCreated {
		t.Fatalf("code = %d body %s", rec.Code, rec.Body)
	}
	var resp tickets.DispatchResponse
	json.Unmarshal(rec.Body.Bytes(), &resp)
	if resp.WorkflowRunID != snapshot.WorkflowRunID(303) ||
		resp.PipelineRunID == nil || *resp.PipelineRunID != 909 {
		t.Errorf("response = %+v", resp)
	}

	got, _, _ := store.Get(id)
	if got.State != tickets.StateRunning {
		t.Errorf("state = %s", got.State)
	}
}

func TestDispatchHandlerErrorMapping(t *testing.T) {
	deps, store, _, _ := v3DispatchDeps(t)

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

	// a workflow whose signature cannot carry a ticket -> 422, not 500
	deps.Workflows.(*fakeWorkflows).byName["no-repo"] =
		definitionWithInputs(t, portSpec{"work-item", "work-item/v1"})
	h = dispatch.NewHTTPHandler(deps, func(*http.Request) string { return "tdm" })
	id = queuedTicket(t, store, "no-repo")
	setRepositorySnapshot(t, store, id, 101)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, dispatchRequest(itoa(id)))
	if rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("undispatchable signature = %d, want 422", rec.Code)
	}

	// bad param -> 400
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, dispatchRequest("zero"))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("bad id = %d, want 400", rec.Code)
	}
}

func TestDispatchHandlerBudgetExhaustedMapsTo409(t *testing.T) {
	deps, store, _, binder := v3DispatchDeps(t)
	// The binder's durable reservation is the admission authority; its
	// denial is what the route must render as a 409 deferral.
	binder.err = workflowrun.ErrBudgetDenied
	id := queuedTicket(t, store, "smoke")
	setRepositorySnapshot(t, store, id, 101)
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

func TestDispatchHandlerV3InputsPendingMapsToSanitizedConflict(t *testing.T) {
	deps, store, _, _ := v3DispatchDeps(t)
	id := queuedTicket(t, store, "smoke")
	h := dispatch.NewHTTPHandler(deps, func(*http.Request) string { return "tdm" })

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, dispatchRequest(itoa(id)))
	if rec.Code != http.StatusConflict {
		t.Fatalf("inputs pending = %d, want 409 (body %s)", rec.Code, rec.Body)
	}
	if rec.Body.String() != "workflow inputs pending\n" {
		t.Fatalf("pending response leaked internal detail: %q", rec.Body.String())
	}
	got, _, _ := store.Get(id)
	if got.State != tickets.StateQueued || got.DispatchReservationKey == "" {
		t.Fatalf("pending ticket must stay durably reserved: %+v", got)
	}
}

// The 201 body is the batch dispatch contract: the workflow run as a quoted
// decimal identity, the pipeline run as an optional diagnostic, and no
// pipeline NAME at all.
func TestDispatchHandlerBodyIsTheDispatchContract(t *testing.T) {
	deps, store, _, _ := v3DispatchDeps(t)
	id := queuedTicket(t, store, "smoke")
	setRepositorySnapshot(t, store, id, 101)
	h := dispatch.NewHTTPHandler(deps, func(*http.Request) string { return "tdm" })

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, dispatchRequest(itoa(id)))
	if rec.Code != http.StatusCreated {
		t.Fatalf("dispatch = %d body %s", rec.Code, rec.Body)
	}
	var wire map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &wire); err != nil {
		t.Fatal(err)
	}
	if wire["workflow_run_id"] != "303" {
		t.Errorf("workflow_run_id must be a quoted decimal identity, got %#v", wire["workflow_run_id"])
	}
	if wire["pipeline_run_id"] != float64(909) {
		t.Errorf("pipeline_run_id = %#v, want 909", wire["pipeline_run_id"])
	}
	for _, retired := range []string{"pipeline_name", "run_id"} {
		if _, present := wire[retired]; present {
			t.Errorf("%q must not be on the wire: %v", retired, wire)
		}
	}

	var response tickets.DispatchResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.WorkflowRunID.String() != "303" {
		t.Fatalf("decoded response = %+v", response)
	}
}

// A run with no execution linkage omits pipeline_run_id rather than reporting
// a zero — but it also cannot link the ticket, so the route reports the fault.
func TestDispatchHandlerMissingPipelineRunIsNotABadRequest(t *testing.T) {
	deps, store, _, binder := v3DispatchDeps(t)
	binder.result.Run.PipelineRunID = nil
	id := queuedTicket(t, store, "smoke")
	setRepositorySnapshot(t, store, id, 101)
	h := dispatch.NewHTTPHandler(deps, func(*http.Request) string { return "tdm" })

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, dispatchRequest(itoa(id)))
	if rec.Code == http.StatusUnprocessableEntity || rec.Code == http.StatusConflict {
		t.Errorf("a missing pipeline run is not the caller's fault, got %d: %s", rec.Code, rec.Body)
	}
	got, _, _ := store.Get(id)
	if got.State != tickets.StateQueued {
		t.Errorf("ticket must stay queued for the retry, got %s", got.State)
	}
}

func itoa(i int) string { return strconv.Itoa(i) }
