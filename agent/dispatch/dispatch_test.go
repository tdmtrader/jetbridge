package dispatch_test

import (
	"strings"
	"sync"

	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/concourse/concourse/agent/api/tickets"
	"github.com/concourse/concourse/agent/api/tickets/ticketstest"
	"github.com/concourse/concourse/agent/dispatch"
	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/agent/workflow"
	"github.com/concourse/concourse/agent/workflowrun"
	"github.com/concourse/concourse/agent/workitem"
	"github.com/concourse/concourse/atc/db"
)

type fakeWorkflows struct {
	byName map[string]*workflow.Definition
}

func (f *fakeWorkflows) Live(name string) (*workflow.Definition, bool, error) {
	d, ok := f.byName[name]
	return d, ok, nil
}

func (f *fakeWorkflows) Get(name string, version int) (*workflow.Definition, bool, error) {
	d, ok := f.byName[name]
	if !ok || d.Version != version {
		return nil, false, nil
	}
	return d, true, nil
}

type fakeWorkItemCapturer struct {
	mu     sync.Mutex
	calls  int
	store  tickets.Store
	result workitem.CaptureResult
	found  bool
	err    error
}

func (fake *fakeWorkItemCapturer) CaptureRevision(_ context.Context, ticketID int) (workitem.CaptureResult, bool, error) {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	fake.calls++
	result := fake.result
	result.TicketID = ticketID
	if fake.store != nil {
		ticket, found, err := fake.store.Get(ticketID)
		if err != nil || !found {
			return workitem.CaptureResult{}, found, err
		}
		result.Revision = ticket.Revision
	}
	return result, fake.found, fake.err
}

func (fake *fakeWorkItemCapturer) CallCount() int {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	return fake.calls
}

type fakeWorkflowBinder struct {
	mu       sync.Mutex
	calls    []workflowrun.BindRequest
	contexts []workflowrun.AdmissionContext
	result   workflowrun.BindResult
	err      error
	// errOnce makes err apply to the FIRST bind only, so a loop test can
	// deny one ticket and admit the next.
	errOnce bool
}

type fakeWorkflowRunCanceler struct {
	mu    sync.Mutex
	calls []snapshot.WorkflowRunID
	teams []int
	found bool
	err   error
}

func (fake *fakeWorkflowRunCanceler) Cancel(
	_ context.Context,
	teamID int,
	runID snapshot.WorkflowRunID,
) (db.AgentWorkflowRun, bool, error) {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	fake.teams = append(fake.teams, teamID)
	fake.calls = append(fake.calls, runID)
	return db.AgentWorkflowRun{ID: runID, TeamID: teamID, Status: db.AgentWorkflowRunStatusAborted}, fake.found, fake.err
}

func (fake *fakeWorkflowRunCanceler) Calls() ([]int, []snapshot.WorkflowRunID) {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	return append([]int(nil), fake.teams...), append([]snapshot.WorkflowRunID(nil), fake.calls...)
}

type ticketStoreRaceBeforeRunLink struct {
	tickets.Store
	once sync.Once
}

func (store *ticketStoreRaceBeforeRunLink) RecordDispatchRun(
	ctx context.Context,
	id int,
	reservationKey string,
	workflowRunID snapshot.WorkflowRunID,
	pipelineRunID int,
) error {
	store.once.Do(func() {
		_ = store.Store.Transition(id, tickets.StateQueued, tickets.StateDraft, tickets.TransitionMeta{})
	})
	return store.Store.RecordDispatchRun(ctx, id, reservationKey, workflowRunID, pipelineRunID)
}

type ticketStoreRaceBeforeRunning struct {
	tickets.Store
	once sync.Once
}

type ticketStoreRecordFailure struct {
	tickets.Store
	err error
}

type countingTicketStore struct {
	tickets.Store
	reserveCalls int
}

func (store *countingTicketStore) ReserveDispatch(
	ctx context.Context,
	id int,
	request tickets.DispatchReservationRequest,
) (tickets.DispatchReservation, error) {
	store.reserveCalls++
	return store.Store.ReserveDispatch(ctx, id, request)
}

func (store ticketStoreRecordFailure) RecordDispatchRun(
	context.Context,
	int,
	string,
	snapshot.WorkflowRunID,
	int,
) error {
	return store.err
}

