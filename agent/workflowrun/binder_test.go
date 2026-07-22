package workflowrun

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/agent/workflow"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
)

func TestBindAndCreateAdmitsFromServerDerivedIdentity(t *testing.T) {
	ctx := context.Background()
	definition := binderTestDefinition()
	rendered := binderTestRendered(t, definition)
	largeSnapshotID := snapshot.SnapshotID(1<<53 + 17)
	largeRunID := snapshot.WorkflowRunID(1<<53 + 29)
	input := binderTestSnapshot(largeSnapshotID, "repository/v1")
	var order []string

	resolver := &resolverStub{live: func(context.Context, string) (workflow.Definition, bool, error) {
		order = append(order, "resolve")
		return definition, true, nil
	}}
	renderer := &rendererStub{
		full: func(got workflow.Definition) (workflow.FunctionTarget, error) {
			order = append(order, "target")
			return workflow.FunctionTarget{
				Kind: workflow.TargetWorkflow, WorkflowDefinitionID: got.ID,
				WorkflowName: got.Name, WorkflowVersion: got.Version,
				SignatureVersion: got.SignatureVersion,
				Signature: workflow.PublicSignature{Inputs: []workflow.SignaturePort{
					{Name: "repo", Type: "repository/v1"},
					{Name: "notes", Type: "review/v1", Optional: true},
				}},
			}, nil
		},
		render: func(workflow.FunctionTarget) (workflow.RenderedFunction, error) {
			order = append(order, "render")
			return rendered, nil
		},
	}
	authorizer := &authorizerStub{get: func(_ context.Context, teamID int, id snapshot.SnapshotID) (snapshot.Snapshot, bool, error) {
		order = append(order, "authorize")
		if teamID != 7 || id != largeSnapshotID {
			t.Fatalf("authorization = team %d snapshot %s", teamID, id.String())
		}
		return input, true, nil
	}}

	admitting := db.AgentWorkflowRun{
		ID: largeRunID, TeamID: 7, TeamName: "research", WorkflowDefinitionID: definition.ID,
		WorkflowName: definition.Name, WorkflowVersion: definition.Version,
		SchemaVersion: 3, SignatureVersion: 1, DefinitionContentHash: definition.ContentHash,
		IdempotencyKey: "request-7", ParameterizedConfig: mustCanonical(t, rendered.Config),
		ParameterizedConfigHash: rendered.TargetConfigHash, OriginKind: "manual",
		CreatedBy: "alice", Status: db.AgentWorkflowRunStatusAdmitting,
	}
	running := admitting
	running.Status = db.AgentWorkflowRunStatusRunning
	pipelineRunID, templateID, instanceID, plannedBuildID := 313, 211, 419, 521
	instanceHash := "instance-hash"
	running.PipelineRunID, running.TemplatePipelineID, running.InstancePipelineID = &pipelineRunID, &templateID, &instanceID
	running.ConcreteConfig, running.ConcreteConfigHash = mustCanonical(t, rendered.Config), &instanceHash
	running.PlannedBuildID = &plannedBuildID
	findCalls := 0
	store := &storeStub{
		find: func(context.Context, int, string) (db.AgentWorkflowRun, bool, error) {
			findCalls++
			if findCalls == 1 {
				order = append(order, "find")
				return db.AgentWorkflowRun{}, false, nil
			}
			return running, true, nil
		},
		create: func(_ context.Context, request db.AgentWorkflowRunCreateRequest) (db.AgentWorkflowRun, bool, error) {
			order = append(order, "allocate")
			if request.Status != db.AgentWorkflowRunStatusAdmitting || request.Inputs["repo"].ID != largeSnapshotID {
				t.Fatalf("durable request = %+v", request)
			}
			return admitting, true, nil
		},
		snapshots: func(context.Context, snapshot.WorkflowRunID) ([]db.AgentWorkflowRunSnapshotBinding, error) {
			return []db.AgentWorkflowRunSnapshotBinding{{
				WorkflowRunID: largeRunID, Direction: db.AgentWorkflowRunSnapshotInput,
				PortName: "repo", Snapshot: snapshot.SnapshotRef{ID: input.ID, Type: input.Type, Digest: input.Digest},
			}}, nil
		},
		transition: func(_ context.Context, id snapshot.WorkflowRunID, from, to db.AgentWorkflowRunStatus, message string) (bool, error) {
			order = append(order, "transition")
			if id != largeRunID || from != db.AgentWorkflowRunStatusAdmitting ||
				to != db.AgentWorkflowRunStatusRunning || message != "" {
				t.Fatalf("transition = (%s, %s, %s, %q)", id.String(), from, to, message)
			}
			return true, nil
		},
	}
	budget := &budgetStub{admit: func(_ context.Context, admission BudgetAdmission) error {
		order = append(order, "budget")
		if admission.Definition.ID != definition.ID || admission.Inputs["repo"].ID != largeSnapshotID {
			t.Fatalf("budget admission = %+v", admission)
		}
		return nil
	}}
	prepared := &preparedSecretStub{attach: func(_ context.Context, pipelineRunID int) error {
		order = append(order, "attach")
		if pipelineRunID != 313 {
			t.Fatalf("pipeline run id = %d", pipelineRunID)
		}
		return nil
	}}
	secrets := &secretStub{prepare: func(_ context.Context, admission AdmissionContext, run db.AgentWorkflowRun) (PreparedRunSecret, error) {
		order = append(order, "prepare")
		if admission.CreatedBy != "alice" || run.ID != largeRunID {
			t.Fatalf("secret preparation = (%+v, %+v)", admission, run)
		}
		return prepared, nil
	}}
	saver := &saverStub{save: func(_ context.Context, _ AdmissionContext, spec ImmutableTemplateSpec) (WorkflowRunTemplateRef, error) {
		order = append(order, "save")
		if spec.Name != rendered.TemplateName || spec.FullHash != rendered.TargetConfigHash ||
			!reflect.DeepEqual(spec.CanonicalJSON, mustCanonical(t, rendered.Config)) {
			t.Fatalf("template spec = %+v", spec)
		}
		return WorkflowRunTemplateRef{PipelineID: 211, TeamID: 7, Name: spec.Name, ConfigVersion: 19, FullHash: spec.FullHash}, nil
	}}
	creator := &creatorStub{create: func(
		ctx context.Context,
		gotRunID snapshot.WorkflowRunID,
		_ WorkflowRunTemplateRef,
		params map[string]any,
		createdBy string,
		beforeCommit BeforeWorkflowRunCommit,
	) (WorkflowRunExecution, bool, error) {
		order = append(order, "execution")
		if gotRunID != largeRunID || createdBy != "alice" {
			t.Fatalf("execution identity = (%s, %q)", gotRunID.String(), createdBy)
		}
		want := map[string]any{
			"workflow_run_id": largeRunID.String(),
			"snapshot_repo":   largeSnapshotID.String(),
			"snapshot_notes":  "0",
		}
		if !reflect.DeepEqual(params, want) {
			t.Fatalf("params = %#v, want %#v", params, want)
		}
		if err := beforeCommit(ctx, 313); err != nil {
			return WorkflowRunExecution{}, false, err
		}
		return WorkflowRunExecution{}, true, nil
	}}

	binder, err := NewBinder(resolver, renderer, authorizer, store, budget, saver, creator, secrets)
	if err != nil {
		t.Fatalf("NewBinder: %v", err)
	}
	result, err := binder.BindAndCreate(ctx, AdmissionContext{
		TeamID: 7, TeamName: "research", CreatedBy: "alice", Origin: Origin{Kind: "manual"},
	}, BindRequest{
		WorkflowName: definition.Name, Inputs: map[string]snapshot.SnapshotID{"repo": largeSnapshotID},
		IdempotencyKey: "request-7",
	})
	if err != nil {
		t.Fatalf("BindAndCreate: %v", err)
	}
	if !result.Created || result.Run.Status != db.AgentWorkflowRunStatusRunning || result.Run.ID != largeRunID {
		t.Fatalf("result = %+v", result)
	}
	wantOrder := []string{"find", "resolve", "target", "render", "authorize", "budget", "allocate", "prepare", "save", "execution", "attach", "transition"}
	if !reflect.DeepEqual(order, wantOrder) {
		t.Fatalf("order = %#v, want %#v", order, wantOrder)
	}
}

