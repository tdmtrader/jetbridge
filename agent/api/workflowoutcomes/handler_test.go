package workflowoutcomes_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/concourse/concourse/agent/api/workflowoutcomes"
	"github.com/concourse/concourse/agent/snapshot"
)

type authorizerStub struct {
	run    bool
	output bool
	err    error
}

func (stub authorizerStub) AuthorizeRun(context.Context, int, string, snapshot.WorkflowRunID) (bool, error) {
	return stub.run, stub.err
}

func (stub authorizerStub) AuthorizeOutput(context.Context, int, snapshot.WorkflowRunID, snapshot.SnapshotID) (bool, error) {
	return stub.output, stub.err
}

func TestHandlerListsTeamScopedRunOutcomes(t *testing.T) {
	store := workflowoutcomes.NewMemoryStore(fixedNow)
	_, _, _ = store.Record(context.Background(), 7, workflowoutcomes.RecordRequest{
		WorkflowRunID: largeRunID, OutputSnapshotID: largeSnapshotID,
		Disposition:      workflowoutcomes.DispositionAccepted,
		PublicationState: workflowoutcomes.PublicationNotRequested, Actor: "watcher",
	})
	handler := mustOutcomeHandler(t, store, authorizerStub{run: true, output: true})
	request := outcomeRequest(http.MethodGet, largeRunID.String(), "", "")
	response := httptest.NewRecorder()
	handler.List(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status/body = %d/%s", response.Code, response.Body.String())
	}
	var outcomes []workflowoutcomes.Outcome
	if err := json.Unmarshal(response.Body.Bytes(), &outcomes); err != nil || len(outcomes) != 1 || outcomes[0].OutputSnapshotID != largeSnapshotID {
		t.Fatalf("outcomes = %+v, %v", outcomes, err)
	}
}

func TestHandlerAcceptsEstablishedConcourseWorkflowIdentifiers(t *testing.T) {
	store := workflowoutcomes.NewMemoryStore(fixedNow)
	handler := mustOutcomeHandler(t, store, authorizerStub{run: true, output: true})
	request := outcomeRequest(http.MethodGet, "21", "", "")
	request.URL.RawQuery = url.Values{
		":workflow_name":   {"review.release_α"},
		":workflow_run_id": {"21"},
	}.Encode()
	response := httptest.NewRecorder()
	handler.List(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status/body = %d/%s", response.Code, response.Body.String())
	}
}

func TestHandlerRecordsAuthorizedHumanUpdateAndDerivesActor(t *testing.T) {
	store := workflowoutcomes.NewMemoryStore(fixedNow)
	_, _, _ = store.Record(context.Background(), 7, workflowoutcomes.RecordRequest{
		WorkflowRunID: largeRunID, OutputSnapshotID: largeSnapshotID,
		Disposition:      workflowoutcomes.DispositionAccepted,
		PublicationState: workflowoutcomes.PublicationNotRequested, Actor: "watcher",
	})
	handler := mustOutcomeHandler(t, store, authorizerStub{run: true, output: true})
	modification := snapshot.SnapshotID(9007199254740997)
	body := `{"disposition":"merged","human_modified":true,"modification_snapshot_id":"` + modification.String() + `","labels":["dogfood"]}`
	request := outcomeRequest(http.MethodPut, largeRunID.String(), largeSnapshotID.String(), body)
	response := httptest.NewRecorder()
	handler.Record(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status/body = %d/%s", response.Code, response.Body.String())
	}
	var outcome workflowoutcomes.Outcome
	if err := json.Unmarshal(response.Body.Bytes(), &outcome); err != nil {
		t.Fatal(err)
	}
	if outcome.Actor != "alice" || outcome.InterventionCount != 1 || outcome.ModificationSnapshotID == nil || *outcome.ModificationSnapshotID != modification {
		t.Fatalf("outcome = %+v", outcome)
	}

	replay := httptest.NewRecorder()
	handler.Record(replay, outcomeRequest(http.MethodPut, largeRunID.String(), largeSnapshotID.String(), body))
	if replay.Code != http.StatusOK {
		t.Fatalf("replay = %d/%s", replay.Code, replay.Body.String())
	}
	stored, _, _ := store.Get(context.Background(), 7, largeRunID, largeSnapshotID)
	if stored.InterventionCount != 1 || stored.Revision != 2 {
		t.Fatalf("replay changed audit counters: %+v", stored)
	}
}

func TestHandlerRejectsMemberClaimsAboutAuthoritativePublicationState(t *testing.T) {
	store := workflowoutcomes.NewMemoryStore(fixedNow)
	handler := mustOutcomeHandler(t, store, authorizerStub{run: true, output: true})
	response := httptest.NewRecorder()
	handler.Record(response, outcomeRequest(
		http.MethodPut,
		"21",
		"22",
		`{"disposition":"merged","publication_state":"published","human_modified":false}`,
	))
	if response.Code != http.StatusBadRequest {
		t.Fatalf("status/body = %d/%s", response.Code, response.Body.String())
	}
	if _, found, err := store.Get(context.Background(), 7, 21, 22); err != nil || found {
		t.Fatalf("member publication claim was persisted: found=%t err=%v", found, err)
	}
}

