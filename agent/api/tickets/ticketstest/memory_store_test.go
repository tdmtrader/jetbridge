package ticketstest_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"

	"github.com/concourse/concourse/agent/api/tickets"
	"github.com/concourse/concourse/agent/api/tickets/ticketstest"
	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/agent/snapshot/contracts"
)

func newTicket() *tickets.Ticket {
	return &tickets.Ticket{
		Title: "fix flaky spec", Body: "it flakes", Origin: "web",
		Repo: "tdmtrader/concourse", TargetBranch: "main",
		UserName: "tdm", CreatedBy: "tdm",
	}
}

func TestMemoryStoreDispatchReservationIsAtomicAndBindingsAreImmutable(t *testing.T) {
	store := ticketstest.NewMemoryStore()
	repositoryID := snapshot.SnapshotID(101)
	id, err := store.Create(&tickets.Ticket{
		Title: "fix", Body: "it", Repo: "example/repo", WorkflowName: "small-fix",
		RepositorySnapshotID: &repositoryID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Transition(id, tickets.StateDraft, tickets.StateQueued, tickets.TransitionMeta{}); err != nil {
		t.Fatal(err)
	}
	before, _, _ := store.Get(id)

	const callers = 16
	results := make(chan tickets.DispatchReservation, callers)
	errs := make(chan error, callers)
	var wait sync.WaitGroup
	for index := 0; index < callers; index++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			reservation, err := store.ReserveDispatch(context.Background(), id, tickets.DispatchReservationRequest{
				ExpectedRevision: before.Revision, WorkflowVersion: 7, WorkflowDefinitionID: 41,
			})
			results <- reservation
			errs <- err
		}()
	}
	wait.Wait()
	close(results)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("ReserveDispatch: %v", err)
		}
	}
	key := ""
	for result := range results {
		if result.Key == "" {
			t.Fatal("reservation key must be durable")
		}
		if key == "" {
			key = result.Key
		} else if result.Key != key {
			t.Fatalf("reservation keys diverged: %q != %q", result.Key, key)
		}
	}

	workItemID := snapshot.SnapshotID(202)
	reserved, _, _ := store.Get(id)
	if err := store.RecordDispatchWorkItem(context.Background(), id, key, reserved.Revision, workItemID); err != nil {
		t.Fatalf("RecordDispatchWorkItem: %v", err)
	}
	if err := store.RecordDispatchWorkItem(context.Background(), id, key, reserved.Revision, workItemID); err != nil {
		t.Fatalf("idempotent RecordDispatchWorkItem: %v", err)
	}
	otherWorkItem := snapshot.SnapshotID(203)
	if err := store.RecordDispatchWorkItem(context.Background(), id, key, reserved.Revision, otherWorkItem); !errors.Is(err, tickets.ErrDispatchConflict) {
		t.Fatalf("rebind work item = %v, want ErrDispatchConflict", err)
	}

	workflowRunID := snapshot.WorkflowRunID(303)
	if err := store.RecordDispatchRun(context.Background(), id, key, workflowRunID, 909); err != nil {
		t.Fatalf("RecordDispatchRun: %v", err)
	}
	if err := store.RecordDispatchRun(context.Background(), id, key, workflowRunID, 909); err != nil {
		t.Fatalf("idempotent RecordDispatchRun: %v", err)
	}
	otherRunID := snapshot.WorkflowRunID(304)
	if err := store.RecordDispatchRun(context.Background(), id, key, otherRunID, 910); !errors.Is(err, tickets.ErrDispatchConflict) {
		t.Fatalf("rebind workflow run = %v, want ErrDispatchConflict", err)
	}

	got, _, _ := store.Get(id)
	if got.DispatchReservationKey != key || got.WorkflowVersion == nil || *got.WorkflowVersion != 7 ||
		got.WorkflowDefinitionID == nil || *got.WorkflowDefinitionID != 41 ||
		got.RepositorySnapshotID == nil || *got.RepositorySnapshotID != repositoryID ||
		got.WorkItemSnapshotID == nil || *got.WorkItemSnapshotID != workItemID ||
		got.WorkflowRunID == nil || *got.WorkflowRunID != workflowRunID ||
		got.PipelineRunID == nil || *got.PipelineRunID != 909 {
		t.Fatalf("durable reservation = %+v", got)
	}
}

