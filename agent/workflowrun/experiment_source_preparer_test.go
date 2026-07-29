package workflowrun

import (
	"context"
	"fmt"
	"testing"

	"github.com/concourse/concourse/agent/experiment"
	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/agent/workflow"
)

func TestExperimentResourceSourcePreparerStoresOneReadyAdmissionPerDefinitionAndHash(t *testing.T) {
	definition := binderResourceSourceDefinition(t)
	resolver := &experimentSourceDefinitionResolver{
		definitions: map[string]workflow.Definition{
			fmt.Sprintf("%s/%d", definition.Name, definition.Version): definition,
		},
	}
	sources := &experimentSourceAdmitterStub{nextAdmissionID: 71}
	preparer, err := NewExperimentResourceSourcePreparer(resolver, sources)
	if err != nil {
		t.Fatalf("construct experiment source preparer: %v", err)
	}
	target := experiment.Target{
		Kind: experiment.TargetWorkflow, WorkflowName: definition.Name,
		DefinitionID: int64(definition.ID), Version: definition.Version,
	}
	prepared, err := preparer.PrepareResourceSources(
		context.Background(),
		experiment.ResourceSourcePreparation{
			TeamID: 7, TeamName: "research", Actor: "alice", ExperimentID: 31,
			Definition: experiment.Definition{
				Variants: []experiment.Variant{
					{Label: "control", Target: target},
					{Label: "candidate", Target: target},
				},
				Evaluator: experiment.Evaluator{Target: target},
			},
		},
	)
	if err != nil {
		t.Fatalf("prepare experiment resource sources: %v", err)
	}

	if len(sources.calls) != 1 {
		t.Fatalf("manual source preparations = %#v, want one shared definition/hash admission", sources.calls)
	}
	call := sources.calls[0]
	if call.admission.TeamID != 7 || call.admission.TeamName != "research" ||
		call.admission.CreatedBy != "alice" ||
		call.admission.Origin.Kind != "experiment-resource-source" ||
		call.admission.Origin.Reference != fmt.Sprintf(
			"experiment:31:definition:%d", definition.ID,
		) {
		t.Fatalf("preparation authority = %#v, want trusted experiment parent identity", call.admission)
	}
	rendered, err := workflow.RenderResourceSourcePipeline(call.target)
	if err != nil {
		t.Fatalf("render prepared target: %v", err)
	}
	wantKey := fmt.Sprintf(
		"experiment:31:resource-source:%d:%s", definition.ID, rendered.ConfigHash,
	)
	if call.idempotencyKey != wantKey {
		t.Fatalf("preparation idempotency = %q, want %q", call.idempotencyKey, wantKey)
	}
	if len(prepared) != 1 ||
		prepared[0].WorkflowDefinitionID != int64(definition.ID) ||
		prepared[0].SourceConfigHash != rendered.ConfigHash ||
		prepared[0].AdmissionID != 71 {
		t.Fatalf("prepared admissions = %#v, want one exact reusable identity", prepared)
	}
}

func TestExperimentResourceSourcePreparerRejectsReadyAdmissionIdentityDrift(t *testing.T) {
	definition := binderResourceSourceDefinition(t)
	resolver := &experimentSourceDefinitionResolver{
		definitions: map[string]workflow.Definition{
			fmt.Sprintf("%s/%d", definition.Name, definition.Version): definition,
		},
	}
	sources := &experimentSourceAdmitterStub{
		nextAdmissionID: 71,
		mutate: func(ready *ReadySourceAdmission) {
			ready.SourceConfigHash = sourceBuildTestHash
		},
	}
	preparer, err := NewExperimentResourceSourcePreparer(resolver, sources)
	if err != nil {
		t.Fatalf("construct experiment source preparer: %v", err)
	}

	_, err = preparer.PrepareResourceSources(
		context.Background(),
		experiment.ResourceSourcePreparation{
			TeamID: 7, TeamName: "research", Actor: "alice", ExperimentID: 31,
			Definition: experiment.Definition{
				Variants: []experiment.Variant{{Target: experiment.Target{
					Kind: experiment.TargetWorkflow, WorkflowName: definition.Name,
					DefinitionID: int64(definition.ID), Version: definition.Version,
				}}},
			},
		},
	)
	if err == nil {
		t.Fatal("expected drifted ready source identity to fail closed")
	}
}

type experimentSourceDefinitionResolver struct {
	definitions map[string]workflow.Definition
}

func (resolver *experimentSourceDefinitionResolver) Live(
	context.Context,
	string,
) (workflow.Definition, bool, error) {
	return workflow.Definition{}, false, nil
}

func (resolver *experimentSourceDefinitionResolver) Get(
	_ context.Context,
	name string,
	version int,
) (workflow.Definition, bool, error) {
	value, found := resolver.definitions[fmt.Sprintf("%s/%d", name, version)]
	return value, found, nil
}

type experimentSourceAdmissionCall struct {
	admission      AdmissionContext
	target         workflow.ResourceSourcePipelineTarget
	idempotencyKey string
}

type experimentSourceAdmitterStub struct {
	nextAdmissionID int64
	calls           []experimentSourceAdmissionCall
	mutate          func(*ReadySourceAdmission)
}

func (stub *experimentSourceAdmitterStub) AdmitManual(
	_ context.Context,
	admission AdmissionContext,
	target workflow.ResourceSourcePipelineTarget,
	idempotencyKey string,
) (ReadySourceAdmission, error) {
	stub.calls = append(stub.calls, experimentSourceAdmissionCall{
		admission: cloneAdmission(admission), target: target,
		idempotencyKey: idempotencyKey,
	})
	rendered, err := workflow.RenderResourceSourcePipeline(target)
	if err != nil {
		return ReadySourceAdmission{}, err
	}
	ready := ReadySourceAdmission{
		AdmissionID: stub.nextAdmissionID, TeamID: target.TeamID,
		WorkflowDefinitionID: target.WorkflowDefinitionID,
		WorkflowName:         target.WorkflowName,
		WorkflowVersion:      target.WorkflowVersion,
		SourceConfigHash:     rendered.ConfigHash,
		Inputs:               make(map[string]snapshot.SnapshotRef, len(target.Sources)),
	}
	for _, source := range target.Sources {
		ready.Inputs[source.Name] = snapshot.SnapshotRef{
			ID: 101, Type: source.Type,
			Digest: snapshot.Digest("sha256:" + sourceBuildTestHash),
		}
	}
	if stub.mutate != nil {
		stub.mutate(&ready)
	}
	return ready, nil
}

func (*experimentSourceAdmitterStub) LoadReady(
	context.Context,
	int,
	int64,
	workflow.ResourceSourcePipelineTarget,
) (ReadySourceAdmission, error) {
	return ReadySourceAdmission{}, fmt.Errorf("unexpected ready load")
}