func TestBindAndCreateUsesExplicitResolutionAndExactFunctionSelectionOnce(t *testing.T) {
	definition := binderTestDefinition()
	rendered := binderTestRendered(t, definition)
	rendered.TemplateName, _ = workflow.TemplateName(
		workflow.TargetFunction, definition.Name, definition.Version, "review-node", rendered.TargetConfigHash,
	)
	target := workflow.FunctionTarget{
		Kind: workflow.TargetFunction, WorkflowDefinitionID: definition.ID,
		WorkflowName: definition.Name, WorkflowVersion: definition.Version,
		SignatureVersion: definition.SignatureVersion, FunctionID: "review-node",
		Signature: rendered.TargetSignature,
	}
	getCalls, extractCalls, renderCalls := 0, 0, 0
	store := &storeStub{find: func(context.Context, int, string) (db.AgentWorkflowRun, bool, error) {
		return db.AgentWorkflowRun{}, false, nil
	}}
	binder, err := NewBinder(
		&resolverStub{
			live: func(context.Context, string) (workflow.Definition, bool, error) {
				t.Fatal("explicit resolution must not call Live")
				return workflow.Definition{}, false, nil
			},
			get: func(_ context.Context, name string, version int) (workflow.Definition, bool, error) {
				getCalls++
				if name != definition.Name || version != definition.Version {
					t.Fatalf("Get = (%q, %d)", name, version)
				}
				return definition, true, nil
			},
		},
		&rendererStub{
			full: func(workflow.Definition) (workflow.FunctionTarget, error) {
				t.Fatal("function selection must not render the whole workflow")
				return workflow.FunctionTarget{}, nil
			},
			extract: func(_ workflow.Definition, functionID string) (workflow.FunctionTarget, error) {
				extractCalls++
				if functionID != "review-node" {
					t.Fatalf("function ID = %q", functionID)
				}
				return target, nil
			},
			render: func(got workflow.FunctionTarget) (workflow.RenderedFunction, error) {
				renderCalls++
				if got.FunctionID != target.FunctionID {
					t.Fatalf("target = %+v", got)
				}
				return rendered, nil
			},
		},
		&authorizerStub{get: func(context.Context, int, snapshot.SnapshotID) (snapshot.Snapshot, bool, error) {
			return snapshot.Snapshot{}, false, nil
		}},
		store,
		&budgetStub{}, &saverStub{}, &creatorStub{}, &secretStub{},
	)
	if err != nil {
		t.Fatal(err)
	}
	version := definition.Version
	_, err = binder.BindAndCreate(context.Background(), AdmissionContext{
		TeamID: 7, TeamName: "research", CreatedBy: "alice", Origin: Origin{Kind: "manual"},
	}, BindRequest{
		WorkflowName: definition.Name, Version: &version, FunctionID: "review-node",
		Inputs: map[string]snapshot.SnapshotID{"repo": 91}, IdempotencyKey: "explicit-function",
	})
	if !errors.Is(err, ErrSnapshotUnavailable) {
		t.Fatalf("error = %v", err)
	}
	if getCalls != 1 || extractCalls != 1 || renderCalls != 1 {
		t.Fatalf("calls = get %d extract %d render %d", getCalls, extractCalls, renderCalls)
	}
}