func (store *ticketStoreRaceBeforeRunning) Transition(
	id int,
	from tickets.State,
	to tickets.State,
	meta tickets.TransitionMeta,
) error {
	if from == tickets.StateQueued && to == tickets.StateRunning {
		store.once.Do(func() {
			_ = store.Store.Transition(id, tickets.StateQueued, tickets.StateDraft, tickets.TransitionMeta{})
		})
	}
	return store.Store.Transition(id, from, to, meta)
}

func (fake *fakeWorkflowBinder) BindAndCreate(
	_ context.Context,
	admission workflowrun.AdmissionContext,
	request workflowrun.BindRequest,
) (workflowrun.BindResult, error) {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	request.Inputs = cloneSnapshotIDs(request.Inputs)
	fake.calls = append(fake.calls, request)
	fake.contexts = append(fake.contexts, admission)
	err := fake.err
	if fake.errOnce {
		fake.err = nil
	}
	return fake.result, err
}

func (fake *fakeWorkflowBinder) Calls() ([]workflowrun.AdmissionContext, []workflowrun.BindRequest) {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	return append([]workflowrun.AdmissionContext(nil), fake.contexts...), append([]workflowrun.BindRequest(nil), fake.calls...)
}

func cloneSnapshotIDs(values map[string]snapshot.SnapshotID) map[string]snapshot.SnapshotID {
	cloned := make(map[string]snapshot.SnapshotID, len(values))
	for key, value := range values {
		cloned[key] = value
	}
	return cloned
}

type portSpec struct{ name, portType string }

// definitionWithInputs compiles a one-step schema-v3 workflow whose authored
// input signature is exactly `ports` — the knob every ticket-dispatchability
// case turns.
func definitionWithInputs(t *testing.T, ports ...portSpec) *workflow.Definition {
	t.Helper()
	var declared, types strings.Builder
	names := make([]string, 0, len(ports))
	for _, port := range ports {
		fmt.Fprintf(&declared, "  - name: %s\n    type: %s\n", port.name, port.portType)
		fmt.Fprintf(&types, "      %s: {type: %s}\n", port.name, port.portType)
		names = append(names, port.name)
	}
	raw := fmt.Sprintf(`schema_version: 3
name: smoke
signature_version: 1
inputs:
%soutputs:
  - name: report
    type: opaque/v1
    from: report
plan:
  - agent: work
    function_id: work
    prompt: Do the captured work.
    inputs: [%s]
    outputs: [report]
    input_types:
%s    output_types:
      report: opaque/v1
`, declared.String(), strings.Join(names, ", "), types.String())
	compiled, err := workflow.ParseCompiled([]byte(raw))
	if err != nil {
		t.Fatalf("parse v3 definition: %v", err)
	}
	return &workflow.Definition{
		ID: 41, Name: "smoke", Version: 7, SchemaVersion: 3, SignatureVersion: 1,
		ContentHash: strings.Repeat("a", 64), Live: true, Compiled: *compiled,
	}
}

func v3Definition(t *testing.T, workItemPort, repositoryPort string) *workflow.Definition {
	t.Helper()
	return definitionWithInputs(t,
		portSpec{workItemPort, "work-item/v1"},
		portSpec{repositoryPort, "repository/v1"},
	)
}

func v3DispatchDeps(t *testing.T) (dispatch.Deps, *ticketstest.MemoryStore, *fakeWorkItemCapturer, *fakeWorkflowBinder) {
	t.Helper()
	store := ticketstest.NewMemoryStore()
	definition := v3Definition(t, "work-item", "repository")
	workItems := &fakeWorkItemCapturer{store: store, found: true, result: workitem.CaptureResult{
		Revision: 4, Snapshot: snapshot.Snapshot{ID: snapshot.SnapshotID(202), Type: snapshot.TypeRef("work-item/v1")},
	}}
	pipelineRunID := 909
	binder := &fakeWorkflowBinder{result: workflowrun.BindResult{Created: true, Run: db.AgentWorkflowRun{
		ID: snapshot.WorkflowRunID(303), TeamID: 1, TeamName: "main",
		WorkflowDefinitionID: definition.ID, WorkflowName: definition.Name,
		WorkflowVersion: definition.Version, SchemaVersion: 3, SignatureVersion: 1,
		ParameterizedConfigHash: strings.Repeat("b", 64), PipelineRunID: &pipelineRunID,
		Status: db.AgentWorkflowRunStatusRunning,
	}}}
	deps := dispatch.Deps{
		Tickets: store, Workflows: &fakeWorkflows{byName: map[string]*workflow.Definition{"smoke": definition}},
		TeamID: 1, TeamName: "main", WorkItems: workItems, WorkflowBinder: binder,
		WorkflowCanceler: &fakeWorkflowRunCanceler{found: true},
	}
	return deps, store, workItems, binder
}

