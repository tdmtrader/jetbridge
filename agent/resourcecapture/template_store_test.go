package resourcecapture_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"testing"

	"github.com/concourse/concourse/agent/resourcecapture"
	"github.com/concourse/concourse/agent/workflow"
	"github.com/concourse/concourse/agent/workflowrun"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
)

type immutableTemplateSaverStub struct {
	admission workflowrun.AdmissionContext
	spec      workflowrun.ImmutableTemplateSpec
	ref       db.WorkflowRunTemplateRef
	err       error
}

func (stub *immutableTemplateSaverStub) SaveOrReuse(_ context.Context, admission workflowrun.AdmissionContext, spec workflowrun.ImmutableTemplateSpec) (workflowrun.WorkflowRunTemplateRef, error) {
	stub.admission, stub.spec = admission, spec
	return stub.ref, stub.err
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

func TestTemplateStoreAcceptsCaptureSpecWithRealTemplateSaver(t *testing.T) {
	database := useRealResourceCaptureDB(t)
	team, err := database.Teams.CreateTeam(atc.Team{Name: "main"})
	if err != nil {
		t.Fatal(err)
	}
	resolved := repositoryResource()
	resolved.TeamID = team.ID()
	coreTemplates := &fakeTemplates{save: func(_ context.Context, spec resourcecapture.TemplateSpec) (resourcecapture.TemplateRef, error) {
		return resourcecapture.TemplateRef{ID: 1, Name: spec.Name}, nil
	}}
	capturer := newCapturer(t, &fakeResolver{resolve: func(context.Context, resourcecapture.ResolveRequest) (resourcecapture.ResolvedResource, bool, error) {
		return resolved, true, nil
	}}, coreTemplates, &fakeExecutions{start: func(_ context.Context, request resourcecapture.ExecutionRequest) (resourcecapture.Execution, bool, error) {
		return resourcecapture.Execution{PipelineRunID: 1, TemplatePipelineID: request.Template.ID, InstancePipelineID: 2, Status: db.PipelineRunRunning}, true, nil
	}}, &fakeOutputs{})
	request := validRequest()
	request.TeamID = team.ID()
	if _, err := capturer.Capture(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	capturedSpec := coreTemplates.calls[0]
	core, err := workflowrun.NewTemplateSaver(database.Teams, database.Templates)
	if err != nil {
		t.Fatal(err)
	}
	store, err := resourcecapture.NewTemplateStore(core)
	if err != nil {
		t.Fatal(err)
	}
	ref, err := store.SaveOrReuse(context.Background(), capturedSpec)
	if err != nil {
		t.Fatalf("real TemplateSaver rejected capture spec: %v", err)
	}
	if ref.ID <= 0 || ref.TeamID != team.ID() || ref.Name != capturedSpec.Name || ref.ConfigVersion <= 0 {
		t.Fatalf("template ref = %#v", ref)
	}
	pipeline, found, err := team.Pipeline(atc.PipelineRef{Name: capturedSpec.Name})
	if err != nil {
		t.Fatal(err)
	}
	if !found {
		t.Fatal("persisted capture template was not found through its owning team")
	}
	owned, err := database.Templates.IsWorkflowRunTemplate(context.Background(), pipeline.ID())
	if err != nil {
		t.Fatal(err)
	}
	if pipeline.ID() != ref.ID || !owned || !pipeline.Template() || pipeline.InstanceVars() != nil || pipeline.Archived() ||
		int(pipeline.ConfigVersion()) != ref.ConfigVersion {
		t.Fatalf("persisted template = id %d, owned %v, template %v, instance vars %#v, archived %v, config version %d",
			pipeline.ID(), owned, pipeline.Template(), pipeline.InstanceVars(), pipeline.Archived(), pipeline.ConfigVersion())
	}
	storedConfig, err := pipeline.Config()
	if err != nil {
		t.Fatal(err)
	}
	storedCanonical, err := storedConfig.CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	if string(storedCanonical) != string(capturedSpec.CanonicalJSON) {
		t.Fatal("persisted template config differs from the captured canonical config")
	}
	reused, err := store.SaveOrReuse(context.Background(), capturedSpec)
	if err != nil {
		t.Fatalf("real TemplateSaver rejected exact reuse: %v", err)
	}
	if reused != ref {
		t.Fatalf("reused ref = %#v, want %#v", reused, ref)
	}

	for _, name := range []string{
		capturedSpec.Name[:len(capturedSpec.Name)-13],
		capturedSpec.Name[:len(capturedSpec.Name)-1] + "0",
	} {
		t.Run(name, func(t *testing.T) {
			spec := capturedSpec.Clone()
			spec.Name = name
			targetHash, err := workflow.TargetConfigHash(spec.Config)
			if err != nil {
				t.Fatal(err)
			}
			_, err = core.SaveOrReuse(context.Background(), workflowrun.AdmissionContext{TeamID: spec.TeamID, TeamName: spec.TeamName}, workflowrun.ImmutableTemplateSpec{
				Name: spec.Name, FullHash: targetHash, CanonicalJSON: spec.CanonicalJSON, Config: spec.Config,
			})
			if !errors.Is(err, workflowrun.ErrImmutableTemplateCollision) {
				t.Fatalf("TemplateSaver error = %v, want immutable template collision", err)
			}
		})
	}
}

func TestTemplateStoreMapsKnownImmutableSaverFailures(t *testing.T) {
	spec := resourcecapture.TemplateSpec{
		TeamID: 7, TeamName: "main", Name: "agent-resource-capture-aaaaaaaaaaaaaaaaaaaaaaaa-aaaaaaaaaaaa",
		OperationKey: "operation", Config: atc.Config{Template: true, Jobs: atc.JobConfigs{{Name: "capture"}}},
	}
	canonical, err := spec.Config.CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	spec.CanonicalJSON = canonical
	sum := sha256.Sum256(canonical)
	spec.FullHash = hex.EncodeToString(sum[:])
	for _, test := range []struct {
		name string
		err  error
		want error
	}{
		{name: "collision", err: workflowrun.ErrImmutableTemplateCollision, want: resourcecapture.ErrTemplateConflict},
		{name: "platform failure", err: workflowrun.ErrPlatformFailure, want: resourcecapture.ErrUnavailable},
	} {
		t.Run(test.name, func(t *testing.T) {
			store, err := resourcecapture.NewTemplateStore(&immutableTemplateSaverStub{err: test.err})
			if err != nil {
				t.Fatal(err)
			}
			_, err = store.SaveOrReuse(context.Background(), spec)
			if !errors.Is(err, test.want) {
				t.Fatalf("SaveOrReuse error = %v, want %v", err, test.want)
			}
		})
	}
}
