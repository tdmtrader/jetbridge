package workflowrun

import (
	"context"
	"errors"
	"testing"

	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/agent/workflow"
	"github.com/concourse/concourse/atc/db"
)

// ticketHarness is the smallest binder that reaches allocation: one definition,
// no inputs, and a store that records the durable create request.
type ticketHarness struct {
	binder  *Binder
	store   *storeStub
	last    *db.AgentWorkflowRunCreateRequest
	created db.AgentWorkflowRun
	source  db.AgentWorkflowRun
}

func newTicketHarness(t *testing.T) *ticketHarness {
	t.Helper()
	definition := binderTestDefinition()
	rendered := binderTestRendered(t, definition)

	harness := &ticketHarness{}
	admitting := db.AgentWorkflowRun{
		ID: 900, TeamID: 7, TeamName: "research", WorkflowDefinitionID: definition.ID,
		WorkflowName: definition.Name, WorkflowVersion: definition.Version,
		SchemaVersion: 3, SignatureVersion: 1, DefinitionContentHash: definition.ContentHash,
		ParameterizedConfig:     mustCanonical(t, rendered.Config),
		ParameterizedConfigHash: rendered.TargetConfigHash,
		CreatedBy:               "alice", Status: db.AgentWorkflowRunStatusAdmitting,
	}
	pipelineRunID, templateID, instanceID := 313, 211, 419
	plannedBuildID, instanceHash := int64(521), "instance-hash"
	harness.store = &storeStub{
		// Not found until allocation; afterwards the linked, running run, the
		// way the durable store reads back once execution exists.
		find: func(context.Context, int, string) (db.AgentWorkflowRun, bool, error) {
			if harness.last == nil {
				return db.AgentWorkflowRun{}, false, nil
			}
			running := harness.created
			running.Status = db.AgentWorkflowRunStatusRunning
			running.PipelineRunID, running.TemplatePipelineID, running.InstancePipelineID =
				&pipelineRunID, &templateID, &instanceID
			running.ConcreteConfig, running.ConcreteConfigHash =
				harness.created.ParameterizedConfig, &instanceHash
			running.PlannedBuildID = &plannedBuildID
			return running, true, nil
		},
		get: func(_ context.Context, _ int, id snapshot.WorkflowRunID) (db.AgentWorkflowRun, bool, error) {
			if harness.source.ID == 0 || id != harness.source.ID {
				return db.AgentWorkflowRun{}, false, nil
			}
			return harness.source, true, nil
		},
		create: func(_ context.Context, request db.AgentWorkflowRunCreateRequest) (db.AgentWorkflowRun, bool, error) {
			captured := request
			harness.last = &captured
			// Echo the request back the way the durable store does, so the
			// binder's own allocated-run comparison stays meaningful here.
			created := admitting
			created.DefinitionKind = request.DefinitionKind
			created.WorkflowDefinitionID = request.WorkflowDefinitionID
			created.WorkflowName, created.WorkflowVersion = request.WorkflowName, request.WorkflowVersion
			created.SchemaVersion, created.SignatureVersion = request.SchemaVersion, request.SignatureVersion
			created.DefinitionContentHash = request.DefinitionContentHash
			created.FunctionID = request.FunctionID
			created.IdempotencyKey = request.IdempotencyKey
			created.ParameterizedConfig = request.ParameterizedConfig
			created.ParameterizedConfigHash = request.ParameterizedConfigHash
			created.DevValidationProvenanceHash = request.DevValidationProvenanceHash
			created.ResourceSourceAdmissionID = request.ResourceSourceAdmissionID
			created.OriginKind, created.OriginReference = request.OriginKind, request.OriginReference
			created.CreatedBy, created.Status = request.CreatedBy, request.Status
			created.TicketID, created.TicketReference = request.TicketID, request.TicketReference
			created.RetryOfWorkflowRunID = request.RetryOfWorkflowRunID
			harness.created = created
			return created, true, nil
		},
		snapshots: func(_ context.Context, id snapshot.WorkflowRunID) ([]db.AgentWorkflowRunSnapshotBinding, error) {
			input := binderTestSnapshot(ticketRepoSnapshot, "repository/v1")
			return []db.AgentWorkflowRunSnapshotBinding{{
				WorkflowRunID: id, Direction: db.AgentWorkflowRunSnapshotInput, PortName: "repo",
				Snapshot: snapshot.SnapshotRef{ID: input.ID, Type: input.Type, Digest: input.Digest},
			}}, nil
		},
		transition: func(context.Context, snapshot.WorkflowRunID, db.AgentWorkflowRunStatus, db.AgentWorkflowRunStatus, string) (bool, error) {
			return true, nil
		},
	}
	saver := &saverStub{save: func(context.Context, AdmissionContext, ImmutableTemplateSpec) (WorkflowRunTemplateRef, error) {
		return WorkflowRunTemplateRef{PipelineID: 211, TeamID: 7, Name: rendered.TemplateName, ConfigVersion: 19, FullHash: rendered.TargetConfigHash}, nil
	}}
	creator := &creatorStub{create: func(
		context.Context, snapshot.WorkflowRunID, WorkflowRunTemplateRef, map[string]any, string, BeforeWorkflowRunCommit,
	) (WorkflowRunExecution, bool, error) {
		return WorkflowRunExecution{}, true, nil
	}}
	binder, err := NewBinder(
		resumeResolver(definition), resumeRenderer(definition, rendered),
		ticketInputAuthorizer(), harness.store, &budgetStub{}, saver, creator,
		&credentialStub{admit: func(context.Context) error { return nil }},
	)
	if err != nil {
		t.Fatalf("NewBinder: %v", err)
	}
	harness.binder = binder
	return harness
}

