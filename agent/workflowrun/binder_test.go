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
	pipelineRunID, templateID, instanceID := 313, 211, 419
	plannedBuildID := int64(521)
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
		if admission.WorkflowRunID != largeRunID || admission.Admission.TeamID != 7 ||
			!reflect.DeepEqual(admission.Config, rendered.Config) {
			t.Fatalf("budget admission = %+v", admission)
		}
		return nil
	}}
	credential := &credentialStub{admit: func(context.Context) error {
		order = append(order, "credential")
		return nil
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
		if beforeCommit != nil {
			t.Fatal("agent pods read the platform secret themselves; the binder must not need a pre-commit hook")
		}
		return WorkflowRunExecution{}, true, nil
	}}

	binder, err := NewBinder(resolver, renderer, authorizer, store, budget, saver, creator, credential)
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
	wantOrder := []string{"find", "resolve", "target", "render", "authorize", "allocate", "budget", "credential", "save", "execution", "transition"}
	if !reflect.DeepEqual(order, wantOrder) {
		t.Fatalf("order = %#v, want %#v", order, wantOrder)
	}
}

func TestBindAndCreateTreatsNonV3DefinitionAsPlatformFailure(t *testing.T) {
	definition := binderTestDefinition()
	definition.SchemaVersion = 2
	binder, err := NewBinder(
		&resolverStub{live: func(context.Context, string) (workflow.Definition, bool, error) {
			return definition, true, nil
		}},
		&rendererStub{},
		&authorizerStub{},
		&storeStub{find: func(context.Context, int, string) (db.AgentWorkflowRun, bool, error) {
			return db.AgentWorkflowRun{}, false, nil
		}},
		&budgetStub{},
		&saverStub{},
		&creatorStub{},
		&credentialStub{},
	)
	if err != nil {
		t.Fatal(err)
	}

	_, err = binder.BindAndCreate(context.Background(), AdmissionContext{
		TeamID: 7, TeamName: "research", CreatedBy: "alice", Origin: Origin{Kind: "manual"},
	}, BindRequest{
		WorkflowName: definition.Name, IdempotencyKey: "non-v3-definition",
	})
	if !errors.Is(err, ErrPlatformFailure) {
		t.Fatalf("error = %v, want ErrPlatformFailure", err)
	}
}