func TestBindAndCreateExistingRunningRunHasNoExternalSideEffects(t *testing.T) {
	run := db.AgentWorkflowRun{
		ID: 41, TeamID: 7, TeamName: "old-name", WorkflowName: "review-flow", WorkflowVersion: 3,
		SchemaVersion: 3, SignatureVersion: 1, DefinitionContentHash: "definition-hash",
		IdempotencyKey: "same", OriginKind: "ticket", OriginReference: "ABC-1",
		CreatedBy: "alice", Status: db.AgentWorkflowRunStatusRunning,
	}
	pipelineRunID, templateID, instanceID, plannedBuildID := 1, 2, 3, 4
	instanceHash := "instance-hash"
	run.PipelineRunID, run.TemplatePipelineID, run.InstancePipelineID = &pipelineRunID, &templateID, &instanceID
	run.ConcreteConfig, run.ConcreteConfigHash = json.RawMessage(`{"jobs":[]}`), &instanceHash
	run.PlannedBuildID = &plannedBuildID
	store := &storeStub{
		find: func(context.Context, int, string) (db.AgentWorkflowRun, bool, error) { return run, true, nil },
		snapshots: func(context.Context, snapshot.WorkflowRunID) ([]db.AgentWorkflowRunSnapshotBinding, error) {
			return nil, nil
		},
	}
	unexpected := errors.New("unexpected external side effect")
	binder, err := NewBinder(
		&resolverStub{live: func(context.Context, string) (workflow.Definition, bool, error) {
			return workflow.Definition{}, false, unexpected
		}},
		&rendererStub{},
		&authorizerStub{get: func(context.Context, int, snapshot.SnapshotID) (snapshot.Snapshot, bool, error) {
			return snapshot.Snapshot{}, false, unexpected
		}},
		store,
		&budgetStub{admit: func(context.Context, BudgetAdmission) error { return unexpected }},
		&saverStub{save: func(context.Context, AdmissionContext, ImmutableTemplateSpec) (WorkflowRunTemplateRef, error) {
			return WorkflowRunTemplateRef{}, unexpected
		}},
		&creatorStub{create: func(context.Context, snapshot.WorkflowRunID, WorkflowRunTemplateRef, map[string]any, string, BeforeWorkflowRunCommit) (WorkflowRunExecution, bool, error) {
			return WorkflowRunExecution{}, false, unexpected
		}},
		&secretStub{prepare: func(context.Context, AdmissionContext, db.AgentWorkflowRun) (PreparedRunSecret, error) {
			return nil, unexpected
		}},
	)
	if err != nil {
		t.Fatalf("NewBinder: %v", err)
	}
	result, err := binder.BindAndCreate(context.Background(), AdmissionContext{
		TeamID: 7, TeamName: "renamed", CreatedBy: "alice", Origin: Origin{Kind: "ticket", Reference: "ABC-1"},
	}, BindRequest{WorkflowName: "review-flow", Inputs: map[string]snapshot.SnapshotID{}, IdempotencyKey: "same"})
	if err != nil {
		t.Fatalf("BindAndCreate: %v", err)
	}
	if result.Created || result.Run.ID != run.ID {
		t.Fatalf("result = %+v", result)
	}
}

func TestBindAndCreateResumesCleanAdmittingRunWithoutResolvingOrRendering(t *testing.T) {
	definition := binderTestDefinition()
	rendered := binderTestRendered(t, definition)
	input := binderTestSnapshot(91, "repository/v1")
	run := db.AgentWorkflowRun{
		ID: 41, TeamID: 7, TeamName: "historical-name", WorkflowDefinitionID: definition.ID,
		WorkflowName: definition.Name, WorkflowVersion: definition.Version,
		SchemaVersion: 3, SignatureVersion: 1, DefinitionContentHash: definition.ContentHash,
		IdempotencyKey: "resume", ParameterizedConfig: mustCanonical(t, rendered.Config),
		ParameterizedConfigHash: rendered.TargetConfigHash, OriginKind: "manual", CreatedBy: "alice",
		Status: db.AgentWorkflowRunStatusAdmitting,
	}
	running := run
	running.Status = db.AgentWorkflowRunStatusRunning
	pipelineRunID, templateID, instanceID, plannedBuildID := 1, 2, 3, 4
	instanceHash := "instance"
	running.PipelineRunID, running.TemplatePipelineID, running.InstancePipelineID = &pipelineRunID, &templateID, &instanceID
	running.ConcreteConfig, running.ConcreteConfigHash = json.RawMessage(`{"jobs":[]}`), &instanceHash
	running.PlannedBuildID = &plannedBuildID
	findCalls := 0
	store := &storeStub{
		find: func(context.Context, int, string) (db.AgentWorkflowRun, bool, error) {
			findCalls++
			if findCalls == 1 {
				return run, true, nil
			}
			return running, true, nil
		},
		snapshots: func(context.Context, snapshot.WorkflowRunID) ([]db.AgentWorkflowRunSnapshotBinding, error) {
			return []db.AgentWorkflowRunSnapshotBinding{{
				WorkflowRunID: run.ID, Direction: db.AgentWorkflowRunSnapshotInput, PortName: "repo",
				Snapshot: snapshot.SnapshotRef{ID: input.ID, Type: input.Type, Digest: input.Digest},
			}}, nil
		},
		transition: func(_ context.Context, id snapshot.WorkflowRunID, from, to db.AgentWorkflowRunStatus, message string) (bool, error) {
			if id != run.ID || from != db.AgentWorkflowRunStatusAdmitting ||
				to != db.AgentWorkflowRunStatusRunning || message != "" {
				t.Fatalf("transition = (%s, %s, %s, %q)", id.String(), from, to, message)
			}
			return true, nil
		},
	}
	resolverCalled := false
	resolver := &resolverStub{live: func(context.Context, string) (workflow.Definition, bool, error) {
		resolverCalled = true
		return workflow.Definition{}, false, errors.New("must not resolve")
	}}
	prepared := &preparedSecretStub{attach: func(context.Context, int) error { return nil }}
	binder, err := NewBinder(
		resolver,
		&rendererStub{},
		&authorizerStub{get: func(context.Context, int, snapshot.SnapshotID) (snapshot.Snapshot, bool, error) {
			t.Fatal("must not authorize a durable retry")
			return snapshot.Snapshot{}, false, nil
		}},
		store,
		&budgetStub{admit: func(context.Context, BudgetAdmission) error {
			t.Fatal("must not re-admit budget")
			return nil
		}},
		&saverStub{save: func(context.Context, AdmissionContext, ImmutableTemplateSpec) (WorkflowRunTemplateRef, error) {
			return WorkflowRunTemplateRef{PipelineID: 2, TeamID: 7, Name: rendered.TemplateName, ConfigVersion: 1, FullHash: rendered.TargetConfigHash}, nil
		}},
		&creatorStub{create: func(ctx context.Context, _ snapshot.WorkflowRunID, _ WorkflowRunTemplateRef, params map[string]any, _ string, callback BeforeWorkflowRunCommit) (WorkflowRunExecution, bool, error) {
			if params["snapshot_notes"] != "0" || params["snapshot_repo"] != input.ID.String() {
				t.Fatalf("durable params = %#v", params)
			}
			return WorkflowRunExecution{}, true, callback(ctx, 1)
		}},
		&secretStub{prepare: func(context.Context, AdmissionContext, db.AgentWorkflowRun) (PreparedRunSecret, error) {
			return prepared, nil
		}},
	)
	if err != nil {
		t.Fatal(err)
	}
	result, err := binder.BindAndCreate(context.Background(), AdmissionContext{
		TeamID: 7, TeamName: "current-name", CreatedBy: "alice", Origin: Origin{Kind: "manual"},
	}, BindRequest{
		WorkflowName: definition.Name, Inputs: map[string]snapshot.SnapshotID{"repo": input.ID},
		IdempotencyKey: "resume",
	})
	if err != nil {
		t.Fatalf("BindAndCreate: %v", err)
	}
	if result.Created || result.Run.Status != db.AgentWorkflowRunStatusRunning || resolverCalled {
		t.Fatalf("result = %+v, resolver called = %t", result, resolverCalled)
	}
}

