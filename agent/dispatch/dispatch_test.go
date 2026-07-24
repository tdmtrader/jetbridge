package dispatch_test

import (
	"strings"
	"sync"
	"time"

	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/concourse/concourse/agent/api/principals"
	"github.com/concourse/concourse/agent/api/tickets"
	"github.com/concourse/concourse/agent/budget"
	"github.com/concourse/concourse/agent/budget/budgetfakes"
	"github.com/concourse/concourse/agent/credentials"
	"github.com/concourse/concourse/agent/credentials/credentialsfakes"
	"github.com/concourse/concourse/agent/dispatch"
	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/agent/workflow"
	"github.com/concourse/concourse/agent/workflowrun"
	"github.com/concourse/concourse/agent/workitem"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/db/dbfakes"
)

// fakeBackend implements the two Backend calls dispatch makes:
// platform-user resolution and credential decryption.
type fakeBackend struct {
	credentials.Backend
	platformUserID int
	creds          map[int]map[string]*credentials.Credential
}

func (f *fakeBackend) UserBySub(sub string) (int, string, bool, error) {
	if sub == credentials.PlatformUserSub {
		return f.platformUserID, credentials.PlatformUserName, true, nil
	}
	return 0, "", false, nil
}

func (f *fakeBackend) Resolve(userID int, kind string) (*credentials.Credential, bool, error) {
	cred, ok := f.creds[userID][kind]
	return cred, ok, nil
}

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

type fakeSaver struct {
	savedName string
	savedCfg  atc.Config
	id        int
	err       error
}

func (f *fakeSaver) SaveTemplate(name string, cfg atc.Config) (int, error) {
	f.savedName, f.savedCfg = name, cfg
	if f.err != nil {
		return 0, f.err
	}
	return f.id, nil
}

func smokeDefinition() *workflow.Definition {
	return &workflow.Definition{
		Name: "smoke", Version: 3, SchemaVersion: 2, ContentHash: "abc123", Live: true,
		Config: workflow.Config{
			SchemaVersion: 2,
			Name:          "smoke",
			SpecDelivery:  "files",
			Defaults:      workflow.Defaults{Model: "claude-sonnet-5", MaxTurns: 5},
			Prompts:       map[string]string{"do": "Do it."},
			Steps: []workflow.Step{
				{Agent: "implement", Prompt: "do", Inputs: []string{"ticket"}, Outputs: []string{"workspace"}},
			},
		},
	}
}

type fakeWorkItemCapturer struct {
	mu     sync.Mutex
	calls  int
	result workitem.CaptureResult
	found  bool
	err    error
}