func setRepositorySnapshot(t *testing.T, store *ticketstest.MemoryStore, ticketID int, id snapshot.SnapshotID) {
	t.Helper()
	if err := store.Update(ticketID, tickets.Update{RepositorySnapshotID: &id}); err != nil {
		t.Fatalf("select repository snapshot: %v", err)
	}
}

func TestDispatchOneSchemaThreeBindsCapturedSnapshotsThroughGenericBinder(t *testing.T) {
	deps, store, workItems, binder := v3DispatchDeps(t)
	id := queuedTicket(t, store, "smoke")
	setRepositorySnapshot(t, store, id, snapshot.SnapshotID(101))

	result, err := dispatch.DispatchOne(context.Background(), deps, id, "admin")
	if err != nil {
		t.Fatalf("DispatchOne: %v", err)
	}
	if result.WorkflowRunID != snapshot.WorkflowRunID(303) ||
		result.PipelineRunID == nil || *result.PipelineRunID != 909 {
		t.Fatalf("result = %+v", result)
	}
	if workItems.CallCount() != 1 {
		t.Fatalf("work-item captures = %d, want 1", workItems.CallCount())
	}

	admissions, calls := binder.Calls()
	if len(calls) != 1 {
		t.Fatalf("binder calls = %d, want 1", len(calls))
	}
	call := calls[0]
	if call.WorkflowName != "smoke" || call.Version == nil || *call.Version != 7 || call.FunctionID != "" {
		t.Fatalf("binder target = %+v", call)
	}
	if call.Inputs["work-item"] != snapshot.SnapshotID(202) || call.Inputs["repository"] != snapshot.SnapshotID(101) {
		t.Fatalf("binder inputs = %+v", call.Inputs)
	}
	if call.IdempotencyKey == "" || admissions[0].TeamID != 1 || admissions[0].TeamName != "main" ||
		admissions[0].CreatedBy != "admin" || admissions[0].Origin.Kind != "ticket" || admissions[0].Origin.Reference != fmt.Sprint(id) {
		t.Fatalf("admission=%+v request=%+v", admissions[0], call)
	}

	got, _, _ := store.Get(id)
	if got.State != tickets.StateRunning || got.WorkflowVersion == nil || *got.WorkflowVersion != 7 ||
		got.WorkflowDefinitionID == nil || *got.WorkflowDefinitionID != 41 ||
		got.WorkflowRunID == nil || *got.WorkflowRunID != snapshot.WorkflowRunID(303) ||
		got.WorkItemSnapshotID == nil || *got.WorkItemSnapshotID != snapshot.SnapshotID(202) ||
		got.RepositorySnapshotID == nil || *got.RepositorySnapshotID != snapshot.SnapshotID(101) ||
		got.PipelineRunID == nil || *got.PipelineRunID != 909 || got.DispatchReservationKey == "" {
		t.Fatalf("durable ticket linkage = %+v", got)
	}
}

// Port names are NOT reserved in schema v3. Inference is by TYPE and is the
// only rule — there is no configurable resolver to disagree with it.
func TestDispatchOneInfersTicketPortsByTypeWhateverTheyAreNamed(t *testing.T) {
	deps, store, _, binder := v3DispatchDeps(t)
	definition := v3Definition(t, "request", "source")
	deps.Workflows = &fakeWorkflows{byName: map[string]*workflow.Definition{"smoke": definition}}
	id := queuedTicket(t, store, "smoke")
	setRepositorySnapshot(t, store, id, snapshot.SnapshotID(101))

	if _, err := dispatch.DispatchOne(context.Background(), deps, id, "admin"); err != nil {
		t.Fatalf("DispatchOne: %v", err)
	}
	_, calls := binder.Calls()
	if calls[0].Inputs["request"] != snapshot.SnapshotID(202) || calls[0].Inputs["source"] != snapshot.SnapshotID(101) {
		t.Fatalf("type-inferred inputs = %+v", calls[0].Inputs)
	}
}

