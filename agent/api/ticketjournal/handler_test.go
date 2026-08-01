package ticketjournal_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/concourse/concourse/agent/api/ticketjournal"
	"github.com/concourse/concourse/agent/api/tickets"
	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/atc/db"
)

const journalTeamID = 4

type stubTicketStore struct {
	found bool
	err   error
}

func (stub *stubTicketStore) Get(id int) (*tickets.Ticket, bool, error) {
	if stub.err != nil {
		return nil, false, stub.err
	}
	if !stub.found {
		return nil, false, nil
	}
	return &tickets.Ticket{ID: id, Title: "fix X", Repo: "org/repo"}, true, nil
}

type countingRunStore struct {
	listCalls int
	filters   []db.AgentWorkflowRunListFilter
	runs      []db.AgentWorkflowRun
	err       error
}

func (store *countingRunStore) List(
	_ context.Context,
	filter db.AgentWorkflowRunListFilter,
) ([]db.AgentWorkflowRun, error) {
	store.listCalls++
	store.filters = append(store.filters, filter)
	return store.runs, store.err
}

func at(minute int) time.Time {
	return time.Date(2026, 7, 31, 9, minute, 0, 0, time.UTC).UTC()
}

type testRun struct {
	ID           int64
	WorkflowName string
	CreatedAt    time.Time
	Status       db.AgentWorkflowRunStatus
	RetryOf      *int64
	TicketID     *int64
}

func durableRun(spec testRun) db.AgentWorkflowRun {
	status := spec.Status
	if status == "" {
		status = db.AgentWorkflowRunStatusSucceeded
	}
	ticketID := spec.TicketID
	if ticketID == nil {
		value := int64(42)
		ticketID = &value
	}
	run := db.AgentWorkflowRun{
		ID: snapshot.WorkflowRunID(spec.ID), TeamID: journalTeamID,
		WorkflowName: spec.WorkflowName, WorkflowVersion: 3,
		DefinitionContentHash: "hash", Status: status,
		OriginKind: "ticket", OriginReference: "42",
		CreatedAt: spec.CreatedAt, UpdatedAt: spec.CreatedAt,
		TicketID: ticketID, TicketReference: "ticket-42",
	}
	if spec.RetryOf != nil {
		source := snapshot.WorkflowRunID(*spec.RetryOf)
		run.RetryOfWorkflowRunID = &source
	}
	return run
}

func newHandlerWithStore(store ticketjournal.RunStore) *ticketjournal.Handler {
	return ticketjournal.NewHandler(
		&stubTicketStore{found: true}, store, ticketjournal.TrustedTeam{ID: journalTeamID},
	)
}

func newHandlerWithTicketRuns(specs []testRun) (*ticketjournal.Handler, *countingRunStore) {
	runs := make([]db.AgentWorkflowRun, 0, len(specs))
	for _, spec := range specs {
		runs = append(runs, durableRun(spec))
	}
	store := &countingRunStore{runs: runs}
	return newHandlerWithStore(store), store
}

func listRuns(handler *ticketjournal.Handler, ticketID string) *httptest.ResponseRecorder {
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.Form = map[string][]string{":ticket_id": {ticketID}}
	handler.ListRuns(recorder, request)
	return recorder
}

func decodeJournal(t *testing.T, recorder *httptest.ResponseRecorder) ticketjournal.JournalResponse {
	t.Helper()
	var response ticketjournal.JournalResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	return response
}

func TestTicketRunsAreChronologicalAcrossWorkflows(t *testing.T) {
	// Deliberately supplied newest-first, the way the paging store returns it.
	handler, _ := newHandlerWithTicketRuns([]testRun{
		{ID: 4, WorkflowName: "small-fix", CreatedAt: at(40)},
		{ID: 3, WorkflowName: "qa", CreatedAt: at(30)},
		{ID: 2, WorkflowName: "pr-create", CreatedAt: at(20)},
		{ID: 1, WorkflowName: "small-fix", CreatedAt: at(10)},
	})

	recorder := listRuns(handler, "42")
	if recorder.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", recorder.Code, recorder.Body)
	}
	response := decodeJournal(t, recorder)

	var order []string
	var names []string
	for _, run := range response.Runs {
		order = append(order, run.WorkflowRunID.String())
		names = append(names, run.WorkflowName)
	}
	want := []string{"1", "2", "3", "4"}
	if !reflect.DeepEqual(order, want) {
		t.Fatalf("expected chronological order %v, got %v", want, order)
	}

	// A repeated execution of the same workflow is its own entry, not a merge.
	if len(response.Runs) != 4 {
		t.Fatalf("expected four separate entries, got %d", len(response.Runs))
	}
	wantNames := []string{"small-fix", "pr-create", "qa", "small-fix"}
	if !reflect.DeepEqual(names, wantNames) {
		t.Fatalf("expected %v, got %v", wantNames, names)
	}
}

