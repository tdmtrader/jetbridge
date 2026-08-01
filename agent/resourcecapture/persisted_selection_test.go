package resourcecapture_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"testing"

	"github.com/concourse/concourse/agent/resourcecapture"
	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/agent/workflow"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
)

func TestCapturePersistedSelectionRejectsSubstitutedResourceIdentity(t *testing.T) {
	resolved := repositoryResource()
	resolved.PipelineID = 13
	resolved.ResourceID = 17
	resolved.ResourceVersionID = resolved.ResourceConfigVersionID
	key, err := db.WorkflowResourceSourceCaptureOperationKey(
		7, 91, 13, resolved.PipelineConfigVersion, "repository", "repository",
		persistedSelectionDigest(resolved.Version), snapshot.TypeRef("repository/v1"),
	)
	if err != nil {
		t.Fatal(err)
	}
	resolver := &fakeResolver{resolve: func(context.Context, resourcecapture.ResolveRequest) (resourcecapture.ResolvedResource, bool, error) {
		drifted := resolved
		drifted.ResourceID++
		return drifted, true, nil
	}}
	templates := &fakeTemplates{}
	capturer := newCapturer(t, resolver, templates, &fakeExecutions{}, &fakeOutputs{})

	_, err = capturer.CapturePersistedSelection(context.Background(), resourcecapture.PersistedSelection{
		AdmissionID: 41, WorkflowDefinitionID: 91, SourceName: "repository", TeamID: 7, TeamName: "main",
		SourcePipelineID: 13, PipelineID: 13, Pipeline: atc.PipelineRef{Name: "delivery"},
		PipelineConfigVersion: resolved.PipelineConfigVersion, ResourceID: resolved.ResourceID,
		Resource: resolved.Resource, ResourceTypes: resolved.ResourceTypes,
		ResourceConfigVersionID: resolved.ResourceConfigVersionID,
		ResourceVersionID:       resolved.ResourceVersionID, VersionDigest: persistedSelectionDigest(resolved.Version),
		Version: resolved.Version, SnapshotType: "repository/v1", CaptureOperationKey: key,
	})
	if !errors.Is(err, resourcecapture.ErrPersistedSelectionDrift) {
		t.Fatalf("CapturePersistedSelection() error = %v, want persisted selection drift", err)
	}
	if len(templates.calls) != 0 {
		t.Fatalf("capture template was saved after persisted-selection drift: %#v", templates.calls)
	}
}

func TestCapturePersistedSelectionUsesStableRenderedTemplateIdentity(t *testing.T) {
	resolved := repositoryResource()
	resolved.PipelineID = 13
	resolved.ResourceID = 17
	resolved.ResourceVersionID = resolved.ResourceConfigVersionID
	selection := persistedSelection(t, resolved)
	resolver := &fakeResolver{resolve: func(context.Context, resourcecapture.ResolveRequest) (resourcecapture.ResolvedResource, bool, error) {
		return resolved, true, nil
	}}
	templates := &fakeTemplates{save: func(_ context.Context, spec resourcecapture.TemplateSpec) (resourcecapture.TemplateRef, error) {
		return resourcecapture.TemplateRef{ID: 41, Name: spec.Name}, nil
	}}
	executions := &fakeExecutions{start: func(_ context.Context, request resourcecapture.ExecutionRequest) (resourcecapture.Execution, bool, error) {
		return resourcecapture.Execution{PipelineRunID: 51, TemplatePipelineID: request.Template.ID, InstancePipelineID: 61, Status: db.PipelineRunRunning}, true, nil
	}}
	capturer := newCapturer(t, resolver, templates, executions, &fakeOutputs{})

	first, err := capturer.CapturePersistedSelection(context.Background(), selection)
	if err != nil {
		t.Fatal(err)
	}
	second, err := capturer.CapturePersistedSelection(context.Background(), selection)
	if err != nil {
		t.Fatal(err)
	}
	if len(templates.calls) != 2 || first.OperationKey != selection.CaptureOperationKey || second.OperationKey != selection.CaptureOperationKey || templates.calls[0].Name != templates.calls[1].Name {
		t.Fatalf("persisted selection did not reuse identity: results=%#v/%#v templates=%#v", first, second, templates.calls)
	}
	targetHash, err := workflow.TargetConfigHash(templates.calls[0].Config)
	if err != nil {
		t.Fatal(err)
	}
	wantName := "agent-resource-capture-" + selection.CaptureOperationKey[:24] + "-" + targetHash[:12]
	if templates.calls[0].Name != wantName {
		t.Fatalf("template name = %q, want %q", templates.calls[0].Name, wantName)
	}
}

func persistedSelection(t *testing.T, resolved resourcecapture.ResolvedResource) resourcecapture.PersistedSelection {
	t.Helper()
	key, err := db.WorkflowResourceSourceCaptureOperationKey(
		7, 91, 13, resolved.PipelineConfigVersion, "repository", "repository",
		persistedSelectionDigest(resolved.Version), snapshot.TypeRef("repository/v1"),
	)
	if err != nil {
		t.Fatal(err)
	}
	return resourcecapture.PersistedSelection{
		AdmissionID: 41, WorkflowDefinitionID: 91, SourceName: "repository", TeamID: 7, TeamName: "main",
		SourcePipelineID: 13, PipelineID: 13, Pipeline: resolved.Pipeline,
		PipelineConfigVersion: resolved.PipelineConfigVersion, ResourceID: resolved.ResourceID,
		Resource: resolved.Resource, ResourceTypes: resolved.ResourceTypes,
		ResourceConfigVersionID: resolved.ResourceConfigVersionID, ResourceVersionID: resolved.ResourceVersionID,
		VersionDigest: persistedSelectionDigest(resolved.Version), Version: resolved.Version,
		SnapshotType: "repository/v1", CaptureOperationKey: key,
	}
}

func persistedSelectionDigest(version atc.Version) string {
	encoded, err := json.Marshal(version)
	if err != nil {
		panic(err)
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])
}