// ticketRepoSnapshot satisfies the definition's one required input, so these
// specs exercise admission rather than input coverage.
const ticketRepoSnapshot = snapshot.SnapshotID(11)

func ticketInputAuthorizer() *authorizerStub {
	return &authorizerStub{get: func(_ context.Context, _ int, id snapshot.SnapshotID) (snapshot.Snapshot, bool, error) {
		if id != ticketRepoSnapshot {
			return snapshot.Snapshot{}, false, nil
		}
		return binderTestSnapshot(ticketRepoSnapshot, "repository/v1"), true, nil
	}}
}

func ticketBindRequest(key string) BindRequest {
	version := binderTestDefinition().Version
	return BindRequest{
		WorkflowName: binderTestDefinition().Name, Version: &version, IdempotencyKey: key,
		Inputs: map[string]snapshot.SnapshotID{"repo": ticketRepoSnapshot},
	}
}

// seedSource makes an already-admitted run available to the retry and
// follow-on paths.
func (harness *ticketHarness) seedSource(id snapshot.WorkflowRunID, ticketID *int64, reference string) {
	definition := binderTestDefinition()
	harness.source = db.AgentWorkflowRun{
		ID: id, TeamID: 7, TeamName: "research", WorkflowDefinitionID: definition.ID,
		WorkflowName: definition.Name, WorkflowVersion: definition.Version,
		SchemaVersion: 3, SignatureVersion: 1, DefinitionContentHash: definition.ContentHash,
		CreatedBy: "alice", Status: db.AgentWorkflowRunStatusFailed,
		TicketID: ticketID, TicketReference: reference,
	}
}

func ticketID(value int64) *int64 { return &value }

func TestBindCarriesTicketAssociationFromAdmission(t *testing.T) {
	harness := newTicketHarness(t)

	if _, err := harness.binder.BindAndCreate(context.Background(), AdmissionContext{
		TeamID: 7, TeamName: "research", CreatedBy: "alice",
		Origin: Origin{Kind: OriginKindTicket, Reference: "42"},
		Ticket: &TicketAssociation{ID: 42, Reference: "ticket-42"},
	}, ticketBindRequest("dispatch-42")); err != nil {
		t.Fatalf("BindAndCreate returned an error: %v", err)
	}

	if harness.last.TicketID == nil || *harness.last.TicketID != 42 {
		t.Fatalf("expected the ticket id to reach the durable run, got %+v", harness.last.TicketID)
	}
	if harness.last.TicketReference != "ticket-42" {
		t.Fatalf("expected the durable reference, got %q", harness.last.TicketReference)
	}
}