func TestBindAndCreateRepairsCompleteAdmittingCrashWindowWithoutExternalSideEffects(t *testing.T) {
	run := db.AgentWorkflowRun{
		ID: 41, TeamID: 7, TeamName: "research", WorkflowName: "review-flow", WorkflowVersion: 3,
		SchemaVersion: 3, SignatureVersion: 1, DefinitionContentHash: "definition-hash",
		IdempotencyKey: "crash-window", ParameterizedConfig: json.RawMessage(`{"jobs":[]}`),
		ParameterizedConfigHash: "parameterized-hash", OriginKind: "manual", CreatedBy: "alice",
		Status: db.AgentWorkflowRunStatusAdmitting,
	}
	pipelineRunID, templateID, instanceID, plannedBuildID := 1, 2, 3, 4
	instanceHash := "instance-hash"
	run.PipelineRunID, run.TemplatePipelineID, run.InstancePipelineID = &pipelineRunID, &templateID, &instanceID
	run.ConcreteConfig, run.ConcreteConfigHash = json.RawMessage(`{"jobs":[{"name":"run"}]}`), &instanceHash
	run.PlannedBuildID = &plannedBuildID
	running := run
	running.Status = db.AgentWorkflowRunStatusRunning

	findCalls := 0
	store := &storeStub{
		find: func(context.Context, int, string) (db.AgentWorkflowRun, bool, error) {
			findCalls++
			if findCalls == 1 {
				return run, true, nil
			}
			return running, true, nil
		},
		snapshots: func(context.Context, snapshot.WorkflowRunID) ([]db.AgentWorkflowRunSnapshotBinding, error) {
			return nil, nil
		},
		transition: func(_ context.Context, id snapshot.WorkflowRunID, from, to db.AgentWorkflowRunStatus, message string) (bool, error) {
			if id != run.ID || from != db.AgentWorkflowRunStatusAdmitting ||
				to != db.AgentWorkflowRunStatusRunning || message != "" {
				t.Fatalf("transition = (%s, %s, %s, %q)", id.String(), from, to, message)
			}
			return true, nil
		},
	}
	unwanted := errors.New("unexpected external side effect")
	binder, err := NewBinder(
		&resolverStub{live: func(context.Context, string) (workflow.Definition, bool, error) {
			return workflow.Definition{}, false, unwanted
		}},
		&rendererStub{},
		&authorizerStub{get: func(context.Context, int, snapshot.SnapshotID) (snapshot.Snapshot, bool, error) {
			return snapshot.Snapshot{}, false, unwanted
		}},
		store,
		&budgetStub{admit: func(context.Context, BudgetAdmission) error { return unwanted }},
		&saverStub{save: func(context.Context, AdmissionContext, ImmutableTemplateSpec) (WorkflowRunTemplateRef, error) {
			return WorkflowRunTemplateRef{}, unwanted
		}},
		&creatorStub{create: func(context.Context, snapshot.WorkflowRunID, WorkflowRunTemplateRef, map[string]any, string, BeforeWorkflowRunCommit) (WorkflowRunExecution, bool, error) {
			return WorkflowRunExecution{}, false, unwanted
		}},
		&secretStub{prepare: func(context.Context, AdmissionContext, db.AgentWorkflowRun) (PreparedRunSecret, error) {
			return nil, unwanted
		}},
	)
	if err != nil {
		t.Fatal(err)
	}
	result, err := binder.BindAndCreate(context.Background(), AdmissionContext{
		TeamID: 7, TeamName: "research", CreatedBy: "alice", Origin: Origin{Kind: "manual"},
	}, BindRequest{WorkflowName: "review-flow", Inputs: map[string]snapshot.SnapshotID{}, IdempotencyKey: "crash-window"})
	if err != nil {
		t.Fatalf("BindAndCreate: %v", err)
	}
	if result.Created || result.Run.Status != db.AgentWorkflowRunStatusRunning || result.Run.ID != run.ID {
		t.Fatalf("result = %+v", result)
	}
}

func TestBindAndCreateSanitizesAndPersistsPostAllocationFailure(t *testing.T) {
	definition := binderTestDefinition()
	rendered := binderTestRendered(t, definition)
	run := db.AgentWorkflowRun{
		ID: 41, TeamID: 7, TeamName: "research", WorkflowDefinitionID: definition.ID,
		WorkflowName: definition.Name, WorkflowVersion: definition.Version,
		SchemaVersion: 3, SignatureVersion: 1, DefinitionContentHash: definition.ContentHash,
		IdempotencyKey: "failure", ParameterizedConfig: mustCanonical(t, rendered.Config),
		ParameterizedConfigHash: rendered.TargetConfigHash, OriginKind: "manual", CreatedBy: "alice",
		Status: db.AgentWorkflowRunStatusAdmitting,
	}
	input := binderTestSnapshot(91, "repository/v1")
	var persisted string
	store := &storeStub{
		find: func(context.Context, int, string) (db.AgentWorkflowRun, bool, error) { return run, true, nil },
		snapshots: func(context.Context, snapshot.WorkflowRunID) ([]db.AgentWorkflowRunSnapshotBinding, error) {
			return []db.AgentWorkflowRunSnapshotBinding{{
				WorkflowRunID: run.ID, Direction: db.AgentWorkflowRunSnapshotInput, PortName: "repo",
				Snapshot: snapshot.SnapshotRef{ID: input.ID, Type: input.Type, Digest: input.Digest},
			}}, nil
		},
		transition: func(_ context.Context, id snapshot.WorkflowRunID, from, to db.AgentWorkflowRunStatus, message string) (bool, error) {
			if id != run.ID || from != db.AgentWorkflowRunStatusAdmitting || to != db.AgentWorkflowRunStatusErrored {
				t.Fatalf("transition = (%s, %s, %s)", id.String(), from, to)
			}
			persisted = message
			return true, nil
		},
	}
	binder, err := NewBinder(
		&resolverStub{}, &rendererStub{}, &authorizerStub{}, store,
		&budgetStub{}, &saverStub{}, &creatorStub{},
		&secretStub{prepare: func(context.Context, AdmissionContext, db.AgentWorkflowRun) (PreparedRunSecret, error) {
			return nil, errors.New("credential=super-secret " + strings.Repeat("界", 5000))
		}},
	)
	if err != nil {
		t.Fatal(err)
	}
	_, err = binder.BindAndCreate(context.Background(), AdmissionContext{
		TeamID: 7, TeamName: "research", CreatedBy: "alice", Origin: Origin{Kind: "manual"},
	}, BindRequest{
		WorkflowName: definition.Name, Inputs: map[string]snapshot.SnapshotID{"repo": input.ID},
		IdempotencyKey: "failure",
	})
	if !errors.Is(err, ErrPlatformFailure) {
		t.Fatalf("error = %v", err)
	}
	if persisted != ErrPlatformFailure.Error() || len(persisted) > 4096 || strings.Contains(persisted, "secret") {
		t.Fatalf("persisted error = %q", persisted)
	}
}

