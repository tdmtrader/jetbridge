package workflowrun

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/agent/workflow"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
)

func TestBindAndCreateManuallyAdmitsAndBindsSealedResourceSources(t *testing.T) {
	definition := binderResourceSourceDefinition(t)
	target, err := workflow.FullFunctionTarget(definition)
	if err != nil {
		t.Fatal(err)
	}
	readyRefs := map[string]snapshot.SnapshotRef{
		"source-repo": {ID: 301, Type: "repository/v1", Digest: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
	}
	rendered, err := workflow.RenderFunctionWithBoundSources(target, readyRefs)
	if err != nil {
		t.Fatal(err)
	}
	sourceTarget, err := workflow.ResourceSourcePipelineTargetFor(definition, 7)
	if err != nil {
		t.Fatal(err)
	}
	sourceConfig, err := workflow.RenderResourceSourcePipeline(sourceTarget)
	if err != nil {
		t.Fatal(err)
	}
	ready := ReadySourceAdmission{
		AdmissionID: 71, TeamID: 7, WorkflowDefinitionID: definition.ID,
		WorkflowName: definition.Name, WorkflowVersion: definition.Version,
		SourceConfigHash: sourceConfig.ConfigHash, Inputs: readyRefs,
	}
	public := binderTestSnapshot(300, "repository/v1")
	runID := snapshot.WorkflowRunID(401)
	running := db.AgentWorkflowRun{
		ID: runID, TeamID: 7, TeamName: "research", WorkflowDefinitionID: definition.ID,
		WorkflowName: definition.Name, WorkflowVersion: definition.Version,
		SchemaVersion: definition.SchemaVersion, SignatureVersion: definition.SignatureVersion,
		DefinitionContentHash: definition.ContentHash, IdempotencyKey: "manual-source",
		ParameterizedConfig: mustCanonical(t, rendered.Config), ParameterizedConfigHash: rendered.TargetConfigHash,
		ResourceSourceAdmissionID: &ready.AdmissionID, OriginKind: "manual", CreatedBy: "alice",
		Status: db.AgentWorkflowRunStatusRunning,
	}
	pipelineRunID, templateID, instanceID, plannedBuildID := 411, 412, 413, int64(414)
	instanceHash := "instance-config-hash"
	running.PipelineRunID, running.TemplatePipelineID, running.InstancePipelineID = &pipelineRunID, &templateID, &instanceID
	running.PlannedBuildID, running.ConcreteConfigHash = &plannedBuildID, &instanceHash
	running.ConcreteConfig = mustCanonical(t, rendered.Config)

	manualCalls, boundRenders := 0, 0
	sources := &binderResourceSourceAdmitterStub{admit: func(_ context.Context, admission AdmissionContext, got workflow.ResourceSourcePipelineTarget, key string) (ReadySourceAdmission, error) {
		manualCalls++
		if admission.TeamID != 7 || !reflect.DeepEqual(got, sourceTarget) || key != "workflow-run-source:manual-source" {
			t.Fatalf("manual source admission = (%+v, %+v, %q)", admission, got, key)
		}
		return ready, nil
	}}
	store := &storeStub{
		find: func(context.Context, int, string) (db.AgentWorkflowRun, bool, error) {
			return db.AgentWorkflowRun{}, false, nil
		},
		create: func(_ context.Context, request db.AgentWorkflowRunCreateRequest) (db.AgentWorkflowRun, bool, error) {
			if request.ResourceSourceAdmissionID == nil || *request.ResourceSourceAdmissionID != ready.AdmissionID ||
				!reflect.DeepEqual(request.Inputs, map[string]snapshot.SnapshotRef{
					"repo": {ID: public.ID, Type: public.Type, Digest: public.Digest}, "source-repo": readyRefs["source-repo"],
				}) {
				t.Fatalf("create request = %#v", request)
			}
			return running, false, nil
		},
		snapshots: func(context.Context, snapshot.WorkflowRunID) ([]db.AgentWorkflowRunSnapshotBinding, error) {
			return []db.AgentWorkflowRunSnapshotBinding{
				{WorkflowRunID: runID, Direction: db.AgentWorkflowRunSnapshotInput, PortName: "repo", Snapshot: snapshot.SnapshotRef{ID: public.ID, Type: public.Type, Digest: public.Digest}},
				{WorkflowRunID: runID, Direction: db.AgentWorkflowRunSnapshotInput, PortName: "source-repo", Snapshot: readyRefs["source-repo"]},
			}, nil
		},
		transition: func(context.Context, snapshot.WorkflowRunID, db.AgentWorkflowRunStatus, db.AgentWorkflowRunStatus, string) (bool, error) {
			return false, errors.New("unexpected transition")
		},
	}
	binder, err := NewBinder(
		&resolverStub{live: func(context.Context, string) (workflow.Definition, bool, error) { return definition, true, nil }},
		&rendererStub{
			full: func(workflow.Definition) (workflow.FunctionTarget, error) { return target, nil },
			render: func(workflow.FunctionTarget) (workflow.RenderedFunction, error) {
				t.Fatal("source workflow must not use unbound rendering")
				return workflow.RenderedFunction{}, nil
			},
			bound: func(got workflow.FunctionTarget, refs map[string]snapshot.SnapshotRef) (workflow.RenderedFunction, error) {
				boundRenders++
				if !reflect.DeepEqual(got, target) || !reflect.DeepEqual(refs, readyRefs) {
					t.Fatalf("bound render = (%#v, %#v)", got, refs)
				}
				return rendered, nil
			},
		},
		&authorizerStub{get: func(_ context.Context, team int, id snapshot.SnapshotID) (snapshot.Snapshot, bool, error) {
			if team != 7 || id != public.ID {
				t.Fatalf("public authorization = (%d, %d)", team, id)
			}
			return public, true, nil
		}},
		store, &budgetStub{}, &saverStub{}, &creatorStub{}, &credentialStub{},
		WithResourceSourceAdmitter(sources),
	)
	if err != nil {
		t.Fatal(err)
	}
	result, err := binder.BindAndCreate(context.Background(), AdmissionContext{
		TeamID: 7, TeamName: "research", CreatedBy: "alice", Origin: Origin{Kind: "manual"},
	}, BindRequest{WorkflowName: definition.Name, Inputs: map[string]snapshot.SnapshotID{"repo": public.ID}, IdempotencyKey: "manual-source"})
	if err != nil {
		t.Fatalf("BindAndCreate: %v", err)
	}
	if result.Run.ID != runID || result.Created || manualCalls != 1 || boundRenders != 1 {
		t.Fatalf("result = %#v, manual=%d renders=%d", result, manualCalls, boundRenders)
	}
}

func TestBindReadySourceAdmissionLaunchesOnlyVerifiedSealedSources(t *testing.T) {
	definition := binderResourceSourceOnlyDefinition(t)
	target, err := workflow.FullFunctionTarget(definition)
	if err != nil {
		t.Fatal(err)
	}
	refs := map[string]snapshot.SnapshotRef{
		"source-repo": {ID: 901, Type: "repository/v1", Digest: "sha256:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"},
	}
	rendered, err := workflow.RenderFunctionWithBoundSources(target, refs)
	if err != nil {
		t.Fatal(err)
	}
	sourceTarget, err := workflow.ResourceSourcePipelineTargetFor(definition, 7)
	if err != nil {
		t.Fatal(err)
	}
	sourceConfig, err := workflow.RenderResourceSourcePipeline(sourceTarget)
	if err != nil {
		t.Fatal(err)
	}
	ready := ReadySourceAdmission{
		AdmissionID: 101, TeamID: 7, WorkflowDefinitionID: definition.ID,
		WorkflowName: definition.Name, WorkflowVersion: definition.Version,
		SourceConfigHash: sourceConfig.ConfigHash, Inputs: refs,
	}
	runID := snapshot.WorkflowRunID(902)
	running := db.AgentWorkflowRun{
		ID: runID, TeamID: 7, TeamName: "research", WorkflowDefinitionID: definition.ID,
		WorkflowName: definition.Name, WorkflowVersion: definition.Version,
		SchemaVersion: definition.SchemaVersion, SignatureVersion: definition.SignatureVersion,
		DefinitionContentHash: definition.ContentHash, IdempotencyKey: "source-build:13:17",
		ParameterizedConfig: mustCanonical(t, rendered.Config), ParameterizedConfigHash: rendered.TargetConfigHash,
		ResourceSourceAdmissionID: &ready.AdmissionID,
		OriginKind:                "resource-source-build", OriginReference: "pipeline:13:build:17",
		CreatedBy: "workflow-resource-source-reconciler", Status: db.AgentWorkflowRunStatusRunning,
	}
	pipelineRunID, templateID, instanceID, plannedBuildID := 911, 912, 913, int64(914)
	instanceHash := "instance-config-hash"
	running.PipelineRunID, running.TemplatePipelineID, running.InstancePipelineID = &pipelineRunID, &templateID, &instanceID
	running.PlannedBuildID, running.ConcreteConfigHash = &plannedBuildID, &instanceHash
	running.ConcreteConfig = mustCanonical(t, rendered.Config)

	loads, boundRenders := 0, 0
	sources := &binderResourceSourceAdmitterStub{
		admit: func(context.Context, AdmissionContext, workflow.ResourceSourcePipelineTarget, string) (ReadySourceAdmission, error) {
			t.Fatal("automatic source handoff must not schedule manual admission")
			return ReadySourceAdmission{}, nil
		},
		load: func(_ context.Context, teamID int, admissionID int64, got workflow.ResourceSourcePipelineTarget) (ReadySourceAdmission, error) {
			loads++
			if teamID != 7 || admissionID != ready.AdmissionID || !reflect.DeepEqual(got, sourceTarget) {
				t.Fatalf("ready source load = (%d, %d, %#v)", teamID, admissionID, got)
			}
			return ready, nil
		},
	}
	store := &storeStub{
		find: func(context.Context, int, string) (db.AgentWorkflowRun, bool, error) {
			return db.AgentWorkflowRun{}, false, nil
		},
		create: func(_ context.Context, request db.AgentWorkflowRunCreateRequest) (db.AgentWorkflowRun, bool, error) {
			if request.ResourceSourceAdmissionID == nil || *request.ResourceSourceAdmissionID != ready.AdmissionID ||
				!reflect.DeepEqual(request.Inputs, refs) {
				t.Fatalf("automatic create request = %#v", request)
			}
			return running, false, nil
		},
		snapshots: func(context.Context, snapshot.WorkflowRunID) ([]db.AgentWorkflowRunSnapshotBinding, error) {
			return []db.AgentWorkflowRunSnapshotBinding{{
				WorkflowRunID: runID, Direction: db.AgentWorkflowRunSnapshotInput,
				PortName: "source-repo", Snapshot: refs["source-repo"],
			}}, nil
		},
		transition: func(context.Context, snapshot.WorkflowRunID, db.AgentWorkflowRunStatus, db.AgentWorkflowRunStatus, string) (bool, error) {
			return false, errors.New("unexpected transition")
		},
	}
	binder, err := NewBinder(
		&resolverStub{get: func(_ context.Context, name string, version int) (workflow.Definition, bool, error) {
			if name != definition.Name || version != definition.Version {
				t.Fatalf("automatic definition lookup = (%q, %d)", name, version)
			}
			return definition, true, nil
		}},
		&rendererStub{
			full: func(workflow.Definition) (workflow.FunctionTarget, error) { return target, nil },
			render: func(workflow.FunctionTarget) (workflow.RenderedFunction, error) {
				t.Fatal("automatic source workflow must not use unbound rendering")
				return workflow.RenderedFunction{}, nil
			},
			bound: func(got workflow.FunctionTarget, gotRefs map[string]snapshot.SnapshotRef) (workflow.RenderedFunction, error) {
				boundRenders++
				if !reflect.DeepEqual(got, target) || !reflect.DeepEqual(gotRefs, refs) {
					t.Fatalf("automatic bound render = (%#v, %#v)", got, gotRefs)
				}
				return rendered, nil
			},
		},
		&authorizerStub{get: func(context.Context, int, snapshot.SnapshotID) (snapshot.Snapshot, bool, error) {
			t.Fatal("automatic source-only run has no public inputs")
			return snapshot.Snapshot{}, false, nil
		}},
		store, &budgetStub{}, &saverStub{}, &creatorStub{}, &credentialStub{},
		WithResourceSourceAdmitter(sources),
	)
	if err != nil {
		t.Fatal(err)
	}
	result, err := binder.BindReadySourceAdmission(context.Background(), AdmissionContext{
		TeamID: 7, TeamName: "research", CreatedBy: "workflow-resource-source-reconciler",
		Origin: Origin{Kind: "resource-source-build", Reference: "pipeline:13:build:17"},
	}, ready, "source-build:13:17")
	if err != nil {
		t.Fatalf("BindReadySourceAdmission: %v", err)
	}
	if result.Run.ID != runID || result.Created || loads != 1 || boundRenders != 1 {
		t.Fatalf("result = %#v, loads=%d renders=%d", result, loads, boundRenders)
	}
}

func TestBindReadySourceAdmissionRejectsExistingWinnerWithDifferentAdmission(t *testing.T) {
	definition := binderResourceSourceOnlyDefinition(t)
	sourceTarget, err := workflow.ResourceSourcePipelineTargetFor(definition, 7)
	if err != nil {
		t.Fatal(err)
	}
	sourceConfig, err := workflow.RenderResourceSourcePipeline(sourceTarget)
	if err != nil {
		t.Fatal(err)
	}
	refs := map[string]snapshot.SnapshotRef{
		"source-repo": {ID: 451, Type: "repository/v1", Digest: "sha256:eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"},
	}
	winningID, conflictingID := int64(111), int64(112)
	winning := ReadySourceAdmission{
		AdmissionID: winningID, TeamID: 7, WorkflowDefinitionID: definition.ID,
		WorkflowName: definition.Name, WorkflowVersion: definition.Version,
		SourceConfigHash: sourceConfig.ConfigHash, Inputs: refs,
	}
	conflicting := winning
	conflicting.AdmissionID = conflictingID
	runID := snapshot.WorkflowRunID(452)
	pipelineRunID, templateID, instanceID, plannedBuildID := 453, 454, 455, int64(456)
	instanceHash := "instance-config-hash"
	winner := db.AgentWorkflowRun{
		ID: runID, TeamID: 7, TeamName: "research", WorkflowDefinitionID: definition.ID,
		WorkflowName: definition.Name, WorkflowVersion: definition.Version,
		SchemaVersion: definition.SchemaVersion, SignatureVersion: definition.SignatureVersion,
		DefinitionContentHash: definition.ContentHash, IdempotencyKey: "source-build:13:17",
		ResourceSourceAdmissionID: &winningID,
		OriginKind:                "resource-source-build", OriginReference: "pipeline:13:build:17",
		CreatedBy: "workflow-resource-source-reconciler", Status: db.AgentWorkflowRunStatusRunning,
	}
	winner.PipelineRunID, winner.TemplatePipelineID, winner.InstancePipelineID = &pipelineRunID, &templateID, &instanceID
	winner.PlannedBuildID, winner.ConcreteConfigHash = &plannedBuildID, &instanceHash
	winner.ConcreteConfig = []byte(`{"template":true}`)
	store := &storeStub{
		find: func(context.Context, int, string) (db.AgentWorkflowRun, bool, error) { return winner, true, nil },
		snapshots: func(context.Context, snapshot.WorkflowRunID) ([]db.AgentWorkflowRunSnapshotBinding, error) {
			return []db.AgentWorkflowRunSnapshotBinding{{
				WorkflowRunID: runID, Direction: db.AgentWorkflowRunSnapshotInput,
				PortName: "source-repo", Snapshot: refs["source-repo"],
			}}, nil
		},
	}
	binder, err := NewBinder(
		&resolverStub{get: func(context.Context, string, int) (workflow.Definition, bool, error) { return definition, true, nil }},
		&rendererStub{},
		&authorizerStub{},
		store, &budgetStub{}, &saverStub{}, &creatorStub{}, &credentialStub{},
		WithResourceSourceAdmitter(&binderResourceSourceAdmitterStub{load: func(_ context.Context, teamID int, id int64, got workflow.ResourceSourcePipelineTarget) (ReadySourceAdmission, error) {
			if teamID != 7 || id != winningID || !reflect.DeepEqual(got, sourceTarget) {
				t.Fatalf("winner ready load = (%d, %d, %#v)", teamID, id, got)
			}
			return winning, nil
		}}),
	)
	if err != nil {
		t.Fatal(err)
	}
	_, err = binder.BindReadySourceAdmission(context.Background(), AdmissionContext{
		TeamID: 7, TeamName: "research", CreatedBy: "workflow-resource-source-reconciler",
		Origin: Origin{Kind: "resource-source-build", Reference: "pipeline:13:build:17"},
	}, conflicting, "source-build:13:17")
	if !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("BindReadySourceAdmission error = %v, want idempotency conflict", err)
	}
}