func TestTicketRunsOrderConcurrentOccurrencesStably(t *testing.T) {
	handler, _ := newHandlerWithTicketRuns([]testRun{
		{ID: 9, WorkflowName: "qa", CreatedAt: at(10)},
		{ID: 7, WorkflowName: "small-fix", CreatedAt: at(10)},
		{ID: 8, WorkflowName: "pr-create", CreatedAt: at(10)},
	})

	response := decodeJournal(t, listRuns(handler, "42"))
	var order []string
	for _, run := range response.Runs {
		order = append(order, run.WorkflowRunID.String())
	}
	if !reflect.DeepEqual(order, []string{"7", "8", "9"}) {
		t.Fatalf("concurrent runs must order by durable identity, got %v", order)
	}
}

func TestTicketRunsIssueOneQuery(t *testing.T) {
	store := &countingRunStore{runs: []db.AgentWorkflowRun{
		durableRun(testRun{ID: 1, WorkflowName: "small-fix", CreatedAt: at(10)}),
		durableRun(testRun{ID: 2, WorkflowName: "qa", CreatedAt: at(20)}),
		durableRun(testRun{ID: 3, WorkflowName: "pr-create", CreatedAt: at(30)}),
	}}
	handler := newHandlerWithStore(store)

	listRuns(handler, "42")

	if store.listCalls != 1 {
		t.Fatalf("the journal must not issue one query per workflow name; got %d calls", store.listCalls)
	}
	filter := store.filters[0]
	if filter.TicketID == nil || *filter.TicketID != 42 || filter.TeamID != journalTeamID {
		t.Fatalf("journal filter = %+v", filter)
	}
	if filter.WorkflowName != "" {
		t.Fatalf("the journal spans workflows; it must not scope to one, got %q", filter.WorkflowName)
	}
}

func TestTicketRunsElevateOutstandingWork(t *testing.T) {
	handler, _ := newHandlerWithTicketRuns([]testRun{
		{ID: 1, WorkflowName: "small-fix", CreatedAt: at(10), Status: db.AgentWorkflowRunStatusSucceeded},
		{ID: 2, WorkflowName: "qa", CreatedAt: at(20), Status: db.AgentWorkflowRunStatusRunning},
		{ID: 3, WorkflowName: "pr-create", CreatedAt: at(30), Status: db.AgentWorkflowRunStatusFailed},
		{ID: 4, WorkflowName: "qa", CreatedAt: at(40), Status: db.AgentWorkflowRunStatusAborted},
	})

	response := decodeJournal(t, listRuns(handler, "42"))
	got := map[string]bool{}
	for _, run := range response.Runs {
		got[run.WorkflowRunID.String()] = run.Outstanding
	}
	want := map[string]bool{"1": false, "2": true, "3": true, "4": true}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("outstanding = %v, want %v", got, want)
	}
}

func TestTicketRunsCarryRetryIdentityForGrouping(t *testing.T) {
	source := int64(1)
	handler, _ := newHandlerWithTicketRuns([]testRun{
		{ID: 1, WorkflowName: "small-fix", CreatedAt: at(10), Status: db.AgentWorkflowRunStatusFailed},
		{ID: 2, WorkflowName: "small-fix", CreatedAt: at(20), RetryOf: &source},
	})

	response := decodeJournal(t, listRuns(handler, "42"))
	if response.Runs[0].RetryOfWorkflowRunID != nil {
		t.Fatal("the source of a retry has no retry identity of its own")
	}
	if response.Runs[1].RetryOfWorkflowRunID == nil ||
		*response.Runs[1].RetryOfWorkflowRunID != snapshot.WorkflowRunID(1) {
		t.Fatalf("retry grouping identity = %+v", response.Runs[1].RetryOfWorkflowRunID)
	}
}