func TestBindAndCreateCategorizesTemplateAndExecutionFailures(t *testing.T) {
	definition := binderTestDefinition()
	rendered := binderTestRendered(t, definition)
	input := binderTestSnapshot(91, "repository/v1")
	baseRun := db.AgentWorkflowRun{
		ID: 41, TeamID: 7, TeamName: "research", WorkflowDefinitionID: definition.ID,
		WorkflowName: definition.Name, WorkflowVersion: definition.Version,
		SchemaVersion: 3, SignatureVersion: 1, DefinitionContentHash: definition.ContentHash,
		IdempotencyKey: "failure", ParameterizedConfig: mustCanonical(t, rendered.Config),
		ParameterizedConfigHash: rendered.TargetConfigHash, OriginKind: "manual", CreatedBy: "alice",
		Status: db.AgentWorkflowRunStatusAdmitting,
	}
	for _, test := range []struct {
		name         string
		templateErr  error
		executionErr error
		want         error
	}{
		{name: "template platform failure", templateErr: errors.New("database details"), want: ErrPlatformFailure},
		{name: "template collision", templateErr: ErrImmutableTemplateCollision, want: ErrImmutableTemplateCollision},
		{name: "execution platform failure", executionErr: errors.New("callback or SQL details"), want: ErrPlatformFailure},
	} {
		t.Run(test.name, func(t *testing.T) {
			run := baseRun
			var persisted string
			store := &storeStub{
				find: func(context.Context, int, string) (db.AgentWorkflowRun, bool, error) { return run, true, nil },
				snapshots: func(context.Context, snapshot.WorkflowRunID) ([]db.AgentWorkflowRunSnapshotBinding, error) {
					return []db.AgentWorkflowRunSnapshotBinding{{
						WorkflowRunID: run.ID, Direction: db.AgentWorkflowRunSnapshotInput, PortName: "repo",
						Snapshot: snapshot.SnapshotRef{ID: input.ID, Type: input.Type, Digest: input.Digest},
					}}, nil
				},
				transition: func(_ context.Context, _ snapshot.WorkflowRunID, _, _ db.AgentWorkflowRunStatus, message string) (bool, error) {
					persisted = message
					return true, nil
				},
			}
			prepared := &preparedSecretStub{attach: func(context.Context, int) error { return nil }}
			binder, err := NewBinder(
				&resolverStub{}, &rendererStub{}, &authorizerStub{}, store, &budgetStub{},
				&saverStub{save: func(context.Context, AdmissionContext, ImmutableTemplateSpec) (WorkflowRunTemplateRef, error) {
					if test.templateErr != nil {
						return WorkflowRunTemplateRef{}, test.templateErr
					}
					return WorkflowRunTemplateRef{PipelineID: 2, TeamID: 7, Name: rendered.TemplateName, ConfigVersion: 1, FullHash: rendered.TargetConfigHash}, nil
				}},
				&creatorStub{create: func(context.Context, snapshot.WorkflowRunID, WorkflowRunTemplateRef, map[string]any, string, BeforeWorkflowRunCommit) (WorkflowRunExecution, bool, error) {
					return WorkflowRunExecution{}, false, test.executionErr
				}},
				&secretStub{prepare: func(context.Context, AdmissionContext, db.AgentWorkflowRun) (PreparedRunSecret, error) {
					return prepared, nil
				}},
			)
			if err != nil {
				t.Fatal(err)
			}
			_, err = binder.BindAndCreate(context.Background(), AdmissionContext{
				TeamID: 7, TeamName: "research", CreatedBy: "alice", Origin: Origin{Kind: "manual"},
			}, BindRequest{
				WorkflowName: definition.Name, Inputs: map[string]snapshot.SnapshotID{"repo": input.ID}, IdempotencyKey: "failure",
			})
			if !errors.Is(err, test.want) {
				t.Fatalf("error = %v, want %v", err, test.want)
			}
			if persisted != test.want.Error() {
				t.Fatalf("persisted = %q, want %q", persisted, test.want.Error())
			}
		})
	}
}

func TestBindAndCreateRejectsPartialAdmittingState(t *testing.T) {
	run := db.AgentWorkflowRun{
		ID: 41, TeamID: 7, WorkflowName: "review-flow", WorkflowVersion: 3,
		IdempotencyKey: "partial", OriginKind: "manual", CreatedBy: "alice",
		Status: db.AgentWorkflowRunStatusAdmitting,
	}
	pipelineRunID := 1
	run.PipelineRunID = &pipelineRunID
	store := &storeStub{
		find: func(context.Context, int, string) (db.AgentWorkflowRun, bool, error) { return run, true, nil },
		snapshots: func(context.Context, snapshot.WorkflowRunID) ([]db.AgentWorkflowRunSnapshotBinding, error) {
			return nil, nil
		},
	}
	binder, err := NewBinder(
		&resolverStub{}, &rendererStub{}, &authorizerStub{}, store,
		&budgetStub{}, &saverStub{}, &creatorStub{}, &secretStub{},
	)
	if err != nil {
		t.Fatal(err)
	}
	_, err = binder.BindAndCreate(context.Background(), AdmissionContext{
		TeamID: 7, TeamName: "research", CreatedBy: "alice", Origin: Origin{Kind: "manual"},
	}, BindRequest{WorkflowName: "review-flow", Inputs: map[string]snapshot.SnapshotID{}, IdempotencyKey: "partial"})
	if !errors.Is(err, ErrCorruptPartialAdmission) {
		t.Fatalf("error = %v", err)
	}
}