func TestMemoryStoreUnqueueClearsAttemptReservationButRetainsRepositorySelection(t *testing.T) {
	store := ticketstest.NewMemoryStore()
	repositoryID := snapshot.SnapshotID(101)
	id, err := store.Create(&tickets.Ticket{
		Title: "fix", Body: "it", Repo: "example/repo", WorkflowName: "small-fix",
		RepositorySnapshotID: &repositoryID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Transition(id, tickets.StateDraft, tickets.StateQueued, tickets.TransitionMeta{}); err != nil {
		t.Fatal(err)
	}
	queued, _, _ := store.Get(id)
	reservation, err := store.ReserveDispatch(context.Background(), id, tickets.DispatchReservationRequest{
		ExpectedRevision: queued.Revision, WorkflowVersion: 7, WorkflowDefinitionID: 41,
	})
	if err != nil {
		t.Fatal(err)
	}
	workItemID := snapshot.SnapshotID(202)
	if err := store.RecordDispatchWorkItem(context.Background(), id, reservation.Key, reservation.Ticket.Revision, workItemID); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordDispatchRun(context.Background(), id, reservation.Key, snapshot.WorkflowRunID(303), 909); err != nil {
		t.Fatal(err)
	}

	if err := store.Transition(id, tickets.StateQueued, tickets.StateDraft, tickets.TransitionMeta{}); err != nil {
		t.Fatal(err)
	}
	got, _, _ := store.Get(id)
	if got.DispatchReservationKey != "" || got.WorkflowRunID != nil || got.WorkItemSnapshotID != nil || got.PipelineRunID != nil {
		t.Fatalf("unqueue retained attempt linkage: %+v", got)
	}
	if got.RepositorySnapshotID == nil || *got.RepositorySnapshotID != repositoryID {
		t.Fatalf("unqueue lost reusable repository selection: %+v", got)
	}
}

func TestMemoryStoreCreateGetListUpdate(t *testing.T) {
	s := ticketstest.NewMemoryStore()

	id, err := s.Create(newTicket())
	if err != nil || id != 1 {
		t.Fatalf("Create = %d, %v; want 1, nil", id, err)
	}

	got, found, err := s.Get(id)
	if err != nil || !found {
		t.Fatalf("Get = %v, %v, %v", got, found, err)
	}
	if got.State != tickets.StateDraft || got.Title != "fix flaky spec" || got.CreatedAt == 0 {
		t.Errorf("unexpected ticket: %+v", got)
	}

	newTitle := "fix the flaky spec"
	newBody := "reproduces on the second run"
	if err := s.Update(id, tickets.Update{Title: &newTitle, Body: &newBody}); err != nil {
		t.Fatalf("Update: %v", err)
	}
	got, _, _ = s.Get(id)
	if got.Title != newTitle || got.Body != newBody {
		t.Errorf("update not applied: %+v", got)
	}

	all, err := s.List(tickets.ListFilter{})
	if err != nil || len(all) != 1 {
		t.Fatalf("List = %d, %v", len(all), err)
	}
	none, _ := s.List(tickets.ListFilter{State: tickets.StateQueued})
	if len(none) != 0 {
		t.Errorf("state filter leaked: %+v", none)
	}
}

func TestMemoryStoreNullableUpdatesAreOmittedSetOrCleared(t *testing.T) {
	store := ticketstest.NewMemoryStore()
	initialVersion := 3
	initialRepository := snapshot.SnapshotID(101)
	id, err := store.Create(&tickets.Ticket{
		Title: "fix", Repo: "example/repo", WorkflowName: "deploy",
		WorkflowVersion: &initialVersion, RepositorySnapshotID: &initialRepository,
	})
	if err != nil {
		t.Fatal(err)
	}

	title := "updated"
	if err := store.Update(id, tickets.Update{Title: &title}); err != nil {
		t.Fatalf("omitted nullable update: %v", err)
	}
	got, _, _ := store.Get(id)
	if got.WorkflowVersion == nil || *got.WorkflowVersion != initialVersion ||
		got.RepositorySnapshotID == nil || *got.RepositorySnapshotID != initialRepository {
		t.Fatalf("omitted nullable fields changed selections: %+v", got)
	}

	if err := store.Update(id, tickets.Update{
		WorkflowVersion:      tickets.ClearField[int](),
		RepositorySnapshotID: tickets.ClearField[snapshot.SnapshotID](),
	}); err != nil {
		t.Fatalf("clear nullable fields: %v", err)
	}
	got, _, _ = store.Get(id)
	if got.WorkflowVersion != nil || got.RepositorySnapshotID != nil {
		t.Fatalf("explicit clears did not persist: %+v", got)
	}

	repository := snapshot.SnapshotID(202)
	if err := store.Update(id, tickets.Update{
		WorkflowVersion:      tickets.SetField(5),
		RepositorySnapshotID: tickets.SetField(repository),
	}); err != nil {
		t.Fatalf("set nullable fields: %v", err)
	}
	if err := store.Transition(id, tickets.StateDraft, tickets.StateQueued, tickets.TransitionMeta{}); err != nil {
		t.Fatal(err)
	}
	queued, _, _ := store.Get(id)
	if _, err := store.ReserveDispatch(context.Background(), id, tickets.DispatchReservationRequest{
		ExpectedRevision: queued.Revision, WorkflowVersion: 7, WorkflowDefinitionID: 41,
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.Update(id, tickets.Update{WorkflowVersion: tickets.ClearField[int]()}); !errors.Is(err, tickets.ErrDispatchConflict) {
		t.Fatalf("clear reserved workflow version = %v, want ErrDispatchConflict", err)
	}
	if err := store.Update(id, tickets.Update{RepositorySnapshotID: tickets.ClearField[snapshot.SnapshotID]()}); !errors.Is(err, tickets.ErrDispatchConflict) {
		t.Fatalf("clear reserved repository snapshot = %v, want ErrDispatchConflict", err)
	}
	got, _, _ = store.Get(id)
	if got.WorkflowVersion == nil || *got.WorkflowVersion != 7 ||
		got.RepositorySnapshotID == nil || *got.RepositorySnapshotID != repository {
		t.Fatalf("failed reserved clears mutated bindings: %+v", got)
	}
}

func TestMemoryStoreTransition(t *testing.T) {
	s := ticketstest.NewMemoryStore()
	id, _ := s.Create(newTicket())

	if err := s.Transition(id, tickets.StateDraft, tickets.StateQueued, tickets.TransitionMeta{}); err != nil {
		t.Fatalf("draft->queued: %v", err)
	}
	err := s.Transition(id, tickets.StateDraft, tickets.StateQueued, tickets.TransitionMeta{})
	if !errors.Is(err, tickets.ErrStaleTransition) {
		t.Errorf("stale from-state: got %v, want ErrStaleTransition", err)
	}
	err = s.Transition(id, tickets.StateQueued, tickets.StateNeedsReview, tickets.TransitionMeta{})
	if !errors.Is(err, tickets.ErrInvalidTransition) {
		t.Errorf("illegal edge: got %v, want ErrInvalidTransition", err)
	}
	err = s.Transition(999, tickets.StateDraft, tickets.StateQueued, tickets.TransitionMeta{})
	if !errors.Is(err, tickets.ErrTicketNotFound) {
		t.Errorf("missing ticket: got %v, want ErrTicketNotFound", err)
	}
}

func TestMemoryStoreIncrementsRevisionForEverySuccessfulMutation(t *testing.T) {
	s := ticketstest.NewMemoryStore()
	id, err := s.Create(newTicket())
	if err != nil {
		t.Fatal(err)
	}

	wantRevision := int64(1)
	assertRevision := func(operation string) {
		t.Helper()
		got, found, err := s.Get(id)
		if err != nil || !found || got.Revision != wantRevision {
			t.Fatalf("%s revision = (%+v, %t, %v), want %d", operation, got, found, err, wantRevision)
		}
	}
	mutated := func(operation string, err error) {
		t.Helper()
		if err != nil {
			t.Fatalf("%s: %v", operation, err)
		}
		wantRevision++
		assertRevision(operation)
	}

	assertRevision("create")
	title := "updated"
	mutated("update", s.Update(id, tickets.Update{Title: &title}))
	mutated("transition", s.Transition(id, tickets.StateDraft, tickets.StateQueued, tickets.TransitionMeta{}))

	beforeFailure := wantRevision
	if err := s.Update(999, tickets.Update{Title: &title}); !errors.Is(err, tickets.ErrTicketNotFound) {
		t.Fatalf("update of a missing ticket = %v, want ErrTicketNotFound", err)
	}
	wantRevision = beforeFailure
	assertRevision("failed update")
}

func TestMemoryStoreCapturesTicketContent(t *testing.T) {
	s := ticketstest.NewMemoryStore()
	version := 3
	id, err := s.Create(&tickets.Ticket{
		Title: "upgrade", Body: "upgrade the dependency", Origin: "web", ExternalRef: "ENG-42", Repo: "repo",
		WorkflowName: "version-upgrade", WorkflowVersion: &version, CreatedBy: "alice",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Transition(id, tickets.StateDraft, tickets.StateQueued, tickets.TransitionMeta{}); err != nil {
		t.Fatal(err)
	}

	captured, found, err := s.CaptureRevision(context.Background(), id)
	if err != nil || !found {
		t.Fatalf("CaptureRevision = (%+v, %t, %v)", captured, found, err)
	}
	var document contracts.WorkItemDocument
	if err := json.Unmarshal(captured.Document, &document); err != nil {
		t.Fatal(err)
	}
	if document.Adapter != "jetbridge" || document.ExternalID != "ENG-42" ||
		document.Title != "upgrade" || document.Body != "upgrade the dependency" {
		t.Fatalf("captured document = %+v", document)
	}
	// work-item/v1 carries no comments, spec or plan keys any more: the ticket
	// body is the whole of the captured prose. It no longer embeds its consumer
	// either — the ticket's lifecycle state and the workflow selected to run over
	// it belong to the durable run. A strict decode of the captured bytes must
	// not find any of them.
	for _, retired := range []string{`"comments"`, `"spec"`, `"plan"`, `"state"`, `"workflow"`} {
		if bytes.Contains(captured.Document, []byte(retired)) {
			t.Fatalf("captured document still carries %s: %s", retired, captured.Document)
		}
	}
}