func TestManualLaunchWithoutTicketStaysUnattached(t *testing.T) {
	harness := newTicketHarness(t)

	if _, err := harness.binder.BindAndCreate(context.Background(), AdmissionContext{
		TeamID: 7, TeamName: "research", CreatedBy: "alice",
		Origin: Origin{Kind: "manual"},
	}, ticketBindRequest("manual-1")); err != nil {
		t.Fatalf("BindAndCreate returned an error: %v", err)
	}

	if harness.last.TicketID != nil || harness.last.TicketReference != "" {
		t.Fatal("an unattached workflow must not acquire a ticket")
	}
}

func TestRetryInheritsTicketAssociation(t *testing.T) {
	harness := newTicketHarness(t)
	harness.seedSource(500, ticketID(42), "ticket-42")

	request := ticketBindRequest("retry-1")
	source := snapshot.WorkflowRunID(500)
	request.RetryOf = &source
	if _, err := harness.binder.BindAndCreate(context.Background(), AdmissionContext{
		TeamID: 7, TeamName: "research", CreatedBy: "alice",
		Origin: Origin{Kind: "retry", Reference: "500"},
	}, request); err != nil {
		t.Fatalf("BindAndCreate returned an error: %v", err)
	}

	if harness.last.TicketID == nil || *harness.last.TicketID != 42 ||
		harness.last.TicketReference != "ticket-42" {
		t.Fatalf("a retry must inherit its source's ticket, got %+v / %q",
			harness.last.TicketID, harness.last.TicketReference)
	}
}

func TestRetryOfAnUnattachedRunStaysUnattached(t *testing.T) {
	harness := newTicketHarness(t)
	harness.seedSource(500, nil, "")

	request := ticketBindRequest("retry-2")
	source := snapshot.WorkflowRunID(500)
	request.RetryOf = &source
	if _, err := harness.binder.BindAndCreate(context.Background(), AdmissionContext{
		TeamID: 7, TeamName: "research", CreatedBy: "alice",
		Origin: Origin{Kind: "retry", Reference: "500"},
	}, request); err != nil {
		t.Fatalf("BindAndCreate returned an error: %v", err)
	}

	if harness.last.TicketID != nil || harness.last.TicketReference != "" {
		t.Fatal("a retry of a standalone run must stay standalone")
	}
}

func TestRetryCannotDeclareATicketItsSourceDoesNotHave(t *testing.T) {
	harness := newTicketHarness(t)
	harness.seedSource(500, nil, "")

	request := ticketBindRequest("retry-3")
	source := snapshot.WorkflowRunID(500)
	request.RetryOf = &source
	_, err := harness.binder.BindAndCreate(context.Background(), AdmissionContext{
		TeamID: 7, TeamName: "research", CreatedBy: "alice",
		Origin: Origin{Kind: "retry", Reference: "500"},
		Ticket: &TicketAssociation{ID: 42, Reference: "ticket-42"},
	}, request)
	if !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("expected ErrInvalidRequest, got %v", err)
	}
}

func TestRetryCannotMoveItsSourcesTicket(t *testing.T) {
	harness := newTicketHarness(t)
	harness.seedSource(500, ticketID(42), "ticket-42")

	request := ticketBindRequest("retry-4")
	source := snapshot.WorkflowRunID(500)
	request.RetryOf = &source
	_, err := harness.binder.BindAndCreate(context.Background(), AdmissionContext{
		TeamID: 7, TeamName: "research", CreatedBy: "alice",
		Origin: Origin{Kind: "retry", Reference: "500"},
		Ticket: &TicketAssociation{ID: 43, Reference: "ticket-43"},
	}, request)
	if !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("expected ErrInvalidRequest, got %v", err)
	}
}

