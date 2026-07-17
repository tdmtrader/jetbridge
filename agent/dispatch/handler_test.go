package dispatch_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"testing"

	"github.com/concourse/concourse/agent/api/tickets"
	"github.com/concourse/concourse/agent/dispatch"
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

	// bad param -> 400
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, dispatchRequest("zero"))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("bad id = %d, want 400", rec.Code)
	}
}

func itoa(i int) string { return strconv.Itoa(i) }