func (fake *fakeWorkItemCapturer) CaptureRevision(context.Context, int) (workitem.CaptureResult, bool, error) {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	fake.calls++
	return fake.result, fake.found, fake.err
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
	return fake.result, fake.err
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

type staticPortResolver struct {
	mapping dispatch.TicketPortMapping
	err     error
}

func (resolver staticPortResolver) ResolveTicketPorts(context.Context, workflow.Definition) (dispatch.TicketPortMapping, error) {
	return resolver.mapping, resolver.err
}

func v3Definition(t *testing.T, workItemPort, repositoryPort string) *workflow.Definition {
	t.Helper()
	raw := fmt.Sprintf(`schema_version: 3
name: smoke
signature_version: 1
inputs:
  - name: %s
    type: work-item/v1
  - name: %s
    type: repository/v1
outputs:
  - name: report
    type: opaque/v1
    from: report
plan:
  - agent: work
    function_id: work
    prompt: Do the captured work.
    inputs: [%s, %s]
    outputs: [report]
    input_types:
      %s: {type: work-item/v1}
      %s: {type: repository/v1}
    output_types:
      report: opaque/v1
`, workItemPort, repositoryPort, workItemPort, repositoryPort, workItemPort, repositoryPort)
	compiled, err := workflow.ParseCompiled([]byte(raw))
	if err != nil {
		t.Fatalf("parse v3 definition: %v", err)
	}
	return &workflow.Definition{
		ID: 41, Name: "smoke", Version: 7, SchemaVersion: 3, SignatureVersion: 1,
		ContentHash: strings.Repeat("a", 64), Live: true, Compiled: *compiled,
	}
}

func v3DispatchDeps(t *testing.T) (dispatch.Deps, *tickets.MemoryStore, *fakeSaver, *dbfakes.FakePipelineRunFactory, *fakeWorkItemCapturer, *fakeWorkflowBinder) {
	t.Helper()
	deps, store, saver, legacyRuns := dispatchDeps(t)
	definition := v3Definition(t, "work-item", "repository")
	deps.Workflows = &fakeWorkflows{byName: map[string]*workflow.Definition{"smoke": definition}}
	workItems := &fakeWorkItemCapturer{found: true, result: workitem.CaptureResult{
		TicketID: 1, Revision: 4, Snapshot: snapshot.Snapshot{ID: snapshot.SnapshotID(202), Type: snapshot.TypeRef("work-item/v1")},
	}}
	pipelineRunID := 909
	binder := &fakeWorkflowBinder{result: workflowrun.BindResult{Created: true, Run: db.AgentWorkflowRun{
		ID: snapshot.WorkflowRunID(303), TeamID: 1, TeamName: "main",
		WorkflowDefinitionID: definition.ID, WorkflowName: definition.Name,
		WorkflowVersion: definition.Version, SchemaVersion: 3, SignatureVersion: 1,
		ParameterizedConfigHash: strings.Repeat("b", 64), PipelineRunID: &pipelineRunID,
		Status: db.AgentWorkflowRunStatusRunning,
	}}}
	deps.TeamID, deps.TeamName = 1, "main"
	deps.WorkItems, deps.WorkflowBinder = workItems, binder
	deps.WorkflowCanceler = &fakeWorkflowRunCanceler{found: true}
	return deps, store, saver, legacyRuns, workItems, binder
}

func setRepositorySnapshot(t *testing.T, store *tickets.MemoryStore, ticketID int, id snapshot.SnapshotID) {
	t.Helper()
	if err := store.Update(ticketID, tickets.Update{RepositorySnapshotID: &id}); err != nil {
		t.Fatalf("select repository snapshot: %v", err)
	}
}

func TestDispatchOneSchemaThreeBindsCapturedSnapshotsThroughGenericBinder(t *testing.T) {
	deps, store, saver, legacyRuns, workItems, binder := v3DispatchDeps(t)
	id := queuedTicket(t, store, "smoke")
	workItems.result.TicketID = id
	setRepositorySnapshot(t, store, id, snapshot.SnapshotID(101))

	result, err := dispatch.DispatchOne(context.Background(), deps, id, "admin")
	if err != nil {
		t.Fatalf("DispatchOne: %v", err)
	}
	if result.RunID != 909 || result.WorkflowRunID == nil || *result.WorkflowRunID != snapshot.WorkflowRunID(303) {
		t.Fatalf("result = %+v", result)
	}
	if saver.savedName != "" || legacyRuns.CreateRunCallCount() != 0 {
		t.Fatalf("v3 reached legacy persistence: saver=%q create-runs=%d", saver.savedName, legacyRuns.CreateRunCallCount())
	}
	if deps.Secrets.(*credentialsfakes.FakeSecretAttacher).AttachCallCount() != 0 {
		t.Fatal("v3 secret attachment belongs to workflowrun.Binder, not the legacy ticket path")
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

func TestDispatchOneSchemaThreeUsesExplicitPortMappingWithoutReservedNames(t *testing.T) {
	deps, store, _, _, workItems, binder := v3DispatchDeps(t)
	definition := v3Definition(t, "request", "source")
	deps.Workflows = &fakeWorkflows{byName: map[string]*workflow.Definition{"smoke": definition}}
	deps.TicketPorts = staticPortResolver{mapping: dispatch.TicketPortMapping{WorkItem: "request", Repository: "source"}}
	binder.result.Run.WorkflowDefinitionID = definition.ID
	binder.result.Run.WorkflowVersion = definition.Version
	id := queuedTicket(t, store, "smoke")
	workItems.result.TicketID = id
	setRepositorySnapshot(t, store, id, snapshot.SnapshotID(101))

	if _, err := dispatch.DispatchOne(context.Background(), deps, id, "admin"); err != nil {
		t.Fatalf("DispatchOne: %v", err)
	}
	_, calls := binder.Calls()
	if calls[0].Inputs["request"] != snapshot.SnapshotID(202) || calls[0].Inputs["source"] != snapshot.SnapshotID(101) {
		t.Fatalf("explicit mapped inputs = %+v", calls[0].Inputs)
	}
}

func TestDispatchOneSchemaThreeReservesAndDefersUntilRepositorySnapshotSelected(t *testing.T) {
	deps, store, _, _, workItems, binder := v3DispatchDeps(t)
	id := queuedTicket(t, store, "smoke")
	workItems.result.TicketID = id

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
	deps, store, _, _, workItems, binder := v3DispatchDeps(t)
	id := queuedTicket(t, store, "smoke")
	workItems.result.TicketID = id
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
	deps, store, _, _, workItems, _ := v3DispatchDeps(t)
	id := queuedTicket(t, store, "smoke")
	workItems.result.TicketID = id
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
	deps, store, _, _, workItems, _ := v3DispatchDeps(t)
	id := queuedTicket(t, store, "smoke")
	workItems.result.TicketID = id
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
	deps, store, _, _, workItems, _ := v3DispatchDeps(t)
	id := queuedTicket(t, store, "smoke")
	workItems.result.TicketID = id
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
	deps, store, _, _, workItems, binder := v3DispatchDeps(t)
	id := queuedTicket(t, store, "smoke")
	workItems.result.TicketID = id
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
	deps, store, _, _, workItems, binder := v3DispatchDeps(t)
	id := queuedTicket(t, store, "smoke")
	workItems.result.TicketID = id
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
	deps, store, _, _, workItems, binder := v3DispatchDeps(t)
	id := queuedTicket(t, store, "smoke")
	workItems.result.TicketID = id
	setRepositorySnapshot(t, store, id, snapshot.SnapshotID(101))
	binder.err = workflowrun.ErrSnapshotTypeMismatch

	_, err := dispatch.DispatchOne(context.Background(), deps, id, "admin")
	if !errors.Is(err, dispatch.ErrRenderRefused) {
		t.Fatalf("error = %v, want ErrRenderRefused", err)
	}
	got, _, _ := store.Get(id)
	if got.State != tickets.StateQueued || got.DispatchReservationKey == "" || got.WorkItemSnapshotID == nil || got.WorkflowRunID != nil {
		t.Fatalf("rejected binding must remain inspectable and resettable: %+v", got)
	}
}

func TestDispatchOneAcceptsSchemaOneOnLegacyPath(t *testing.T) {
	deps, store, _, _ := dispatchDeps(t)
	definition := deps.Workflows.(*fakeWorkflows).byName["smoke"]
	definition.SchemaVersion = 1
	definition.Config.SchemaVersion = 1
	id := queuedTicket(t, store, "smoke")
	if _, err := dispatch.DispatchOne(context.Background(), deps, id, "admin"); err != nil {
		t.Fatalf("schema-version-1 legacy dispatch: %v", err)
	}
}

func dispatchDeps(t *testing.T) (dispatch.Deps, *tickets.MemoryStore, *fakeSaver, *dbfakes.FakePipelineRunFactory) {
	t.Helper()
	store := tickets.NewMemoryStore()
	saver := &fakeSaver{id: 77}
	runs := new(dbfakes.FakePipelineRunFactory)
	run := new(dbfakes.FakePipelineRun)
	run.IDReturns(555)
	runs.CreateRunReturns(run, nil)

	deps := dispatch.Deps{
		Tickets:   store,
		Workflows: &fakeWorkflows{byName: map[string]*workflow.Definition{"smoke": smokeDefinition()}},
		Templates: saver,
		Runs:      runs,
		Credentials: &fakeBackend{
			platformUserID: 9,
			creds: map[int]map[string]*credentials.Credential{
				9: {credentials.KindAnthropicOAuth: {Kind: credentials.KindAnthropicOAuth, Token: "platform-tok"}},
			},
		},
		Secrets:        new(credentialsfakes.FakeSecretAttacher),
		ATCExternalURL: "http://concourse.home",
		RepoBaseURL:    "https://github.com",
	}
	return deps, store, saver, runs
}

func queuedTicket(t *testing.T, store *tickets.MemoryStore, workflowName string) int {
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

func TestDispatchOneHappyPath(t *testing.T) {
	deps, store, saver, runs := dispatchDeps(t)
	id := queuedTicket(t, store, "smoke")

	res, err := dispatch.DispatchOne(context.Background(), deps, id, "admin")
	if err != nil {
		t.Fatalf("DispatchOne: %v", err)
	}
	if res.RunID != 555 || res.PipelineName != fmt.Sprintf("agent-ticket-%d", id) {
		t.Errorf("result = %+v", res)
	}

	if saver.savedName != fmt.Sprintf("agent-ticket-%d", id) || !saver.savedCfg.Template {
		t.Errorf("template save wrong: name=%q template=%v", saver.savedName, saver.savedCfg.Template)
	}

	templateID, params, createdBy := runs.CreateRunArgsForCall(0)
	if templateID != 77 || params != nil || createdBy != "admin" {
		t.Errorf("CreateRun args = %d %v %q", templateID, params, createdBy)
	}

	got, _, _ := store.Get(id)
	if got.State != tickets.StateRunning || got.PipelineRunID == nil || *got.PipelineRunID != 555 {
		t.Errorf("ticket after dispatch = %+v", got)
	}
	if got.WorkflowVersion == nil || *got.WorkflowVersion != 3 {
		t.Errorf("live workflow version must be frozen onto the ticket at dispatch, got %+v", got.WorkflowVersion)
	}

	// §8.2: the run secret is the ONLY token path into a ticketed agent
	// pod — dispatch must attach agent-run-<id> before the step's pod
	// can start (live finding: CreateContainerConfigError without it).
	att := deps.Secrets.(*credentialsfakes.FakeSecretAttacher)
	if att.AttachCallCount() != 1 {
		t.Fatalf("Attach calls = %d, want 1", att.AttachCallCount())
	}
	_, runID, cred := att.AttachArgsForCall(0)
	if runID != 555 || cred == nil || cred.Token != "platform-tok" {
		t.Errorf("Attach args: runID=%d cred=%+v", runID, cred)
	}
}

func TestDispatchOneAttachFailureLeavesTicketQueued(t *testing.T) {
	deps, store, _, _ := dispatchDeps(t)
	id := queuedTicket(t, store, "smoke")
	deps.Secrets.(*credentialsfakes.FakeSecretAttacher).AttachReturns("", errors.New("k8s down"))

	if _, err := dispatch.DispatchOne(context.Background(), deps, id, "admin"); err == nil {
		t.Fatal("attach failure must surface")
	}
	got, _, _ := store.Get(id)
	if got.State != tickets.StateQueued {
		t.Errorf("ticket must stay queued for a retry, state = %s", got.State)
	}
}

func TestDispatchOneNoCredentialFailsBeforeTransition(t *testing.T) {
	deps, store, _, _ := dispatchDeps(t)
	id := queuedTicket(t, store, "smoke")
	deps.Credentials.(*fakeBackend).creds = map[int]map[string]*credentials.Credential{}

	_, err := dispatch.DispatchOne(context.Background(), deps, id, "admin")
	if err == nil {
		t.Fatal("missing vaulted credential must surface")
	}
	got, _, _ := store.Get(id)
	if got.State != tickets.StateQueued {
		t.Errorf("ticket must stay queued, state = %s", got.State)
	}
}

func TestDispatchOnePinnedVersion(t *testing.T) {
	deps, store, _, _ := dispatchDeps(t)
	id := queuedTicket(t, store, "smoke")
	pin := 3
	store.Update(id, tickets.Update{WorkflowVersion: &pin})

	if _, err := dispatch.DispatchOne(context.Background(), deps, id, "admin"); err != nil {
		t.Fatalf("pinned dispatch: %v", err)
	}

	deps2, store2, _, _ := dispatchDeps(t)
	id2 := queuedTicket(t, store2, "smoke")
	missing := 9
	store2.Update(id2, tickets.Update{WorkflowVersion: &missing})
	if _, err := dispatch.DispatchOne(context.Background(), deps2, id2, "admin"); !errors.Is(err, dispatch.ErrWorkflowNotFound) {
		t.Errorf("missing pinned version: got %v, want ErrWorkflowNotFound", err)
	}
}

func TestDispatchOneRefusals(t *testing.T) {
	deps, store, _, _ := dispatchDeps(t)

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

func TestDispatchOneRunCreationFailureLeavesTicketQueued(t *testing.T) {
	deps, store, _, runs := dispatchDeps(t)
	id := queuedTicket(t, store, "smoke")
	runs.CreateRunReturns(nil, errors.New("boom"))

	if _, err := dispatch.DispatchOne(context.Background(), deps, id, "admin"); err == nil {
		t.Fatal("run-creation failure must surface")
	}
	got, _, _ := store.Get(id)
	if got.State != tickets.StateQueued {
		t.Errorf("ticket must stay queued for a retry, state = %s", got.State)
	}
}

func TestDispatchOneDefersWhenTicketBudgetExhausted(t *testing.T) {
	deps, store, _, runs := dispatchDeps(t)
	checker := new(budgetfakes.FakeChecker)
	checker.TicketRemainingReturns(budget.Remaining{LimitUSD: 5, SpentUSD: 6, RemainingUSD: -1, Exhausted: true}, nil)
	deps.Budget = checker
	id := queuedTicket(t, store, "smoke")

	_, err := dispatch.DispatchOne(context.Background(), deps, id, "loop")
	if !errors.Is(err, dispatch.ErrBudgetExhausted) {
		t.Fatalf("want ErrBudgetExhausted, got %v", err)
	}
	if runs.CreateRunCallCount() != 0 {
		t.Error("over-cap admission must run BEFORE CreateRun")
	}
	got, _, _ := store.Get(id)
	if got.State != tickets.StateQueued {
		t.Errorf("over-cap ticket must STAY queued (never failed), state=%s", got.State)
	}
}

func TestDispatchOneDefersWhenGlobalDailyCapExhausted(t *testing.T) {
	deps, store, _, runs := dispatchDeps(t)
	checker := new(budgetfakes.FakeChecker)
	checker.TicketRemainingReturns(budget.Remaining{}, nil) // uncapped ticket
	checker.GlobalDailyRemainingReturns(budget.Remaining{LimitUSD: 50, SpentUSD: 50, Exhausted: true}, nil)
	deps.Budget = checker
	id := queuedTicket(t, store, "smoke")

	_, err := dispatch.DispatchOne(context.Background(), deps, id, "loop")
	if !errors.Is(err, dispatch.ErrBudgetExhausted) {
		t.Fatalf("want ErrBudgetExhausted, got %v", err)
	}
	if runs.CreateRunCallCount() != 0 {
		t.Error("daily-cap admission must run BEFORE CreateRun")
	}
	got, _, _ := store.Get(id)
	if got.State != tickets.StateQueued {
		t.Errorf("daily-capped ticket must stay queued, state=%s", got.State)
	}
}

func TestDispatchOneBudgetCheckerErrorIsPlatformFaultNotDeferral(t *testing.T) {
	deps, store, _, _ := dispatchDeps(t)
	checker := new(budgetfakes.FakeChecker)
	checker.TicketRemainingReturns(budget.Remaining{}, errors.New("ledger down"))
	deps.Budget = checker
	id := queuedTicket(t, store, "smoke")

	_, err := dispatch.DispatchOne(context.Background(), deps, id, "loop")
	if err == nil || errors.Is(err, dispatch.ErrBudgetExhausted) {
		t.Fatalf("checker error must surface as a platform fault, got %v", err)
	}
	got, _, _ := store.Get(id)
	if got.State != tickets.StateQueued {
		t.Errorf("platform fault leaves ticket queued, state=%s", got.State)
	}
}

func TestDispatchOneNilBudgetSkipsAdmission(t *testing.T) {
	deps, store, _, _ := dispatchDeps(t) // deps.Budget nil
	id := queuedTicket(t, store, "smoke")
	if _, err := dispatch.DispatchOne(context.Background(), deps, id, "admin"); err != nil {
		t.Fatalf("nil Budget must preserve landed behavior: %v", err)
	}
}

var _ dispatch.RunCreator = db.PipelineRunFactory(nil)

type fakeUserLookup struct{ ids map[string]int }

func (f fakeUserLookup) FindByUsername(name string) (int, bool, error) {
	id, ok := f.ids[name]
	return id, ok, nil
}

func TestDispatchOneResolvesAndPersistsUserID(t *testing.T) {
	deps, store, _, _ := dispatchDeps(t)
	deps.Users = fakeUserLookup{ids: map[string]int{"tdm": 42}}
	// Give user 42 a vaulted credential so user-first resolution is provable.
	deps.Credentials = &fakeBackend{
		platformUserID: 9,
		creds: map[int]map[string]*credentials.Credential{
			9:  {credentials.KindAnthropicOAuth: {UserID: 9, Kind: credentials.KindAnthropicOAuth, Token: "platform-tok"}},
			42: {credentials.KindAnthropicOAuth: {UserID: 42, UserName: "tdm", Kind: credentials.KindAnthropicOAuth, Token: "tdm-tok"}},
		},
	}
	attacher := new(credentialsfakes.FakeSecretAttacher)
	deps.Secrets = attacher
	id := queuedTicket(t, store, "smoke") // UserName "tdm", UserID nil

	if _, err := dispatch.DispatchOne(context.Background(), deps, id, "loop"); err != nil {
		t.Fatalf("DispatchOne: %v", err)
	}
	got, _, _ := store.Get(id)
	if got.UserID == nil || *got.UserID != 42 {
		t.Fatalf("user_id must be resolved+persisted at dispatch, got %v", got.UserID)
	}
	if attacher.AttachCallCount() != 1 {
		t.Fatal("expected one Attach")
	}
	_, _, cred := attacher.AttachArgsForCall(0)
	if cred.Token != "tdm-tok" {
		t.Errorf("user-first credential must fund the run once user_id resolves, got token %q", cred.Token)
	}
}

func TestDispatchOneUnknownUserFallsBackToPlatform(t *testing.T) {
	deps, store, _, _ := dispatchDeps(t)
	deps.Users = fakeUserLookup{ids: map[string]int{}} // "tdm" not found
	attacher := new(credentialsfakes.FakeSecretAttacher)
	deps.Secrets = attacher
	id := queuedTicket(t, store, "smoke")

	if _, err := dispatch.DispatchOne(context.Background(), deps, id, "loop"); err != nil {
		t.Fatalf("unknown user must not block dispatch (platform funds it): %v", err)
	}
	got, _, _ := store.Get(id)
	if got.UserID != nil {
		t.Errorf("unresolvable user leaves user_id NULL, got %v", got.UserID)
	}
}

func TestAttachLeavesPrincipalStoreEmpty(t *testing.T) {
	deps, store, _, _ := dispatchDeps(t)
	pstore := principals.NewMemoryStore()
	id := queuedTicket(t, store, "smoke")

	if _, err := dispatch.DispatchOne(context.Background(), deps, id, "loop"); err != nil {
		t.Fatalf("DispatchOne: %v", err)
	}
	if deps.Secrets.(*credentialsfakes.FakeSecretAttacher).AttachCallCount() != 1 {
		t.Fatal("dispatch must attach the selected credential")
	}
	principals, err := pstore.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(principals) != 0 {
		t.Fatalf("per-run principal creation is removed, got %v", principals)
	}
}

func TestResolveRunCredentialSkipsExpiredNamingOwner(t *testing.T) {
	deps, store, _, _ := dispatchDeps(t)
	deps.Users = fakeUserLookup{ids: map[string]int{"tdm": 42}}
	expired := time.Now().Add(-time.Hour).Unix()
	deps.Credentials = &fakeBackend{
		platformUserID: 9,
		creds: map[int]map[string]*credentials.Credential{
			// user cred expired; platform cred valid → platform funds the run
			42: {credentials.KindAnthropicOAuth: {UserID: 42, UserName: "tdm", Kind: credentials.KindAnthropicOAuth, Token: "stale", ExpiresAt: expired}},
			9:  {credentials.KindAnthropicOAuth: {UserID: 9, Kind: credentials.KindAnthropicOAuth, Token: "platform-tok"}},
		},
	}
	attacher := new(credentialsfakes.FakeSecretAttacher)
	deps.Secrets = attacher
	id := queuedTicket(t, store, "smoke")

	if _, err := dispatch.DispatchOne(context.Background(), deps, id, "loop"); err != nil {
		t.Fatalf("expired user cred must fall back to platform: %v", err)
	}
	_, _, cred := attacher.AttachArgsForCall(0)
	if cred.Token != "platform-tok" {
		t.Errorf("expected platform fallback past expired user cred, got %q", cred.Token)
	}
}

func TestResolveRunCredentialAllExpiredErrorsWithOwner(t *testing.T) {
	deps, store, _, _ := dispatchDeps(t)
	expired := time.Now().Add(-time.Hour).Unix()
	deps.Credentials = &fakeBackend{
		platformUserID: 9,
		creds: map[int]map[string]*credentials.Credential{
			9: {credentials.KindAnthropicOAuth: {UserID: 9, UserName: "platform", Kind: credentials.KindAnthropicOAuth, Token: "stale", ExpiresAt: expired}},
		},
	}
	id := queuedTicket(t, store, "smoke")

	_, err := dispatch.DispatchOne(context.Background(), deps, id, "loop")
	if err == nil || !strings.Contains(err.Error(), "expired") || !strings.Contains(err.Error(), "platform") {
		t.Fatalf("all-expired must error naming the owner, got %v", err)
	}
	got, _, _ := store.Get(id)
	if got.State != tickets.StateQueued {
		t.Errorf("credential failure is pre-transition: ticket stays queued, got %s", got.State)
	}
}