// A workflow that does not present exactly one work-item/v1 and one
// repository/v1 input is not something a ticket can run, and saying so is a
// pure read of the authored signature — so it happens before ANY side effect.
func TestDispatchOneRefusesUndispatchableSignatureBeforeSideEffects(t *testing.T) {
	for _, tc := range []struct {
		name        string
		ports       []portSpec
		wantMessage string
	}{
		{
			name:        "no work-item input",
			ports:       []portSpec{{"repository", "repository/v1"}},
			wantMessage: "declares 0 work-item/v1 and 1 repository/v1 inputs",
		},
		{
			name:        "no repository input",
			ports:       []portSpec{{"work-item", "work-item/v1"}},
			wantMessage: "declares 1 work-item/v1 and 0 repository/v1 inputs",
		},
		{
			name: "two work-item inputs is ambiguous",
			ports: []portSpec{
				{"work-item", "work-item/v1"},
				{"other-work-item", "work-item/v1"},
				{"repository", "repository/v1"},
			},
			wantMessage: "declares 2 work-item/v1 and 1 repository/v1 inputs",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			deps, store, workItems, binder := v3DispatchDeps(t)
			deps.Workflows = &fakeWorkflows{byName: map[string]*workflow.Definition{
				"smoke": definitionWithInputs(t, tc.ports...),
			}}
			countingStore := &countingTicketStore{Store: store}
			deps.Tickets = countingStore
			id := queuedTicket(t, store, "smoke")
			setRepositorySnapshot(t, store, id, snapshot.SnapshotID(101))

			_, err := dispatch.DispatchOne(context.Background(), deps, id, "admin")
			if !errors.Is(err, dispatch.ErrNotTicketDispatchable) {
				t.Fatalf("error = %v, want ErrNotTicketDispatchable", err)
			}
			if !strings.Contains(err.Error(), tc.wantMessage) {
				t.Errorf("error %q must say which way the signature is wrong (%q)", err, tc.wantMessage)
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
			got, _, _ := store.Get(id)
			if got.DispatchReservationKey != "" || got.WorkItemSnapshotID != nil || got.WorkflowRunID != nil {
				t.Errorf("refusal linked dispatch state: %+v", got)
			}
		})
	}
}

// Schema admission belongs to the binder, which is the one authority that
// actually creates the run. Dispatch no longer keeps its own copy of the gate,
// so a non-v3 definition is refused BY THE BINDER and surfaces as its error.
func TestDispatchOneLeavesSchemaAdmissionToTheBinder(t *testing.T) {
	deps, store, _, binder := v3DispatchDeps(t)
	deps.Workflows.(*fakeWorkflows).byName["smoke"].SchemaVersion = 2
	binder.err = fmt.Errorf("%w: workflow smoke v7 uses schema_version 2", workflowrun.ErrPlatformFailure)
	id := queuedTicket(t, store, "smoke")
	setRepositorySnapshot(t, store, id, snapshot.SnapshotID(101))

	_, err := dispatch.DispatchOne(context.Background(), deps, id, "admin")
	if !errors.Is(err, workflowrun.ErrPlatformFailure) {
		t.Fatalf("error = %v, want the binder's own refusal", err)
	}
	if _, calls := binder.Calls(); len(calls) != 1 {
		t.Fatalf("the binder must be consulted, calls = %d", len(calls))
	}
	got, _, _ := store.Get(id)
	if got.State != tickets.StateQueued || got.WorkflowRunID != nil {
		t.Errorf("refused ticket must stay queued and unlinked: %+v", got)
	}
}

// A run with no execution linkage is legal (an admission that never reached a
// pipeline run). It is not a dispatch CONFLICT, and the workflow-run identity
// still comes back so a caller can inspect it.
func TestDispatchOneMissingPipelineRunIsNotAConflict(t *testing.T) {
	deps, store, _, binder := v3DispatchDeps(t)
	binder.result.Run.PipelineRunID = nil
	id := queuedTicket(t, store, "smoke")
	setRepositorySnapshot(t, store, id, snapshot.SnapshotID(101))

	result, err := dispatch.DispatchOne(context.Background(), deps, id, "admin")
	if err == nil {
		t.Fatal("a ticket cannot be linked without execution linkage; want a retryable error")
	}
	if errors.Is(err, tickets.ErrDispatchConflict) {
		t.Errorf("a missing pipeline run is not a conflict: %v", err)
	}
	if result.WorkflowRunID != snapshot.WorkflowRunID(303) || result.PipelineRunID != nil {
		t.Errorf("result must still carry the identity: %+v", result)
	}
	got, _, _ := store.Get(id)
	if got.State != tickets.StateQueued || got.DispatchReservationKey == "" {
		t.Errorf("the reservation must survive for the next pass: %+v", got)
	}
}