func TestBindAndCreateNilVersionRaceRejectsDifferentResolvedWinner(t *testing.T) {
	definition := binderTestDefinition()
	rendered := binderTestRendered(t, definition)
	input := binderTestSnapshot(91, "repository/v1")
	winner := db.AgentWorkflowRun{
		ID: 52, TeamID: 7, TeamName: "research", WorkflowDefinitionID: definition.ID + 1,
		WorkflowName: definition.Name, WorkflowVersion: definition.Version + 1,
		SchemaVersion: 3, SignatureVersion: 1, DefinitionContentHash: "different-live-definition",
		IdempotencyKey: "live-race", ParameterizedConfig: mustCanonical(t, rendered.Config),
		ParameterizedConfigHash: rendered.TargetConfigHash, OriginKind: "manual", CreatedBy: "alice",
		Status: db.AgentWorkflowRunStatusRunning,
	}
	pipelineRunID, templateID, instanceID, plannedBuildID := 1, 2, 3, 4
	instanceHash := "instance"
	winner.PipelineRunID, winner.TemplatePipelineID, winner.InstancePipelineID = &pipelineRunID, &templateID, &instanceID
	winner.ConcreteConfig, winner.ConcreteConfigHash = json.RawMessage(`{"jobs":[]}`), &instanceHash
	winner.PlannedBuildID = &plannedBuildID
	findCalls := 0
	store := &storeStub{
		find: func(context.Context, int, string) (db.AgentWorkflowRun, bool, error) {
			findCalls++
			if findCalls == 1 {
				return db.AgentWorkflowRun{}, false, nil
			}
			return winner, true, nil
		},
		create: func(context.Context, db.AgentWorkflowRunCreateRequest) (db.AgentWorkflowRun, bool, error) {
			return db.AgentWorkflowRun{}, false, errors.New("concurrent idempotency winner")
		},
		snapshots: func(context.Context, snapshot.WorkflowRunID) ([]db.AgentWorkflowRunSnapshotBinding, error) {
			return []db.AgentWorkflowRunSnapshotBinding{{
				WorkflowRunID: winner.ID, Direction: db.AgentWorkflowRunSnapshotInput, PortName: "repo",
				Snapshot: snapshot.SnapshotRef{ID: input.ID, Type: input.Type, Digest: input.Digest},
			}}, nil
		},
	}
	target := workflow.FunctionTarget{
		Kind: workflow.TargetWorkflow, WorkflowDefinitionID: definition.ID,
		WorkflowName: definition.Name, WorkflowVersion: definition.Version,
		SignatureVersion: definition.SignatureVersion,
		Signature: workflow.PublicSignature{Inputs: []workflow.SignaturePort{
			{Name: "repo", Type: "repository/v1"},
			{Name: "notes", Type: "review/v1", Optional: true},
		}},
	}
	binder, err := NewBinder(
		&resolverStub{live: func(context.Context, string) (workflow.Definition, bool, error) {
			return definition, true, nil
		}},
		&rendererStub{
			full:   func(workflow.Definition) (workflow.FunctionTarget, error) { return target, nil },
			render: func(workflow.FunctionTarget) (workflow.RenderedFunction, error) { return rendered, nil },
		},
		&authorizerStub{get: func(context.Context, int, snapshot.SnapshotID) (snapshot.Snapshot, bool, error) {
			return input, true, nil
		}},
		store,
		&budgetStub{admit: func(context.Context, BudgetAdmission) error { return nil }},
		&saverStub{}, &creatorStub{}, &secretStub{},
	)
	if err != nil {
		t.Fatal(err)
	}
	_, err = binder.BindAndCreate(context.Background(), AdmissionContext{
		TeamID: 7, TeamName: "research", CreatedBy: "alice", Origin: Origin{Kind: "manual"},
	}, BindRequest{
		WorkflowName: definition.Name, Inputs: map[string]snapshot.SnapshotID{"repo": input.ID},
		IdempotencyKey: "live-race",
	})
	if !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("error = %v, want idempotency conflict", err)
	}
}

func TestRequestValidationGrammarAndBounds(t *testing.T) {
	positiveVersion := 3
	positiveRetry := snapshot.WorkflowRunID(9)
	baseAdmission := AdmissionContext{
		TeamID: 7, TeamName: "research", CreatedBy: "alice",
		Origin: Origin{Kind: "ticket", Reference: "ABC-1"},
	}
	baseRequest := BindRequest{
		WorkflowName: "review-flow", Version: &positiveVersion, FunctionID: "review-node",
		Inputs: map[string]snapshot.SnapshotID{"repo": 5}, IdempotencyKey: "request-1", RetryOf: &positiveRetry,
	}
	tests := []struct {
		name   string
		mutate func(*AdmissionContext, *BindRequest)
	}{
		{name: "zero team", mutate: func(a *AdmissionContext, _ *BindRequest) { a.TeamID = 0 }},
		{name: "blank team name", mutate: func(a *AdmissionContext, _ *BindRequest) { a.TeamName = " " }},
		{name: "team outer whitespace", mutate: func(a *AdmissionContext, _ *BindRequest) { a.TeamName = " research" }},
		{name: "team control", mutate: func(a *AdmissionContext, _ *BindRequest) { a.TeamName = "research\x00" }},
		{name: "long creator", mutate: func(a *AdmissionContext, _ *BindRequest) { a.CreatedBy = strings.Repeat("a", 257) }},
		{name: "bad workflow identifier", mutate: func(_ *AdmissionContext, r *BindRequest) { r.WorkflowName = "Bad Name" }},
		{name: "bad function identifier", mutate: func(_ *AdmissionContext, r *BindRequest) { r.FunctionID = "Bad Name" }},
		{name: "zero version", mutate: func(_ *AdmissionContext, r *BindRequest) { value := 0; r.Version = &value }},
		{name: "zero retry", mutate: func(_ *AdmissionContext, r *BindRequest) { value := snapshot.WorkflowRunID(0); r.RetryOf = &value }},
		{name: "blank key", mutate: func(_ *AdmissionContext, r *BindRequest) { r.IdempotencyKey = "" }},
		{name: "key outer whitespace", mutate: func(_ *AdmissionContext, r *BindRequest) { r.IdempotencyKey = " request" }},
		{name: "key control", mutate: func(_ *AdmissionContext, r *BindRequest) { r.IdempotencyKey = "request\n" }},
		{name: "bad origin kind", mutate: func(a *AdmissionContext, _ *BindRequest) { a.Origin.Kind = "Ticket" }},
		{name: "empty nonmanual reference", mutate: func(a *AdmissionContext, _ *BindRequest) { a.Origin.Reference = "" }},
		{name: "origin reference control", mutate: func(a *AdmissionContext, _ *BindRequest) { a.Origin.Reference = "ABC\x00" }},
		{name: "long origin reference", mutate: func(a *AdmissionContext, _ *BindRequest) { a.Origin.Reference = strings.Repeat("a", 1025) }},
		{name: "bad input port", mutate: func(_ *AdmissionContext, r *BindRequest) { r.Inputs = map[string]snapshot.SnapshotID{"Bad Port": 5} }},
		{name: "zero snapshot", mutate: func(_ *AdmissionContext, r *BindRequest) { r.Inputs = map[string]snapshot.SnapshotID{"repo": 0} }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			admission, request := baseAdmission, baseRequest
			request.Inputs = map[string]snapshot.SnapshotID{"repo": 5}
			test.mutate(&admission, &request)
			_, _, err := validateAndClone(admission, request)
			if !errors.Is(err, ErrInvalidRequest) {
				t.Fatalf("error = %v", err)
			}
		})
	}

	admission, request, err := validateAndClone(baseAdmission, baseRequest)
	if err != nil {
		t.Fatalf("valid request: %v", err)
	}
	baseRequest.Inputs["repo"] = 99
	positiveVersion = 99
	positiveRetry = 99
	if admission.TeamName != "research" || request.Inputs["repo"] != 5 || *request.Version != 3 || *request.RetryOf != 9 {
		t.Fatalf("validated request aliases caller state: %+v %+v", admission, request)
	}
}

