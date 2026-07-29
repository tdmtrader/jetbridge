package workflowrun

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/concourse/concourse/agent/experiment"
	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/agent/workflow"
	"github.com/concourse/concourse/atc/db"
)

type experimentNativeBinderStub struct {
	bind func(context.Context, AdmissionContext, BindRequest, *int64) (BindResult, error)
}

func (stub *experimentNativeBinderStub) BindExperimentWithReadySourceAdmission(
	ctx context.Context,
	admission AdmissionContext,
	request BindRequest,
	resourceSourceAdmissionID *int64,
) (BindResult, error) {
	return stub.bind(ctx, admission, request, resourceSourceAdmissionID)
}

func TestExperimentBinderPreservesFrozenTargetIdentity(t *testing.T) {
	ctx := context.Background()
	version := 3
	hash := strings.Repeat("a", 64)
	devHash := strings.Repeat("b", 64)
	functionID := "review"
	inputs := map[string]snapshot.SnapshotID{"repo": 9007199254740993}
	adapter, err := NewExperimentBinderAdapter(&experimentNativeBinderStub{bind: func(
		gotContext context.Context,
		admission AdmissionContext,
		request BindRequest,
		resourceSourceAdmissionID *int64,
	) (BindResult, error) {
		if gotContext != ctx {
			t.Fatal("context was not preserved")
		}
		if admission.TeamID != 7 || admission.TeamName != "research" || admission.CreatedBy != "alice" ||
			admission.Origin != (Origin{Kind: "experiment", Reference: "experiment:11:cell:13"}) {
			t.Fatalf("admission = %+v", admission)
		}
		if request.WorkflowName != "review-flow" || request.Version == nil || *request.Version != version ||
			request.FunctionID != functionID || request.IdempotencyKey != "experiment:11:cell:13:candidate" ||
			request.ExpectedWorkflowDefinitionID != 41 || request.ExpectedTargetConfigHash != hash ||
			request.ExpectedDevValidationProvenanceHash == nil || *request.ExpectedDevValidationProvenanceHash != devHash ||
			request.ExperimentAdmission == nil ||
			request.ExperimentAdmission.ExperimentID != 11 ||
			request.ExperimentAdmission.CellID != 13 ||
			request.ExperimentAdmission.Phase != "candidate" ||
			!reflect.DeepEqual(request.Inputs, inputs) {
			t.Fatalf("bind request = %+v", request)
		}
		if resourceSourceAdmissionID != nil {
			t.Fatalf("source admission = %#v, want nil for source-free target", resourceSourceAdmissionID)
		}
		return BindResult{Run: db.AgentWorkflowRun{
			ID: 9007199254741001, WorkflowDefinitionID: 41, WorkflowName: "review-flow",
			WorkflowVersion: version, FunctionID: &functionID, ParameterizedConfigHash: hash, DevValidationProvenanceHash: devHash,
		}}, nil
	}})
	if err != nil {
		t.Fatal(err)
	}

	result, err := adapter.BindAndCreate(ctx, experiment.AdmissionContext{
		TeamID: 7, TeamName: "research", CreatedBy: "alice",
		Origin: experiment.Origin{Kind: "experiment", Reference: "experiment:11:cell:13"},
	}, experiment.BindRequest{
		WorkflowName: "review-flow", DefinitionID: 41, Version: &version, FunctionID: functionID,
		Inputs: inputs, IdempotencyKey: "experiment:11:cell:13:candidate", ExpectedTargetConfigHash: hash, ExpectedDevValidationProvenanceHash: devHash,
		AdmissionGate: experiment.AdmissionGate{
			ExperimentID: 11, CellID: 13, Phase: experiment.AdmissionCandidate,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.WorkflowRunID != 9007199254741001 || result.WorkflowDefinitionID != 41 ||
		result.WorkflowName != "review-flow" || result.WorkflowVersion != version ||
		result.FunctionID != functionID || result.TargetConfigHash != hash || result.DevValidationProvenanceHash != devHash {
		t.Fatalf("bind result = %+v", result)
	}
}

func TestExperimentBinderPassesServerDerivedReadySourceAdmissionPrivately(t *testing.T) {
	sourceAdmissionID := int64(71)
	adapter, err := NewExperimentBinderAdapter(&experimentNativeBinderStub{bind: func(
		_ context.Context,
		_ AdmissionContext,
		request BindRequest,
		gotSourceAdmissionID *int64,
	) (BindResult, error) {
		if gotSourceAdmissionID == nil || *gotSourceAdmissionID != sourceAdmissionID {
			t.Fatalf("resource source admission = %#v, want %d", gotSourceAdmissionID, sourceAdmissionID)
		}
		if request.ExperimentAdmission == nil || request.ExperimentAdmission.ExperimentID != 11 {
			t.Fatalf("experiment gate = %#v", request.ExperimentAdmission)
		}
		return BindResult{Run: db.AgentWorkflowRun{ID: 101}}, nil
	}})
	if err != nil {
		t.Fatal(err)
	}

	_, err = adapter.BindAndCreate(context.Background(), experiment.AdmissionContext{
		TeamID: 7, TeamName: "research", CreatedBy: "alice",
		Origin: experiment.Origin{Kind: "experiment", Reference: "experiment:11:cell:13"},
	}, experiment.BindRequest{
		ResourceSourceAdmissionID: &sourceAdmissionID,
		AdmissionGate: experiment.AdmissionGate{
			ExperimentID: 11, CellID: 13, Phase: experiment.AdmissionCandidate,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestExperimentBinderPreservesNonV3PlatformFailureClassification(t *testing.T) {
	definition := binderTestDefinition()
	definition.SchemaVersion = 2
	binder, err := NewBinder(
		&resolverStub{live: func(context.Context, string) (workflow.Definition, bool, error) {
			return definition, true, nil
		}},
		&rendererStub{},
		&authorizerStub{},
		&storeStub{},
		&budgetStub{},
		&saverStub{},
		&creatorStub{},
		&credentialStub{},
	)
	if err != nil {
		t.Fatal(err)
	}
	adapter, err := NewExperimentBinderAdapter(binder)
	if err != nil {
		t.Fatal(err)
	}

	_, err = adapter.BindAndCreate(context.Background(), experiment.AdmissionContext{
		TeamID: 7, TeamName: "research", CreatedBy: "alice",
		Origin: experiment.Origin{Kind: "experiment", Reference: "experiment:11:cell:13"},
	}, experiment.BindRequest{
		WorkflowName: definition.Name, DefinitionID: int64(definition.ID),
		IdempotencyKey: "experiment:11:cell:13:candidate",
		AdmissionGate: experiment.AdmissionGate{
			ExperimentID: 11, CellID: 13, Phase: experiment.AdmissionCandidate,
		},
	})
	if !errors.Is(err, ErrPlatformFailure) {
		t.Fatalf("error = %v, want ErrPlatformFailure", err)
	}
	if errors.Is(err, experiment.ErrBindInvalidRequest) {
		t.Fatalf("platform failure was reclassified as invalid request: %v", err)
	}
}

func TestExperimentBinderClassifiesClosedAdmission(t *testing.T) {
	adapter, err := NewExperimentBinderAdapter(&experimentNativeBinderStub{bind: func(
		context.Context,
		AdmissionContext,
		BindRequest,
		*int64,
	) (BindResult, error) {
		return BindResult{}, ErrExperimentAdmissionClosed
	}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = adapter.BindAndCreate(context.Background(), experiment.AdmissionContext{
		Origin: experiment.Origin{Kind: "experiment", Reference: "experiment:11:cell:13"},
	}, experiment.BindRequest{AdmissionGate: experiment.AdmissionGate{
		ExperimentID: 11, CellID: 13, Phase: experiment.AdmissionCandidate,
	}})
	if !errors.Is(err, experiment.ErrBindAdmissionClosed) {
		t.Fatalf("error = %v, want closed experiment admission", err)
	}
}

func TestExperimentBinderClassifiesFrozenAdmissionDrift(t *testing.T) {
	adapter, err := NewExperimentBinderAdapter(&experimentNativeBinderStub{bind: func(
		context.Context,
		AdmissionContext,
		BindRequest,
		*int64,
	) (BindResult, error) {
		return BindResult{}, fmt.Errorf("%w: frozen workflow definition changed", ErrInvalidRequest)
	}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = adapter.BindAndCreate(context.Background(), experiment.AdmissionContext{
		Origin: experiment.Origin{Kind: "experiment", Reference: "experiment:11:cell:13"},
	}, experiment.BindRequest{AdmissionGate: experiment.AdmissionGate{
		ExperimentID: 11, CellID: 13, Phase: experiment.AdmissionCandidate,
	}})
	if !errors.Is(err, experiment.ErrBindInvalidRequest) {
		t.Fatalf("error = %v, want invalid experiment admission", err)
	}
}