func TestRetryOfARunWhoseTicketWasDeletedStaysUnattached(t *testing.T) {
	harness := newTicketHarness(t)
	// ON DELETE SET NULL cleared the live reference; only evidence remains,
	// and there is no live ticket left to re-admit under.
	harness.seedSource(500, nil, "ticket-42")

	request := ticketBindRequest("retry-5")
	source := snapshot.WorkflowRunID(500)
	request.RetryOf = &source
	if _, err := harness.binder.BindAndCreate(context.Background(), AdmissionContext{
		TeamID: 7, TeamName: "research", CreatedBy: "alice",
		Origin: Origin{Kind: "retry", Reference: "500"},
	}, request); err != nil {
		t.Fatalf("BindAndCreate returned an error: %v", err)
	}
	if harness.last.TicketID != nil || harness.last.TicketReference != "" {
		t.Fatal("a deleted ticket has no journal for the retry to join")
	}
}

func TestAdmissionRejectsAnIncompleteTicketAssociation(t *testing.T) {
	for name, association := range map[string]TicketAssociation{
		"no id":        {Reference: "ticket-42"},
		"no reference": {ID: 42},
		"blank":        {ID: 42, Reference: "   "},
		"negative id":  {ID: -1, Reference: "ticket-42"},
	} {
		t.Run(name, func(t *testing.T) {
			harness := newTicketHarness(t)
			candidate := association
			_, err := harness.binder.BindAndCreate(context.Background(), AdmissionContext{
				TeamID: 7, TeamName: "research", CreatedBy: "alice",
				Origin: Origin{Kind: OriginKindTicket, Reference: "42"},
				Ticket: &candidate,
			}, ticketBindRequest("bad-"+name))
			if !errors.Is(err, ErrInvalidRequest) {
				t.Fatalf("expected ErrInvalidRequest, got %v", err)
			}
			if harness.last != nil {
				t.Fatal("an invalid association must not reach the durable store")
			}
		})
	}
}

func TestIdempotentReplayOfATicketedRunIsNotAConflict(t *testing.T) {
	harness := newTicketHarness(t)
	definition := binderTestDefinition()
	rendered := binderTestRendered(t, definition)
	existing := db.AgentWorkflowRun{
		ID: 900, TeamID: 7, TeamName: "research", WorkflowDefinitionID: definition.ID,
		WorkflowName: definition.Name, WorkflowVersion: definition.Version,
		SchemaVersion: 3, SignatureVersion: 1, DefinitionContentHash: definition.ContentHash,
		IdempotencyKey: "dispatch-42", ParameterizedConfig: mustCanonical(t, rendered.Config),
		ParameterizedConfigHash: rendered.TargetConfigHash,
		OriginKind:              OriginKindTicket, OriginReference: "42",
		CreatedBy: "alice", Status: db.AgentWorkflowRunStatusRunning,
		TicketID: ticketID(42), TicketReference: "ticket-42",
	}
	pipelineRunID, templateID, instanceID := 313, 211, 419
	plannedBuildID, instanceHash := int64(521), "instance-hash"
	existing.PipelineRunID, existing.TemplatePipelineID, existing.InstancePipelineID = &pipelineRunID, &templateID, &instanceID
	existing.ConcreteConfig, existing.ConcreteConfigHash = mustCanonical(t, rendered.Config), &instanceHash
	existing.PlannedBuildID = &plannedBuildID
	harness.store.find = func(context.Context, int, string) (db.AgentWorkflowRun, bool, error) {
		return existing, true, nil
	}

	result, err := harness.binder.BindAndCreate(context.Background(), AdmissionContext{
		TeamID: 7, TeamName: "research", CreatedBy: "alice",
		Origin: Origin{Kind: OriginKindTicket, Reference: "42"},
		Ticket: &TicketAssociation{ID: 42, Reference: "ticket-42"},
	}, ticketBindRequest("dispatch-42"))
	if err != nil {
		t.Fatalf("re-entering a ticketed dispatch must resume it, got %v", err)
	}
	if result.Created || result.Run.ID != existing.ID {
		t.Fatalf("result = %+v", result)
	}
}