func TestInputCoverageAndAuthorization(t *testing.T) {
	signature := workflow.PublicSignature{Inputs: []workflow.SignaturePort{
		{Name: "repo", Type: "repository/v1"},
		{Name: "notes", Type: "review/v1", Optional: true},
	}}
	if err := validateInputCoverage(signature, map[string]snapshot.SnapshotID{"repo": 1}); err != nil {
		t.Fatalf("required plus absent optional: %v", err)
	}
	for name, inputs := range map[string]map[string]snapshot.SnapshotID{
		"missing required": {},
		"extra":            {"repo": 1, "other": 2},
	} {
		t.Run(name, func(t *testing.T) {
			if err := validateInputCoverage(signature, inputs); !errors.Is(err, ErrInvalidRequest) {
				t.Fatalf("error = %v", err)
			}
		})
	}

	exact := binderTestSnapshot(1, "repository/v1")
	tests := []struct {
		name  string
		value snapshot.Snapshot
		found bool
		err   error
		want  error
	}{
		{name: "missing", found: false, want: ErrSnapshotUnavailable},
		{name: "authorization failure", err: errors.New("private detail"), want: ErrPlatformFailure},
		{name: "wrong id", value: func() snapshot.Snapshot { value := exact; value.ID = 2; return value }(), found: true, want: ErrSnapshotUnavailable},
		{name: "expired", value: func() snapshot.Snapshot {
			value := exact
			value.ContentState = snapshot.ContentStateExpired
			return value
		}(), found: true, want: ErrSnapshotUnavailable},
		{name: "wrong type", value: func() snapshot.Snapshot { value := exact; value.Type = "review/v1"; return value }(), found: true, want: ErrSnapshotTypeMismatch},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			binder := &Binder{snapshots: &authorizerStub{get: func(context.Context, int, snapshot.SnapshotID) (snapshot.Snapshot, bool, error) {
				return test.value, test.found, test.err
			}}}
			_, err := binder.authorizeInputs(context.Background(), 7, signature, map[string]snapshot.SnapshotID{"repo": 1})
			if !errors.Is(err, test.want) {
				t.Fatalf("error = %v, want %v", err, test.want)
			}
			if strings.Contains(fmt.Sprint(err), "private detail") {
				t.Fatalf("authorization detail leaked: %v", err)
			}
		})
	}
}

func TestRenderedTargetRequiresExactTaskFiveParameterSchemas(t *testing.T) {
	definition := binderTestDefinition()
	rendered := binderTestRendered(t, definition)
	target := workflow.FunctionTarget{
		Kind: workflow.TargetWorkflow, WorkflowDefinitionID: definition.ID,
		WorkflowName: definition.Name, WorkflowVersion: definition.Version,
		SignatureVersion: definition.SignatureVersion, Signature: rendered.TargetSignature,
	}
	rendered.Config.Params = rendered.Config.Params[:2]
	rendered.TargetConfigHash, _ = workflow.TargetConfigHash(rendered.Config)
	rendered.TemplateName, _ = workflow.TemplateName(
		workflow.TargetWorkflow, definition.Name, definition.Version, "", rendered.TargetConfigHash,
	)
	if _, err := validateRendered(target, rendered); !errors.Is(err, ErrPlatformFailure) {
		t.Fatalf("error = %v", err)
	}
}

func binderTestDefinition() workflow.Definition {
	function := &workflow.FunctionConfig{
		SignatureVersion: 1,
		Inputs: []snapshot.Port{
			{Name: "repo", Type: "repository/v1"},
			{Name: "notes", Type: "review/v1", Optional: true},
		},
		Plan: []atc.Step{{Config: &atc.TaskStep{
			Name: "noop", Config: &atc.TaskConfig{Platform: "linux", Run: atc.TaskRunConfig{Path: "/bin/true"}},
		}}},
	}
	return workflow.Definition{
		ID: 101, Name: "review-flow", Version: 3, SchemaVersion: 3, SignatureVersion: 1,
		ContentHash: "definition-hash",
		Compiled:    workflow.CompiledDefinition{SchemaVersion: 3, Name: "review-flow", Function: function},
	}
}