func TestBindAndCreateRequiresExperimentGateEvenForExistingIdempotencyKey(t *testing.T) {
	definition := binderTestDefinition()
	rendered := binderTestRendered(t, definition)
	input := binderTestSnapshot(91, "repository/v1")
	findCalls := 0
	createCalls := 0
	store := &storeStub{
		find: func(context.Context, int, string) (db.AgentWorkflowRun, bool, error) {
			findCalls++
			return db.AgentWorkflowRun{ID: 99}, true, nil
		},
		create: func(_ context.Context, request db.AgentWorkflowRunCreateRequest) (db.AgentWorkflowRun, bool, error) {
			createCalls++
			if request.ExperimentAdmission == nil ||
				request.ExperimentAdmission.ExperimentID != 11 ||
				request.ExperimentAdmission.CellID != 13 ||
				request.ExperimentAdmission.Phase != "candidate" {
				t.Fatalf("experiment admission = %+v", request.ExperimentAdmission)
			}
			return db.AgentWorkflowRun{}, false, db.ErrAgentWorkflowRunExperimentAdmissionClosed
		},
	}
	binder, err := NewBinder(
		&resolverStub{live: func(context.Context, string) (workflow.Definition, bool, error) {
			return definition, true, nil
		}},
		&rendererStub{
			full: func(value workflow.Definition) (workflow.FunctionTarget, error) {
				return workflow.FunctionTarget{
					Kind: workflow.TargetWorkflow, WorkflowDefinitionID: value.ID,
					WorkflowName: value.Name, WorkflowVersion: value.Version,
					SignatureVersion: value.SignatureVersion,
					Signature:        rendered.TargetSignature,
				}, nil
			},
			render: func(workflow.FunctionTarget) (workflow.RenderedFunction, error) {
				return rendered, nil
			},
		},
		&authorizerStub{get: func(context.Context, int, snapshot.SnapshotID) (snapshot.Snapshot, bool, error) {
			return input, true, nil
		}},
		store,
		&budgetStub{},
		&saverStub{save: func(context.Context, AdmissionContext, ImmutableTemplateSpec) (WorkflowRunTemplateRef, error) {
			t.Fatal("closed admission must not save a template")
			return WorkflowRunTemplateRef{}, nil
		}},
		&creatorStub{create: func(context.Context, snapshot.WorkflowRunID, WorkflowRunTemplateRef, map[string]any, string, BeforeWorkflowRunCommit) (WorkflowRunExecution, bool, error) {
			t.Fatal("closed admission must not create an execution")
			return WorkflowRunExecution{}, false, nil
		}},
		&credentialStub{admit: func(context.Context) error {
			t.Fatal("closed admission must not check the model credential")
			return nil
		}},
	)
	if err != nil {
		t.Fatal(err)
	}
	_, err = binder.BindAndCreate(context.Background(), AdmissionContext{
		TeamID: 7, TeamName: "research", CreatedBy: "alice",
		Origin: Origin{Kind: "experiment", Reference: "experiment:11:cell:13"},
	}, BindRequest{
		WorkflowName:   definition.Name,
		Inputs:         map[string]snapshot.SnapshotID{"repo": input.ID},
		IdempotencyKey: "experiment:11:cell:13:candidate",
		ExperimentAdmission: &ExperimentAdmissionGate{
			ExperimentID: 11, CellID: 13, Phase: "candidate",
		},
	})
	if !errors.Is(err, ErrExperimentAdmissionClosed) {
		t.Fatalf("error = %v, want closed experiment admission", err)
	}
	if findCalls != 0 || createCalls != 1 {
		t.Fatalf("find calls = %d, create calls = %d", findCalls, createCalls)
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
		&budgetStub{}, &saverStub{}, &creatorStub{}, &credentialStub{},
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
	pipelineRunID, templateID, instanceID := 1, 2, 3
	plannedBuildID := int64(4)
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
		&credentialStub{admit: func(context.Context) error { return unexpected }},
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
	pipelineRunID, templateID, instanceID := 1, 2, 3
	plannedBuildID := int64(4)
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
	binder, err := NewBinder(
		resolver,
		&rendererStub{},
		&authorizerStub{get: func(context.Context, int, snapshot.SnapshotID) (snapshot.Snapshot, bool, error) {
			t.Fatal("must not authorize a durable retry")
			return snapshot.Snapshot{}, false, nil
		}},
		store,
		&budgetStub{admit: func(_ context.Context, admission BudgetAdmission) error {
			if admission.WorkflowRunID != run.ID || !reflect.DeepEqual(admission.Config, rendered.Config) {
				t.Fatalf("replayed budget admission = %+v", admission)
			}
			return nil
		}},
		&saverStub{save: func(context.Context, AdmissionContext, ImmutableTemplateSpec) (WorkflowRunTemplateRef, error) {
			return WorkflowRunTemplateRef{PipelineID: 2, TeamID: 7, Name: rendered.TemplateName, ConfigVersion: 1, FullHash: rendered.TargetConfigHash}, nil
		}},
		&creatorStub{create: func(ctx context.Context, _ snapshot.WorkflowRunID, _ WorkflowRunTemplateRef, params map[string]any, _ string, callback BeforeWorkflowRunCommit) (WorkflowRunExecution, bool, error) {
			if params["snapshot_notes"] != "0" || params["snapshot_repo"] != input.ID.String() {
				t.Fatalf("durable params = %#v", params)
			}
			if callback != nil {
				return WorkflowRunExecution{}, true, callback(ctx, 1)
			}
			return WorkflowRunExecution{}, true, nil
		}},
		&credentialStub{admit: func(context.Context) error { return nil }},
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
	pipelineRunID, templateID, instanceID := 1, 2, 3
	plannedBuildID := int64(4)
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
		&credentialStub{admit: func(context.Context) error { return unwanted }},
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

func TestBindAndCreateLeavesPostAllocationPlatformFailureRetryableAndRedacted(t *testing.T) {
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
	store := &storeStub{
		find: func(context.Context, int, string) (db.AgentWorkflowRun, bool, error) { return run, true, nil },
		snapshots: func(context.Context, snapshot.WorkflowRunID) ([]db.AgentWorkflowRunSnapshotBinding, error) {
			return []db.AgentWorkflowRunSnapshotBinding{{
				WorkflowRunID: run.ID, Direction: db.AgentWorkflowRunSnapshotInput, PortName: "repo",
				Snapshot: snapshot.SnapshotRef{ID: input.ID, Type: input.Type, Digest: input.Digest},
			}}, nil
		},
		transition: func(_ context.Context, id snapshot.WorkflowRunID, from, to db.AgentWorkflowRunStatus, message string) (bool, error) {
			t.Fatalf("transient platform failure must remain admitting, got transition = (%s, %s, %s, %q)", id.String(), from, to, message)
			return false, nil
		},
	}
	binder, err := NewBinder(
		&resolverStub{}, &rendererStub{}, &authorizerStub{}, store,
		&budgetStub{}, &saverStub{}, &creatorStub{},
		&credentialStub{admit: func(context.Context) error {
			return errors.New("credential=super-secret " + strings.Repeat("界", 5000))
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
	if strings.Contains(err.Error(), "super-secret") || strings.Contains(err.Error(), "credential=") {
		t.Fatalf("platform error leaked secret detail: %q", err)
	}
}

func TestBindAndCreateRetriesSameAdmittingIdentityAfterTransientPlatformFailure(t *testing.T) {
	definition := binderTestDefinition()
	rendered := binderTestRendered(t, definition)
	input := binderTestSnapshot(91, "repository/v1")
	run := db.AgentWorkflowRun{
		ID: 41, TeamID: 7, TeamName: "research", WorkflowDefinitionID: definition.ID,
		WorkflowName: definition.Name, WorkflowVersion: definition.Version,
		SchemaVersion: 3, SignatureVersion: 1, DefinitionContentHash: definition.ContentHash,
		IdempotencyKey: "retry-platform", ParameterizedConfig: mustCanonical(t, rendered.Config),
		ParameterizedConfigHash: rendered.TargetConfigHash, OriginKind: "manual", CreatedBy: "alice",
		Status: db.AgentWorkflowRunStatusAdmitting,
	}
	store := &storeStub{
		find: func(context.Context, int, string) (db.AgentWorkflowRun, bool, error) { return run, true, nil },
		snapshots: func(context.Context, snapshot.WorkflowRunID) ([]db.AgentWorkflowRunSnapshotBinding, error) {
			return []db.AgentWorkflowRunSnapshotBinding{{
				WorkflowRunID: run.ID, Direction: db.AgentWorkflowRunSnapshotInput, PortName: "repo",
				Snapshot: snapshot.SnapshotRef{ID: input.ID, Type: input.Type, Digest: input.Digest},
			}}, nil
		},
		transition: func(_ context.Context, id snapshot.WorkflowRunID, from, to db.AgentWorkflowRunStatus, message string) (bool, error) {
			if id != run.ID || from != db.AgentWorkflowRunStatusAdmitting || to != db.AgentWorkflowRunStatusRunning || message != "" {
				t.Fatalf("transition = (%s, %s, %s, %q)", id.String(), from, to, message)
			}
			run.Status = db.AgentWorkflowRunStatusRunning
			return true, nil
		},
	}
	credentialCalls := 0
	binder, err := NewBinder(
		&resolverStub{}, &rendererStub{}, &authorizerStub{}, store, &budgetStub{},
		&saverStub{save: func(context.Context, AdmissionContext, ImmutableTemplateSpec) (WorkflowRunTemplateRef, error) {
			return WorkflowRunTemplateRef{PipelineID: 2, TeamID: 7, Name: rendered.TemplateName, ConfigVersion: 1, FullHash: rendered.TargetConfigHash}, nil
		}},
		&creatorStub{create: func(ctx context.Context, _ snapshot.WorkflowRunID, _ WorkflowRunTemplateRef, _ map[string]any, _ string, before BeforeWorkflowRunCommit) (WorkflowRunExecution, bool, error) {
			if before != nil {
				if err := before(ctx, 73); err != nil {
					return WorkflowRunExecution{}, false, err
				}
			}
			pipelineRunID, templateID, instanceID, plannedBuildID := 73, 2, 3, int64(5)
			instanceHash := strings.Repeat("c", 64)
			run.PipelineRunID, run.TemplatePipelineID, run.InstancePipelineID = &pipelineRunID, &templateID, &instanceID
			run.PlannedBuildID, run.ConcreteConfigHash = &plannedBuildID, &instanceHash
			run.ConcreteConfig = mustCanonical(t, rendered.Config)
			return WorkflowRunExecution{}, true, nil
		}},
		&credentialStub{admit: func(context.Context) error {
			credentialCalls++
			if credentialCalls == 1 {
				return errors.New("temporary vault outage")
			}
			return nil
		}},
	)
	if err != nil {
		t.Fatal(err)
	}
	request := BindRequest{
		WorkflowName: definition.Name, Inputs: map[string]snapshot.SnapshotID{"repo": input.ID},
		IdempotencyKey: "retry-platform",
	}
	admission := AdmissionContext{TeamID: 7, TeamName: "research", CreatedBy: "alice", Origin: Origin{Kind: "manual"}}
	if _, err := binder.BindAndCreate(context.Background(), admission, request); !errors.Is(err, ErrPlatformFailure) {
		t.Fatalf("first BindAndCreate error = %v, want platform failure", err)
	}
	if run.Status != db.AgentWorkflowRunStatusAdmitting {
		t.Fatalf("status after transient failure = %s, want admitting", run.Status)
	}
	result, err := binder.BindAndCreate(context.Background(), admission, request)
	if err != nil {
		t.Fatalf("retry BindAndCreate: %v", err)
	}
	if result.Created || result.Run.ID != run.ID || result.Run.Status != db.AgentWorkflowRunStatusRunning || credentialCalls != 2 {
		t.Fatalf("retry result=%+v credential calls=%d", result, credentialCalls)
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
				&credentialStub{admit: func(context.Context) error { return nil }},
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
			wantPersisted := ""
			if errors.Is(test.want, ErrImmutableTemplateCollision) {
				wantPersisted = test.want.Error()
			}
			if persisted != wantPersisted {
				t.Fatalf("persisted = %q, want %q", persisted, wantPersisted)
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
		&budgetStub{}, &saverStub{}, &creatorStub{}, &credentialStub{},
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

func TestWorkflowTargetRendererPinsTrustedAgentRuntimeIntoImmutableConfig(t *testing.T) {
	compiled, err := workflow.ParseCompiled([]byte(`schema_version: 3
name: runtime-test
signature_version: 1
inputs:
  - name: repo
    type: repository/v1
outputs:
  - name: report
    type: opaque/v1
    from: report
plan:
  - agent: inspect
    function_id: inspect
    prompt: Inspect the repository.
    inputs: [repo]
    outputs: [report]
    input_types:
      repo: {type: repository/v1}
    output_types:
      report: opaque/v1
`))
	if err != nil {
		t.Fatal(err)
	}
	definition := workflow.Definition{
		ID: 101, Name: "runtime-test", Version: 3, SchemaVersion: 3, SignatureVersion: 1,
		ContentHash: strings.Repeat("d", 64), Compiled: *compiled,
	}
	target, err := workflow.FullFunctionTarget(definition)
	if err != nil {
		t.Fatal(err)
	}
	firstImage := "registry.example/agent-runner@sha256:" + strings.Repeat("a", 64)
	first, err := (WorkflowTargetRenderer{RuntimeImage: firstImage}).RenderFunction(target)
	if err != nil {
		t.Fatal(err)
	}
	agent, ok := first.Config.Jobs[0].PlanSequence[len(first.Config.Jobs[0].PlanSequence)-1].Config.(*atc.AgentStep)
	if !ok || agent.RuntimeImage != firstImage {
		t.Fatalf("rendered agent runtime = %#v", agent)
	}
	second, err := (WorkflowTargetRenderer{
		RuntimeImage: "registry.example/agent-runner@sha256:" + strings.Repeat("b", 64),
	}).RenderFunction(target)
	if err != nil {
		t.Fatal(err)
	}
	if first.TargetConfigHash == second.TargetConfigHash || first.TemplateName == second.TemplateName {
		t.Fatal("runtime image identity did not affect immutable target identity")
	}
	if _, err := (WorkflowTargetRenderer{RuntimeImage: "registry.example/agent-runner:v1"}).RenderFunction(target); err == nil {
		t.Fatal("mutable runtime image was admitted")
	}
}

func TestBindAndCreateRejectsFrozenTargetConfigDriftBeforeAllocating(t *testing.T) {
	definition := binderTestDefinition()
	rendered := binderTestRendered(t, definition)
	target := workflow.FunctionTarget{
		Kind: workflow.TargetWorkflow, WorkflowDefinitionID: definition.ID,
		WorkflowName: definition.Name, WorkflowVersion: definition.Version,
		SignatureVersion: definition.SignatureVersion, Signature: rendered.TargetSignature,
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
			t.Fatal("target drift must fail before snapshot authorization")
			return snapshot.Snapshot{}, false, nil
		}},
		&storeStub{find: func(context.Context, int, string) (db.AgentWorkflowRun, bool, error) {
			return db.AgentWorkflowRun{}, false, nil
		}},
		&budgetStub{}, &saverStub{}, &creatorStub{}, &credentialStub{},
	)
	if err != nil {
		t.Fatal(err)
	}
	_, err = binder.BindAndCreate(context.Background(), AdmissionContext{
		TeamID: 7, TeamName: "research", CreatedBy: "alice", Origin: Origin{Kind: "experiment", Reference: "experiment:1:cell:1"},
	}, BindRequest{
		WorkflowName: definition.Name, Inputs: map[string]snapshot.SnapshotID{"repo": 91},
		IdempotencyKey: "frozen-target-drift", ExpectedTargetConfigHash: strings.Repeat("f", 64),
	})
	if !errors.Is(err, ErrInvalidRequest) || !strings.Contains(err.Error(), "frozen target config") {
		t.Fatalf("error = %v, want frozen target drift", err)
	}
}

func TestBindAndCreateRejectsFrozenDefinitionDriftBeforeRendering(t *testing.T) {
	definition := binderTestDefinition()
	rendered := false
	binder, err := NewBinder(
		&resolverStub{get: func(context.Context, string, int) (workflow.Definition, bool, error) {
			return definition, true, nil
		}},
		&rendererStub{full: func(workflow.Definition) (workflow.FunctionTarget, error) {
			rendered = true
			return workflow.FunctionTarget{}, nil
		}},
		&authorizerStub{},
		&storeStub{find: func(context.Context, int, string) (db.AgentWorkflowRun, bool, error) {
			return db.AgentWorkflowRun{}, false, nil
		}},
		&budgetStub{}, &saverStub{}, &creatorStub{}, &credentialStub{},
	)
	if err != nil {
		t.Fatal(err)
	}
	version := definition.Version
	_, err = binder.BindAndCreate(context.Background(), AdmissionContext{
		TeamID: 7, TeamName: "research", CreatedBy: "alice", Origin: Origin{Kind: "experiment", Reference: "experiment:1:cell:1"},
	}, BindRequest{
		WorkflowName: definition.Name, Version: &version, Inputs: map[string]snapshot.SnapshotID{"repo": 91},
		IdempotencyKey: "frozen-definition-drift", ExpectedWorkflowDefinitionID: int64(definition.ID + 1),
	})
	if !errors.Is(err, ErrInvalidRequest) || !strings.Contains(err.Error(), "frozen workflow definition") {
		t.Fatalf("error = %v, want frozen definition drift", err)
	}
	if rendered {
		t.Fatal("renderer was called after frozen definition drift")
	}
}

func TestBindAndCreateRejectsExistingRunOutsideFrozenTargetConfig(t *testing.T) {
	rendered := binderTestRendered(t, binderTestDefinition())
	run := db.AgentWorkflowRun{
		ID: 41, TeamID: 7, WorkflowName: "review-flow", WorkflowVersion: 3,
		IdempotencyKey: "existing-drift", ParameterizedConfigHash: rendered.TargetConfigHash,
		OriginKind: "experiment", OriginReference: "experiment:1:cell:1", CreatedBy: "alice",
		Status: db.AgentWorkflowRunStatusRunning,
	}
	binder, err := NewBinder(
		&resolverStub{}, &rendererStub{}, &authorizerStub{},
		&storeStub{find: func(context.Context, int, string) (db.AgentWorkflowRun, bool, error) {
			return run, true, nil
		}},
		&budgetStub{}, &saverStub{}, &creatorStub{}, &credentialStub{},
	)
	if err != nil {
		t.Fatal(err)
	}
	_, err = binder.BindAndCreate(context.Background(), AdmissionContext{
		TeamID: 7, TeamName: "research", CreatedBy: "alice", Origin: Origin{Kind: "experiment", Reference: "experiment:1:cell:1"},
	}, BindRequest{
		WorkflowName: "review-flow", Inputs: map[string]snapshot.SnapshotID{}, IdempotencyKey: "existing-drift",
		ExpectedTargetConfigHash: strings.Repeat("f", 64),
	})
	if !errors.Is(err, ErrInvalidRequest) || !strings.Contains(err.Error(), "frozen target config") {
		t.Fatalf("error = %v, want frozen target drift", err)
	}
}

func TestBindAndCreateRejectsExistingRunOutsideFrozenDefinition(t *testing.T) {
	run := db.AgentWorkflowRun{
		ID: 42, TeamID: 7, WorkflowDefinitionID: 41, WorkflowName: "review-flow", WorkflowVersion: 3,
		IdempotencyKey: "existing-definition-drift", ParameterizedConfigHash: strings.Repeat("a", 64),
		OriginKind: "experiment", OriginReference: "experiment:1:cell:1", CreatedBy: "alice",
		Status: db.AgentWorkflowRunStatusRunning,
	}
	binder, err := NewBinder(
		&resolverStub{}, &rendererStub{}, &authorizerStub{},
		&storeStub{find: func(context.Context, int, string) (db.AgentWorkflowRun, bool, error) {
			return run, true, nil
		}},
		&budgetStub{}, &saverStub{}, &creatorStub{}, &credentialStub{},
	)
	if err != nil {
		t.Fatal(err)
	}
	_, err = binder.BindAndCreate(context.Background(), AdmissionContext{
		TeamID: 7, TeamName: "research", CreatedBy: "alice", Origin: Origin{Kind: "experiment", Reference: "experiment:1:cell:1"},
	}, BindRequest{
		WorkflowName: "review-flow", Inputs: map[string]snapshot.SnapshotID{}, IdempotencyKey: "existing-definition-drift",
		ExpectedWorkflowDefinitionID: 42,
	})
	if !errors.Is(err, ErrInvalidRequest) || !strings.Contains(err.Error(), "frozen workflow definition") {
		t.Fatalf("error = %v, want frozen definition drift", err)
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
	pipelineRunID, templateID, instanceID := 1, 2, 3
	plannedBuildID := int64(4)
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
		&saverStub{}, &creatorStub{}, &credentialStub{},
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
		{name: "negative expected definition", mutate: func(_ *AdmissionContext, r *BindRequest) { r.ExpectedWorkflowDefinitionID = -1 }},
		{name: "malformed target config hash", mutate: func(_ *AdmissionContext, r *BindRequest) { r.ExpectedTargetConfigHash = strings.Repeat("A", 64) }},
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

func TestAwaitSnapshotDefaultsAreAuthorizedAtAdmission(t *testing.T) {
	config := atc.Config{Jobs: atc.JobConfigs{{Name: "run", PlanSequence: []atc.Step{{Config: &atc.TimeoutStep{
		Duration: "1h",
		Step: &atc.AwaitSnapshotStep{
			Name: "answer", Question: "question", Type: "human-answer/v1",
			OnTimeout: atc.AwaitSnapshotOnTimeoutDefault, DefaultSnapshotID: "9007199254740993",
		},
	}}}}}}

	for _, test := range []struct {
		name  string
		value snapshot.Snapshot
		found bool
		err   error
		want  error
	}{
		{name: "authorized", value: binderTestSnapshot(9007199254740993, "human-answer/v1"), found: true},
		{name: "missing", found: false, want: ErrSnapshotUnavailable},
		{name: "wrong type", value: binderTestSnapshot(9007199254740993, "review/v1"), found: true, want: ErrSnapshotTypeMismatch},
		{name: "dependency error", err: errors.New("private storage detail"), want: ErrPlatformFailure},
	} {
		t.Run(test.name, func(t *testing.T) {
			calls := 0
			binder := &Binder{snapshots: &authorizerStub{get: func(_ context.Context, teamID int, id snapshot.SnapshotID) (snapshot.Snapshot, bool, error) {
				calls++
				if teamID != 17 || id.String() != "9007199254740993" {
					t.Fatalf("authorization identity = (%d, %s)", teamID, id.String())
				}
				return test.value, test.found, test.err
			}}}
			err := binder.authorizeAwaitDefaults(context.Background(), 17, config)
			if test.want == nil && err != nil || test.want != nil && !errors.Is(err, test.want) {
				t.Fatalf("error = %v, want %v", err, test.want)
			}
			if calls != 1 {
				t.Fatalf("authorization calls = %d", calls)
			}
			if strings.Contains(fmt.Sprint(err), "private storage detail") {
				t.Fatalf("dependency detail leaked: %v", err)
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
	if s.admit == nil {
		return nil
	}
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

type credentialStub struct {
	admit func(context.Context) error
}

func (s *credentialStub) AdmitModelCredential(ctx context.Context) error {
	return s.admit(ctx)
}