func TestHandlerConcealsUnauthorizedRunOutputAndModification(t *testing.T) {
	store := workflowoutcomes.NewMemoryStore(fixedNow)
	for name, authorizer := range map[string]authorizerStub{
		"run":    {run: false, output: true},
		"output": {run: true, output: false},
	} {
		t.Run(name, func(t *testing.T) {
			handler := mustOutcomeHandler(t, store, authorizer)
			body := `{"disposition":"accepted","human_modified":true,"modification_snapshot_id":"23"}`
			response := httptest.NewRecorder()
			handler.Record(response, outcomeRequest(http.MethodPut, "21", "22", body))
			if response.Code != http.StatusNotFound {
				t.Fatalf("status/body = %d/%s", response.Code, response.Body.String())
			}
		})
	}
}

func TestHandlerRejectsMalformedOrUntrustedRequests(t *testing.T) {
	store := workflowoutcomes.NewMemoryStore(fixedNow)
	handler := mustOutcomeHandler(t, store, authorizerStub{run: true, output: true})
	tests := []struct {
		name   string
		method string
		runID  string
		output string
		body   string
		status int
	}{
		{name: "method", method: http.MethodPost, runID: "21", output: "22", body: `{}`, status: http.StatusMethodNotAllowed},
		{name: "run", method: http.MethodPut, runID: "0", output: "22", body: `{}`, status: http.StatusBadRequest},
		{name: "snapshot", method: http.MethodPut, runID: "21", output: "nope", body: `{}`, status: http.StatusBadRequest},
		{name: "unknown field", method: http.MethodPut, runID: "21", output: "22", body: `{"disposition":"accepted","publication_state":"not_requested","unknown":true}`, status: http.StatusBadRequest},
		{name: "invalid disposition", method: http.MethodPut, runID: "21", output: "22", body: `{"disposition":"maybe"}`, status: http.StatusBadRequest},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response := httptest.NewRecorder()
			handler.Record(response, outcomeRequest(test.method, test.runID, test.output, test.body))
			if response.Code != test.status {
				t.Fatalf("status/body = %d/%s, want %d", response.Code, response.Body.String(), test.status)
			}
		})
	}
}

func TestHandlerRejectsAValidPrefixWithAnOversizedTrailingBody(t *testing.T) {
	store := workflowoutcomes.NewMemoryStore(fixedNow)
	handler := mustOutcomeHandler(t, store, authorizerStub{run: true, output: true})
	body := `{"disposition":"accepted"}` + strings.Repeat(" ", 64<<10)
	response := httptest.NewRecorder()
	handler.Record(response, outcomeRequest(http.MethodPut, "21", "22", body))
	if response.Code != http.StatusBadRequest {
		t.Fatalf("status/body = %d/%s", response.Code, response.Body.String())
	}
	if _, found, err := store.Get(context.Background(), 7, 21, 22); err != nil || found {
		t.Fatalf("oversized request was persisted: found=%t err=%v", found, err)
	}
}

func TestHandlerMapsDependencyErrorsWithoutDisclosure(t *testing.T) {
	store := workflowoutcomes.NewMemoryStore(fixedNow)
	handler := mustOutcomeHandler(t, store, authorizerStub{run: true, output: true, err: errors.New("secret DSN")})
	response := httptest.NewRecorder()
	handler.List(response, outcomeRequest(http.MethodGet, "21", "", ""))
	if response.Code != http.StatusInternalServerError || strings.Contains(response.Body.String(), "secret DSN") {
		t.Fatalf("status/body = %d/%s", response.Code, response.Body.String())
	}
}

func mustOutcomeHandler(t *testing.T, store workflowoutcomes.Store, authorizer workflowoutcomes.Authorizer) *workflowoutcomes.Handler {
	t.Helper()
	handler, err := workflowoutcomes.NewHandler(workflowoutcomes.HandlerConfig{
		TeamID: 7, TeamName: "main", Store: store, Authorizer: authorizer,
		Identity: func(*http.Request) (string, error) { return "alice", nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	return handler
}

func outcomeRequest(method, runID, outputID, body string) *http.Request {
	path := "/api/v1/agent/workflows/review/runs/" + runID + "/outcomes"
	values := url.Values{":workflow_name": {"review"}, ":workflow_run_id": {runID}}
	if outputID != "" {
		path = "/api/v1/agent/workflows/review/runs/" + runID + "/outputs/" + outputID + "/outcome"
		values[":snapshot_id"] = []string{outputID}
	}
	request := httptest.NewRequest(method, path+"?"+values.Encode(), strings.NewReader(body))
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	return request
}

func fixedNow() time.Time { return time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC) }