func TestDispatchOneSchemaThreeReservesAndDefersUntilRepositorySnapshotSelected(t *testing.T) {
	deps, store, workItems, binder := v3DispatchDeps(t)
	id := queuedTicket(t, store, "smoke")

	_, err := dispatch.DispatchOne(context.Background(), deps, id, "admin")
	if !errors.Is(err, dispatch.ErrInputsPending) {
		t.Fatalf("error = %v, want ErrInputsPending", err)
	}
	got, _, _ := store.Get(id)
	if got.State != tickets.StateQueued || got.DispatchReservationKey == "" || got.WorkflowDefinitionID == nil ||
		got.WorkItemSnapshotID != nil || got.WorkflowRunID != nil {
		t.Fatalf("pending reservation = %+v", got)
	}
	if workItems.CallCount() != 0 {
		t.Fatal("work item must not be captured until all durable adapter inputs are selected")
	}
	_, calls := binder.Calls()
	if len(calls) != 0 {
		t.Fatal("binder must not run with a missing repository snapshot")
	}

	setRepositorySnapshot(t, store, id, snapshot.SnapshotID(101))
	other := snapshot.SnapshotID(102)
	if err := store.Update(id, tickets.Update{RepositorySnapshotID: &other}); !errors.Is(err, tickets.ErrDispatchConflict) {
		t.Fatalf("changing a reserved repository snapshot = %v, want ErrDispatchConflict", err)
	}
	if _, err := dispatch.DispatchOne(context.Background(), deps, id, "admin"); err != nil {
		t.Fatalf("resume dispatch: %v", err)
	}
}

func TestDispatchOneSchemaThreeConcurrentCallsConvergeOnOneReservation(t *testing.T) {
	deps, store, _, binder := v3DispatchDeps(t)
	id := queuedTicket(t, store, "smoke")
	setRepositorySnapshot(t, store, id, snapshot.SnapshotID(101))

	const callers = 12
	errs := make(chan error, callers)
	var wait sync.WaitGroup
	for index := 0; index < callers; index++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			_, err := dispatch.DispatchOne(context.Background(), deps, id, "admin")
			errs <- err
		}()
	}
	wait.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent dispatch: %v", err)
		}
	}
	_, calls := binder.Calls()
	if len(calls) == 0 {
		t.Fatal("binder was not called")
	}
	wantKey := calls[0].IdempotencyKey
	for _, call := range calls[1:] {
		if call.IdempotencyKey != wantKey || call.Inputs["repository"] != snapshot.SnapshotID(101) || call.Inputs["work-item"] != snapshot.SnapshotID(202) {
			t.Fatalf("concurrent bind diverged: first=%+v current=%+v", calls[0], call)
		}
	}
	got, _, _ := store.Get(id)
	if got.State != tickets.StateRunning || got.DispatchReservationKey != wantKey ||
		got.WorkflowRunID == nil || *got.WorkflowRunID != snapshot.WorkflowRunID(303) {
		t.Fatalf("concurrent result = %+v", got)
	}
}

func TestDispatchOneSchemaThreeCancelsRunWhenTicketLosesReservationBeforeLink(t *testing.T) {
	deps, store, _, _ := v3DispatchDeps(t)
	id := queuedTicket(t, store, "smoke")
	setRepositorySnapshot(t, store, id, snapshot.SnapshotID(101))
	canceler := &fakeWorkflowRunCanceler{found: true}
	deps.WorkflowCanceler = canceler
	deps.Tickets = &ticketStoreRaceBeforeRunLink{Store: store}

	_, err := dispatch.DispatchOne(context.Background(), deps, id, "admin")
	if !errors.Is(err, tickets.ErrDispatchConflict) {
		t.Fatalf("DispatchOne error = %v, want dispatch conflict", err)
	}
	teams, runs := canceler.Calls()
	if len(runs) != 1 || runs[0] != snapshot.WorkflowRunID(303) || teams[0] != 1 {
		t.Fatalf("cancellation calls teams=%v runs=%v, want exact team/run", teams, runs)
	}
}