func TestBindAndCreateResumesExistingSealedSourceAdmissionWithoutReopeningSelection(t *testing.T) {
	definition := binderResourceSourceOnlyDefinition(t)
	target, err := workflow.FullFunctionTarget(definition)
	if err != nil {
		t.Fatal(err)
	}
	refs := map[string]snapshot.SnapshotRef{
		"source-repo": {ID: 501, Type: "repository/v1", Digest: "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"},
	}
	rendered, err := workflow.RenderFunctionWithBoundSources(target, refs)
	if err != nil {
		t.Fatal(err)
	}
	sourceTarget, err := workflow.ResourceSourcePipelineTargetFor(definition, 7)
	if err != nil {
		t.Fatal(err)
	}
	sourceConfig, err := workflow.RenderResourceSourcePipeline(sourceTarget)
	if err != nil {
		t.Fatal(err)
	}
	admissionID := int64(81)
	ready := ReadySourceAdmission{
		AdmissionID: admissionID, TeamID: 7, WorkflowDefinitionID: definition.ID,
		WorkflowName: definition.Name, WorkflowVersion: definition.Version,
		SourceConfigHash: sourceConfig.ConfigHash, Inputs: refs,
	}
	runID := snapshot.WorkflowRunID(601)
	admitting := db.AgentWorkflowRun{
		ID: runID, TeamID: 7, TeamName: "research", WorkflowDefinitionID: definition.ID,
		WorkflowName: definition.Name, WorkflowVersion: definition.Version,
		SchemaVersion: definition.SchemaVersion, SignatureVersion: definition.SignatureVersion,
		DefinitionContentHash: definition.ContentHash, IdempotencyKey: "resume-source",
		ParameterizedConfig: mustCanonical(t, rendered.Config), ParameterizedConfigHash: rendered.TargetConfigHash,
		ResourceSourceAdmissionID: &admissionID, OriginKind: "manual", CreatedBy: "alice",
		Status: db.AgentWorkflowRunStatusAdmitting,
	}
	running := admitting
	running.Status = db.AgentWorkflowRunStatusRunning
	pipelineRunID, templateID, instanceID, plannedBuildID := 611, 612, 613, int64(614)
	instanceHash := "instance-config-hash"
	running.PipelineRunID, running.TemplatePipelineID, running.InstancePipelineID = &pipelineRunID, &templateID, &instanceID
	running.PlannedBuildID, running.ConcreteConfigHash = &plannedBuildID, &instanceHash
	running.ConcreteConfig = mustCanonical(t, rendered.Config)

	loads, renders := 0, 0
	sources := &binderResourceSourceAdmitterStub{
		admit: func(context.Context, AdmissionContext, workflow.ResourceSourcePipelineTarget, string) (ReadySourceAdmission, error) {
			t.Fatal("existing source run must not schedule a manual source admission")
			return ReadySourceAdmission{}, nil
		},
		load: func(_ context.Context, teamID int, gotAdmissionID int64, got workflow.ResourceSourcePipelineTarget) (ReadySourceAdmission, error) {
			loads++
			if teamID != 7 || gotAdmissionID != admissionID || !reflect.DeepEqual(got, sourceTarget) {
				t.Fatalf("ready load = (%d, %d, %#v)", teamID, gotAdmissionID, got)
			}
			return ready, nil
		},
	}
	finds := 0
	store := &storeStub{
		find: func(context.Context, int, string) (db.AgentWorkflowRun, bool, error) {
			finds++
			if finds == 1 {
				return admitting, true, nil
			}
			return running, true, nil
		},
		snapshots: func(_ context.Context, id snapshot.WorkflowRunID) ([]db.AgentWorkflowRunSnapshotBinding, error) {
			if id != runID {
				t.Fatalf("binding run ID = %d", id)
			}
			return []db.AgentWorkflowRunSnapshotBinding{{
				WorkflowRunID: runID, Direction: db.AgentWorkflowRunSnapshotInput,
				PortName: "source-repo", Snapshot: refs["source-repo"],
			}}, nil
		},
		transition: func(_ context.Context, id snapshot.WorkflowRunID, from, to db.AgentWorkflowRunStatus, message string) (bool, error) {
			if id != runID || from != db.AgentWorkflowRunStatusAdmitting || to != db.AgentWorkflowRunStatusRunning || message != "" {
				t.Fatalf("transition = (%d, %s, %s, %q)", id, from, to, message)
			}
			return true, nil
		},
	}
	binder, err := NewBinder(
		&resolverStub{get: func(_ context.Context, name string, version int) (workflow.Definition, bool, error) {
			if name != definition.Name || version != definition.Version {
				t.Fatalf("definition reuse lookup = (%q, %d)", name, version)
			}
			return definition, true, nil
		}},
		&rendererStub{
			full: func(workflow.Definition) (workflow.FunctionTarget, error) { return target, nil },
			render: func(workflow.FunctionTarget) (workflow.RenderedFunction, error) {
				t.Fatal("existing source run must not render without sealed sources")
				return workflow.RenderedFunction{}, nil
			},
			bound: func(got workflow.FunctionTarget, gotRefs map[string]snapshot.SnapshotRef) (workflow.RenderedFunction, error) {
				renders++
				if !reflect.DeepEqual(got, target) || !reflect.DeepEqual(gotRefs, refs) {
					t.Fatalf("bound resume render = (%#v, %#v)", got, gotRefs)
				}
				return rendered, nil
			},
		},
		&authorizerStub{get: func(context.Context, int, snapshot.SnapshotID) (snapshot.Snapshot, bool, error) {
			t.Fatal("resume must use sealed source bindings, not reauthorize selection")
			return snapshot.Snapshot{}, false, nil
		}},
		store, &budgetStub{}, &saverStub{save: func(context.Context, AdmissionContext, ImmutableTemplateSpec) (WorkflowRunTemplateRef, error) {
			return WorkflowRunTemplateRef{PipelineID: templateID}, nil
		}},
		&creatorStub{create: func(_ context.Context, gotRunID snapshot.WorkflowRunID, _ WorkflowRunTemplateRef, params map[string]any, _ string, _ BeforeWorkflowRunCommit) (WorkflowRunExecution, bool, error) {
			sourceParam, paramErr := workflow.InputParamName("source-repo")
			if paramErr != nil || gotRunID != runID || params[sourceParam] != refs["source-repo"].ID.String() {
				t.Fatalf("opaque envelope params = (%d, %#v)", gotRunID, params)
			}
			return WorkflowRunExecution{}, true, nil
		}},
		&credentialStub{admit: func(context.Context) error { return nil }},
		WithResourceSourceAdmitter(sources),
	)
	if err != nil {
		t.Fatal(err)
	}
	result, err := binder.BindAndCreate(context.Background(), AdmissionContext{
		TeamID: 7, TeamName: "research", CreatedBy: "alice", Origin: Origin{Kind: "manual"},
	}, BindRequest{WorkflowName: definition.Name, IdempotencyKey: "resume-source"})
	if err != nil {
		t.Fatalf("BindAndCreate: %v", err)
	}
	if result.Run.ID != runID || result.Created || loads != 2 || renders != 1 {
		t.Fatalf("result = %#v, loads=%d renders=%d", result, loads, renders)
	}
}