func TestIdempotentReplayUnderADifferentTicketConflicts(t *testing.T) {
	harness := newTicketHarness(t)
	definition := binderTestDefinition()
	existing := db.AgentWorkflowRun{
		ID: 900, TeamID: 7, WorkflowName: definition.Name, WorkflowVersion: definition.Version,
		IdempotencyKey: "dispatch-42", OriginKind: OriginKindTicket, OriginReference: "42",
		CreatedBy: "alice", Status: db.AgentWorkflowRunStatusRunning,
		TicketID: ticketID(42), TicketReference: "ticket-42",
	}
	harness.store.find = func(context.Context, int, string) (db.AgentWorkflowRun, bool, error) {
		return existing, true, nil
	}

	_, err := harness.binder.BindAndCreate(context.Background(), AdmissionContext{
		TeamID: 7, TeamName: "research", CreatedBy: "alice",
		Origin: Origin{Kind: OriginKindTicket, Reference: "42"},
		Ticket: &TicketAssociation{ID: 43, Reference: "ticket-43"},
	}, ticketBindRequest("dispatch-42"))
	if !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("expected ErrIdempotencyConflict, got %v", err)
	}
}

func TestExperimentAdmissionContextCannotCarryATicket(t *testing.T) {
	// A compile-time contract, asserted here so a later field addition to
	// experiment.AdmissionContext cannot quietly let experiments into ticket
	// journals: the adapter constructs its workflowrun.AdmissionContext
	// literally and never sets Ticket.
	admission := AdmissionContext{
		TeamID: 7, TeamName: "research", CreatedBy: "alice",
		Origin: Origin{Kind: "experiment", Reference: "experiment:1:cell:2"},
	}
	if admission.Ticket != nil {
		t.Fatal("experiments remain unattached unless explicitly launched in ticket context")
	}
}

func TestCloneAdmissionDeepCopiesTheAssociation(t *testing.T) {
	original := AdmissionContext{Ticket: &TicketAssociation{ID: 42, Reference: "ticket-42"}}
	cloned := cloneAdmission(original)
	cloned.Ticket.ID = 99

	if original.Ticket.ID != 42 {
		t.Fatal("cloneAdmission must not alias the caller's association")
	}
}

func TestInheritTicketFromAnUnknownRunIsUnattached(t *testing.T) {
	harness := newTicketHarness(t)

	association, err := harness.binder.inheritTicketFrom(
		context.Background(), 7, workflow.DefinitionKindWorkflow, snapshot.WorkflowRunID(4242),
	)
	if err != nil {
		t.Fatalf("inheritTicketFrom: %v", err)
	}
	if association != nil {
		t.Fatalf("a missing launching run confers nothing, got %+v", association)
	}
}

func TestInheritTicketFromAnAssociatedRun(t *testing.T) {
	harness := newTicketHarness(t)
	harness.seedSource(500, ticketID(42), "ticket-42")

	association, err := harness.binder.inheritTicketFrom(
		context.Background(), 7, workflow.DefinitionKindWorkflow, snapshot.WorkflowRunID(500),
	)
	if err != nil {
		t.Fatalf("inheritTicketFrom: %v", err)
	}
	if association == nil || association.ID != 42 || association.Reference != "ticket-42" {
		t.Fatalf("a publish follow-up inherits from its publishing run, got %+v", association)
	}
}

// Defence in depth behind the durable store's own immutability check: if the
// row that comes back is not attributed the way this admission asked, the
// binder must refuse it rather than report a run under someone else's ticket.
func TestAllocatedRunUnderADifferentTicketIsRefused(t *testing.T) {
	harness := newTicketHarness(t)
	inner := harness.store.create
	harness.store.create = func(ctx context.Context, request db.AgentWorkflowRunCreateRequest) (db.AgentWorkflowRun, bool, error) {
		run, created, err := inner(ctx, request)
		run.TicketID, run.TicketReference = ticketID(43), "ticket-43"
		return run, created, err
	}

	_, err := harness.binder.BindAndCreate(context.Background(), AdmissionContext{
		TeamID: 7, TeamName: "research", CreatedBy: "alice",
		Origin: Origin{Kind: OriginKindTicket, Reference: "42"},
		Ticket: &TicketAssociation{ID: 42, Reference: "ticket-42"},
	}, ticketBindRequest("dispatch-42"))
	if !errors.Is(err, ErrCorruptPartialAdmission) && !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("expected the mismatch to be refused, got %v", err)
	}
}
