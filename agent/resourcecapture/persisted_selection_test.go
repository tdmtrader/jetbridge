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

func persistedSelectionDigest(version atc.Version) string {
	encoded, err := json.Marshal(version)
	if err != nil {
		panic(err)
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])
}