func binderTestRendered(t *testing.T, definition workflow.Definition) workflow.RenderedFunction {
	t.Helper()
	config := atc.Config{
		Template: true,
		Params: []atc.ParamSchema{
			{Name: "workflow_run_id", Type: "string", Format: atc.ParamFormatPositiveDecimalInt64, Required: true},
			{Name: "snapshot_repo", Type: "string", Format: atc.ParamFormatPositiveDecimalInt64, Required: true},
			{Name: "snapshot_notes", Type: "string", Format: atc.ParamFormatZeroOrPositiveDecimalInt64, Default: "0"},
		},
		Jobs: atc.JobConfigs{{Name: "run", PlanSequence: []atc.Step{{Config: &atc.TaskStep{
			Name: "noop", Config: &atc.TaskConfig{Platform: "linux", Run: atc.TaskRunConfig{Path: "/bin/true"}},
		}}}}},
	}
	hash, err := workflow.TargetConfigHash(config)
	if err != nil {
		t.Fatal(err)
	}
	name, err := workflow.TemplateName(workflow.TargetWorkflow, definition.Name, definition.Version, "", hash)
	if err != nil {
		t.Fatal(err)
	}
	return workflow.RenderedFunction{
		TemplateName: name,
		TargetSignature: workflow.PublicSignature{Inputs: []workflow.SignaturePort{
			{Name: "repo", Type: "repository/v1"}, {Name: "notes", Type: "review/v1", Optional: true},
		}},
		Config: config, TargetConfigHash: hash,
		InputParamNames: map[string]string{"repo": "snapshot_repo", "notes": "snapshot_notes"},
	}
}

func binderTestSnapshot(id snapshot.SnapshotID, typ snapshot.TypeRef) snapshot.Snapshot {
	return snapshot.Snapshot{
		ID: id, Type: typ, Digest: snapshot.Digest("sha256:" + strings.Repeat("0", 64)),
		Representation: "application/vnd.test", ContentState: snapshot.ContentStateAvailable,
		CreatedAt: time.Unix(1, 0),
	}
}

func mustCanonical(t *testing.T, config atc.Config) []byte {
	t.Helper()
	canonical, err := config.CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	return canonical
}

type resolverStub struct {
	live func(context.Context, string) (workflow.Definition, bool, error)
	get  func(context.Context, string, int) (workflow.Definition, bool, error)
}

func (s *resolverStub) Live(ctx context.Context, name string) (workflow.Definition, bool, error) {
	return s.live(ctx, name)
}
func (s *resolverStub) Get(ctx context.Context, name string, version int) (workflow.Definition, bool, error) {
	return s.get(ctx, name, version)
}

type rendererStub struct {
	full    func(workflow.Definition) (workflow.FunctionTarget, error)
	extract func(workflow.Definition, string) (workflow.FunctionTarget, error)
	render  func(workflow.FunctionTarget) (workflow.RenderedFunction, error)
}

func (s *rendererStub) FullFunctionTarget(def workflow.Definition) (workflow.FunctionTarget, error) {
	return s.full(def)
}
func (s *rendererStub) ExtractFunctionTarget(def workflow.Definition, id string) (workflow.FunctionTarget, error) {
	return s.extract(def, id)
}
func (s *rendererStub) RenderFunction(target workflow.FunctionTarget) (workflow.RenderedFunction, error) {
	return s.render(target)
}

type authorizerStub struct {
	get func(context.Context, int, snapshot.SnapshotID) (snapshot.Snapshot, bool, error)
}

func (s *authorizerStub) GetAuthorized(ctx context.Context, teamID int, id snapshot.SnapshotID) (snapshot.Snapshot, bool, error) {
	return s.get(ctx, teamID, id)
}

type storeStub struct {
	find       func(context.Context, int, string) (db.AgentWorkflowRun, bool, error)
	create     func(context.Context, db.AgentWorkflowRunCreateRequest) (db.AgentWorkflowRun, bool, error)
	snapshots  func(context.Context, snapshot.WorkflowRunID) ([]db.AgentWorkflowRunSnapshotBinding, error)
	transition func(context.Context, snapshot.WorkflowRunID, db.AgentWorkflowRunStatus, db.AgentWorkflowRunStatus, string) (bool, error)
}

func (s *storeStub) FindByIdempotencyKey(ctx context.Context, teamID int, key string) (db.AgentWorkflowRun, bool, error) {
	return s.find(ctx, teamID, key)
}
func (s *storeStub) CreateWithInputs(ctx context.Context, request db.AgentWorkflowRunCreateRequest) (db.AgentWorkflowRun, bool, error) {
	return s.create(ctx, request)
}
func (s *storeStub) Snapshots(ctx context.Context, id snapshot.WorkflowRunID) ([]db.AgentWorkflowRunSnapshotBinding, error) {
	return s.snapshots(ctx, id)
}
func (s *storeStub) Transition(ctx context.Context, id snapshot.WorkflowRunID, from, to db.AgentWorkflowRunStatus, message string) (bool, error) {
	return s.transition(ctx, id, from, to, message)
}

type budgetStub struct {
	admit func(context.Context, BudgetAdmission) error
}

func (s *budgetStub) Admit(ctx context.Context, admission BudgetAdmission) error {
	return s.admit(ctx, admission)
}

type saverStub struct {
	save func(context.Context, AdmissionContext, ImmutableTemplateSpec) (WorkflowRunTemplateRef, error)
}

func (s *saverStub) SaveOrReuse(ctx context.Context, admission AdmissionContext, spec ImmutableTemplateSpec) (WorkflowRunTemplateRef, error) {
	return s.save(ctx, admission, spec)
}

type creatorStub struct {
	create func(context.Context, snapshot.WorkflowRunID, WorkflowRunTemplateRef, map[string]any, string, BeforeWorkflowRunCommit) (WorkflowRunExecution, bool, error)
}

func (s *creatorStub) CreateRunForWorkflowRun(ctx context.Context, runID snapshot.WorkflowRunID, template WorkflowRunTemplateRef, params map[string]any, createdBy string, callback BeforeWorkflowRunCommit) (WorkflowRunExecution, bool, error) {
	return s.create(ctx, runID, template, params, createdBy, callback)
}

type secretStub struct {
	prepare func(context.Context, AdmissionContext, db.AgentWorkflowRun) (PreparedRunSecret, error)
}

func (s *secretStub) Prepare(ctx context.Context, admission AdmissionContext, run db.AgentWorkflowRun) (PreparedRunSecret, error) {
	return s.prepare(ctx, admission, run)
}

type preparedSecretStub struct {
	attach func(context.Context, int) error
}

func (s *preparedSecretStub) Attach(ctx context.Context, pipelineRunID int) error {
	return s.attach(ctx, pipelineRunID)
}
