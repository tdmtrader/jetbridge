package resourcecapture_test

import (
	"context"
	"testing"

	"github.com/concourse/concourse/agent/resourcecapture"
	"github.com/concourse/concourse/agent/workflow"
	"github.com/concourse/concourse/agent/workflowrun"
	"github.com/concourse/concourse/atc/db"
)

type immutableTemplateSaverStub struct {
	admission workflowrun.AdmissionContext
	spec      workflowrun.ImmutableTemplateSpec
	ref       db.WorkflowRunTemplateRef
}

func (stub *immutableTemplateSaverStub) SaveOrReuse(_ context.Context, admission workflowrun.AdmissionContext, spec workflowrun.ImmutableTemplateSpec) (workflowrun.WorkflowRunTemplateRef, error) {
	stub.admission, stub.spec = admission, spec
	return stub.ref, nil
}

func TestTemplateStoreUsesImmutableServerRegistryAndCanonicalHash(t *testing.T) {
	resolved := repositoryResource()
	coreTemplates := &fakeTemplates{save: func(_ context.Context, spec resourcecapture.TemplateSpec) (resourcecapture.TemplateRef, error) {
		return resourcecapture.TemplateRef{ID: 1, Name: spec.Name}, nil
	}}
	// Let the core build a real capture template, then feed that exact spec to
	// the adapter under test.
	capturer := newCapturer(t, &fakeResolver{resolve: func(context.Context, resourcecapture.ResolveRequest) (resourcecapture.ResolvedResource, bool, error) {
		return resolved, true, nil
	}}, coreTemplates, &fakeExecutions{start: func(_ context.Context, request resourcecapture.ExecutionRequest) (resourcecapture.Execution, bool, error) {
		return resourcecapture.Execution{PipelineRunID: 1, TemplatePipelineID: request.Template.ID, InstancePipelineID: 2, Status: db.PipelineRunRunning}, true, nil
	}}, &fakeOutputs{})
	if _, err := capturer.Capture(context.Background(), validRequest()); err != nil {
		t.Fatal(err)
	}
	spec := coreTemplates.calls[0]
	wantHash, err := workflow.TargetConfigHash(spec.Config)
	if err != nil {
		t.Fatal(err)
	}
	saver := &immutableTemplateSaverStub{ref: db.WorkflowRunTemplateRef{PipelineID: 41, TeamID: 7, Name: spec.Name, ConfigVersion: 1, FullHash: wantHash}}
	store, err := resourcecapture.NewTemplateStore(saver)
	if err != nil {
		t.Fatal(err)
	}
	ref, err := store.SaveOrReuse(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	if ref.ID != 41 || ref.Name != spec.Name || ref.FullHash != wantHash {
		t.Fatalf("ref = %#v", ref)
	}
	if saver.admission.TeamID != 7 || saver.admission.TeamName != "main" || saver.admission.CreatedBy != "jetbridge" ||
		saver.admission.Origin.Kind != "resource-version" || saver.admission.Origin.Reference != spec.OperationKey {
		t.Fatalf("admission = %#v", saver.admission)
	}
	if saver.spec.FullHash != wantHash || saver.spec.Name != spec.Name || string(saver.spec.CanonicalJSON) != string(spec.CanonicalJSON) {
		t.Fatalf("immutable spec = %#v", saver.spec)
	}
}