func TestBindAndCreateReplaysSealedSourceAdmissionAndExactBindings(t *testing.T) {
	definition := binderResourceSourceOnlyDefinition(t)
	target, err := workflow.FullFunctionTarget(definition)
	if err != nil {
		t.Fatal(err)
	}
	refs := map[string]snapshot.SnapshotRef{
		"source-repo": {ID: 701, Type: "repository/v1", Digest: "sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"},
	}
	rendered, err := workflow.RenderFunctionWithBoundSources(target, refs)
	if err != nil {
		t.Fatal(err)
	}
	sourceTarget, err := workflow.ResourceSourcePipelineTargetFor(definition, 7)
	if err != nil {
		t.Fatal(err)
	}
	sourceConfig, err := workflow.RenderResourceSourcePipeline(sourceTarget)
	if err != nil {
		t.Fatal(err)
	}
	admissionID := int64(91)
	ready := ReadySourceAdmission{
		AdmissionID: admissionID, TeamID: 7, WorkflowDefinitionID: definition.ID,
		WorkflowName: definition.Name, WorkflowVersion: definition.Version,
		SourceConfigHash: sourceConfig.ConfigHash, Inputs: refs,
	}
	sourceID, replayID := snapshot.WorkflowRunID(801), snapshot.WorkflowRunID(802)
	source := db.AgentWorkflowRun{
		ID: sourceID, TeamID: 7, TeamName: "research", WorkflowDefinitionID: definition.ID,
		WorkflowName: definition.Name, WorkflowVersion: definition.Version,
		SchemaVersion: definition.SchemaVersion, SignatureVersion: definition.SignatureVersion,
		DefinitionContentHash: definition.ContentHash, IdempotencyKey: "source-original",
		ParameterizedConfig: mustCanonical(t, rendered.Config), ParameterizedConfigHash: rendered.TargetConfigHash,
		ResourceSourceAdmissionID: &admissionID, OriginKind: "manual", CreatedBy: "alice",
		Status: db.AgentWorkflowRunStatusFailed,
	}
	replayed := source
	replayed.ID, replayed.IdempotencyKey = replayID, "source-replay"
	replayed.OriginKind, replayed.OriginReference = "retry", sourceID.String()
	replayed.RetryOfWorkflowRunID = &sourceID
	replayed.Status = db.AgentWorkflowRunStatusRunning
	pipelineRunID, templateID, instanceID, plannedBuildID := 811, 812, 813, int64(814)
	instanceHash := "instance-config-hash"
	replayed.PipelineRunID, replayed.TemplatePipelineID, replayed.InstancePipelineID = &pipelineRunID, &templateID, &instanceID
	replayed.PlannedBuildID, replayed.ConcreteConfigHash = &plannedBuildID, &instanceHash
	replayed.ConcreteConfig = mustCanonical(t, rendered.Config)

	loads := 0
	sources := &binderResourceSourceAdmitterStub{
		admit: func(context.Context, AdmissionContext, workflow.ResourceSourcePipelineTarget, string) (ReadySourceAdmission, error) {
			t.Fatal("source replay must not schedule manual source admission")
			return ReadySourceAdmission{}, nil
		},
		load: func(_ context.Context, teamID int, gotAdmissionID int64, got workflow.ResourceSourcePipelineTarget) (ReadySourceAdmission, error) {
			loads++
			if teamID != 7 || gotAdmissionID != admissionID || !reflect.DeepEqual(got, sourceTarget) {
				t.Fatalf("replay ready load = (%d, %d, %#v)", teamID, gotAdmissionID, got)
			}
			return ready, nil
		},
	}
	store := &storeStub{
		find: func(context.Context, int, string) (db.AgentWorkflowRun, bool, error) {
			return db.AgentWorkflowRun{}, false, nil
		},
		get: func(_ context.Context, teamID int, id snapshot.WorkflowRunID) (db.AgentWorkflowRun, bool, error) {
			if teamID != 7 || id != sourceID {
				t.Fatalf("replay source lookup = (%d, %d)", teamID, id)
			}
			return source, true, nil
		},
		create: func(_ context.Context, request db.AgentWorkflowRunCreateRequest) (db.AgentWorkflowRun, bool, error) {
			if request.ResourceSourceAdmissionID == nil || *request.ResourceSourceAdmissionID != admissionID ||
				request.RetryOfWorkflowRunID == nil || *request.RetryOfWorkflowRunID != sourceID ||
				!reflect.DeepEqual(request.Inputs, refs) {
				t.Fatalf("replay create request = %#v", request)
			}
			return replayed, false, nil
		},
		snapshots: func(_ context.Context, id snapshot.WorkflowRunID) ([]db.AgentWorkflowRunSnapshotBinding, error) {
			if id != sourceID && id != replayID {
				t.Fatalf("replay binding lookup = %d", id)
			}
			return []db.AgentWorkflowRunSnapshotBinding{{
				WorkflowRunID: id, Direction: db.AgentWorkflowRunSnapshotInput,
				PortName: "source-repo", Snapshot: refs["source-repo"],
			}}, nil
		},
		transition: func(context.Context, snapshot.WorkflowRunID, db.AgentWorkflowRunStatus, db.AgentWorkflowRunStatus, string) (bool, error) {
			return false, errors.New("unexpected transition")
		},
	}
	binder, err := NewBinder(
		&resolverStub{
			live: func(context.Context, string) (workflow.Definition, bool, error) {
				t.Fatal("source replay must not resolve the current live revision")
				return workflow.Definition{}, false, nil
			},
			get: func(context.Context, string, int) (workflow.Definition, bool, error) { return definition, true, nil },
		},
		&rendererStub{
			full: func(workflow.Definition) (workflow.FunctionTarget, error) { return target, nil },
			render: func(workflow.FunctionTarget) (workflow.RenderedFunction, error) {
				t.Fatal("replay must not render an unbound source target")
				return workflow.RenderedFunction{}, nil
			},
			bound: func(workflow.FunctionTarget, map[string]snapshot.SnapshotRef) (workflow.RenderedFunction, error) {
				t.Fatal("replay existing running result needs no render")
				return workflow.RenderedFunction{}, nil
			},
		},
		&authorizerStub{get: func(context.Context, int, snapshot.SnapshotID) (snapshot.Snapshot, bool, error) {
			t.Fatal("source-only replay has no caller input")
			return snapshot.Snapshot{}, false, nil
		}},
		store, &budgetStub{}, &saverStub{}, &creatorStub{}, &credentialStub{},
		WithResourceSourceAdmitter(sources),
	)
	if err != nil {
		t.Fatal(err)
	}
	result, err := binder.BindAndCreate(context.Background(), AdmissionContext{
		TeamID: 7, TeamName: "research", CreatedBy: "alice", Origin: Origin{Kind: "retry", Reference: sourceID.String()},
	}, BindRequest{WorkflowName: definition.Name, IdempotencyKey: "source-replay", RetryOf: &sourceID})
	if err != nil {
		t.Fatalf("BindAndCreate: %v", err)
	}
	if result.Run.ID != replayID || result.Created || loads != 1 {
		t.Fatalf("result = %#v, loads=%d", result, loads)
	}
}