func TestDispatchOneSchemaThreeCancelsLinkedRunWhenTicketIsUnqueuedBeforeRunning(t *testing.T) {
	deps, store, _, _ := v3DispatchDeps(t)
	id := queuedTicket(t, store, "smoke")
	setRepositorySnapshot(t, store, id, snapshot.SnapshotID(101))
	canceler := &fakeWorkflowRunCanceler{found: true}
	deps.WorkflowCanceler = canceler
	deps.Tickets = &ticketStoreRaceBeforeRunning{Store: store}

	_, err := dispatch.DispatchOne(context.Background(), deps, id, "admin")
	if !errors.Is(err, tickets.ErrStaleTransition) {
		t.Fatalf("DispatchOne error = %v, want stale transition", err)
	}
	teams, runs := canceler.Calls()
	if len(runs) != 1 || runs[0] != snapshot.WorkflowRunID(303) || teams[0] != 1 {
		t.Fatalf("cancellation calls teams=%v runs=%v, want exact team/run", teams, runs)
	}
}

func TestDispatchOneSchemaThreeKeepsOwnedRunRetryableWhenRunLinkWriteFails(t *testing.T) {
	deps, store, _, _ := v3DispatchDeps(t)
	id := queuedTicket(t, store, "smoke")
	setRepositorySnapshot(t, store, id, snapshot.SnapshotID(101))
	canceler := &fakeWorkflowRunCanceler{found: true}
	deps.WorkflowCanceler = canceler
	wantErr := errors.New("temporary database failure")
	deps.Tickets = ticketStoreRecordFailure{Store: store, err: wantErr}

	_, err := dispatch.DispatchOne(context.Background(), deps, id, "admin")
	if !errors.Is(err, wantErr) {
		t.Fatalf("DispatchOne error = %v, want record failure", err)
	}
	if _, runs := canceler.Calls(); len(runs) != 0 {
		t.Fatalf("owned retryable run was cancelled: %v", runs)
	}
	got, _, _ := store.Get(id)
	if got.State != tickets.StateQueued || got.DispatchReservationKey == "" || got.WorkflowRunID != nil || got.PipelineRunID != nil {
		t.Fatalf("retryable reservation = %+v", got)
	}
}

func TestDispatchOneSchemaThreeKeepsCapturedInputAcrossLaterTicketEdits(t *testing.T) {
	deps, store, workItems, binder := v3DispatchDeps(t)
	id := queuedTicket(t, store, "smoke")
	setRepositorySnapshot(t, store, id, snapshot.SnapshotID(101))
	if _, err := dispatch.DispatchOne(context.Background(), deps, id, "admin"); err != nil {
		t.Fatalf("first dispatch: %v", err)
	}

	updated := "edited after immutable capture"
	if err := store.Update(id, tickets.Update{Body: &updated}); err != nil {
		t.Fatal(err)
	}
	if _, err := dispatch.DispatchOne(context.Background(), deps, id, "admin"); err != nil {
		t.Fatalf("idempotent resume after edit: %v", err)
	}
	if workItems.CallCount() != 1 {
		t.Fatalf("later ticket edit caused recapture: captures=%d", workItems.CallCount())
	}
	_, calls := binder.Calls()
	if len(calls) != 2 || calls[0].Inputs["work-item"] != calls[1].Inputs["work-item"] ||
		calls[0].IdempotencyKey != calls[1].IdempotencyKey {
		t.Fatalf("active run inputs changed after edit: %+v", calls)
	}
}

func TestDispatchOneSchemaThreeMapsGenericBudgetDenialToRetryableTicketDeferral(t *testing.T) {
	deps, store, _, binder := v3DispatchDeps(t)
	id := queuedTicket(t, store, "smoke")
	setRepositorySnapshot(t, store, id, snapshot.SnapshotID(101))
	binder.err = workflowrun.ErrBudgetDenied

	_, err := dispatch.DispatchOne(context.Background(), deps, id, "admin")
	if !errors.Is(err, dispatch.ErrBudgetExhausted) {
		t.Fatalf("error = %v, want ErrBudgetExhausted", err)
	}
	got, _, _ := store.Get(id)
	if got.State != tickets.StateQueued || got.DispatchReservationKey == "" || got.WorkItemSnapshotID == nil || got.WorkflowRunID != nil {
		t.Fatalf("generic budget denial must keep a resumable reservation: %+v", got)
	}
}