func TestTicketWithNoRunsIsAnEmptyJournal(t *testing.T) {
	handler, _ := newHandlerWithTicketRuns(nil)

	recorder := listRuns(handler, "42")
	if recorder.Code != http.StatusOK {
		t.Fatalf("a ticket with no runs is ordinary, got %d", recorder.Code)
	}
	response := decodeJournal(t, recorder)
	if response.TicketID != 42 || len(response.Runs) != 0 {
		t.Fatalf("response = %+v", response)
	}
}

func TestUnknownTicketIsNotFound(t *testing.T) {
	handler := ticketjournal.NewHandler(
		&stubTicketStore{}, &countingRunStore{}, ticketjournal.TrustedTeam{ID: journalTeamID},
	)

	if code := listRuns(handler, "42").Code; code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", code)
	}
}

func TestInvalidTicketIDIsRejected(t *testing.T) {
	handler, store := newHandlerWithTicketRuns(nil)

	for _, value := range []string{"0", "-1", "abc", ""} {
		if code := listRuns(handler, value).Code; code != http.StatusBadRequest {
			t.Fatalf("ticket_id %q: expected 400, got %d", value, code)
		}
	}
	if store.listCalls != 0 {
		t.Fatal("a rejected ticket id must not reach the run store")
	}
}

func TestNonGetIsRejected(t *testing.T) {
	handler, _ := newHandlerWithTicketRuns(nil)

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/", nil)
	request.Form = map[string][]string{":ticket_id": {"42"}}
	handler.ListRuns(recorder, request)

	if recorder.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405, got %d", recorder.Code)
	}
}

func TestRunStoreFailureIsInternal(t *testing.T) {
	handler := newHandlerWithStore(&countingRunStore{err: errors.New("boom")})

	if code := listRuns(handler, "42").Code; code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d", code)
	}
}

func TestARunFromAnotherTicketIsNeverPresented(t *testing.T) {
	other := int64(43)
	handler, _ := newHandlerWithTicketRuns([]testRun{
		{ID: 1, WorkflowName: "small-fix", CreatedAt: at(10), TicketID: &other},
	})

	if code := listRuns(handler, "42").Code; code != http.StatusInternalServerError {
		t.Fatalf("a foreign run must not be presented as this ticket's history, got %d", code)
	}
}

func TestARunFromAnotherTeamIsNeverPresented(t *testing.T) {
	run := durableRun(testRun{ID: 1, WorkflowName: "small-fix", CreatedAt: at(10)})
	run.TeamID = journalTeamID + 1
	handler := newHandlerWithStore(&countingRunStore{runs: []db.AgentWorkflowRun{run}})

	if code := listRuns(handler, "42").Code; code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d", code)
	}
}

func TestJournalReportsTheExactRevisionOfEachOccurrence(t *testing.T) {
	first := durableRun(testRun{ID: 1, WorkflowName: "small-fix", CreatedAt: at(10)})
	first.WorkflowVersion = 2
	second := durableRun(testRun{ID: 2, WorkflowName: "small-fix", CreatedAt: at(20)})
	second.WorkflowVersion = 5
	handler := newHandlerWithStore(&countingRunStore{runs: []db.AgentWorkflowRun{first, second}})

	response := decodeJournal(t, listRuns(handler, "42"))
	if response.Runs[0].WorkflowVersion != 2 || response.Runs[1].WorkflowVersion != 5 {
		t.Fatalf("each occurrence reports its own revision, got %d and %d",
			response.Runs[0].WorkflowVersion, response.Runs[1].WorkflowVersion)
	}
}

func TestJournalEmitsRunIDsAsStrings(t *testing.T) {
	// Durable run IDs exceed exact JS number range; the wire form is a quoted
	// decimal, matching every other run surface.
	big := int64(1)<<53 + 29
	handler, _ := newHandlerWithTicketRuns([]testRun{
		{ID: big, WorkflowName: "small-fix", CreatedAt: at(10)},
	})

	body := listRuns(handler, "42").Body.String()
	if !strings.Contains(body, `"workflow_run_id":"9007199254741021"`) {
		t.Fatalf("run ids must ride as quoted decimals, got %s", body)
	}
}
