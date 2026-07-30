package exec

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/agent/snapshot/snapshotfakes"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/exec/build"
)

func TestAgentValidationRequirementUsesPhysicalMappedCandidate(t *testing.T) {
	candidate, governed, err := agentValidationRequirement(atc.AgentPlan{
		Inputs:       []string{"candidate", "validation"},
		InputMapping: map[string]string{"candidate": "physical-candidate", "validation": "physical-validation"},
		SnapshotInputs: map[string]atc.SnapshotInputConfig{
			"candidate":  {Type: snapshot.TypeRef("repository-change/v1")},
			"validation": {Type: snapshot.TypeRef("validation/v1")},
		},
		SnapshotOutputs: map[string]atc.SnapshotOutputConfig{"question": {Type: snapshot.TypeRef("question/v1")}},
	})
	if err != nil || !governed || candidate != "physical-candidate" {
		t.Fatalf("agent validation candidate = (%q, %t, %v), want physical mapped candidate", candidate, governed, err)
	}
}

func TestRequireValidationRequirementUsesPhysicalMappedRepositoryNames(t *testing.T) {
	candidate := mappedValidationManifest(101, snapshot.TypeRef("repository-change/v1"), 'a')
	validation := mappedValidationManifest(102, snapshot.TypeRef("validation/v1"), 'b')
	repository := build.NewRepository()
	if err := repository.RegisterArtifacts(map[build.ArtifactName]build.ArtifactEntry{
		"physical-candidate":  {Snapshot: mappedValidationRef(candidate)},
		"physical-validation": {Snapshot: mappedValidationRef(validation)},
	}); err != nil {
		t.Fatalf("register mapped validation artifacts: %v", err)
	}
	for _, logical := range []build.ArtifactName{"candidate", "validation"} {
		if _, found := repository.ArtifactEntryFor(logical); found {
			t.Fatalf("logical repository key %q was registered", logical)
		}
	}

	authority := mappedValidationAuthority(t, "physical-candidate")
	requirement := reviewRequirement(atc.ReviewValidationRequirement{
		Candidate: "physical-candidate", Validation: "physical-validation", Authority: &authority,
	})
	metadataStore := new(snapshotfakes.FakeMetadataStore)
	metadataStore.GetAuthorizedCalls(func(_ context.Context, _ int, id snapshot.SnapshotID) (snapshot.Snapshot, bool, error) {
		switch id {
		case candidate.ID:
			return candidate, true, nil
		case validation.ID:
			return validation, true, nil
		default:
			return snapshot.Snapshot{}, false, nil
		}
	})
	contentStore := new(snapshotfakes.FakeContentStore)
	contentStore.OpenReturns(nil, errors.New("validation gate reached"))
	definitionID, version := authority.WorkflowDefinitionID, authority.WorkflowVersion
	err := requireValidationRequirement(
		context.Background(), "agent", repository,
		StepMetadata{TeamID: 1, WorkflowDefinitionID: &definitionID, WorkflowVersion: &version},
		metadataStore, contentStore, requirement, "physical-candidate",
	)
	if err == nil || !strings.Contains(err.Error(), "authoritative validation was rejected") {
		t.Fatalf("require mapped validation: %v", err)
	}
	if contentStore.OpenCallCount() != 1 {
		t.Fatalf("validation content open calls = %d, want 1 (the validation gate)", contentStore.OpenCallCount())
	}
	if metadataStore.GetAuthorizedCallCount() != 2 {
		t.Fatalf("authorized lookup calls = %d, want candidate and validation physical refs", metadataStore.GetAuthorizedCallCount())
	}
	_, _, candidateID := metadataStore.GetAuthorizedArgsForCall(0)
	_, _, validationID := metadataStore.GetAuthorizedArgsForCall(1)
	if candidateID != candidate.ID || validationID != validation.ID {
		t.Fatalf("authorized lookup IDs = (%d, %d), want mapped candidate and validation (%d, %d)", candidateID, validationID, candidate.ID, validation.ID)
	}
}

func mappedValidationManifest(id snapshot.SnapshotID, typ snapshot.TypeRef, hex byte) snapshot.Snapshot {
	return snapshot.Snapshot{
		ID: id, Type: typ, Digest: snapshot.Digest("sha256:" + strings.Repeat(string(hex), 64)),
		Representation: "application/x-tar", ContentState: snapshot.ContentStateAvailable, CreatedAt: time.Now().UTC(),
	}
}

func mappedValidationRef(manifest snapshot.Snapshot) *snapshot.SnapshotRef {
	return &snapshot.SnapshotRef{ID: manifest.ID, Type: manifest.Type, Digest: manifest.Digest}
}

func mappedValidationAuthority(t *testing.T, candidate string) atc.DevValidationAuthority {
	t.Helper()
	profile, config, image := []byte("profile"), []byte("config"), []byte("image")
	digest := func(value []byte) snapshot.Digest {
		sum := sha256.Sum256(value)
		return snapshot.Digest(fmt.Sprintf("sha256:%x", sum))
	}
	authority := atc.DevValidationAuthority{
		ProfileName: "mapped-gate", Profile: profile, ProfileDigest: digest(profile),
		ProtectedConfig: config, ProtectedConfigDigest: digest(config),
		CapabilityImage: "registry.example/validator@" + digest(image).String(), CapabilityImageDigest: digest(image),
		WorkflowDefinitionID: 7, WorkflowVersion: 3, CandidateInput: candidate,
	}
	if err := authority.Validate(); err != nil {
		t.Fatalf("mapped validation authority: %v", err)
	}
	return authority
}