func TestDispatchOneSchemaThreeClassifiesRejectedSnapshotBindingAsClientInput(t *testing.T) {
	deps, store, _, binder := v3DispatchDeps(t)
	id := queuedTicket(t, store, "smoke")
	setRepositorySnapshot(t, store, id, snapshot.SnapshotID(101))
	binder.err = workflowrun.ErrSnapshotTypeMismatch

	_, err := dispatch.DispatchOne(context.Background(), deps, id, "admin")
	if !errors.Is(err, dispatch.ErrNotTicketDispatchable) {
		t.Fatalf("error = %v, want ErrNotTicketDispatchable", err)
	}
	got, _, _ := store.Get(id)
	if got.State != tickets.StateQueued || got.DispatchReservationKey == "" || got.WorkItemSnapshotID == nil || got.WorkflowRunID != nil {
		t.Fatalf("rejected binding must remain inspectable and resettable: %+v", got)
	}
}

func queuedTicket(t *testing.T, store *ticketstest.MemoryStore, workflowName string) int {
	t.Helper()
	id, err := store.Create(&tickets.Ticket{
		Title: "fix X", Body: "details", Origin: "fly",
		Repo: "tdmtrader/jetbridge", TargetBranch: "main",
		WorkflowName: workflowName, UserName: "tdm", CreatedBy: "tdm",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Transition(id, tickets.StateDraft, tickets.StateQueued, tickets.TransitionMeta{}); err != nil {
		t.Fatal(err)
	}
	return id
}

func TestDispatchOnePinnedVersion(t *testing.T) {
	deps, store, _, _ := v3DispatchDeps(t)
	id := queuedTicket(t, store, "smoke")
	pin := 7
	if err := store.Update(id, tickets.Update{WorkflowVersion: &pin}); err != nil {
		t.Fatal(err)
	}
	setRepositorySnapshot(t, store, id, 101)

	if _, err := dispatch.DispatchOne(context.Background(), deps, id, "admin"); err != nil {
		t.Fatalf("pinned dispatch: %v", err)
	}

	deps2, store2, _, _ := v3DispatchDeps(t)
	id2 := queuedTicket(t, store2, "smoke")
	missing := 9
	if err := store2.Update(id2, tickets.Update{WorkflowVersion: &missing}); err != nil {
		t.Fatal(err)
	}
	if _, err := dispatch.DispatchOne(context.Background(), deps2, id2, "admin"); !errors.Is(err, dispatch.ErrWorkflowNotFound) {
		t.Errorf("missing pinned version: got %v, want ErrWorkflowNotFound", err)
	}
}

func TestDispatchOneRefusals(t *testing.T) {
	deps, store, _, _ := v3DispatchDeps(t)

	if _, err := dispatch.DispatchOne(context.Background(), deps, 999, "admin"); !errors.Is(err, tickets.ErrTicketNotFound) {
		t.Errorf("missing ticket: got %v", err)
	}

	draft, _ := store.Create(&tickets.Ticket{Title: "d", Repo: "r", WorkflowName: "smoke"})
	if _, err := dispatch.DispatchOne(context.Background(), deps, draft, "admin"); !errors.Is(err, dispatch.ErrNotQueued) {
		t.Errorf("draft ticket: got %v, want ErrNotQueued", err)
	}

	noWF := queuedTicket(t, store, "")
	if _, err := dispatch.DispatchOne(context.Background(), deps, noWF, "admin"); !errors.Is(err, dispatch.ErrNoWorkflow) {
		t.Errorf("no workflow name: got %v, want ErrNoWorkflow", err)
	}

	unknown := queuedTicket(t, store, "nope")
	if _, err := dispatch.DispatchOne(context.Background(), deps, unknown, "admin"); !errors.Is(err, dispatch.ErrWorkflowNotFound) {
		t.Errorf("unknown workflow: got %v, want ErrWorkflowNotFound", err)
	}
}