func binderResourceSourceDefinition(t *testing.T) workflow.Definition {
	t.Helper()
	definition := binderTestDefinition()
	definition.Compiled.Function.Plan = []atc.Step{{Config: &atc.AgentStep{
		Name: "noop", FunctionID: "noop", Prompt: "noop",
	}}}
	definition.Compiled.Function.Resources = atc.ResourceConfigs{{
		Name: "resource-repository", Type: "git",
		Source: atc.Source{"uri": "https://example.invalid/repository.git"},
	}}
	definition.Compiled.Function.ResourceSources = []workflow.ResourceSource{{
		Name: "source-repo", Resource: "resource-repository", Type: "repository/v1",
	}}
	return definition
}

func binderResourceSourceOnlyDefinition(t *testing.T) workflow.Definition {
	t.Helper()
	definition := binderResourceSourceDefinition(t)
	definition.Compiled.Function.Inputs = nil
	definition.Compiled.Function.Plan = []atc.Step{{Config: &atc.AgentStep{
		Name: "consume-source", FunctionID: "consume-source", Prompt: "consume source",
		Inputs: []string{"source-repo"},
		SnapshotInputs: map[string]atc.SnapshotInputConfig{
			"source-repo": {Type: "repository/v1"},
		},
	}}}
	return definition
}

type binderResourceSourceAdmitterStub struct {
	admit func(context.Context, AdmissionContext, workflow.ResourceSourcePipelineTarget, string) (ReadySourceAdmission, error)
	load  func(context.Context, int, int64, workflow.ResourceSourcePipelineTarget) (ReadySourceAdmission, error)
}

func (stub *binderResourceSourceAdmitterStub) AdmitManual(ctx context.Context, admission AdmissionContext, target workflow.ResourceSourcePipelineTarget, key string) (ReadySourceAdmission, error) {
	if stub.admit == nil {
		return ReadySourceAdmission{}, errors.New("unexpected manual source admission")
	}
	return stub.admit(ctx, admission, target, key)
}

func (stub *binderResourceSourceAdmitterStub) LoadReady(ctx context.Context, teamID int, admissionID int64, target workflow.ResourceSourcePipelineTarget) (ReadySourceAdmission, error) {
	if stub.load == nil {
		return ReadySourceAdmission{}, errors.New("unexpected ready source load")
	}
	return stub.load(ctx, teamID, admissionID, target)
}
