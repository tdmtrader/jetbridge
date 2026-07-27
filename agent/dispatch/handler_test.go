package dispatch_test

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/concourse/concourse/agent/api/tickets"
	"github.com/concourse/concourse/agent/dispatch"
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
	if resp.RunID != 909 || resp.PipelineName == "" || resp.WorkflowRunID == nil {
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

	// ticket-input adaptation refusal -> 422, not 500
	deps.TicketPorts = staticPortResolver{err: errors.New("ambiguous ticket ports")}
	h = dispatch.NewHTTPHandler(deps, func(*http.Request) string { return "tdm" })
	id = queuedTicket(t, store, "smoke")
	setRepositorySnapshot(t, store, id, 101)
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

func TestDispatchHandlerV3KeepsLegacyRunFieldsAndAddsWorkflowRunIdentity(t *testing.T) {
	deps, store, _, _ := v3DispatchDeps(t)
	id := queuedTicket(t, store, "smoke")
	setRepositorySnapshot(t, store, id, 101)
	h := dispatch.NewHTTPHandler(deps, func(*http.Request) string { return "tdm" })

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, dispatchRequest(itoa(id)))
	if rec.Code != http.StatusCreated {
		t.Fatalf("v3 dispatch = %d body %s", rec.Code, rec.Body)
	}
	var response tickets.DispatchResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.RunID != 909 || response.PipelineName == "" || response.WorkflowRunID == nil || response.WorkflowRunID.String() != "303" {
		t.Fatalf("v3 response = %+v", response)
	}
}

func TestDispatchHandlerNonV3MapsToUnprocessableEntity(t *testing.T) {
	deps, store, workItems, binder := v3DispatchDeps(t)
	definition := deps.Workflows.(*fakeWorkflows).byName["smoke"]
	definition.SchemaVersion = 2
	countingStore := &countingTicketStore{Store: store}
	deps.Tickets = countingStore
	id := queuedTicket(t, store, "smoke")
	handler := dispatch.NewHTTPHandler(deps, func(*http.Request) string { return "tdm" })

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, dispatchRequest(itoa(id)))
	if recorder.Code != http.StatusUnprocessableEntity {
		t.Errorf("status = %d, want 422; body=%q", recorder.Code, recorder.Body.String())
	}
	wantBody := "workflow definition is not schema v3: workflow smoke v7 uses schema_version 2\n"
	if recorder.Body.String() != wantBody {
		t.Errorf("body = %q, want %q", recorder.Body.String(), wantBody)
	}
	if countingStore.reserveCalls != 0 {
		t.Errorf("ReserveDispatch calls = %d, want 0", countingStore.reserveCalls)
	}
	if workItems.CallCount() != 0 {
		t.Errorf("CaptureRevision calls = %d, want 0", workItems.CallCount())
	}
	if _, calls := binder.Calls(); len(calls) != 0 {
		t.Errorf("BindAndCreate calls = %d, want 0", len(calls))
	}
	got, found, getErr := store.Get(id)
	if getErr != nil || !found {
		t.Fatalf("read ticket after rejection: found=%v err=%v", found, getErr)
	}
	if got.WorkflowVersion != nil || got.WorkflowDefinitionID != nil ||
		got.WorkItemSnapshotID != nil || got.RepositorySnapshotID != nil ||
		got.WorkflowRunID != nil || got.PipelineRunID != nil ||
		got.DispatchReservationKey != "" {
		t.Errorf("non-v3 HTTP rejection linked dispatch state: %+v", got)
	}
}

func itoa(i int) string { return strconv.Itoa(i) }
